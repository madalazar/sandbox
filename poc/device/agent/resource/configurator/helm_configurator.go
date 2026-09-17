package configurator

import (
	"fmt"
	"maps"
	"strings"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

// places a pod into an nri balloon
const BalloonPodAnnotationKey = "balloon.balloons.resource-policy.nri.io/pod"

// associates a pod with an nri rdt cache class
const RdtClassPodAnnotationKey = "rdtclass.resource-policy.nri.io/pod"

// applies cpu and cache plans to a chart's values map. It produces no artifact, so it has no
// cleanup counterpart to the compose path
type HelmConfigurator struct{}

func NewHelmConfigurator() *HelmConfigurator {
	return &HelmConfigurator{}
}

// merges cpu and cache plans into values, injecting balloon and rdt class annotations
func (c *HelmConfigurator) Apply(
	cpuPlan model.CpuPlan,
	cachePlan model.CachePlan,
	owner model.OwnerRef,
	values map[string]any,
) (map[string]any, error) {
	if values == nil {
		values = map[string]any{}
	}

	annotations := map[string]string{}
	if balloon := cpuPlan.PlacementClass(); balloon != "" {
		annotations[BalloonPodAnnotationKey] = balloon
	}
	if cachePlan.HasCache() {
		clos := cachePlan.L3CacheAssignment.Clos
		if clos.Held() {
			annotations[RdtClassPodAnnotationKey] = clos.String()
		}
	}

	if len(annotations) > 0 {
		values["podAnnotations"] = c.mergePodAnnotations(
			values["podAnnotations"],
			annotations,
		)
	}

	component := string(owner.Component)
	if cpuset := cpuPlan.CpuSet(); strings.TrimSpace(cpuset) != "" && component != "" {
		values[component] = c.mergeComponentCpuset(values[component], cpuset)
	}

	return values, nil
}

// overlays annotations onto whatever the chart values already carry
func (c *HelmConfigurator) mergePodAnnotations(existing any, annotations map[string]string) map[string]any {
	merged := c.normalizeMap(existing, "podAnnotations", "resolved NRI annotations")
	for k, v := range merged {
		merged[k] = fmt.Sprintf("%v", v)
	}
	for k, v := range annotations {
		merged[k] = v
	}

	return merged
}

func (c *HelmConfigurator) mergeComponentCpuset(existing any, cpuset string) map[string]any {
	merged := c.normalizeMap(existing, "component", "resolved cpuset")
	merged["cpuset"] = cpuset
	return merged
}

// normalizes an untyped section of chart values into a string-keyed map
func (c *HelmConfigurator) normalizeMap(existing any, field string, replacement string) map[string]any {
	merged := map[string]any{}

	switch typed := existing.(type) {
	case nil:
		// No existing values to merge.
	case map[string]any:
		maps.Copy(merged, typed)
	case map[string]string:
		for k, v := range typed {
			merged[k] = v
		}
	case map[any]any:
		for k, v := range typed {
			merged[fmt.Sprintf("%v", k)] = v
		}
	default:
		fmt.Printf(
			"existing %s value has unsupported type %T; replacing with %s\n",
			field, existing, replacement,
		)
	}

	return merged
}
