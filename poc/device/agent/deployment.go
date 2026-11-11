// deploy/manager.go
package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kr/pretty"
	"github.com/margo/sandbox/poc/device/agent/database"
	"github.com/margo/sandbox/poc/device/agent/resource"
	"github.com/margo/sandbox/poc/device/agent/resource/configurator"
	"github.com/margo/sandbox/poc/device/agent/resource/controller"
	"github.com/margo/sandbox/poc/device/agent/resource/model"
	"github.com/margo/sandbox/poc/device/agent/resource/planner"
	"github.com/margo/sandbox/poc/device/agent/types"
	"github.com/margo/sandbox/shared-lib/constraints"
	"github.com/margo/sandbox/shared-lib/workloads"
	"github.com/margo/sandbox/standard/generatedCode/wfm/sbi"
	"github.com/margo/sandbox/standard/pkg"
	"go.uber.org/zap"
	"k8s.io/client-go/dynamic"
)

type DeploymentManagerIfc interface {
	Start()
	Stop()
}

type DeploymentManager struct {
	database           database.DatabaseIfc
	helmClient         *workloads.HelmClient
	composeClient      *workloads.DockerComposeCliClient
	capabilities       *sbi.DeviceCapabilitiesManifest
	policyReader       model.BalloonPolicyReader
	composeCoordinator *resource.ResourceCoordinator
	helmCoordinator    *resource.ResourceCoordinator
	dynClient          dynamic.Interface
	log                *zap.SugaredLogger
	stopChan           chan struct{}
	hostTopology       types.HostTopology
	rtEnabled          bool
	//  Mutex to prevent concurrent reconciliation
	reconcileLocks sync.Map // map[deploymentId]bool
}

func NewDeploymentManager(
	db database.DatabaseIfc,
	capabilities *sbi.DeviceCapabilitiesManifest,
	helmClient *workloads.HelmClient,
	composeClient *workloads.DockerComposeCliClient,
	policyReader model.BalloonPolicyReader,
	dynClient dynamic.Interface,
	hostTopology types.HostTopology,
	rtEnabled bool,
	log *zap.SugaredLogger,
) (*DeploymentManager, error) {
	var composeCoord *resource.ResourceCoordinator
	var helmCoord *resource.ResourceCoordinator

	if rtEnabled {
		allocMu := &sync.Mutex{}

		var err error
		composeCoord, err = resource.NewResourceCoordinatorBuilder().
			WithStore(resource.NewDatabaseReservationStore(db, hostTopology.IsolatedCpuSet)).
			WithCpuPlanner(planner.NewTopologyCpuPlanner(hostTopology.IsolatedCpuIndices)).
			WithCachePlanner(planner.NewL3CachePlanner(hostTopology.L3Caches)).
			WithCacheTopology(hostTopology.L3Caches, hostTopology.MaxClos).
			WithClassNamer(controller.NewPqosClassNamer()).
			WithCacheController(controller.NewPqosCacheController(controller.NewNsenterRunner(), hostTopology.L3Caches, hostTopology.MaxClos)).
			WithAllocationLock(allocMu).
			Build()
		if err != nil {
			return nil, fmt.Errorf("failed to build compose resource coordinator: %w", err)
		}

		helmCoord, err = resource.NewResourceCoordinatorBuilder().
			WithStore(resource.NewDatabaseReservationStore(db, hostTopology.IsolatedCpuSet)).
			WithCpuPlanner(planner.NewBalloonCpuPlanner(policyReader, hostTopology.IsolatedCpuIndices)).
			WithCachePlanner(planner.NewL3CachePlanner(hostTopology.L3Caches)).
			WithCacheTopology(hostTopology.L3Caches, hostTopology.MaxClos).
			WithClassNamer(controller.NewRdtClassNamer()).
			WithCacheController(controller.NewRdtPolicyControllerWithClient(dynClient, policyReader, hostTopology.L3Caches)).
			WithAllocationLock(allocMu).
			Build()
		if err != nil {
			return nil, fmt.Errorf("failed to build helm resource coordinator: %w", err)
		}
	}

	return &DeploymentManager{
		database:           db,
		helmClient:         helmClient,
		composeClient:      composeClient,
		policyReader:       policyReader,
		composeCoordinator: composeCoord,
		helmCoordinator:    helmCoord,
		dynClient:          dynClient,
		hostTopology:       hostTopology,
		rtEnabled:          rtEnabled,
		log:                log,
		capabilities:       capabilities,
		stopChan:           make(chan struct{}),
		reconcileLocks:     sync.Map{},
	}, nil
}

func (dm *DeploymentManager) Start() {
	// Subscribe to database changes
	dm.database.Subscribe(dm.onDeploymentChange)

	// Start reconciliation loop
	go dm.reconcileLoop()
}

