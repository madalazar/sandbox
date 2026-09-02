package resource

// BalloonPolicyReader exposes non-blocking reads of the latest parsed policy snapshot.
type BalloonPolicyReader interface {
	Parsed() *ParsedBalloonPolicy
}

// ParsedBalloonType contains the balloon fields needed by later scheduling logic.
type ParsedBalloonType struct {
	Name                 string
	PreferCoreType       string
	PreferIsolCpus       *bool
	MinCPUs              *int64
	MaxCPUs              *int64
	PreferCloseToDevices []string
}

// ParsedBalloonPolicy is the in-memory snapshot used by the deployment reconciler.
type ParsedBalloonPolicy struct {
	Name         string
	Namespace    string
	BalloonTypes []ParsedBalloonType
	RDT          ParsedRDTPolicy
}

type ParsedRDTPolicy struct {
	Partitions map[string]struct{}
	Classes    map[string]struct{}
}

func (p *ParsedBalloonPolicy) HasRDTPartition(name string) bool {
	if p == nil {
		return false
	}
	_, ok := p.RDT.Partitions[name]
	return ok
}

func (p *ParsedBalloonPolicy) HasRDTClass(name string) bool {
	if p == nil {
		return false
	}
	_, ok := p.RDT.Classes[name]
	return ok
}
