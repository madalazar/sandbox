package controller

import (
	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

var _ ClassNamer = (*RdtClassNamer)(nil)

// names classes of service for rdt policy using a deterministic naming convention
type RdtClassNamer struct{}

func NewRdtClassNamer() *RdtClassNamer {
	return &RdtClassNamer{}
}

// generates a deterministic class name for the owner reference
func (n *RdtClassNamer) Name(owner model.OwnerRef, taken []model.ClosId) (model.ClosId, error) {
	return model.ClosId(rdtPartitionName(owner) + "_class"), nil
}

func rdtPartitionName(owner model.OwnerRef) string {
	comp := string(owner.Component)
	dep := owner.Deployment
	if dep == "" {
		return comp
	}
	if len(dep) > 8 {
		dep = dep[:8]
	}
	return comp + "-" + dep
}
