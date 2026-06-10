package device

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/margo/sandbox/poc/device/agent/database"
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
	deviceCapabilities *sbi.DeviceCapabilitiesManifest
	cpuIndexToCoreKey map[int]database.CoreKey
	log               *zap.SugaredLogger
	stopChan          chan struct{}
}

func NewCapacityPublisher(
	auth CapabilitiesReporter,
	deviceID string,
	db database.DatabaseIfc,
	deviceCapabilities *sbi.DeviceCapabilitiesManifest,
	cpuIndexToCoreKey map[int]database.CoreKey,
	log *zap.SugaredLogger,
) (*CapacityPublisher, error) {
	if cpuIndexToCoreKey == nil {
		if log != nil {
			log.Errorw("topology cpu index map is nil; capacity publisher cannot start")
		}
		return nil, fmt.Errorf("topology cpu index map is nil")
	}

	return &CapacityPublisher{
		auth:              auth,
		deviceID:          strings.TrimSpace(deviceID),
		database:          db,
		deviceCapabilities: deviceCapabilities,
		cpuIndexToCoreKey: cpuIndexToCoreKey,
		log:               log,
		stopChan:          make(chan struct{}),
	}, nil
}

func (cp *CapacityPublisher) Start() {
	cp.database.Subscribe(cp.onDeploymentChange)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := cp.PublishNow(ctx); err != nil {
		cp.log.Errorw("failed to publish initial capabilities", "err", err.Error())
	}
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
	if cp.deviceCapabilities == nil {
		return fmt.Errorf("device capabilities are not loaded")
	}
	if cp.auth == nil {
		return fmt.Errorf("auth settings are not configured")
	}
	if cp.deviceID == "" {
		return fmt.Errorf("device id is not configured")
	}

	manifest, err := cloneCapabilities(cp.deviceCapabilities)
	if err != nil {
		return fmt.Errorf("clone device capabilities: %w", err)
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