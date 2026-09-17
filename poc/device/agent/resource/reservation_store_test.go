package resource

import (
	"reflect"
	"testing"

	"github.com/margo/sandbox/poc/device/agent/database"
	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

func TestDatabaseReservationStoreLoadReservation(t *testing.T) {
	db := database.NewDatabase(t.TempDir())
	const deploymentID = "deployment-1"
	const componentName = "cyclictest"

	if err := db.SetDesiredState(deploymentID, database.AppDeploymentState{}); err != nil {
		t.Fatalf("SetDesiredState() error = %v", err)
	}
	if err := db.SetAllocations(deploymentID, database.Allocations{
		Cpus: map[string][]int{componentName: {4, 2}},
	}); err != nil {
		t.Fatalf("SetAllocations() error = %v", err)
	}

	owner := model.NewOwnerRef(deploymentID, componentName)
	store := NewDatabaseReservationStore(db, map[int]struct{}{2: {}, 4: {}})

	reservation, found, err := store.LoadReservation(owner)
	if err != nil {
		t.Fatalf("LoadReservation() error = %v", err)
	}
	if !found {
		t.Fatal("LoadReservation() found = false, want true")
	}
	if reservation.Owner != owner {
		t.Fatalf("reservation owner = %#v, want %#v", reservation.Owner, owner)
	}
	if reservation.CpuSet() != "2,4" {
		t.Fatalf("reservation.CpuSet() = %q, want %q", reservation.CpuSet(), "2,4")
	}
	wantCpus := []int{4, 2}
	if !reflect.DeepEqual(reservation.Cpus, wantCpus) {
		t.Fatalf("reservation.Cpus = %#v, want %#v", reservation.Cpus, wantCpus)
	}
}

func TestDatabaseReservationStoreLoadReservationNotFound(t *testing.T) {
	db := database.NewDatabase(t.TempDir())
	const deploymentID = "deployment-1"

	if err := db.SetDesiredState(deploymentID, database.AppDeploymentState{}); err != nil {
		t.Fatalf("SetDesiredState() error = %v", err)
	}

	store := NewDatabaseReservationStore(db, nil)
	_, found, err := store.LoadReservation(model.NewOwnerRef(deploymentID, "unknown"))
	if err != nil {
		t.Fatalf("LoadReservation() error = %v", err)
	}
	if found {
		t.Fatal("LoadReservation() found = true, want false")
	}
}

func TestDatabaseReservationStoreSaveAllocations(t *testing.T) {
	db := database.NewDatabase(t.TempDir())
	const deploymentID = "deployment-1"

	if err := db.SetDesiredState(deploymentID, database.AppDeploymentState{}); err != nil {
		t.Fatalf("SetDesiredState() error = %v", err)
	}

	store := NewDatabaseReservationStore(db, nil)
	componentName1 := "comp1"
	cpuSet1 := []int{1, 2}

	if err := store.SaveReservation(deploymentID,
		model.Reservation{Cpus: cpuSet1, Owner: model.OwnerRef{Deployment: deploymentID, Component: model.ComponentRef(componentName1)}}); err != nil {
		t.Fatalf("SaveAllocations() error = %v", err)
	}

	allocations, err := db.GetAllocations(deploymentID)
	if err != nil {
		t.Fatalf("GetAllocations() error = %v", err)
	}
	if !reflect.DeepEqual(allocations.Cpus[componentName1], cpuSet1) {
		t.Fatalf("GetAllocations() cpus = %#v, want %#v", allocations.Cpus[componentName1], cpuSet1)
	}

	// Saving allocations for another component preserves existing component allocations
	componentName2 := "comp2"
	cpuSet2 := []int{3}

	if err := store.SaveReservation(deploymentID,
		model.Reservation{Cpus: cpuSet2, Owner: model.OwnerRef{Deployment: deploymentID, Component: model.ComponentRef(componentName2)}}); err != nil {
		t.Fatalf("SaveAllocations() error = %v", err)
	}

	allocations2, err := db.GetAllocations(deploymentID)
	if err != nil {
		t.Fatalf("GetAllocations() error = %v", err)
	}
	wantMerged := map[string][]int{componentName1: cpuSet1, componentName2: cpuSet2}
	if !reflect.DeepEqual(allocations2.Cpus, wantMerged) {
		t.Fatalf("GetAllocations() after merge = %#v, want %#v", allocations2.Cpus, wantMerged)
	}
}

func TestDatabaseReservationStoreSnapshot(t *testing.T) {
	db := database.NewDatabase(t.TempDir())
	const deploymentID = "deployment-1"

	if err := db.SetDesiredState(deploymentID, database.AppDeploymentState{}); err != nil {
		t.Fatalf("SetDesiredState() error = %v", err)
	}
	if err := db.SetAllocations(deploymentID, database.Allocations{
		Cpus: map[string][]int{"comp1": {1, 2, 5}},
	}); err != nil {
		t.Fatalf("SetAllocations() error = %v", err)
	}

	isolated := map[int]struct{}{1: {}, 2: {}} // cpu 5 is not isolated
	store := NewDatabaseReservationStore(db, isolated)

	snapshot, err := store.LoadSnapshot()
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}

	wantOwner := model.NewOwnerRef(deploymentID, "comp1")
	if snapshot.CpuOwners[1] != wantOwner {
		t.Fatalf("snapshot.CpuOwners[1] = %v, want %v", snapshot.CpuOwners[1], wantOwner)
	}
	if snapshot.CpuOwners[2] != wantOwner {
		t.Fatalf("snapshot.CpuOwners[2] = %v, want %v", snapshot.CpuOwners[2], wantOwner)
	}
	if _, exists := snapshot.CpuOwners[5]; exists {
		t.Fatalf("snapshot.CpuOwners[5] should not exist (not isolated)")
	}
}

