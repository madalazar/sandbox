package planner

import (
	"errors"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
	"github.com/margo/sandbox/poc/device/agent/types"
)

var errNotImplemented = errors.New("not implemented")

var _ CachePlanner = (*L3CachePlanner)(nil)

// plans exclusive l3 cache allocations and classes of service for components
type L3CachePlanner struct {
	caches []types.HostTopologyCache
}

func NewL3CachePlanner(caches []types.HostTopologyCache) *L3CachePlanner {
	return &L3CachePlanner{caches: caches}
}

// decides cache way allocations and class reservation for a component
func (p *L3CachePlanner) PlanCache(request CachePlanningRequest) (model.CachePlan, error) {
	return model.CachePlan{}, errNotImplemented
}
