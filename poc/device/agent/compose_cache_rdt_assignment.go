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
	defaultPQoSHelperImage = "pqos-helper:local"
	maxPQoSClassID         = 63
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

	exclusiveReqs := make([]sbi.Cache, 0)
	for _, cacheReq := range *requiredResources.Cache {
		if cacheReq.Level != sbi.L3 {
			return componentAssignments, false, fmt.Errorf("component %q requests unsupported cache level %q (only L3 is supported)", componentName, cacheReq.Level)
		}
		if cacheReq.Allocation == sbi.CacheAllocationExclusive {
			exclusiveReqs = append(exclusiveReqs, cacheReq)
		}
	}

	if len(exclusiveReqs) == 0 {
		return componentAssignments, false, nil
	}
	if len(exclusiveReqs) > 1 {
		return componentAssignments, false, fmt.Errorf("component %q has %d exclusive cache requests; only one exclusive L3 request per component is supported", componentName, len(exclusiveReqs))
	}
	if len(dm.topologyLookup.L3Caches) == 0 {
		return componentAssignments, false, fmt.Errorf("component %q requests exclusive L3 cache but topology artifact has no L3 cache entries", componentName)
	}

	requiredKi, err := parseBinarySizeKi(exclusiveReqs[0].Size)
	if err != nil {
		return componentAssignments, false, fmt.Errorf("component %q has invalid cache size: %w", componentName, err)
	}

	assignedCPUs := uniqueAssignedCPUs(componentCPUAssignments)
	if len(assignedCPUs) == 0 {
		return componentAssignments, false, fmt.Errorf("component %q requests exclusive cache but has no CPU assignments", componentName)
	}

	candidateCaches, err := dm.filterL3CachesByAssignedCPUs(assignedCPUs)
	if err != nil {
		return componentAssignments, false, fmt.Errorf("component %q could not map assigned CPUs to L3 cache IDs: %w", componentName, err)
	}

	persisted := dm.database.AllocatedCaches()
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

	classID, err := nextAvailablePQoSClassID(persisted, inFlightAssignments)
	if err != nil {
		return componentAssignments, false, fmt.Errorf("component %q class selection failed: %w", componentName, err)
	}

	requirementName := componentName
	componentAssignments[requirementName] = []database.CacheAssignment{{
		ComponentName: componentName,
		Level:         selectedCache.Level,
		CacheID:       selectedCache.ID,
		SizeKB:        requiredKi,
		Mask:          wayMask,
		ClassID:       classID,
	}}

	dm.log.Infow("Resolved compose exclusive L3 cache assignment",
		"componentName", componentName,
		"assignedCPUs", assignedCPUs,
		"classID", classID,
		"cacheID", selectedCache.ID,
		"requiredKi", requiredKi,
		"neededWays", neededWays,
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

		pqosCommand := fmt.Sprintf(
			"modprobe msr >/dev/null 2>&1 || true; pqos -e 'llc@%s:%s=%s' -a 'core:%s=%s'",
			cacheID,
			cosID,
			mask,
			cosID,
			cpuset,
		)

		cmd := exec.CommandContext(
			ctx,
			"docker",
			"run",
			"--rm",
			"--privileged",
			"--pid=host",
			"--network=host",
			defaultPQoSHelperImage,
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

		dm.log.Infow("Applied compose pqos assignment using docker-socket host exec",
			"componentName", componentName,
			"classID", assignment.ClassID,
			"cacheID", cacheID,
			"mask", mask,
			"cpuset", cpuset,
		)
	}

	return nil
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
