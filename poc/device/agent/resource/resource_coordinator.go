package resource

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/margo/sandbox/poc/device/agent/resource/controller"
	"github.com/margo/sandbox/poc/device/agent/resource/ledger"
	"github.com/margo/sandbox/poc/device/agent/resource/model"
	"github.com/margo/sandbox/poc/device/agent/resource/planner"
	"github.com/margo/sandbox/standard/generatedCode/wfm/sbi"
	"go.uber.org/zap"
)

const ReleaseOnFailureCtxTimeout = 30 * time.Second

// one component's ask, as it arrives from the manifest. The generated sbi shape is
// unwrapped by normalization inside Plan, so no later stage sees a generated pointer
type ResourceRequest struct {
	Owner        model.OwnerRef
	Requirements *sbi.RequiredResources
}

// what planning decided for one component. It gains a Cache field when the cache
// branch lands, which is why it wraps CpuPlan rather than being it
type ResourcePlan struct {
	Owner model.OwnerRef
	Cpu   model.CpuPlan
	Cache model.CachePlan
}

func (p ResourcePlan) HasCpu() bool {
	return p.Cpu.HasCpus()
}

func (p ResourcePlan) HasCache() bool {
	return p.Cache.HasCache()
}

// owns a component's resource lifecycle, split into the following stages:
//
//	plan     normalize the request and run the appropriate planner against the
//	         deployment's ledger
//	commit   record the reservation before the workload is started, so every later
//	         stage can work from storage rather than from memory
//	activate verify, after the workload is running, that the device still matches
//	         what was committed; might not be needed
//	release  works from storage, never from an in-memory plan: it loads what
//			 was committed for the owner and undoes that
//
// rendering the plan is deliberately not a stage. Its input is a compose file path for
// one runtime and a helm values map for the other, so the deploy (deployOrUpdate* methods)
// code will calls its own configurator directly. A shared configurator might need to be
// further researched
type ResourceCoordinator struct {
	store           ReservationStore
	planner         planner.CpuPlanner
	cachePlanner    planner.CachePlanner
	cacheController controller.CacheIsolationController
}

// constructs a ResourceCoordinator ensuring all dependencies are non-nil.
type ResourceCoordinatorBuilder struct {
	store           ReservationStore
	planner         planner.CpuPlanner
	cachePlanner    planner.CachePlanner
	cacheController controller.CacheIsolationController
}

func NewResourceCoordinatorBuilder() *ResourceCoordinatorBuilder {
	return &ResourceCoordinatorBuilder{}
}

func (b *ResourceCoordinatorBuilder) WithStore(store ReservationStore) *ResourceCoordinatorBuilder {
	b.store = store
	return b
}

func (b *ResourceCoordinatorBuilder) WithCpuPlanner(planner planner.CpuPlanner) *ResourceCoordinatorBuilder {
	b.planner = planner
	return b
}

func (b *ResourceCoordinatorBuilder) WithCachePlanner(planner planner.CachePlanner) *ResourceCoordinatorBuilder {
	b.cachePlanner = planner
	return b
}

func (b *ResourceCoordinatorBuilder) WithCacheController(ctrl controller.CacheIsolationController) *ResourceCoordinatorBuilder {
	b.cacheController = ctrl
	return b
}

