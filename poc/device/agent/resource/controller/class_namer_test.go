package controller

import (
	"testing"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

type dummyClassNamer struct{}

func (d *dummyClassNamer) Name(ref model.ComponentRef, taken []model.ClosId) (model.ClosId, error) {
	return model.ClosId("dummy"), nil
}

func TestClassNamerInterface(t *testing.T) {
	var _ ClassNamer = (*dummyClassNamer)(nil)

	namer := &dummyClassNamer{}
	name, err := namer.Name("comp-a", nil)
	if err != nil {
		t.Fatalf("unexpected Name error: %v", err)
	}
	if name != model.ClosId("dummy") {
		t.Fatalf("expected ClosId 'dummy', got %v", name)
	}
}
