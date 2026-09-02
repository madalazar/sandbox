package planner

import (
	"fmt"

	"github.com/margo/sandbox/poc/device/agent/resource"
)

var _ CPUPlanner = TopologyCPUPlanner{}

// TopologyCPUPlanner pins a component directly onto isolated CPU indices taken from
// the device topology. It is the Compose runtime's CPU planner.
type TopologyCPUPlanner struct {
	isolated []int
}

func NewTopologyCPUPlanner(isolated []int) TopologyCPUPlanner {
	return TopologyCPUPlanner{isolated: isolated}
}

// PlanCPU picks the isolated CPU indices each requirement gets, then reserves them all
// in one ledger call so a failure part-way through leaves nothing reserved.
func (p TopologyCPUPlanner) PlanCPU(request CPUPlanningRequest) (resource.CpuPlan, error) {
	requirements := request.Requirements

	if !requirements.HasIsolatedCores() || len(p.isolated) == 0 {
		return resource.CpuPlan{}, nil
	}

	plan := resource.CpuPlan{Assignments: make([]resource.CpuAssignment, 0, len(requirements.Isolated))}
	claimed := map[int]bool{}
	all := []int(nil)

	for _, requirement := range requirements.Isolated {
		selected := make([]int, 0, requirement.Cores)
		for i := 0; i < len(p.isolated) && len(selected) < requirement.Cores; i++ {
			cpu := p.isolated[i]
			// Nothing is reserved until the end, so the ledger cannot exclude what an
			// earlier requirement in this call already took.
			if claimed[cpu] {
				continue
			}
			if !request.Ledger.IsCpuAvailable(cpu, requirements.Component) {
				continue
			}
			selected = append(selected, cpu)
			claimed[cpu] = true
		}

		if len(selected) < requirement.Cores {
			return resource.CpuPlan{}, fmt.Errorf(
				"no free isolated CPUs available for component %q (required=%d)",
				requirements.Component, requirement.Cores,
			)
		}

		all = append(all, selected...)
		plan.Assignments = append(plan.Assignments, resource.CpuAssignment{
			Component: requirements.Component,
			Cpus:      selected,
		})
	}

	if err := request.Ledger.ReserveCPUs(requirements.Component, all); err != nil {
		return resource.CpuPlan{}, err
	}

	return plan, nil
}
