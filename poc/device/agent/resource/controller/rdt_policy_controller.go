package controller

import (
	"context"
	"errors"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
	"github.com/margo/sandbox/poc/device/agent/types"
)

var errNotImplemented = errors.New("not implemented")

var _ CacheIsolationController = (*RdtPolicyController)(nil)

// manages kubernetes rdt cache classes via nri balloon resource policies
type RdtPolicyController struct {
	caches []types.HostTopologyCache
}

func NewRdtPolicyController(caches []types.HostTopologyCache) *RdtPolicyController {
	return &RdtPolicyController{caches: caches}
}

// applies rdt cache class(es) for the committed reservation
func (c *RdtPolicyController) Apply(ctx context.Context, reservation model.Reservation) error {
	return errNotImplemented
}

// verifies that the rdt cache class(es) are present in the nri balloon resource policy
func (c *RdtPolicyController) Verify(ctx context.Context, reservation model.Reservation) error {
	return errNotImplemented
}

// removes rdt cache class(es) from the nri balloon resource policy
func (c *RdtPolicyController) Release(ctx context.Context, reservation model.Reservation) error {
	return errNotImplemented
}
