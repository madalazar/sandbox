package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
	"github.com/margo/sandbox/poc/device/agent/types"
)

var _ CacheIsolationController = (*RdtPolicyController)(nil)

const (
	// JSON merge patch templates
	releasePatchTemplate = `{%q:{%q:{%q:{%q:{%q:null},%q:{%q:null}}}}}`

	// Condition error messages
	errFmtPolicyUpdateNotSynced = "balloon policy cache did not reflect RDT updates for component %q and class %q"
	errFmtPolicyRemoveNotSynced = "balloon policy cache did not reflect RDT removals for component %q and class %q"
)

var defaultPollDelays = []time.Duration{
	100 * time.Millisecond,
	200 * time.Millisecond,
	400 * time.Millisecond,
	800 * time.Millisecond,
	1600 * time.Millisecond,
}

// manages kubernetes rdt cache classes via nri balloon resource policies
type RdtPolicyController struct {
	dynClient dynamic.Interface
	reader    model.BalloonPolicyReader
	caches    []types.HostTopologyCache
}

func NewRdtPolicyController(caches []types.HostTopologyCache) *RdtPolicyController {
	return &RdtPolicyController{
		caches: caches,
	}
}

func NewRdtPolicyControllerWithClient(dynClient dynamic.Interface, reader model.BalloonPolicyReader, caches []types.HostTopologyCache) *RdtPolicyController {
	return &RdtPolicyController{
		dynClient: dynClient,
		reader:    reader,
		caches:    caches,
	}
}

// applies rdt cache class(es) for the committed reservation
func (c *RdtPolicyController) Apply(ctx context.Context, reservation model.Reservation) error {
	if !reservation.HasL3Cache() || reservation.L3CacheAssignment == nil {
		return nil
	}

	componentName := string(reservation.Owner.Component)
	if componentName == "" {
		return fmt.Errorf("reservation has empty component name")
	}

	cosId := string(reservation.Clos)
	if cosId == "" {
		cosId = string(reservation.L3CacheAssignment.Clos)
	}
	if cosId == "" || cosId == string(model.ClassUnset) {
		return fmt.Errorf("component %q has invalid or unset class id %q", componentName, cosId)
	}

	alloc := reservation.L3CacheAssignment
	selectedCacheId := strings.TrimSpace(alloc.CacheId)
	if selectedCacheId == "" {
		return fmt.Errorf("component %q has empty cache id", componentName)
	}

	selectedMask := strings.TrimSpace(alloc.Mask)
	if selectedMask == "" {
		return fmt.Errorf("component %q has empty cache mask", componentName)
	}

	partitionMasks := make(map[string]string)
	partitionMasks[selectedCacheId] = selectedMask

	// synthesize non-exclusive filler masks for other l3 caches
	// as it required by the nri plugin
	for _, cache := range c.caches {
		id := strings.TrimSpace(cache.Id)
		if id == "" || id == selectedCacheId {
			continue
		}
		partitionMasks[id] = model.RdtFillerCacheMask
	}

	namespace, policyName := c.policyTarget()

	patchBytes, err := c.buildApplyPatch(componentName, cosId, selectedCacheId, partitionMasks)
	if err != nil {
		return fmt.Errorf("failed to marshal balloons policy patch for component %q: %w", componentName, err)
	}

	if c.dynClient != nil {
		_, err = c.dynClient.Resource(model.BalloonsPolicyGVR).
			Namespace(namespace).
			Patch(ctx, policyName, k8stypes.MergePatchType, patchBytes, metav1.PatchOptions{})
		if err != nil {
			return fmt.Errorf("failed to patch balloons policy for component %q: %w", componentName, err)
		}
	}

	if c.reader != nil {
		if err := c.waitForResourcePolicyUpdate(ctx, componentName, cosId); err != nil {
			return err
		}
	}

	return nil
}

// verifies that the rdt cache class(es) are present in the nri balloon resource policy
func (c *RdtPolicyController) Verify(ctx context.Context, reservation model.Reservation) error {
	return nil
}