func (dm *DeploymentManager) Stop() {
	close(dm.stopChan)
}

func (dm *DeploymentManager) onDeploymentChange(
	deploymentId string,
	record *database.DeploymentRecord,
	changeType database.DeploymentRecordChangeType,
) {
	if changeType == database.DeploymentChangeTypeDesiredStateAdded {
		if dm.database.NeedsReconciliation(deploymentId) {
			dm.log.Infow("Deployment needs reconciliation", "appId", deploymentId)
			go dm.reconcileDeployment(deploymentId)
		}
	}
}

func (dm *DeploymentManager) reconcileLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			dm.reconcileAll()
		case <-dm.stopChan:
			return
		}
	}
}

func (dm *DeploymentManager) reconcileAll() {
	deployments := dm.database.ListDeployments()
	for _, deployment := range deployments {
		if dm.database.NeedsReconciliation(deployment.DeploymentID) {
			go dm.reconcileDeployment(deployment.DeploymentID)
		}
	}
}

func (dm *DeploymentManager) reconcileDeployment(deploymentId string) {
	//  Prevent concurrent reconciliation of the same deployment
	if _, loaded := dm.reconcileLocks.LoadOrStore(deploymentId, true); loaded {
		dm.log.Debugw("Reconciliation already in progress, skipping", "deploymentId", deploymentId)
		return
	}
	defer dm.reconcileLocks.Delete(deploymentId)

	record, err := dm.database.GetDeployment(deploymentId)
	if err != nil {
		dm.log.Errorw("Failed to get deployment", "deploymentId", deploymentId, "error", err)
		return
	}

	if record.DesiredState == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Get the desired state from the manifest
	desiredState := record.DesiredState.Status.Status.State

	// Get current state (what's actually deployed)
	var currentState sbi.DeploymentStatusManifestStatusState
	if record.CurrentState != nil {
		currentState = record.CurrentState.Status.Status.State
	} else {
		currentState = sbi.DeploymentStatusManifestStatusStatePending
	}

	dm.log.Debugw("Reconciling deployment",
		"deploymentId", deploymentId,
		"desiredState", desiredState,
		"currentState", currentState)

	// Only reconcile if states don't match
	switch desiredState {
	case sbi.DeploymentStatusManifestStatusStatePending:
		// Only deploy if not already installed
		if currentState != sbi.DeploymentStatusManifestStatusStateInstalled {
			dm.log.Debugw("deploying pending deployment", "deploymentId", deploymentId)
			dm.deployOrUpdate(ctx, deploymentId, *record.DesiredState)
		} else {
			dm.log.Debugw("deployment already installed, skipping", "deploymentId", deploymentId)
		}

	case sbi.DeploymentStatusManifestStatusStateInstalling:
		// Only deploy if not already installed
		if currentState != sbi.DeploymentStatusManifestStatusStateInstalled {
			dm.log.Debugw("deploying or updating the deployment", "deploymentId", deploymentId)
			dm.deployOrUpdate(ctx, deploymentId, *record.DesiredState)
		} else {
			dm.log.Debugw("deployment already installed, skipping", "deploymentId", deploymentId)
		}

	case sbi.DeploymentStatusManifestStatusStateRemoving:
		// Only remove if not already removed
		if currentState != sbi.DeploymentStatusManifestStatusStateRemoved {
			dm.log.Debugw("removing the deployment", "deploymentId", deploymentId)
			dm.remove(ctx, deploymentId)
		} else {
			dm.log.Debugw("deployment already removed, skipping", "deploymentId", deploymentId)
		}

	case sbi.DeploymentStatusManifestStatusStateRemoved:
		dm.log.Debugw("deployment already removed", "deploymentId", deploymentId)
		return

	case sbi.DeploymentStatusManifestStatusStateInstalled:
		// Check if current state matches
		if currentState != sbi.DeploymentStatusManifestStatusStateInstalled {
			dm.log.Debugw(
				"current state doesn't match desired, reconciling",
				"deploymentId",
				deploymentId,
			)
			dm.deployOrUpdate(ctx, deploymentId, *record.DesiredState)
		} else {
			dm.log.Debugw(
				"deployment already installed and matches desired state",
				"deploymentId",
				deploymentId,
			)
		}

	case sbi.DeploymentStatusManifestStatusStateFailed:
		dm.log.Warnw("deployment in failed state", "deploymentId", deploymentId)
		return

	default:
		dm.log.Warnw(
			"unknown deployment state",
			"deploymentId",
			deploymentId,
			"state",
			desiredState,
		)
	}
}

func (dm *DeploymentManager) checkDeviceEligibility(
	desiredState database.AppDeploymentState,
) (bool, string, error) {
	dc := constraints.New()
	ok, reason, err := dc.IsDeviceEligible(
		dm.capabilities,
		desiredState.Spec.DeploymentProfile.DeviceConstraints,
	)
	if err != nil {
		return false, "", err
	}

	if !ok {
		return false, reason, nil
	}

	return true, "", nil
}

