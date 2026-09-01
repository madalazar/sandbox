package main

import (
	"reflect"
	"testing"

	"github.com/margo/sandbox/poc/device/agent/database"
	"github.com/margo/sandbox/poc/device/agent/device"
	"github.com/margo/sandbox/standard/generatedCode/wfm/sbi"
	"go.uber.org/zap"
)

func TestResolveComponentCpuAssignments(t *testing.T) {
	dm := newCPUAssignmentTestDeploymentManager(t)
	ledger := dm.newAllocationLedger("deployment-123")

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
			want: []CpuAssignment{
				{Component: "caterpillar_compose", Cpus: []int{3}},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := dm.resolveComponentCpuAssignments(
				test.componentName,
				test.requiredResources,
				ledger,
			)
			if err != nil {
				t.Fatalf("resolveComponentCpuAssignments() error = %v", err)
			}
			if !reflect.DeepEqual(got.Assignments, test.want) {
				t.Fatalf("resolveComponentCpuAssignments() = %#v, want %#v", got.Assignments, test.want)
			}
		})
	}
}

// A requirement named after the container rather than the component used to be dropped
// silently, leaving the workload unpinned and no error reported.
func TestResolveComponentCpuAssignmentsIgnoresRequirementName(t *testing.T) {
	dm := newCPUAssignmentTestDeploymentManager(t)

	got, err := dm.resolveComponentCpuAssignments(
		"cyclictest_compose",
		isolatedCPURequirement("rt-container"),
		dm.newAllocationLedger("deployment-123"),
	)
	if err != nil {
		t.Fatalf("resolveComponentCpuAssignments() error = %v", err)
	}

	want := []CpuAssignment{{Component: "cyclictest_compose", Cpus: []int{1}}}
	if !reflect.DeepEqual(got.Assignments, want) {
		t.Fatalf("resolveComponentCpuAssignments() = %#v, want %#v", got.Assignments, want)
	}
}

func TestResolveComponentCpuAssignmentsRejectsSecondIsolatedRequirement(t *testing.T) {
	dm := newCPUAssignmentTestDeploymentManager(t)

	required := isolatedCPURequirement("cyclictest_compose")
	*required.Cpu = append(*required.Cpu, (*required.Cpu)[0])

	got, err := dm.resolveComponentCpuAssignments(
		"cyclictest_compose",
		required,
		dm.newAllocationLedger("deployment-123"),
	)
	if err == nil {
		t.Fatalf("resolveComponentCpuAssignments() = %#v, want an error", got)
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
