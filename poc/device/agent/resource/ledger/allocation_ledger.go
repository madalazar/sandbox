package ledger

import (
	"errors"
	"fmt"

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

// answers free-versus-taken for one component's planning pass against the
// device-wide persisted AllocationSnapshot. Because each component's plan is
// committed to storage before the next component is planned, the snapshot is
// the single source of truth for both other deployments and sibling components.
type AllocationLedger struct {
	snapshot      AllocationSnapshot
	deploymentId  string
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
		snapshot:      snapshot,
		deploymentId:  deploymentId,
		cacheCapacity: cacheCapacity,
		classNamer:    classNamer,
	}
}

// reports whether ref may take cpuIndex: unheld, or already persisted to
// ref itself
func (l *AllocationLedger) IsCpuAvailable(cpuIndex int, ref model.ComponentRef) bool {
	return model.NewOwnerRef(l.deploymentId, string(ref)).CanTake(l.snapshot.CpuOwners[cpuIndex])
}

// returns the contiguous way intervals on cacheId that ref may take: unused,
// or already persisted to ref itself
func (l *AllocationLedger) FreeWays(cacheId string, ref model.ComponentRef) []model.WayInterval {
	totalWays, exists := l.cacheCapacity.Ways[cacheId]

	if !exists || totalWays <= 0 {
		return nil
	}

	used := make([]bool, totalWays)
	requester := model.NewOwnerRef(l.deploymentId, string(ref))

	// mark ways from persisted snapshot
	for _, res := range l.snapshot.Caches {
		if requester.CanTake(res.Owner) {
			continue
		}

		var interval model.WayInterval
		if res.CacheId == cacheId {
			interval = res.Interval
		}

		if mask := res.Masks[cacheId]; mask != "" {
			if parsedInterval, err := model.ParseContiguousWayMask(mask, totalWays); err == nil {
				interval = parsedInterval
			}
		}

		if interval.Start >= 0 && interval.Length > 0 {
			for bit := interval.Start; bit < interval.End() && bit < totalWays; bit++ {
				used[bit] = true
			}
		}
	}

	return model.FreeWayIntervals(used)
}

// reserves one class of service from the shared device pool and names it via ClassNamer
func (l *AllocationLedger) ReserveClass(ref model.ComponentRef) (model.ClosId, error) {
	usable := l.cacheCapacity.ClosPool.Usable()
	if usable <= 0 {
		return model.ClassUnset, fmt.Errorf("no usable classes in class pool: %w", ErrCapacityExhausted)
	}

	// gather all taken classes from persisted snapshot
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

	owner := model.NewOwnerRef(l.deploymentId, string(ref))
	namedClass, err := l.classNamer.Name(owner, takenList)
	if err != nil {
		return model.ClassUnset, fmt.Errorf("failed to name class for component %q: %w", ref, err)
	}

	return namedClass, nil
}
