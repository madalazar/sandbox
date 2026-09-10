package model

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/margo/sandbox/standard/generatedCode/wfm/sbi"
)

// represents the allocation mode for cache resources
type CacheAllocationMode string

const (
	CacheAllocationExclusive CacheAllocationMode = "exclusive"
	CacheAllocationShared    CacheAllocationMode = "shared"
)

// represents one normalized cache request for a component
type CacheRequirement struct {
	Level      string
	Allocation CacheAllocationMode
	SizeKiB    int64
}

// represents one component's normalized cache requests
type NormalizedCacheRequirements struct {
	Component          ComponentRef
	L3CacheRequirement *CacheRequirement
}

// reports whether any cache requirements are present
func (r NormalizedCacheRequirements) HasCache() bool {
	return r.L3CacheRequirement != nil
}

// normalizes and validates cache requests for a component from SBI
// the current scope, only exclusive l3 cache requirements are supported,
// with at most one l3 requirement per component
func NormalizeCacheRequirements(
	ref ComponentRef,
	requiredResources *sbi.RequiredResources,
) (NormalizedCacheRequirements, error) {
	normalized := NormalizedCacheRequirements{Component: ref}
	if requiredResources == nil || requiredResources.Cache == nil {
		return normalized, nil
	}

	exclusiveReqs := make([]sbi.Cache, 0)
	// filter only exclusive l3 level cache requirements
	for _, req := range *requiredResources.Cache {
		levelStr := string(req.Level)
		if req.Level != sbi.CacheLevelL3 {
			return NormalizedCacheRequirements{}, fmt.Errorf(
				"component %q requests cache level %q; only l3 cache is supported",
				ref, levelStr,
			)
		}

		if req.Allocation == sbi.CacheAllocationExclusive {
			exclusiveReqs = append(exclusiveReqs, req)
		}
	}

	if len(exclusiveReqs) == 0 {
		fmt.Printf("component %q requires no exclusive l3 cache allocations, will proceed to use a shared clos\n", ref)
		return NormalizedCacheRequirements{}, nil
	}

	if len(exclusiveReqs) > 1 {
		return NormalizedCacheRequirements{},
			fmt.Errorf("component %q has %d exclusive l3 cache requests; only one request is supported",
				ref, len(exclusiveReqs))
	}

	sizeKiB, err := parseBinarySizeKi(exclusiveReqs[0].Size)
	if err != nil {
		return NormalizedCacheRequirements{}, fmt.Errorf("component %q  requires valid l3 cache size: %w", ref, err)
	}

	normalized.L3CacheRequirement = &CacheRequirement{
		Level:      string(exclusiveReqs[0].Level),
		Allocation: CacheAllocationExclusive,
		SizeKiB:    sizeKiB,
	}

	return normalized, nil
}

// parses a binary unit string (ki, mi, gi) into kib (kibibytes)
func parseBinarySizeKi(raw *string) (int64, error) {
	if raw == nil {
		return 0, fmt.Errorf("size is required")
	}
	normalized := strings.ReplaceAll(strings.TrimSpace(*raw), " ", "")
	if normalized == "" {
		return 0, fmt.Errorf("size is empty")
	}

	unitMultiplier := map[string]float64{
		"KI": 1,
		"MI": 1024,
		"GI": 1024 * 1024,
	}

	upper := strings.ToUpper(normalized)
	for suffix, multiplier := range unitMultiplier {
		if !strings.HasSuffix(upper, suffix) {
			continue
		}

		numberPart := strings.TrimSpace(normalized[:len(normalized)-len(suffix)])
		if numberPart == "" {
			return 0, fmt.Errorf("missing numeric value in %q", *raw)
		}

		value, err := strconv.ParseFloat(numberPart, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid numeric value %q", numberPart)
		}
		if value <= 0 {
			return 0, fmt.Errorf("size must be > 0 in %q", *raw)
		}
		return int64(math.Ceil(value * multiplier)), nil
	}

	return 0, fmt.Errorf("unsupported size unit in %q (expected ki, mi, or gi)", *raw)
}
