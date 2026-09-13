package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
	"github.com/margo/sandbox/poc/device/agent/types"
)

type fakeRunner struct {
	calls  []string
	output []byte
	err    error
}

func (f *fakeRunner) Run(ctx context.Context, command string, args ...string) ([]byte, error) {
	call := command + " " + strings.Join(args, " ")
	f.calls = append(f.calls, call)
	return f.output, f.err
}

func TestPqosCacheControllerInterface(t *testing.T) {
	var _ CacheIsolationController = (*PqosCacheController)(nil)

	ctrl := NewPqosCacheController(nil, []types.HostTopologyCache{}, 0)
	if ctrl == nil {
		t.Fatal("expected non-nil PqosCacheController")
	}
}

func TestPqosCacheControllerNoCache(t *testing.T) {
	runner := &fakeRunner{}
	ctrl := NewPqosCacheController(runner, nil, 8)
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

func TestPqosCacheControllerApply(t *testing.T) {
	caches := []types.HostTopologyCache{
		{Id: "0", Level: "L3", Ways: 12, WaySizeKB: 1024},
	}

	res := model.Reservation{
		Owner: model.NewOwnerRef("dep-1", "comp-a"),
		Cpus:  []int{2, 3},
		L3CacheAssignment: &model.CacheAssignment{
			CacheId: "0",
			Mask:    "0x3",
			Clos:    "1",
		},
		Clos: "1",
	}

	t.Run("successful apply", func(t *testing.T) {
		runner := &fakeRunner{}
		ctrl := NewPqosCacheController(runner, caches, 8)

		if err := ctrl.Apply(context.Background(), res); err != nil {
			t.Fatalf("Apply failed: %v", err)
		}
		if len(runner.calls) != 1 {
			t.Fatalf("expected 1 runner call, got %d", len(runner.calls))
		}
		expectedPart := "llc@0:1=0x3"
		if !strings.Contains(runner.calls[0], expectedPart) {
			t.Fatalf("expected call to contain %q, got %q", expectedPart, runner.calls[0])
		}
		expectedCore := "core:1=2-3"
		if !strings.Contains(runner.calls[0], expectedCore) {
			t.Fatalf("expected call to contain %q, got %q", expectedCore, runner.calls[0])
		}
	})

	t.Run("runner failure", func(t *testing.T) {
		runner := &fakeRunner{err: errors.New("pqos error")}
		ctrl := NewPqosCacheController(runner, caches, 8)

		if err := ctrl.Apply(context.Background(), res); err == nil {
			t.Fatal("expected error on runner failure, got nil")
		}
	})

	t.Run("missing cpuset", func(t *testing.T) {
		runner := &fakeRunner{}
		ctrl := NewPqosCacheController(runner, caches, 8)
		resNoCPUs := res
		resNoCPUs.Cpus = nil

		if err := ctrl.Apply(context.Background(), resNoCPUs); err == nil {
			t.Fatal("expected error for missing cpuset, got nil")
		}
	})
}

func TestPqosCacheControllerVerify(t *testing.T) {
	caches := []types.HostTopologyCache{
		{Id: "0", Level: "L3", Ways: 12, WaySizeKB: 1024},
	}

	res := model.Reservation{
		Owner: model.NewOwnerRef("dep-1", "comp-a"),
		Cpus:  []int{2, 3},
		L3CacheAssignment: &model.CacheAssignment{
			CacheId: "0",
			Mask:    "0x3",
			Clos:    "1",
		},
		Clos: "1",
	}

	t.Run("verify success", func(t *testing.T) {
		runner := &fakeRunner{output: []byte("COS 1: LLC mask = 0x3, Cores = 2,3")}
		ctrl := NewPqosCacheController(runner, caches, 8)

		if err := ctrl.Verify(context.Background(), res); err != nil {
			t.Fatalf("Verify failed: %v", err)
		}
	})

	t.Run("verify absent class", func(t *testing.T) {
		runner := &fakeRunner{output: []byte("COS 2: LLC mask = 0xf")}
		ctrl := NewPqosCacheController(runner, caches, 8)

		if err := ctrl.Verify(context.Background(), res); err == nil {
			t.Fatal("expected error when class is absent, got nil")
		}
	})
}

func TestPqosCacheControllerRelease(t *testing.T) {
	caches := []types.HostTopologyCache{
		{Id: "0", Level: "L3", Ways: 12, WaySizeKB: 1024},
	}

	res := model.Reservation{
		Owner: model.NewOwnerRef("dep-1", "comp-a"),
		Cpus:  []int{2, 3},
		L3CacheAssignment: &model.CacheAssignment{
			CacheId: "0",
			Mask:    "0x3",
			Clos:    "1",
		},
		Clos: "1",
	}

	t.Run("successful release", func(t *testing.T) {
		runner := &fakeRunner{}
		ctrl := NewPqosCacheController(runner, caches, 8)

		if err := ctrl.Release(context.Background(), res); err != nil {
			t.Fatalf("Release failed: %v", err)
		}
		if len(runner.calls) != 1 {
			t.Fatalf("expected 1 runner call, got %d", len(runner.calls))
		}
		// Reset mask for 12 ways should be 0xFFF
		expectedPart := "llc@0:1=0xFFF"
		if !strings.Contains(runner.calls[0], expectedPart) {
			t.Fatalf("expected call to contain %q, got %q", expectedPart, runner.calls[0])
		}
		// Cores moved back to COS 0
		expectedCore := "core:0=2-3"
		if !strings.Contains(runner.calls[0], expectedCore) {
			t.Fatalf("expected call to contain %q, got %q", expectedCore, runner.calls[0])
		}
	})

	t.Run("unknown cache in topology", func(t *testing.T) {
		runner := &fakeRunner{}
		ctrl := NewPqosCacheController(runner, nil, 8)

		if err := ctrl.Release(context.Background(), res); err == nil {
			t.Fatal("expected error when cache ID not found in topology, got nil")
		}
	})
}
