package controller

import (
	"testing"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

func TestRdtClassNamerInterface(t *testing.T) {
	var _ ClassNamer = (*RdtClassNamer)(nil)

	namer := NewRdtClassNamer()
	if namer == nil {
		t.Fatal("expected non-nil RdtClassNamer")
	}
}

func TestRdtClassNamerDeterministicNaming(t *testing.T) {
	namer := NewRdtClassNamer()

	tests := []struct {
		ref   model.ComponentRef
		taken []model.ClosId
		want  model.ClosId
	}{
		{
			ref:   model.ComponentRef("cyclictest"),
			taken: nil,
			want:  model.ClosId("cyclictest_class"),
		},
		{
			ref:   model.ComponentRef("worker-node"),
			taken: []model.ClosId{"cyclictest_class"},
			want:  model.ClosId("worker-node_class"),
		},
		{
			ref:   model.ComponentRef("stressng"),
			taken: []model.ClosId{"1", "2"},
			want:  model.ClosId("stressng_class"),
		},
	}

	for _, tc := range tests {
		t.Run(string(tc.ref), func(t *testing.T) {
			got, err := namer.Name(tc.ref, tc.taken)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Name(%q) = %v, want %v", tc.ref, got, tc.want)
			}
		})
	}
}