func (dm *DeploymentManager) deployOrUpdate(
	ctx context.Context,
	deploymentId string,
	desiredState database.AppDeploymentState,
) {
	// Use the AppDeploymentManifest directly instead of converting
	appDeployment := desiredState.AppDeploymentManifest

	// Get component
	if len(appDeployment.Spec.DeploymentProfile.Components) == 0 {
		// Set current state even on failure
		failedState := desiredState
		failedState.Status.Status.State = sbi.DeploymentStatusManifestStatusStateFailed
		dm.database.SetCurrentState(deploymentId, failedState)
		dm.database.SetPhase(deploymentId, "FAILED", "No components found")
		return
	}

	ok, reason, err := dm.checkDeviceEligibility(desiredState)
	if err != nil {
		// Set current state even on failure
		failedState := desiredState
		failedState.Status.Status.State = sbi.DeploymentStatusManifestStatusStateFailed
		dm.database.SetCurrentState(deploymentId, failedState)
		dm.database.SetPhase(
			deploymentId,
			"FAILED",
			fmt.Sprintf("failed to check for device eligibility, err : %s", err.Error()),
		)
		return
	}

	if !ok {
		// Set current state even on failure
		failedState := desiredState
		failedState.Status.Status.State = sbi.DeploymentStatusManifestStatusStateFailed
		dm.database.SetCurrentState(deploymentId, failedState)
		dm.database.SetPhase(
			deploymentId,
			"FAILED",
			fmt.Sprintf("device ineligibile, reason : %s", reason),
		)
		return
	}

	dm.log.Infow("device eligible for deployment", "appId", deploymentId)

	// Initialize per-component status for ALL components before starting deployment.
	// This ensures the status report always contains one entry per component (spec requirement).
	componentNames := dm.extractComponentNames(appDeployment)
	for _, name := range componentNames {
		dm.database.SetComponentStatus(deploymentId, name, sbi.ComponentStatus{
			Name:  name,
			State: sbi.ComponentStatusStateInstalling,
		})
	}

	dm.database.SetPhase(deploymentId, "DEPLOYING", "Starting deployment")

	profileType := appDeployment.Spec.DeploymentProfile.Type

	switch profileType {
	case sbi.AppDeploymentProfileTypeHelm:
		//  Check if Helm client is available
		if dm.helmClient == nil {
			err = fmt.Errorf(
				"helm client not initialized (device may not support Helm deployments)",
			)
		} else {
			err = dm.deployOrUpdateHelm(ctx, deploymentId, appDeployment)
		}

	case sbi.AppDeploymentProfileTypeCompose:
		// Check if Compose client is available
		if dm.composeClient == nil {
			err = fmt.Errorf(
				"docker Compose client not initialized (device may not support Compose deployments)",
			)
		} else {
			err = dm.deployOrUpdateCompose(ctx, deploymentId, appDeployment)
		}

	default:
		// Set current state on unsupported type
		for _, name := range componentNames {
			dm.database.SetComponentStatus(deploymentId, name, sbi.ComponentStatus{
				Name:  name,
				State: sbi.ComponentStatusStateFailed,
			})
		}
		failedState := desiredState
		failedState.Status.Status.State = sbi.DeploymentStatusManifestStatusStateFailed
		dm.database.SetCurrentState(deploymentId, failedState)
		dm.database.SetPhase(
			deploymentId,
			"FAILED",
			fmt.Sprintf("Unsupported deployment type: %s", profileType),
		)
		return
	}

	// Handle deployment errors
	if err != nil {
		for _, name := range componentNames {
			errMsg := err.Error()
			dm.database.SetComponentStatus(deploymentId, name, sbi.ComponentStatus{
				Name:  name,
				State: sbi.ComponentStatusStateFailed,
				Error: &struct {
					Code    *string `json:"code,omitempty"`
					Message *string `json:"message,omitempty"`
					Source  *string `json:"source,omitempty"`
				}{
					Code:    GetAddress("DEPLOYMENT_ERROR"),
					Message: &errMsg,
					Source:  &dm.capabilities.Properties.Id,
				},
			})
		}
		failedState := desiredState
		failedState.Status.Status.State = sbi.DeploymentStatusManifestStatusStateFailed
		dm.database.SetCurrentState(deploymentId, failedState)
		dm.database.SetPhase(
			deploymentId,
			"FAILED",
			fmt.Sprintf("%s operation failed: %v", profileType, err),
		)
		return
	}

	// Success - update all component states to installed
	for _, name := range componentNames {
		dm.database.SetComponentStatus(deploymentId, name, sbi.ComponentStatus{
			Name:  name,
			State: sbi.ComponentStatusStateInstalled,
		})
	}
	currentState := desiredState
	currentState.Status.Status.State = sbi.DeploymentStatusManifestStatusStateInstalled
	dm.database.SetCurrentState(deploymentId, currentState)
	dm.database.SetPhase(deploymentId, "RUNNING", "Deployment successful")
	dm.log.Infow("Deployment successful", "appId", deploymentId)
}

