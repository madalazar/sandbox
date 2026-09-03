package ledger

import (
	"fmt"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

// device-wide read of persisted allocations, taken once per
// reconcile of one deployment, never mutated after construction
type AllocationSnapshot struct {
	CpuOwners map[int]model.OwnerRef
}

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
	return AllocationSnapshot{CpuOwners: owners}
}

// answers free-versus-taken for one deployment's reconcile pass. It
// keeps the persisted snapshot separate from what this pass has handed out, because a
// component may reuse the CPUs it already holds but may not take a sibling's
type AllocationLedger struct {
	snapshot     AllocationSnapshot
	deploymentId string
	reservedCpus map[int]model.ComponentRef
}

func NewAllocationLedger(snapshot AllocationSnapshot, deploymentId string) *AllocationLedger {
	return &AllocationLedger{
		snapshot:     snapshot,
		deploymentId: deploymentId,
		reservedCpus: map[int]model.ComponentRef{},
	}
}

// reports whether ref may take cpuIndex: unheld, or already persisted to
// ref itself. A sibling component's claim blocks, whether persisted or made earlier in
// this pass
func (l *AllocationLedger) IsCpuAvailable(cpuIndex int, ref model.ComponentRef) bool {
	if holder, reserved := l.reservedCpus[cpuIndex]; reserved {
		return holder == ref
	}
	return model.NewOwnerRef(l.deploymentId, string(ref)).CanTake(l.snapshot.CpuOwners[cpuIndex])
}

// records an exclusive claim for this pass. It reserves nothing when any
// index is unavailable.
func (l *AllocationLedger) ReserveCpus(ref model.ComponentRef, cpus []int) error {
	for _, cpuIndex := range cpus {
		if !l.IsCpuAvailable(cpuIndex, ref) {
			return fmt.Errorf("cpu %d is not available to component %q", cpuIndex, ref)
		}
	}

	for _, cpuIndex := range cpus {
		l.reservedCpus[cpuIndex] = ref
	}

	return nil
}
