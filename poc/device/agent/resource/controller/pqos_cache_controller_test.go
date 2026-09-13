package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
	"github.com/margo/sandbox/poc/device/agent/types"
)

func TestPqosCacheControllerInterface(t *testing.T) {
	var _ CacheIsolationController = (*PqosCacheController)(nil)

	ctrl := NewPqosCacheController(nil, []types.HostTopologyCache{}, 0)
	if ctrl == nil {
		t.Fatal("expected non-nil PqosCacheController")
	}

	ctx := context.Background()
	res := model.Reservation{}

	if err := ctrl.Apply(ctx, res); !errors.Is(err, errNotImplemented) {
		t.Fatalf("Apply() error = %v, want %v", err, errNotImplemented)
	}
	if err := ctrl.Verify(ctx, res); !errors.Is(err, errNotImplemented) {
		t.Fatalf("Verify() error = %v, want %v", err, errNotImplemented)
	}
	if err := ctrl.Release(ctx, res); !errors.Is(err, errNotImplemented) {
		t.Fatalf("Release() error = %v, want %v", err, errNotImplemented)
	}
}
