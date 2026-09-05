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
	cpus := map[string][]int{"comp1": {1, 2}}
	if err := store.SaveAllocations(deploymentID, cpus); err != nil {
		t.Fatalf("SaveAllocations() error = %v", err)
	}

	allocations, err := db.GetAllocations(deploymentID)
	if err != nil {
		t.Fatalf("GetAllocations() error = %v", err)
	}
	if !reflect.DeepEqual(allocations.Cpus, cpus) {
		t.Fatalf("GetAllocations() cpus = %#v, want %#v", allocations.Cpus, cpus)
	}

	// Saving allocations for another component preserves existing component allocations
	cpus2 := map[string][]int{"comp2": {3}}
	if err := store.SaveAllocations(deploymentID, cpus2); err != nil {
		t.Fatalf("SaveAllocations() error = %v", err)
	}

	allocations2, err := db.GetAllocations(deploymentID)
	if err != nil {
		t.Fatalf("GetAllocations() error = %v", err)
	}
	wantMerged := map[string][]int{"comp1": {1, 2}, "comp2": {3}}
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

	snapshot, err := store.Snapshot()
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
