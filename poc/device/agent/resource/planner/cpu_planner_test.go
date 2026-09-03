package planner

import (
	"errors"
	"testing"
)

// the skeletons are wired but not yet implemented; the bodies land in a later commit
func TestCpuPlannersAreNotYetImplemented(t *testing.T) {

	planners := map[string]CpuPlanner{
		"topology": NewTopologyCpuPlanner(nil),
		"balloon":  NewBalloonCpuPlanner(nil, nil),
	}

	for name, planner := range planners {
		t.Run(name, func(t *testing.T) {
			if _, err := planner.PlanCpu(CpuPlanningRequest{}); !errors.Is(err, plannerErrNotImplemented) {
				t.Fatalf("PlanCpu() error = %v, want %v", err, plannerErrNotImplemented)
			}
		})
	}
}
