package controller

import (
	"errors"
	"testing"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

func TestPqosClassNamerInterface(t *testing.T) {
	var _ ClassNamer = (*PqosClassNamer)(nil)

	namer := NewPqosClassNamer()
	if namer == nil {
		t.Fatal("expected non-nil PqosClassNamer")
	}

	_, err := namer.Name(model.ComponentRef("comp-1"), nil)
	if !errors.Is(err, errNotImplemented) {
		t.Fatalf("Name() error = %v, want %v", err, errNotImplemented)
	}
}
