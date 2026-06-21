package main

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/margo/sandbox/standard/generatedCode/wfm/sbi"
)

const rdtClassAnnotationKey = "rdtclass.resource-policy.nri.io/pod"

func (dm *DeploymentManager) resolveComponentCacheAnnotations(
	ctx context.Context,
	deploymentID string,
	componentName string,
	requiredResources *sbi.RequiredResources,
) (map[string]string, bool, error) {
	annotations := map[string]string{}
	if requiredResources == nil || requiredResources.Cache == nil || len(*requiredResources.Cache) == 0 {
		return annotations, false, nil
	}

	exclusiveReqs := make([]sbi.Cache, 0)
	for _, cacheReq := range *requiredResources.Cache {
		if cacheReq.Level != sbi.L3 {
			return annotations, false, fmt.Errorf("component %q requests unsupported cache level %q (only L3 is supported)", componentName, cacheReq.Level)
		}

		if cacheReq.Allocation == sbi.CacheAllocationExclusive {
			exclusiveReqs = append(exclusiveReqs, cacheReq)
		}
	}

	if len(exclusiveReqs) == 0 {
		dm.log.Infow("Component uses shared L3 cache only; skipping RDT class allocation", "componentName", componentName)
		return annotations, false, nil
	}

	if len(exclusiveReqs) > 1 {
		return annotations, false, fmt.Errorf("component %q has %d exclusive cache requests; only one exclusive L3 request per component is supported", componentName, len(exclusiveReqs))
	}

	if len(dm.topologyLookup.L3Caches) == 0 {
		return annotations, false, fmt.Errorf("component %q requests exclusive L3 cache but topology artifact has no L3 cache entries", componentName)
	}

	// TODO: pick the first cache you find for now
	selectedCache := dm.topologyLookup.L3Caches[0]
	requiredKi, err := parseBinarySizeKi(exclusiveReqs[0].Size)
	if err != nil {
		return annotations, false, fmt.Errorf("component %q has invalid cache size: %w", componentName, err)
	}

	neededWays := int64(math.Ceil(float64(requiredKi) / float64(selectedCache.WaySizeKB)))
	if neededWays <= 0 {
		return annotations, false, fmt.Errorf("component %q computed invalid way count %d", componentName, neededWays)
	}
	if neededWays > selectedCache.Ways {
		return annotations, false, fmt.Errorf("component %q requests %d KiB cache requiring %d ways, but cache %s has only %d ways", componentName, requiredKi, neededWays, selectedCache.ID, selectedCache.Ways)
	}

	wayMask, err := contiguousWayMaskHex(neededWays)
	if err != nil {
		return annotations, false, err
	}

	className := componentName + "_class"
	if err := dm.updateBalloonPolicyRDTWithYQ(ctx, deploymentID, componentName, className, selectedCache.ID, wayMask); err != nil {
		return annotations, false, err
	}
	if err := dm.waitForRDTPolicyUpdate(ctx, componentName, className); err != nil {
		return annotations, false, err
	}

	annotations[rdtClassAnnotationKey] = className
	dm.log.Infow("Resolved exclusive L3 cache to RDT class", "componentName", componentName, "className", className, "cacheID", selectedCache.ID, "requiredKi", requiredKi, "neededWays", neededWays, "wayMask", wayMask)

	return annotations, true, nil
}

func (dm *DeploymentManager) updateBalloonPolicyRDTWithYQ(
	ctx context.Context,
	deploymentID string,
	componentName string,
	className string,
	cacheID string,
	wayMask string,
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
	cacheQ := strconv.Quote(cacheID)
	maskQ := strconv.Quote(wayMask)
	expr := fmt.Sprintf(
		`.spec.control.rdt.partitions[%s].l3Allocation[%s].unified = %s | .spec.control.rdt.partitions[%s].classes[%s].l3Allocation[%s].unified = "100%%"`,
		compQ,
		cacheQ,
		maskQ,
		compQ,
		classQ,
		cacheQ,
	)

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

func contiguousWayMaskHex(ways int64) (string, error) {
	if ways <= 0 {
		return "", fmt.Errorf("ways must be > 0")
	}

	mask := new(big.Int).Lsh(big.NewInt(1), uint(ways))
	mask.Sub(mask, big.NewInt(1))
	return "0x" + strings.ToUpper(mask.Text(16)), nil
}

func runCommand(ctx context.Context, command string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s %s failed: %w: %s", command, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}
