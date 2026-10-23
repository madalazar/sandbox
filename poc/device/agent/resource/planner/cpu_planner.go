package planner

import (
	"errors"

	"github.com/margo/sandbox/poc/device/agent/resource/ledger"
	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

var plannerErrNotImplemented = errors.New("not implemented")

// everything a planner may look at: one component's normalized requirements and the
// ledger that answers free-versus-taken for the deployment being planned. There is no
// deployment id, no persisted map and no context: planners perform no I/O
type CpuPlanningRequest struct {
	Requirements model.NormalizedCpuRequirements
	Ledger       *ledger.AllocationLedger
}

// decides which cpu indices a component gets. Implementations are deterministic and
// side-effect free apart from reserving on the ledger
type CpuPlanner interface {
	PlanCpu(CpuPlanningRequest) (model.CpuPlan, error)
}