func (dm *DeploymentManager) deployOrUpdateHelm(
	ctx context.Context,
	deploymentId string,
	appDeployment sbi.AppDeploymentManifest,
) (err error) {
	var coordinator *resource.ResourceCoordinator
	var helmConfigurator *configurator.HelmConfigurator
	if dm.rtEnabled {
		coordinator, err = dm.HelmResourceCoordinator()
		if err != nil {
			return fmt.Errorf("unable to initialize helm resource coordinator: %w", err)
		}
		helmConfigurator = configurator.NewHelmConfigurator()
	} else {
		for _, component := range appDeployment.Spec.DeploymentProfile.Components {
			if err = dm.rejectRTRequirementsWhenDisabled(component); err != nil {
				return err
			}
		}
	}

	for _, component := range appDeployment.Spec.DeploymentProfile.Components {
		if err != nil {
			return fmt.Errorf("invalid helm component: %v", err)
		}
		owner := model.NewOwnerRef(deploymentId, component.Name)
		dm.log.Infow("view of the helm component", "helmComp", pretty.Sprint(component))
		dm.log.Infow("deploying app component", "appId", deploymentId, "componentName", component.Name)

		releaseName := fmt.Sprintf("%s-%s", component.Name, deploymentId[:8])

		values := map[string]any{}
		if appDeployment.Spec.Parameters != nil {
			var componentValues map[string]map[string]any
			componentValues, err = pkg.ConvertAllAppDeploymentParamsToValues(
				*appDeployment.Spec.Parameters,
			)
			if err != nil {
				return fmt.Errorf("failed to convert deployment profiles: %w", err)
			}
			if v, exists := componentValues[component.Name]; exists {
				values = normalizeHelmValues(v)
			}
		}

		values["fullnameOverride"] = releaseName

		var rollback *resource.ResourceRollback
		if dm.rtEnabled {
			var plan resource.ResourcePlan
			plan, err = coordinator.Allocate(ctx, resource.ResourceRequest{
				Owner:        owner,
				Requirements: dm.extractComponentCapacityRequirements(component),
			})
			if err != nil {
				return fmt.Errorf("failed to resolve resource policy balloon annotations for component %s: %w", component.Name, err)
			}

			if plan.HasCpu() {
				rollback = resource.NewResourceRollback(ctx, coordinator, owner, dm.log)
				defer rollback.ReleaseOnFailure(&err)

				if err = coordinator.Activate(ctx, owner); err != nil {
					return fmt.Errorf("failed to activate cache isolation for component %s: %w", component.Name, err)
				}
			}

			// update pod annotations for this helm component
			values, err = helmConfigurator.Apply(plan.Cpu, plan.Cache, owner, values)
			if err != nil {
				return fmt.Errorf("failed to apply CPU plan to helm values for component %s: %w", component.Name, err)
			}

			dm.log.Infow("Applied resource annotations to Helm values", "componentName", component.Name, "releaseName",
				releaseName, "podAnnotations", values["podAnnotations"], "componentCPUSet", plan.Cpu.CpuSet())
		}

		dm.log.Infow("Deploying with unique resource names", "releaseName", releaseName, "fullnameOverride", releaseName)

		// Deploy/Update
		release, statusErr := dm.helmClient.GetReleaseStatus(ctx, releaseName, "")
		if statusErr != nil {
			dm.log.Infow(
				"failed to check whether a release exists or not, assuming that it doesn't exist, will proceed with installation",
				"releaseName", releaseName, "deploymentId", deploymentId, "err", statusErr.Error())
		}

		if release != nil {
			// Release exists, update it
			dm.log.Infow("Updating existing Helm release", "releaseName", releaseName, "deploymentId", deploymentId)

			if err = dm.helmClient.UpdateChart(ctx, releaseName, component.Properties.Repository, "", values); err != nil {
				return fmt.Errorf("failed to upgrade existing release: %v", err)
			}

			if rollback != nil {
				rollback.Complete()
			}

			continue
		}

		// New deployment
		dm.log.Infow("Installing new Helm release", "releaseName", releaseName, "deploymentId", deploymentId)

		revision := component.Properties.Revision

		wait := component.Properties.Wait != nil && *component.Properties.Wait
		if err = dm.helmClient.InstallChart(ctx, releaseName, component.Properties.Repository, "", revision, wait, values); err != nil {
			return err
		}

		dm.log.Infow("Helm deployment successful", "appId", deploymentId, "releaseName", releaseName)

		if rollback != nil {
			rollback.Complete()
		}
	}

	return nil
}

