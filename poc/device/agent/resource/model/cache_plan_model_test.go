package model

import (
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
		Owner:    owner,
		Level:    "L3",
		CacheId:  "0",
		SizeKiB:  2048,
		Interval: WayInterval{Start: 0, Length: 2},
		Mask:     "0x3",
		Clos:     ClosId("1"),
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
