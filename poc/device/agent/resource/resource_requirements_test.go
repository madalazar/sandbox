package resource

import (
	"testing"

	"github.com/margo/sandbox/standard/generatedCode/wfm/sbi"
)

func TestNormalizeCPURequirements(t *testing.T) {
	tests := []struct {
		name         string
		required     *sbi.RequiredResources
		wantShared   []CPURequirement
		wantIsolated []CPURequirement
	}{
		{name: "no required resources"},
		{
			name:         "fractional isolated cores round up",
			required:     cpuRequirements(cpuRequirement(sbi.CpuTypeIsolated, 0.5)),
			wantIsolated: []CPURequirement{{Mode: CPUModeIsolated, Cores: 1}},
		},
		{
			name:       "absent cores means one shared core",
			required:   cpuRequirements(sbi.Cpu{}),
			wantShared: []CPURequirement{{Mode: CPUModeShared, Cores: 1}},
		},
		{
			name: "shared cores are classified separately",
			required: cpuRequirements(
				cpuRequirement(sbi.CpuTypeShared, 2.1),
				cpuRequirement(sbi.CpuTypeShared, 1),
			),
			wantShared: []CPURequirement{
				{Mode: CPUModeShared, Cores: 3},
				{Mode: CPUModeShared, Cores: 1},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized, err := NormalizeCPURequirements("component-a", test.required)
			if err != nil {
				t.Fatalf("NormalizeCPURequirements() error = %v", err)
			}
			if len(normalized.Shared) != len(test.wantShared) {
				t.Fatalf("shared = %#v, want %#v", normalized.Shared, test.wantShared)
			}
			for i, want := range test.wantShared {
				if normalized.Shared[i] != want {
					t.Errorf("shared[%d] = %#v, want %#v", i, normalized.Shared[i], want)
				}
			}
			if len(normalized.Isolated) != len(test.wantIsolated) {
				t.Fatalf("isolated = %#v, want %#v", normalized.Isolated, test.wantIsolated)
			}
			for i, want := range test.wantIsolated {
				if normalized.Isolated[i] != want {
					t.Errorf("isolated[%d] = %#v, want %#v", i, normalized.Isolated[i], want)
				}
			}
		})
	}
}

func TestNormalizeCPURequirementsRejectsSecondIsolatedRequirement(t *testing.T) {
	required := cpuRequirements(
		cpuRequirement(sbi.CpuTypeIsolated, 1),
		cpuRequirement(sbi.CpuTypeIsolated, 1),
	)

	if _, err := NormalizeCPURequirements("component-a", required); err == nil {
		t.Fatal("NormalizeCPURequirements() = nil error, want a rejection")
	}
}

// One unit has one cpuset, so a component cannot ask for both modes at once.
func TestNormalizeCPURequirementsRejectsMixedModes(t *testing.T) {
	required := cpuRequirements(
		cpuRequirement(sbi.CpuTypeShared, 2),
		cpuRequirement(sbi.CpuTypeIsolated, 1),
	)

	if _, err := NormalizeCPURequirements("component-a", required); err == nil {
		t.Fatal("NormalizeCPURequirements() = nil error, want a rejection")
	}
}

func cpuRequirement(cpuType sbi.CpuType, cores float32) sbi.Cpu {
	return sbi.Cpu{Type: &cpuType, Cores: &cores}
}

func cpuRequirements(reqs ...sbi.Cpu) *sbi.RequiredResources {
	return &sbi.RequiredResources{Cpu: &reqs}
}
