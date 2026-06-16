// deploy/manager.go
package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
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
	yaml "gopkg.in/yaml.v2"
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
	policyReader BalloonPolicyReader,
	topologyLookup device.TopologyLookup,
	log *zap.SugaredLogger,
) *DeploymentManager {
	return &DeploymentManager{
		database:       db,
		helmClient:     helmClient,
		composeClient:  composeClient,
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
) error {
	assignments := map[string][]int{}

	for _, component := range appDeployment.Spec.DeploymentProfile.Components {
		helmComp, err := component.AsHelmApplicationDeploymentProfileComponent()
		if err != nil {
			return fmt.Errorf("invalid helm component: %v", err)
		}
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
				values = v
			}
		}

		values["fullnameOverride"] = releaseName // Makes all K8s resources unique

		nriAnnotations, hasNriAnnotations, err := dm.resolveComponentBalloonAnnotations(
			helmComp.Name,
			appDeployment.Spec.DeploymentProfile.RequiredResources,
		)
		if err != nil {
			return fmt.Errorf("failed to resolve NRI balloon annotations for component %s: %w", helmComp.Name, err)
		}

		componentAssignments, err := dm.resolveComponentIsolatedCpuAssignments(
			deploymentId,
			helmComp.Name,
			appDeployment.Spec.DeploymentProfile.RequiredResources,
		)
		if err != nil {
			return fmt.Errorf("failed to resolve isolated CPU assignments for component %s: %w", helmComp.Name, err)
		}
		for requirementName, cpus := range componentAssignments {
			copied := make([]int, len(cpus))
			copy(copied, cpus)
			assignments[requirementName] = copied
		}

		if hasNriAnnotations {
			overrideFile, err := dm.generateNriValuesOverrideFile(deploymentId, helmComp.Name, nriAnnotations)
			if err != nil {
				return fmt.Errorf("failed to generate NRI values override file for component %s: %w", helmComp.Name, err)
			}
			defer func() {
				if removeErr := os.Remove(overrideFile); removeErr != nil {
					dm.log.Warnw("Failed to remove temporary NRI values override file", "path", overrideFile, "err", removeErr)
				}
			}()

			dm.logNriAnnotationPlan(helmComp.Name, releaseName, nriAnnotations)
			flatAnnotations := dm.flattenNriAnnotations(nriAnnotations)
			podAnnotationsValues := dm.mergePodAnnotations(values["podAnnotations"], flatAnnotations)
			values["podAnnotations"] = podAnnotationsValues
			dm.log.Infow(
				"Applied NRI balloon annotations to Helm values",
				"componentName", helmComp.Name,
				"releaseName", releaseName,
				"overrideFile", overrideFile,
				"podAnnotations", podAnnotationsValues,
			)
		} else {
			dm.log.Infow(
				"No NRI balloon annotations resolved for component",
				"componentName", helmComp.Name,
				"releaseName", releaseName,
			)
		}

		dm.log.Infow("Deploying with unique resource names",
			"releaseName", releaseName,
			"fullnameOverride", releaseName)

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
			continue
		}

		// New deployment
		dm.log.Infow(
			"Installing new Helm release",
			"releaseName",
			releaseName,
			"deploymentId",
			deploymentId,
		)
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
		dm.log.Infow(
			"Helm deployment successful",
			"appId",
			deploymentId,
			"releaseName",
			releaseName,
		)
	}

	if err := dm.database.SetCpuAssignments(deploymentId, assignments); err != nil {
		return fmt.Errorf("failed to persist cpu assignments for helm deployment: %w", err)
	}

	return nil
}

