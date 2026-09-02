// deploy/manager.go
package main

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kr/pretty"
	"github.com/margo/sandbox/poc/device/agent/database"
	"github.com/margo/sandbox/poc/device/agent/device"
	"github.com/margo/sandbox/shared-lib/workloads"
	"github.com/margo/sandbox/standard/generatedCode/wfm/sbi"
	"github.com/margo/sandbox/standard/pkg"
	"go.uber.org/zap"
)

var cpuIndexRegex = regexp.MustCompile(`cpu(\d+)`)

type NriAnnotations struct {
	PodLevel       map[string]string
	ContainerLevel map[string]map[string]string
}

type DeploymentManagerIfc interface {
	Start()
	Stop()
}

type DeploymentManager struct {
	database       database.DatabaseIfc
	helmClient     *workloads.HelmClient
	composeClient  *workloads.DockerComposeCliClient
	pqosFactory    pqosCommandFactory
	policyReader   BalloonPolicyReader
	topologyLookup device.TopologyLookup
	log            *zap.SugaredLogger
	stopChan       chan struct{}
	//  Mutex to prevent concurrent reconciliation
	reconcileLocks sync.Map // map[deploymentId]bool
}

func NewDeploymentManager(
	db database.DatabaseIfc,
	helmClient *workloads.HelmClient,
	composeClient *workloads.DockerComposeCliClient,
	pqosFactory pqosCommandFactory,
	policyReader BalloonPolicyReader,
	topologyLookup device.TopologyLookup,
	log *zap.SugaredLogger,
) *DeploymentManager {
	return &DeploymentManager{
		database:       db,
		helmClient:     helmClient,
		composeClient:  composeClient,
		pqosFactory:    pqosFactory,
		policyReader:   policyReader,
		topologyLookup: topologyLookup,
		log:            log,
		stopChan:       make(chan struct{}),
		reconcileLocks: sync.Map{},
	}
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
	var err error

	switch profileType {
	case sbi.Helm:
	case sbi.AppDeploymentProfileType("helm.v3"):
		//  Check if Helm client is available
		if dm.helmClient == nil {
			err = fmt.Errorf(
				"helm client not initialized (device may not support Helm deployments)",
			)
		} else {
			err = dm.deployOrUpdateHelm(ctx, deploymentId, appDeployment)
		}

	case sbi.Compose:
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
				}{
					Code:    strPtr("DEPLOYMENT_ERROR"),
					Message: &errMsg,
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
	assignments := map[string][]int{}
	cacheAssignments := map[string][]database.CacheAssignment{}
	coordinator := dm.newHelmResourceCoordinator()
	configurator := NewHelmConfigurator()
	planner := NewBalloonCPUPlanner(dm.policyReader, dm.topologyLookup.IsolatedCPUIndices)
	// One snapshot per reconcile of this deployment; the ledger, not a re-read, is what
	// later components see.
	ledger := dm.newAllocationLedger(deploymentId)

	for _, component := range appDeployment.Spec.DeploymentProfile.Components {
		helmComp, err := component.AsHelmApplicationDeploymentProfileComponent()
		if err != nil {
			return fmt.Errorf("invalid helm component: %v", err)
		}
		owner := NewOwnerRef(deploymentId, helmComp.Name)
		dm.log.Infow(
			"deploying app component",
			"appId",
			deploymentId,
			"componentName",
			helmComp.Name,
		)

		// Generate release name
		releaseName := fmt.Sprintf("%s-%s", helmComp.Name, deploymentId[:8])

		values := map[string]any{}
		if appDeployment.Spec.Parameters != nil {
			componentValues, err := pkg.ConvertAllAppDeploymentParamsToValues(
				*appDeployment.Spec.Parameters,
			)
			if err != nil {
				return fmt.Errorf("failed to convert deployment profiles: %w", err)
			}
			if v, exists := componentValues[helmComp.Name]; exists {
				values = normalizeHelmValues(v)
			}
		}

		values["fullnameOverride"] = releaseName // Makes all K8s resources unique

		cpuRequirements, err := NormalizeCPURequirements(ComponentRef(helmComp.Name), helmComp.RequiredResources)
		if err != nil {
			return fmt.Errorf("invalid CPU requirements for component %s: %w", helmComp.Name, err)
		}

		cpuPlan, err := planner.PlanCPU(CPUPlanningRequest{Requirements: cpuRequirements, Ledger: ledger})
		if err != nil {
			return fmt.Errorf("failed to resolve NRI balloon annotations for component %s: %w", helmComp.Name, err)
		}

		componentAssignments := cpuPlan.AssignmentMap()

		dm.log.Infow("calling resolveComponentCacheAnnotations", "appId", deploymentId)
		cacheAnnotations, componentCacheAssignments, hasCacheAnnotations, err := dm.resolveComponentCacheAnnotations(
			ctx,
			deploymentId,
			helmComp.Name,
			helmComp.RequiredResources,
			componentAssignments,
			cacheAssignments,
		)
		if err != nil {
			return fmt.Errorf("failed to resolve cache annotations for component %s: %w", helmComp.Name, err)
		}

		dm.log.Infow("found the following cache annotations: ", "appId", deploymentId, "componentName", helmComp.Name, "cacheAnnotations", cacheAnnotations)

		for requirementName, cpus := range componentAssignments {
			copied := make([]int, len(cpus))
			copy(copied, cpus)
			assignments[requirementName] = copied
		}

		for requirementName, componentAssignmentList := range componentCacheAssignments {
			cacheAssignments[requirementName] = append(cacheAssignments[requirementName], componentAssignmentList...)
		}

		// TODO: I don't line the resourceRollback field, need to rethink it a bit more
		// I think we dont need to pass a logger to the ReesouceRollback, just printing might suffice. needs testing
		var rollback *ResourceRollback
		if len(componentAssignments) > 0 || len(componentCacheAssignments) > 0 {
			if err := dm.database.SetAllocations(deploymentId, database.Allocations{
				CPUs:   assignments,
				Caches: cacheAssignments,
			}); err != nil {
				return fmt.Errorf("failed to persist allocations for helm component %s: %w", helmComp.Name, err)
			}

			rollback = NewResourceRollback(ctx, coordinator, owner, dm.log)
			defer rollback.ReleaseOnFailure(&err)
		}

		values, err = configurator.Apply(cpuPlan, owner, values)
		if err != nil {
			return fmt.Errorf("failed to apply CPU plan to helm values for component %s: %w", helmComp.Name, err)
		}

		// Cache annotations are merged here until the cache half moves behind the configurator.
		if hasCacheAnnotations {
			values["podAnnotations"] = configurator.MergePodAnnotations(values["podAnnotations"], cacheAnnotations)
		}

		dm.log.Infow("Applied resource annotations to Helm values", "componentName", helmComp.Name,
			"releaseName", releaseName, "podAnnotations", values["podAnnotations"],
			"componentCPUSet", cpuPlan.CpuSet())

		dm.log.Infow("Deploying with unique resource names", "releaseName", releaseName, "fullnameOverride", releaseName)

		// Deploy/Update
		release, err := dm.helmClient.GetReleaseStatus(ctx, releaseName, "")
		if err != nil {
			dm.log.Infow(
				"failed to check whether a release exists or not, assuming that it doesn't exist, will proceed with installation",
				"releaseName",
				releaseName,
				"deploymentId",
				deploymentId,
				"err",
				err.Error(),
			)

		}

		if release != nil {
			// Release exists, update it
			dm.log.Infow(
				"Updating existing Helm release",
				"releaseName",
				releaseName,
				"deploymentId",
				deploymentId,
			)
			err = dm.helmClient.UpdateChart(
				ctx,
				releaseName,
				helmComp.Properties.Repository,
				"",
				values,
			)
			if err != nil {
				return fmt.Errorf("failed to upgrade existing release: %v", err)
			}
			// we had return nil here before, which made it look like
			// either we don't support multiple components per deployment
			// or we support one update/deployment component
			// had to comment it allow for core accumulation of cpu assignmetns
			if rollback != nil {
				rollback.Complete()
			}
			continue
		}

		// New deployment
		dm.log.Infow("Installing new Helm release", "releaseName", releaseName, "deploymentId", deploymentId)
		revision := "latest"
		if helmComp.Properties.Revision != nil {
			revision = *helmComp.Properties.Revision
		}
		wait := helmComp.Properties.Wait != nil && *helmComp.Properties.Wait
		err = dm.helmClient.InstallChart(
			ctx,
			releaseName,
			helmComp.Properties.Repository,
			"",
			revision,
			wait,
			values,
		)
		if err != nil {
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
	composeAssignments := map[string][]int{}
	composeCacheAssignments := map[string][]database.CacheAssignment{}
	coordinator := dm.newComposeResourceCoordinator()
	configurator := NewComposeConfigurator()
	planner := NewTopologyCPUPlanner(dm.topologyLookup.IsolatedCPUIndices)
	// One snapshot per reconcile of this deployment; the ledger, not a re-read, is what
	// later components see.
	ledger := dm.newAllocationLedger(deploymentId)

	if dm.pqosFactory == nil {
		return fmt.Errorf("pqos command factory is not initialized")
	}

	for _, component := range appDeployment.Spec.DeploymentProfile.Components {
		composeComp, err := component.AsComposeApplicationDeploymentProfileComponent()
		if err != nil {
			return fmt.Errorf("invalid compose component %v", err)
		}
		owner := NewOwnerRef(deploymentId, composeComp.Name)
		dm.log.Infow(
			"deploying app component",
			"appId",
			deploymentId,
			"componentName",
			composeComp.Name,
		)

		// Get compose content from package location
		dm.log.Infow("view of the compose component", "composecomp", pretty.Sprint(composeComp))

		// Generate project name (must be valid Docker Compose project name)
		projectName := fmt.Sprintf("%s-%s", strings.ToLower(composeComp.Name), deploymentId[:8])
		projectName = strings.ReplaceAll(projectName, "_", "-")

		values := map[string]any{}
		if appDeployment.Spec.Parameters != nil {
			componentValues, err := pkg.ConvertAllAppDeploymentParamsToValues(
				*appDeployment.Spec.Parameters,
			)
			if err != nil {
				return fmt.Errorf("failed to parse compose parameters: %w", err)
			}
			if v, exists := componentValues[composeComp.Name]; exists {
				values = v
			}
		}

		composeFilename, err := dm.composeClient.DownloadCompose(
			ctx,
			composeComp.Properties.PackageLocation,
			composeComp.Properties.KeyLocation,
			projectName,
		)
		if err != nil {
			return fmt.Errorf("failed to get compose content: %v", err)
		}
		dm.log.Debugw("preview of the compose file", "composeFilename", composeFilename)
		dm.log.Debugw("isolated cpu indices", "cpu indices", summarizeIsolatedCPUIndices(dm.topologyLookup.IsolatedCPUIndices))

		componentCPUAssignments := map[string][]int{}

		cpuRequirements, err := NormalizeCPURequirements(ComponentRef(composeComp.Name), composeComp.RequiredResources)
		if err != nil {
			return fmt.Errorf("invalid CPU requirements for component %s: %w", composeComp.Name, err)
		}

		cpuPlan, err := planner.PlanCPU(CPUPlanningRequest{Requirements: cpuRequirements, Ledger: ledger})
		if err != nil {
			return fmt.Errorf("failed to resolve compose CPU assignments for component %s: %w", composeComp.Name, err)
		}
		dm.log.Debugw("assignments for current component", "assignments", cpuPlan.Assignments)

		// Merged before the cache write below, so no persisted state shows this
		// component's ways without the cores they were planned against.
		if cpuPlan.HasCpus() {
			componentCPUAssignments = cpuPlan.AssignmentMap()
			for requirement, cpus := range componentCPUAssignments {
				copied := make([]int, len(cpus))
				copy(copied, cpus)
				composeAssignments[requirement] = copied
			}
		}
		//TODO: we should understand here if we have multiple cache assignments for the same component
		// if we do, then how do we assign the cpu-set?
		componentCacheAssignments, hasCacheAssignments, err := dm.resolveComposeComponentCacheAssignments(
			deploymentId,
			composeComp.Name,
			composeComp.RequiredResources,
			componentCPUAssignments,
			composeCacheAssignments,
		)
		if err != nil {
			return fmt.Errorf("failed to resolve cache assignments for compose component %s: %w", composeComp.Name, err)
		}

		if hasCacheAssignments {
			for requirementName, assignmentList := range componentCacheAssignments {
				composeCacheAssignments[requirementName] = append(composeCacheAssignments[requirementName], assignmentList...)
			}
		}

		var rollback *ResourceRollback
		if len(componentCPUAssignments) > 0 || len(componentCacheAssignments) > 0 {
			if err := dm.database.SetAllocations(deploymentId, database.Allocations{
				CPUs:   composeAssignments,
				Caches: composeCacheAssignments,
			}); err != nil {
				return fmt.Errorf("failed to persist compose allocations for component %s: %w", composeComp.Name, err)
			}

			rollback = NewResourceRollback(ctx, coordinator, owner, dm.log)
			defer rollback.ReleaseOnFailure(&err)

			if len(componentCacheAssignments) > 0 {
				if err := dm.applyComposeComponentPQoS(ctx, composeComp.Name, componentCacheAssignments[composeComp.Name], componentCPUAssignments, dm.pqosFactory); err != nil {
					return fmt.Errorf("failed to apply compose pqos assignment for component %s: %w", composeComp.Name, err)
				}
			}
		}

		composeFilename, cleanup, err := configurator.Apply(cpuPlan, owner, composeFilename)
		if err != nil {
			return fmt.Errorf("failed to prepare compose file for component %s: %w", composeComp.Name, err)
		}
		defer cleanup()

		// Convert parameters to environment variables
		envVars := dm.convertParametersToEnvVars(values, composeComp.Name)
		dm.log.Debugw("converted parameters to env vars", "envVars", envVars, "componentName", composeComp.Name)

		// Check if project already exists
		exists, err := dm.composeClient.ComposeExists(ctx, composeFilename, projectName)
		if err != nil {
			return fmt.Errorf("failed to check compose project existence: %v", err)
		}
		if exists {
			// Update existing deployment
			dm.log.Infow(
				"Updating existing Docker Compose project",
				"projectName",
				projectName,
				"deploymentId",
				deploymentId,
				"composeFilename",
				composeFilename,
			)
			err = dm.composeClient.UpdateCompose(ctx, projectName, composeFilename, envVars)
		} else {
			// New deployment
			dm.log.Infow(
				"Deploying new Docker Compose project",
				"projectName",
				projectName,
				"deploymentId",
				deploymentId,
				"composeFilename",
				composeFilename,
			)
			err = dm.composeClient.DeployCompose(ctx, projectName, composeFilename, envVars)
		}

		if err != nil {
			return fmt.Errorf("docker compose operation failed: %v", err)
		}

		dm.log.Infow(
			"Docker Compose deployment successful",
			"appId",
			deploymentId,
			"componentName",
			composeComp.Name,
			"projectName",
			projectName,
		)
		if rollback != nil {
			rollback.Complete()
		}
	}

	dm.log.Infow("composed finished")
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

		if err := dm.database.SetAllocations(deploymentId, database.Allocations{}); err != nil {
			dm.log.Warnw("Failed to clear allocations during removal", "deploymentId", deploymentId, "err", err)
		}

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
	case sbi.Helm:
	case sbi.AppDeploymentProfileType("helm.v3"):
		removeErr = dm.removeHelm(ctx, deploymentId, appDeployment)
	case sbi.Compose:
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
				}{
					Code:    strPtr("REMOVAL_ERROR"),
					Message: strPtr(removeErr.Error()),
				},
			})
		} else {
			dm.database.SetComponentStatus(deploymentId, name, sbi.ComponentStatus{
				Name:  name,
				State: sbi.ComponentStatusStateRemoved,
			})
		}
	}

	// Update current state to REMOVED (even if removal failed)
	removedState := currentState
	removedState.Status.Status.State = sbi.DeploymentStatusManifestStatusStateRemoved
	dm.database.SetCurrentState(deploymentId, removedState)

	if removeErr != nil {
		dm.log.Errorw("Removal failed but marking as removed",
			"deploymentId", deploymentId,
			"error", removeErr)
		dm.database.SetPhase(
			deploymentId,
			"REMOVED",
			fmt.Sprintf("Removal completed with errors: %v", removeErr),
		)
	} else {
		// TODO: check if we need to remove the clos partition + class here as well
		if err := dm.database.SetAllocations(deploymentId, database.Allocations{}); err != nil {
			dm.log.Warnw("Failed to clear allocations during removal", "deploymentId", deploymentId, "err", err)
		}
		dm.database.SetPhase(deploymentId, "REMOVED", "Removal Complete")
	}

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
		dm.log.Warnw(
			"Helm client not initialized, skipping Helm removal",
			"deploymentId",
			deploymentId,
		)
		return nil
	}

	// TODO: refactor this as we should exit here
	cacheAssignmentsByComponent := map[string][]database.CacheAssignment{}
	if record, err := dm.database.GetDeployment(deploymentId); err != nil {
		dm.log.Warnw("Failed to load deployment record for cache assignment cleanup", "deploymentId", deploymentId, "error", err)
	} else if record != nil && record.CacheAssignments != nil {
		cacheAssignmentsByComponent = record.CacheAssignments
	}

	for _, component := range appDeployment.Spec.DeploymentProfile.Components {
		helmComp, err := component.AsHelmApplicationDeploymentProfileComponent()
		if err != nil {
			dm.log.Warnw("Failed to parse helm component during removal", "error", err)
			continue // ✅ Continue to next component
		}

		releaseName := fmt.Sprintf("%s-%s", helmComp.Name, deploymentId[:8])
		dm.log.Infow("Removing Helm release",
			"releaseName", releaseName,
			"componentName", helmComp.Name,
			"deploymentId", deploymentId)

		if err := dm.helmClient.UninstallChart(ctx, releaseName, ""); err != nil {
			dm.log.Warnw("Failed to uninstall Helm chart",
				"releaseName", releaseName,
				"componentName", helmComp.Name,
				"error", err)
			// ✅ Continue removing other components
		} else {
			dm.log.Infow("Helm release removed successfully",
				"releaseName", releaseName,
				"componentName", helmComp.Name)

			dm.cleanupHelmComponentRDTOnRemoval(ctx, deploymentId, helmComp.Name, cacheAssignmentsByComponent)
		}
	}

	return nil // ✅ All components processed
}

