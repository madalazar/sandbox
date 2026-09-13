package controller

import (
	"context"
	"testing"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

type dummyIsolationController struct{}

func (d *dummyIsolationController) Apply(ctx context.Context, r model.Reservation) error {
	return nil
}

func (d *dummyIsolationController) Verify(ctx context.Context, r model.Reservation) error {
	return nil
}

func (d *dummyIsolationController) Release(ctx context.Context, r model.Reservation) error {
	return nil
}

func TestCacheIsolationControllerInterface(t *testing.T) {
	var _ CacheIsolationController = (*dummyIsolationController)(nil)

	ctrl := &dummyIsolationController{}
	ctx := context.Background()
	res := model.Reservation{}

	if err := ctrl.Apply(ctx, res); err != nil {
		t.Fatalf("unexpected Apply error: %v", err)
	}
	if err := ctrl.Verify(ctx, res); err != nil {
		t.Fatalf("unexpected Verify error: %v", err)
	}
	if err := ctrl.Release(ctx, res); err != nil {
		t.Fatalf("unexpected Release error: %v", err)
	}
}
