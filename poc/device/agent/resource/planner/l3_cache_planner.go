package planner

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
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
			Clos:      model.ClassUnset,
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
	selectedCache, selectedInterval, neededWays, err := pickSmallestFittingCacheInterval(
		candidateCaches,
		request.Ledger,
		request.Requirements.Component,
		req.SizeKiB,
	)
	if err != nil {
		return model.CachePlan{}, err
	}

	// 3. generate way mask
	maskHex, err := wayMaskHexForInterval(selectedInterval.Start, selectedInterval.Length)
	if err != nil {
		return model.CachePlan{}, err
	}

	// 4. reserve ways on ledger
	if err := request.Ledger.ReserveWays(request.Requirements.Component, selectedCache.Id, selectedInterval); err != nil {
		return model.CachePlan{}, err
	}

	// 5. reserve class slot on ledger
	clos, err := request.Ledger.ReserveClass(request.Requirements.Component)
	if err != nil {
		return model.CachePlan{}, err
	}

	assignment := &model.CacheAssignment{
		Level:    selectedCache.Level,
		CacheId:  selectedCache.Id,
		SizeKiB:  neededWays * selectedCache.WaySizeKB,
		Interval: selectedInterval,
		Mask:     maskHex,
		Clos:     clos,
	}

	return model.CachePlan{
		Component:         request.Requirements.Component,
		L3CacheAssignment: assignment,
		Clos:              clos,
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
		cores, err := parseCpuRangeList(cache.Cores)
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
) (types.HostTopologyCache, model.WayInterval, int64, error) {
	var bestCache types.HostTopologyCache
	var bestInterval model.WayInterval
	bestIntervalFound := false
	bestLength := int64(math.MaxInt64)
	bestCacheID := ""
	bestNeededWays := int64(0)

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
				bestNeededWays = neededWays
			}
		}
	}

	if !bestIntervalFound {
		return types.HostTopologyCache{}, model.WayInterval{}, 0, fmt.Errorf("no contiguous L3 cache interval can fit %d KiB: %w", requiredKiB, ledger.ErrCapacityExhausted)
	}

	return bestCache, bestInterval, bestNeededWays, nil
}

func maskWays(mask string, maxWays int64) []int64 {
	parsed := new(big.Int)
	if _, ok := parsed.SetString(strings.TrimSpace(mask), 0); !ok {
		return nil
	}

	ways := make([]int64, 0)
	for bit := range maxWays {
		if parsed.Bit(int(bit)) == 1 {
			ways = append(ways, bit)
		}
	}

	return ways
}

func wayMaskHexForInterval(start, length int64) (string, error) {
	if start < 0 {
		return "", fmt.Errorf("interval start must be >= 0")
	}
	if length <= 0 {
		return "", fmt.Errorf("interval length must be > 0")
	}

	mask := new(big.Int).Lsh(big.NewInt(1), uint(length))
	mask.Sub(mask, big.NewInt(1))
	mask.Lsh(mask, uint(start))

	return fmt.Sprintf("0x%X", mask), nil
}

func parseCpuRangeList(raw string) ([]int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	parts := strings.Split(raw, ",")
	seen := make(map[int]struct{}, len(parts))
	out := make([]int, 0, len(parts))

	for _, part := range parts {
		token := strings.TrimSpace(part)
		if token == "" {
			continue
		}

		var start, end int
		if startStr, endStr, hasRange := strings.Cut(token, "-"); hasRange {
			var err error
			start, err = strconv.Atoi(strings.TrimSpace(startStr))
			if err != nil {
				return nil, fmt.Errorf("invalid cpu core range start %q: %w", token, err)
			}
			end, err = strconv.Atoi(strings.TrimSpace(endStr))
			if err != nil {
				return nil, fmt.Errorf("invalid cpu core range end %q: %w", token, err)
			}
			if start < 0 || end < 0 || start > end {
				return nil, fmt.Errorf("invalid cpu core range %q", token)
			}
		} else {
			var err error
			start, err = strconv.Atoi(token)
			if err != nil {
				return nil, fmt.Errorf("invalid cpu core index %q: %w", token, err)
			}
			if start < 0 {
				return nil, fmt.Errorf("invalid negative cpu core index %d", start)
			}
			end = start
		}

		for idx := start; idx <= end; idx++ {
			if _, exists := seen[idx]; !exists {
				seen[idx] = struct{}{}
				out = append(out, idx)
			}
		}
	}

	sort.Ints(out)
	return out, nil
}
