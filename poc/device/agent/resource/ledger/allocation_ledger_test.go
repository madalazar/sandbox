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

func (f *fakeClassNamer) Name(owner model.OwnerRef, taken []model.ClosId) (model.ClosId, error) {
	if f.err != nil {
		return model.ClassUnset, f.err
	}
	if name, ok := f.names[owner.Component]; ok {
		return name, nil
	}
	return model.ClosId(fmt.Sprintf("cos-%s", owner.Component)), nil
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

func TestAllocationLedgerFreeWays(t *testing.T) {
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

	// Verify persisted mask parsing: contiguous mask marks ways used, non-contiguous mask (e.g. 0xF00F) is ignored
	maskSnapshot := NewAllocationSnapshot(nil, nil, []model.CacheAssignment{
		{
			Owner:   model.NewOwnerRef("deployment-2", "comp-valid-mask"),
			CacheId: "0",
			Mask:    "0x0F", // ways [0, 4)
		},
		{
			Owner:   model.NewOwnerRef("deployment-3", "comp-non-contiguous"),
			CacheId: "0",
			Mask:    "0xF00F", // non-contiguous / invalid -> ignored
		},
	})
	maskLedger := NewAllocationLedger(maskSnapshot, "deployment-1", caps, &fakeClassNamer{})
	freeWithMasks := maskLedger.FreeWays("0", "component-a")
	wantFreeWithMasks := []model.WayInterval{
		{Start: 4, Length: 8},
	}
	if !reflect.DeepEqual(freeWithMasks, wantFreeWithMasks) {
		t.Fatalf("FreeWays with persisted masks got %+v, want %+v", freeWithMasks, wantFreeWithMasks)
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

	// Once comp-c's reservation is in the snapshot, comp-d fails due to class exhaustion
	exhaustedSnapshot := NewAllocationSnapshot(nil, nil, append(persistedCaches, model.CacheAssignment{
		Owner: model.NewOwnerRef("deployment-1", "comp-c"),
		Clos:  clsC,
	}))
	exhaustedLedger := NewAllocationLedger(exhaustedSnapshot, "deployment-1", caps, namer)
	_, err = exhaustedLedger.ReserveClass("comp-d")
	if err == nil || !errors.Is(err, ErrCapacityExhausted) {
		t.Fatalf("expected ErrCapacityExhausted for comp-d, got %v", err)
	}
}
