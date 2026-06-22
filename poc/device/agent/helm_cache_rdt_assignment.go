package main

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/margo/sandbox/poc/device/agent/database"
	"github.com/margo/sandbox/poc/device/agent/device"
	"github.com/margo/sandbox/standard/generatedCode/wfm/sbi"
)

const rdtClassAnnotationKey = "rdtclass.resource-policy.nri.io/pod"

func (dm *DeploymentManager) resolveComponentCacheAnnotations(
	ctx context.Context,
	deploymentID string,
	componentName string,
	requiredResources *sbi.RequiredResources,
	componentCPUAssignments map[string][]int,
	inFlightAssignments map[string][]database.CacheAssignment,
) (map[string]string, map[string][]database.CacheAssignment, bool, error) {
	annotations := map[string]string{}
	componentAssignments := map[string][]database.CacheAssignment{}
	if requiredResources == nil || requiredResources.Cache == nil || len(*requiredResources.Cache) == 0 {
		return annotations, componentAssignments, false, nil
	}

	exclusiveReqs := make([]sbi.Cache, 0)
	for _, cacheReq := range *requiredResources.Cache {
		if cacheReq.Level != sbi.L3 {
			return annotations, componentAssignments, false, fmt.Errorf("component %q requests unsupported cache level %q (only L3 is supported)", componentName, cacheReq.Level)
		}

		if cacheReq.Allocation == sbi.CacheAllocationExclusive {
			exclusiveReqs = append(exclusiveReqs, cacheReq)
		}
	}

	if len(exclusiveReqs) == 0 {
		dm.log.Infow("Component uses shared L3 cache only; skipping RDT class allocation", "componentName", componentName)
		return annotations, componentAssignments, false, nil
	}

	if len(exclusiveReqs) > 1 {
		return annotations, componentAssignments, false, fmt.Errorf("component %q has %d exclusive cache requests; only one exclusive L3 request per component is supported", componentName, len(exclusiveReqs))
	}

	if len(dm.topologyLookup.L3Caches) == 0 {
		return annotations, componentAssignments, false, fmt.Errorf("component %q requests exclusive L3 cache but topology artifact has no L3 cache entries", componentName)
	}

	requiredKi, err := parseBinarySizeKi(exclusiveReqs[0].Size)
	if err != nil {
		return annotations, componentAssignments, false, fmt.Errorf("component %q has invalid cache size: %w", componentName, err)
	}

	allAllocations := dm.database.AllocatedCaches()
	candidateCaches := dm.topologyLookup.L3Caches
	assignedCPUs := uniqueAssignedCPUs(componentCPUAssignments)
	if len(assignedCPUs) > 0 {
		mappedCaches, mapErr := dm.filterL3CachesByAssignedCPUs(assignedCPUs)
		if mapErr != nil {
			return annotations, componentAssignments, false, fmt.Errorf("component %q could not map assigned CPUs to L3 cache IDs: %w", componentName, mapErr)
		}
		candidateCaches = mappedCaches
	}

	selectedCache, selectedInterval, neededWays, err := pickSmallestFittingCacheInterval(
		candidateCaches,
		allAllocations,
		inFlightAssignments,
		deploymentID,
		requiredKi,
	)
	if err != nil {
		return annotations, componentAssignments, false, fmt.Errorf("component %q cache allocation failed: %w", componentName, err)
	}

	if neededWays <= 0 {
		return annotations, componentAssignments, false, fmt.Errorf("component %q computed invalid way count %d", componentName, neededWays)
	}

	wayMask, err := wayMaskHexForInterval(selectedInterval.Start, selectedInterval.Length)
	if err != nil {
		return annotations, componentAssignments, false, err
	}

	partitionMasks, err := dm.buildPartitionMasksForAllCaches(
		deploymentID,
		selectedCache.ID,
		wayMask,
		inFlightAssignments,
	)
	if err != nil {
		return annotations, componentAssignments, false, err
	}

	className := componentName + "_class"
	if err := dm.updateBalloonPolicyRDTWithYQ(ctx, deploymentID, componentName, className, selectedCache.ID, partitionMasks); err != nil {
		return annotations, componentAssignments, false, err
	}
	if err := dm.waitForRDTPolicyUpdate(ctx, componentName, className); err != nil {
		return annotations, componentAssignments, false, err
	}

	requirementName := componentName
	componentAssignments[requirementName] = append(componentAssignments[requirementName], database.CacheAssignment{
		ComponentName: componentName,
		Level:         selectedCache.Level,
		CacheID:       selectedCache.ID,
		SizeKB:        requiredKi,
		Mask:          wayMask,
	})

	for cacheID, mask := range partitionMasks {
		if cacheID == selectedCache.ID {
			continue
		}

		cacheInfo, ok := dm.findL3CacheByID(cacheID)
		if !ok {
			continue
		}

		componentAssignments[requirementName] = append(componentAssignments[requirementName], database.CacheAssignment{
			ComponentName: componentName,
			Level:         cacheInfo.Level,
			CacheID:       cacheID,
			SizeKB:        cacheInfo.WaySizeKB,
			Mask:          mask,
		})
	}

	annotations[rdtClassAnnotationKey] = className
	dm.log.Infow("Resolved exclusive L3 cache to RDT class",
		"componentName", componentName,
		"assignedCPUs", assignedCPUs,
		"className", className,
		"cacheID", selectedCache.ID,
		"requiredKi", requiredKi,
		"neededWays", neededWays,
		"wayMask", wayMask,
		"partitionMasks", partitionMasks,
		"selectedInterval", fmt.Sprintf("%d-%d", selectedInterval.Start, selectedInterval.Start+selectedInterval.Length-1),
	)

	return annotations, componentAssignments, true, nil
}

