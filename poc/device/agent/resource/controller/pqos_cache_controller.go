package controller

import (
	"context"
	"fmt"
	"strings"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
	"github.com/margo/sandbox/poc/device/agent/types"
)

var _ CacheIsolationController = (*PqosCacheController)(nil)

// COS0 is the hardware/OS default class of service to which all unassociated
// cores belong; component reservations must never overwrite COS0's mask
const defaultPqosCosId = "0"

// manages l3 cache allocation and core association using pqos
type PqosCacheController struct {
	runner  CommandRunner
	factory PqosCommandFactory
	caches  []types.HostTopologyCache
	maxClos int
}

func NewPqosCacheController(runner CommandRunner, caches []types.HostTopologyCache, maxClos int) *PqosCacheController {
	factory, _ := NewPqosCommandFactory(PqosInterfaceOs)
	return &PqosCacheController{
		runner:  runner,
		factory: factory,
		caches:  caches,
		maxClos: maxClos,
	}
}

func NewPqosCacheControllerWithFactory(runner CommandRunner, factory PqosCommandFactory, caches []types.HostTopologyCache, maxClos int) *PqosCacheController {
	return &PqosCacheController{
		runner:  runner,
		factory: factory,
		caches:  caches,
		maxClos: maxClos,
	}
}

// applies cache way masks and associates pinned cores with the reserved clos
func (c *PqosCacheController) Apply(ctx context.Context, reservation model.Reservation) error {
	if !reservation.HasL3Cache() || reservation.L3CacheAssignment == nil {
		return nil
	}

	componentName := string(reservation.Owner.Component)
	alloc := reservation.L3CacheAssignment

	if !alloc.Clos.Held() {
		return fmt.Errorf("component %q has invalid or unset class id %q", componentName, alloc.Clos)
	}
	cosId := alloc.Clos.String()
	if cosId == defaultPqosCosId {
		return fmt.Errorf("component %q cannot use reserved default pqos class %q", componentName, cosId)
	}

	cacheID := strings.TrimSpace(alloc.CacheId)
	if cacheID == "" {
		return fmt.Errorf("component %q has empty cache id", componentName)
	}

	// we expect exactly one cache allocation for a partition for this
	// c-ller
	if len(alloc.Masks) != 1 {
		return fmt.Errorf("component %q expected 1 cache mask for pqos, got %d", componentName, len(alloc.Masks))
	}

	mask := strings.TrimSpace(alloc.Masks[cacheID])
	if mask == "" {
		return fmt.Errorf("component %q has empty cache mask for cache id %q", componentName, cacheID)
	}

	cpuset := reservation.CpuSet()
	if strings.TrimSpace(cpuset) == "" {
		return fmt.Errorf("component %q has no assigned cpus for pqos association", componentName)
	}

	args := c.factory.BuildApplyArgs(cacheID, cosId, mask, cpuset)
	_, err := c.runner.Run(ctx, "pqos", args...)
	if err != nil {
		return fmt.Errorf("failed to apply pqos assignment for component %q: %w", componentName, err)
	}

	return nil
}

// verifies that the live pqos allocation matches the committed reservation
func (c *PqosCacheController) Verify(ctx context.Context, reservation model.Reservation) error {
	return nil
}

// resets the cache way mask to all ways and moves cores back to default COS 0
func (c *PqosCacheController) Release(ctx context.Context, reservation model.Reservation) error {
	if !reservation.HasL3Cache() || reservation.L3CacheAssignment == nil {
		return nil
	}

	componentName := string(reservation.Owner.Component)
	alloc := reservation.L3CacheAssignment
	if !alloc.Clos.Held() || alloc.Clos.String() == defaultPqosCosId {
		return nil
	}
	cosId := alloc.Clos.String()

	cacheId := strings.TrimSpace(reservation.L3CacheAssignment.CacheId)
	if cacheId == "" {
		return nil
	}

	var cacheInfo *types.HostTopologyCache
	for i := range c.caches {
		if c.caches[i].Id == cacheId {
			cacheInfo = &c.caches[i]
			break
		}
	}
	if cacheInfo == nil {
		return fmt.Errorf("component %q cache id %q not found in topology for pqos reset", componentName, cacheId)
	}

	fullMask, err := model.WayInterval{Start: 0, Length: cacheInfo.Ways}.MaskHex()
	if err != nil {
		return fmt.Errorf("failed to compute full-way mask for cache id %q: %w", cacheId, err)
	}

	classCPUSet := reservation.CpuSet()
	args := c.factory.BuildResetArgs(cacheId, cosId, fullMask, classCPUSet)
	_, err = c.runner.Run(ctx, "pqos", args...)
	if err != nil {
		return fmt.Errorf("failed to reset pqos assignment for component %q: %w", componentName, err)
	}

	return nil
}
