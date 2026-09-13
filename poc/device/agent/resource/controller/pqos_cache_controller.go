package controller

import (
	"context"
	"fmt"
	"math/big"
	"strings"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
	"github.com/margo/sandbox/poc/device/agent/types"
)

var _ CacheIsolationController = (*PqosCacheController)(nil)

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

	cosId := string(reservation.Clos)
	if cosId == "" {
		cosId = string(alloc.Clos)
	}
	if cosId == "" || cosId == string(model.ClassUnset) || cosId == defaultPqosCosId {
		return fmt.Errorf("component %q has invalid or unset class id %q", componentName, cosId)
	}

	cacheID := strings.TrimSpace(alloc.CacheId)
	if cacheID == "" {
		return fmt.Errorf("component %q has empty cache id", componentName)
	}

	mask := strings.TrimSpace(alloc.Mask)
	if mask == "" {
		return fmt.Errorf("component %q has empty cache mask", componentName)
	}

	cpuset := reservation.CpuSet()
	if strings.TrimSpace(cpuset) == "" {
		return fmt.Errorf("component %q has no assigned cpus for pqos association", componentName)
	}

	cmd := c.factory.BuildApplyCommand(cacheID, cosId, mask, cpuset)
	_, err := c.runner.Run(ctx, "/bin/sh", "-c", cmd)
	if err != nil {
		return fmt.Errorf("failed to apply pqos assignment for component %q: %w", componentName, err)
	}

	return nil
}

// verifies that the live pqos allocation matches the committed reservation
func (c *PqosCacheController) Verify(ctx context.Context, reservation model.Reservation) error {
	return errNotImplemented
}

// resets the cache way mask to all ways and moves cores back to default COS 0
func (c *PqosCacheController) Release(ctx context.Context, reservation model.Reservation) error {
	if !reservation.HasL3Cache() || reservation.L3CacheAssignment == nil {
		return nil
	}

	componentName := string(reservation.Owner.Component)
	cosId := string(reservation.Clos)
	if cosId == "" {
		cosId = string(reservation.L3CacheAssignment.Clos)
	}
	if cosId == "" || cosId == string(model.ClassUnset) || cosId == defaultPqosCosId {
		return nil
	}

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
	if cacheInfo.Ways <= 0 {
		return fmt.Errorf("component %q cache id %q has invalid ways=%d for pqos reset", componentName, cacheId, cacheInfo.Ways)
	}

	fullMask, err := fullWayMaskHex(cacheInfo.Ways)
	if err != nil {
		return fmt.Errorf("failed to compute full-way mask for cache id %q: %w", cacheId, err)
	}

	classCPUSet := reservation.CpuSet()
	cmd := c.factory.BuildResetCommand(cacheId, cosId, fullMask, classCPUSet)
	_, err = c.runner.Run(ctx, "/bin/sh", "-c", cmd)
	if err != nil {
		return fmt.Errorf("failed to reset pqos assignment for component %q: %w", componentName, err)
	}

	return nil
}

func fullWayMaskHex(ways int64) (string, error) {
	if ways <= 0 {
		return "", fmt.Errorf("cache ways must be > 0")
	}
	mask := new(big.Int).Lsh(big.NewInt(1), uint(ways))
	mask.Sub(mask, big.NewInt(1))
	return "0x" + strings.ToUpper(mask.Text(16)), nil
}
