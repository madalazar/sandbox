package main

import "fmt"

// Nothing consumes CPUPlanner yet; remove once the runtime factory assigns this into
// Runtime.CPUPlanner and the compiler checks it there.
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
func (p TopologyCPUPlanner) PlanCPU(request CPUPlanningRequest) (CpuPlan, error) {
	requirements := request.Requirements

	if !requirements.HasIsolatedCores() || len(p.isolated) == 0 {
		return CpuPlan{}, nil
	}

	plan := CpuPlan{Assignments: make([]CpuAssignment, 0, len(requirements.Isolated))}
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
			return CpuPlan{}, fmt.Errorf(
				"no free isolated CPUs available for component %q (required=%d)",
				requirements.Component, requirement.Cores,
			)
		}

		all = append(all, selected...)
		plan.Assignments = append(plan.Assignments, CpuAssignment{
			Component: requirements.Component,
			Cpus:      selected,
		})
	}

	if err := request.Ledger.ReserveCPUs(requirements.Component, all); err != nil {
		return CpuPlan{}, err
	}

	return plan, nil
}
