package main

import (
	"fmt"
	"math"

	"github.com/margo/sandbox/standard/generatedCode/wfm/sbi"
)

type CPUMode string

const (
	CPUModeShared   CPUMode = "shared"
	CPUModeIsolated CPUMode = "isolated"
)

// CPURequirement is one CPU request after normalization: an explicit mode, whole
// cores, and no generated-SBI pointers for the planners to unwrap.
type CPURequirement struct {
	Mode  CPUMode
	Cores int
}

// NormalizedCPURequirements is one component's CPU requests. Component membership is
// structural - requiredResources is nested inside the component - so
// requiredResources[].name is neither read nor matched against the component name.
type NormalizedCPURequirements struct {
	Component ComponentRef
	Shared    []CPURequirement
	Isolated  []CPURequirement
}

func (r NormalizedCPURequirements) HasSharedCores() bool { return len(r.Shared) > 0 }

func (r NormalizedCPURequirements) HasIsolatedCores() bool { return len(r.Isolated) > 0 }

func (r NormalizedCPURequirements) CountIsolatedCores() int {
	total := 0
	for _, requirement := range r.Isolated {
		total += requirement.Cores
	}
	return total
}

// NormalizeCPURequirements classifies and validates one component's CPU requests. It
// is the only place that reads the generated SBI CPU shape.
func NormalizeCPURequirements(
	ref ComponentRef,
	requiredResources *sbi.RequiredResources,
) (NormalizedCPURequirements, error) {
	normalized := NormalizedCPURequirements{Component: ref}
	if requiredResources == nil || requiredResources.Cpu == nil {
		return normalized, nil
	}

	for _, req := range *requiredResources.Cpu {
		requirement := CPURequirement{Mode: CPUModeShared, Cores: normalizeCores(req.Cores)}
		if req.Type != nil && *req.Type == sbi.CpuTypeIsolated {
			requirement.Mode = CPUModeIsolated
			normalized.Isolated = append(normalized.Isolated, requirement)
			continue
		}
		normalized.Shared = append(normalized.Shared, requirement)
	}

	// One component deploys one unit with one cpuset, so a second isolated requirement
	// has nowhere to go and would otherwise alias onto the first one's CPUs.
	if len(normalized.Isolated) > 1 {
		return NormalizedCPURequirements{}, fmt.Errorf(
			"component %q declares %d isolated CPU requirements; only one is supported",
			ref, len(normalized.Isolated),
		)
	}

	if normalized.HasSharedCores() && normalized.HasIsolatedCores() {
		return NormalizedCPURequirements{}, fmt.Errorf(
			"component %q mixes shared and isolated CPU requirements; this combination is not supported",
			ref,
		)
	}

	return normalized, nil
}

// normalizeCores rounds a fractional core request up. An absent or non-positive
// request means one core.
func normalizeCores(cores *float32) int {
	if cores == nil || *cores <= 0 {
		return 1
	}
	return int(math.Ceil(float64(*cores)))
}
