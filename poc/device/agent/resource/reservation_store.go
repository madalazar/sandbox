package resource

import (
	"errors"

	"github.com/margo/sandbox/poc/device/agent/database"
	"github.com/margo/sandbox/poc/device/agent/resource/ledger"
	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

var errNotImplemented = errors.New("not implemented")

// one component's recorded (for now only cpu) allocation
type Reservation struct {
	Owner model.OwnerRef
	Cpus  []int
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
	return ledger.NewAllocationSnapshot(s.db.AllocatedCpus(), s.isolatedCpus), nil
}

func (s *DatabaseReservationStore) LoadReservation(owner model.OwnerRef) (Reservation, bool, error) {
	allocations, err := s.db.GetAllocations(owner.Deployment)
	if err != nil {
		return Reservation{}, false, err
	}

	key := string(owner.Component)
	cpus, hasCpus := allocations.Cpus[key]
	if !hasCpus {
		return Reservation{}, false, nil
	}

	return Reservation{
		Owner: owner,
		Cpus:  append([]int(nil), cpus...),
	}, true, nil
}

func (s *DatabaseReservationStore) SaveReservation(deploymentId string, reservation Reservation) error {
	existing, err := s.db.GetAllocations(deploymentId)
	if err != nil {
		return err
	}

	merged := make(map[string][]int, len(existing.Cpus)+len(reservation.Cpus))
	for k, v := range existing.Cpus {
		merged[k] = append([]int(nil), v...)
	}
	merged[string(reservation.Owner.Component)] = append([]int(nil), reservation.Cpus...)

	return s.db.SetAllocations(deploymentId, database.Allocations{Cpus: merged})
}

func (s *DatabaseReservationStore) ClearComponent(owner model.OwnerRef) error {
	return s.db.ClearComponentAllocations(owner.Deployment, string(owner.Component))
}
