package resource

import (
	"context"
	"errors"
	"testing"

	"github.com/margo/sandbox/poc/device/agent/resource/controller"
	"github.com/margo/sandbox/poc/device/agent/resource/ledger"
	"github.com/margo/sandbox/poc/device/agent/resource/model"
	"github.com/margo/sandbox/poc/device/agent/resource/planner"
	"github.com/margo/sandbox/standard/generatedCode/wfm/sbi"
	"go.uber.org/zap"
)

type fakeReservationStore struct {
	reservation model.Reservation
	found       bool
	loadErr     error
	clearErr    error
	cleared     []model.OwnerRef
	savedDep    string
	savedCpus   map[string][]int
	snapshot    ledger.AllocationSnapshot
}

func (s *fakeReservationStore) LoadSnapshot() (ledger.AllocationSnapshot, error) {
	return s.snapshot, nil
}

func (s *fakeReservationStore) LoadReservation(owner model.OwnerRef) (model.Reservation, bool, error) {
	return s.reservation, s.found, s.loadErr
}

func (s *fakeReservationStore) SaveReservation(deploymentId string, reservation model.Reservation) error {
	s.savedDep = deploymentId
	s.savedCpus = map[string][]int{string(reservation.Owner.Component): reservation.Cpus}
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

type fakeCachePlanner struct {
	plan model.CachePlan
	err  error
}

func (f *fakeCachePlanner) PlanCache(req planner.CachePlanningRequest) (model.CachePlan, error) {
	return f.plan, f.err
}

type fakeIsolationController struct {
	applied  model.Reservation
	verified model.Reservation
	released model.Reservation
	err      error
}

func (f *fakeIsolationController) Apply(ctx context.Context, r model.Reservation) error {
	f.applied = r
	return f.err
}

func (f *fakeIsolationController) Verify(ctx context.Context, r model.Reservation) error {
	f.verified = r
	return f.err
}

func (f *fakeIsolationController) Release(ctx context.Context, r model.Reservation) error {
	f.released = r
	return f.err
}

func newTestCoordinator(store ReservationStore, cpu planner.CpuPlanner, cache planner.CachePlanner, iso controller.CacheIsolationController) *ResourceCoordinator {
	if store == nil {
		store = &fakeReservationStore{}
	}
	if cpu == nil {
		cpu = &fakeCpuPlanner{}
	}
	if cache == nil {
		cache = &fakeCachePlanner{}
	}
	if iso == nil {
		iso = &fakeIsolationController{}
	}
	c, err := NewResourceCoordinatorBuilder().
		WithStore(store).
		WithCpuPlanner(cpu).
		WithCachePlanner(cache).
		WithCacheController(iso).
		Build()
	if err != nil {
		panic(err)
	}
	return c
}

func TestResourceCoordinatorBuilderFailsOnNil(t *testing.T) {
	store := &fakeReservationStore{}
	cpu := &fakeCpuPlanner{}
	cache := &fakeCachePlanner{}
	iso := &fakeIsolationController{}

	tests := []struct {
		name        string
		store       ReservationStore
		cpu         planner.CpuPlanner
		cache       planner.CachePlanner
		iso         controller.CacheIsolationController
		expectError bool
	}{
		{"all non-nil", store, cpu, cache, iso, false},
		{"nil store", nil, cpu, cache, iso, true},
		{"nil cpu", store, nil, cache, iso, true},
		{"nil cache", store, cpu, nil, iso, true},
		{"nil iso", store, cpu, cache, nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewResourceCoordinatorBuilder().
				WithStore(tt.store).
				WithCpuPlanner(tt.cpu).
				WithCachePlanner(tt.cache).
				WithCacheController(tt.iso).
				Build()
			if (err != nil) != tt.expectError {
				t.Fatalf("NewResourceCoordinator() error = %v, expectError = %v", err, tt.expectError)
			}
		})
	}

	t.Run("builder With... methods fail on nil", func(t *testing.T) {
		b := NewResourceCoordinatorBuilder().
			WithStore(nil).
			WithCpuPlanner(nil).
			WithCachePlanner(nil).
			WithCacheController(nil)

		_, err := b.Build()
		if err == nil {
			t.Fatal("expected error from builder with nil values, got nil")
		}
	})
}

