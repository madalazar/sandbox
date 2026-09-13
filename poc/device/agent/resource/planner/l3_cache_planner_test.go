package planner

import (
	"context"
	"errors"
	"testing"

	"github.com/margo/sandbox/poc/device/agent/types"
)

func TestL3CachePlannerInterface(t *testing.T) {
	var _ CachePlanner = (*L3CachePlanner)(nil)

	planner := NewL3CachePlanner([]types.HostTopologyCache{})
	if planner == nil {
		t.Fatal("expected non-nil L3CachePlanner")
	}

	_, err := planner.PlanCache(context.Background(), CachePlanningRequest{})
	if !errors.Is(err, errNotImplemented) {
		t.Fatalf("PlanCache() error = %v, want %v", err, errNotImplemented)
	}
}