func uniqueAssignedCPUs(componentCPUAssignments map[string][]int) []int {
	if len(componentCPUAssignments) == 0 {
		return nil
	}

	seen := map[int]struct{}{}
	out := make([]int, 0)
	for _, cpuIndices := range componentCPUAssignments {
		for _, cpuIdx := range cpuIndices {
			if cpuIdx < 0 {
				continue
			}
			if _, exists := seen[cpuIdx]; exists {
				continue
			}
			seen[cpuIdx] = struct{}{}
			out = append(out, cpuIdx)
		}
	}

	sort.Ints(out)
	return out
}

func (dm *DeploymentManager) filterL3CachesByAssignedCPUs(assignedCPUs []int) ([]device.TopologyCacheInfo, error) {
	if len(assignedCPUs) == 0 {
		return dm.topologyLookup.L3Caches, nil
	}

	assignedSet := map[int]struct{}{}
	for _, cpuIdx := range assignedCPUs {
		assignedSet[cpuIdx] = struct{}{}
	}

	candidates := make([]device.TopologyCacheInfo, 0)
	for _, cache := range dm.topologyLookup.L3Caches {
		cacheCores, err := device.ParseCPUCoreRangeList(cache.Cores)
		if err != nil {
			return nil, fmt.Errorf("invalid cores range %q for cache ID %q: %w", cache.Cores, cache.ID, err)
		}
		if len(cacheCores) == 0 {
			continue
		}

		for _, cacheCPU := range cacheCores {
			if _, ok := assignedSet[cacheCPU]; !ok {
				continue
			}
			candidates = append(candidates, cache)
			break
		}
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no L3 cache from topology matches assigned CPUs %v", assignedCPUs)
	}

	return candidates, nil
}

type wayInterval struct {
	Start  int64
	Length int64
}

