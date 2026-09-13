package controller

import (
	"errors"
	"testing"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

func TestRdtClassNamerInterface(t *testing.T) {
	var _ ClassNamer = (*RdtClassNamer)(nil)

	namer := NewRdtClassNamer()
	if namer == nil {
		t.Fatal("expected non-nil RdtClassNamer")
	}

	_, err := namer.Name(model.ComponentRef("comp-1"), nil)
	if !errors.Is(err, errNotImplemented) {
		t.Fatalf("Name() error = %v, want %v", err, errNotImplemented)
	}
}