func (dm *DeploymentManager) deployOrUpdateCompose(
	ctx context.Context,
	deploymentId string,
	appDeployment sbi.AppDeploymentManifest,
) (err error) {
	var coordinator *resource.ResourceCoordinator
	var composeConfigurator *configurator.ComposeConfigurator
	if dm.rtEnabled {
		coordinator, err = dm.ComposeResourceCoordinator()
		if err != nil {
			return fmt.Errorf("unable to initialize compose resource coordinator: %w", err)
		}
		composeConfigurator = configurator.NewComposeConfigurator()
	} else {
		for _, component := range appDeployment.Spec.DeploymentProfile.Components {
			if err = dm.rejectRTRequirementsWhenDisabled(component); err != nil {
				return err
			}
		}
	}

	for _, component := range appDeployment.Spec.DeploymentProfile.Components {
		if err != nil {
			return fmt.Errorf("invalid compose component %v", err)
		}
		owner := model.NewOwnerRef(deploymentId, component.Name)
		dm.log.Infow("deploying app component", "appId", deploymentId, "componentName", component.Name)

		// Get compose content from package location
		dm.log.Infow("view of the compose component", "composecomp", pretty.Sprint(component))
		dm.log.Infow("compose component capacity reqs", "appId", deploymentId, "profileType",
			appDeployment.Spec.DeploymentProfile.Type, "profile device constraints",
			appDeployment.Spec.DeploymentProfile.DeviceConstraints, "componentName",
			component.Name, "component capacity reqs", dm.extractComponentCapacityRequirements(component))

		// Generate project name (must be valid Docker Compose project name)
		projectName := fmt.Sprintf("%s-%s", strings.ToLower(component.Name), deploymentId[:8])
		projectName = strings.ReplaceAll(projectName, "_", "-")

		values := map[string]any{}
		if appDeployment.Spec.Parameters != nil {
			var componentValues map[string]map[string]any
			componentValues, err = pkg.ConvertAllAppDeploymentParamsToValues(
				*appDeployment.Spec.Parameters,
			)
			if err != nil {
				return fmt.Errorf("failed to parse compose parameters: %w", err)
			}
			if v, exists := componentValues[component.Name]; exists {
				values = v
			}
		}

		composeFilename, err := dm.composeClient.DownloadCompose(ctx, component.Properties.Repository,
			component.Properties.Revision, projectName)
		if err != nil {
			return fmt.Errorf("failed to get compose content: %v", err)
		}
		dm.log.Debugw("compose file downloaded", "composeFilename", composeFilename)

		var rollback *resource.ResourceRollback
		preparedComposeFilename := composeFilename

		if dm.rtEnabled {
			var resourcePlan resource.ResourcePlan
			resourcePlan, err = coordinator.Allocate(ctx, resource.ResourceRequest{
				Owner:        owner,
				Requirements: dm.extractComponentCapacityRequirements(component),
			})
			if err != nil {
				return fmt.Errorf("failed to resolve compose cpu assignments for component %s: %w", component.Name, err)
			}

			dm.log.Debugw("assignments for current component", "assignments", resourcePlan.Cpu.Cpus)

			if resourcePlan.HasCpu() {
				rollback = resource.NewResourceRollback(ctx, coordinator, owner, dm.log)
				defer rollback.ReleaseOnFailure(&err)
			}

			if resourcePlan.HasCpu() {
				var prepErr error
				preparedComposeFilename, prepErr = composeConfigurator.Apply(resourcePlan.Cpu, owner, composeFilename)
				if prepErr != nil {
					err = fmt.Errorf("failed to prepare compose file for component %s: %w", component.Name, prepErr)
					return err
				}

				defer func(prep string, src string) {
					if prep != src {
						if removeErr := os.Remove(prep); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
							dm.log.Warnw("Failed to remove compose file", "path", prep, "error", removeErr)
						}
					}
				}(preparedComposeFilename, composeFilename)
			}
		}

		// Convert parameters to environment variables
		envVars := dm.convertParametersToEnvVars(values, component.Name)

		// Check if project already exists
		var exists bool
		exists, err = dm.composeClient.ComposeExists(ctx, preparedComposeFilename, projectName)
		if err != nil {
			return fmt.Errorf("failed to check compose project existence: %v", err)
		}

		if exists {
			// Update existing deployment
			dm.log.Infow("Updating existing Docker Compose project",
				"projectName", projectName, "deploymentId", deploymentId, "composeFilename", preparedComposeFilename)
			err = dm.composeClient.UpdateCompose(ctx, projectName, preparedComposeFilename, envVars)
		} else {
			// New deployment
			dm.log.Infow("Deploying new Docker Compose project",
				"projectName", projectName, "deploymentId", deploymentId, "composeFilename", preparedComposeFilename)
			err = dm.composeClient.DeployCompose(ctx, projectName, preparedComposeFilename, envVars)
		}

		if err != nil {
			return fmt.Errorf("docker compose operation failed: %v", err)
		}

		dm.log.Infow("Docker Compose deployment successful",
			"appId", deploymentId, "componentName", component.Name, "projectName", projectName)

		if rollback != nil {
			rollback.Complete()
		}
	}
	return nil
}

