package configurator

import (
	"testing"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

func helmCpuPlanFor(component string, cpus []int, rdtClassName string) model.CpuPlan {
	return model.CpuPlan{
		Component: model.ComponentRef(component),
		Cpus:      cpus,
		Placement: model.CpuPlacement{Class: rdtClassName},
	}
}

func newTestHelmConfigurator() *HelmConfigurator {
	return NewHelmConfigurator()
}

func TestHelmConfiguratorApplyMergesPlacementAndCpuset(t *testing.T) {
	configurator := newTestHelmConfigurator()
	plan := helmCpuPlanFor("worker", []int{8, 9}, "rt-balloon")

	values, err := configurator.Apply(plan, model.CachePlan{}, model.NewOwnerRef("deployment-1", "worker"), map[string]any{
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

	values, err := configurator.Apply(model.CpuPlan{}, model.CachePlan{}, model.NewOwnerRef("deployment-1", "worker"), map[string]any{
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
	plan := helmCpuPlanFor("worker", []int{3}, "")

	values, err := configurator.Apply(plan, model.CachePlan{}, model.NewOwnerRef("deployment-1", "worker"), nil)
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

func TestHelmConfiguratorApplyNormalizesDifferentMapTypes(t *testing.T) {
	configurator := newTestHelmConfigurator()
	plan := helmCpuPlanFor("worker", []int{2}, "balloon-1")

	values, err := configurator.Apply(plan, model.CachePlan{}, model.NewOwnerRef("deployment-1", "worker"), map[string]any{
		"podAnnotations": map[any]any{"intKey": 123, "strKey": "val"},
		"worker":         map[string]string{"image": "worker:latest"},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	annotations, ok := values["podAnnotations"].(map[string]any)
	if !ok {
		t.Fatalf("podAnnotations unexpected type %T", values["podAnnotations"])
	}
	if annotations["intKey"] != "123" || annotations["strKey"] != "val" {
		t.Errorf("podAnnotations not converted properly: %v", annotations)
	}
	if annotations[BalloonPodAnnotationKey] != "balloon-1" {
		t.Errorf("missing balloon annotation: %v", annotations)
	}

	worker, ok := values["worker"].(map[string]any)
	if !ok {
		t.Fatalf("worker unexpected type %T", values["worker"])
	}
	if worker["image"] != "worker:latest" || worker["cpuset"] != "2" {
		t.Errorf("worker values unexpected: %v", worker)
	}
}

func TestHelmConfiguratorApplyWithCacheInjectsRdtAnnotation(t *testing.T) {
	configurator := newTestHelmConfigurator()
	cpuPlan := model.CpuPlan{
		Component: "cyclictest",
		Cpus:      []int{2, 3},
	}
	cachePlan := model.CachePlan{
		Component: "cyclictest",
		L3CacheAssignment: &model.CacheAssignment{
			CacheId: "0",
			Mask:    "0x3",
			Clos:    "cyclictest_class",
		},
	}

	values, err := configurator.Apply(cpuPlan, cachePlan, model.NewOwnerRef("deployment-1", "cyclictest"), map[string]any{
		"replicaCount": 1,
	})
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	annotations, ok := values["podAnnotations"].(map[string]any)
	if !ok {
		t.Fatalf("podAnnotations unexpected type %T", values["podAnnotations"])
	}
	if annotations[RdtClassPodAnnotationKey] != "cyclictest_class" {
		t.Errorf("rdt annotation = %v, want cyclictest_class", annotations[RdtClassPodAnnotationKey])
	}
	if _, hasBalloon := annotations[BalloonPodAnnotationKey]; hasBalloon {
		t.Errorf("balloon annotation present unexpectedly: %v", annotations)
	}

	component, ok := values["cyclictest"].(map[string]any)
	if !ok {
		t.Fatalf("component values unexpected type %T", values["cyclictest"])
	}
	if component["cpuset"] != "2-3" {
		t.Errorf("cpuset = %v, want 2-3", component["cpuset"])
	}
}

func TestHelmConfiguratorApplyWithBothBalloonAndRdtAnnotations(t *testing.T) {
	configurator := newTestHelmConfigurator()
	cpuPlan := helmCpuPlanFor("worker", []int{4, 5}, "isolated-balloon")
	cachePlan := model.CachePlan{
		Component: "worker",
		L3CacheAssignment: &model.CacheAssignment{
			CacheId: "0",
			Mask:    "0xf",
			Clos:    "worker_class",
		},
	}

	values, err := configurator.Apply(cpuPlan, cachePlan, model.NewOwnerRef("deployment-1", "worker"), map[string]any{
		"podAnnotations": map[string]any{"custom": "user-value"},
	})
	if err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	annotations, ok := values["podAnnotations"].(map[string]any)
	if !ok {
		t.Fatalf("podAnnotations unexpected type %T", values["podAnnotations"])
	}
	if annotations["custom"] != "user-value" {
		t.Errorf("existing user annotation lost: %v", annotations)
	}
	if annotations[BalloonPodAnnotationKey] != "isolated-balloon" {
		t.Errorf("balloon annotation = %v, want isolated-balloon", annotations[BalloonPodAnnotationKey])
	}
	if annotations[RdtClassPodAnnotationKey] != "worker_class" {
		t.Errorf("rdt annotation = %v, want worker_class", annotations[RdtClassPodAnnotationKey])
	}
}
