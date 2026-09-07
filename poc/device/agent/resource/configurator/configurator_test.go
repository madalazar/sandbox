package configurator

import (
	"errors"
	"testing"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

// the skeletons are wired but not yet implemented; the bodies land in a later commit
func TestHelmConfiguratorIsNotYetImplemented(t *testing.T) {
	if _, err := NewHelmConfigurator().Apply(model.CpuPlan{}, model.OwnerRef{}, nil); !errors.Is(err, errNotImplemented) {
		t.Fatalf("Apply() error = %v, want %v", err, errNotImplemented)
	}
}
