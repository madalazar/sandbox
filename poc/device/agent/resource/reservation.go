package resource

import (
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
	return FormatCpuSet(r.CPUs)
}

// ReservationStore is the persistence boundary used by ResourceCoordinator to
// reconstruct and release a component's recorded CPU and cache reservation.
type ReservationStore interface {
	LoadReservation(owner OwnerRef) (Reservation, bool, error)
	ClearComponent(owner OwnerRef) error
}

// DatabaseReservationStore adapts database.DatabaseIfc to ReservationStore. It
// maps deployment allocation records into domain Reservation values and clears a
// component's CPU and cache holdings through one atomic SetAllocations write while
// preserving successful sibling components.
type DatabaseReservationStore struct {
	db database.DatabaseIfc
}

func NewDatabaseReservationStore(db database.DatabaseIfc) ReservationStore {
	return &DatabaseReservationStore{db: db}
}

func (s *DatabaseReservationStore) LoadReservation(owner OwnerRef) (Reservation, bool, error) {
	allocations, err := s.db.GetAllocations(owner.Deployment)
	if err != nil {
		return Reservation{}, false, err
	}

	key := string(owner.Component)
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

func (s *DatabaseReservationStore) ClearComponent(owner OwnerRef) error {
	return s.db.ClearComponentAllocations(owner.Deployment, string(owner.Component))
}

// ToCacheAssignments maps domain cache reservations back to the persisted form the
// PQoS and RDT helpers still consume.
func ToCacheAssignments(componentName string, reservations []CacheReservation) []database.CacheAssignment {
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

// AllocatedCPUOwners converts database.AllocatedCpus()'s owner strings into domain OwnerRef values.
func AllocatedCPUOwners(allocated map[int]string) map[int]OwnerRef {
	owners := make(map[int]OwnerRef, len(allocated))
	for cpuIndex, owner := range allocated {
		owners[cpuIndex] = ParseOwnerRef(owner)
	}
	return owners
}