func (dm *DeploymentManager) removeCompose(
	ctx context.Context,
	deploymentId string,
	appDeployment sbi.AppDeploymentManifest,
) error {
	// Check if Compose client is available
	if dm.composeClient == nil {
		dm.log.Warnw(
			"Docker Compose client not initialized, skipping Compose removal",
			"deploymentId",
			deploymentId,
		)
		return nil
	}

	record, err := dm.database.GetDeployment(deploymentId)
	if err != nil {
		return fmt.Errorf("failed to load deployment record for compose cache reset cleanup: %w", err)
	}
	//TODO, not sure
	if dm.pqosFactory == nil {
		return fmt.Errorf("pqos command factory is not initialized")
	}

	// Iterate through ALL components (matching deployOrUpdateCompose pattern)
	for _, component := range appDeployment.Spec.DeploymentProfile.Components {
		composeComp, err := component.AsComposeApplicationDeploymentProfileComponent()
		if err != nil {
			dm.log.Warnw("Failed to parse compose component during removal", "error", err)
			continue // Continue removing other components even if one fails to parse
		}

		// Generate project name (same logic as deployment)
		projectName := fmt.Sprintf("%s-%s", strings.ToLower(composeComp.Name), deploymentId[:8])
		projectName = strings.ReplaceAll(projectName, "_", "-")

		dm.log.Infow("Removing Docker Compose project",
			"projectName", projectName,
			"componentName", composeComp.Name,
			"deploymentId", deploymentId)

		if err := dm.composeClient.RemoveCompose(ctx, projectName); err != nil {
			dm.log.Warnw("Failed to remove Docker Compose project",
				"projectName", projectName,
				"componentName", composeComp.Name,
				"error", err)
			// Continue removing other components even if one fails
		} else {
			dm.log.Infow("Docker Compose project removed successfully",
				"projectName", projectName,
				"componentName", composeComp.Name)

			if err := dm.resetComposeComponentPQoSMask(ctx, composeComp.Name, record.CacheAssignments, record.CpuAssignments, dm.pqosFactory); err != nil {
				dm.log.Warnw("Failed to reset compose pqos mask during removal",
					"deploymentId", deploymentId,
					"componentName", composeComp.Name,
					"error", err)
			}
		}
	}

	return nil
}

