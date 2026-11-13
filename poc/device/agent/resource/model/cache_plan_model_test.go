package model

import (
	"math"
	"testing"
)

func TestWayInterval(t *testing.T) {
	iv := WayInterval{Start: 2, Length: 4}
	if got := iv.End(); got != 6 {
		t.Fatalf("expected End() == 6, got %d", got)
	}

	overlapping := WayInterval{Start: 5, Length: 2}
	if !iv.Overlaps(overlapping) {
		t.Fatalf("expected iv %+v to overlap %+v", iv, overlapping)
	}

	disjoint := WayInterval{Start: 6, Length: 2}
	if iv.Overlaps(disjoint) {
		t.Fatalf("expected iv %+v not to overlap %+v", iv, disjoint)
	}

	before := WayInterval{Start: 0, Length: 2}
	if iv.Overlaps(before) {
		t.Fatalf("expected iv %+v not to overlap %+v", iv, before)
	}
}

func TestWayIntervalMaskHex(t *testing.T) {
	tests := []struct {
		name    string
		iv      WayInterval
		want    string
		wantErr bool
	}{
		{
			name: "start 0 length 2",
			iv:   WayInterval{Start: 0, Length: 2},
			want: "0x3",
		},
		{
			name: "start 4 length 4",
			iv:   WayInterval{Start: 4, Length: 4},
			want: "0xF0",
		},
		{
			name: "full 64 ways",
			iv:   WayInterval{Start: 0, Length: MaxCacheWays},
			want: "0xFFFFFFFFFFFFFFFF",
		},
		{
			name: "highest bit 63",
			iv:   WayInterval{Start: 63, Length: 1},
			want: "0x8000000000000000",
		},
		{
			name:    "negative start",
			iv:      WayInterval{Start: -1, Length: 2},
			wantErr: true,
		},
		{
			name:    "zero length",
			iv:      WayInterval{Start: 0, Length: 0},
			wantErr: true,
		},
		{
			name:    "length exceeds max ways",
			iv:      WayInterval{Start: 0, Length: MaxCacheWays + 1},
			wantErr: true,
		},
		{
			name:    "interval end exceeds max ways",
			iv:      WayInterval{Start: 60, Length: 5},
			wantErr: true,
		},
		{
			name:    "overflow start with length 1",
			iv:      WayInterval{Start: math.MaxInt64, Length: 1},
			wantErr: true,
		},
		{
			name:    "overflow length with start 1",
			iv:      WayInterval{Start: 1, Length: math.MaxInt64},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.iv.MaskHex()
			if (err != nil) != tc.wantErr {
				t.Fatalf("WayInterval%+v.MaskHex() error = %v, wantErr %v", tc.iv, err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Fatalf("WayInterval%+v.MaskHex() = %s, want %s", tc.iv, got, tc.want)
			}
		})
	}

	t.Run("round trip with ParseContiguousWayMask", func(t *testing.T) {
		for start := int64(0); start < MaxCacheWays; start += 7 {
			for length := int64(1); start+length <= MaxCacheWays; length += 5 {
				iv := WayInterval{Start: start, Length: length}
				hex, err := iv.MaskHex()
				if err != nil {
					t.Fatalf("MaskHex() for %+v failed: %v", iv, err)
				}
				parsed, err := ParseContiguousWayMask(hex, MaxCacheWays)
				if err != nil {
					t.Fatalf("ParseContiguousWayMask(%q) failed: %v", hex, err)
				}
				if parsed != iv {
					t.Fatalf("round trip got %+v, want %+v", parsed, iv)
				}
			}
		}
	})
}

