package main

// This file is the Adapter between the agent's persistence layer (package database)
// and the resource domain model. It is the only place allowed to know the shape of a
// database call, so no planner or model file has to.
//
// allocatedCPUOwners is the read half of the future ReservationStore.Snapshot(): once
// the store lands it moves inside that method and populates AllocationSnapshot.CPUOwners,
// joined by the equivalent conversion over database.AllocatedCaches() for cache ways.
// Snapshot() is taken once per reconcile of one deployment rather than once per
// component, so this conversion stops being called from the planners at that point.

// allocatedCPUOwners converts database.AllocatedCpus()'s owner strings into domain OwnerRef values.
func allocatedCPUOwners(allocated map[int]string) map[int]OwnerRef {
	owners := make(map[int]OwnerRef, len(allocated))
	for cpuIndex, owner := range allocated {
		owners[cpuIndex] = ParseOwnerRef(owner)
	}
	return owners
}
