package resource

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/margo/sandbox/poc/device/agent/database"
	"github.com/margo/sandbox/poc/device/agent/resource/controller"
	"github.com/margo/sandbox/poc/device/agent/resource/ledger"
	"github.com/margo/sandbox/poc/device/agent/resource/model"
	"github.com/margo/sandbox/poc/device/agent/resource/planner"
	"github.com/margo/sandbox/poc/device/agent/types"
	"github.com/margo/sandbox/standard/generatedCode/wfm/sbi"
	"github.com/stretchr/testify/mock"
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
		WithClassNamer(controller.NewPqosClassNamer()).
		WithAllocationLock(&sync.Mutex{}).
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
	namer := controller.NewPqosClassNamer()
	mu := &sync.Mutex{}

	tests := []struct {
		name        string
		store       ReservationStore
		cpu         planner.CpuPlanner
		cache       planner.CachePlanner
		iso         controller.CacheIsolationController
		namer       controller.ClassNamer
		mu          *sync.Mutex
		expectError bool
	}{
		{"all non-nil", store, cpu, cache, iso, namer, mu, false},
		{"nil store", nil, cpu, cache, iso, namer, mu, true},
		{"nil cpu", store, nil, cache, iso, namer, mu, true},
		{"nil cache", store, cpu, nil, iso, namer, mu, true},
		{"nil iso", store, cpu, cache, nil, namer, mu, true},
		{"nil namer", store, cpu, cache, iso, nil, mu, true},
		{"nil allocMu", store, cpu, cache, iso, namer, nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewResourceCoordinatorBuilder().
				WithStore(tt.store).
				WithCpuPlanner(tt.cpu).
				WithCachePlanner(tt.cache).
				WithCacheController(tt.iso).
				WithClassNamer(tt.namer).
				WithAllocationLock(tt.mu).
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
			WithCacheController(nil).
			WithClassNamer(nil).
			WithAllocationLock(nil)

		_, err := b.Build()
		if err == nil {
			t.Fatal("expected error from builder with nil values, got nil")
		}
	})
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
	plan, err := c.plan(nil, req)
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
				Masks:   map[string]string{"0": "0x3"},
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
	_, err := c.plan(nil, req)
	if err == nil {
		t.Fatal("Plan() expected error when cache requested without CPU assignments, got nil")
	}
	wantErr := `component "comp-1" requests exclusive cache but has no cpu assignments`
	if err.Error() != wantErr {
		t.Fatalf("Plan() error = %q, want %q", err.Error(), wantErr)
	}

	// With CPU assignments, cache planning succeeds
	fakeCpu := &fakeCpuPlanner{
		plan: model.CpuPlan{
			Component: "comp-1",
			Cpus:      []int{2},
		},
	}
	cWithCpu := newTestCoordinator(nil, fakeCpu, fakeCache, nil)
	plan, err := cWithCpu.plan(nil, req)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if !plan.HasCache() {
		t.Fatalf("plan.HasCache() = false, want true")
	}
	if plan.Cache.L3CacheAssignment.Clos != "1" {
		t.Fatalf("plan.Cache.Clos = %v, want 1", plan.Cache.L3CacheAssignment.Clos)
	}

	fakeCacheErr := &fakeCachePlanner{
		err: errors.New("cache planning failed"),
	}
	cWithErr := newTestCoordinator(nil, fakeCpu, fakeCacheErr, nil)
	if _, err := cWithErr.Plan(nil, req); err == nil {
		t.Fatal("Plan() expected error when cache planner fails, got nil")
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

	if err := c.commit(context.Background(), plan); err != nil {
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
	if err := c.commit(context.Background(), emptyPlan); err != nil {
		t.Fatalf("Commit() empty plan error = %v", err)
	}
	if store.savedDep != "" {
		t.Fatalf("Commit() empty plan should not write to store")
	}
}

func TestResourceCoordinatorCommitWithCache(t *testing.T) {
	store := &fakeReservationStore{}
	iso := &fakeIsolationController{}
	c := newTestCoordinator(store, nil, nil, iso)

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
				Masks:   map[string]string{"0": "0x1"},
				Clos:    "2",
			},
		},
	}

	if err := c.commit(context.Background(), plan); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if store.savedDep != "dep-1" {
		t.Fatalf("savedDep = %q, want dep-1", store.savedDep)
	}
	if !iso.applied.HasL3Cache() || iso.applied.L3CacheAssignment.Clos != "2" {
		t.Fatalf("expected isolation.Apply to receive reservation with clos '2', got %+v", iso.applied)
	}

	// Commit failure on Apply
	isoFail := &fakeIsolationController{err: errors.New("apply failed")}
	cFail := newTestCoordinator(store, nil, nil, isoFail)
	if err := cFail.commit(context.Background(), plan); err == nil {
		t.Fatal("expected error when isolation.Apply fails, got nil")
	}
}

