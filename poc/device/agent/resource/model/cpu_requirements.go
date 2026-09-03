package model

import (
	"fmt"
	"math"

	"github.com/margo/sandbox/standard/generatedCode/wfm/sbi"
)

type CpuMode string

const (
	CpuModeShared   CpuMode = "shared"
	CpuModeIsolated CpuMode = "isolated"
)

// this is one cpu request after normalization: an explicit mode, whole
// cores, and no generated-SBI pointers for the planners to unwrap
type CpuRequirement struct {
	Mode  CpuMode
	Cores int
}

// represents one component's cpu requests. Component membership is
// structural - requiredResources is nested inside the component - so
// requiredResources[].name is neither read nor matched against the component name
type NormalizedCpuRequirements struct {
	Component ComponentRef
	Shared    []CpuRequirement
	Isolated  []CpuRequirement
}

func (r NormalizedCpuRequirements) HasSharedCores() bool { return len(r.Shared) > 0 }

func (r NormalizedCpuRequirements) HasIsolatedCores() bool { return len(r.Isolated) > 0 }

func (r NormalizedCpuRequirements) CountIsolatedCores() int {
	total := 0
	for _, requirement := range r.Isolated {
		total += requirement.Cores
	}
	return total
}

// normalizes, classifies and validates one component's cpu requests. It
// is the only place that reads the generated SBI cpu shape
func NormalizeCpuRequirements(
	ref ComponentRef,
	requiredResources *sbi.RequiredResources,
) (NormalizedCpuRequirements, error) {
	normalized := NormalizedCpuRequirements{Component: ref}
	if requiredResources == nil || requiredResources.Cpu == nil {
		return normalized, nil
	}

	for _, req := range *requiredResources.Cpu {
		requirement := CpuRequirement{Mode: CpuModeShared, Cores: normalizeCores(req.Cores)}
		if req.Type != nil && *req.Type == sbi.CpuTypeIsolated {
			requirement.Mode = CpuModeIsolated
			normalized.Isolated = append(normalized.Isolated, requirement)
			continue
		}
		normalized.Shared = append(normalized.Shared, requirement)
	}

	// one component deploys one unit with one cpuset, so a second isolated requirement
	// has nowhere to go and would otherwise alias onto the first one's cpu
	if len(normalized.Isolated) > 1 {
		return NormalizedCpuRequirements{}, fmt.Errorf(
			"component %q declares %d isolated cpu requirements; only one is supported",
			ref, len(normalized.Isolated),
		)
	}

	if normalized.HasSharedCores() && normalized.HasIsolatedCores() {
		return NormalizedCpuRequirements{}, fmt.Errorf(
			"component %q mixes shared and isolated cpu requirements; this combination is not supported",
			ref,
		)
	}

	return normalized, nil
}

// rounds a fractional core request up. An absent or non-positive request means one core
func normalizeCores(cores *float32) int {
	if cores == nil || *cores <= 0 {
		return 1
	}
	return int(math.Ceil(float64(*cores)))
}
