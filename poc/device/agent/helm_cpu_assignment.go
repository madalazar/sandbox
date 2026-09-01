package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/margo/sandbox/standard/generatedCode/wfm/sbi"
	yaml "gopkg.in/yaml.v2"
)

func (dm *DeploymentManager) resolveComponentBalloonAnnotations(deploymentID string, componentName string,
	requiredResources *sbi.RequiredResources, inFlightAssignments map[string][]int,
) (map[string]string, map[string][]int, bool, error) {

	annotations := map[string]string{}
	currentAssignments := map[string][]int{}
	owner := NewOwnerRef(deploymentID, componentName)

	componentCPUReqs := componentCPURequirements(requiredResources)
	dm.log.Debugw("Resolved component CPU requirements from deployment profile for NRI processing",
		"componentName", componentName,
		"hasRequiredResources", requiredResources != nil,
		"cpuRequirementCount", len(componentCPUReqs),
		"cpuRequirements", summarizeCpuRequirements(componentCPUReqs),
	)

	if len(componentCPUReqs) == 0 {
		dm.log.Infow("Skipping NRI processing: component has no matching deployment-profile CPU requirements",
			"componentName", componentName,
		)
		return annotations, currentAssignments, false, nil
	}

	sharedReqs := make([]sbi.Cpu, 0, len(componentCPUReqs))
	isolatedReqs := make([]sbi.Cpu, 0, len(componentCPUReqs))
	for _, req := range componentCPUReqs {
		if req.Type != nil && *req.Type == sbi.CpuTypeIsolated {
			isolatedReqs = append(isolatedReqs, req)
			continue
		}
		sharedReqs = append(sharedReqs, req)
	}

	if len(sharedReqs) > 0 && len(isolatedReqs) > 0 {
		return annotations, currentAssignments, false, fmt.Errorf(
			"component %q mixes shared and isolated CPU requirements; this combination is not supported",
			componentName,
		)
	}

	if len(isolatedReqs) == 0 {
		dm.log.Infow(
			"Component uses shared cores only; skipping NRI annotations and isolated CPU allocations",
			"componentName", componentName,
		)
		return annotations, currentAssignments, false, nil
	}

	requiredIsolatedCores := int64(0)
	for _, req := range isolatedReqs {
		cores := int64(1)
		if req.Cores != nil && *req.Cores > 0 {
			cores = int64(math.Ceil(float64(*req.Cores)))
		}
		requiredIsolatedCores += cores
	}
	if requiredIsolatedCores > 1 {
		return annotations, currentAssignments, false, fmt.Errorf(
			"component %q requests %d isolated cores; only workloads requiring 1 isolated core are supported",
			componentName, requiredIsolatedCores,
		)
	}

	allocatedIsolated := map[int]OwnerRef{}
	for idx, holder := range allocatedCPUOwners(dm.database.AllocatedCpus()) {
		if _, exists := dm.topologyLookup.IsolatedCPUSet[idx]; !exists {
			continue
		}
		allocatedIsolated[idx] = holder
	}

	for requirement, cpus := range inFlightAssignments {
		holder := NewOwnerRef(deploymentID, requirement)

		for _, idx := range cpus {
			if _, exists := dm.topologyLookup.IsolatedCPUSet[idx]; !exists {
				continue
			}
			allocatedIsolated[idx] = holder
		}
	}

	if dm.policyReader == nil {
		return annotations, currentAssignments, false, fmt.Errorf(
			"cannot resolve isolated CPU assignment for component %q: policy reader not configured",
			componentName,
		)
	}

	policy := dm.policyReader.Parsed()
	if policy == nil {
		return annotations, currentAssignments, false, fmt.Errorf(
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
		if len(refs) < int(requiredIsolatedCores) {
			continue
		}

		hasAllocatedCPU := false
		for _, idx := range refs {
			holder, exists := allocatedIsolated[idx]
			if !exists {
				continue
			}
			if !owner.CanTake(holder) {
				hasAllocatedCPU = true
				break
			}
		}
		if hasAllocatedCPU {
			continue
		}

		if len(selectedBalloonCPUs) == 0 || len(refs) < len(selectedBalloonCPUs) {
			selectedBalloonName = balloon.Name
			selectedBalloonCPUs = refs
		}
	}

	if selectedBalloonName == "" || len(selectedBalloonCPUs) == 0 {
		return annotations, currentAssignments, false, fmt.Errorf(
			"no free isolated balloon found for component %q (requiredIsolatedCores=%d)",
			componentName, requiredIsolatedCores,
		)
	}

	componentAssignments := map[string][]int{string(owner.Ref): selectedBalloonCPUs}
	annotations["balloon.balloons.resource-policy.nri.io/pod"] = selectedBalloonName

	dm.log.Infow("NRI isolated balloon selected",
		"componentName", componentName,
		"requirementName", string(owner.Ref),
		"balloonName", selectedBalloonName,
		"selectedCpuIndices", selectedBalloonCPUs,
	)

	return annotations, componentAssignments, true, nil
}

func summarizeCpuRequirements(reqs []sbi.Cpu) []map[string]any {
	out := make([]map[string]any, 0, len(reqs))
	for _, req := range reqs {
		entry := map[string]any{}
		if req.Name != nil {
			entry["containerName"] = strings.TrimSpace(*req.Name)
		} else {
			entry["containerName"] = ""
		}
		if req.Class != nil {
			entry["class"] = string(*req.Class)
		}
		if req.Type != nil {
			entry["type"] = string(*req.Type)
		}
		if req.Cores != nil {
			entry["cores"] = *req.Cores
		}
		out = append(out, entry)
	}
	return out
}

// Membership is structural - requiredResources is nested inside the component - so
// requiredResources[].name is neither read nor matched against the component name.
func componentCPURequirements(requiredResources *sbi.RequiredResources) []sbi.Cpu {
	if requiredResources == nil || requiredResources.Cpu == nil || len(*requiredResources.Cpu) == 0 {
		return nil
	}

	return append([]sbi.Cpu(nil), *requiredResources.Cpu...)
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
