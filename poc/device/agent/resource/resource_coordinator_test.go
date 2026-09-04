package resource

import (
	"context"
	"errors"
	"testing"

	"github.com/margo/sandbox/poc/device/agent/resource/ledger"
	"github.com/margo/sandbox/poc/device/agent/resource/model"
	"github.com/margo/sandbox/poc/device/agent/resource/planner"
	"github.com/margo/sandbox/standard/generatedCode/wfm/sbi"
	"go.uber.org/zap"
)

type fakeReservationStore struct {
	reservation Reservation
	found       bool
	loadErr     error
	clearErr    error
	cleared     []model.OwnerRef
	savedDep    string
	savedCpus   map[string][]int
	snapshot    ledger.AllocationSnapshot
}

func (s *fakeReservationStore) Snapshot() (ledger.AllocationSnapshot, error) {
	return s.snapshot, nil
}

func (s *fakeReservationStore) LoadReservation(owner model.OwnerRef) (Reservation, bool, error) {
	return s.reservation, s.found, s.loadErr
}

func (s *fakeReservationStore) SaveAllocations(deploymentId string, cpus map[string][]int) error {
	s.savedDep = deploymentId
	s.savedCpus = cpus
	return nil
}

func (s *fakeReservationStore) ClearComponent(owner model.OwnerRef) error {
	s.cleared = append(s.cleared, owner)
	return s.clearErr
}

type fakeCpuPlanner struct {
	plan model.CpuPlan
	err  error
}

func (f *fakeCpuPlanner) PlanCpu(req planner.CpuPlanningRequest) (model.CpuPlan, error) {
	return f.plan, f.err
}

func TestResourceCoordinatorNewLedger(t *testing.T) {
	store := &fakeReservationStore{
		snapshot: ledger.AllocationSnapshot{
			CpuOwners: map[int]model.OwnerRef{
				1: model.NewOwnerRef("dep1", "comp1"),
			},
		},
	}
	c := NewResourceCoordinator(store, nil)
	l, err := c.NewLedger("dep1")
	if err != nil {
		t.Fatalf("NewLedger() error = %v", err)
	}
	if l == nil {
		t.Fatal("NewLedger() returned nil")
	}

	// newLedger delegates to NewLedger
	l2, err := c.newLedger("dep1")
	if err != nil || l2 == nil {
		t.Fatalf("newLedger() error = %v, l = %v", err, l2)
	}
}

func TestResourceCoordinatorPlan(t *testing.T) {
	owner := model.NewOwnerRef("dep-1", "comp-1")
	cores := float32(1)
	class := sbi.CpuClassPerformance
	cpuType := sbi.CpuTypeIsolated
	compName := "comp-1"
	req := ResourceRequest{
		Owner: owner,
		Requirements: &sbi.RequiredResources{
			Cpu: &[]sbi.Cpu{
				{
					Name:  &compName,
					Cores: &cores,
					Class: &class,
					Type:  &cpuType,
				},
			},
		},
	}

	fakePlanner := &fakeCpuPlanner{
		plan: model.CpuPlan{
			Component: "comp-1",
			Cpus:      []int{2},
		},
	}
	c := NewResourceCoordinator(nil, fakePlanner)
	plan, err := c.Plan(nil, req)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if plan.Owner != owner {
		t.Fatalf("plan.Owner = %v, want %v", plan.Owner, owner)
	}
	if len(plan.Cpu.Cpus) != 1 || plan.Cpu.Cpus[0] != 2 {
		t.Fatalf("plan.Cpu.Cpus = %v, want [2]", plan.Cpu.Cpus)
	}
}

func TestResourceCoordinatorCommit(t *testing.T) {
	store := &fakeReservationStore{}
	c := NewResourceCoordinator(store, nil)

	owner := model.NewOwnerRef("dep-1", "comp-1")
	plan := ResourcePlan{
		Owner: owner,
		Cpu: model.CpuPlan{
			Component: "comp-1",
			Cpus:      []int{3, 4},
		},
	}

	if err := c.Commit(context.Background(), plan); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if store.savedDep != "dep-1" {
		t.Fatalf("savedDep = %q, want dep-1", store.savedDep)
	}
	if len(store.savedCpus["comp-1"]) != 2 {
		t.Fatalf("savedCpus = %v, want [3 4]", store.savedCpus)
	}

	// Commit with no cpus is a no-op
	store.savedDep = ""
	emptyPlan := ResourcePlan{Owner: owner}
	if err := c.Commit(context.Background(), emptyPlan); err != nil {
		t.Fatalf("Commit() empty plan error = %v", err)
	}
	if store.savedDep != "" {
		t.Fatalf("Commit() empty plan should not write to store")
	}
}

func TestResourceCoordinatorActivateIsANoOp(t *testing.T) {
	if err := NewResourceCoordinator(nil, nil).Activate(context.Background(), model.OwnerRef{}); err != nil {
		t.Fatalf("Activate() error = %v, want nil", err)
	}
}

func TestResourceCoordinatorReleaseClearsReservation(t *testing.T) {
	owner := model.NewOwnerRef("deployment", "component")
	reservation := Reservation{Owner: owner, Cpus: []int{2}}
	store := &fakeReservationStore{reservation: reservation, found: true}
	coordinator := NewResourceCoordinator(store, nil)

	if err := coordinator.Release(context.Background(), owner); err != nil {
		t.Fatalf("Release() error = %v, want nil", err)
	}
	if len(store.cleared) != 1 || store.cleared[0] != owner {
		t.Fatalf("cleared owners = %#v, want %#v", store.cleared, owner)
	}
}

func TestResourceCoordinatorReleaseDoesNothingWhenAbsent(t *testing.T) {
	owner := model.NewOwnerRef("deployment", "component")
	store := &fakeReservationStore{}
	coordinator := NewResourceCoordinator(store, nil)

	if err := coordinator.Release(context.Background(), owner); err != nil {
		t.Fatalf("Release() error = %v, want nil", err)
	}
	if len(store.cleared) != 0 {
		t.Fatalf("cleared count = %d, want 0", len(store.cleared))
	}
}

func TestResourceRollbackReleasesOnlyOnFailure(t *testing.T) {
	owner := model.NewOwnerRef("deployment", "component")
	store := &fakeReservationStore{
		reservation: Reservation{Owner: owner},
		found:       true,
	}
	coordinator := NewResourceCoordinator(store, nil)
	logger := zap.NewNop().Sugar()

	// When error occurs and active
	deployErr := errors.New("deploy failed")
	rollback := NewResourceRollback(context.Background(), coordinator, owner, logger)
	rollback.ReleaseOnFailure(&deployErr)
	if len(store.cleared) != 1 {
		t.Fatalf("cleared count = %d, want 1", len(store.cleared))
	}

	// When disarmed via Complete()
	store.cleared = nil
	completedRollback := NewResourceRollback(context.Background(), coordinator, owner, logger)
	completedRollback.Complete()
	completedRollback.ReleaseOnFailure(&deployErr)
	if len(store.cleared) != 0 {
		t.Fatalf("completed rollback cleared count = %d, want 0", len(store.cleared))
	}

	// When error is nil
	var noErr error
	nilErrRollback := NewResourceRollback(context.Background(), coordinator, owner, logger)
	nilErrRollback.ReleaseOnFailure(&noErr)
	if len(store.cleared) != 0 {
		t.Fatalf("nil err rollback cleared count = %d, want 0", len(store.cleared))
	}
}
