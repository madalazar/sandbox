package resource

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"
)

// CacheIsolationReleaser resets the runtime cache isolation held by a committed
// reservation. It stands in for CacheIsolationController.Release until the PQoS and
// RDT controllers move into the coordinator.
type CacheIsolationReleaser interface {
	ReleaseIsolation(ctx context.Context, reservation Reservation) error
}

type ResourceCoordinator struct {
	store     ReservationStore
	isolation CacheIsolationReleaser
}

func NewResourceCoordinator(
	store ReservationStore,
	isolation CacheIsolationReleaser,
) *ResourceCoordinator {
	return &ResourceCoordinator{store: store, isolation: isolation}
}

func (c *ResourceCoordinator) Release(ctx context.Context, owner OwnerRef) error {
	reservation, found, loadErr := c.store.LoadReservation(owner)
	if loadErr != nil {
		return loadErr
	}
	if !found {
		return nil
	}

	var errs []error
	if reservation.HasCache() && c.isolation != nil {
		if err := c.isolation.ReleaseIsolation(ctx, reservation); err != nil {
			errs = append(errs, err)
		}
	}

	// Cleared even when the runtime reset failed; holding the record forever is worse.
	if err := c.store.ClearComponent(reservation.Owner); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

func ReleaseOnFailure(
	ctx context.Context,
	coordinator *ResourceCoordinator,
	owner OwnerRef,
	log *zap.SugaredLogger,
) {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	if err := coordinator.Release(releaseCtx, owner); err != nil {
		log.Errorw("Failed to release resources during deployment rollback",
			"deploymentId", owner.Deployment,
			"componentName", owner.Component,
			"error", err)
	}
}

type ResourceRollback struct {
	ctx         context.Context
	coordinator *ResourceCoordinator
	owner       OwnerRef
	log         *zap.SugaredLogger
	active      bool
}

func NewResourceRollback(
	ctx context.Context,
	coordinator *ResourceCoordinator,
	owner OwnerRef,
	log *zap.SugaredLogger,
) *ResourceRollback {
	return &ResourceRollback{
		ctx:         ctx,
		coordinator: coordinator,
		owner:       owner,
		log:         log,
		active:      true,
	}
}

func (r *ResourceRollback) ReleaseOnFailure(err *error) {
	if r.active && err != nil && *err != nil {
		ReleaseOnFailure(r.ctx, r.coordinator, r.owner, r.log)
	}
}

func (r *ResourceRollback) Complete() {
	r.active = false
}
