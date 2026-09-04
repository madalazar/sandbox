package planner

import (
	"errors"
	"testing"

	"github.com/margo/sandbox/poc/device/agent/resource/ledger"
	"github.com/margo/sandbox/poc/device/agent/resource/model"
	"github.com/margo/sandbox/standard/generatedCode/wfm/sbi"
)

var testIsolatedCpuIndices = []int{1, 3}

func newCpuPlanningRequest(
	t *testing.T,
	componentName string,
	requiredResources *sbi.RequiredResources,
	l *ledger.AllocationLedger,
) CpuPlanningRequest {
	t.Helper()

	requirements, err := model.NormalizeCpuRequirements(model.ComponentRef(componentName), requiredResources)
	if err != nil {
		t.Fatalf("NormalizeCpuRequirements() error = %v", err)
	}

	return CpuPlanningRequest{Requirements: requirements, Ledger: l}
}

func newCpuPlanningTestLedger(deploymentId string) *ledger.AllocationLedger {
	isolated := make(map[int]struct{}, len(testIsolatedCpuIndices))
	for _, idx := range testIsolatedCpuIndices {
		isolated[idx] = struct{}{}
	}

	return ledger.NewAllocationLedger(ledger.NewAllocationSnapshot(nil, isolated), deploymentId)
}

func isolatedCpuRequirement(name string) *sbi.RequiredResources {
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

// BalloonCpuPlanner is tested as not yet implemented until its implementation lands
func TestBalloonCpuPlannerIsNotYetImplemented(t *testing.T) {
	planner := NewBalloonCpuPlanner(nil, nil)
	if _, err := planner.PlanCpu(CpuPlanningRequest{}); !errors.Is(err, plannerErrNotImplemented) {
		t.Fatalf("PlanCpu() error = %v, want %v", err, plannerErrNotImplemented)
	}
}
