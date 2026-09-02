package planner

import (
	"reflect"
	"testing"

	"github.com/margo/sandbox/poc/device/agent/resource"
	"github.com/margo/sandbox/standard/generatedCode/wfm/sbi"
)

func TestTopologyCPUPlannerPlanCPU(t *testing.T) {
	planner := NewTopologyCPUPlanner(testIsolatedCPUIndices)
	ledger := newCPUPlanningTestLedger("deployment-123")

	tests := []struct {
		name              string
		componentName     string
		requiredResources *sbi.RequiredResources
		want              []resource.CpuAssignment
	}{
		{
			name:              "cyclictest gets first isolated CPU",
			componentName:     "cyclictest_compose",
			requiredResources: isolatedCPURequirement("cyclictest_compose"),
			want: []resource.CpuAssignment{
				{Component: "cyclictest_compose", Cpus: []int{1}},
			},
		},
		{
			name:          "stressng has no CPU requirement",
			componentName: "stressng_compose",
			want:          nil,
		},
		{
			name:              "caterpillar gets remaining isolated CPU",
			componentName:     "caterpillar_compose",
			requiredResources: isolatedCPURequirement("caterpillar_compose"),
			want: []resource.CpuAssignment{
				{Component: "caterpillar_compose", Cpus: []int{3}},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := planner.PlanCPU(
				newCPUPlanningRequest(t, test.componentName, test.requiredResources, ledger),
			)
			if err != nil {
				t.Fatalf("PlanCPU() error = %v", err)
			}
			if !reflect.DeepEqual(got.Assignments, test.want) {
				t.Fatalf("PlanCPU() = %#v, want %#v", got.Assignments, test.want)
			}
		})
	}
}

// A requirement named after the container rather than the component used to be dropped
// silently, leaving the workload unpinned and no error reported.
func TestTopologyCPUPlannerIgnoresRequirementName(t *testing.T) {
	planner := NewTopologyCPUPlanner(testIsolatedCPUIndices)

	got, err := planner.PlanCPU(newCPUPlanningRequest(
		t,
		"cyclictest_compose",
		isolatedCPURequirement("rt-container"),
		newCPUPlanningTestLedger("deployment-123"),
	))
	if err != nil {
		t.Fatalf("PlanCPU() error = %v", err)
	}

	want := []resource.CpuAssignment{{Component: "cyclictest_compose", Cpus: []int{1}}}
	if !reflect.DeepEqual(got.Assignments, want) {
		t.Fatalf("PlanCPU() = %#v, want %#v", got.Assignments, want)
	}
}

func TestTopologyCPUPlannerFailsWhenIsolatedCPUsAreExhausted(t *testing.T) {
	planner := NewTopologyCPUPlanner(testIsolatedCPUIndices)
	ledger := newCPUPlanningTestLedger("deployment-123")

	for _, component := range []string{"component-a", "component-b"} {
		if _, err := planner.PlanCPU(
			newCPUPlanningRequest(t, component, isolatedCPURequirement(component), ledger),
		); err != nil {
			t.Fatalf("PlanCPU(%s) error = %v", component, err)
		}
	}

	got, err := planner.PlanCPU(
		newCPUPlanningRequest(t, "component-c", isolatedCPURequirement("component-c"), ledger),
	)
	if err == nil {
		t.Fatalf("PlanCPU() = %#v, want an error", got)
	}
}

// A component keeps the CPUs it already holds when it is planned again.
func TestTopologyCPUPlannerReusesOwnPersistedCPUs(t *testing.T) {
	planner := NewTopologyCPUPlanner(testIsolatedCPUIndices)
	snapshot := resource.NewAllocationSnapshot(
		map[int]string{1: "deployment-123/cyclictest_compose"},
		map[int]struct{}{1: {}, 3: {}},
	)

	got, err := planner.PlanCPU(newCPUPlanningRequest(
		t,
		"cyclictest_compose",
		isolatedCPURequirement("cyclictest_compose"),
		resource.NewAllocationLedger(snapshot, "deployment-123"),
	))
	if err != nil {
		t.Fatalf("PlanCPU() error = %v", err)
	}

	want := []resource.CpuAssignment{{Component: "cyclictest_compose", Cpus: []int{1}}}
	if !reflect.DeepEqual(got.Assignments, want) {
		t.Fatalf("PlanCPU() = %#v, want %#v", got.Assignments, want)
	}
}

// A sibling's persisted claim blocks, unlike the component's own.
func TestTopologyCPUPlannerSkipsSiblingPersistedCPUs(t *testing.T) {
	planner := NewTopologyCPUPlanner(testIsolatedCPUIndices)
	snapshot := resource.NewAllocationSnapshot(
		map[int]string{1: "deployment-123/caterpillar_compose"},
		map[int]struct{}{1: {}, 3: {}},
	)

	got, err := planner.PlanCPU(newCPUPlanningRequest(
		t,
		"cyclictest_compose",
		isolatedCPURequirement("cyclictest_compose"),
		resource.NewAllocationLedger(snapshot, "deployment-123"),
	))
	if err != nil {
		t.Fatalf("PlanCPU() error = %v", err)
	}

	want := []resource.CpuAssignment{{Component: "cyclictest_compose", Cpus: []int{3}}}
	if !reflect.DeepEqual(got.Assignments, want) {
		t.Fatalf("PlanCPU() = %#v, want %#v", got.Assignments, want)
	}
}

func TestTopologyCPUPlannerWithoutIsolatedCPUsPlansNothing(t *testing.T) {
	planner := NewTopologyCPUPlanner(nil)

	got, err := planner.PlanCPU(newCPUPlanningRequest(
		t,
		"cyclictest_compose",
		isolatedCPURequirement("cyclictest_compose"),
		newCPUPlanningTestLedger("deployment-123"),
	))
	if err != nil {
		t.Fatalf("PlanCPU() error = %v", err)
	}
	if got.HasCpus() {
		t.Fatalf("PlanCPU() = %#v, want an empty plan", got.Assignments)
	}
}
