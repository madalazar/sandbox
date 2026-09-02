package main

import (
	"testing"

	"github.com/margo/sandbox/standard/generatedCode/wfm/sbi"
)

var testIsolatedCPUIndices = []int{1, 3}

func newCPUPlanningRequest(
	t *testing.T,
	componentName string,
	requiredResources *sbi.RequiredResources,
	ledger *AllocationLedger,
) CPUPlanningRequest {
	t.Helper()

	requirements, err := NormalizeCPURequirements(ComponentRef(componentName), requiredResources)
	if err != nil {
		t.Fatalf("NormalizeCPURequirements() error = %v", err)
	}

	return CPUPlanningRequest{Requirements: requirements, Ledger: ledger}
}

func newCPUPlanningTestLedger(deploymentID string) *AllocationLedger {
	isolated := make(map[int]struct{}, len(testIsolatedCPUIndices))
	for _, idx := range testIsolatedCPUIndices {
		isolated[idx] = struct{}{}
	}

	return NewAllocationLedger(NewAllocationSnapshot(nil, isolated), deploymentID)
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