func TestParseContiguousWayMask(t *testing.T) {
	tests := []struct {
		name    string
		mask    string
		maxWays int64
		want    WayInterval
		wantErr bool
	}{
		{
			name:    "valid low bits 0x3",
			mask:    "0x3",
			maxWays: 12,
			want:    WayInterval{Start: 0, Length: 2},
		},
		{
			name:    "valid middle bits 0xF0",
			mask:    "0xF0",
			maxWays: 12,
			want:    WayInterval{Start: 4, Length: 4},
		},
		{
			name:    "valid high bits 0xF00",
			mask:    "0xF00",
			maxWays: 12,
			want:    WayInterval{Start: 8, Length: 4},
		},
		{
			name:    "non-contiguous mask 0xF00F rejected",
			mask:    "0xF00F",
			maxWays: 16,
			wantErr: true,
		},
		{
			name:    "non-contiguous mask 0x5 rejected",
			mask:    "0x5",
			maxWays: 12,
			wantErr: true,
		},
		{
			name:    "zero mask rejected",
			mask:    "0x0",
			maxWays: 12,
			wantErr: true,
		},
		{
			name:    "empty mask rejected",
			mask:    "",
			maxWays: 12,
			wantErr: true,
		},
		{
			name:    "exceeds maxWays rejected",
			mask:    "0x1000",
			maxWays: 12,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseContiguousWayMask(tc.mask, tc.maxWays)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseContiguousWayMask(%q, %d) error = %v, wantErr %v", tc.mask, tc.maxWays, err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Fatalf("ParseContiguousWayMask(%q, %d) = %+v, want %+v", tc.mask, tc.maxWays, got, tc.want)
			}
		})
	}
}

func TestFreeWayIntervals(t *testing.T) {
	tests := []struct {
		name string
		used []bool
		want []WayInterval
	}{
		{
			name: "empty array",
			used: nil,
			want: []WayInterval{},
		},
		{
			name: "all free",
			used: []bool{false, false, false, false},
			want: []WayInterval{{Start: 0, Length: 4}},
		},
		{
			name: "all used",
			used: []bool{true, true, true, true},
			want: []WayInterval{},
		},
		{
			name: "fragmented intervals",
			used: []bool{false, false, true, true, false, true, false, false, false},
			want: []WayInterval{
				{Start: 0, Length: 2},
				{Start: 4, Length: 1},
				{Start: 6, Length: 3},
			},
		},
		{
			name: "used at start and end",
			used: []bool{true, false, false, true},
			want: []WayInterval{{Start: 1, Length: 2}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := FreeWayIntervals(tc.used)
			if len(got) != len(tc.want) {
				t.Fatalf("expected %d intervals, got %d: %+v", len(tc.want), len(got), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("at index %d: expected %+v, got %+v", i, tc.want[i], got[i])
				}
			}
		})
	}
}

func TestClassID(t *testing.T) {
	if ClassUnset.Held() {
		t.Fatal("expected ClassUnset not to be held")
	}

	cid := ClosId("cos1")
	if !cid.Held() {
		t.Fatal("expected ClassID('cos1') to be held")
	}
	if cid.String() != "cos1" {
		t.Fatalf("expected String() 'cos1', got %s", cid.String())
	}
}

func TestClassPoolUsable(t *testing.T) {
	tests := []struct {
		pool     ClosPool
		expected int
	}{
		{pool: ClosPool{NumClos: 16, Reserved: 1}, expected: 15},
		{pool: ClosPool{NumClos: 4, Reserved: 4}, expected: 0},
		{pool: ClosPool{NumClos: 2, Reserved: 5}, expected: 0},
	}

	for _, tc := range tests {
		if got := tc.pool.Usable(); got != tc.expected {
			t.Fatalf("expected usable %d for pool %+v, got %d", tc.expected, tc.pool, got)
		}
	}
}

func TestCachePlan(t *testing.T) {
	emptyPlan := CachePlan{}
	if emptyPlan.HasCache() {
		t.Fatal("expected empty CachePlan not to have cache")
	}

	owner := NewOwnerRef("dep1", "comp1")
	assignment := CacheAssignment{
		Owner:   owner,
		Level:   "L3",
		CacheId: "0",
		SizeKiB: 2048,
		Masks:   map[string]string{"0": "0x3"},
		Clos:    ClosId("1"),
	}
	plan := CachePlan{
		Component:         ComponentRef("comp1"),
		L3CacheAssignment: &assignment,
	}

	if !plan.HasCache() {
		t.Fatal("expected plan with assignment to have cache")
	}
	if plan.L3CacheAssignment == nil {
		t.Fatal("expected non-nil assignment")
	}
	a := *plan.L3CacheAssignment
	if a.Owner != owner || a.Clos != ClosId("1") || a.CacheId != "0" || a.SizeKiB != 2048 {
		t.Fatalf("unexpected assignment fields: %+v", a)
	}
}
