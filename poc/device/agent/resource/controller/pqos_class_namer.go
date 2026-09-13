package controller

import (
	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

var _ ClassNamer = (*PqosClassNamer)(nil)

// names classes of service for pqos by selecting the lowest free integer cos
type PqosClassNamer struct{}

func NewPqosClassNamer() *PqosClassNamer {
	return &PqosClassNamer{}
}

// selects the lowest free class of service index as decimal string
func (n *PqosClassNamer) Name(ref model.ComponentRef, taken []model.ClosId) (model.ClosId, error) {
	return model.ClassUnset, errNotImplemented
}