// extractComponentNames returns the name of every component in an AppDeploymentManifest,
// regardless of the deployment profile type (Helm, Compose, etc.).
func (dm *DeploymentManager) extractComponentNames(
	appDeployment sbi.AppDeploymentManifest,
) []string {
	names := make([]string, 0, len(appDeployment.Spec.DeploymentProfile.Components))
	for _, comp := range appDeployment.Spec.DeploymentProfile.Components {
		if helmComp, err := comp.AsHelmApplicationDeploymentProfileComponent(); err == nil {
			names = append(names, helmComp.Name)
		} else if composeComp, err := comp.AsComposeApplicationDeploymentProfileComponent(); err == nil {
			names = append(names, composeComp.Name)
		}
	}
	return names
}

func strPtr(s string) *string {
	return &s
}

func hasCacheAssignmentForComponent(
	assignments map[string][]database.CacheAssignment,
	componentName string,
) bool {
	if len(assignments) == 0 || strings.TrimSpace(componentName) == "" {
		return false
	}

	if direct, ok := assignments[componentName]; ok && len(direct) > 0 {
		return true
	}

	for _, assignmentList := range assignments {
		for _, assignment := range assignmentList {
			if strings.TrimSpace(assignment.ComponentName) == componentName {
				return true
			}
		}
	}

	return false
}

func sanitizeFileToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	replacer := regexp.MustCompile(`[^a-zA-Z0-9_-]+`)
	cleaned := replacer.ReplaceAllString(value, "-")
	cleaned = strings.Trim(cleaned, "-")
	if cleaned == "" {
		return "unknown"
	}
	return cleaned
}

func summarizeIsolatedCPUIndices(cpuIndices []int) []int {
	if len(cpuIndices) == 0 {
		return nil
	}

	sorted := append([]int(nil), cpuIndices...)
	sort.Ints(sorted)

	return sorted
}

// newAllocationLedger takes the device-wide allocation snapshot once, before the
// component loop, and returns the ledger that answers free-versus-taken for the rest
// of this deployment's reconcile pass.
func (dm *DeploymentManager) newAllocationLedger(deploymentID string) *AllocationLedger {
	snapshot := NewAllocationSnapshot(dm.database.AllocatedCpus(), dm.topologyLookup.IsolatedCPUSet)
	return NewAllocationLedger(snapshot, deploymentID)
}

// newComposeResourceCoordinator is the Compose runtime injection point. It provides
// reservation rollback and PQoS release; topology planning will be added when the
// Compose runtime bundle is migrated.
func (dm *DeploymentManager) newComposeResourceCoordinator() *ResourceCoordinator {
	return NewResourceCoordinator(
		newDatabaseReservationStore(dm.database),
		composePQoSReleaser{dm: dm},
	)
}

