package controller

import (
	"errors"
	"fmt"
	"strings"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

var _ ClassNamer = (*RdtClassNamer)(nil)

const rdtClassSuffix = "_class"

// names classes of service for rdt policy using a deterministic naming convention
type RdtClassNamer struct{}

func NewRdtClassNamer() *RdtClassNamer {
	return &RdtClassNamer{}
}

// generates a deterministic class name for the owner reference
func (n *RdtClassNamer) Name(owner model.OwnerRef, taken []model.ClosId) (model.ClosId, error) {
	comp := strings.TrimSpace(string(owner.Component))
	if comp == "" {
		return model.ClassUnset, errors.New("component name cannot be empty")
	}

	dep := strings.TrimSpace(owner.Deployment)
	if dep == "" {
		return model.ClassUnset, fmt.Errorf("deployment id cannot be empty for component %q", comp)
	}

	if len(dep) > 8 {
		dep = dep[:8]
	}

	return model.ClosId(comp + "_" + dep + rdtClassSuffix), nil
}
