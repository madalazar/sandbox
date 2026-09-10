package database

import (
	"os"
	"reflect"
	"testing"
)

func TestAllocationsClone(t *testing.T) {
	orig := Allocations{
		Cpus: map[string][]int{
			"comp1": {1, 2},
		},
		Caches: map[string]CacheAllocation{
			"comp1": {
				ComponentName: "comp1",
				Level:         "L3",
				CacheID:       "0",
				SizeKB:        2048,
				Mask:          "0x3",
				Clos:          "1",
			},
		},
	}

	cloned := orig.clone()
	if !reflect.DeepEqual(orig, cloned) {
		t.Fatalf("expected cloned to equal orig, got %+v", cloned)
	}

	// Mutate clone and assert original is unmodified
	cloned.Cpus["comp1"][0] = 99
	entry := cloned.Caches["comp1"]
	entry.Clos = "99"
	cloned.Caches["comp1"] = entry
	if orig.Cpus["comp1"][0] == 99 {
		t.Fatal("expected original Cpus not to be modified")
	}
	if orig.Caches["comp1"].Clos == "99" {
		t.Fatal("expected original Caches not to be modified")
	}
}

func TestDatabaseAllocationsLifecycle(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-db-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	db := NewDatabase(tmpDir)
	deploymentID := "dep-1"

	err = db.SetDesiredState(deploymentID, AppDeploymentState{
		AppId: "test-app",
	})
	if err != nil {
		t.Fatalf("SetDesiredState failed: %v", err)
	}

	allocs := Allocations{
		Cpus: map[string][]int{
			"comp-a": {2, 3},
			"comp-b": {4, 5},
		},
		Caches: map[string]CacheAllocation{
			"comp-a": {
				ComponentName: "comp-a",
				Level:         "L3",
				CacheID:       "0",
				SizeKB:        4096,
				Mask:          "0xf",
				Clos:          "2",
			},
		},
	}

	if err := db.SetAllocations(deploymentID, allocs); err != nil {
		t.Fatalf("SetAllocations failed: %v", err)
	}

	gotAllocs, err := db.GetAllocations(deploymentID)
	if err != nil {
		t.Fatalf("GetAllocations failed: %v", err)
	}
	if !reflect.DeepEqual(gotAllocs, allocs) {
		t.Fatalf("expected allocations %+v, got %+v", allocs, gotAllocs)
	}

	// Check AllocatedCpus
	cpus := db.AllocatedCpus()
	if cpus[2] != "dep-1/comp-a" || cpus[3] != "dep-1/comp-a" {
		t.Fatalf("unexpected AllocatedCpus for comp-a: %+v", cpus)
	}
	if cpus[4] != "dep-1/comp-b" || cpus[5] != "dep-1/comp-b" {
		t.Fatalf("unexpected AllocatedCpus for comp-b: %+v", cpus)
	}

	// Check AllocatedCaches
	caches := db.AllocatedCaches()
	if len(caches) != 1 {
		t.Fatalf("expected 1 allocated cache, got %d", len(caches))
	}
	expectedAlloc := CacheAllocation{
		Owner:         "dep-1/comp-a",
		ComponentName: "comp-a",
		Level:         "L3",
		CacheID:       "0",
		SizeKB:        4096,
		Mask:          "0xf",
		Clos:          "2",
	}
	if !reflect.DeepEqual(caches[0], expectedAlloc) {
		t.Fatalf("expected allocated cache %+v, got %+v", expectedAlloc, caches[0])
	}

	// Clear comp-a
	if err := db.ClearComponentAllocations(deploymentID, "comp-a"); err != nil {
		t.Fatalf("ClearComponentAllocations failed: %v", err)
	}

	afterClear, err := db.GetAllocations(deploymentID)
	if err != nil {
		t.Fatalf("GetAllocations after clear failed: %v", err)
	}
	if _, exists := afterClear.Cpus["comp-a"]; exists {
		t.Fatal("expected comp-a CPUs to be cleared")
	}
	if _, exists := afterClear.Caches["comp-a"]; exists {
		t.Fatal("expected comp-a Caches to be cleared")
	}
	if len(afterClear.Cpus["comp-b"]) != 2 {
		t.Fatalf("expected comp-b CPUs to remain, got %+v", afterClear.Cpus["comp-b"])
	}

	// Assert AllocatedCaches is now empty
	if len(db.AllocatedCaches()) != 0 {
		t.Fatalf("expected 0 allocated caches after clear, got %d", len(db.AllocatedCaches()))
	}
}
