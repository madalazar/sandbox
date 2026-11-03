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
		name  string
		owner model.OwnerRef
		taken []model.ClosId
		want  model.ClosId
	}{
		{
			name:  "short deployment id",
			owner: model.NewOwnerRef("dep-1", "cyclictest"),
			taken: nil,
			want:  model.ClosId("cyclictest-dep-1_class"),
		},
		{
			name:  "long deployment id truncated to 8 chars",
			owner: model.NewOwnerRef("1234567890abcdef", "worker-node"),
			taken: []model.ClosId{"cyclictest-dep-1_class"},
			want:  model.ClosId("worker-node-12345678_class"),
		},
		{
			name:  "empty deployment id falls back to component name",
			owner: model.OwnerRef{Component: "stressng"},
			taken: []model.ClosId{"1", "2"},
			want:  model.ClosId("stressng_class"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := namer.Name(tc.owner, tc.taken)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Name(%+v) = %v, want %v", tc.owner, got, tc.want)
			}
		})
	}
}