func TestResourceCoordinatorVerify(t *testing.T) {
	owner := model.NewOwnerRef("deployment", "component")
	res := model.Reservation{
		Owner: owner,
		L3CacheAssignment: &model.CacheAssignment{
			CacheId: "0",
			Masks:   map[string]string{"0": "0x3"},
			Clos:    "1",
		},
	}

	t.Run("successful verify", func(t *testing.T) {
		store := &fakeReservationStore{reservation: res, found: true}
		iso := &fakeIsolationController{}
		c := newTestCoordinator(store, nil, nil, iso)

		if err := c.Verify(context.Background(), owner); err != nil {
			t.Fatalf("Verify() error = %v", err)
		}
		if iso.verified.L3CacheAssignment.Clos != "1" {
			t.Fatalf("expected isolation.Verify to receive reservation with clos '1', got %+v", iso.verified)
		}
	})

	t.Run("absent reservation is a no-op", func(t *testing.T) {
		store := &fakeReservationStore{found: false}
		iso := &fakeIsolationController{}
		c := newTestCoordinator(store, nil, nil, iso)

		if err := c.Verify(context.Background(), owner); err != nil {
			t.Fatalf("Verify() error = %v", err)
		}
		if iso.verified.L3CacheAssignment != nil {
			t.Fatal("expected no verification for absent reservation")
		}
	})

	t.Run("verify failure propagates", func(t *testing.T) {
		store := &fakeReservationStore{reservation: res, found: true}
		iso := &fakeIsolationController{err: errors.New("verify failed")}
		c := newTestCoordinator(store, nil, nil, iso)

		if err := c.Verify(context.Background(), owner); err == nil {
			t.Fatal("expected error when Verify fails, got nil")
		}
	})
}

func TestResourceCoordinatorReleaseClearsReservation(t *testing.T) {
	owner := model.NewOwnerRef("deployment", "component")
	reservation := model.Reservation{
		Owner: owner,
		Cpus:  []int{2},
		L3CacheAssignment: &model.CacheAssignment{
			CacheId: "0",
			Masks:   map[string]string{"0": "0x3"},
			Clos:    "1",
		},
	}
	store := &fakeReservationStore{reservation: reservation, found: true}
	iso := &fakeIsolationController{}
	coordinator := newTestCoordinator(store, nil, nil, iso)

	if err := coordinator.Release(context.Background(), owner); err != nil {
		t.Fatalf("Release() error = %v, want nil", err)
	}
	if len(store.cleared) != 1 || store.cleared[0] != owner {
		t.Fatalf("cleared owners = %#v, want %#v", store.cleared, owner)
	}
	if iso.released.L3CacheAssignment.Clos != "1" {
		t.Fatalf("expected isolation.Release to be called with clos '1', got %+v", iso.released)
	}
}

