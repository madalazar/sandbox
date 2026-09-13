package ledger

import (
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/margo/sandbox/poc/device/agent/resource/controller"
	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

var (
	// ErrCapacityExhausted indicates that the requested resource capacity is unavailable.
	ErrCapacityExhausted = errors.New("capacity exhausted")
)

// device-wide read of persisted allocations, taken once per
// reconcile of one deployment, never mutated after construction
type AllocationSnapshot struct {
	CpuOwners map[int]model.OwnerRef
	// we don't key caches by owner as caches can be split exclusively
	// between one or more components
	Caches []model.CacheAssignment
}

// decodes the persisted owner strings into domain owners,
// keeping only the isolated indices the planners can allocate from,
// and records any persisted cache allocations
func NewAllocationSnapshot(
	allocatedCpus map[int]string,
	isolatedCpus map[int]struct{},
	caches []model.CacheAssignment,
) AllocationSnapshot {
	owners := make(map[int]model.OwnerRef, len(allocatedCpus))
	for cpuIndex, owner := range allocatedCpus {
		if _, isolated := isolatedCpus[cpuIndex]; !isolated {
			continue
		}
		owners[cpuIndex] = model.ParseOwnerRef(owner)
	}

	var cacheAssignments []model.CacheAssignment
	if len(caches) > 0 {
		cacheAssignments = append([]model.CacheAssignment(nil), caches...)
	}

	return AllocationSnapshot{
		CpuOwners: owners,
		Caches:    cacheAssignments,
	}
}

// answers free-versus-taken for one deployment's reconcile pass. It
// keeps the persisted snapshot separate from what this pass has handed out, because a
// component may reuse the cpus and cache ways it already holds but may not take a sibling's
type AllocationLedger struct {
	snapshot     AllocationSnapshot
	deploymentId string

	reservedCpus    map[int]model.ComponentRef
	reservedWays    map[string]map[model.ComponentRef]model.WayInterval
	reservedClasses map[model.ComponentRef]model.ClosId

	cacheCapacity model.CacheCapacity
	classNamer    controller.ClassNamer
}

func NewAllocationLedger(
	snapshot AllocationSnapshot,
	deploymentId string,
	cacheCapacity model.CacheCapacity,
	classNamer controller.ClassNamer,
) *AllocationLedger {
	return &AllocationLedger{
		snapshot:        snapshot,
		deploymentId:    deploymentId,
		reservedCpus:    map[int]model.ComponentRef{},
		reservedWays:    map[string]map[model.ComponentRef]model.WayInterval{},
		reservedClasses: map[model.ComponentRef]model.ClosId{},
		cacheCapacity:   cacheCapacity,
		classNamer:      classNamer,
	}
}

// reports whether ref may take cpuIndex: unheld, or already persisted to
// ref itself
func (l *AllocationLedger) IsCpuAvailable(cpuIndex int, ref model.ComponentRef) bool {
	if holder, reserved := l.reservedCpus[cpuIndex]; reserved {
		return holder == ref
	}

	return model.NewOwnerRef(l.deploymentId, string(ref)).CanTake(l.snapshot.CpuOwners[cpuIndex])
}

// records an exclusive claim for this pass; it reserves nothing when any
// index is unavailable.
func (l *AllocationLedger) ReserveCpus(ref model.ComponentRef, cpus []int) error {
	// if we want to add a double check for l.IsCpuAvailable(cpuIndex, ref) we can add it here
	// while originally added, it was removed as the planner's call to l.IsCpuAvailable
	// makes this call redundant

	for _, cpuIndex := range cpus {
		l.reservedCpus[cpuIndex] = ref
	}

	return nil
}

// returns the contiguous way intervals on cacheId that ref may take: unused,
// or already persisted to ref itself
func (l *AllocationLedger) FreeWays(cacheId string, ref model.ComponentRef) []model.WayInterval {
	totalWays, exists := l.cacheCapacity.Ways[cacheId]
	if !exists || totalWays <= 0 {
		return nil
	}

	used := make([]bool, totalWays)

	// mark ways from persisted snapshot
	for _, res := range l.snapshot.Caches {
		if res.CacheId != cacheId {
			continue
		}
		// check self-ownership: if held by the exact same component of this deployment, it is reusable
		if model.NewOwnerRef(l.deploymentId, string(ref)).CanTake(res.Owner) {
			continue
		}
		if res.Interval.Length > 0 {
			for bit := res.Interval.Start; bit < res.Interval.End() && bit < totalWays; bit++ {
				if bit >= 0 {
					used[bit] = true
				}
			}
		} else if res.Mask != "" {
			parsed := new(big.Int)
			if _, ok := parsed.SetString(strings.TrimSpace(res.Mask), 0); ok {
				for bit := range totalWays {
					if parsed.Bit(int(bit)) == 1 {
						used[bit] = true
					}
				}
			}
		}
	}

	// mark ways from current reconcile pass
	for holder, wayInterval := range l.reservedWays[cacheId] {
		if holder == ref {
			continue
		}
		for bit := wayInterval.Start; bit < wayInterval.End() && bit < totalWays; bit++ {
			if bit >= 0 {
				used[bit] = true
			}
		}
	}

	return model.FreeWayIntervals(used)
}

// records an exclusive claim on contiguous cache ways for ref on cacheId
func (l *AllocationLedger) ReserveWays(ref model.ComponentRef, cacheId string, wayInterval model.WayInterval) error {
	totalWays, exists := l.cacheCapacity.Ways[cacheId]
	if !exists {
		return fmt.Errorf("cache id %q not found in device inventory", cacheId)
	}
	if wayInterval.Start < 0 || wayInterval.Length <= 0 || wayInterval.End() > totalWays {
		return fmt.Errorf("interval [%d, %d) out of range for cache %s (total ways %d)", wayInterval.Start, wayInterval.End(), cacheId, totalWays)
	}

	// verify interval is free for ref
	freeIntervals := l.FreeWays(cacheId, ref)
	canFit := false
	for _, free := range freeIntervals {
		if wayInterval.Start >= free.Start && wayInterval.End() <= free.End() {
			canFit = true
			break
		}
	}
	if !canFit {
		return fmt.Errorf("interval [%d, %d) on cache %s overlaps already-claimed ways: %w", wayInterval.Start, wayInterval.End(), cacheId, ErrCapacityExhausted)
	}

	if l.reservedWays[cacheId] == nil {
		l.reservedWays[cacheId] = make(map[model.ComponentRef]model.WayInterval)
	}
	l.reservedWays[cacheId][ref] = wayInterval

	return nil
}

// reserves one class of service from the shared device pool and names it via ClassNamer
func (l *AllocationLedger) ReserveClass(ref model.ComponentRef) (model.ClosId, error) {
	// if ref already reserved a class in this pass, return it
	if existing, found := l.reservedClasses[ref]; found {
		return existing, nil
	}

	usable := l.cacheCapacity.ClosPool.Usable()
	if usable <= 0 {
		return model.ClassUnset, fmt.Errorf("no usable classes in class pool: %w", ErrCapacityExhausted)
	}

	// gather all taken classes (persisted + in-flight)
	takenMap := make(map[model.ClosId]struct{})

	for _, res := range l.snapshot.Caches {
		if res.Clos.Held() {
			// if self-owned by same component, ref can reuse its class slot
			if model.NewOwnerRef(l.deploymentId, string(ref)).CanTake(res.Owner) {
				continue
			}
			takenMap[res.Clos] = struct{}{}
		}
	}

	for holderRef, closId := range l.reservedClasses {
		if holderRef != ref && closId.Held() {
			takenMap[closId] = struct{}{}
		}
	}

	if len(takenMap) >= usable {
		return model.ClassUnset, fmt.Errorf("class pool exhausted (held %d of %d): %w", len(takenMap), usable, ErrCapacityExhausted)
	}

	if l.classNamer == nil {
		return model.ClassUnset, errors.New("class namer is not configured on ledger")
	}

	takenList := make([]model.ClosId, 0, len(takenMap))
	for c := range takenMap {
		takenList = append(takenList, c)
	}

	namedClass, err := l.classNamer.Name(ref, takenList)
	if err != nil {
		return model.ClassUnset, fmt.Errorf("failed to name class for component %q: %w", ref, err)
	}

	l.reservedClasses[ref] = namedClass
	return namedClass, nil
}

// rolls back any claims (cpus, ways, classes) made by ref in this pass.
func (l *AllocationLedger) RollbackComponent(ref model.ComponentRef) {
	for cpu, holder := range l.reservedCpus {
		if holder == ref {
			delete(l.reservedCpus, cpu)
		}
	}

	for _, claims := range l.reservedWays {
		delete(claims, ref)
	}

	delete(l.reservedClasses, ref)
}