func pickSmallestFittingCacheInterval(
	topologyCaches []device.TopologyCacheInfo,
	persisted []database.OwnedCacheAssignment,
	inFlight map[string][]database.CacheAssignment,
	deploymentID string,
	requiredKi int64,
) (device.TopologyCacheInfo, wayInterval, int64, error) {
	bestCache := device.TopologyCacheInfo{}
	bestInterval := wayInterval{}
	bestIntervalFound := false
	bestLength := int64(math.MaxInt64)
	bestCacheID := ""
	bestNeededWays := int64(0)

	for _, cache := range topologyCaches {
		if cache.WaySizeKB <= 0 || cache.Ways <= 0 {
			continue
		}

		neededWays := int64(math.Ceil(float64(requiredKi) / float64(cache.WaySizeKB)))
		if neededWays <= 0 || neededWays > cache.Ways {
			continue
		}

		used := make([]bool, int(cache.Ways))

		for _, owned := range persisted {
			if owned.Owner == deploymentID || strings.HasPrefix(owned.Owner, deploymentID+"/") {
				continue
			}
			if !strings.EqualFold(owned.Assignment.Level, cache.Level) || strings.TrimSpace(owned.Assignment.CacheID) != strings.TrimSpace(cache.ID) {
				continue
			}

			for _, way := range maskWays(owned.Assignment.Mask, cache.Ways) {
				if way >= 0 && way < int64(len(used)) {
					used[way] = true
				}
			}
		}

		for _, assignmentList := range inFlight {
			for _, assignment := range assignmentList {
				if !strings.EqualFold(assignment.Level, cache.Level) || strings.TrimSpace(assignment.CacheID) != strings.TrimSpace(cache.ID) {
					continue
				}
				for _, way := range maskWays(assignment.Mask, cache.Ways) {
					if way >= 0 && way < int64(len(used)) {
						used[way] = true
					}
				}
			}
		}

		intervals := freeWayIntervals(used)
		for _, interval := range intervals {
			if interval.Length < neededWays {
				continue
			}

			if !bestIntervalFound || interval.Length < bestLength || (interval.Length == bestLength && cache.ID < bestCacheID) {
				bestCache = cache
				bestInterval = wayInterval{Start: interval.Start, Length: neededWays}
				bestIntervalFound = true
				bestLength = interval.Length
				bestCacheID = cache.ID
				bestNeededWays = neededWays
			}
		}
	}

	if !bestIntervalFound {
		return device.TopologyCacheInfo{}, wayInterval{}, 0, fmt.Errorf("no contiguous L3 cache interval can fit %d KiB", requiredKi)
	}

	return bestCache, bestInterval, bestNeededWays, nil
}

func freeWayIntervals(used []bool) []wayInterval {
	intervals := make([]wayInterval, 0)
	start := -1

	for i := 0; i < len(used); i++ {
		if !used[i] {
			if start == -1 {
				start = i
			}
			continue
		}

		if start != -1 {
			intervals = append(intervals, wayInterval{Start: int64(start), Length: int64(i - start)})
			start = -1
		}
	}

	if start != -1 {
		intervals = append(intervals, wayInterval{Start: int64(start), Length: int64(len(used) - start)})
	}

	return intervals
}

