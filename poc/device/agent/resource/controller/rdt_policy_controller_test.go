package controller

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
	"github.com/margo/sandbox/poc/device/agent/types"
)

func newFakeBalloonsPolicy(namespace, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "config.nri/v1alpha1",
			"kind":       "BalloonsPolicy",
			"metadata": map[string]any{
				"namespace": namespace,
				"name":      name,
			},
			"spec": map[string]any{
				"control": map[string]any{
					"rdt": map[string]any{
						"partitions": map[string]any{},
					},
				},
			},
		},
	}
}

type fakePolicyReader struct {
	policy *model.ParsedBalloonPolicy
}

func (f *fakePolicyReader) Parsed() *model.ParsedBalloonPolicy {
	return f.policy
}

func TestRdtPolicyControllerInterface(t *testing.T) {
	var _ CacheIsolationController = (*RdtPolicyController)(nil)

	ctrl := NewRdtPolicyController([]types.HostTopologyCache{})
	if ctrl == nil {
		t.Fatal("expected non-nil RdtPolicyController")
	}
}

func TestRdtPolicyControllerNoCache(t *testing.T) {
	fakeClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	ctrl := NewRdtPolicyControllerWithClient(fakeClient, nil, nil)
	ctx := context.Background()

	res := model.Reservation{
		Owner: model.NewOwnerRef("dep-1", "comp-a"),
	}

	if err := ctrl.Apply(ctx, res); err != nil {
		t.Fatalf("expected nil for Apply without cache, got %v", err)
	}
	if err := ctrl.Verify(ctx, res); err != nil {
		t.Fatalf("expected nil for Verify without cache, got %v", err)
	}
	if err := ctrl.Release(ctx, res); err != nil {
		t.Fatalf("expected nil for Release without cache, got %v", err)
	}
	if len(fakeClient.Actions()) != 0 {
		t.Fatalf("expected 0 client actions, got %d", len(fakeClient.Actions()))
	}
}

func TestRdtPolicyControllerApply(t *testing.T) {
	caches := []types.HostTopologyCache{
		{Id: "0", Level: "L3", Ways: 12, WaySizeKB: 1024},
		{Id: "1", Level: "L3", Ways: 12, WaySizeKB: 1024},
	}

	res := model.Reservation{
		Owner: model.NewOwnerRef("dep-1", "worker"),
		Cpus:  []int{4, 5},
		L3CacheAssignment: &model.CacheAssignment{
			CacheId: "0",
			Mask:    "0x7",
			Clos:    "worker_class",
		},
	}

	t.Run("successful apply with multi-cache filler synthesis", func(t *testing.T) {
		fakeClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), newFakeBalloonsPolicy("kube-system", "default"))
		ctrl := NewRdtPolicyControllerWithClient(fakeClient, nil, caches)

		if err := ctrl.Apply(context.Background(), res); err != nil {
			t.Fatalf("Apply failed: %v", err)
		}
		actions := fakeClient.Actions()
		if len(actions) != 1 {
			t.Fatalf("expected 1 client action, got %d", len(actions))
		}

		patchAction, ok := actions[0].(clienttesting.PatchAction)
		if !ok {
			t.Fatalf("expected PatchAction, got %T", actions[0])
		}
		if patchAction.GetNamespace() != "kube-system" {
			t.Errorf("expected namespace kube-system, got %s", patchAction.GetNamespace())
		}
		if patchAction.GetName() != "default" {
			t.Errorf("expected name default, got %s", patchAction.GetName())
		}

		var patchMap map[string]any
		if err := json.Unmarshal(patchAction.GetPatch(), &patchMap); err != nil {
			t.Fatalf("failed to unmarshal patch JSON: %v", err)
		}

		spec := patchMap["spec"].(map[string]any)
		control := spec["control"].(map[string]any)
		rdt := control["rdt"].(map[string]any)
		partitions := rdt["partitions"].(map[string]any)
		workerPart := partitions["worker"].(map[string]any)

		// Check classes
		classes := workerPart["classes"].(map[string]any)
		workerClass := classes["worker_class"].(map[string]any)
		classL3 := workerClass["l3Allocation"].(map[string]any)
		cache0Class := classL3["0"].(map[string]any)
		if cache0Class["unified"] != "100%" {
			t.Errorf("expected unified 100%% for selected cache 0, got %v", cache0Class["unified"])
		}

		// Check partition l3Allocation (selected cache mask + synthesized filler mask)
		partL3 := workerPart["l3Allocation"].(map[string]any)
		cache0Part := partL3["0"].(map[string]any)
		if cache0Part["unified"] != "0x7" {
			t.Errorf("expected unified 0x7 for selected cache 0, got %v", cache0Part["unified"])
		}
		cache1Part := partL3["1"].(map[string]any)
		if cache1Part["unified"] != "0x1" {
			t.Errorf("expected synthesized filler mask 0x1 for other cache 1, got %v", cache1Part["unified"])
		}
	})

	t.Run("successful apply with custom policy namespace and name", func(t *testing.T) {
		fakeClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), newFakeBalloonsPolicy("custom-ns", "custom-balloons"))
		reader := &fakePolicyReader{
			policy: &model.ParsedBalloonPolicy{
				Name:      "custom-balloons",
				Namespace: "custom-ns",
				RdtConfig: model.RdtConfig{
					Partitions: map[string]struct{}{"worker": {}},
					Classes:    map[string]struct{}{"worker_class": {}},
				},
			},
		}
		ctrl := NewRdtPolicyControllerWithClient(fakeClient, reader, caches)

		if err := ctrl.Apply(context.Background(), res); err != nil {
			t.Fatalf("Apply failed: %v", err)
		}
		actions := fakeClient.Actions()
		if len(actions) != 1 {
			t.Fatalf("expected 1 client action, got %d", len(actions))
		}
		patchAction, ok := actions[0].(clienttesting.PatchAction)
		if !ok {
			t.Fatalf("expected PatchAction, got %T", actions[0])
		}
		if patchAction.GetNamespace() != "custom-ns" || patchAction.GetName() != "custom-balloons" {
			t.Fatalf("expected custom namespace custom-ns and name custom-balloons, got %s/%s",
				patchAction.GetNamespace(), patchAction.GetName())
		}
	})

	t.Run("client failure returns error", func(t *testing.T) {
		fakeClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), newFakeBalloonsPolicy("kube-system", "default"))
		fakeClient.PrependReactor("patch", "*", func(action clienttesting.Action) (handled bool, ret runtime.Object, err error) {
			return true, nil, errors.New("k8s api server unavailable")
		})
		ctrl := NewRdtPolicyControllerWithClient(fakeClient, nil, caches)

		if err := ctrl.Apply(context.Background(), res); err == nil {
			t.Fatal("expected error on client failure, got nil")
		}
	})

	t.Run("missing cache id returns error", func(t *testing.T) {
		fakeClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), newFakeBalloonsPolicy("kube-system", "default"))
		ctrl := NewRdtPolicyControllerWithClient(fakeClient, nil, caches)
		badRes := res
		badRes.L3CacheAssignment = &model.CacheAssignment{
			CacheId: "",
			Mask:    "0x7",
			Clos:    "worker_class",
		}

		if err := ctrl.Apply(context.Background(), badRes); err == nil {
			t.Fatal("expected error for empty cache ID, got nil")
		}
	})

	t.Run("missing cache mask returns error", func(t *testing.T) {
		fakeClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), newFakeBalloonsPolicy("kube-system", "default"))
		ctrl := NewRdtPolicyControllerWithClient(fakeClient, nil, caches)
		badRes := res
		badRes.L3CacheAssignment = &model.CacheAssignment{
			CacheId: "0",
			Mask:    "",
			Clos:    "worker_class",
		}

		if err := ctrl.Apply(context.Background(), badRes); err == nil {
			t.Fatal("expected error for empty mask, got nil")
		}
	})
}

