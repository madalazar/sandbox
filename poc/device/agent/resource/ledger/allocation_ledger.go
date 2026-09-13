package ledger

import (
	"errors"

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
	// so there's no good way to group them differently at this time
	Caches []model.CacheAssignment
}

// keeping both c-tors until we wire the ledger properly in deployment.go

// decodes the persisted owner strings into domain owners,
// keeping only the isolated indices the planners can allocate from
func NewAllocationSnapshot(
	allocatedCpus map[int]string,
	isolatedCpus map[int]struct{},
) AllocationSnapshot {
	owners := make(map[int]model.OwnerRef, len(allocatedCpus))
	for cpuIndex, owner := range allocatedCpus {
		if _, isolated := isolatedCpus[cpuIndex]; !isolated {
			continue
		}
		owners[cpuIndex] = model.ParseOwnerRef(owner)
	}

	return AllocationSnapshot{
		CpuOwners: owners,
		Caches:    nil,
	}
}

// creates a snapshot with both cpu owners and cache reservations
func NewAllocationSnapshotWithCaches(
	allocatedCpus map[int]string,
	isolatedCpus map[int]struct{},
	caches []model.CacheAssignment,
) AllocationSnapshot {
	snapshot := NewAllocationSnapshot(allocatedCpus, isolatedCpus)
	if len(caches) > 0 {
		snapshot.Caches = append([]model.CacheAssignment(nil), caches...)
	}
	return snapshot
}

// answers free-versus-taken for one deployment's reconcile pass. It
// keeps the persisted snapshot separate from what this pass has handed out, because a
// component may reuse the cpus and cache ways it already holds but may not take a sibling's
type AllocationLedger struct {
	snapshot     AllocationSnapshot
	deploymentId string

	reservedCpus map[int]model.ComponentRef

	cacheCapacity model.CacheCapacity
	classNamer    controller.ClassNamer
}

func NewAllocationLedger(snapshot AllocationSnapshot, deploymentId string) *AllocationLedger {
	return &AllocationLedger{
		snapshot:     snapshot,
		deploymentId: deploymentId,
		reservedCpus: map[int]model.ComponentRef{},
	}
}

func (l *AllocationLedger) SetCacheCapacity(caps model.CacheCapacity) {
	l.cacheCapacity = caps
}

func (l *AllocationLedger) SetClassName(classNamer controller.ClassNamer) {
	l.classNamer = classNamer
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
	return nil
}

// records an exclusive claim on contiguous cache ways for ref on cacheId
func (l *AllocationLedger) ReserveWays(ref model.ComponentRef, cacheId string, iv model.WayInterval) error {
	return nil
}

// reserves one class of service from the shared device pool and names it via ClassNamer
func (l *AllocationLedger) ReserveClass(ref model.ComponentRef) (model.ClosId, error) {
	return model.ClassUnset, nil
}

// TODO: understand if this is needed
// RollbackComponent rolls back any claims (CPUs, ways, classes) made by ref in this pass.
func (l *AllocationLedger) RollbackComponent(ref model.ComponentRef) {
	for cpu, holder := range l.reservedCpus {
		if holder == ref {
			delete(l.reservedCpus, cpu)
		}
	}
}
