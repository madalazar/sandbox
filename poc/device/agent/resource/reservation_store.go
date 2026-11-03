package resource

import (
	"github.com/margo/sandbox/poc/device/agent/database"
	"github.com/margo/sandbox/poc/device/agent/resource/ledger"
	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

// read device-wide allocations, and record, reconstruct and release a component's
// recorded reservation
type ReservationStore interface {
	// every deployment's holdings, not just one; taken once per reconcile, before the
	// component loop, so a ledger built from it also sees siblings planned in that pass
	LoadSnapshot() (ledger.AllocationSnapshot, error)
	LoadReservation(owner model.OwnerRef) (model.Reservation, bool, error)
	// replaces the deployment's holdings rather than merging, in one write
	SaveReservation(deploymentId string, reservation model.Reservation) error
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

func toModelCacheAssignment(owner model.OwnerRef, alloc database.CacheAllocation) model.CacheAssignment {
	return model.CacheAssignment{
		Owner:   owner,
		Level:   alloc.Level,
		CacheId: alloc.CacheId,
		SizeKiB: alloc.SizeKB,
		Mask:    alloc.Mask,
		Clos:    model.ClosId(alloc.Clos),
	}
}

func toDatabaseCacheAllocation(componentName string, c *model.CacheAssignment) database.CacheAllocation {
	return database.CacheAllocation{
		ComponentName: componentName,
		Level:         c.Level,
		CacheId:       c.CacheId,
		SizeKB:        c.SizeKiB,
		Mask:          c.Mask,
		Clos:          c.Clos.String(),
	}
}

func (s *DatabaseReservationStore) LoadSnapshot() (ledger.AllocationSnapshot, error) {
	allocatedCpus, allocatedCaches := s.db.AllocatedResources()

	caches := make([]model.CacheAssignment, 0, len(allocatedCaches))
	for _, alloc := range allocatedCaches {
		caches = append(caches, toModelCacheAssignment(model.ParseOwnerRef(alloc.Owner), alloc))
	}
	return ledger.NewAllocationSnapshot(allocatedCpus, s.isolatedCpus, caches), nil
}

func (s *DatabaseReservationStore) LoadReservation(owner model.OwnerRef) (model.Reservation, bool, error) {
	allocations, err := s.db.GetAllocations(owner.Deployment)
	if err != nil {
		return model.Reservation{}, false, err
	}

	key := string(owner.Component)
	cpus, hasCpus := allocations.Cpus[key]
	cacheAlloc, hasCache := allocations.Caches[key]
	if !hasCpus && !hasCache {
		return model.Reservation{}, false, nil
	}

	reservation := model.Reservation{
		Owner: owner,
		Cpus:  append([]int(nil), cpus...),
	}

	if hasCache {
		res := toModelCacheAssignment(owner, cacheAlloc)
		reservation.L3CacheAssignment = &res
	}

	return reservation, true, nil
}

func (s *DatabaseReservationStore) SaveReservation(deploymentId string, reservation model.Reservation) error {
	compKey := string(reservation.Owner.Component)
	var cacheAlloc *database.CacheAllocation
	if reservation.HasL3Cache() {
		converted := toDatabaseCacheAllocation(compKey, reservation.L3CacheAssignment)
		cacheAlloc = &converted
	}

	return s.db.SetComponentAllocations(deploymentId, compKey, reservation.Cpus, cacheAlloc)
}

func (s *DatabaseReservationStore) ClearComponent(owner model.OwnerRef) error {
	return s.db.ClearComponentAllocations(owner.Deployment, string(owner.Component))
}
