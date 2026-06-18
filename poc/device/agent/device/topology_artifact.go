package device

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type TopologyLookup struct {
	IsolatedCPUIndices []int
	IsolatedCPUSet     map[int]struct{}
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
		if core.ID < 0 {
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
	isolatedSet := make(map[int]struct{})
	isolatedIndices := make([]int, 0, len(cores))

	for _, core := range cores {
		coreType := strings.ToLower(strings.TrimSpace(core.Type))
		if coreType != "" && coreType != "isolated" {
			continue
		}
		if _, exists := isolatedSet[core.ID]; exists {
			continue
		}
		isolatedSet[core.ID] = struct{}{}
		isolatedIndices = append(isolatedIndices, core.ID)
	}

	sort.Ints(isolatedIndices)

	return TopologyLookup{
		IsolatedCPUIndices: isolatedIndices,
		IsolatedCPUSet:     isolatedSet,
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