func (dm *DeploymentManager) deployOrUpdateCompose(
	ctx context.Context,
	deploymentId string,
	appDeployment sbi.AppDeploymentManifest,
) error {
	composeAssignments := map[string][]int{}

	for _, component := range appDeployment.Spec.DeploymentProfile.Components {
		composeComp, err := component.AsComposeApplicationDeploymentProfileComponent()
		if err != nil {
			return fmt.Errorf("invalid compose component %v", err)
		}
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

		values := map[string]interface{}{}
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
		dm.log.Debugw("cpu indices", "cpu indices", summarizeTopologyCPUIndices(dm.topologyLookup.CPUIndices))

		if len(dm.topologyLookup.CPUIndices) > 0 {
			dm.log.Debugw("looking for cpu indices", "cpu indices", summarizeTopologyCPUIndices(dm.topologyLookup.CPUIndices))
			assignments, err := dm.resolveComponentCpuAssignments(deploymentId, composeComp.Name, composeFilename,
				appDeployment.Spec.DeploymentProfile.RequiredResources,
			)

			dm.log.Debugw("assignments for current component", "assignments", assignments)

			if err != nil {
				return fmt.Errorf("failed to resolve compose CPU assignments for component %s: %w", composeComp.Name, err)
			}

			if len(assignments) > 0 {
				pinnedFile, err := os.CreateTemp(
					"",
					fmt.Sprintf(
						"compose-pinned-%s-%s-*.yaml",
						sanitizeFileToken(composeComp.Name),
						sanitizeFileToken(deploymentId),
					),
				)
				if err != nil {
					return fmt.Errorf("failed to create temporary pinned compose file for component %s: %w", composeComp.Name, err)
				}
				pinnedPath := filepath.Clean(pinnedFile.Name())
				if closeErr := pinnedFile.Close(); closeErr != nil {
					return fmt.Errorf("failed to close temporary pinned compose file for component %s: %w", composeComp.Name, closeErr)
				}
				defer func(path string) {
					if removeErr := os.Remove(path); removeErr != nil {
						dm.log.Warnw("Failed to remove temporary pinned compose file", "path", path, "err", removeErr)
					}
				}(pinnedPath)
				if err := rewriteComposeFile(composeFilename, pinnedPath, assignments); err != nil {
					return fmt.Errorf("compose yaml rewrite failed for component %s: %w", composeComp.Name, err)
				}

				for requirement, cpus := range toAssignmentMap(assignments) {
					copied := make([]int, len(cpus))
					copy(copied, cpus)
					composeAssignments[requirement] = copied
				}

				// Persist before compose up/update so failed deploy attempts remain deterministic.
				dm.log.Debugw("persist before update")
				if err := dm.database.SetCpuAssignments(deploymentId, composeAssignments); err != nil {
					return fmt.Errorf("failed to persist compose cpu assignments: %w", err)
				}

				composeFilename = pinnedPath
			}
		}

		// Convert parameters to environment variables
		envVars := dm.convertParametersToEnvVars(values, composeComp.Name)

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
	}

	if len(dm.topologyLookup.CPUIndices) > 0 {
		dm.log.Infow("**about to set cpu assignement", "cpu indices", summarizeTopologyCPUIndices(dm.topologyLookup.CPUIndices))
		if err := dm.database.SetCpuAssignments(deploymentId, composeAssignments); err != nil {
			return fmt.Errorf("failed to persist compose cpu assignments: %w", err)
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

		if err := dm.database.ClearCpuAssignments(deploymentId); err != nil {
			dm.log.Warnw("Failed to clear CPU assignments during removal", "deploymentId", deploymentId, "err", err)
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
		if err := dm.database.ClearCpuAssignments(deploymentId); err != nil {
			dm.log.Warnw("Failed to clear CPU assignments during removal", "deploymentId", deploymentId, "err", err)
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

func (dm *DeploymentManager) resolveComponentBalloonAnnotations(
	componentName string,
	requiredResources *sbi.RequiredResources,
) (NriAnnotations, bool, error) {
	empty := NriAnnotations{PodLevel: map[string]string{}, ContainerLevel: map[string]map[string]string{}}

	componentCPUReqs := filterCPURequirementsForComponent(requiredResources, componentName)
	dm.log.Debugw(
		"Resolved component CPU requirements from deployment profile for NRI balloon resolution",
		"componentName", componentName,
		"hasRequiredResources", requiredResources != nil,
		"cpuRequirementCount", len(componentCPUReqs),
		"cpuRequirements", summarizeCpuRequirements(componentCPUReqs),
	)

	if len(componentCPUReqs) == 0 {
		dm.log.Infow(
			"Skipping NRI balloon resolution: component has no matching deployment-profile CPU requirements",
			"componentName", componentName,
		)
		return empty, false, nil
	}

	if dm.policyReader == nil {
		dm.log.Debugw("Skipping NRI balloon resolution: policy reader not configured", "componentName", componentName)
		return empty, false, nil
	}

	policy := dm.policyReader.Parsed()
	if policy == nil {
		dm.log.Infow("Skipping NRI balloon resolution: no BalloonsPolicy snapshot available", "componentName", componentName)
		return empty, false, nil
	}

	dm.log.Debugw(
		"Attempting NRI balloon resolution",
		"componentName", componentName,
		"policyName", policy.Name,
		"balloonTypeCount", len(policy.BalloonTypes),
		"cpuRequirementCount", len(componentCPUReqs),
		"cpuRequirements", summarizeCpuRequirements(componentCPUReqs),
	)

	annotations, err := resolveNriAnnotations(componentCPUReqs, policy)
	if err != nil {
		dm.log.Warnw(
			"NRI balloon resolution failed",
			"componentName", componentName,
			"policyName", policy.Name,
			"cpuRequirements", summarizeCpuRequirements(componentCPUReqs),
			"err", err,
		)
		return empty, false, err
	}

	has := len(annotations.PodLevel) > 0 || len(annotations.ContainerLevel) > 0
	if has {
		dm.log.Infow(
			"NRI balloon resolution succeeded",
			"componentName", componentName,
			"podAnnotationCount", len(annotations.PodLevel),
			"containerAnnotationCount", len(annotations.ContainerLevel),
		)
	} else {
		dm.log.Infow(
			"NRI balloon resolution produced no annotations",
			"componentName", componentName,
			"cpuRequirements", summarizeCpuRequirements(componentCPUReqs),
		)
	}
	return annotations, has, nil
}

func summarizeCpuRequirements(reqs []sbi.Cpu) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(reqs))
	for _, req := range reqs {
		entry := map[string]interface{}{}
		if req.Name != nil {
			entry["containerName"] = strings.TrimSpace(*req.Name)
		} else {
			entry["containerName"] = ""
		}
		if req.Class != nil {
			entry["class"] = string(*req.Class)
		}
		if req.Type != nil {
			entry["type"] = string(*req.Type)
		}
		if req.Cores != nil {
			entry["cores"] = *req.Cores
		}
		out = append(out, entry)
	}
	return out
}

func filterCPURequirementsForComponent(
	requiredResources *sbi.RequiredResources,
	componentName string,
) []sbi.Cpu {
	if requiredResources == nil || requiredResources.Cpu == nil || len(*requiredResources.Cpu) == 0 {
		return nil
	}

	matching := make([]sbi.Cpu, 0)
	for _, req := range *requiredResources.Cpu {
		if req.Name == nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(*req.Name), componentName) {
			matching = append(matching, req)
		}
	}

	return matching
}

func filterIsolatedCPURequirements(reqs []sbi.Cpu) []sbi.Cpu {
	if len(reqs) == 0 {
		return nil
	}

	isolationOnly := make([]sbi.Cpu, 0, len(reqs))
	for _, req := range reqs {
		if req.Type == nil || *req.Type != sbi.CpuTypeIsolated {
			continue
		}
		isolationOnly = append(isolationOnly, req)
	}

	return isolationOnly
}

func (dm *DeploymentManager) resolveComponentIsolatedCpuAssignments(
	deploymentId string,
	componentName string,
	requiredResources *sbi.RequiredResources,
) (map[string][]int, error) {
	assignments := map[string][]int{}

	componentCPUReqs := filterCPURequirementsForComponent(requiredResources, componentName)
	isolationOnly := filterIsolatedCPURequirements(componentCPUReqs)
	if len(isolationOnly) == 0 {
		return assignments, nil
	}

	if dm.policyReader == nil {
		dm.log.Debugw("Skipping isolated CPU assignment resolution: policy reader not configured",
			"componentName", componentName)
		return assignments, nil
	}

	// we can parse the policy only once (before dm.resolveComponentBalloonAnnotaton ) and just use it
	// in both dm.resolveComponentBalloonAnnotaton and   dm.resolveComponentIsolatedCpuAssignments
	policy := dm.policyReader.Parsed()
	if policy == nil {
		dm.log.Debugw("Skipping isolated CPU assignment resolution: no BalloonsPolicy snapshot available",
			"componentName", componentName)
		return assignments, nil
	}

	for _, req := range isolationOnly {
		if req.Name == nil {
			continue
		}

		requirementName := strings.TrimSpace(*req.Name)
		if requirementName == "" {
			continue
		}

		_, cpus, err := selectBalloonForGroupWithCPURefs(req, policy)
		if err != nil {
			return assignments, fmt.Errorf("isolated CPU assignment resolution failed for requirement %q: %w",
				requirementName, err)
		}

		assignments[requirementName] = cpus
	}

	if err := dm.validateSelectedIsolatedAssignments(deploymentId, isolationOnly, assignments); err != nil {
		return nil, err
	}

	return assignments, nil
}

func (dm *DeploymentManager) validateSelectedIsolatedAssignments(
	deploymentId string,
	isolationOnly []sbi.Cpu,
	assignments map[string][]int,
) error {
	// Validate only the selected cores for this component and reject overlaps.
	cpuIndexToCoreKey := map[int]database.CoreKey{}
	for _, req := range isolationOnly {
		if req.Name == nil || req.Class == nil || req.Type == nil {
			continue
		}
		requirementName := strings.TrimSpace(*req.Name)
		if requirementName == "" {
			continue
		}
		selected, ok := assignments[requirementName]
		if !ok {
			continue
		}
		coreKey := database.CoreKey{Class: string(*req.Class), Type: string(*req.Type)}
		for _, idx := range selected {
			cpuIndexToCoreKey[idx] = coreKey
		}
	}

	alreadyAllocated := dm.database.AllocatedCpus(cpuIndexToCoreKey)
	for _, req := range isolationOnly {
		if req.Name == nil || req.Class == nil || req.Type == nil {
			continue
		}

		requirementName := strings.TrimSpace(*req.Name)
		if requirementName == "" {
			continue
		}

		selected, ok := assignments[requirementName]
		if !ok {
			continue
		}

		coreKey := database.CoreKey{Class: string(*req.Class), Type: string(*req.Type)}
		takenIndices := alreadyAllocated[coreKey]
		for _, idx := range selected {
			owner, taken := takenIndices[idx]
			if !taken {
				continue
			}
			if owner == deploymentId || strings.HasPrefix(owner, deploymentId+"/") {
				continue
			}
			return fmt.Errorf(
				"insufficient isolated CPUs: cpu index %d required by %q is already allocated to deployment %q",
				idx, requirementName, owner,
			)
		}
	}

	return nil
}

func resolveNriAnnotations(cpuReqs []sbi.Cpu, policy *ParsedBalloonPolicy) (NriAnnotations, error) {
	out := NriAnnotations{
		PodLevel:       map[string]string{},
		ContainerLevel: map[string]map[string]string{},
	}
	if policy == nil {
		return out, nil
	}

	grouped := map[string][]sbi.Cpu{}
	for _, req := range cpuReqs {
		containerName := ""
		if req.Name != nil {
			containerName = strings.TrimSpace(*req.Name)
		}
		grouped[containerName] = append(grouped[containerName], req)
	}

	for containerName, group := range grouped {
		if len(group) == 0 {
			continue
		}

		balloonName, err := selectBalloonForGroup(group[0], policy)
		if err != nil {
			scope := "pod"
			if containerName != "" {
				scope = "container " + containerName
			}
			return out, fmt.Errorf("balloon resolution failed for %s: %w", scope, err)
		}

		key := "balloon.balloons.resource-policy.nri.io/pod"
		if containerName != "" {
			key = fmt.Sprintf("balloon.balloons.resource-policy.nri.io/container.%s", containerName)
		}

		if containerName == "" {
			out.PodLevel[key] = balloonName
			continue
		}

		if out.ContainerLevel[containerName] == nil {
			out.ContainerLevel[containerName] = map[string]string{}
		}
		out.ContainerLevel[containerName][key] = balloonName
	}

	return out, nil
}

func selectBalloonForGroup(req sbi.Cpu, policy *ParsedBalloonPolicy) (string, error) {
	balloonName, _, err := selectBalloonForGroupWithCPURefs(req, policy)
	if err != nil {
		return "", err
	}

	return balloonName, nil
}

func selectBalloonForGroupWithCPURefs(req sbi.Cpu, policy *ParsedBalloonPolicy) (string, []int, error) {
	if policy == nil {
		return "", nil, fmt.Errorf("policy is not available")
	}
	if len(policy.BalloonTypes) == 0 {
		return "", nil, fmt.Errorf("policy contains no balloon types")
	}

	requestedName := ""
	if req.Name != nil {
		requestedName = strings.TrimSpace(*req.Name)
	}
	if requestedName == "" {
		return "", nil, fmt.Errorf("cpu.name is required for balloon selection")
	}

	expectedBalloonName := ""
	if requestedName != "" {
		expectedBalloonName = fmt.Sprintf("balloon_%s", requestedName)
	}

	requiredCores := int64(1)
	if req.Cores != nil && *req.Cores > 0 {
		requiredCores = int64(math.Ceil(float64(*req.Cores)))
	}

	for _, balloon := range policy.BalloonTypes {
		if !strings.EqualFold(strings.TrimSpace(balloon.Name), expectedBalloonName) {
			continue
		}

		matchedCPURefs := uniqueSortedCPURefs(balloon.PreferCloseToDevices)
		matchingCount := int64(len(matchedCPURefs))
		if matchingCount < requiredCores {
			return "", nil, fmt.Errorf(
				"balloon %q matched cpu.name=%q but does not cover required cores=%d (matchingCPURefs=%d)",
				balloon.Name,
				requestedName,
				requiredCores,
				matchingCount,
			)
		}

		selected := append([]int(nil), matchedCPURefs[:requiredCores]...)
		return balloon.Name, selected, nil
	}

	return "", nil, fmt.Errorf(
		"no balloon found matching cpu.name=%q (expected balloon=%q)",
		requestedName,
		expectedBalloonName,
	)
}

func countUniqueCPURefs(paths []string) int64 {
	return int64(len(uniqueSortedCPURefs(paths)))
}

func uniqueSortedCPURefs(paths []string) []int {
	if len(paths) == 0 {
		return nil
	}

	seen := map[int]struct{}{}
	for _, path := range paths {
		matches := cpuIndexRegex.FindAllStringSubmatch(path, -1)
		for _, m := range matches {
			if len(m) != 2 {
				continue
			}
			idx, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			seen[idx] = struct{}{}
		}
	}

	refs := make([]int, 0, len(seen))
	for idx := range seen {
		refs = append(refs, idx)
	}
	sort.Ints(refs)

	return refs
}

func (dm *DeploymentManager) flattenNriAnnotations(annotations NriAnnotations) map[string]string {
	out := map[string]string{}
	for k, v := range annotations.PodLevel {
		out[k] = v
	}
	for _, perContainer := range annotations.ContainerLevel {
		for k, v := range perContainer {
			out[k] = v
		}
	}
	return out
}

func (dm *DeploymentManager) mergePodAnnotations(
	existing interface{},
	annotations map[string]string,
) map[string]interface{} {
	merged := map[string]interface{}{}

	switch typed := existing.(type) {
	case nil:
		// No existing pod annotations to merge.
	case map[string]string:
		for k, v := range typed {
			merged[k] = v
		}
	case map[string]interface{}:
		for k, v := range typed {
			merged[k] = fmt.Sprintf("%v", v)
		}
	case map[interface{}]interface{}:
		for k, v := range typed {
			key := fmt.Sprintf("%v", k)
			merged[key] = fmt.Sprintf("%v", v)
		}
	default:
		dm.log.Warnw(
			"Existing podAnnotations value has unsupported type; replacing with resolved NRI annotations",
			"type", fmt.Sprintf("%T", existing),
		)
	}

	for k, v := range annotations {
		merged[k] = v
	}

	return merged
}

func (dm *DeploymentManager) generateNriValuesOverrideFile(
	deploymentID string,
	componentName string,
	annotations NriAnnotations,
) (string, error) {
	flat := dm.flattenNriAnnotations(annotations)
	if len(flat) == 0 {
		return "", fmt.Errorf("no annotations to write")
	}

	payload := map[string]interface{}{
		"podAnnotations": flat,
	}

	bytes, err := yaml.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal NRI override yaml: %w", err)
	}

	pattern := fmt.Sprintf("nri-override-%s-%s-*.yaml", sanitizeFileToken(componentName), sanitizeFileToken(deploymentID))
	file, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", fmt.Errorf("failed to create temporary NRI override file: %w", err)
	}
	defer file.Close()

	if _, err := file.Write(bytes); err != nil {
		return "", fmt.Errorf("failed to write NRI override yaml: %w", err)
	}

	return filepath.Clean(file.Name()), nil
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

func (dm *DeploymentManager) logNriAnnotationPlan(
	componentName string,
	releaseName string,
	annotations NriAnnotations,
) {
	podKeys := sortedAnnotationPairs(annotations.PodLevel)
	dm.log.Infow("Resolved pod-level NRI annotations",
		"componentName", componentName,
		"releaseName", releaseName,
		"annotations", podKeys,
	)

	containerNames := make([]string, 0, len(annotations.ContainerLevel))
	for name := range annotations.ContainerLevel {
		containerNames = append(containerNames, name)
	}
	sort.Strings(containerNames)

	for _, containerName := range containerNames {
		dm.log.Infow("Resolved container-level NRI annotations",
			"componentName", componentName,
			"releaseName", releaseName,
			"containerName", containerName,
			"annotations", sortedAnnotationPairs(annotations.ContainerLevel[containerName]),
		)
	}
}

func sortedAnnotationPairs(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, fmt.Sprintf("%s=%s", k, m[k]))
	}
	return pairs
}

func summarizeTopologyCPUIndices(cpuIndices map[database.CoreKey][]int) []string {
	if len(cpuIndices) == 0 {
		return nil
	}

	summary := make([]string, 0, len(cpuIndices))
	for key, cpus := range cpuIndices {
		sorted := append([]int(nil), cpus...)
		sort.Ints(sorted)
		summary = append(summary, fmt.Sprintf("%s/%s=%v", key.Class, key.Type, sorted))
	}
	sort.Strings(summary)

	return summary
}

// Helper function to convert parameters to environment variables
func (dm *DeploymentManager) convertParametersToEnvVars(
	params map[string]interface{},
	componentName string,
) map[string]string {
	envVars := make(map[string]string)

	// Convert component-specific parameters
	if componentParams, exists := params[componentName]; exists {
		if paramMap, ok := componentParams.(map[string]interface{}); ok {
			for key, value := range paramMap {
				envVars[strings.ToUpper(key)] = fmt.Sprintf("%v", value)
			}
		}
	}

	// Convert global parameters
	for key, value := range params {
		if key != componentName { // Skip component-specific params already processed
			envVars[strings.ToUpper(key)] = fmt.Sprintf("%v", value)
		}
	}

	return envVars
}
