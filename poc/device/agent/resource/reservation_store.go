package resource

import (
	"github.com/margo/sandbox/poc/device/agent/database"
	"github.com/margo/sandbox/poc/device/agent/resource/ledger"
	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

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
	// every deployment's holdings, not just one. Taken once per reconcile, before the
	// component loop, so a ledger built from it also sees siblings planned in that pass
	// TODO: rename to GetSnapthot, LoadSnapshot ... something...
	Snapshot() (ledger.AllocationSnapshot, error)
	LoadReservation(owner model.OwnerRef) (Reservation, bool, error)
	// replaces the deployment's holdings rather than merging, in one write
	SaveAllocations(deploymentId string, cpus map[string][]int) error
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

func (s *DatabaseReservationStore) Snapshot() (ledger.AllocationSnapshot, error) {
	return ledger.AllocationSnapshot{}, errNotImplemented
}

func (s *DatabaseReservationStore) LoadReservation(owner model.OwnerRef) (Reservation, bool, error) {
	return Reservation{}, false, errNotImplemented
}

func (s *DatabaseReservationStore) SaveAllocations(deploymentId string, cpus map[string][]int) error {
	return errNotImplemented
}

func (s *DatabaseReservationStore) ClearComponent(owner model.OwnerRef) error {
	return errNotImplemented
}