func TestResourceCoordinatorReleaseRetainsReservationWhenIsolationFails(t *testing.T) {
	owner := model.NewOwnerRef("deployment", "component")
	reservation := model.Reservation{
		Owner: owner,
		L3CacheAssignment: &model.CacheAssignment{
			CacheId: "0",
			Masks:   map[string]string{"0": "0x3"},
			Clos:    "1",
		},
	}
	store := &fakeReservationStore{reservation: reservation, found: true}
	iso := &fakeIsolationController{err: errors.New("release failed")}
	coordinator := newTestCoordinator(store, nil, nil, iso)

	err := coordinator.Release(context.Background(), owner)
	if err == nil {
		t.Fatal("expected error returned from Release when isolation fails")
	}
	// On hardware/policy release failure, the reservation must remain in the store
	if len(store.cleared) != 0 {
		t.Fatalf("expected ClearComponent not to be called when isolation release fails, got cleared=%v", store.cleared)
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

type mockIsolationController struct {
	mock.Mock
}

func (m *mockIsolationController) Apply(ctx context.Context, r model.Reservation) error {
	args := m.Called(ctx, r)
	return args.Error(0)
}

func (m *mockIsolationController) Verify(ctx context.Context, r model.Reservation) error {
	args := m.Called(ctx, r)
	return args.Error(0)
}

func (m *mockIsolationController) Release(ctx context.Context, r model.Reservation) error {
	args := m.Called(ctx, r)
	return args.Error(0)
}

type staticBalloonPolicyReader struct {
	policy *model.ParsedBalloonPolicy
}

func (r staticBalloonPolicyReader) Parsed() *model.ParsedBalloonPolicy {
	return r.policy
}

func makeCpuAndCacheReqs(comp string, cores float32, cacheSize string) *sbi.RequiredResources {
	class := sbi.CpuClassPerformance
	cpuType := sbi.CpuTypeIsolated
	level := sbi.CacheLevelL3
	mode := sbi.CacheAllocationExclusive

	return &sbi.RequiredResources{
		Cpu: &[]sbi.Cpu{
			{
				Name:  &comp,
				Cores: &cores,
				Class: &class,
				Type:  &cpuType,
			},
		},
		Cache: &[]sbi.Cache{
			{
				Level:      level,
				Allocation: mode,
				Size:       &cacheSize,
			},
		},
	}
}

func TestResourceCoordinatorConcurrentAllocateAndRelease(t *testing.T) {
	db := database.NewDatabase(t.TempDir())

	isolatedIndices := []int{1, 2, 3, 4, 5, 6, 7, 8}
	isolatedSet := make(map[int]struct{}, len(isolatedIndices))
	balloonTypes := make([]model.ParsedBalloonType, 0, len(isolatedIndices))
	preferIsol := true

	for _, cpu := range isolatedIndices {
		isolatedSet[cpu] = struct{}{}
		balloonTypes = append(balloonTypes, model.ParsedBalloonType{
			Name:                 fmt.Sprintf("ipc%d", cpu),
			PreferIsolCpus:       &preferIsol,
			PreferCloseToDevices: []string{fmt.Sprintf("/sys/devices/system/cpu/cpu%d/cache/index2", cpu)},
		})
	}

	l3Caches := []types.HostTopologyCache{
		{
			Id:        "0",
			Level:     "L3",
			Ways:      12,
			WaySizeKB: 2048,
			Cores:     "1-8",
		},
	}
	const maxClos = 16

	sharedAllocMu := &sync.Mutex{}

	composeIsoMock := &mockIsolationController{}
	composeIsoMock.On("Apply", mock.Anything, mock.AnythingOfType("model.Reservation")).Return(nil)
	composeIsoMock.On("Release", mock.Anything, mock.AnythingOfType("model.Reservation")).Return(nil)

	composeCoord, err := NewResourceCoordinatorBuilder().
		WithStore(NewDatabaseReservationStore(db, isolatedSet)).
		WithCpuPlanner(planner.NewTopologyCpuPlanner(isolatedIndices)).
		WithCachePlanner(planner.NewL3CachePlanner(l3Caches)).
		WithCacheTopology(l3Caches, maxClos).
		WithClassNamer(controller.NewPqosClassNamer()).
		WithCacheController(composeIsoMock).
		WithAllocationLock(sharedAllocMu).
		Build()
	if err != nil {
		t.Fatalf("failed to build compose coordinator: %v", err)
	}

	policyReader := staticBalloonPolicyReader{
		policy: &model.ParsedBalloonPolicy{
			Name:         "default",
			Namespace:    "kube-system",
			BalloonTypes: balloonTypes,
		},
	}

	helmIsoMock := &mockIsolationController{}
	helmIsoMock.On("Apply", mock.Anything, mock.AnythingOfType("model.Reservation")).Return(nil)
	helmIsoMock.On("Release", mock.Anything, mock.AnythingOfType("model.Reservation")).Return(nil)

	helmCoord, err := NewResourceCoordinatorBuilder().
		WithStore(NewDatabaseReservationStore(db, isolatedSet)).
		WithCpuPlanner(planner.NewBalloonCpuPlanner(policyReader, isolatedIndices)).
		WithCachePlanner(planner.NewL3CachePlanner(l3Caches)).
		WithCacheTopology(l3Caches, maxClos).
		WithClassNamer(controller.NewRdtClassNamer()).
		WithCacheController(helmIsoMock).
		WithAllocationLock(sharedAllocMu).
		Build()
	if err != nil {
		t.Fatalf("failed to build helm coordinator: %v", err)
	}

	// Phase 1: N concurrent fresh allocations (mixed Compose + Helm, 8 deployments for 8 CPUs / 8 cache ways)
	const numConcurrent = 8
	for i := range numConcurrent {
		depID := fmt.Sprintf("dep-%02d", i)
		if err := db.SetDesiredState(depID, database.AppDeploymentState{}); err != nil {
			t.Fatalf("SetDesiredState(%s) error = %v", depID, err)
		}
	}

	type allocResult struct {
		owner model.OwnerRef
		coord *ResourceCoordinator
		plan  ResourcePlan
		err   error
	}

	startBarrier := make(chan struct{})
	results := make([]allocResult, numConcurrent)
	var wg sync.WaitGroup

	for i := range numConcurrent {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-startBarrier

			depID := fmt.Sprintf("dep-%02d", idx)
			compName := fmt.Sprintf("comp-%02d", idx)
			owner := model.NewOwnerRef(depID, compName)

			coord := composeCoord
			if idx%2 == 1 {
				coord = helmCoord
			}

			plan, allocErr := coord.Allocate(context.Background(), ResourceRequest{
				Owner:        owner,
				Requirements: makeCpuAndCacheReqs(compName, 1, "2048 KI"), // 1 isolated core, 1 L3 way
			})
			results[idx] = allocResult{owner: owner, coord: coord, plan: plan, err: allocErr}
		}(i)
	}

	close(startBarrier)
	wg.Wait()

	// Verify 4 Compose and 4 Helm Apply calls occurred during Phase 1
	composeIsoMock.AssertNumberOfCalls(t, "Apply", 4)
	helmIsoMock.AssertNumberOfCalls(t, "Apply", 4)

	// Verify zero overlap in CPUs, L3 cache ways, and CLOS IDs
	seenCpus := make(map[int]model.OwnerRef)
	seenWays := make(map[int64]model.OwnerRef)
	seenClos := make(map[model.ClosId]model.OwnerRef)

	for _, res := range results {
		if res.err != nil {
			t.Fatalf("Allocate(%v) unexpected error = %v", res.owner, res.err)
		}
		if len(res.plan.Cpu.Cpus) != 1 {
			t.Fatalf("Allocate(%v) cpus = %v, want 1 CPU", res.owner, res.plan.Cpu.Cpus)
		}
		cpu := res.plan.Cpu.Cpus[0]
		if prev, exists := seenCpus[cpu]; exists {
			t.Fatalf("CPU %d allocated to both %v and %v", cpu, prev, res.owner)
		}
		seenCpus[cpu] = res.owner

		cacheAssign := res.plan.Cache.L3CacheAssignment
		if cacheAssign == nil {
			t.Fatalf("Allocate(%v) expected L3CacheAssignment, got nil", res.owner)
		}
		mask := cacheAssign.Masks[cacheAssign.CacheId]
		interval, err := model.ParseContiguousWayMask(mask, 12)
		if err != nil {
			t.Fatalf("ParseContiguousWayMask(%q) error = %v", mask, err)
		}
		for way := interval.Start; way < interval.End(); way++ {
			if prev, exists := seenWays[way]; exists {
				t.Fatalf("L3 cache way %d allocated to both %v and %v", way, prev, res.owner)
			}
			seenWays[way] = res.owner
		}

		if prev, exists := seenClos[cacheAssign.Clos]; exists {
			t.Fatalf("CLOS %q allocated to both %v and %v", cacheAssign.Clos, prev, res.owner)
		}
		seenClos[cacheAssign.Clos] = res.owner
	}

	// Phase 2: Concurrent Release of half the deployments while new deployments contend for freed resources
	const numChurn = 4
	for i := range numChurn {
		depID := fmt.Sprintf("dep-churn-%02d", i)
		if err := db.SetDesiredState(depID, database.AppDeploymentState{}); err != nil {
			t.Fatalf("SetDesiredState(%s) error = %v", depID, err)
		}
	}

	churnBarrier := make(chan struct{})
	var churnWg sync.WaitGroup

	// Release first 4 deployments concurrently (2 Compose + 2 Helm)
	for i := range numChurn {
		churnWg.Add(1)
		go func(idx int) {
			defer churnWg.Done()
			<-churnBarrier
			if err := results[idx].coord.Release(context.Background(), results[idx].owner); err != nil {
				t.Errorf("Release(%v) error = %v", results[idx].owner, err)
			}
		}(i)
	}

	// Simultaneously try to allocate 4 new deployments
	for i := range numChurn {
		churnWg.Add(1)
		go func(idx int) {
			defer churnWg.Done()
			<-churnBarrier

			depID := fmt.Sprintf("dep-churn-%02d", idx)
			compName := fmt.Sprintf("comp-churn-%02d", idx)
			owner := model.NewOwnerRef(depID, compName)
			coord := composeCoord
			if idx%2 == 1 {
				coord = helmCoord
			}

			_, _ = coord.Allocate(context.Background(), ResourceRequest{
				Owner:        owner,
				Requirements: makeCpuAndCacheReqs(compName, 1, "2048 KI"),
			})
		}(i)
	}

	close(churnBarrier)
	churnWg.Wait()

	// Verify 2 Compose and 2 Helm Release calls occurred during Phase 2
	composeIsoMock.AssertNumberOfCalls(t, "Release", 2)
	helmIsoMock.AssertNumberOfCalls(t, "Release", 2)

	// Verify persisted DB snapshot still has no overlapping CPUs or cache ways
	allocCpus, allocCaches := db.AllocatedResources()
	if len(allocCpus) > len(isolatedIndices) {
		t.Fatalf("persisted CPUs (%d) exceed isolated pool (%d)", len(allocCpus), len(isolatedIndices))
	}

	persistedWays := make(map[int64]string)
	for _, c := range allocCaches {
		mask := c.Masks[c.CacheId]
		interval, err := model.ParseContiguousWayMask(mask, 12)
		if err != nil {
			t.Fatalf("invalid persisted mask %q: %v", mask, err)
		}
		for way := interval.Start; way < interval.End(); way++ {
			if prev, exists := persistedWays[way]; exists {
				t.Fatalf("persisted L3 way %d owned by both %s and %s", way, prev, c.Owner)
			}
			persistedWays[way] = c.Owner
		}
	}
}