func (dm *DeploymentManager) remove(ctx context.Context, deploymentId string) {
	record, err := dm.database.GetDeployment(deploymentId)
	if err != nil {
		dm.log.Warnw("Deployment not found for removal", "deploymentId", deploymentId)
		return
	}

	if record.CurrentState == nil {
		dm.log.Infow(
			"No current state found, proceeding with complete removal",
			"deploymentId",
			deploymentId,
		)

		// Update desired state to REMOVED before deleting
		if record.DesiredState != nil {
			componentNames := dm.extractComponentNames(record.DesiredState.AppDeploymentManifest)
			for _, name := range componentNames {
				dm.database.SetComponentStatus(deploymentId, name, sbi.ComponentStatus{
					Name:  name,
					State: sbi.ComponentStatusStateRemoved,
				})
			}

			removedState := *record.DesiredState
			removedState.Status.Status.State = sbi.DeploymentStatusManifestStatusStateRemoved
			dm.database.SetCurrentState(deploymentId, removedState)
		}

		dm.database.SetPhase(deploymentId, "REMOVED", "Removal Complete")
		dm.database.RemoveDeployment(deploymentId)
		return
	}

	// Use the AppDeploymentManifest directly
	appDeployment := record.CurrentState.AppDeploymentManifest

	// Initialize per-component status to "removing"
	componentNames := dm.extractComponentNames(appDeployment)
	for _, name := range componentNames {
		dm.database.SetComponentStatus(deploymentId, name, sbi.ComponentStatus{
			Name:  name,
			State: sbi.ComponentStatusStateRemoving,
		})
	}

	//  Set current state to REMOVING
	currentState := *record.CurrentState
	currentState.Status.Status.State = sbi.DeploymentStatusManifestStatusStateRemoving
	dm.database.SetCurrentState(deploymentId, currentState)
	dm.database.SetPhase(deploymentId, "REMOVING", "Starting removal")

	if len(appDeployment.Spec.DeploymentProfile.Components) == 0 {
		dm.log.Warnw("No components to remove", "deploymentId", deploymentId)

		// Update state to REMOVED
		removedState := currentState
		removedState.Status.Status.State = sbi.DeploymentStatusManifestStatusStateRemoved
		dm.database.SetCurrentState(deploymentId, removedState)

		dm.database.SetPhase(deploymentId, "REMOVED", "No components to remove")
		dm.database.RemoveDeployment(deploymentId)
		return
	}

	// Route removal based on deployment type
	profileType := appDeployment.Spec.DeploymentProfile.Type

	var removeErr error
	switch profileType {
	case sbi.AppDeploymentProfileTypeHelm:
		removeErr = dm.removeHelm(ctx, deploymentId, appDeployment)
	case sbi.AppDeploymentProfileTypeCompose:
		removeErr = dm.removeCompose(ctx, deploymentId, appDeployment)
	default:
		dm.log.Warnw(
			"Unknown deployment type for removal",
			"type",
			profileType,
			"deploymentId",
			deploymentId,
		)
	}

	// Update per-component status to "removed" (or "failed")
	for _, name := range componentNames {
		if removeErr != nil {
			dm.database.SetComponentStatus(deploymentId, name, sbi.ComponentStatus{
				Name:  name,
				State: sbi.ComponentStatusStateFailed,
				Error: &struct {
					Code    *string `json:"code,omitempty"`
					Message *string `json:"message,omitempty"`
					Source  *string `json:"source,omitempty"`
				}{
					Code:    GetAddress("REMOVAL_ERROR"),
					Message: GetAddress(removeErr.Error()),
					Source:  &dm.capabilities.Properties.Id,
				},
			})
		} else {
			dm.database.SetComponentStatus(deploymentId, name, sbi.ComponentStatus{
				Name:  name,
				State: sbi.ComponentStatusStateRemoved,
			})
		}
	}

	if removeErr != nil {
		dm.log.Errorw("Removal failed",
			"deploymentId", deploymentId,
			"error", removeErr)
		failedState := currentState
		failedState.Status.Status.State = sbi.DeploymentStatusManifestStatusStateFailed
		dm.database.SetCurrentState(deploymentId, failedState)
		dm.database.SetPhase(
			deploymentId,
			"FAILED",
			fmt.Sprintf("Removal failed: %v", removeErr),
		)
		return
	}

	// Update current state to REMOVED only when all components were removed and released
	removedState := currentState
	removedState.Status.Status.State = sbi.DeploymentStatusManifestStatusStateRemoved
	dm.database.SetCurrentState(deploymentId, removedState)
	dm.database.SetPhase(deploymentId, "REMOVED", "Removal Complete")

	// Remove from local database (triggers status report via subscriber)
	dm.database.RemoveDeployment(deploymentId)

	dm.log.Infow("Removal completed", "appId", deploymentId)
}

