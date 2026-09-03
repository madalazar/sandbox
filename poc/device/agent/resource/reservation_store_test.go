package resource

import (
	"errors"
	"testing"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

// the skeletons are wired but not yet implemented; the bodies land in a later commit
func TestDatabaseReservationStoreIsNotYetImplemented(t *testing.T) {
	store := NewDatabaseReservationStore(nil, nil)

	if _, err := store.Snapshot(); !errors.Is(err, errNotImplemented) {
		t.Fatalf("Snapshot() error = %v, want %v", err, errNotImplemented)
	}
	if _, _, err := store.LoadReservation(model.OwnerRef{}); !errors.Is(err, errNotImplemented) {
		t.Fatalf("LoadReservation() error = %v, want %v", err, errNotImplemented)
	}
	if err := store.SaveAllocations("deployment-1", nil); !errors.Is(err, errNotImplemented) {
		t.Fatalf("SaveAllocations() error = %v, want %v", err, errNotImplemented)
	}
	if err := store.ClearComponent(model.OwnerRef{}); !errors.Is(err, errNotImplemented) {
		t.Fatalf("ClearComponent() error = %v, want %v", err, errNotImplemented)
	}
}
