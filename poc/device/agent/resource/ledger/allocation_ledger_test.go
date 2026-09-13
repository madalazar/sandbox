package ledger

import (
	"errors"
	"fmt"
	"reflect"
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
	}, isolated, nil)

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
			ledger := NewAllocationLedger(snapshot, "deployment-1", model.CacheCapacity{}, nil)
			if got := ledger.IsCpuAvailable(test.cpuIndex, "component-a"); got != test.want {
				t.Fatalf("IsCpuAvailable(%d) = %v, want %v", test.cpuIndex, got, test.want)
			}
		})
	}
}

func TestAllocationLedgerReserveBlocksSiblingsInSamePass(t *testing.T) {
	ledger := NewAllocationLedger(NewAllocationSnapshot(nil, nil, nil), "deployment-1", model.CacheCapacity{}, nil)

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

func TestAllocationLedgerFreeWaysAndReserveWays(t *testing.T) {
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
		{
			Owner:    model.NewOwnerRef("deployment-1", "component-b"),
			Level:    "L3",
			CacheId:  "0",
			SizeKiB:  2048,
			Interval: model.WayInterval{Start: 2, Length: 2},
			Clos:     model.ClosId("2"),
		},
		{
			Owner:    model.NewOwnerRef("deployment-2", "component-c"),
			Level:    "L3",
			CacheId:  "0",
			SizeKiB:  2048,
			Interval: model.WayInterval{Start: 8, Length: 4},
			Clos:     model.ClosId("3"),
		},
	}

	snapshot := NewAllocationSnapshot(nil, nil, persistedCaches)
	ledger := NewAllocationLedger(snapshot, "deployment-1", caps, &fakeClassNamer{})

	// For component-a:
	// - [0, 2) is self-owned persisted -> reusable!
	// - [2, 4) is owned by sibling component-b -> blocked
	// - [4, 8) is unheld -> free
	// - [8, 12) is owned by deployment-2 -> blocked
	// Expected free intervals for component-a: [0, 2) and [4, 8)
	freeA := ledger.FreeWays("0", "component-a")
	wantFreeA := []model.WayInterval{
		{Start: 0, Length: 2},
		{Start: 4, Length: 4},
	}
	if !reflect.DeepEqual(freeA, wantFreeA) {
		t.Fatalf("FreeWays for component-a got %+v, want %+v", freeA, wantFreeA)
	}

	// For a new component-new in deployment-1:
	// - [0, 4) blocked by component-a and component-b
	// - [4, 8) free
	// - [8, 12) blocked by deployment-2
	freeNew := ledger.FreeWays("0", "component-new")
	wantFreeNew := []model.WayInterval{
		{Start: 4, Length: 4},
	}
	if !reflect.DeepEqual(freeNew, wantFreeNew) {
		t.Fatalf("FreeWays for component-new got %+v, want %+v", freeNew, wantFreeNew)
	}

	// Reserve ways in this pass for component-new
	err := ledger.ReserveWays("component-new", "0", model.WayInterval{Start: 4, Length: 2})
	if err != nil {
		t.Fatalf("ReserveWays failed: %v", err)
	}

	// Sibling component-newer now only has [6, 8) free
	freeNewer := ledger.FreeWays("0", "component-newer")
	wantFreeNewer := []model.WayInterval{
		{Start: 6, Length: 2},
	}
	if !reflect.DeepEqual(freeNewer, wantFreeNewer) {
		t.Fatalf("FreeWays for component-newer after reserve got %+v, want %+v", freeNewer, wantFreeNewer)
	}

	// Attempting to reserve overlapping ways fails
	err = ledger.ReserveWays("component-newer", "0", model.WayInterval{Start: 5, Length: 2})
	if err == nil || !errors.Is(err, ErrCapacityExhausted) {
		t.Fatalf("expected ErrCapacityExhausted on overlapping reserve, got %v", err)
	}
}

