package main

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/margo/sandbox/poc/device/agent/database"
	"github.com/margo/sandbox/standard/generatedCode/wfm/sbi"
)

const (
	maxPQoSClassID = 63
)

func (dm *DeploymentManager) resolveComposeComponentCacheAssignments(
	deploymentID string,
	componentName string,
	requiredResources *sbi.RequiredResources,
	componentCPUAssignments map[string][]int,
	inFlightAssignments map[string][]database.CacheAssignment,
) (map[string][]database.CacheAssignment, bool, error) {
	componentAssignments := map[string][]database.CacheAssignment{}
	if requiredResources == nil || requiredResources.Cache == nil || len(*requiredResources.Cache) == 0 {
		return componentAssignments, false, nil
	}

	exclusiveReqs := make([]sbi.CacheRequirement, 0)
	for _, cacheReq := range *requiredResources.Cache {
		if cacheReq.Level != sbi.CacheRequirementLevelL3 {
			return componentAssignments, false, fmt.Errorf("component %q requests unsupported cache level %q (only L3 is supported)", componentName, cacheReq.Level)
		}
		if cacheReq.Allocation == sbi.CacheRequirementAllocationExclusive {
			exclusiveReqs = append(exclusiveReqs, cacheReq)
		}
	}

	if len(exclusiveReqs) == 0 {
		return componentAssignments, false, nil
	}
	if len(exclusiveReqs) > 1 {
		//TODO: this might not be useful
		return componentAssignments, false, fmt.Errorf("component %q has %d exclusive cache requests; only one exclusive L3 request per component is supported", componentName, len(exclusiveReqs))
	}
	if len(dm.topologyLookup.L3Caches) == 0 {
		dm.log.Warnw("Component requests exclusive L3 cache but topology artifact has no L3 cache entries",
			"componentName", componentName,
			"exclusiveReqs", exclusiveReqs,
		)
		// TODO: commented for now to run pqos
		return componentAssignments, false, fmt.Errorf("component %q requests exclusive L3 cache but topology artifact has no L3 cache entries", componentName)
	}

	hasRDTWays := false
	for _, cache := range dm.topologyLookup.L3Caches {
		if cache.Ways > 0 && cache.WaySizeKB > 0 {
			hasRDTWays = true
			break
		}
	}
	if !hasRDTWays {
		dm.log.Warnw("Component requests exclusive L3 cache but topology artifact has no RDT/CAT ways available",
			"componentName", componentName,
			"exclusiveReqs", exclusiveReqs,
		)
		// TODO: comment for now to run pqos
		return componentAssignments, false, fmt.Errorf("component %q requests exclusive L3 cache but CAT/RDT ways are unavailable on this device (topology ways/way_size_kb are 0)", componentName)
	}

	requiredKi, err := parseBinarySizeKi(exclusiveReqs[0].Size)
	if err != nil {
		return componentAssignments, false, fmt.Errorf("component %q has invalid cache size: %w", componentName, err)
	}

	assignedCPUs := uniqueAssignedCPUs(componentCPUAssignments)
	if len(assignedCPUs) == 0 {
		return componentAssignments, false, fmt.Errorf("component %q requests exclusive cache but has no CPU assignments", componentName)
	}

	// TODO: commenting for now to run pqos
	candidateCaches, err := dm.filterL3CachesByAssignedCPUs(assignedCPUs)
	if err != nil {
		return componentAssignments, false, fmt.Errorf("component %q could not map assigned CPUs to L3 cache IDs: %w", componentName, err)
	}

	persisted := dm.database.AllocatedCaches()

	// TODO: this is used from the helm file, is that a good idea????
	// TODO: I'm commenting all of this so run pqos
	selectedCache, selectedInterval, neededWays, err := pickSmallestFittingCacheInterval(
		candidateCaches,
		persisted,
		inFlightAssignments,
		deploymentID,
		requiredKi,
	)
	if err != nil {
		return componentAssignments, false, fmt.Errorf("component %q cache allocation failed: %w", componentName, err)
	}
	if neededWays <= 0 {
		return componentAssignments, false, fmt.Errorf("component %q computed invalid way count %d", componentName, neededWays)
	}

	wayMask, err := wayMaskHexForInterval(selectedInterval.Start, selectedInterval.Length)
	if err != nil {
		return componentAssignments, false, err
	}

	//TODO: comment to runpqos with hardcoded options
	// wayMask := "0x0"
	// if componentName == "caterpillar" {
	// 	wayMask = "0x00f" // TODO: hardcoded for now, but we should compute this from the selected interval
	// 	dm.log.Infow("Resolved compose exclusive L3 cache assignment",
	// 		"componentName", componentName,
	// 		"assignedCPUs", assignedCPUs,
	// 		"wayMask", wayMask)
	// } else if componentName == "cyclictest" {
	// 	wayMask = "0x00f" // TODO: hardcoded for now, but we should compute this from the selected interval
	// 	dm.log.Infow("Resolved compose exclusive L3 cache assignment",
	// 		"componentName", componentName,
	// 		"assignedCPUs", assignedCPUs,
	// 		"wayMask", wayMask)
	// }

	classID, err := nextAvailablePQoSClassID(persisted, inFlightAssignments)
	if err != nil {
		return componentAssignments, false, fmt.Errorf("component %q class selection failed: %w", componentName, err)
	}

	requirementName := componentName
	componentAssignments[requirementName] = []database.CacheAssignment{{
		ComponentName: componentName,
		Level:         selectedCache.Level,
		// Level: "L3",
		CacheID: selectedCache.ID,
		// CacheID: "0",
		SizeKB:  requiredKi,
		Mask:    wayMask,
		ClassID: classID,
	}}

	dm.log.Infow("Resolved compose exclusive L3 cache assignment",
		"componentName", componentName,
		"assignedCPUs", assignedCPUs,
		"classID", classID,
		"cacheID", selectedCache.ID,
		// "cacheID", "0",
		"requiredKi", requiredKi,
		"neededWays", neededWays,
		// "neededWays", "needed ways status",
		"wayMask", wayMask,
		"selectedInterval", fmt.Sprintf("%d-%d", selectedInterval.Start, selectedInterval.Start+selectedInterval.Length-1),
	)

	return componentAssignments, true, nil
}

