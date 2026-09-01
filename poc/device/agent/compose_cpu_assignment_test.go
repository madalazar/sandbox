package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/margo/sandbox/poc/device/agent/database"
	"github.com/margo/sandbox/poc/device/agent/device"
	"github.com/margo/sandbox/standard/generatedCode/wfm/sbi"
	"go.uber.org/zap"
)

func TestResolveComponentCpuAssignments(t *testing.T) {
	dm := newCPUAssignmentTestDeploymentManager(t)
	deploymentID := "deployment-123"
	inFlightAssignments := map[string][]int{}

	tests := []struct {
		name              string
		componentName     string
		requiredResources *sbi.RequiredResources
		want              []CpuAssignment
	}{
		{
			name:              "cyclictest gets first isolated CPU",
			componentName:     "cyclictest_compose",
			requiredResources: isolatedCPURequirement("cyclictest_compose"),
			want: []CpuAssignment{
				{Requirement: "cyclictest_compose", Cpus: []int{1}},
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
			want: []CpuAssignment{
				{Requirement: "caterpillar_compose", Cpus: []int{3}},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			composePath := filepath.Join(t.TempDir(), "compose.yaml")
			content := []byte("services:\n  " + test.componentName + ":\n    image: example:latest\n")
			if err := os.WriteFile(composePath, content, 0o600); err != nil {
				t.Fatalf("write compose fixture: %v", err)
			}

			got, err := dm.resolveComponentCpuAssignments(
				deploymentID,
				test.componentName,
				composePath,
				test.requiredResources,
				inFlightAssignments,
			)
			if err != nil {
				t.Fatalf("resolveComponentCpuAssignments() error = %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("resolveComponentCpuAssignments() = %#v, want %#v", got, test.want)
			}

			for requirement, cpus := range toAssignmentMap(got) {
				inFlightAssignments[requirement] = cpus
			}
		})
	}

	wantFinalAssignments := map[string][]int{
		"cyclictest_compose":  {1},
		"caterpillar_compose": {3},
	}
	if !reflect.DeepEqual(inFlightAssignments, wantFinalAssignments) {
		t.Fatalf("final assignments = %#v, want %#v", inFlightAssignments, wantFinalAssignments)
	}
}

func newCPUAssignmentTestDeploymentManager(t *testing.T) *DeploymentManager {
	t.Helper()

	return &DeploymentManager{
		database: database.NewDatabase(t.TempDir()),
		topologyLookup: device.TopologyLookup{
			IsolatedCPUIndices: []int{1, 3},
			IsolatedCPUSet: map[int]struct{}{
				1: {},
				3: {},
			},
		},
		log: zap.NewNop().Sugar(),
	}
}

func isolatedCPURequirement(name string) *sbi.RequiredResources {
	cores := float32(1)
	class := sbi.CpuClassPerformance
	cpuType := sbi.CpuTypeIsolated
	cpuRequirements := []sbi.Cpu{
		{
			Name:  &name,
			Cores: &cores,
			Class: &class,
			Type:  &cpuType,
		},
	}

	return &sbi.RequiredResources{Cpu: &cpuRequirements}
}
