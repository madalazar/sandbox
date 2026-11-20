package planner

import (
	"reflect"
	"testing"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
	"github.com/margo/sandbox/standard/generatedCode/wfm/sbi"
)

type staticBalloonPolicyReader struct {
	policy *model.ParsedBalloonPolicy
}

func (reader staticBalloonPolicyReader) Parsed() *model.ParsedBalloonPolicy {
	return reader.policy
}

func newTestBalloonCpuPlanner() BalloonCpuPlanner {
	preferIsolated := true

	return NewBalloonCpuPlanner(staticBalloonPolicyReader{
		policy: &model.ParsedBalloonPolicy{
			Name:      "default",
			Namespace: "kube-system",
			BalloonTypes: []model.ParsedBalloonType{
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
	}, testIsolatedCpuIndices)
}

func TestBalloonCpuPlannerPlanCpu(t *testing.T) {
	planner := newTestBalloonCpuPlanner()
	l := newCpuPlanningTestLedger("deployment-123")

	tests := []struct {
		name              string
		componentName     string
		requiredResources *sbi.RequiredResources
		wantCpus          []int
		wantClass         string
	}{
		{
			name:              "caterpillar gets cpu1 balloon",
			componentName:     "caterpillar",
			requiredResources: isolatedCpuRequirement("caterpillar"),
			wantCpus:          []int{1},
			wantClass:         "ipc1",
		},
		{
			name:              "cyclictest gets cpu3 balloon",
			componentName:     "cyclictest",
			requiredResources: isolatedCpuRequirement("cyclictest"),
			wantCpus:          []int{3},
			wantClass:         "ipc3",
		},
		{
			name:          "stressng has no CPU requirement",
			componentName: "stressng",
			wantCpus:      nil,
			wantClass:     "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, err := planner.PlanCpu(
				newCpuPlanningRequest(t, test.componentName, test.requiredResources, l),
			)
			if err != nil {
				t.Fatalf("PlanCpu() error = %v", err)
			}
			if !reflect.DeepEqual(plan.Cpus, test.wantCpus) {
				t.Errorf("cpus = %#v, want %#v", plan.Cpus, test.wantCpus)
			}
			if plan.PlacementClass() != test.wantClass {
				t.Errorf("placement class = %q, want %q", plan.PlacementClass(), test.wantClass)
			}
		})
	}
}

func TestBalloonCpuPlannerFailsWhenEveryBalloonIsOccupied(t *testing.T) {
	planner := newTestBalloonCpuPlanner()
	l := newCpuPlanningTestLedger("deployment-123")

	for _, component := range []string{"component-a", "component-b"} {
		if _, err := planner.PlanCpu(
			newCpuPlanningRequest(t, component, isolatedCpuRequirement(component), l),
		); err != nil {
			t.Fatalf("PlanCpu(%s) error = %v", component, err)
		}
	}

	plan, err := planner.PlanCpu(
		newCpuPlanningRequest(t, "component-c", isolatedCpuRequirement("component-c"), l),
	)
	if err == nil {
		t.Fatalf("PlanCpu() = %#v, want an error", plan)
	}
}

func TestBalloonCpuPlannerRequiresAPolicySnapshot(t *testing.T) {
	planner := NewBalloonCpuPlanner(staticBalloonPolicyReader{}, testIsolatedCpuIndices)

	plan, err := planner.PlanCpu(newCpuPlanningRequest(
		t,
		"cyclictest",
		isolatedCpuRequirement("cyclictest"),
		newCpuPlanningTestLedger("deployment-123"),
	))
	if err == nil {
		t.Fatalf("PlanCpu() = %#v, want an error", plan)
	}
}