func (dm *DeploymentManager) applyComposeComponentPQoS(
	ctx context.Context,
	componentName string,
	cacheAssignments []database.CacheAssignment,
	componentCPUAssignments map[string][]int,
	factory pqosCommandFactory,
) error {
	if len(cacheAssignments) == 0 {
		return nil
	}

	assignedCPUs := uniqueAssignedCPUs(componentCPUAssignments)
	if len(assignedCPUs) == 0 {
		return fmt.Errorf("component %q has no assigned CPUs for pqos association", componentName)
	}

	cpuset := formatCPUSet(assignedCPUs)
	if strings.TrimSpace(cpuset) == "" {
		return fmt.Errorf("component %q resolved empty cpuset for pqos association", componentName)
	}
	// TODO: this usually has only one CLOS assignment, if we were to support
	// multiple CLOS we  need to think about how to associate the cpuset
	for _, assignment := range cacheAssignments {
		if assignment.ClassID <= 0 {
			return fmt.Errorf("component %q has invalid class ID %d", componentName, assignment.ClassID)
		}

		cosID := strconv.Itoa(assignment.ClassID)
		cacheID := strings.TrimSpace(assignment.CacheID)
		if cacheID == "" {
			return fmt.Errorf("component %q has empty cache ID", componentName)
		}
		mask := strings.TrimSpace(assignment.Mask)
		if mask == "" {
			return fmt.Errorf("component %q has empty cache mask", componentName)
		}

		pqosCommand := factory.BuildApplyCommand(cacheID, cosID, mask, cpuset)

		dm.log.Debugw("Applying compose pqos assignment using direct host namespace exec",
			"componentName", componentName,
			"classID", assignment.ClassID,
			"pqosInterface", factory.GetPQoSInterface(),
			"cacheID", cacheID,
			"mask", mask,
			"cpuset", cpuset,
			"pqosCommand", pqosCommand,
		)

		cmd := exec.CommandContext(
			ctx,
			"nsenter",
			"-t",
			"1",
			"-m",
			"-u",
			"-i",
			"-n",
			"-p",
			"--",
			"/bin/sh",
			"-c",
			pqosCommand,
		)

		dm.log.Debugw("Executing pqos command in host namespace",
			"componentName", componentName,
			"classID", assignment.ClassID,
			"pqosInterface", factory.GetPQoSInterface(),
			"cacheID", cacheID,
			"mask", mask,
			"cpuset", cpuset,
			"command", cmd.String(),
		)

		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf(
				"failed to apply pqos assignment for component %q (classId=%d cacheID=%s mask=%s cpuset=%s): %w: %s",
				componentName,
				assignment.ClassID,
				cacheID,
				mask,
				cpuset,
				err,
				strings.TrimSpace(string(output)),
			)
		}

		dm.log.Infow("Applied compose pqos assignment using direct host namespace exec",
			"componentName", componentName,
			"classID", assignment.ClassID,
			"pqosInterface", factory.GetPQoSInterface(),
			"cacheID", cacheID,
			"mask", mask,
			"cpuset", cpuset,
		)
	}

	return nil
}

