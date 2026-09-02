package resource

import (
	"context"
	"errors"
	"testing"

	"github.com/margo/sandbox/poc/device/agent/database"
	"go.uber.org/zap"
)

type fakeReservationStore struct {
	reservation Reservation
	found       bool
	loadErr     error
	clearErr    error
	cleared     []OwnerRef
}

func (s *fakeReservationStore) LoadReservation(owner OwnerRef) (Reservation, bool, error) {
	return s.reservation, s.found, s.loadErr
}

func (s *fakeReservationStore) ClearComponent(owner OwnerRef) error {
	s.cleared = append(s.cleared, owner)
	return s.clearErr
}

type fakeIsolationReleaser struct {
	released []Reservation
	err      error
}

func (r *fakeIsolationReleaser) ReleaseIsolation(ctx context.Context, reservation Reservation) error {
	r.released = append(r.released, reservation)
	return r.err
}

func TestResourceCoordinatorReleaseClearsReservation(t *testing.T) {
	owner := NewOwnerRef("deployment", "component")
	reservation := Reservation{Owner: owner, CPUs: []int{2}}
	store := &fakeReservationStore{reservation: reservation, found: true}
	coordinator := NewResourceCoordinator(store, nil)

	if err := coordinator.Release(context.Background(), owner); err != nil {
		t.Fatalf("Release() error = %v, want nil", err)
	}
	if len(store.cleared) != 1 || store.cleared[0] != owner {
		t.Fatalf("cleared owners = %#v, want %#v", store.cleared, owner)
	}
}

func TestResourceCoordinatorReleaseResetsIsolationOnlyWhenCacheIsHeld(t *testing.T) {
	owner := NewOwnerRef("deployment", "component")
	cpuOnly := &fakeIsolationReleaser{}
	cpuOnlyStore := &fakeReservationStore{
		reservation: Reservation{Owner: owner, CPUs: []int{2}},
		found:       true,
	}
	if err := NewResourceCoordinator(cpuOnlyStore, cpuOnly).Release(context.Background(), owner); err != nil {
		t.Fatalf("Release() error = %v, want nil", err)
	}
	if len(cpuOnly.released) != 0 {
		t.Fatalf("cpu-only reservation triggered %d isolation releases, want 0", len(cpuOnly.released))
	}

	withCache := &fakeIsolationReleaser{}
	cacheStore := &fakeReservationStore{
		reservation: Reservation{
			Owner:  owner,
			CPUs:   []int{2},
			Caches: []CacheReservation{{CacheID: "0", Mask: "0x3", ClassID: 2}},
		},
		found: true,
	}
	if err := NewResourceCoordinator(cacheStore, withCache).Release(context.Background(), owner); err != nil {
		t.Fatalf("Release() error = %v, want nil", err)
	}
	if len(withCache.released) != 1 {
		t.Fatalf("cache reservation triggered %d isolation releases, want 1", len(withCache.released))
	}
}

func TestResourceCoordinatorReleaseClearsRecordWhenIsolationResetFails(t *testing.T) {
	owner := NewOwnerRef("deployment", "component")
	store := &fakeReservationStore{
		reservation: Reservation{
			Owner:  owner,
			Caches: []CacheReservation{{CacheID: "0", Mask: "0x3", ClassID: 2}},
		},
		found: true,
	}
	resetErr := errors.New("pqos reset failed")
	coordinator := NewResourceCoordinator(store, &fakeIsolationReleaser{err: resetErr})

	err := coordinator.Release(context.Background(), owner)
	if !errors.Is(err, resetErr) {
		t.Fatalf("Release() error = %v, want %v", err, resetErr)
	}
	if len(store.cleared) != 1 {
		t.Fatalf("clear count = %d, want 1 despite the failed reset", len(store.cleared))
	}
}

func TestResourceCoordinatorReleaseDoesNothingWhenReservationIsAbsent(t *testing.T) {
	owner := NewOwnerRef("deployment", "component")
	store := &fakeReservationStore{}
	coordinator := NewResourceCoordinator(store, nil)

	if err := coordinator.Release(context.Background(), owner); err != nil {
		t.Fatalf("Release() error = %v, want nil", err)
	}
	if len(store.cleared) != 0 {
		t.Fatalf("absent reservation caused clear=%d", len(store.cleared))
	}
}