func (dm *DeploymentManager) removeHelm(
	ctx context.Context,
	deploymentId string,
	appDeployment sbi.AppDeploymentManifest,
) error {
	if dm.helmClient == nil {
		dm.log.Warnw("Helm client not initialized, skipping", "deploymentId", deploymentId)
		return nil
	}

	var coordinator *resource.ResourceCoordinator
	if dm.rtEnabled {
		var err error
		coordinator, err = dm.HelmResourceCoordinator()
		if err != nil {
			return fmt.Errorf("unable to initialize helm resource coordinator: %w", err)
		}
	}

	var errs []error
	for _, component := range appDeployment.Spec.DeploymentProfile.Components {
		releaseName := fmt.Sprintf("%s-%s", component.Name, deploymentId[:8])
		dm.log.Infow(
			"Removing Helm release",
			"releaseName", releaseName,
			"componentName", component.Name,
			"deploymentId", deploymentId,
		)

		if err := dm.helmClient.UninstallChart(ctx, releaseName, ""); err != nil {
			dm.log.Warnw(
				"Failed to uninstall Helm chart",
				"releaseName", releaseName,
				"componentName", component.Name,
				"error", err,
			)
			errs = append(errs, fmt.Errorf("failed to uninstall helm chart %q for component %q: %w", releaseName, component.Name, err))
			continue
		}

		dm.log.Infow(
			"Helm release removed successfully",
			"releaseName", releaseName,
			"componentName", component.Name,
		)

		if coordinator != nil {
			owner := model.NewOwnerRef(deploymentId, component.Name)
			if err := coordinator.Release(ctx, owner); err != nil {
				dm.log.Warnw("Failed to release helm component reservation during removal",
					"deploymentId", deploymentId,
					"componentName", component.Name,
					"error", err)
				errs = append(errs, fmt.Errorf("failed to release reservation for component %q: %w", component.Name, err))
			}
		}
	}
	return errors.Join(errs...)
}

func (dm *DeploymentManager) removeCompose(
	ctx context.Context,
	deploymentId string,
	appDeployment sbi.AppDeploymentManifest,
) error {
	if dm.composeClient == nil {
		dm.log.Warnw("Compose client not initialized, skipping", "deploymentId", deploymentId)
		return nil
	}

	var coordinator *resource.ResourceCoordinator
	if dm.rtEnabled {
		var err error
		coordinator, err = dm.ComposeResourceCoordinator()
		if err != nil {
			return fmt.Errorf("unable to initialize compose resource coordinator: %w", err)
		}
	}

	var errs []error
	// Iterate through ALL components (matching deployOrUpdateCompose pattern)
	for _, component := range appDeployment.Spec.DeploymentProfile.Components {
		projectName := fmt.Sprintf("%s-%s", strings.ToLower(component.Name), deploymentId[:8])
		projectName = strings.ReplaceAll(projectName, "_", "-")

		dm.log.Infow(
			"Removing Docker Compose project",
			"projectName", projectName,
			"componentName", component.Name,
			"deploymentId", deploymentId,
		)

		if err := dm.composeClient.RemoveCompose(ctx, projectName); err != nil {
			dm.log.Warnw(
				"Failed to remove Docker Compose project",
				"projectName", projectName,
				"componentName", component.Name,
				"error", err,
			)
			errs = append(errs, fmt.Errorf("failed to remove compose project %q for component %q: %w", projectName, component.Name, err))
			continue
		}

		dm.log.Infow(
			"Docker Compose project removed successfully",
			"projectName", projectName,
			"componentName", component.Name,
		)

		if coordinator != nil {
			owner := model.NewOwnerRef(deploymentId, component.Name)
			if err := coordinator.Release(ctx, owner); err != nil {
				dm.log.Warnw("Failed to release compose component reservation during removal",
					"deploymentId", deploymentId,
					"componentName", component.Name,
					"error", err)
				errs = append(errs, fmt.Errorf("failed to release reservation for component %q: %w", component.Name, err))
			}
		}
	}
	return errors.Join(errs...)
}

