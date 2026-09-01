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

func TestResolveComponentBalloonAnnotations(t *testing.T) {
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

	deploymentID := "deployment-123"
	inFlightAssignments := map[string][]int{}
	tests := []struct {
		name               string
		componentName      string
		requiredResources  *sbi.RequiredResources
		wantAnnotations    map[string]string
		wantAssignments    map[string][]int
		wantHasAnnotations bool
	}{
		{
			name:               "caterpillar gets cpu1 balloon",
			componentName:      "caterpillar",
			requiredResources:  isolatedCPURequirement("caterpillar"),
			wantAnnotations:    map[string]string{"balloon.balloons.resource-policy.nri.io/pod": "ipc1"},
			wantAssignments:    map[string][]int{"caterpillar": {1}},
			wantHasAnnotations: true,
		},
		{
			name:               "cyclictest gets cpu3 balloon",
			componentName:      "cyclictest",
			requiredResources:  isolatedCPURequirement("cyclictest"),
			wantAnnotations:    map[string]string{"balloon.balloons.resource-policy.nri.io/pod": "ipc3"},
			wantAssignments:    map[string][]int{"cyclictest": {3}},
			wantHasAnnotations: true,
		},
		{
			name:               "stressng has no CPU requirement",
			componentName:      "stressng",
			wantAnnotations:    map[string]string{},
			wantAssignments:    map[string][]int{},
			wantHasAnnotations: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			annotations, assignments, hasAnnotations, err := dm.resolveComponentBalloonAnnotations(
				deploymentID,
				test.componentName,
				test.requiredResources,
				inFlightAssignments,
			)
			if err != nil {
				t.Fatalf("resolveComponentBalloonAnnotations() error = %v", err)
			}
			if !reflect.DeepEqual(annotations, test.wantAnnotations) {
				t.Errorf("annotations = %#v, want %#v", annotations, test.wantAnnotations)
			}
			if !reflect.DeepEqual(assignments, test.wantAssignments) {
				t.Errorf("assignments = %#v, want %#v", assignments, test.wantAssignments)
			}
			if hasAnnotations != test.wantHasAnnotations {
				t.Errorf("hasAnnotations = %v, want %v", hasAnnotations, test.wantHasAnnotations)
			}

			for requirement, cpus := range assignments {
				inFlightAssignments[requirement] = append([]int(nil), cpus...)
			}
		})
	}

	wantFinalAssignments := map[string][]int{
		"caterpillar": {1},
		"cyclictest":  {3},
	}
	if !reflect.DeepEqual(inFlightAssignments, wantFinalAssignments) {
		t.Fatalf("final assignments = %#v, want %#v", inFlightAssignments, wantFinalAssignments)
	}
}
