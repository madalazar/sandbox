package controller

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
	"github.com/margo/sandbox/poc/device/agent/types"
)

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
	runner := &fakeRunner{}
	ctrl := NewRdtPolicyControllerWithReader(runner, nil, nil)
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
	if len(runner.calls) != 0 {
		t.Fatalf("expected 0 calls to runner, got %d", len(runner.calls))
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
		Clos: "worker_class",
	}

	t.Run("successful apply with multi-cache filler synthesis", func(t *testing.T) {
		runner := &fakeRunner{}
		ctrl := NewRdtPolicyControllerWithReader(runner, nil, caches)

		if err := ctrl.Apply(context.Background(), res); err != nil {
			t.Fatalf("Apply failed: %v", err)
		}
		if len(runner.calls) != 1 {
			t.Fatalf("expected 1 runner call, got %d", len(runner.calls))
		}

		call := runner.calls[0]
		if !strings.Contains(call, "kubectl -n kube-system patch balloonspolicies default --type=merge --patch") {
			t.Fatalf("unexpected patch command: %s", call)
		}

		// Verify patch JSON contents
		patchIdx := strings.Index(call, "--patch ")
		if patchIdx == -1 {
			t.Fatalf("could not find patch in command: %s", call)
		}
		patchStr := call[patchIdx+len("--patch "):]

		var patchMap map[string]any
		if err := json.Unmarshal([]byte(patchStr), &patchMap); err != nil {
			t.Fatalf("failed to unmarshal patch JSON %q: %v", patchStr, err)
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
		runner := &fakeRunner{}
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
		ctrl := NewRdtPolicyControllerWithReader(runner, reader, caches)

		if err := ctrl.Apply(context.Background(), res); err != nil {
			t.Fatalf("Apply failed: %v", err)
		}
		if len(runner.calls) != 1 {
			t.Fatalf("expected 1 runner call, got %d", len(runner.calls))
		}
		if !strings.Contains(runner.calls[0], "-n custom-ns patch balloonspolicies custom-balloons") {
			t.Fatalf("expected custom namespace and policy name in command: %s", runner.calls[0])
		}
	})

	t.Run("runner failure returns error", func(t *testing.T) {
		runner := &fakeRunner{err: errors.New("kubectl connection failed")}
		ctrl := NewRdtPolicyControllerWithReader(runner, nil, caches)

		if err := ctrl.Apply(context.Background(), res); err == nil {
			t.Fatal("expected error on runner failure, got nil")
		}
	})

	t.Run("missing cache id returns error", func(t *testing.T) {
		runner := &fakeRunner{}
		ctrl := NewRdtPolicyControllerWithReader(runner, nil, caches)
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
		runner := &fakeRunner{}
		ctrl := NewRdtPolicyControllerWithReader(runner, nil, caches)
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
		Clos: "worker_class",
	}

	runner := &fakeRunner{}
	ctrl := NewRdtPolicyControllerWithReader(runner, nil, caches)

	if err := ctrl.Verify(context.Background(), res); err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("expected 0 runner calls for Verify, got %d", len(runner.calls))
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
		Clos: "worker_class",
	}

	t.Run("successful release sets partition and class to null", func(t *testing.T) {
		runner := &fakeRunner{}
		ctrl := NewRdtPolicyControllerWithReader(runner, nil, caches)

		if err := ctrl.Release(context.Background(), res); err != nil {
			t.Fatalf("Release failed: %v", err)
		}
		if len(runner.calls) != 1 {
			t.Fatalf("expected 1 runner call, got %d", len(runner.calls))
		}

		expectedCall := `kubectl -n kube-system patch balloonspolicies default --type=merge --patch {"spec":{"control":{"rdt":{"partitions":{"worker":null},"classes":{"worker_class":null}}}}}`
		if runner.calls[0] != expectedCall {
			t.Fatalf("expected call %q, got %q", expectedCall, runner.calls[0])
		}
	})

	t.Run("runner failure returns error", func(t *testing.T) {
		runner := &fakeRunner{err: errors.New("failed to patch")}
		ctrl := NewRdtPolicyControllerWithReader(runner, nil, caches)

		if err := ctrl.Release(context.Background(), res); err == nil {
			t.Fatal("expected error on runner failure, got nil")
		}
	})
}

func TestRdtPolicyControllerWaitTimeout(t *testing.T) {
	runner := &fakeRunner{}
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
	ctrl := NewRdtPolicyControllerWithReader(runner, reader, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := ctrl.waitForResourcePolicyUpdate(ctx, "worker", "worker_class")
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}
