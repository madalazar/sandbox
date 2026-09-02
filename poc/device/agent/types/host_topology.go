package types

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const hostTopologyArtifactPath = "./config/host-topology.json"

type HostTopology struct {
	IsolatedCpuIndices []int
	IsolatedCpuSet     map[int]struct{}
	L3Caches           []HostTopologyCache
	PqoSInterface      string
	// MaxClos is the most classes of service the device can hold. Zero means the
	// artifact predates the field or resctrl exposed none.
	MaxClos int
}

type HostTopologyCpu struct {
	Id    int    `json:"id"`
	Class string `json:"class"`
	Type  string `json:"type"`
}

type HostTopologyCache struct {
	Level     string `json:"level"`
	Id        string `json:"id"`
	SizeKB    int64  `json:"size_kb"`
	Ways      int64  `json:"ways"`
	WaySizeKB int64  `json:"way_size_kb"`
	Cores     string `json:"cores"`
}

type topologyArtifact struct {
	SchemaVersion string              `json:"schemaVersion"`
	GeneratedAt   string              `json:"generatedAt"`
	PQoSInterface string              `json:"pqos_interface"`
	MaxClos       int                 `json:"max_clos"`
	Cores         []HostTopologyCpu   `json:"cores"`
	Caches        []HostTopologyCache `json:"caches"`
}

func ResolveHostTopologyArtifactPath() string {
	return filepath.Clean(hostTopologyArtifactPath)
}

func readCpuInfo(path string) ([]HostTopologyCpu, error) {
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

	cores := make([]HostTopologyCpu, 0, len(artifact.Cores))
	for _, core := range artifact.Cores {
		core.Class = strings.TrimSpace(core.Class)
		core.Type = strings.TrimSpace(core.Type)
		if core.Id < 0 {
			continue
		}
		cores = append(cores, core)
	}

	sort.Slice(cores, func(i, j int) bool {
		return cores[i].Id < cores[j].Id
	})

	return cores, nil
}

func collectIsolatedCpus(cores []HostTopologyCpu) ([]int, map[int]struct{}) {
	isolatedSet := make(map[int]struct{})
	isolatedIndices := make([]int, 0, len(cores))

	for _, core := range cores {
		coreType := strings.ToLower(strings.TrimSpace(core.Type))
		if coreType != "" && coreType != "isolated" {
			continue
		}
		if _, exists := isolatedSet[core.Id]; exists {
			continue
		}
		isolatedSet[core.Id] = struct{}{}
		isolatedIndices = append(isolatedIndices, core.Id)
	}

	sort.Ints(isolatedIndices)

	return isolatedIndices, isolatedSet
}

func readCacheInfoFromArtifact(path string) ([]HostTopologyCache, error) {
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

	caches := make([]HostTopologyCache, 0, len(artifact.Caches))
	for _, cache := range artifact.Caches {
		cache.Level = strings.TrimSpace(cache.Level)
		cache.Id = strings.TrimSpace(cache.Id)
		cache.Cores = strings.TrimSpace(cache.Cores)
		if !strings.EqualFold(cache.Level, "L3") {
			continue
		}
		if cache.Id == "" || cache.SizeKB <= 0 || cache.Ways <= 0 || cache.WaySizeKB <= 0 {
			continue
		}
		caches = append(caches, cache)
	}

	sort.Slice(caches, func(i, j int) bool {
		leftID, leftErr := strconv.Atoi(caches[i].Id)
		rightID, rightErr := strconv.Atoi(caches[j].Id)
		if leftErr == nil && rightErr == nil {
			return leftID < rightID
		}
		return caches[i].Id < caches[j].Id
	})

	return caches, nil
}

func readPqoSInterfaceFromArtifact(path string) (string, error) {
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
func readMaxClosFromArtifact(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read topology artifact %s: %w", path, err)
	}

	var artifact topologyArtifact
	if err := json.Unmarshal(data, &artifact); err != nil {
		return 0, fmt.Errorf("parse topology artifact %s: %w", path, err)
	}

	if artifact.MaxClos < 0 {
		return 0, fmt.Errorf("parse topology artifact %s: invalid max_clos %d", path, artifact.MaxClos)
	}

	return artifact.MaxClos, nil
}

func LoadHostTopology(path string) (HostTopology, error) {
	cores, err := readCpuInfo(path)
	if err != nil {
		return HostTopology{}, err
	}
	caches, err := readCacheInfoFromArtifact(path)
	if err != nil {
		return HostTopology{}, err
	}

	isolatedIndices, isolatedSet := collectIsolatedCpus(cores)
	lookup := HostTopology{
		IsolatedCpuIndices: isolatedIndices,
		IsolatedCpuSet:     isolatedSet,
	}
	lookup.L3Caches = caches
	pqosInterface, err := readPqoSInterfaceFromArtifact(path)
	if err != nil {
		return HostTopology{}, err
	}
	lookup.PqoSInterface = pqosInterface

	maxClos, err := readMaxClosFromArtifact(path)
	if err != nil {
		return HostTopology{}, err
	}
	lookup.MaxClos = maxClos

	return lookup, nil
}
