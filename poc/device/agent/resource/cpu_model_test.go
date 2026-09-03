package resource

import (
	"reflect"
	"testing"
)

func TestFormatCpuSet(t *testing.T) {
	tests := []struct {
		name string
		cpus []int
		want string
	}{
		{name: "no cpus", cpus: nil, want: ""},
		{name: "single cpu", cpus: []int{3}, want: "3"},
		{name: "contiguous run collapses", cpus: []int{2, 3, 4}, want: "2-4"},
		{name: "disjoint indices stay listed", cpus: []int{1, 3, 5}, want: "1,3,5"},
		{name: "runs and singles mix", cpus: []int{1, 2, 3, 7, 9, 10}, want: "1-3,7,9-10"},
		{name: "unsorted input is ordered", cpus: []int{9, 1, 10, 2, 3, 7}, want: "1-3,7,9-10"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := FormatCpuSet(test.cpus); got != test.want {
				t.Fatalf("FormatCpuSet(%v) = %q, want %q", test.cpus, got, test.want)
			}
		})
	}
}

func TestFormatCpuSetDoesNotMutateInput(t *testing.T) {
	cpus := []int{5, 1, 3}
	FormatCpuSet(cpus)
	if want := []int{5, 1, 3}; !reflect.DeepEqual(cpus, want) {
		t.Fatalf("input mutated to %v, want %v", cpus, want)
	}
}

func TestCpuPlanCpuSetSpansAllAssignments(t *testing.T) {
	plan := CpuPlan{Cpus: []int{4, 5, 1}}

	if !plan.HasCpus() {
		t.Fatal("HasCpus() = false, want true")
	}
	if got, want := plan.CpuSet(), "1,4-5"; got != want {
		t.Fatalf("CpuSet() = %q, want %q", got, want)
	}
}

func TestCpuPlanEmpty(t *testing.T) {
	var plan CpuPlan

	if plan.HasCpus() {
		t.Fatal("HasCpus() = true, want false")
	}
	if got := plan.CpuSet(); got != "" {
		t.Fatalf("CpuSet() = %q, want empty", got)
	}
	if got := plan.PlacementClass(); got != "" {
		t.Fatalf("PlacementClass() = %q, want empty", got)
	}
}

func TestCpuPlanPlacementClass(t *testing.T) {
	pinned := CpuPlan{Cpus: []int{1}}
	if got := pinned.PlacementClass(); got != "" {
		t.Fatalf("PlacementClass() = %q, want empty for direct pinning", got)
	}

	ballooned := CpuPlan{Cpus: []int{1}, Placement: CpuPlacement{Class: "rt-balloon"}}
	if got, want := ballooned.PlacementClass(), "rt-balloon"; got != want {
		t.Fatalf("PlacementClass() = %q, want %q", got, want)
	}
}

func TestCpuPlanAssignmentMapCopiesCpus(t *testing.T) {
	plan := CpuPlan{Cpus: []int{1, 2, 3}}

	assignments := plan.AssignmentMap()
	want := map[string][]int{string(plan.Component): {1, 2, 3}}
	if !reflect.DeepEqual(assignments, want) {
		t.Fatalf("AssignmentMap() = %v, want %v", assignments, want)
	}

	assignments[string(plan.Component)][0] = 99
	if plan.Cpus[0] != 1 {
		t.Fatal("AssignmentMap() aliased the plan's CPU slice")
	}
}