// removes rdt cache class(es) from the nri balloon resource policy
func (c *RdtPolicyController) Release(ctx context.Context, reservation model.Reservation) error {
	if !reservation.HasL3Cache() || reservation.L3CacheAssignment == nil {
		return nil
	}

	componentName := string(reservation.Owner.Component)
	cosId := string(reservation.Clos)
	if cosId == "" {
		cosId = string(reservation.L3CacheAssignment.Clos)
	}
	if cosId == "" || cosId == string(model.ClassUnset) {
		return nil
	}

	namespace, policyName := c.policyTarget()
	patchPayload := c.buildReleasePatch(componentName, cosId)

	if c.dynClient != nil {
		_, err := c.dynClient.Resource(model.BalloonsPolicyGVR).
			Namespace(namespace).
			Patch(ctx, policyName, k8stypes.MergePatchType, []byte(patchPayload), metav1.PatchOptions{})
		if err != nil {
			return fmt.Errorf("failed to remove rdt policy for component %q: %w", componentName, err)
		}
	}

	if c.reader != nil {
		if err := c.waitForResourcePolicyRemoval(ctx, componentName, cosId); err != nil {
			return err
		}
	}

	return nil
}

func (c *RdtPolicyController) buildApplyPatch(componentName, cosId, selectedCacheId string, partitionMasks map[string]string) ([]byte, error) {
	classesMap := map[string]any{
		cosId: map[string]any{
			model.RdtKeyL3Allocation: map[string]any{
				selectedCacheId: map[string]any{
					model.RdtKeyUnified: model.RdtFullAllocation,
				},
			},
		},
	}

	l3AllocMap := make(map[string]any, len(partitionMasks))
	for cacheId, mask := range partitionMasks {
		l3AllocMap[cacheId] = map[string]any{
			model.RdtKeyUnified: mask,
		}
	}

	patchMap := map[string]any{
		model.PolicyKeySpec: map[string]any{
			model.PolicyKeyControl: map[string]any{
				model.RdtKeyControl: map[string]any{
					model.RdtKeyPartitions: map[string]any{
						componentName: map[string]any{
							model.RdtKeyClasses:      classesMap,
							model.RdtKeyL3Allocation: l3AllocMap,
						},
					},
				},
			},
		},
	}

	return json.Marshal(patchMap)
}

func (c *RdtPolicyController) buildReleasePatch(componentName, cosId string) string {
	return fmt.Sprintf(
		releasePatchTemplate,
		model.PolicyKeySpec,
		model.PolicyKeyControl,
		model.RdtKeyControl,
		model.RdtKeyPartitions,
		componentName,
		model.RdtKeyClasses,
		cosId,
	)
}

func (c *RdtPolicyController) policyTarget() (string, string) {
	namespace := model.DefaultBalloonsPolicyNamespace
	name := model.DefaultBalloonsPolicyName
	if c.reader != nil {
		if policy := c.reader.Parsed(); policy != nil {
			if policy.Namespace != "" {
				namespace = policy.Namespace
			}
			if policy.Name != "" {
				name = policy.Name
			}
		}
	}
	return namespace, name
}

func (c *RdtPolicyController) waitForResourcePolicyUpdate(ctx context.Context, componentName, className string) error {
	return c.waitForResourcePolicyCondition(
		ctx,
		componentName,
		className,
		func(policy *model.ParsedBalloonPolicy) bool {
			return policy != nil && policy.HasPartition(componentName) && policy.HasClass(className)
		},
		errFmtPolicyUpdateNotSynced,
	)
}

func (c *RdtPolicyController) waitForResourcePolicyRemoval(ctx context.Context, componentName, className string) error {
	return c.waitForResourcePolicyCondition(
		ctx,
		componentName,
		className,
		func(policy *model.ParsedBalloonPolicy) bool {
			return policy != nil && !policy.HasPartition(componentName) && !policy.HasClass(className)
		},
		errFmtPolicyRemoveNotSynced,
	)
}

func (c *RdtPolicyController) waitForResourcePolicyCondition(
	ctx context.Context,
	componentName string,
	className string,
	condition func(policy *model.ParsedBalloonPolicy) bool,
	failureFormat string,
) error {
	if c.reader == nil {
		return nil
	}

	for idx, delay := range defaultPollDelays {
		policy := c.reader.Parsed()
		if condition(policy) {
			return nil
		}

		if idx == len(defaultPollDelays)-1 {
			break
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out while waiting for balloons policy cache refresh: %w", ctx.Err())
		case <-time.After(delay):
		}
	}

	return fmt.Errorf(failureFormat, componentName, className)
}
