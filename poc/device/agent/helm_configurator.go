package main

import (
	"fmt"
	"strings"
)

// balloonPodAnnotationKey places a pod into an NRI balloon.
const balloonPodAnnotationKey = "balloon.balloons.resource-policy.nri.io/pod"

// HelmConfigurator applies a CPU plan to a chart's values map. It produces no artifact,
// so it has no cleanup counterpart to the Compose path.
type HelmConfigurator struct{}

func NewHelmConfigurator() *HelmConfigurator {
	return &HelmConfigurator{}
}

// Apply merges the plan's balloon placement and cpuset into values, preserving unrelated
// user-supplied entries.
func (c *HelmConfigurator) Apply(
	plan CpuPlan,
	owner OwnerRef,
	values map[string]any,
) (map[string]any, error) {
	if values == nil {
		values = map[string]any{}
	}

	if balloon := plan.PlacementClass(); balloon != "" {
		values["podAnnotations"] = c.MergePodAnnotations(
			values["podAnnotations"],
			map[string]string{balloonPodAnnotationKey: balloon},
		)
	}

	component := string(owner.Component)
	if cpuset := plan.CpuSet(); strings.TrimSpace(cpuset) != "" && component != "" {
		values[component] = c.mergeComponentCpuset(values[component], cpuset)
	}

	return values, nil
}

// MergePodAnnotations overlays annotations onto whatever the chart values already carry.
// It is exported to the call site because cache annotations are still merged there.
func (c *HelmConfigurator) MergePodAnnotations(existing any, annotations map[string]string) map[string]any {
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
			merged[fmt.Sprintf("%v", k)] = fmt.Sprintf("%v", v)
		}
	default:
		fmt.Printf(
			"Existing podAnnotations value has unsupported type %T; replacing with resolved NRI annotations\n",
			existing,
		)
	}

	for k, v := range annotations {
		merged[k] = v
	}

	return merged
}

func (c *HelmConfigurator) mergeComponentCpuset(existing any, cpuset string) map[string]any {
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
		fmt.Printf(
			"Existing component value has unsupported type %T; replacing with resolved cpuset\n",
			existing,
		)
	}

	merged["cpuset"] = cpuset
	return merged
}