func (b *ResourceCoordinatorBuilder) Build() (*ResourceCoordinator, error) {
	var errs []error
	if b.store == nil {
		errs = append(errs, errors.New("reservation store cannot be nil"))
	}
	if b.planner == nil {
		errs = append(errs, errors.New("cpu planner cannot be nil"))
	}
	if b.cachePlanner == nil {
		errs = append(errs, errors.New("cache planner cannot be nil"))
	}
	if b.cacheController == nil {
		errs = append(errs, errors.New("cache isolation controller cannot be nil"))
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return &ResourceCoordinator{
		store:           b.store,
		planner:         b.planner,
		cachePlanner:    b.cachePlanner,
		cacheController: b.cacheController,
	}, nil
}

// takes the device-wide snapshot once, for one reconcile of one deployment. The caller
// threads the result through every component of that deployment, so a component cannot
// be planned onto cpus a sibling took earlier in the same pass
func (c *ResourceCoordinator) NewLedger(deploymentId string) (*ledger.AllocationLedger, error) {
	snapshot, err := c.store.LoadSnapshot()
	if err != nil {
		return nil, err
	}
	return ledger.NewAllocationLedger(snapshot, deploymentId, model.CacheCapacity{}, nil), nil
}

// normalizes the request and asks the planner for cpus, reserving them on the ledger.
// no i/o and no context: planning must stay reproducible from its inputs alone
func (c *ResourceCoordinator) Plan(ledger *ledger.AllocationLedger, request ResourceRequest) (ResourcePlan, error) {
	cpuRequirements, err := model.NormalizeCpuRequirements(request.Owner.Component, request.Requirements)
	if err != nil {
		return ResourcePlan{}, err
	}

	cacheReqs, err := model.NormalizeCacheRequirements(request.Owner.Component, request.Requirements)
	if err != nil {
		return ResourcePlan{}, err
	}

	cpuPlan, err := c.planner.PlanCpu(planner.CpuPlanningRequest{
		Requirements: cpuRequirements,
		Ledger:       ledger,
	})
	if err != nil {
		return ResourcePlan{}, err
	}

	if !cpuPlan.HasCpus() {
		if cacheReqs.HasCache() {
			return ResourcePlan{}, fmt.Errorf("component %q requests exclusive cache but has no cpu assignments",
				request.Owner.Component)
		}

		return ResourcePlan{Owner: request.Owner, Cpu: cpuPlan}, nil
	}

	cachePlan := model.CachePlan{Component: request.Owner.Component}
	if cacheReqs.HasCache() {
		cachePlan, err = c.cachePlanner.PlanCache(planner.CachePlanningRequest{
			Requirements: cacheReqs,
			CpuPlan:      cpuPlan,
			Ledger:       ledger,
		})
		if err != nil {
			return ResourcePlan{}, err
		}
	}

	return ResourcePlan{
		Owner: request.Owner,
		Cpu:   cpuPlan,
		Cache: cachePlan,
	}, nil
}

// records the plan before the workload starts, so nothing is ever applied to the device
// that is not already recoverable from storage
func (c *ResourceCoordinator) Commit(ctx context.Context, plan ResourcePlan) error {
	if !plan.Cpu.HasCpus() && !plan.HasCache() {
		return nil
	}

	reservation := model.Reservation{
		Owner:             model.NewOwnerRef(plan.Owner.Deployment, string(plan.Owner.Component)),
		Cpus:              plan.Cpu.Cpus,
		L3CacheAssignment: plan.Cache.L3CacheAssignment,
	}

	if err := c.store.SaveReservation(plan.Owner.Deployment, reservation); err != nil {
		return err
	}

	if c.cacheController != nil && reservation.HasL3Cache() {
		if err := c.cacheController.Apply(ctx, reservation); err != nil {
			return err
		}
	}

	return nil
}

// verifies, after the workload is running, that the device still matches what was
// committed
func (c *ResourceCoordinator) Activate(ctx context.Context, owner model.OwnerRef) error {
	reservation, found, err := c.store.LoadReservation(owner)
	if err != nil {
		return err
	}
	if !found || !reservation.HasL3Cache() {
		return nil
	}

	if c.cacheController != nil {
		return c.cacheController.Verify(ctx, reservation)
	}

	return nil
}

// releases everything the component holds. Releasing what was never reserved is not an
// error. The context is the release deadline, and the seam the deferred cache
// isolation reset hooks into
func (c *ResourceCoordinator) Release(ctx context.Context, owner model.OwnerRef) error {
	reservation, found, err := c.store.LoadReservation(owner)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}

	var errs []error
	if c.cacheController != nil && reservation.HasL3Cache() {
		if err := c.cacheController.Release(ctx, reservation); err != nil {
			errs = append(errs, err)
		}
	}

	if err := c.store.ClearComponent(reservation.Owner); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

// detaches from the caller's context, because a deploy that failed precisely because
// ctx was cancelled or timed out must still be rolled back. A release failure is logged
// rather than returned: the original failure is what the deployment status must report
func releaseOnFailure(
	ctx context.Context,
	coordinator *ResourceCoordinator,
	owner model.OwnerRef,
	log *zap.SugaredLogger,
) {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ReleaseOnFailureCtxTimeout)
	defer cancel()

	if err := coordinator.Release(releaseCtx, owner); err != nil {
		if log != nil {
			log.Errorw("Failed to release resources during deployment rollback",
				"deploymentId", owner.Deployment,
				"componentName", owner.Component,
				"error", err)
		}
	}
}

// the deploy path's compensation handle. A component that has claimed resources but has
// not yet started successfully must leave nothing behind, and there are many ways to
// fail between those two points: package download, configurator, helm install, status
// write
// releasing at each of them is how one gets missed, so a deploy path arms this
// once - immediately after the reservation is persisted - defers ReleaseOnFailure, and
// calls Complete on the success path.
//
// rollback is scoped to the one failing component. Siblings in the same deployment that
// already started keep their reservations, so a deployment can end up partially
// deployed and marked failed
type ResourceRollback struct {
	ctx         context.Context
	coordinator *ResourceCoordinator
	owner       model.OwnerRef
	// TODO: might not need the logger
	log    *zap.SugaredLogger
	active bool
}

func NewResourceRollback(
	ctx context.Context,
	coordinator *ResourceCoordinator,
	owner model.OwnerRef,
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

// takes the caller's named error return because a deferred call has no other way to see
// whether the function it is unwinding from succeeded
func (r *ResourceRollback) ReleaseOnFailure(err *error) {
	if r.active && err != nil && *err != nil {
		releaseOnFailure(r.ctx, r.coordinator, r.owner, r.log)
	}
}

// disarms the rollback once the component has started successfully
func (r *ResourceRollback) Complete() {
	r.active = false
}