func TestAllocationLedgerReserveClass(t *testing.T) {
	caps := model.CacheCapacity{
		Ways: map[string]int64{"0": 8},
		// Usable classes = 3 (4 - 1)
		ClosPool: model.ClosPool{NumClos: 4, Reserved: 1},
	}

	persistedCaches := []model.CacheAssignment{
		{
			Owner: model.NewOwnerRef("deployment-1", "comp-a"),
			Clos:  model.ClosId("1"),
		},
		{
			Owner: model.NewOwnerRef("deployment-2", "comp-b"),
			Clos:  model.ClosId("2"),
		},
	}

	snapshot := NewAllocationSnapshot(nil, nil, persistedCaches)

	namer := &fakeClassNamer{
		names: map[model.ComponentRef]model.ClosId{
			"comp-a": "1",
			"comp-c": "3",
			"comp-d": "4",
		},
	}

	ledger := NewAllocationLedger(snapshot, "deployment-1", caps, namer)

	// comp-a reuses its own class slot
	clsA, err := ledger.ReserveClass("comp-a")
	if err != nil {
		t.Fatalf("ReserveClass for comp-a failed: %v", err)
	}
	if clsA != model.ClosId("1") {
		t.Fatalf("expected comp-a to reuse class '1', got %s", clsA)
	}

	// comp-c takes the last available slot (3 out of 3)
	clsC, err := ledger.ReserveClass("comp-c")
	if err != nil {
		t.Fatalf("ReserveClass for comp-c failed: %v", err)
	}
	if clsC != model.ClosId("3") {
		t.Fatalf("expected comp-c to get '3', got %s", clsC)
	}

	// comp-d fails due to class exhaustion
	_, err = ledger.ReserveClass("comp-d")
	if err == nil || !errors.Is(err, ErrCapacityExhausted) {
		t.Fatalf("expected ErrCapacityExhausted for comp-d, got %v", err)
	}
}

func TestAllocationLedgerRollbackComponent(t *testing.T) {

	caps := model.CacheCapacity{
		Ways:     map[string]int64{"0": 12},
		ClosPool: model.ClosPool{NumClos: 8, Reserved: 1},
	}
	ledger := NewAllocationLedger(NewAllocationSnapshot(nil, nil, nil), "deployment-1", caps, &fakeClassNamer{})

	if err := ledger.ReserveCpus("comp-a", []int{1, 2}); err != nil {
		t.Fatalf("ReserveCpus failed: %v", err)
	}
	if err := ledger.ReserveWays("comp-a", "0", model.WayInterval{Start: 0, Length: 4}); err != nil {
		t.Fatalf("ReserveWays failed: %v", err)
	}
	cls, err := ledger.ReserveClass("comp-a")
	if err != nil || cls == model.ClassUnset {
		t.Fatalf("ReserveClass failed: %v", err)
	}

	// Assert CPU, way, class taken
	if ledger.IsCpuAvailable(1, "comp-b") {
		t.Fatal("expected CPU 1 to be blocked for comp-b")
	}
	freeWaysBefore := ledger.FreeWays("0", "comp-b")
	if len(freeWaysBefore) != 1 || freeWaysBefore[0].Start != 4 {
		t.Fatalf("expected free ways [4, 12) for comp-b, got %+v", freeWaysBefore)
	}

	// Rollback comp-a
	ledger.RollbackComponent("comp-a")

	// Assert CPU is released
	if !ledger.IsCpuAvailable(1, "comp-b") {
		t.Fatal("expected CPU 1 to be available after rollback")
	}
	freeWaysAfter := ledger.FreeWays("0", "comp-b")
	if len(freeWaysAfter) != 1 || freeWaysAfter[0].Start != 0 || freeWaysAfter[0].Length != 12 {
		t.Fatalf("expected all 12 ways free for comp-b after rollback, got %+v", freeWaysAfter)
	}
}