func TestResourceCoordinatorNewLedger(t *testing.T) {
	store := &fakeReservationStore{
		snapshot: ledger.AllocationSnapshot{
			CpuOwners: map[int]model.OwnerRef{
				1: model.NewOwnerRef("dep1", "comp1"),
			},
		},
	}
	c := newTestCoordinator(store, nil, nil, nil)
	l, err := c.NewLedger("dep1")
	if err != nil {
		t.Fatalf("NewLedger() error = %v", err)
	}
	if l == nil {
		t.Fatal("NewLedger() returned nil")
	}

	// newLedger delegates to NewLedger
	l2, err := c.NewLedger("dep1")
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
	c := newTestCoordinator(nil, fakePlanner, nil, nil)
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
	if plan.HasCache() {
		t.Fatalf("plan.HasCache() = true, want false when no cache requested")
	}
}

func TestResourceCoordinatorPlanWithCache(t *testing.T) {
	owner := model.NewOwnerRef("dep-1", "comp-1")
	cacheLevel := sbi.CacheLevelL3
	allocMode := sbi.CacheAllocationExclusive
	cacheSize := "4096 KI"

	req := ResourceRequest{
		Owner: owner,
		Requirements: &sbi.RequiredResources{
			Cache: &[]sbi.Cache{
				{
					Level:      cacheLevel,
					Allocation: allocMode,
					Size:       &cacheSize,
				},
			},
		},
	}

	fakeCache := &fakeCachePlanner{
		plan: model.CachePlan{
			Component: "comp-1",
			L3CacheAssignment: &model.CacheAssignment{
				Owner:   owner,
				Level:   "L3",
				CacheId: "0",
				SizeKiB: 4096,
				Mask:    "0x3",
				Clos:    "1",
			},
		},
	}

	// Without CPU assignments, requesting cache returns an error
	fakeCpuNoCpus := &fakeCpuPlanner{
		plan: model.CpuPlan{
			Component: "comp-1",
			Cpus:      []int{},
		},
	}
	c := newTestCoordinator(nil, fakeCpuNoCpus, fakeCache, nil)
	_, err := c.Plan(nil, req)
	if err == nil {
		t.Fatal("Plan() expected error when cache requested without CPU assignments, got nil")
	}

	// With CPU assignments, cache planning succeeds
	fakeCpu := &fakeCpuPlanner{
		plan: model.CpuPlan{
			Component: "comp-1",
			Cpus:      []int{2},
		},
	}
	cWithCpu := newTestCoordinator(nil, fakeCpu, fakeCache, nil)
	plan, err := cWithCpu.Plan(nil, req)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if !plan.HasCache() {
		t.Fatalf("plan.HasCache() = false, want true")
	}
	if plan.Cache.L3CacheAssignment.Clos != "1" {
		t.Fatalf("plan.Cache.Clos = %v, want 1", plan.Cache.L3CacheAssignment.Clos)
	}
}

func TestResourceCoordinatorCommit(t *testing.T) {
	store := &fakeReservationStore{}
	c := newTestCoordinator(store, nil, nil, nil)

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

func TestResourceCoordinatorCommitWithCache(t *testing.T) {
	store := &fakeReservationStore{}
	c := newTestCoordinator(store, nil, nil, nil)

	owner := model.NewOwnerRef("dep-1", "comp-1")
	plan := ResourcePlan{
		Owner: owner,
		Cache: model.CachePlan{
			Component: "comp-1",
			L3CacheAssignment: &model.CacheAssignment{
				Owner:   owner,
				Level:   "L3",
				CacheId: "0",
				SizeKiB: 2048,
				Mask:    "0x1",
				Clos:    "2",
			},
		},
	}

	if err := c.Commit(context.Background(), plan); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if store.savedDep != "dep-1" {
		t.Fatalf("savedDep = %q, want dep-1", store.savedDep)
	}
}

func TestResourceCoordinatorActivateIsANoOp(t *testing.T) {
	c := newTestCoordinator(nil, nil, nil, nil)
	if err := c.Activate(context.Background(), model.OwnerRef{}); err != nil {
		t.Fatalf("Activate() error = %v, want nil", err)
	}
}

func TestResourceCoordinatorReleaseClearsReservation(t *testing.T) {
	owner := model.NewOwnerRef("deployment", "component")
	reservation := model.Reservation{Owner: owner, Cpus: []int{2}}
	store := &fakeReservationStore{reservation: reservation, found: true}
	coordinator := newTestCoordinator(store, nil, nil, nil)

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
	coordinator := newTestCoordinator(store, nil, nil, nil)

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
		reservation: model.Reservation{Owner: owner},
		found:       true,
	}
	coordinator := newTestCoordinator(store, nil, nil, nil)
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
