package resource

import (
	"context"
	"errors"
	"testing"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

// the skeletons are wired but not yet implemented; the bodies land in a later commit
func TestResourceCoordinatorIsNotYetImplemented(t *testing.T) {
	c := NewResourceCoordinator(nil, nil)

	if _, err := c.newLedger("deployment-1"); !errors.Is(err, errNotImplemented) {
		t.Fatalf("NewLedger() error = %v, want %v", err, errNotImplemented)
	}
	if _, err := c.Plan(nil, ResourceRequest{}); !errors.Is(err, errNotImplemented) {
		t.Fatalf("Plan() error = %v, want %v", err, errNotImplemented)
	}
	if err := c.Commit(context.Background(), ResourcePlan{}); !errors.Is(err, errNotImplemented) {
		t.Fatalf("Commit() error = %v, want %v", err, errNotImplemented)
	}
	if err := c.Release(context.Background(), model.OwnerRef{}); !errors.Is(err, errNotImplemented) {
		t.Fatalf("Release() error = %v, want %v", err, errNotImplemented)
	}
}

// Activate has no cpu-only work, so it is a no-op rather than a stub
func TestResourceCoordinatorActivateIsANoOp(t *testing.T) {
	if err := NewResourceCoordinator(nil, nil).Activate(context.Background(), model.OwnerRef{}); err != nil {
		t.Fatalf("Activate() error = %v, want nil", err)
	}
}