func (dm *DeploymentManager) extractComponentNames(
	appDeployment sbi.AppDeploymentManifest,
) []string {
	names := make([]string, 0, len(appDeployment.Spec.DeploymentProfile.Components))
	for _, comp := range appDeployment.Spec.DeploymentProfile.Components {
		names = append(names, comp.Name) // direct access, no union unwrapping needed
	}
	return names
}

func GetAddress[T any](a T) *T {
	return &a
}

// Helper function to convert parameters to environment variables
func (dm *DeploymentManager) convertParametersToEnvVars(
	params map[string]any,
	componentName string,
) map[string]string {
	envVars := make(map[string]string)

	// Convert component-specific parameters
	if componentParams, exists := params[componentName]; exists {
		if paramMap, ok := componentParams.(map[string]any); ok {
			for key, value := range paramMap {
				envVars[strings.ToUpper(key)] = dm.formatEnvValue(value)
			}
		}
	}

	// Convert global parameters
	for key, value := range params {
		if key != componentName { // Skip component-specific params already processed
			envVars[strings.ToUpper(key)] = dm.formatEnvValue(value)
		}
	}

	return envVars
}

// extractComponentCapacityRequirements reads the capacity requirements from the component
func (dm *DeploymentManager) extractComponentCapacityRequirements(
	component sbi.ApplicationDeploymentProfileComponent,
) *sbi.CapacityRequirements {
	if component.DeviceConstraints != nil && component.DeviceConstraints.CapacityRequirements != nil {
		return component.DeviceConstraints.CapacityRequirements
	}

	return nil
}

// rejectRTRequirementsWhenDisabled returns an error if a component requests
// isolated CPU cores or exclusive L3 cache when the RT feature is disabled.
func (dm *DeploymentManager) rejectRTRequirementsWhenDisabled(
	component sbi.ApplicationDeploymentProfileComponent,
) error {
	reqs := dm.extractComponentCapacityRequirements(component)
	if reqs == nil {
		return nil
	}

	ref := model.ComponentRef(component.Name)
	cpuReqs, err := model.NormalizeCpuRequirements(ref, reqs)
	if err != nil {
		return err
	}
	if cpuReqs.HasIsolatedCores() {
		return fmt.Errorf(
			"component %q requests isolated CPU cores, but RT feature is not supported",
			component.Name,
		)
	}

	cacheReqs, err := model.NormalizeCacheRequirements(ref, reqs)
	if err != nil {
		return err
	}
	if cacheReqs.HasCache() {
		return fmt.Errorf(
			"component %q requests exclusive cache, but RT feature is not supported",
			component.Name,
		)
	}

	return nil
}

func (dm *DeploymentManager) ComposeResourceCoordinator() (*resource.ResourceCoordinator, error) {
	if dm.composeCoordinator == nil {
		return nil, errors.New("compose resource coordinator not initialized")
	}

	return dm.composeCoordinator, nil
}

func (dm *DeploymentManager) HelmResourceCoordinator() (*resource.ResourceCoordinator, error) {
	if dm.helmCoordinator == nil {
		return nil, errors.New("helm resource coordinator not initialized")
	}

	return dm.helmCoordinator, nil
}

// needed to properly convert integers with more > 6 digits
func (dm *DeploymentManager) formatEnvValue(value any) string {
	switch v := value.(type) {
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Sprintf("%v", v)
		}
		if v == math.Trunc(v) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case float32:
		f := float64(v)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return fmt.Sprintf("%v", v)
		}
		if f == math.Trunc(f) {
			return strconv.FormatInt(int64(f), 10)
		}
		return strconv.FormatFloat(f, 'f', -1, 32)
	default:
		return fmt.Sprintf("%v", value)
	}
}

// normalizeHelmValues recursively normalizes values before Helm templating.
// JSON-decoded numeric values arrive as float64; convert whole-number floats
// to int64 so templates render integers (e.g. 1000000 instead of 1e+06).
func normalizeHelmValues(values map[string]any) map[string]any {
	normalized := make(map[string]any, len(values))
	for key, value := range values {
		normalized[key] = normalizeHelmValue(value)
	}
	return normalized
}

func normalizeHelmValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		normalized := make(map[string]any, len(v))
		for key, nested := range v {
			normalized[key] = normalizeHelmValue(nested)
		}
		return normalized
	case []any:
		normalized := make([]any, len(v))
		for i, nested := range v {
			normalized[i] = normalizeHelmValue(nested)
		}
		return normalized
	case float64:
		if !math.IsNaN(v) && !math.IsInf(v, 0) && v == math.Trunc(v) {
			return int64(v)
		}
		return v
	case float32:
		f := float64(v)
		if !math.IsNaN(f) && !math.IsInf(f, 0) && f == math.Trunc(f) {
			return int64(f)
		}
		return v
	default:
		return value
	}
}
