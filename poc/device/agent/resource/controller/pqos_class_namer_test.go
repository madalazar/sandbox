package controller

import (
	"testing"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

func TestPqosClassNamerInterface(t *testing.T) {
	var _ ClassNamer = (*PqosClassNamer)(nil)

	namer := NewPqosClassNamer()
	if namer == nil {
		t.Fatal("expected non-nil PqosClassNamer")
	}
}

func TestPqosClassNamerSelection(t *testing.T) {
	namer := NewPqosClassNamer()

	tests := []struct {
		name  string
		taken []model.ClosId
		want  model.ClosId
	}{
		{
			name:  "empty taken returns 1",
			taken: nil,
			want:  "1",
		},
		{
			name:  "skips COS 0",
			taken: []model.ClosId{"0"},
			want:  "1",
		},
		{
			name:  "1 is taken returns 2",
			taken: []model.ClosId{"1"},
			want:  "2",
		},
		{
			name:  "1 and 2 taken returns 3",
			taken: []model.ClosId{"1", "2"},
			want:  "3",
		},
		{
			name:  "finds lowest gap",
			taken: []model.ClosId{"1", "3"},
			want:  "2",
		},
		{
			name:  "ignores non-numeric strings",
			taken: []model.ClosId{"rdt_class", "1"},
			want:  "2",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := namer.Name("comp-1", tc.taken)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Name() = %v, want %v", got, tc.want)
			}
		})
	}
}