func maskWays(mask string, maxWays int64) []int64 {
	parsed := new(big.Int)
	if _, ok := parsed.SetString(strings.TrimSpace(mask), 0); !ok {
		return nil
	}

	ways := make([]int64, 0)
	for bit := int64(0); bit < maxWays; bit++ {
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

	return "0x" + strings.ToUpper(mask.Text(16)), nil
}

func (dm *DeploymentManager) updateBalloonPolicyRDTWithYQ(
	ctx context.Context,
	deploymentID string,
	componentName string,
	className string,
	selectedCacheID string,
	partitionMasks map[string]string,
) error {
	if _, err := exec.LookPath("kubectl"); err != nil {
		return fmt.Errorf("kubectl is required to update BalloonsPolicy: %w", err)
	}
	if _, err := exec.LookPath("yq"); err != nil {
		return fmt.Errorf("yq is required to update BalloonsPolicy: %w", err)
	}
	if dm.policyReader == nil {
		return fmt.Errorf("cannot update RDT policy for component %q: policy reader not configured", componentName)
	}

	policy := dm.policyReader.Parsed()
	if policy == nil {
		return fmt.Errorf("cannot update RDT policy for component %q: no BalloonsPolicy snapshot available", componentName)
	}

	tmp, err := os.CreateTemp("", fmt.Sprintf("balloon-policy-%s-%s-*.yaml", sanitizeFileToken(componentName), sanitizeFileToken(deploymentID)))
	if err != nil {
		return fmt.Errorf("failed to create temporary policy file: %w", err)
	}
	tmpPath := filepath.Clean(tmp.Name())
	if closeErr := tmp.Close(); closeErr != nil {
		return fmt.Errorf("failed to close temporary policy file: %w", closeErr)
	}
	defer func() {
		if removeErr := os.Remove(tmpPath); removeErr != nil {
			dm.log.Warnw("Failed to remove temporary policy file", "path", tmpPath, "err", removeErr)
		}
	}()

	content, err := runCommand(ctx, "kubectl", "-n", policy.Namespace, "get", "balloonspolicies", policy.Name, "-o", "yaml")
	if err != nil {
		return fmt.Errorf("failed to read BalloonsPolicy %s/%s: %w", policy.Namespace, policy.Name, err)
	}
	if writeErr := os.WriteFile(tmpPath, content, 0o600); writeErr != nil {
		return fmt.Errorf("failed to persist temporary policy yaml: %w", writeErr)
	}

	compQ := strconv.Quote(componentName)
	classQ := strconv.Quote(className)

	cacheIDs := make([]string, 0, len(dm.topologyLookup.L3Caches)+1)
	cacheIDSet := make(map[string]struct{}, len(dm.topologyLookup.L3Caches)+1)
	for _, topoCache := range dm.topologyLookup.L3Caches {
		id := strings.TrimSpace(topoCache.ID)
		if id == "" {
			continue
		}
		if _, seen := cacheIDSet[id]; seen {
			continue
		}
		cacheIDSet[id] = struct{}{}
		cacheIDs = append(cacheIDs, id)
	}

	selectedCacheID = strings.TrimSpace(selectedCacheID)
	if selectedCacheID == "" {
		return fmt.Errorf("selected cache ID is empty for component %q", componentName)
	}
	if _, seen := cacheIDSet[selectedCacheID]; !seen {
		cacheIDs = append(cacheIDs, selectedCacheID)
	}

	for cacheID, mask := range partitionMasks {
		id := strings.TrimSpace(cacheID)
		if id == "" {
			continue
		}
		if strings.TrimSpace(mask) == "" {
			return fmt.Errorf("empty mask provided for cache ID %q", id)
		}
		if _, seen := cacheIDSet[id]; !seen {
			cacheIDs = append(cacheIDs, id)
			cacheIDSet[id] = struct{}{}
		}
	}
	sort.Strings(cacheIDs)

	exprParts := make([]string, 0, len(cacheIDs)+3)
	exprParts = append(exprParts,
		fmt.Sprintf(`.spec.control.rdt.partitions[%s].classes[%s].l3Allocation = {}`, compQ, classQ),
		fmt.Sprintf(`.spec.control.rdt.partitions[%s].classes[%s].l3Allocation[%s].unified = "100%%"`, compQ, classQ, strconv.Quote(selectedCacheID)),
	)

	for _, currentCacheID := range cacheIDs {
		partitionMask, ok := partitionMasks[currentCacheID]
		if !ok {
			return fmt.Errorf("missing partition mask for cache ID %q", currentCacheID)
		}

		cacheQ := strconv.Quote(currentCacheID)
		exprParts = append(exprParts,
			fmt.Sprintf(
				`.spec.control.rdt.partitions[%s].l3Allocation[%s].unified = %s`,
				compQ,
				cacheQ,
				strconv.Quote(partitionMask),
			),
		)
	}

	expr := strings.Join(exprParts, " | ")

	if _, err := runCommand(ctx, "yq", "eval", "-i", expr, tmpPath); err != nil {
		return fmt.Errorf("failed to update BalloonsPolicy with yq: %w", err)
	}

	patchExpr := `.spec.control.rdt.partitions`
	patchContent, err := runCommand(ctx, "yq", "eval", patchExpr, "-o=json", tmpPath)
	if err != nil {
		return fmt.Errorf("failed to extract partitions patch from updated BalloonsPolicy: %w", err)
	}

	patchValue := strings.TrimSpace(string(patchContent))
	if patchValue == "" || patchValue == "null" {
		return fmt.Errorf("failed to build partitions patch: empty value")
	}

	patchPayload := fmt.Sprintf(`{"spec":{"control":{"rdt":{"partitions":%s}}}}`, patchValue)
	if _, err := runCommand(
		ctx,
		"kubectl",
		"-n",
		policy.Namespace,
		"patch",
		"balloonspolicies",
		policy.Name,
		"--type=merge",
		"--patch",
		patchPayload,
	); err != nil {
		return fmt.Errorf("failed to patch updated BalloonsPolicy: %w", err)
	}

	return nil
}

func (dm *DeploymentManager) buildPartitionMasksForAllCaches(
	deploymentID string,
	selectedCacheID string,
	selectedMask string,
	inFlightAssignments map[string][]database.CacheAssignment,
) (map[string]string, error) {
	persisted := dm.database.AllocatedCaches()
	masks := make(map[string]string, len(dm.topologyLookup.L3Caches))

	selectedCacheID = strings.TrimSpace(selectedCacheID)
	if selectedCacheID == "" {
		return nil, fmt.Errorf("selected cache ID cannot be empty")
	}

	masks[selectedCacheID] = strings.TrimSpace(selectedMask)

	for _, cache := range dm.topologyLookup.L3Caches {
		cacheID := strings.TrimSpace(cache.ID)
		if cacheID == "" || cacheID == selectedCacheID {
			continue
		}
		if cache.Ways <= 0 {
			return nil, fmt.Errorf("L3 cache %q has invalid way count %d", cacheID, cache.Ways)
		}

		nextMask, err := nextAvailableSingleWayMask(cache, persisted, inFlightAssignments, deploymentID)
		if err != nil {
			return nil, err
		}
		masks[cacheID] = nextMask
	}

	return masks, nil
}

func nextAvailableSingleWayMask(
	cache device.TopologyCacheInfo,
	persisted []database.OwnedCacheAssignment,
	inFlight map[string][]database.CacheAssignment,
	deploymentID string,
) (string, error) {
	used := make([]bool, int(cache.Ways))

	for _, owned := range persisted {
		if owned.Owner == deploymentID || strings.HasPrefix(owned.Owner, deploymentID+"/") {
			continue
		}
		if !strings.EqualFold(owned.Assignment.Level, cache.Level) || strings.TrimSpace(owned.Assignment.CacheID) != strings.TrimSpace(cache.ID) {
			continue
		}

		for _, way := range maskWays(owned.Assignment.Mask, cache.Ways) {
			if way >= 0 && way < int64(len(used)) {
				used[way] = true
			}
		}
	}

	for _, assignmentList := range inFlight {
		for _, assignment := range assignmentList {
			if !strings.EqualFold(assignment.Level, cache.Level) || strings.TrimSpace(assignment.CacheID) != strings.TrimSpace(cache.ID) {
				continue
			}
			for _, way := range maskWays(assignment.Mask, cache.Ways) {
				if way >= 0 && way < int64(len(used)) {
					used[way] = true
				}
			}
		}
	}

	for bit := int64(0); bit < cache.Ways; bit++ {
		if used[bit] {
			continue
		}

		mask, err := wayMaskHexForInterval(bit, 1)
		if err != nil {
			return "", err
		}
		return mask, nil
	}

	return "", fmt.Errorf("no free single-way L3 mask available for cache ID %q", strings.TrimSpace(cache.ID))
}

func (dm *DeploymentManager) findL3CacheByID(cacheID string) (device.TopologyCacheInfo, bool) {
	target := strings.TrimSpace(cacheID)
	for _, cache := range dm.topologyLookup.L3Caches {
		if strings.TrimSpace(cache.ID) == target {
			return cache, true
		}
	}

	return device.TopologyCacheInfo{}, false
}

func (dm *DeploymentManager) waitForRDTPolicyUpdate(ctx context.Context, componentName, className string) error {
	if dm.policyReader == nil {
		return fmt.Errorf("cannot verify RDT policy update: policy reader not configured")
	}

	delays := []time.Duration{200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond, 1600 * time.Millisecond, 3200 * time.Millisecond}
	for idx, delay := range delays {
		policy := dm.policyReader.Parsed()
		if policy != nil && policy.HasRDTPartition(componentName) && policy.HasRDTClass(className) {
			return nil
		}

		if idx == len(delays)-1 {
			break
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out while waiting for BalloonsPolicy cache refresh: %w", ctx.Err())
		case <-time.After(delay):
		}
	}

	return fmt.Errorf("balloon policy cache did not reflect RDT updates for component %q and class %q", componentName, className)
}

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

	return 0, fmt.Errorf("unsupported size unit in %q (expected Ki, Mi, or Gi)", *raw)
}

func runCommand(ctx context.Context, command string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s %s failed: %w: %s", command, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}
