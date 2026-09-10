package controller

import (
	"context"

	"github.com/margo/sandbox/poc/device/agent/resource"
)

// manages hardware or kernel cache isolation mechanisms (pqos, rdt policy)
// all methods take a committed reservation, ensuring lifecycle operations are driven from persisted state
type CacheIsolationController interface {
	Apply(context.Context, resource.Reservation) error
	Verify(context.Context, resource.Reservation) error
	Release(context.Context, resource.Reservation) error
}
