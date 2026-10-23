package planner

import (
	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

var _ CpuPlanner = BalloonCpuPlanner{}

// places a component into an NRI balloon whose cpus are isolated. It is the helm
// runtime's cpu planner: it returns the balloon's cpus and the balloon name, and
// constructs no kubernetes annotations
type BalloonCpuPlanner struct {
	policies model.BalloonPolicyReader
	isolated []int
}

func NewBalloonCpuPlanner(policies model.BalloonPolicyReader, isolated []int) BalloonCpuPlanner {
	return BalloonCpuPlanner{policies: policies, isolated: isolated}
}

// selects the smallest free isolated balloon that satisfies the component's isolated
// core request and reserves its cpus on the ledger
func (p BalloonCpuPlanner) PlanCpu(request CpuPlanningRequest) (model.CpuPlan, error) {
	return model.CpuPlan{}, plannerErrNotImplemented
}
