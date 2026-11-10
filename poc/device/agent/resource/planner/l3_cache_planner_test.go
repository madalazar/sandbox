package planner

import (
	"errors"
	"reflect"
	"testing"

	"github.com/margo/sandbox/poc/device/agent/resource/ledger"
	"github.com/margo/sandbox/poc/device/agent/resource/model"
	"github.com/margo/sandbox/poc/device/agent/types"
)

type testClassNamer struct {
	name model.ClosId
	err  error
}

func (f *testClassNamer) Name(owner model.OwnerRef, taken []model.ClosId) (model.ClosId, error) {
	if f.err != nil {
		return model.ClassUnset, f.err
	}
	if f.name != "" {
		return f.name, nil
	}
	return model.ClosId("1"), nil
}

func TestL3CachePlannerInterface(t *testing.T) {
	var _ CachePlanner = (*L3CachePlanner)(nil)

	planner := NewL3CachePlanner([]types.HostTopologyCache{})
	if planner == nil {
		t.Fatal("expected non-nil L3CachePlanner")
	}
}

func TestFilterL3CachesByAssignedCPUs(t *testing.T) {
	caches := []types.HostTopologyCache{
		{Id: "0", Level: "L3", Cores: "0-3,8-11", Ways: 12, WaySizeKB: 1024},
		{Id: "1", Level: "L3", Cores: "4-7,12-15", Ways: 12, WaySizeKB: 1024},
	}

	tests := []struct {
		name         string
		assignedCPUs []int
		wantIDs      []string
		wantErr      bool
	}{
		{
			name:         "empty assigned CPUs returns all caches",
			assignedCPUs: nil,
			wantIDs:      []string{"0", "1"},
		},
		{
			name:         "matching cache 0",
			assignedCPUs: []int{2, 3},
			wantIDs:      []string{"0"},
		},
		{
			name:         "matching both caches",
			assignedCPUs: []int{2, 5},
			wantIDs:      []string{"0", "1"},
		},
		{
			name:         "no matching cache returns error",
			assignedCPUs: []int{99},
			wantErr:      true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := filterL3CachesByAssignedCpus(caches, tc.assignedCPUs)
			if (err != nil) != tc.wantErr {
				t.Fatalf("filterL3CachesByAssignedCPUs() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			var gotIDs []string
			for _, c := range got {
				gotIDs = append(gotIDs, c.Id)
			}
			if !reflect.DeepEqual(gotIDs, tc.wantIDs) {
				t.Fatalf("got IDs %v, want %v", gotIDs, tc.wantIDs)
			}
		})
	}
}

func TestPickSmallestFittingCacheInterval(t *testing.T) {
	caches := []types.HostTopologyCache{
		{Id: "0", Level: "L3", Ways: 12, WaySizeKB: 1024},
		{Id: "1", Level: "L3", Ways: 12, WaySizeKB: 1024},
	}

	// Cache 0 has free interval of length 6 [0, 6)
	// Cache 1 has free interval of length 4 [0, 4)
	caps := model.CacheCapacity{
		Ways:     map[string]int64{"0": 12, "1": 12},
		ClosPool: model.ClosPool{NumClos: 8, Reserved: 1},
	}

	persisted := []model.CacheAssignment{
		{
			Owner:   model.NewOwnerRef("other", "other"),
			Level:   "L3",
			CacheId: "0",
			Masks:   map[string]string{"0": "0xfc0"}, // ways [6, 12)
		},
		{
			Owner:   model.NewOwnerRef("other", "other"),
			Level:   "L3",
			CacheId: "1",
			Masks:   map[string]string{"1": "0xff0"}, // ways [4, 12)
		},
	}

	snap := ledger.NewAllocationSnapshot(nil, nil, persisted)
	l := ledger.NewAllocationLedger(snap, "deployment-1", caps, &testClassNamer{})

	// Required 2048 KiB -> 2 ways
	// Cache 1 has free run length 4, Cache 0 has free run length 6
	// Best-fit picks Cache 1 (length 4 < length 6)
	bestCache, bestInterval, err := pickSmallestFittingCacheInterval(caches, l, "comp-a", 2048)
	if err != nil {
		t.Fatalf("pickSmallestFittingCacheInterval() error = %v", err)
	}
	if bestCache.Id != "1" {
		t.Fatalf("expected Cache 1, got %s", bestCache.Id)
	}
	if bestInterval.Start != 0 || bestInterval.Length != 2 {
		t.Fatalf("expected interval [0, 2), got %+v", bestInterval)
	}

	// Required 5120 KiB -> 5 ways
	// Cache 1 cannot fit 5 ways (only 4 free), Cache 0 fits (6 free)
	bestCache, bestInterval, err = pickSmallestFittingCacheInterval(caches, l, "comp-a", 5120)
	if err != nil {
		t.Fatalf("pickSmallestFittingCacheInterval() error = %v", err)
	}
	if bestCache.Id != "0" {
		t.Fatalf("expected Cache 0, got %s", bestCache.Id)
	}
	if bestInterval.Start != 0 || bestInterval.Length != 5 {
		t.Fatalf("expected interval [0, 5), got %+v", bestInterval)
	}

	// Required 8192 KiB -> 8 ways -> neither fits
	_, _, err = pickSmallestFittingCacheInterval(caches, l, "comp-a", 8192)
	if err == nil || !errors.Is(err, ledger.ErrCapacityExhausted) {
		t.Fatalf("expected ErrCapacityExhausted, got %v", err)
	}
}

func TestL3CachePlannerPlanCache(t *testing.T) {
	caches := []types.HostTopologyCache{
		{Id: "0", Level: "L3", Cores: "0-3", Ways: 12, WaySizeKB: 1024},
	}
	planner := NewL3CachePlanner(caches)

	caps := model.CacheCapacity{
		Ways:     map[string]int64{"0": 12},
		ClosPool: model.ClosPool{NumClos: 8, Reserved: 1},
	}

	t.Run("no cache requirements", func(t *testing.T) {
		l := ledger.NewAllocationLedger(ledger.NewAllocationSnapshot(nil, nil, nil), "dep-1", caps, &testClassNamer{})

		plan, err := planner.PlanCache(CachePlanningRequest{
			Requirements: model.NormalizedCacheRequirements{Component: "comp-a"},
			CpuPlan:      model.CpuPlan{Component: "comp-a", Cpus: []int{0}},
			Ledger:       l,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if plan.HasCache() {
			t.Fatal("expected HasCache() = false")
		}
	})

	t.Run("successful cache planning", func(t *testing.T) {
		l := ledger.NewAllocationLedger(ledger.NewAllocationSnapshot(nil, nil, nil), "dep-1", caps, &testClassNamer{name: "cos-1"})

		req := model.NormalizedCacheRequirements{
			Component: "comp-a",
			L3CacheRequirement: &model.CacheRequirement{
				Level:      "L3",
				Allocation: model.CacheAllocationExclusive,
				SizeKiB:    2048,
			},
		}

		plan, err := planner.PlanCache(CachePlanningRequest{
			Requirements: req,
			CpuPlan:      model.CpuPlan{Component: "comp-a", Cpus: []int{1}},
			Ledger:       l,
		})
		if err != nil {
			t.Fatalf("PlanCache failed: %v", err)
		}
		if !plan.HasCache() {
			t.Fatal("expected HasCache() = true")
		}
		if plan.L3CacheAssignment.CacheId != "0" {
			t.Fatalf("expected cacheId '0', got %s", plan.L3CacheAssignment.CacheId)
		}
		if plan.L3CacheAssignment.Masks["0"] != "0x3" {
			t.Fatalf("expected mask 0x3, got %s", plan.L3CacheAssignment.Masks["0"])
		}
	})

	t.Run("capacity exhaustion", func(t *testing.T) {
		persisted := []model.CacheAssignment{
			{
				Owner:   model.NewOwnerRef("other", "other"),
				Level:   "L3",
				CacheId: "0",
				Masks:   map[string]string{"0": "0xfff"}, // ways [0, 12)
			},
		}
		snap := ledger.NewAllocationSnapshot(nil, nil, persisted)
		l := ledger.NewAllocationLedger(snap, "dep-1", caps, &testClassNamer{})

		req := model.NormalizedCacheRequirements{
			Component: "comp-a",
			L3CacheRequirement: &model.CacheRequirement{
				Level:      "L3",
				Allocation: model.CacheAllocationExclusive,
				SizeKiB:    2048,
			},
		}

		_, err := planner.PlanCache(CachePlanningRequest{
			Requirements: req,
			CpuPlan:      model.CpuPlan{Component: "comp-a", Cpus: []int{1}},
			Ledger:       l,
		})
		if err == nil || !errors.Is(err, ledger.ErrCapacityExhausted) {
			t.Fatalf("expected ErrCapacityExhausted, got %v", err)
		}
	})

	t.Run("rdt multi-cache masks do not overlap across deployments", func(t *testing.T) {
		multiCaches := []types.HostTopologyCache{
			{Id: "0", Level: "L3", Cores: "0-3", Ways: 12, WaySizeKB: 2304},
			{Id: "1", Level: "L3", Cores: "4-7", Ways: 12, WaySizeKB: 2304},
		}
		multiCaps := model.CacheCapacity{
			Ways:     map[string]int64{"0": 12, "1": 12},
			ClosPool: model.ClosPool{NumClos: 8, Reserved: 1},
		}
		multiPlanner := NewL3CachePlanner(multiCaches)

		// Component 2 requests 578 KiB on L3 "0" -> 1 way (0x1), filler on L3 "1" -> 0x1
		l1 := ledger.NewAllocationLedger(ledger.NewAllocationSnapshot(nil, nil, nil), "e32acdad", multiCaps, &testClassNamer{name: "part2_e32acdad_class"})
		plan2, err := multiPlanner.PlanCache(CachePlanningRequest{
			Requirements: model.NormalizedCacheRequirements{
				Component: "part2",
				L3CacheRequirement: &model.CacheRequirement{
					Level:      "L3",
					Allocation: model.CacheAllocationExclusive,
					SizeKiB:    578,
				},
			},
			CpuPlan: model.CpuPlan{
				Component: "part2",
				Cpus:      []int{1},
				Placement: model.CpuPlacement{Class: "ipc1"},
			},
			Ledger: l1,
		})
		if err != nil {
			t.Fatalf("PlanCache for part2 failed: %v", err)
		}
		if plan2.L3CacheAssignment.Masks["0"] != "0x1" || plan2.L3CacheAssignment.Masks["1"] != "0x1" {
			t.Fatalf("unexpected part2 masks: %v", plan2.L3CacheAssignment.Masks)
		}

		// Persist part2 and plan Component 1 requesting 9216 KiB on L3 "0" -> 4 ways (0x1E), filler on L3 "1" -> 0x2
		persistedPart2 := *plan2.L3CacheAssignment
		persistedPart2.Owner = model.NewOwnerRef("e32acdad", "part2")
		snap2 := ledger.NewAllocationSnapshot(nil, nil, []model.CacheAssignment{persistedPart2})
		l2 := ledger.NewAllocationLedger(snap2, "99b59365", multiCaps, &testClassNamer{name: "part1_99b59365_class"})

		plan1, err := multiPlanner.PlanCache(CachePlanningRequest{
			Requirements: model.NormalizedCacheRequirements{
				Component: "part1",
				L3CacheRequirement: &model.CacheRequirement{
					Level:      "L3",
					Allocation: model.CacheAllocationExclusive,
					SizeKiB:    9216,
				},
			},
			CpuPlan: model.CpuPlan{
				Component: "part1",
				Cpus:      []int{2},
				Placement: model.CpuPlacement{Class: "ipc2"},
			},
			Ledger: l2,
		})
		if err != nil {
			t.Fatalf("PlanCache for part1 failed: %v", err)
		}
		if plan1.L3CacheAssignment.Masks["0"] != "0x1E" {
			t.Fatalf("expected part1 mask 0x1E on cache 0, got %s", plan1.L3CacheAssignment.Masks["0"])
		}
		if plan1.L3CacheAssignment.Masks["1"] != "0x2" {
			t.Fatalf("expected part1 non-overlapping filler mask 0x2 on cache 1, got %s", plan1.L3CacheAssignment.Masks["1"])
		}

		// Compose (no Placement.Class) on multi-cache only populates 1 entry in Masks
		composePlan, err := multiPlanner.PlanCache(CachePlanningRequest{
			Requirements: model.NormalizedCacheRequirements{
				Component: "compose-comp",
				L3CacheRequirement: &model.CacheRequirement{
					Level:      "L3",
					Allocation: model.CacheAllocationExclusive,
					SizeKiB:    2304,
				},
			},
			CpuPlan: model.CpuPlan{
				Component: "compose-comp",
				Cpus:      []int{3},
			},
			Ledger: l2,
		})
		if err != nil {
			t.Fatalf("PlanCache for compose-comp failed: %v", err)
		}
		if len(composePlan.L3CacheAssignment.Masks) != 1 || composePlan.L3CacheAssignment.Masks["0"] != "0x2" {
			t.Fatalf("expected single mask entry 0x2 on cache 0 for compose, got %v", composePlan.L3CacheAssignment.Masks)
		}
	})
}