func TestRdtPolicyControllerVerify(t *testing.T) {
	caches := []types.HostTopologyCache{
		{Id: "0", Level: "L3", Ways: 12, WaySizeKB: 1024},
	}

	res := model.Reservation{
		Owner: model.NewOwnerRef("dep-1", "worker"),
		Cpus:  []int{4, 5},
		L3CacheAssignment: &model.CacheAssignment{
			CacheId: "0",
			Mask:    "0x7",
			Clos:    "worker_class",
		},
	}

	fakeClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), newFakeBalloonsPolicy("kube-system", "default"))
	ctrl := NewRdtPolicyControllerWithClient(fakeClient, nil, caches)

	if err := ctrl.Verify(context.Background(), res); err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if len(fakeClient.Actions()) != 0 {
		t.Fatalf("expected 0 client actions for Verify, got %d", len(fakeClient.Actions()))
	}
}

func TestRdtPolicyControllerRelease(t *testing.T) {
	caches := []types.HostTopologyCache{
		{Id: "0", Level: "L3", Ways: 12, WaySizeKB: 1024},
	}

	res := model.Reservation{
		Owner: model.NewOwnerRef("dep-1", "worker"),
		Cpus:  []int{4, 5},
		L3CacheAssignment: &model.CacheAssignment{
			CacheId: "0",
			Mask:    "0x7",
			Clos:    "worker_class",
		},
	}

	t.Run("successful release sets partition and class to null", func(t *testing.T) {
		fakeClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), newFakeBalloonsPolicy("kube-system", "default"))
		ctrl := NewRdtPolicyControllerWithClient(fakeClient, nil, caches)

		if err := ctrl.Release(context.Background(), res); err != nil {
			t.Fatalf("Release failed: %v", err)
		}
		actions := fakeClient.Actions()
		if len(actions) != 1 {
			t.Fatalf("expected 1 client action, got %d", len(actions))
		}

		patchAction, ok := actions[0].(clienttesting.PatchAction)
		if !ok {
			t.Fatalf("expected PatchAction, got %T", actions[0])
		}
		expectedPatch := `{"spec":{"control":{"rdt":{"partitions":{"worker":null},"classes":{"worker_class":null}}}}}`
		if string(patchAction.GetPatch()) != expectedPatch {
			t.Fatalf("expected patch %q, got %q", expectedPatch, string(patchAction.GetPatch()))
		}
	})

	t.Run("client failure returns error", func(t *testing.T) {
		fakeClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), newFakeBalloonsPolicy("kube-system", "default"))
		fakeClient.PrependReactor("patch", "*", func(action clienttesting.Action) (handled bool, ret runtime.Object, err error) {
			return true, nil, errors.New("failed to patch")
		})
		ctrl := NewRdtPolicyControllerWithClient(fakeClient, nil, caches)

		if err := ctrl.Release(context.Background(), res); err == nil {
			t.Fatal("expected error on client failure, got nil")
		}
	})
}

func TestRdtPolicyControllerWaitTimeout(t *testing.T) {
	fakeClient := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), newFakeBalloonsPolicy("kube-system", "default"))
	reader := &fakePolicyReader{
		policy: &model.ParsedBalloonPolicy{
			Name:      "default",
			Namespace: "kube-system",
			RdtConfig: model.RdtConfig{
				Partitions: map[string]struct{}{},
				Classes:    map[string]struct{}{},
			},
		},
	}
	ctrl := NewRdtPolicyControllerWithClient(fakeClient, reader, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := ctrl.waitForResourcePolicyUpdate(ctx, "worker", "worker_class")
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}
