package planner

import (
	"reflect"
	"testing"

	"github.com/margo/sandbox/poc/device/agent/resource"
	"github.com/margo/sandbox/standard/generatedCode/wfm/sbi"
)

type staticBalloonPolicyReader struct {
	policy *resource.ParsedBalloonPolicy
}

func (reader staticBalloonPolicyReader) Parsed() *resource.ParsedBalloonPolicy {
	return reader.policy
}

func newTestBalloonCPUPlanner() BalloonCPUPlanner {
	preferIsolated := true

	return NewBalloonCPUPlanner(staticBalloonPolicyReader{
		policy: &resource.ParsedBalloonPolicy{
			Name:      "default",
			Namespace: "kube-system",
			BalloonTypes: []resource.ParsedBalloonType{
				{
					Name:                 "ipc1",
					PreferIsolCpus:       &preferIsolated,
					PreferCloseToDevices: []string{"/sys/devices/system/cpu/cpu1/cache/index2"},
				},
				{
					Name:                 "ipc3",
					PreferIsolCpus:       &preferIsolated,
					PreferCloseToDevices: []string{"/sys/devices/system/cpu/cpu3/cache/index2"},
				},
			},
		},
	}, testIsolatedCPUIndices)
}

func TestBalloonCPUPlannerPlanCPU(t *testing.T) {
	planner := newTestBalloonCPUPlanner()
	ledger := newCPUPlanningTestLedger("deployment-123")

	tests := []struct {
		name              string
		componentName     string
		requiredResources *sbi.RequiredResources
		want              []resource.CpuAssignment
	}{
		{
			name:              "caterpillar gets cpu1 balloon",
			componentName:     "caterpillar",
			requiredResources: isolatedCPURequirement("caterpillar"),
			want: []resource.CpuAssignment{
				{Component: "caterpillar", Cpus: []int{1}, Placement: resource.CpuPlacement{Class: "ipc1"}},
			},
		},
		{
			name:              "cyclictest gets cpu3 balloon",
			componentName:     "cyclictest",
			requiredResources: isolatedCPURequirement("cyclictest"),
			want: []resource.CpuAssignment{
				{Component: "cyclictest", Cpus: []int{3}, Placement: resource.CpuPlacement{Class: "ipc3"}},
			},
		},
		{
			name:          "stressng has no CPU requirement",
			componentName: "stressng",
			want:          nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, err := planner.PlanCPU(
				newCPUPlanningRequest(t, test.componentName, test.requiredResources, ledger),
			)
			if err != nil {
				t.Fatalf("PlanCPU() error = %v", err)
			}
			if !reflect.DeepEqual(plan.Assignments, test.want) {
				t.Errorf("assignments = %#v, want %#v", plan.Assignments, test.want)
			}
		})
	}
}

func TestBalloonCPUPlannerFailsWhenEveryBalloonIsOccupied(t *testing.T) {
	planner := newTestBalloonCPUPlanner()
	ledger := newCPUPlanningTestLedger("deployment-123")

	for _, component := range []string{"component-a", "component-b"} {
		if _, err := planner.PlanCPU(
			newCPUPlanningRequest(t, component, isolatedCPURequirement(component), ledger),
		); err != nil {
			t.Fatalf("PlanCPU(%s) error = %v", component, err)
		}
	}

	plan, err := planner.PlanCPU(
		newCPUPlanningRequest(t, "component-c", isolatedCPURequirement("component-c"), ledger),
	)
	if err == nil {
		t.Fatalf("PlanCPU() = %#v, want an error", plan)
	}
}

func TestBalloonCPUPlannerRequiresAPolicySnapshot(t *testing.T) {
	planner := NewBalloonCPUPlanner(staticBalloonPolicyReader{}, testIsolatedCPUIndices)

	plan, err := planner.PlanCPU(newCPUPlanningRequest(
		t,
		"cyclictest",
		isolatedCPURequirement("cyclictest"),
		newCPUPlanningTestLedger("deployment-123"),
	))
	if err == nil {
		t.Fatalf("PlanCPU() = %#v, want an error", plan)
	}
}
