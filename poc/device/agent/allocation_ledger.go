package main

import "fmt"

// AllocationSnapshot is a device-wide read of persisted allocations, taken once per
// reconcile of one deployment. It is never mutated after construction.
type AllocationSnapshot struct {
	CPUOwners map[int]OwnerRef
}

// NewAllocationSnapshot decodes the persisted owner strings into domain owners,
// keeping only the isolated indices the planners can allocate from.
func NewAllocationSnapshot(
	allocatedCPUs map[int]string,
	isolatedCPUs map[int]struct{},
) AllocationSnapshot {
	owners := make(map[int]OwnerRef, len(allocatedCPUs))
	for cpuIndex, owner := range allocatedCPUs {
		if _, isolated := isolatedCPUs[cpuIndex]; !isolated {
			continue
		}
		owners[cpuIndex] = ParseOwnerRef(owner)
	}
	return AllocationSnapshot{CPUOwners: owners}
}

// AllocationLedger answers free-versus-taken for one deployment's reconcile pass. It
// keeps the persisted snapshot separate from what this pass has handed out, because a
// component may reuse the CPUs it already holds but may not take a sibling's.
type AllocationLedger struct {
	snapshot     AllocationSnapshot
	deploymentId string
	reservedCPUs map[int]ComponentRef
}

func NewAllocationLedger(snapshot AllocationSnapshot, deploymentId string) *AllocationLedger {
	return &AllocationLedger{
		snapshot:     snapshot,
		deploymentId: deploymentId,
		reservedCPUs: map[int]ComponentRef{},
	}
}

// IsCpuAvailable reports whether ref may take cpuIndex: unheld, or already persisted to
// ref itself. A sibling component's claim blocks, whether persisted or made earlier in
// this pass.
func (l *AllocationLedger) IsCpuAvailable(cpuIndex int, ref ComponentRef) bool {
	if holder, reserved := l.reservedCPUs[cpuIndex]; reserved {
		return holder == ref
	}
	return NewOwnerRef(l.deploymentId, string(ref)).CanTake(l.snapshot.CPUOwners[cpuIndex])
}

// ReserveCPUs records an exclusive claim for this pass. It reserves nothing when any
// index is unavailable.
func (l *AllocationLedger) ReserveCPUs(ref ComponentRef, cpus []int) error {
	for _, cpuIndex := range cpus {
		if !l.IsCpuAvailable(cpuIndex, ref) {
			return fmt.Errorf("cpu %d is not available to component %q", cpuIndex, ref)
		}
	}

	for _, cpuIndex := range cpus {
		l.reservedCPUs[cpuIndex] = ref
	}

	return nil
}
