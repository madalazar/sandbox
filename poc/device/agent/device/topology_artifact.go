package device

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type TopologyLookup struct {
	IsolatedCPUIndices []int
	IsolatedCPUSet     map[int]struct{}
	L3Caches           []TopologyCacheInfo
	PQoSInterface      string
	// MaxClos is the most classes of service the device can hold. Zero means the
	// artifact predates the field or resctrl exposed none.
	MaxClos int
}

type TopologyCoreInfo struct {
	ID    int    `json:"id"`
	Class string `json:"class"`
	Type  string `json:"type"`
}

type TopologyCacheInfo struct {
	Level     string `json:"level"`
	ID        string `json:"id"`
	SizeKB    int64  `json:"size_kb"`
	Ways      int64  `json:"ways"`
	WaySizeKB int64  `json:"way_size_kb"`
	Cores     string `json:"cores"`
}

type topologyArtifact struct {
	SchemaVersion string              `json:"schemaVersion"`
	GeneratedAt   string              `json:"generatedAt"`
	PQoSInterface string              `json:"pqos_interface"`
	MaxClos       int                 `json:"max_closids"`
	Cores         []TopologyCoreInfo  `json:"cores"`
	Caches        []TopologyCacheInfo `json:"caches"`
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

func ReadCacheInfoFromAgentArtifact(path string) ([]TopologyCacheInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read topology artifact %s: %w", path, err)
	}

	var artifact topologyArtifact
	if err := json.Unmarshal(data, &artifact); err != nil {
		return nil, fmt.Errorf("parse topology artifact %s: %w", path, err)
	}

	if len(artifact.Caches) == 0 {
		return nil, nil
	}

	caches := make([]TopologyCacheInfo, 0, len(artifact.Caches))
	for _, cache := range artifact.Caches {
		cache.Level = strings.TrimSpace(cache.Level)
		cache.ID = strings.TrimSpace(cache.ID)
		cache.Cores = strings.TrimSpace(cache.Cores)
		if !strings.EqualFold(cache.Level, "L3") {
			continue
		}
		if cache.ID == "" || cache.SizeKB <= 0 || cache.Ways <= 0 || cache.WaySizeKB <= 0 {
			continue
		}
		caches = append(caches, cache)
	}

	sort.Slice(caches, func(i, j int) bool {
		leftID, leftErr := strconv.Atoi(caches[i].ID)
		rightID, rightErr := strconv.Atoi(caches[j].ID)
		if leftErr == nil && rightErr == nil {
			return leftID < rightID
		}
		return caches[i].ID < caches[j].ID
	})

	return caches, nil
}

func ReadPQoSInterfaceFromAgentArtifact(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read topology artifact %s: %w", path, err)
	}

	var artifact topologyArtifact
	if err := json.Unmarshal(data, &artifact); err != nil {
		return "", fmt.Errorf("parse topology artifact %s: %w", path, err)
	}

	iface := strings.ToLower(strings.TrimSpace(artifact.PQoSInterface))
	switch iface {
	case "os", "msr":
		return iface, nil
	default:
		return "", fmt.Errorf("parse topology artifact %s: invalid pqos_interface %q (expected os or msr)", path, artifact.PQoSInterface)
	}
}

// ReadMaxClosFromAgentArtifact returns the most classes of service the device can hold.
// A missing field yields 0, which callers must treat as "cache allocation is unavailable"
// rather than substituting a guessed ceiling.
func ReadMaxClosFromAgentArtifact(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read topology artifact %s: %w", path, err)
	}

	var artifact topologyArtifact
	if err := json.Unmarshal(data, &artifact); err != nil {
		return 0, fmt.Errorf("parse topology artifact %s: %w", path, err)
	}

	if artifact.MaxClos < 0 {
		return 0, fmt.Errorf("parse topology artifact %s: invalid max_closids %d", path, artifact.MaxClos)
	}

	return artifact.MaxClos, nil
}

func LoadCPUIndicesFromTopologyArtifact(path string) (TopologyLookup, error) {
	cores, err := ReadCoreInfoFromAgentArtifact(path)
	if err != nil {
		return TopologyLookup{}, err
	}
	caches, err := ReadCacheInfoFromAgentArtifact(path)
	if err != nil {
		return TopologyLookup{}, err
	}

	lookup := CPUIndicesFromCoreInfo(cores)
	lookup.L3Caches = caches
	pqosInterface, err := ReadPQoSInterfaceFromAgentArtifact(path)
	if err != nil {
		return TopologyLookup{}, err
	}
	lookup.PQoSInterface = pqosInterface

	maxClos, err := ReadMaxClosFromAgentArtifact(path)
	if err != nil {
		return TopologyLookup{}, err
	}
	lookup.MaxClos = maxClos

	return lookup, nil
}

func ParseCPUCoreRangeList(raw string) ([]int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	parts := strings.Split(raw, ",")
	seen := map[int]struct{}{}
	out := make([]int, 0)

	for _, part := range parts {
		token := strings.TrimSpace(part)
		if token == "" {
			continue
		}

		if strings.Contains(token, "-") {
			rangeParts := strings.Split(token, "-")
			if len(rangeParts) != 2 {
				return nil, fmt.Errorf("invalid CPU core range token %q", token)
			}

			start, err := strconv.Atoi(strings.TrimSpace(rangeParts[0]))
			if err != nil {
				return nil, fmt.Errorf("invalid CPU core range start %q: %w", token, err)
			}
			end, err := strconv.Atoi(strings.TrimSpace(rangeParts[1]))
			if err != nil {
				return nil, fmt.Errorf("invalid CPU core range end %q: %w", token, err)
			}
			if start < 0 || end < 0 || start > end {
				return nil, fmt.Errorf("invalid CPU core range %q", token)
			}

			for idx := start; idx <= end; idx++ {
				if _, exists := seen[idx]; exists {
					continue
				}
				seen[idx] = struct{}{}
				out = append(out, idx)
			}
			continue
		}

		idx, err := strconv.Atoi(token)
		if err != nil {
			return nil, fmt.Errorf("invalid CPU core token %q: %w", token, err)
		}
		if idx < 0 {
			return nil, fmt.Errorf("invalid negative CPU core index %d", idx)
		}
		if _, exists := seen[idx]; exists {
			continue
		}
		seen[idx] = struct{}{}
		out = append(out, idx)
	}

	sort.Ints(out)
	return out, nil
}
