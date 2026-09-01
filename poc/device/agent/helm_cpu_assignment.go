package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/margo/sandbox/standard/generatedCode/wfm/sbi"
	yaml "gopkg.in/yaml.v2"
)

// balloonPodAnnotationKey places a pod into an NRI balloon.
const balloonPodAnnotationKey = "balloon.balloons.resource-policy.nri.io/pod"

func (dm *DeploymentManager) resolveComponentBalloonCPUPlan(
	componentName string,
	requiredResources *sbi.RequiredResources,
	ledger *AllocationLedger,
) (CpuPlan, error) {
	ref := ComponentRef(componentName)

	requirements, err := NormalizeCPURequirements(ref, requiredResources)
	if err != nil {
		return CpuPlan{}, err
	}

	dm.log.Debugw("Resolved component CPU requirements from deployment profile for NRI processing",
		"componentName", componentName,
		"hasRequiredResources", requiredResources != nil,
		"sharedRequirementCount", len(requirements.Shared),
		"isolatedRequirementCount", len(requirements.Isolated),
	)

	if !requirements.HasIsolatedCores() || len(dm.topologyLookup.IsolatedCPUIndices) == 0 {
		dm.log.Infow(
			"Component uses shared cores only; skipping NRI annotations and isolated CPU allocations",
			"componentName", componentName,
		)
		return CpuPlan{}, nil
	}

	requiredIsolatedCores := requirements.CountIsolatedCores()
	if requiredIsolatedCores > 1 {
		return CpuPlan{}, fmt.Errorf(
			"component %q requests %d isolated cores; only workloads requiring 1 isolated core are supported",
			componentName, requiredIsolatedCores,
		)
	}

	if dm.policyReader == nil {
		return CpuPlan{}, fmt.Errorf(
			"cannot resolve isolated CPU assignment for component %q: policy reader not configured",
			componentName,
		)
	}

	policy := dm.policyReader.Parsed()
	if policy == nil {
		return CpuPlan{}, fmt.Errorf(
			"cannot resolve isolated CPU assignment for component %q: no BalloonsPolicy snapshot available",
			componentName,
		)
	}

	selectedBalloonName := ""
	selectedBalloonCPUs := []int(nil)

	for _, balloon := range policy.BalloonTypes {
		if balloon.PreferIsolCpus != nil && !*balloon.PreferIsolCpus {
			continue
		}

		refs := uniqueSortedCPURefs(balloon.PreferCloseToDevices)
		if len(refs) < requiredIsolatedCores {
			continue
		}

		occupied := false
		for _, idx := range refs {
			if !ledger.IsCpuAvailable(idx, ref) {
				occupied = true
				break
			}
		}
		if occupied {
			continue
		}

		if len(selectedBalloonCPUs) == 0 || len(refs) < len(selectedBalloonCPUs) {
			selectedBalloonName = balloon.Name
			selectedBalloonCPUs = refs
		}
	}

	if selectedBalloonName == "" || len(selectedBalloonCPUs) == 0 {
		return CpuPlan{}, fmt.Errorf(
			"no free isolated balloon found for component %q (requiredIsolatedCores=%d)",
			componentName, requiredIsolatedCores,
		)
	}

	if err := ledger.ReserveCPUs(ref, selectedBalloonCPUs); err != nil {
		return CpuPlan{}, err
	}

	dm.log.Infow("NRI isolated balloon selected",
		"componentName", componentName,
		"balloonName", selectedBalloonName,
		"selectedCpuIndices", selectedBalloonCPUs,
	)

	return CpuPlan{Assignments: []CpuAssignment{{
		Component: ref,
		Cpus:      selectedBalloonCPUs,
		Placement: CpuPlacement{Class: selectedBalloonName},
	}}}, nil
}

func uniqueSortedCPURefs(paths []string) []int {
	if len(paths) == 0 {
		return nil
	}

	seen := map[int]struct{}{}
	for _, path := range paths {
		matches := cpuIndexRegex.FindAllStringSubmatch(path, -1)
		for _, m := range matches {
			if len(m) != 2 {
				continue
			}
			idx, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			seen[idx] = struct{}{}
		}
	}

	refs := make([]int, 0, len(seen))
	for idx := range seen {
		refs = append(refs, idx)
	}
	sort.Ints(refs)

	return refs
}

func (dm *DeploymentManager) mergePodAnnotations(existing any, annotations map[string]string) map[string]any {
	merged := map[string]any{}

	switch typed := existing.(type) {
	case nil:
		// No existing pod annotations to merge.
	case map[string]string:
		for k, v := range typed {
			merged[k] = v
		}
	case map[string]any:
		for k, v := range typed {
			merged[k] = fmt.Sprintf("%v", v)
		}
	case map[any]any:
		for k, v := range typed {
			key := fmt.Sprintf("%v", k)
			merged[key] = fmt.Sprintf("%v", v)
		}
	default:
		dm.log.Warnw(
			"Existing podAnnotations value has unsupported type; replacing with resolved NRI annotations",
			"type", fmt.Sprintf("%T", existing),
		)
	}

	for k, v := range annotations {
		merged[k] = v
	}

	return merged
}

func (dm *DeploymentManager) generateNriValuesOverrideFile(deploymentID string, componentName string, annotations map[string]string,
	cpuset string,
) (string, error) {
	if len(annotations) == 0 && strings.TrimSpace(cpuset) == "" {
		return "", fmt.Errorf("no NRI values to write")
	}

	payload := map[string]any{
		"podAnnotations": annotations,
	}
	if strings.TrimSpace(cpuset) != "" {
		payload[componentName] = map[string]any{
			"cpuset": cpuset,
		}
	}

	bytes, err := yaml.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal NRI override yaml: %w", err)
	}

	pattern := fmt.Sprintf("nri-override-%s-%s-*.yaml", sanitizeFileToken(componentName), sanitizeFileToken(deploymentID))
	file, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", fmt.Errorf("failed to create temporary NRI override file: %w", err)
	}
	defer file.Close()

	if _, err := file.Write(bytes); err != nil {
		return "", fmt.Errorf("failed to write NRI override yaml: %w", err)
	}

	return filepath.Clean(file.Name()), nil
}

func (dm *DeploymentManager) mergeComponentCPUSet(existing any, cpuset string) map[string]any {
	merged := map[string]any{}

	switch typed := existing.(type) {
	case nil:
		// No existing component values to merge.
	case map[string]any:
		for k, v := range typed {
			merged[k] = v
		}
	case map[string]string:
		for k, v := range typed {
			merged[k] = v
		}
	case map[any]any:
		for k, v := range typed {
			merged[fmt.Sprintf("%v", k)] = v
		}
	default:
		dm.log.Warnw(
			"Existing component value has unsupported type; replacing with resolved cpuset",
			"type", fmt.Sprintf("%T", existing),
		)
	}

	merged["cpuset"] = cpuset
	return merged
}

func (dm *DeploymentManager) logNriAnnotationPlan(componentName string, releaseName string, annotations map[string]string) {
	podKeys := sortedAnnotationPairs(annotations)
	dm.log.Infow("Resolved pod-level NRI annotations",
		"componentName", componentName,
		"releaseName", releaseName,
		"annotations", podKeys,
	)
}

func sortedAnnotationPairs(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, fmt.Sprintf("%s=%s", k, m[k]))
	}
	return pairs
}
