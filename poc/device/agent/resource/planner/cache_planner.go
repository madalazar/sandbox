package planner

import (
	"context"

	"github.com/margo/sandbox/poc/device/agent/resource/ledger"
	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

// carries everything a cache planner may look at:
// the component's normalized cache requirements, the cpu plan decided for it,
// and the ledger answering free-versus-taken for the deployment being planned
type CachePlanningRequest struct {
	Requirements model.NormalizedCacheRequirements
	CpuPlan      model.CpuPlan
	Ledger       *ledger.AllocationLedger
}

// decides cache way allocations and class reservation for a component
type CachePlanner interface {
	PlanCache(context.Context, CachePlanningRequest) (model.CachePlan, error)
}
