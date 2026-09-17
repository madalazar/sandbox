package controller

import (
	"context"
	"errors"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
	"github.com/margo/sandbox/poc/device/agent/types"
)

var errNotImplemented = errors.New("not implemented")

var _ CacheIsolationController = (*PqosCacheController)(nil)

// manages l3 cache allocation and core association using pqos
type PqosCacheController struct {
	runner  CommandRunner
	caches  []types.HostTopologyCache
	maxClos int
}

func NewPqosCacheController(runner CommandRunner, caches []types.HostTopologyCache, maxClos int) *PqosCacheController {
	return &PqosCacheController{
		runner:  runner,
		caches:  caches,
		maxClos: maxClos,
	}
}

// applies cache way masks and associates pinned cores with the reserved CLOS
func (c *PqosCacheController) Apply(ctx context.Context, reservation model.Reservation) error {
	return errNotImplemented
}

// verifies that the live pqos allocation matches the committed reservation
func (c *PqosCacheController) Verify(ctx context.Context, reservation model.Reservation) error {
	return errNotImplemented
}

// resets the cache way mask to all ways and moves cores back to default COS 0
func (c *PqosCacheController) Release(ctx context.Context, reservation model.Reservation) error {
	return errNotImplemented
}