// newHelmResourceCoordinator is the Kubernetes runtime injection point. It provides
// reservation rollback and RDT release; balloon planning will be added when the
// Kubernetes runtime bundle is migrated.
func (dm *DeploymentManager) newHelmResourceCoordinator() *ResourceCoordinator {
	return NewResourceCoordinator(
		newDatabaseReservationStore(dm.database),
		helmRDTReleaser{dm: dm},
	)
}

type composePQoSReleaser struct {
	dm *DeploymentManager
}

func (r composePQoSReleaser) ReleaseIsolation(ctx context.Context, reservation Reservation) error {
	if r.dm.pqosFactory == nil {
		return fmt.Errorf("pqos command factory is not initialized")
	}

	componentName := string(reservation.Owner.Component)
	return r.dm.resetComposeComponentPQoSMask(
		ctx,
		componentName,
		map[string][]database.CacheAssignment{
			componentName: toCacheAssignments(componentName, reservation.Caches),
		},
		map[string][]int{componentName: reservation.CPUs},
		r.dm.pqosFactory,
	)
}

type helmRDTReleaser struct {
	dm *DeploymentManager
}

func (r helmRDTReleaser) ReleaseIsolation(ctx context.Context, reservation Reservation) error {
	componentName := string(reservation.Owner.Component)
	r.dm.cleanupHelmComponentRDTOnRemoval(
		ctx,
		reservation.Owner.Deployment,
		componentName,
		map[string][]database.CacheAssignment{
			componentName: toCacheAssignments(componentName, reservation.Caches),
		},
	)
	return nil
}

// Helper function to convert parameters to environment variables
func (dm *DeploymentManager) convertParametersToEnvVars(
	params map[string]interface{},
	componentName string,
) map[string]string {
	envVars := make(map[string]string)

	// Convert component-specific parameters
	if componentParams, exists := params[componentName]; exists {
		if paramMap, ok := componentParams.(map[string]any); ok {
			for key, value := range paramMap {
				envVars[strings.ToUpper(key)] = formatEnvValue(value)
			}
		}
	}

	// Convert global parameters
	for key, value := range params {
		if key != componentName { // Skip component-specific params already processed
			envVars[strings.ToUpper(key)] = formatEnvValue(value)
		}
	}

	return envVars
}

func formatEnvValue(value any) string {
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
