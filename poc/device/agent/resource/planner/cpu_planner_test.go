package planner

import (
	"testing"

	"github.com/margo/sandbox/poc/device/agent/resource"
	"github.com/margo/sandbox/standard/generatedCode/wfm/sbi"
)

var testIsolatedCPUIndices = []int{1, 3}

func newCPUPlanningRequest(
	t *testing.T,
	componentName string,
	requiredResources *sbi.RequiredResources,
	ledger *resource.AllocationLedger,
) CPUPlanningRequest {
	t.Helper()

	requirements, err := resource.NormalizeCPURequirements(resource.ComponentRef(componentName), requiredResources)
	if err != nil {
		t.Fatalf("NormalizeCPURequirements() error = %v", err)
	}

	return CPUPlanningRequest{Requirements: requirements, Ledger: ledger}
}

func newCPUPlanningTestLedger(deploymentID string) *resource.AllocationLedger {
	isolated := make(map[int]struct{}, len(testIsolatedCPUIndices))
	for _, idx := range testIsolatedCPUIndices {
		isolated[idx] = struct{}{}
	}

	return resource.NewAllocationLedger(resource.NewAllocationSnapshot(nil, isolated), deploymentID)
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
