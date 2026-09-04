package configurator

import (
	"testing"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

func cpuPlanFor(component string, cpus ...int) model.CpuPlan {
	return model.CpuPlan{
		Component: model.ComponentRef(component),
		Cpus:      cpus,
	}
}

func newTestHelmConfigurator() *HelmConfigurator {
	return NewHelmConfigurator()
}

func TestHelmConfiguratorApplyMergesPlacementAndCpuset(t *testing.T) {
	configurator := newTestHelmConfigurator()
	plan := model.CpuPlan{
		Component: "worker",
		Cpus:      []int{8, 9},
		Placement: model.CpuPlacement{Class: "rt-balloon"},
	}

	values, err := configurator.Apply(plan, model.NewOwnerRef("deployment-1", "worker"), map[string]any{
		"replicaCount":   1,
		"podAnnotations": map[string]any{"existing": "keep"},
		"worker":         map[string]any{"image": "worker:latest"},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	annotations, ok := values["podAnnotations"].(map[string]any)
	if !ok {
		t.Fatalf("podAnnotations has unexpected type %T", values["podAnnotations"])
	}
	if annotations["existing"] != "keep" {
		t.Errorf("existing annotation lost: %v", annotations)
	}
	if annotations[BalloonPodAnnotationKey] != "rt-balloon" {
		t.Errorf("balloon annotation missing: %v", annotations)
	}

	component, ok := values["worker"].(map[string]any)
	if !ok {
		t.Fatalf("component values have unexpected type %T", values["worker"])
	}
	if component["cpuset"] != "8-9" {
		t.Errorf("cpuset = %v, want 8-9", component["cpuset"])
	}
	if component["image"] != "worker:latest" {
		t.Errorf("existing component value lost: %v", component)
	}
	if values["replicaCount"] != 1 {
		t.Errorf("unrelated value lost: %v", values["replicaCount"])
	}
}

func TestHelmConfiguratorApplyLeavesValuesAloneWithoutAPlan(t *testing.T) {
	configurator := newTestHelmConfigurator()

	values, err := configurator.Apply(model.CpuPlan{}, model.NewOwnerRef("deployment-1", "worker"), map[string]any{
		"replicaCount": 1,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if _, exists := values["podAnnotations"]; exists {
		t.Errorf("podAnnotations added for a plan with no CPUs: %v", values)
	}
	if _, exists := values["worker"]; exists {
		t.Errorf("component values added for a plan with no CPUs: %v", values)
	}
}

// A directly pinned component has a cpuset but no balloon, so the chart must still
// receive the cpuset without gaining an empty annotation map.
func TestHelmConfiguratorApplyWithoutPlacementClass(t *testing.T) {
	configurator := newTestHelmConfigurator()
	plan := cpuPlanFor("worker", 3)

	values, err := configurator.Apply(plan, model.NewOwnerRef("deployment-1", "worker"), nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if _, exists := values["podAnnotations"]; exists {
		t.Errorf("podAnnotations added without a placement class: %v", values)
	}
	component, ok := values["worker"].(map[string]any)
	if !ok {
		t.Fatalf("component values have unexpected type %T", values["worker"])
	}
	if component["cpuset"] != "3" {
		t.Errorf("cpuset = %v, want 3", component["cpuset"])
	}
}
