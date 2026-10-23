package configurator

import (
	"errors"
	"testing"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

// the skeletons are wired but not yet implemented; the bodies land in a later commit
func TestConfiguratorsAreNotYetImplemented(t *testing.T) {
	t.Run("compose", func(t *testing.T) {
		if _, err := NewComposeConfigurator().Apply(model.CpuPlan{}, model.OwnerRef{}, ""); !errors.Is(err, errNotImplemented) {
			t.Fatalf("Apply() error = %v, want %v", err, errNotImplemented)
		}
	})

	t.Run("helm", func(t *testing.T) {
		if _, err := NewHelmConfigurator().Apply(model.CpuPlan{}, model.OwnerRef{}, nil); !errors.Is(err, errNotImplemented) {
			t.Fatalf("Apply() error = %v, want %v", err, errNotImplemented)
		}
	})
}