func TestDatabaseReservationStoreLoadReservationIncludesCPUsAndCaches(t *testing.T) {
	db := database.NewDatabase(t.TempDir())
	const deploymentID = "deployment"
	const componentName = "component"
	if err := db.SetDesiredState(deploymentID, database.AppDeploymentState{}); err != nil {
		t.Fatalf("SetDesiredState() error = %v", err)
	}
	if err := db.SetAllocations(deploymentID, database.Allocations{
		CPUs: map[string][]int{componentName: {4, 2}},
		Caches: map[string][]database.CacheAssignment{
			componentName: {{
				ComponentName: componentName,
				Level:         "L3",
				CacheID:       "0",
				SizeKB:        1024,
				Mask:          "0x3",
				ClassID:       2,
			}},
		},
	}); err != nil {
		t.Fatalf("SetAllocations() error = %v", err)
	}

	owner := NewOwnerRef(deploymentID, componentName)
	reservation, found, err := NewDatabaseReservationStore(db).LoadReservation(owner)
	if err != nil {
		t.Fatalf("LoadReservation() error = %v", err)
	}
	if !found {
		t.Fatal("LoadReservation() found = false, want true")
	}
	if reservation.Owner != owner || reservation.CPUSet() != "2,4" {
		t.Fatalf("reservation owner/cpuset = %#v/%q, want %#v/%q", reservation.Owner, reservation.CPUSet(), owner, "2,4")
	}
	if len(reservation.Caches) != 1 {
		t.Fatalf("cache reservation count = %d, want 1", len(reservation.Caches))
	}
	wantCache := CacheReservation{Level: "L3", CacheID: "0", SizeKiB: 1024, Mask: "0x3", ClassID: 2}
	if reservation.Caches[0] != wantCache {
		t.Fatalf("cache reservation = %#v, want %#v", reservation.Caches[0], wantCache)
	}
}

func TestDatabaseReservationStoreClearComponentPreservesSiblings(t *testing.T) {
	db := database.NewDatabase(t.TempDir())
	const deploymentID = "deployment"
	const componentName = "component"
	const siblingName = "sibling"
	if err := db.SetDesiredState(deploymentID, database.AppDeploymentState{}); err != nil {
		t.Fatalf("SetDesiredState() error = %v", err)
	}
	wantCaches := map[string][]database.CacheAssignment{
		componentName: {{ComponentName: componentName, CacheID: "0", Mask: "0x3"}},
		siblingName:   {{ComponentName: siblingName, CacheID: "1", Mask: "0xC"}},
	}
	if err := db.SetAllocations(deploymentID, database.Allocations{
		CPUs:   map[string][]int{componentName: {2}, siblingName: {4}},
		Caches: wantCaches,
	}); err != nil {
		t.Fatalf("SetAllocations() error = %v", err)
	}

	store := NewDatabaseReservationStore(db)
	if err := store.ClearComponent(NewOwnerRef(deploymentID, componentName)); err != nil {
		t.Fatalf("ClearComponent() error = %v", err)
	}
	allocations, err := db.GetAllocations(deploymentID)
	if err != nil {
		t.Fatalf("GetAllocations() error = %v", err)
	}
	if _, found := allocations.CPUs[componentName]; found {
		t.Fatalf("CPU assignment for %q was not cleared", componentName)
	}
	if _, found := allocations.Caches[componentName]; found {
		t.Fatalf("cache assignment for %q was not cleared", componentName)
	}
	if got := allocations.CPUs[siblingName]; len(got) != 1 || got[0] != 4 {
		t.Fatalf("sibling CPU assignment = %#v, want [4]", got)
	}
	gotCaches := allocations.Caches[siblingName]
	if len(gotCaches) != 1 || gotCaches[0] != wantCaches[siblingName][0] {
		t.Fatalf("sibling cache assignments = %#v, want %#v", gotCaches, wantCaches[siblingName])
	}
}

func TestResourceRollbackReleasesOnlyActiveFailedComponent(t *testing.T) {
	owner := NewOwnerRef("deployment", "component")
	store := &fakeReservationStore{
		reservation: Reservation{Owner: owner},
		found:       true,
	}
	coordinator := NewResourceCoordinator(store, nil)
	logger := zap.NewNop().Sugar()

	deploymentErr := errors.New("deployment failed")
	rollback := NewResourceRollback(context.Background(), coordinator, owner, logger)
	rollback.ReleaseOnFailure(&deploymentErr)
	if len(store.cleared) != 1 {
		t.Fatalf("failed component clear count = %d, want 1", len(store.cleared))
	}

	store.cleared = nil
	completed := NewResourceRollback(context.Background(), coordinator, owner, logger)
	completed.Complete()
	completed.ReleaseOnFailure(&deploymentErr)
	if len(store.cleared) != 0 {
		t.Fatalf("completed component clear count = %d, want 0", len(store.cleared))
	}
}
