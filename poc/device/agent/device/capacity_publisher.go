package device

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/margo/sandbox/poc/device/agent/database"
	"github.com/margo/sandbox/poc/device/agent/types"
	"github.com/margo/sandbox/standard/generatedCode/wfm/sbi"
	"go.uber.org/zap"
)

type CapacityPublisherIfc interface {
	Start()
	Stop()
	PublishNow(ctx context.Context) error
}

type CapabilitiesReporter interface {
	ReportCapabilities(ctx context.Context, capabilities sbi.DeviceCapabilitiesManifest) error
}

type CapacityPublisher struct {
	auth              CapabilitiesReporter
	deviceID          string
	database          database.DatabaseIfc
	capabilitiesPath  string
	baseCapabilities  *sbi.DeviceCapabilitiesManifest
	cpuIndexToCoreKey map[int]database.CoreKey
	log               *zap.SugaredLogger
	stopChan          chan struct{}
}

type topologyCoreKey struct {
	Class string
	Type  string
}

type topologyCoreInfo struct {
	ID    int    `json:"id"`
	Class string `json:"class"`
	Type  string `json:"type"`
}

type topologyArtifact struct {
	SchemaVersion string             `json:"schemaVersion"`
	GeneratedAt   string             `json:"generatedAt"`
	Cores         []topologyCoreInfo `json:"cores"`
}

func NewCapacityPublisher(
	auth CapabilitiesReporter,
	deviceID string,
	db database.DatabaseIfc,
	capabilitiesPath string,
	log *zap.SugaredLogger,
) *CapacityPublisher {
	return &CapacityPublisher{
		auth:              auth,
		deviceID:          strings.TrimSpace(deviceID),
		database:          db,
		capabilitiesPath:  capabilitiesPath,
		cpuIndexToCoreKey: make(map[int]database.CoreKey),
		log:               log,
		stopChan:          make(chan struct{}),
	}
}

func (cp *CapacityPublisher) Start() {
	cp.loadCapabilitiesAndTopology()
	cp.database.Subscribe(cp.onDeploymentChange)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := cp.PublishNow(ctx); err != nil {
		cp.log.Errorw("failed to publish initial capabilities", "err", err.Error())
	}
}

func (cp *CapacityPublisher) loadCapabilitiesAndTopology() {
	baseCapabilities, capabilityErr := types.LoadCapabilities(cp.capabilitiesPath)
	if capabilityErr != nil {
		cp.log.Errorw(
			"failed to load capabilities file; capacity publish is disabled until restart",
			"err",
			capabilityErr.Error(),
		)
	} else {
		cp.baseCapabilities = baseCapabilities
	}

	cpuIndexToCoreKey := make(map[int]database.CoreKey)
	topologyArtifactPath := strings.TrimSpace(os.Getenv("MARGO_CPU_TOPOLOGY_ARTIFACT"))
	if topologyArtifactPath == "" {
		topologyArtifactPath = filepath.Clean("./config/cpu-topology-agent.json")
	}
	coreInfo, topologyErr := readCoreInfoFromAgentArtifact(topologyArtifactPath)
	if topologyErr != nil {
		cp.log.Warnw(
			"failed to read CPU topology artifact; continuing without indexed CPU lookup",
			"path", topologyArtifactPath,
			"err", topologyErr,
		)
	} else {
		for index, key := range indexToKey(coreInfo) {
			cpuIndexToCoreKey[index] = database.CoreKey{Class: key.Class, Type: key.Type}
		}
		cp.log.Infow("Loaded CPU topology artifact", "path", topologyArtifactPath, "coreCount", len(coreInfo))
	}

	cp.cpuIndexToCoreKey = cpuIndexToCoreKey
}

func (cp *CapacityPublisher) Stop() {
	select {
	case <-cp.stopChan:
		return
	default:
		close(cp.stopChan)
	}
}

func (cp *CapacityPublisher) PublishNow(ctx context.Context) error {
	if cp.baseCapabilities == nil {
		return fmt.Errorf("base capabilities are not loaded")
	}
	if cp.auth == nil {
		return fmt.Errorf("auth settings are not configured")
	}
	if cp.deviceID == "" {
		return fmt.Errorf("device id is not configured")
	}

	manifest, err := cloneCapabilities(cp.baseCapabilities)
	if err != nil {
		return fmt.Errorf("clone base capabilities: %w", err)
	}

	allocated := cp.database.AllocatedCpus(cp.cpuIndexToCoreKey)
	for idx := range manifest.Properties.Resources.Cpu {
		cpu := &manifest.Properties.Resources.Cpu[idx]
		if cpu.Type == nil || cpu.Class == nil {
			continue
		}
		if string(*cpu.Type) != string(sbi.CpuTypeIsolated) {
			continue
		}

		key := database.CoreKey{Class: string(*cpu.Class), Type: string(*cpu.Type)}
		used := float32(len(allocated[key]))
		remaining := cpu.Cores - used
		if remaining < 0 {
			remaining = 0
		}
		cpu.Cores = remaining
	}

	manifest.Properties.Id = cp.deviceID
	if err := cp.auth.ReportCapabilities(ctx, *manifest); err != nil {
		return fmt.Errorf("report capabilities: %w", err)
	}

	return nil
}

func (cp *CapacityPublisher) onDeploymentChange(
	deploymentId string,
	record *database.DeploymentRecord,
	changeType database.DeploymentRecordChangeType,
) {
	if changeType != database.DeploymentChangeTypeCpuAssignmentsChanged {
		return
	}

	select {
	case <-cp.stopChan:
		return
	default:
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := cp.PublishNow(ctx); err != nil {
		cp.log.Warnw("failed to republish capabilities after CPU assignment update", "deploymentId", deploymentId, "err", err)
	}
}

func cloneCapabilities(base *sbi.DeviceCapabilitiesManifest) (*sbi.DeviceCapabilitiesManifest, error) {
	data, err := json.Marshal(base)
	if err != nil {
		return nil, err
	}

	var cloned sbi.DeviceCapabilitiesManifest
	if err := json.Unmarshal(data, &cloned); err != nil {
		return nil, err
	}

	return &cloned, nil
}

func readCoreInfoFromAgentArtifact(path string) ([]topologyCoreInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read topology artifact %s: %w", path, err)
	}

	var artifact topologyArtifact
	if err := json.Unmarshal(data, &artifact); err != nil {
		return nil, fmt.Errorf("parse topology artifact %s: %w", path, err)
	}

	if len(artifact.Cores) == 0 {
		return nil, nil
	}

	cores := make([]topologyCoreInfo, 0, len(artifact.Cores))
	for _, core := range artifact.Cores {
		core.Class = strings.TrimSpace(core.Class)
		core.Type = strings.TrimSpace(core.Type)
		if core.Class == "" || core.Type == "" {
			continue
		}
		cores = append(cores, core)
	}

	sort.Slice(cores, func(i, j int) bool {
		return cores[i].ID < cores[j].ID
	})

	return cores, nil
}

func indexToKey(cores []topologyCoreInfo) map[int]topologyCoreKey {
	result := make(map[int]topologyCoreKey, len(cores))
	for _, core := range cores {
		result[core.ID] = topologyCoreKey{Class: core.Class, Type: core.Type}
	}
	return result
}