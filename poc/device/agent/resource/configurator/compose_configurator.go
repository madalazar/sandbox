package configurator

import (
	"errors"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

// marks a contract whose body lands in a later commit
var errNotImplemented = errors.New("not implemented")

// applies a cpu plan to a compose package by rewriting the downloaded file into a
// temporary copy it owns
type ComposeConfigurator struct{}

func NewComposeConfigurator() *ComposeConfigurator {
	return &ComposeConfigurator{}
}

// writes the plan's cpuset into the source file's single service and returns the path of
// the prepared copy. A plan with no cpus returns sourcePath unchanged, so the caller
// must not assume it owns the returned file
func (c *ComposeConfigurator) Apply(
	plan model.CpuPlan,
	owner model.OwnerRef,
	sourcePath string,
) (preparedPath string, err error) {
	return "", errNotImplemented
}