func TestDatabaseReservationStoreClearComponentPreservesSiblings(t *testing.T) {
	db := database.NewDatabase(t.TempDir())
	const deploymentID = "deployment-1"
	const componentName = "component"
	const siblingName = "sibling"

	if err := db.SetDesiredState(deploymentID, database.AppDeploymentState{}); err != nil {
		t.Fatalf("SetDesiredState() error = %v", err)
	}
	if err := db.SetAllocations(deploymentID, database.Allocations{
		Cpus: map[string][]int{componentName: {2}, siblingName: {4}},
	}); err != nil {
		t.Fatalf("SetAllocations() error = %v", err)
	}

	store := NewDatabaseReservationStore(db, map[int]struct{}{2: {}, 4: {}})
	if err := store.ClearComponent(model.NewOwnerRef(deploymentID, componentName)); err != nil {
		t.Fatalf("ClearComponent() error = %v", err)
	}

	allocations, err := db.GetAllocations(deploymentID)
	if err != nil {
		t.Fatalf("GetAllocations() error = %v", err)
	}
	if _, found := allocations.Cpus[componentName]; found {
		t.Fatalf("CPU assignment for %q was not cleared", componentName)
	}
	if got := allocations.Cpus[siblingName]; len(got) != 1 || got[0] != 4 {
		t.Fatalf("sibling CPU assignment = %#v, want [4]", got)
	}
}

func TestDatabaseReservationStoreCacheSupport(t *testing.T) {
	db := database.NewDatabase(t.TempDir())
	const deploymentID = "deployment-1"
	const componentName = "cyclictest"

	if err := db.SetDesiredState(deploymentID, database.AppDeploymentState{}); err != nil {
		t.Fatalf("SetDesiredState() error = %v", err)
	}

	owner := model.NewOwnerRef(deploymentID, componentName)
	store := NewDatabaseReservationStore(db, map[int]struct{}{2: {}, 4: {}})

	res := model.Reservation{
		Owner: owner,
		Cpus:  []int{2, 4},
		L3CacheAssignment: &model.CacheAssignment{
			Owner:   owner,
			Level:   "L3",
			CacheId: "0",
			SizeKiB: 2048,
			Mask:    "0xC",
			Clos:    model.ClosId("cos1"),
		},
	}

	if !res.HasL3Cache() {
		t.Fatal("expected res.HasCache() == true")
	}

	if err := store.SaveReservation(deploymentID, res); err != nil {
		t.Fatalf("SaveReservation() error = %v", err)
	}

	// LoadReservation asserts both CPU and Cache are reconstructed
	loaded, found, err := store.LoadReservation(owner)
	if err != nil {
		t.Fatalf("LoadReservation() error = %v", err)
	}
	if !found {
		t.Fatal("LoadReservation() found = false, want true")
	}
	if !loaded.HasL3Cache() {
		t.Fatal("expected loaded reservation to have cache")
	}
	if loaded.L3CacheAssignment == nil {
		t.Fatal("expected non-nil cache reservation")
	}
	cacheRes := *loaded.L3CacheAssignment
	if cacheRes.Clos != model.ClosId("cos1") {
		t.Fatalf("expected class 'cos1', got %s", cacheRes.Clos)
	}
	if cacheRes.CacheId != "0" || cacheRes.Mask != "0xC" {
		t.Fatalf("unexpected cache reservation ID/mask: %+v", cacheRes)
	}

	// LoadSnapshot populates AllocationSnapshot.Caches
	snapshot, err := store.LoadSnapshot()
	if err != nil {
		t.Fatalf("LoadSnapshot() error = %v", err)
	}
	if len(snapshot.Caches) != 1 {
		t.Fatalf("expected 1 cache in snapshot, got %d", len(snapshot.Caches))
	}
	snapCache := snapshot.Caches[0]
	if snapCache.Owner != owner || snapCache.Mask != "0xC" {
		t.Fatalf("unexpected snapshot cache: %+v", snapCache)
	}

	// ClearComponent clears both CPU and cache
	if err := store.ClearComponent(owner); err != nil {
		t.Fatalf("ClearComponent() error = %v", err)
	}

	_, foundAfter, err := store.LoadReservation(owner)
	if err != nil {
		t.Fatalf("LoadReservation after clear error = %v", err)
	}
	if foundAfter {
		t.Fatal("expected reservation to be cleared")
	}
}