func (dm *DeploymentManager) resetComposeComponentPQoSMask(
	ctx context.Context,
	componentName string,
	cacheAssignmentsByComponent map[string][]database.CacheAssignment,
	componentCPUAssignments map[string][]int,
	factory pqosCommandFactory,
) error {
	if cacheAssignmentsByComponent == nil {
		return nil
	}

	cacheAssignments := make([]database.CacheAssignment, 0)
	// TODO: look for overuse of trimming the component name
	trimmedComponentName := strings.TrimSpace(componentName)
	if trimmedComponentName == "" {
		return nil
	}

	if direct, ok := cacheAssignmentsByComponent[trimmedComponentName]; ok && len(direct) > 0 {
		cacheAssignments = append(cacheAssignments, direct...)
	} else {
		for _, assignmentList := range cacheAssignmentsByComponent {
			for _, assignment := range assignmentList {
				if strings.TrimSpace(assignment.ComponentName) == trimmedComponentName {
					cacheAssignments = append(cacheAssignments, assignment)
				}
			}
		}
	}

	if len(cacheAssignments) == 0 {
		return nil
	}

	classCPUSet := resolveComponentCPUListFromDB(trimmedComponentName, componentCPUAssignments)

	processed := map[string]struct{}{}
	resetCacheEntries := make([]string, 0, len(cacheAssignments))
	for _, assignment := range cacheAssignments {
		if assignment.ClassID <= 0 {
			dm.log.Warnw("Skipping compose pqos reset due to invalid class ID",
				"componentName", componentName,
				"classID", assignment.ClassID,
				"cacheID", assignment.CacheID)
			continue
		}

		cacheID := strings.TrimSpace(assignment.CacheID)
		if cacheID == "" {
			dm.log.Warnw("Skipping compose pqos reset due to empty cache ID",
				"componentName", componentName,
				"classID", assignment.ClassID)
			continue
		}

		cacheInfo, ok := dm.findL3CacheByID(cacheID)
		if !ok {
			return fmt.Errorf("component %q cache ID %q not found in topology for pqos reset", componentName, cacheID)
		}
		if cacheInfo.Ways <= 0 {
			return fmt.Errorf("component %q cache ID %q has invalid ways=%d for pqos reset", componentName, cacheID, cacheInfo.Ways)
		}

		// reset to use all ways for this cache ID
		resetMask, err := wayMaskHexForInterval(0, cacheInfo.Ways)
		if err != nil {
			return fmt.Errorf("component %q failed to compute full-way mask for cache ID %q: %w", componentName, cacheID, err)
		}

		key := fmt.Sprintf("%s/%d", cacheID, assignment.ClassID)
		if _, exists := processed[key]; exists {
			continue
		}
		processed[key] = struct{}{}

		cosID := strconv.Itoa(assignment.ClassID)
		resetCacheEntry := fmt.Sprintf("llc@%s:%s=%s", cacheID, cosID, resetMask)
		resetCacheEntries = append(resetCacheEntries, resetCacheEntry)

		dm.log.Debugw("Prepared compose pqos class mask reset entry",
			"componentName", componentName,
			"classID", assignment.ClassID,
			"cacheID", cacheID,
			"resetMask", resetMask,
			"classCPUSet", classCPUSet,
			"resetEntry", resetCacheEntry,
		)
	}

	if len(resetCacheEntries) == 0 {
		dm.log.Warnw("No compose pqos reset entries were prepared",
			"componentName", componentName,
			"cacheAssignmentsCount", len(cacheAssignments),
		)
		return nil
	}

	resetSpec := strings.Join(resetCacheEntries, ";")
	pqosCommand := factory.BuildResetCommand(resetSpec, classCPUSet)

	dm.log.Debugw("Resetting compose pqos class masks with single pqos command",
		"componentName", componentName,
		"pqosInterface", factory.GetPQoSInterface(),
		"classCPUSet", classCPUSet,
		"resetSpec", resetSpec,
		"pqosCommand", pqosCommand,
	)

	cmd := exec.CommandContext(
		ctx,
		"nsenter",
		"-t",
		"1",
		"-m",
		"-u",
		"-i",
		"-n",
		"-p",
		"--",
		"/bin/sh",
		"-c",
		pqosCommand,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf(
			"failed to reset pqos class masks/core association for component %q (resetSpec=%s cpuset=%s): %w: %s",
			componentName,
			resetSpec,
			classCPUSet,
			err,
			strings.TrimSpace(string(output)),
		)
	}

	dm.log.Infow("Reset compose pqos class masks",
		"componentName", componentName,
		"pqosInterface", factory.GetPQoSInterface(),
		"resetSpec", resetSpec,
		"cpusetMovedToCos0", classCPUSet,
	)

	return nil
}

