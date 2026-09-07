package planner

import (
	"reflect"
	"testing"

	"github.com/margo/sandbox/poc/device/agent/resource/ledger"
	"github.com/margo/sandbox/standard/generatedCode/wfm/sbi"
)

func TestTopologyCpuPlannerPlanCpu(t *testing.T) {
	planner := NewTopologyCpuPlanner(testIsolatedCpuIndices)
	l := newCpuPlanningTestLedger("deployment-123")

	tests := []struct {
		name              string
		componentName     string
		requiredResources *sbi.RequiredResources
		wantCpus          []int
	}{
		{
			name:              "cyclictest gets first isolated cpu",
			componentName:     "cyclictest_compose",
			requiredResources: isolatedCpuRequirement("cyclictest_compose"),
			wantCpus:          []int{1},
		},
		{
			name:          "stressng has no cpu requirement",
			componentName: "stressng_compose",
			wantCpus:      nil,
		},
		{
			name:              "caterpillar gets remaining isolated cpu",
			componentName:     "caterpillar_compose",
			requiredResources: isolatedCpuRequirement("caterpillar_compose"),
			wantCpus:          []int{3},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := planner.PlanCpu(
				newCpuPlanningRequest(t, test.componentName, test.requiredResources, l),
			)
			if err != nil {
				t.Fatalf("PlanCpu() error = %v", err)
			}
			if !reflect.DeepEqual(got.Cpus, test.wantCpus) {
				t.Fatalf("PlanCpu() cpus = %#v, want %#v", got.Cpus, test.wantCpus)
			}
		})
	}
}

func TestTopologyCpuPlannerIgnoresRequirementName(t *testing.T) {
	planner := NewTopologyCpuPlanner(testIsolatedCpuIndices)

	got, err := planner.PlanCpu(newCpuPlanningRequest(
		t,
		"cyclictest_compose",
		isolatedCpuRequirement("rt-container"),
		newCpuPlanningTestLedger("deployment-123"),
	))
	if err != nil {
		t.Fatalf("PlanCpu() error = %v", err)
	}

	want := []int{1}
	if !reflect.DeepEqual(got.Cpus, want) {
		t.Fatalf("PlanCpu() cpus = %#v, want %#v", got.Cpus, want)
	}
}

func TestTopologyCpuPlannerFailsWhenIsolatedCpusAreExhausted(t *testing.T) {
	planner := NewTopologyCpuPlanner(testIsolatedCpuIndices)
	l := newCpuPlanningTestLedger("deployment-123")

	for _, component := range []string{"component-a", "component-b"} {
		if _, err := planner.PlanCpu(
			newCpuPlanningRequest(t, component, isolatedCpuRequirement(component), l),
		); err != nil {
			t.Fatalf("PlanCpu(%s) error = %v", component, err)
		}
	}

	got, err := planner.PlanCpu(
		newCpuPlanningRequest(t, "component-c", isolatedCpuRequirement("component-c"), l),
	)
	if err == nil {
		t.Fatalf("PlanCpu() = %#v, want an error", got)
	}
}

func TestTopologyCpuPlannerReusesOwnPersistedCpus(t *testing.T) {
	planner := NewTopologyCpuPlanner(testIsolatedCpuIndices)
	snapshot := ledger.NewAllocationSnapshot(
		map[int]string{1: "deployment-123/cyclictest_compose"},
		map[int]struct{}{1: {}, 3: {}},
	)

	got, err := planner.PlanCpu(newCpuPlanningRequest(
		t,
		"cyclictest_compose",
		isolatedCpuRequirement("cyclictest_compose"),
		ledger.NewAllocationLedger(snapshot, "deployment-123"),
	))
	if err != nil {
		t.Fatalf("PlanCpu() error = %v", err)
	}

	want := []int{1}
	if !reflect.DeepEqual(got.Cpus, want) {
		t.Fatalf("PlanCpu() cpus = %#v, want %#v", got.Cpus, want)
	}
}

// A sibling's persisted claim blocks, unlike the component's own.
func TestTopologyCpuPlannerSkipsSiblingPersistedCpus(t *testing.T) {
	planner := NewTopologyCpuPlanner(testIsolatedCpuIndices)
	snapshot := ledger.NewAllocationSnapshot(
		map[int]string{1: "deployment-123/caterpillar_compose"},
		map[int]struct{}{1: {}, 3: {}},
	)

	got, err := planner.PlanCpu(newCpuPlanningRequest(
		t,
		"cyclictest_compose",
		isolatedCpuRequirement("cyclictest_compose"),
		ledger.NewAllocationLedger(snapshot, "deployment-123"),
	))
	if err != nil {
		t.Fatalf("PlanCpu() error = %v", err)
	}

	want := []int{3}
	if !reflect.DeepEqual(got.Cpus, want) {
		t.Fatalf("PlanCpu() cpus = %#v, want %#v", got.Cpus, want)
	}
}

func TestTopologyCpuPlannerWithoutIsolatedCpusPlansNothing(t *testing.T) {
	planner := NewTopologyCpuPlanner(nil)

	got, err := planner.PlanCpu(newCpuPlanningRequest(
		t,
		"cyclictest_compose",
		isolatedCpuRequirement("cyclictest_compose"),
		newCpuPlanningTestLedger("deployment-123"),
	))
	if err != nil {
		t.Fatalf("PlanCpu() error = %v", err)
	}
	if got.HasCpus() {
		t.Fatalf("PlanCpu() = %#v, want an empty plan", got.Cpus)
	}
}
