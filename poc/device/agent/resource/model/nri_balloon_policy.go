package model

// To regenerate zz_generated.deepcopy.go after modifying the types below, run:
//   go generate ./resource/model/...
// from poc/device/agent (or `go generate` in this directory).
//go:generate go run k8s.io/code-generator/cmd/deepcopy-gen@v0.35.1 --output-file zz_generated.deepcopy.go --bounding-dirs github.com/margo/sandbox/poc/device/agent/resource/model .

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
}
