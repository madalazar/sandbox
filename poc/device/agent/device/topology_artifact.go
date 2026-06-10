package device

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/margo/sandbox/poc/device/agent/database"
)

type TopologyLookup struct {
	CPUIndices       map[database.CoreKey][]int
	CPUIndexToCoreKey map[int]database.CoreKey
}

type TopologyCoreInfo struct {
	ID    int    `json:"id"`
	Class string `json:"class"`
	Type  string `json:"type"`
}

type topologyArtifact struct {
	SchemaVersion string             `json:"schemaVersion"`
	GeneratedAt   string             `json:"generatedAt"`
	Cores         []TopologyCoreInfo `json:"cores"`
}

func ResolveTopologyArtifactPath() string {
	return filepath.Clean("./config/cpu-topology-agent.json")
}

func ReadCoreInfoFromAgentArtifact(path string) ([]TopologyCoreInfo, error) {
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

	cores := make([]TopologyCoreInfo, 0, len(artifact.Cores))
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

func CPUIndicesFromCoreInfo(cores []TopologyCoreInfo) TopologyLookup {
	cpuIndices := make(map[database.CoreKey][]int)
	cpuIndexToCoreKey := make(map[int]database.CoreKey)

	for _, core := range cores {
		key := database.CoreKey{Class: core.Class, Type: core.Type}
		cpuIndices[key] = append(cpuIndices[key], core.ID)
		cpuIndexToCoreKey[core.ID] = key
	}

	for key := range cpuIndices {
		sort.Ints(cpuIndices[key])
	}

	return TopologyLookup{
		CPUIndices:       cpuIndices,
		CPUIndexToCoreKey: cpuIndexToCoreKey,
	}
}

func LoadCPUIndicesFromTopologyArtifact(path string) (TopologyLookup, error) {
	cores, err := ReadCoreInfoFromAgentArtifact(path)
	if err != nil {
		return TopologyLookup{}, err
	}

	lookup := CPUIndicesFromCoreInfo(cores)
	return lookup, nil
}
