package main

import (
	"maps"
	"sort"
	"strconv"
	"strings"

	"github.com/margo/sandbox/poc/device/agent/database"
)

// Reservation is one component's recorded CPU and cache allocation.
type Reservation struct {
	Owner  OwnerRef
	CPUs   []int
	Caches []CacheReservation
}

// CacheReservation is the domain representation of one persisted cache holding.
// ClassID remains an int until the planned ClassID string storage migration lands.
type CacheReservation struct {
	Level   string
	CacheID string
	SizeKiB int64
	Mask    string
	ClassID int
}

func (r Reservation) HasCache() bool {
	return len(r.Caches) > 0
}

func (r Reservation) CPUSet() string {
	if len(r.CPUs) == 0 {
		return ""
	}
	cpus := append([]int(nil), r.CPUs...)
	sort.Ints(cpus)
	parts := make([]string, 0, len(cpus))
	for _, cpu := range cpus {
		parts = append(parts, strconv.Itoa(cpu))
	}
	return strings.Join(parts, ",")
}

// ReservationStore is the persistence boundary used by ResourceCoordinator to
// reconstruct and release a component's recorded CPU and cache reservation.
type ReservationStore interface {
	LoadReservation(owner OwnerRef) (Reservation, bool, error)
	ClearComponent(owner OwnerRef) error
}

// databaseReservationStore adapts database.DatabaseIfc to ReservationStore. It
// maps deployment allocation records into domain Reservation values and clears a
// component's CPU and cache holdings through one atomic SetAllocations write while
// preserving successful sibling components.
type databaseReservationStore struct {
	db database.DatabaseIfc
}

func newDatabaseReservationStore(db database.DatabaseIfc) ReservationStore {
	return &databaseReservationStore{db: db}
}

func (s *databaseReservationStore) LoadReservation(owner OwnerRef) (Reservation, bool, error) {
	allocations, err := s.db.GetAllocations(owner.Deployment)
	if err != nil {
		return Reservation{}, false, err
	}

	key := string(owner.Ref)
	cpus, hasCPUs := allocations.CPUs[key]
	cacheAssignments, hasCaches := allocations.Caches[key]
	if !hasCPUs && !hasCaches {
		return Reservation{}, false, nil
	}

	reservation := Reservation{
		Owner:  owner,
		CPUs:   append([]int(nil), cpus...),
		Caches: make([]CacheReservation, 0, len(cacheAssignments)),
	}
	for _, assignment := range cacheAssignments {
		reservation.Caches = append(reservation.Caches, CacheReservation{
			Level:   assignment.Level,
			CacheID: assignment.CacheID,
			SizeKiB: assignment.SizeKB,
			Mask:    assignment.Mask,
			ClassID: assignment.ClassID,
		})
	}

	return reservation, true, nil
}

func (s *databaseReservationStore) ClearComponent(owner OwnerRef) error {
	return s.db.ClearComponentAllocations(owner.Deployment, string(owner.Ref))
}

// toCacheAssignments maps domain cache reservations back to the persisted form the
// PQoS and RDT helpers still consume.
func toCacheAssignments(componentName string, reservations []CacheReservation) []database.CacheAssignment {
	assignments := make([]database.CacheAssignment, 0, len(reservations))
	for _, reservation := range reservations {
		assignments = append(assignments, database.CacheAssignment{
			ComponentName: componentName,
			Level:         reservation.Level,
			CacheID:       reservation.CacheID,
			SizeKB:        reservation.SizeKiB,
			Mask:          reservation.Mask,
			ClassID:       reservation.ClassID,
		})
	}
	return assignments
}

// allocatedCPUOwners converts database.AllocatedCpus()'s owner strings into domain OwnerRef values.
func allocatedCPUOwners(allocated map[int]string) map[int]OwnerRef {
	owners := make(map[int]OwnerRef, len(allocated))
	for cpuIndex, owner := range allocated {
		owners[cpuIndex] = ParseOwnerRef(owner)
	}
	return owners
}

// mergeExistingAssignments overlays this reconcile pass's assignments onto the persisted
// owners, so a component cannot be planned onto a CPU a sibling took earlier in the pass.
// Indices outside isolatedSet are ignored. The result is a new map; taken is not modified.
func mergeExistingAssignments(
	taken map[int]OwnerRef,
	deploymentID string,
	existing map[string][]int,
	isolatedSet map[int]struct{},
) map[int]OwnerRef {
	merged := make(map[int]OwnerRef, len(taken)+len(existing))
	maps.Copy(merged, taken)

	for requirement, cpuIndices := range existing {
		holder := NewOwnerRef(deploymentID, requirement)

		for _, cpuIndex := range cpuIndices {
			if _, isolated := isolatedSet[cpuIndex]; !isolated {
				continue
			}
			merged[cpuIndex] = holder
		}
	}

	return merged
}
