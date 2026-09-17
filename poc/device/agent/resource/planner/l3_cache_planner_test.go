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

func (f *testClassNamer) Name(ref model.ComponentRef, taken []model.ClosId) (model.ClosId, error) {
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

func TestMaskWays(t *testing.T) {
	tests := []struct {
		name    string
		mask    string
		maxWays int64
		want    []int64
	}{
		{
			name:    "mask 0x3",
			mask:    "0x3",
			maxWays: 12,
			want:    []int64{0, 1},
		},
		{
			name:    "mask 0xF0",
			mask:    "0xF0",
			maxWays: 12,
			want:    []int64{4, 5, 6, 7},
		},
		{
			name:    "invalid mask",
			mask:    "not-a-mask",
			maxWays: 12,
			want:    nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := maskWays(tc.mask, tc.maxWays)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("maskWays() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestWayMaskHexForInterval(t *testing.T) {
	tests := []struct {
		name    string
		start   int64
		length  int64
		want    string
		wantErr bool
	}{
		{
			name:   "start 0 length 2",
			start:  0,
			length: 2,
			want:   "0x3",
		},
		{
			name:   "start 4 length 4",
			start:  4,
			length: 4,
			want:   "0xF0",
		},
		{
			name:    "negative start",
			start:   -1,
			length:  2,
			wantErr: true,
		},
		{
			name:    "zero length",
			start:   0,
			length:  0,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := wayMaskHexForInterval(tc.start, tc.length)
			if (err != nil) != tc.wantErr {
				t.Fatalf("wayMaskHexForInterval() error = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Fatalf("wayMaskHexForInterval() = %s, want %s", got, tc.want)
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
			Owner:    model.NewOwnerRef("other", "other"),
			Level:    "L3",
			CacheId:  "0",
			Interval: model.WayInterval{Start: 6, Length: 6},
		},
		{
			Owner:    model.NewOwnerRef("other", "other"),
			Level:    "L3",
			CacheId:  "1",
			Interval: model.WayInterval{Start: 4, Length: 8},
		},
	}

	snap := ledger.NewAllocationSnapshot(nil, nil, persisted)
	l := ledger.NewAllocationLedger(snap, "deployment-1", caps, &testClassNamer{})

	// Required 2048 KiB -> 2 ways
	// Cache 1 has free run length 4, Cache 0 has free run length 6
	// Best-fit picks Cache 1 (length 4 < length 6)
	bestCache, bestInterval, neededWays, err := pickSmallestFittingCacheInterval(caches, l, "comp-a", 2048)
	if err != nil {
		t.Fatalf("pickSmallestFittingCacheInterval() error = %v", err)
	}
	if bestCache.Id != "1" {
		t.Fatalf("expected Cache 1, got %s", bestCache.Id)
	}
	if neededWays != 2 {
		t.Fatalf("expected 2 needed ways, got %d", neededWays)
	}
	if bestInterval.Start != 0 || bestInterval.Length != 2 {
		t.Fatalf("expected interval [0, 2), got %+v", bestInterval)
	}

	// Required 5120 KiB -> 5 ways
	// Cache 1 cannot fit 5 ways (only 4 free), Cache 0 fits (6 free)
	bestCache, bestInterval, neededWays, err = pickSmallestFittingCacheInterval(caches, l, "comp-a", 5120)
	if err != nil {
		t.Fatalf("pickSmallestFittingCacheInterval() error = %v", err)
	}
	if bestCache.Id != "0" {
		t.Fatalf("expected Cache 0, got %s", bestCache.Id)
	}
	if neededWays != 5 {
		t.Fatalf("expected 5 needed ways, got %d", neededWays)
	}
	if bestInterval.Start != 0 || bestInterval.Length != 5 {
		t.Fatalf("expected interval [0, 5), got %+v", bestInterval)
	}

	// Required 8192 KiB -> 8 ways -> neither fits
	_, _, _, err = pickSmallestFittingCacheInterval(caches, l, "comp-a", 8192)
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
		if plan.Clos != model.ClassUnset {
			t.Fatalf("expected ClassUnset, got %v", plan.Clos)
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
		if plan.Clos != "cos-1" {
			t.Fatalf("expected clos 'cos-1', got %s", plan.Clos)
		}
		if plan.L3CacheAssignment.CacheId != "0" {
			t.Fatalf("expected cacheId '0', got %s", plan.L3CacheAssignment.CacheId)
		}
		if plan.L3CacheAssignment.Interval.Start != 0 || plan.L3CacheAssignment.Interval.Length != 2 {
			t.Fatalf("expected interval [0, 2), got %+v", plan.L3CacheAssignment.Interval)
		}
		if plan.L3CacheAssignment.Mask != "0x3" {
			t.Fatalf("expected mask 0x3, got %s", plan.L3CacheAssignment.Mask)
		}
	})

	t.Run("capacity exhaustion", func(t *testing.T) {
		persisted := []model.CacheAssignment{
			{
				Owner:    model.NewOwnerRef("other", "other"),
				Level:    "L3",
				CacheId:  "0",
				Interval: model.WayInterval{Start: 0, Length: 12},
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
}

func TestParseCpuRangeList(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    []int
		wantErr bool
	}{
		{
			name: "empty string",
			raw:  "",
			want: nil,
		},
		{
			name: "single cpu",
			raw:  "3",
			want: []int{3},
		},
		{
			name: "comma separated cpus with duplicates",
			raw:  "0, 4, 2, 4, 0",
			want: []int{0, 2, 4},
		},
		{
			name: "range",
			raw:  "0-3",
			want: []int{0, 1, 2, 3},
		},
		{
			name: "mixed ranges and singles with whitespace",
			raw:  "0-2, 6, 4-5, 2",
			want: []int{0, 1, 2, 4, 5, 6},
		},
		{
			name:    "invalid range start",
			raw:     "a-3",
			wantErr: true,
		},
		{
			name:    "invalid range end",
			raw:     "0-b",
			wantErr: true,
		},
		{
			name:    "invalid inverted range",
			raw:     "4-2",
			wantErr: true,
		},
		{
			name:    "invalid negative index",
			raw:     "-1",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCpuRangeList(tc.raw)
			if (err != nil) != tc.wantErr {
				t.Fatalf("parseCpuRangeList(%q) error = %v, wantErr %v", tc.raw, err, tc.wantErr)
			}
			if !tc.wantErr && !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseCpuRangeList(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}
