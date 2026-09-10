package ledger

import (
	"fmt"
	"testing"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

type fakeClassNamer struct {
	names map[model.ComponentRef]model.ClosId
	err   error
}

func (f *fakeClassNamer) Name(ref model.ComponentRef, taken []model.ClosId) (model.ClosId, error) {
	if f.err != nil {
		return model.ClassUnset, f.err
	}
	if name, ok := f.names[ref]; ok {
		return name, nil
	}
	return model.ClosId(fmt.Sprintf("cos-%s", ref)), nil
}

func TestAllocationLedgerIsCpuAvailable(t *testing.T) {
	isolated := map[int]struct{}{1: {}, 2: {}, 3: {}, 4: {}}
	snapshot := NewAllocationSnapshot(map[int]string{
		1: "deployment-1/component-a",
		2: "deployment-1/component-b",
		3: "deployment-2/component-a",
		5: "deployment-2/component-a", // not isolated; never allocatable
	}, isolated)

	tests := []struct {
		name     string
		cpuIndex int
		want     bool
	}{
		{name: "unheld index is free", cpuIndex: 4, want: true},
		{name: "own persisted claim is reusable", cpuIndex: 1, want: true},
		{name: "sibling component blocks", cpuIndex: 2, want: false},
		{name: "another deployment blocks", cpuIndex: 3, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ledger := NewAllocationLedger(snapshot, "deployment-1")
			if got := ledger.IsCpuAvailable(test.cpuIndex, "component-a"); got != test.want {
				t.Fatalf("IsCpuAvailable(%d) = %v, want %v", test.cpuIndex, got, test.want)
			}
		})
	}
}

func TestAllocationLedgerReserveBlocksSiblingsInSamePass(t *testing.T) {
	ledger := NewAllocationLedger(NewAllocationSnapshot(nil, nil), "deployment-1")

	if err := ledger.ReserveCpus("component-a", []int{4}); err != nil {
		t.Fatalf("ReserveCpus() error = %v", err)
	}

	if ledger.IsCpuAvailable(4, "component-b") {
		t.Fatal("IsCpuAvailable() = true for a cpu reserved earlier in this pass")
	}
	if !ledger.IsCpuAvailable(4, "component-a") {
		t.Fatal("IsCpuAvailable() = false for the component that reserved the cpu")
	}
}

func TestAllocationLedgerCacheStubs(t *testing.T) {
	caps := model.CacheCapacity{
		Ways: map[string]int64{
			"0": 12,
		},
		ClosPool: model.ClosPool{NumClos: 8, Reserved: 1},
	}

	persistedCaches := []model.CacheAssignment{
		{
			Owner:    model.NewOwnerRef("deployment-1", "component-a"),
			Level:    "L3",
			CacheId:  "0",
			SizeKiB:  2048,
			Interval: model.WayInterval{Start: 0, Length: 2},
			Clos:     model.ClosId("1"),
		},
	}

	snapshot := NewAllocationSnapshotWithCaches(nil, nil, persistedCaches)
	ledger := NewAllocationLedger(snapshot, "deployment-1")
	ledger.SetCacheCapacity(caps)
	ledger.SetClassName(&fakeClassNamer{})

	// FreeWays stub returns nil in Phase 1
	if got := ledger.FreeWays("0", "component-a"); got != nil {
		t.Fatalf("FreeWays expected nil stub, got %+v", got)
	}

	// ReserveWays stub returns nil in Phase 1
	if err := ledger.ReserveWays("component-a", "0", model.WayInterval{Start: 0, Length: 2}); err != nil {
		t.Fatalf("ReserveWays expected nil error stub, got %v", err)
	}

	// ReserveClass stub returns ClassUnset in Phase 1
	cls, err := ledger.ReserveClass("component-a")
	if err != nil {
		t.Fatalf("ReserveClass expected nil error stub, got %v", err)
	}
	if cls != model.ClassUnset {
		t.Fatalf("ReserveClass expected ClassUnset, got %v", cls)
	}
}

func TestAllocationLedgerRollbackComponent(t *testing.T) {
	ledger := NewAllocationLedger(NewAllocationSnapshot(nil, nil), "deployment-1")

	if err := ledger.ReserveCpus("comp-a", []int{1, 2}); err != nil {
		t.Fatalf("ReserveCpus failed: %v", err)
	}

	// Assert CPU is taken
	if ledger.IsCpuAvailable(1, "comp-b") {
		t.Fatal("expected CPU 1 to be blocked for comp-b")
	}

	// Rollback comp-a
	ledger.RollbackComponent("comp-a")

	// Assert CPU is released
	if !ledger.IsCpuAvailable(1, "comp-b") {
		t.Fatal("expected CPU 1 to be available after rollback")
	}
}
