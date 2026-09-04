package configurator

import (
	"errors"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

// marks a contract whose body lands in a later commit
var errNotImplemented = errors.New("not implemented")

// places a pod into an NRI balloon
const BalloonPodAnnotationKey = "balloon.balloons.resource-policy.nri.io/pod"

// applies a cpu plan to a chart's values map. It produces no artifact, so it has no
// cleanup counterpart to the compose path
type HelmConfigurator struct{}

func NewHelmConfigurator() *HelmConfigurator {
	return &HelmConfigurator{}
}

// merges the plan's balloon placement and cpuset into values, preserving unrelated
// user-supplied entries
func (c *HelmConfigurator) Apply(
	plan model.CpuPlan,
	owner model.OwnerRef,
	values map[string]any,
) (map[string]any, error) {
	return nil, errNotImplemented
}
