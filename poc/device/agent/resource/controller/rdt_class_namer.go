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

// generates a deterministic class name for the component reference
func (n *RdtClassNamer) Name(ref model.ComponentRef, taken []model.ClosId) (model.ClosId, error) {
	return model.ClosId(string(ref) + "_class"), nil
}
