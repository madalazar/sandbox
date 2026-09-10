package resource

import (
	"maps"

	"github.com/margo/sandbox/poc/device/agent/database"
	"github.com/margo/sandbox/poc/device/agent/resource/ledger"
	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

// one component's recorded cpu and cache allocation
type Reservation struct {
	Owner             model.OwnerRef
	Cpus              []int
	L3CacheAssignment *model.CacheAssignment
	// TODO: this might be duplicated, once we wire things up
	// we might have to clean it
	Clos model.ClosId
}

func (r Reservation) HasL3Cache() bool {
	return r.L3CacheAssignment != nil
}

func (r Reservation) CpuSet() string {
	return model.FormatCpuSet(r.Cpus)
}

// read device-wide allocations, and record, reconstruct and release a component's
// recorded reservation
type ReservationStore interface {
	// every deployment's holdings, not just one; taken once per reconcile, before the
	// component loop, so a ledger built from it also sees siblings planned in that pass
	LoadSnapshot() (ledger.AllocationSnapshot, error)
	LoadReservation(owner model.OwnerRef) (Reservation, bool, error)
	// replaces the deployment's holdings rather than merging, in one write
	SaveReservation(deploymentId string, reservation Reservation) error
	ClearComponent(owner model.OwnerRef) error
}

var _ ReservationStore = (*DatabaseReservationStore)(nil)

// adapts database.DatabaseIfc to ReservationStore, mapping deployment allocation
// records onto domain values such as Reservation
type DatabaseReservationStore struct {
	db           database.DatabaseIfc
	isolatedCpus map[int]struct{}
}

// isolatedCpus bounds what Snapshot reports: only indices a planner can allocate from
func NewDatabaseReservationStore(db database.DatabaseIfc, isolatedCpus map[int]struct{}) ReservationStore {
	return &DatabaseReservationStore{db: db, isolatedCpus: isolatedCpus}
}

func (s *DatabaseReservationStore) LoadSnapshot() (ledger.AllocationSnapshot, error) {
	allocatedCpus := s.db.AllocatedCpus()
	allocatedCaches := s.db.AllocatedCaches()

	caches := make([]model.CacheAssignment, 0, len(allocatedCaches))
	for _, alloc := range allocatedCaches {
		caches = append(caches, model.CacheAssignment{
			Owner:   model.ParseOwnerRef(alloc.Owner),
			Level:   alloc.Level,
			CacheId: alloc.CacheID,
			SizeKiB: alloc.SizeKB,
			Mask:    alloc.Mask,
			Clos:    model.ClosId(alloc.Clos),
		})
	}
	// TODO: use the proper c-tor at the end
	return ledger.NewAllocationSnapshotWithCaches(allocatedCpus, s.isolatedCpus, caches), nil
}

func (s *DatabaseReservationStore) LoadReservation(owner model.OwnerRef) (Reservation, bool, error) {
	allocations, err := s.db.GetAllocations(owner.Deployment)
	if err != nil {
		return Reservation{}, false, err
	}

	key := string(owner.Component)
	cpus, hasCpus := allocations.Cpus[key]
	cacheAlloc, hasCache := allocations.Caches[key]
	if !hasCpus && !hasCache {
		return Reservation{}, false, nil
	}

	reservation := Reservation{
		Owner: owner,
		Cpus:  append([]int(nil), cpus...),
	}

	if hasCache {
		res := model.CacheAssignment{
			Owner:   owner,
			Level:   cacheAlloc.Level,
			CacheId: cacheAlloc.CacheID,
			SizeKiB: cacheAlloc.SizeKB,
			Mask:    cacheAlloc.Mask,
			Clos:    model.ClosId(cacheAlloc.Clos),
		}
		reservation.L3CacheAssignment = &res
		reservation.Clos = res.Clos
	}

	return reservation, true, nil
}

func (s *DatabaseReservationStore) SaveReservation(deploymentId string, reservation Reservation) error {
	existing, err := s.db.GetAllocations(deploymentId)
	if err != nil {
		return err
	}

	mergedCpus := make(map[string][]int, len(existing.Cpus)+1)
	for k, v := range existing.Cpus {
		mergedCpus[k] = append([]int(nil), v...)
	}
	if len(reservation.Cpus) > 0 {
		mergedCpus[string(reservation.Owner.Component)] = append([]int(nil), reservation.Cpus...)
	} else {
		delete(mergedCpus, string(reservation.Owner.Component))
	}

	mergedCaches := make(map[string]database.CacheAllocation, len(existing.Caches)+1)
	maps.Copy(mergedCaches, existing.Caches)

	if reservation.HasL3Cache() {
		c := reservation.L3CacheAssignment
		classStr := c.Clos.String()
		if classStr == "" && reservation.Clos.Held() {
			classStr = reservation.Clos.String()
		}
		mergedCaches[string(reservation.Owner.Component)] = database.CacheAllocation{
			ComponentName: string(reservation.Owner.Component),
			Level:         c.Level,
			CacheID:       c.CacheId,
			SizeKB:        c.SizeKiB,
			Mask:          c.Mask,
			Clos:          classStr,
		}
	} else {
		delete(mergedCaches, string(reservation.Owner.Component))
	}

	return s.db.SetAllocations(deploymentId, database.Allocations{
		Cpus:   mergedCpus,
		Caches: mergedCaches,
	})
}

func (s *DatabaseReservationStore) ClearComponent(owner model.OwnerRef) error {
	return s.db.ClearComponentAllocations(owner.Deployment, string(owner.Component))
}