func resolveComponentCPUListFromDB(
	componentName string,
	componentCPUAssignments map[string][]int,
) string {
	if len(componentCPUAssignments) == 0 {
		return ""
	}

	componentName = strings.TrimSpace(componentName)
	if componentName == "" {
		return ""
	}

	collected := make([]int, 0)
	seen := map[int]struct{}{}

	appendUnique := func(cpus []int) {
		for _, cpu := range cpus {
			if _, exists := seen[cpu]; exists {
				continue
			}
			seen[cpu] = struct{}{}
			collected = append(collected, cpu)
		}
	}

	if cpus, ok := componentCPUAssignments[componentName]; ok {
		appendUnique(cpus)
	}

	for key, cpus := range componentCPUAssignments {
		if strings.TrimSpace(key) == componentName {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(key), componentName) {
			appendUnique(cpus)
		}
	}

	if len(collected) == 0 {
		return ""
	}

	return formatCPUSet(collected)
}

func nextAvailablePQoSClassID(
	persisted []database.OwnedCacheAssignment,
	inFlight map[string][]database.CacheAssignment,
) (int, error) {
	used := map[int]struct{}{}

	for _, owned := range persisted {
		if owned.Assignment.ClassID > 0 {
			used[owned.Assignment.ClassID] = struct{}{}
		}
	}

	for _, assignments := range inFlight {
		for _, assignment := range assignments {
			if assignment.ClassID > 0 {
				used[assignment.ClassID] = struct{}{}
			}
		}
	}

	candidateIDs := make([]int, 0, len(used))
	for id := range used {
		candidateIDs = append(candidateIDs, id)
	}
	sort.Ints(candidateIDs)

	for classID := 1; classID <= maxPQoSClassID; classID++ {
		if _, exists := used[classID]; exists {
			continue
		}
		return classID, nil
	}

	return 0, fmt.Errorf("no free pqos class id available in range 1-%d", maxPQoSClassID)
}
