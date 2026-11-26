package model

// To regenerate zz_generated.deepcopy.go after modifying the types below, run:
//
//	go generate ./resource/model/...
//
// from poc/device/agent (or `go generate` in this directory).
//
//go:generate go run k8s.io/code-generator/cmd/deepcopy-gen@v0.35.1 --output-file zz_generated.deepcopy.go --bounding-dirs github.com/margo/sandbox/poc/device/agent/resource/model .
const (
	// Resource and namespace defaults
	DefaultBalloonsPolicyNamespace = "kube-system"
	DefaultBalloonsPolicyName      = "default"
	BalloonsPolicyResource         = "balloonspolicies"

	// BalloonsPolicy schema keys
	PolicyKeySpec         = "spec"
	PolicyKeyConfig       = "config"
	PolicyKeyBalloonTypes = "balloonTypes"
	PolicyKeyControl      = "control"

	// BalloonType schema keys
	BalloonKeyName                 = "name"
	BalloonKeyPreferCoreType       = "preferCoreType"
	BalloonKeyPreferIsolCpus       = "preferIsolCpus"
	BalloonKeyMinCPUs              = "minCPUs"
	BalloonKeyMaxCPUs              = "maxCPUs"
	BalloonKeyPreferCloseToDevices = "preferCloseToDevices"

	// RDT control schema keys and values
	RdtKeyControl      = "rdt"
	RdtKeyPartitions   = "partitions"
	RdtKeyClasses      = "classes"
	RdtKeyL3Allocation = "l3Allocation"
	RdtKeyUnified      = "unified"
	RdtFillerCacheMask = "0x1"
	RdtFullAllocation  = "100%"
)

// non-blocking reads of the latest parsed policy snapshot
type BalloonPolicyReader interface {
	Parsed() *ParsedBalloonPolicy
}

// the balloon fields the cpu planner needs
// +k8s:deepcopy-gen=true
type ParsedBalloonType struct {
	Name                 string   `json:"name"`
	PreferCoreType       string   `json:"preferCoreType"`
	PreferIsolCpus       *bool    `json:"preferIsolCpus"`
	MinCpus              *int64   `json:"minCPUs"`
	MaxCpus              *int64   `json:"maxCPUs"`
	PreferCloseToDevices []string `json:"preferCloseToDevices"`
}

// the in-memory snapshot the balloon cpu planner reads
// +k8s:deepcopy-gen=true
type ParsedBalloonPolicy struct {
	Name         string
	Namespace    string
	BalloonTypes []ParsedBalloonType
	RdtConfig    RdtConfig
}

// represents the partitions and classes parsed from a balloon policy
// +k8s:deepcopy-gen=true
type RdtConfig struct {
	Partitions map[string]struct{}
	Classes    map[string]struct{}
}

// reports whether the policy contains the specified partition
func (p *ParsedBalloonPolicy) HasPartition(name string) bool {
	if p == nil || p.RdtConfig.Partitions == nil {
		return false
	}
	_, ok := p.RdtConfig.Partitions[name]
	return ok
}

// reports whether the policy contains the specified class
func (p *ParsedBalloonPolicy) HasClass(name string) bool {
	if p == nil || p.RdtConfig.Classes == nil {
		return false
	}
	_, ok := p.RdtConfig.Classes[name]
	return ok
}
