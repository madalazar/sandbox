package planner

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/margo/sandbox/poc/device/agent/resource/ledger"
	"github.com/margo/sandbox/poc/device/agent/resource/model"
	"github.com/margo/sandbox/poc/device/agent/types"
)

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
	if !request.Requirements.HasCache() || request.Requirements.L3CacheRequirement == nil {
		return model.CachePlan{
			Component: request.Requirements.Component,
		}, nil
	}

	req := request.Requirements.L3CacheRequirement
	if !strings.EqualFold(req.Level, "L3") {
		return model.CachePlan{}, fmt.Errorf("component %q requests cache level %q; only L3 cache is supported", request.Requirements.Component, req.Level)
	}
	if req.Allocation != model.CacheAllocationExclusive {
		return model.CachePlan{}, fmt.Errorf("component %q requests cache allocation mode %q; only exclusive is supported", request.Requirements.Component, req.Allocation)
	}
	if req.SizeKiB <= 0 {
		return model.CachePlan{}, fmt.Errorf("component %q requests non-positive cache size %d KiB", request.Requirements.Component, req.SizeKiB)
	}

	if request.Ledger == nil {
		return model.CachePlan{}, errors.New("allocation ledger is required for cache planning")
	}

	// 1. map assigned cpus to candidate l3 caches
	candidateCaches, err := filterL3CachesByAssignedCpus(p.caches, request.CpuPlan.Cpus)
	if err != nil {
		return model.CachePlan{}, err
	}

	// 2. pick smallest fitting cache interval
	selectedCache, selectedInterval, err := pickSmallestFittingCacheInterval(
		candidateCaches,
		request.Ledger,
		request.Requirements.Component,
		req.SizeKiB,
	)
	if err != nil {
		return model.CachePlan{}, err
	}

	// 3. generate way mask
	maskHex, err := selectedInterval.MaskHex()
	if err != nil {
		return model.CachePlan{}, err
	}

	masks := map[string]string{
		selectedCache.Id: maskHex,
	}

	// 4. when placed into an NRI balloon (Helm/k3s RDT), pick non-overlapping 1-way
	// masks on remaining L3 caches as required by the NRI plugin
	if request.CpuPlan.PlacementClass() != "" {
		for _, cache := range p.caches {
			if cache.Id == "" || cache.Id == selectedCache.Id {
				continue
			}
			_, fillerInterval, err := pickSmallestFittingCacheInterval([]types.HostTopologyCache{cache}, request.Ledger, request.Requirements.Component, cache.WaySizeKB)
			if err != nil {
				return model.CachePlan{}, fmt.Errorf("no free filler way on L3 cache %q: %w", cache.Id, err)
			}
			if masks[cache.Id], err = fillerInterval.MaskHex(); err != nil {
				return model.CachePlan{}, err
			}
		}
	}

	// 5. reserve class slot on ledger
	clos, err := request.Ledger.ReserveClass(request.Requirements.Component)
	if err != nil {
		return model.CachePlan{}, err
	}

	assignment := &model.CacheAssignment{
		Level:   selectedCache.Level,
		CacheId: selectedCache.Id,
		SizeKiB: selectedInterval.Length * selectedCache.WaySizeKB,
		Masks:   masks,
		Clos:    clos,
	}

	fmt.Printf("[cache planner] cache assignment %+v, clos: %v\n", assignment, clos)

	return model.CachePlan{
		Component:         request.Requirements.Component,
		L3CacheAssignment: assignment,
	}, nil
}

func filterL3CachesByAssignedCpus(caches []types.HostTopologyCache, assignedCpus []int) ([]types.HostTopologyCache, error) {
	if len(assignedCpus) == 0 {
		return caches, nil
	}

	assignedSet := make(map[int]struct{}, len(assignedCpus))
	for _, cpu := range assignedCpus {
		assignedSet[cpu] = struct{}{}
	}

	candidates := make([]types.HostTopologyCache, 0)
	for _, cache := range caches {
		cores, err := model.ParseCpuSet(cache.Cores)
		if err != nil {
			return nil, fmt.Errorf("invalid cores range %q for cache id %q: %w", cache.Cores, cache.Id, err)
		}
		if len(cores) == 0 {
			continue
		}

		for _, cpu := range cores {
			if _, ok := assignedSet[cpu]; ok {
				candidates = append(candidates, cache)
				break
			}
		}
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no l3 cache from topology matches assigned cpus %v", assignedCpus)
	}

	return candidates, nil
}

func pickSmallestFittingCacheInterval(
	caches []types.HostTopologyCache,
	l *ledger.AllocationLedger,
	ref model.ComponentRef,
	requiredKiB int64,
) (types.HostTopologyCache, model.WayInterval, error) {
	var bestCache types.HostTopologyCache
	var bestInterval model.WayInterval
	bestIntervalFound := false
	bestLength := int64(math.MaxInt64)
	bestCacheID := ""

	for _, cache := range caches {
		if cache.WaySizeKB <= 0 || cache.Ways <= 0 {
			continue
		}

		neededWays := (requiredKiB + cache.WaySizeKB - 1) / cache.WaySizeKB
		if neededWays <= 0 || neededWays > cache.Ways {
			continue
		}

		intervals := l.FreeWays(cache.Id, ref)
		for _, interval := range intervals {
			if interval.Length < neededWays {
				continue
			}

			isBetter := !bestIntervalFound ||
				interval.Length < bestLength ||
				(interval.Length == bestLength && (cache.Id < bestCacheID || (cache.Id == bestCacheID && interval.Start < bestInterval.Start)))

			if isBetter {
				bestCache = cache
				bestInterval = model.WayInterval{Start: interval.Start, Length: neededWays}
				bestIntervalFound = true
				bestLength = interval.Length
				bestCacheID = cache.Id
			}
		}
	}

	if !bestIntervalFound {
		return types.HostTopologyCache{}, model.WayInterval{}, fmt.Errorf("no contiguous L3 cache interval can fit %d KiB: %w", requiredKiB, ledger.ErrCapacityExhausted)
	}

	return bestCache, bestInterval, nil
}
