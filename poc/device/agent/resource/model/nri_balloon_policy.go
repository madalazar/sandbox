package model

import "k8s.io/apimachinery/pkg/runtime/schema"

const (
	// Resource and namespace defaults
	DefaultBalloonsPolicyNamespace = "kube-system"
	DefaultBalloonsPolicyName      = "default"
	BalloonsPolicyGroup            = "config.nri"
	BalloonsPolicyVersion          = "v1alpha1"
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

var BalloonsPolicyGVR = schema.GroupVersionResource{
	Group:    BalloonsPolicyGroup,
	Version:  BalloonsPolicyVersion,
	Resource: BalloonsPolicyResource,
}

// non-blocking reads of the latest parsed policy snapshot
type BalloonPolicyReader interface {
	Parsed() *ParsedBalloonPolicy
}

// the balloon fields the cpu planner needs
type ParsedBalloonType struct {
	Name                 string
	PreferCoreType       string
	PreferIsolCpus       *bool
	MinCpus              *int64
	MaxCpus              *int64
	PreferCloseToDevices []string
}

// the in-memory snapshot the balloon cpu planner reads
type ParsedBalloonPolicy struct {
	Name         string
	Namespace    string
	BalloonTypes []ParsedBalloonType
	RdtConfig    RdtConfig
}

// represents the partitions and classes parsed from a balloon policy
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
