package planner

import (
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
	return model.CpuPlan{}, plannerErrNotImplemented
}
