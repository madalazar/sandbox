package model

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
}
