package main

import (
	"reflect"
	"testing"

	"github.com/margo/sandbox/standard/generatedCode/wfm/sbi"
)

type staticBalloonPolicyReader struct {
	policy *ParsedBalloonPolicy
}

func (reader staticBalloonPolicyReader) Parsed() *ParsedBalloonPolicy {
	return reader.policy
}

func TestResolveComponentBalloonCPUPlan(t *testing.T) {
	dm := newCPUAssignmentTestDeploymentManager(t)
	preferIsolated := true
	dm.policyReader = staticBalloonPolicyReader{
		policy: &ParsedBalloonPolicy{
			Name:      "default",
			Namespace: "kube-system",
			BalloonTypes: []ParsedBalloonType{
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
	}

	ledger := dm.newAllocationLedger("deployment-123")
	tests := []struct {
		name              string
		componentName     string
		requiredResources *sbi.RequiredResources
		want              []CpuAssignment
	}{
		{
			name:              "caterpillar gets cpu1 balloon",
			componentName:     "caterpillar",
			requiredResources: isolatedCPURequirement("caterpillar"),
			want: []CpuAssignment{
				{Component: "caterpillar", Cpus: []int{1}, Placement: CpuPlacement{Class: "ipc1"}},
			},
		},
		{
			name:              "cyclictest gets cpu3 balloon",
			componentName:     "cyclictest",
			requiredResources: isolatedCPURequirement("cyclictest"),
			want: []CpuAssignment{
				{Component: "cyclictest", Cpus: []int{3}, Placement: CpuPlacement{Class: "ipc3"}},
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
			plan, err := dm.resolveComponentBalloonCPUPlan(
				test.componentName,
				test.requiredResources,
				ledger,
			)
			if err != nil {
				t.Fatalf("resolveComponentBalloonCPUPlan() error = %v", err)
			}
			if !reflect.DeepEqual(plan.Assignments, test.want) {
				t.Errorf("assignments = %#v, want %#v", plan.Assignments, test.want)
			}
		})
	}
}
