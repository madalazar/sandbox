package planner

import (
	"fmt"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

var _ CpuPlanner = TopologyCpuPlanner{}

// pins a component directly onto isolated cpu indices taken from the device topology.
// It is the compose runtime's cpu planner
type TopologyCpuPlanner struct {
	isolated []int
}

func NewTopologyCpuPlanner(isolated []int) TopologyCpuPlanner {
	return TopologyCpuPlanner{isolated: isolated}
}

// picks the isolated cpu indices the component gets, then reserves them in a single
// ledger call so a failure part-way through leaves nothing reserved
func (p TopologyCpuPlanner) PlanCpu(request CpuPlanningRequest) (model.CpuPlan, error) {
	requirements := request.Requirements

	if !requirements.HasIsolatedCores() || len(p.isolated) == 0 {
		return model.CpuPlan{Component: requirements.Component}, nil
	}

	needed := requirements.CountIsolatedCores()
	selected := make([]int, 0, needed)
	for _, cpu := range p.isolated {
		if len(selected) == needed {
			break
		}
		if request.Ledger.IsCpuAvailable(cpu, requirements.Component) {
			selected = append(selected, cpu)
		}
	}

	if len(selected) < needed {
		return model.CpuPlan{}, fmt.Errorf(
			"no free isolated cpus available for component %q (required=%d)",
			requirements.Component, needed,
		)
	}

	if err := request.Ledger.ReserveCpus(requirements.Component, selected); err != nil {
		return model.CpuPlan{}, err
	}

	return model.CpuPlan{
		Component: requirements.Component,
		Cpus:      selected,
	}, nil
}
