package controller

import (
	"context"
	"testing"
)

type mockRunner struct {
	lastCmd  string
	lastArgs []string
	output   []byte
	err      error
}

func (m *mockRunner) Run(ctx context.Context, command string, args ...string) ([]byte, error) {
	m.lastCmd = command
	m.lastArgs = args
	return m.output, m.err
}

func TestNewPqosCommandFactory(t *testing.T) {
	tests := []struct {
		name      string
		rawIface  string
		wantIface string
		wantErr   bool
	}{
		{name: "default empty", rawIface: "", wantIface: "os"},
		{name: "os interface", rawIface: "os", wantIface: "os"},
		{name: "msr interface", rawIface: "msr", wantIface: "msr"},
		{name: "case insensitive", rawIface: " MSR ", wantIface: "msr"},
		{name: "invalid interface", rawIface: "invalid", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			factory, err := NewPqosCommandFactory(tc.rawIface)
			if (err != nil) != tc.wantErr {
				t.Fatalf("NewPqosCommandFactory(%q) error = %v, wantErr %v", tc.rawIface, err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if factory.PqosInterface() != tc.wantIface {
				t.Fatalf("expected iface %q, got %q", tc.wantIface, factory.PqosInterface())
			}
		})
	}
}

func TestBuildApplyArgs(t *testing.T) {
	factory, err := NewPqosCommandFactory("os")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	args := factory.BuildApplyArgs("0", "1", "0x3", "2,3")
	expected := []string{"--iface=os", "-e", "llc@0:1=0x3", "-a", "core:1=2,3"}
	if len(args) != len(expected) {
		t.Fatalf("expected args %+v, got %+v", expected, args)
	}
	for i := range args {
		if args[i] != expected[i] {
			t.Fatalf("args[%d] = %q, want %q", i, args[i], expected[i])
		}
	}
}

func TestBuildResetArgs(t *testing.T) {
	factory, err := NewPqosCommandFactory("msr")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	t.Run("with core assignment", func(t *testing.T) {
		args := factory.BuildResetArgs("0", "1", "0xfff", "2,3")
		expected := []string{"--iface=msr", "-e", "llc@0:1=0xfff", "-a", "core:0=2,3"}
		if len(args) != len(expected) {
			t.Fatalf("expected args %+v, got %+v", expected, args)
		}
		for i := range args {
			if args[i] != expected[i] {
				t.Fatalf("args[%d] = %q, want %q", i, args[i], expected[i])
			}
		}
	})

	t.Run("without core assignment", func(t *testing.T) {
		args := factory.BuildResetArgs("0", "1", "0xfff", "")
		expected := []string{"--iface=msr", "-e", "llc@0:1=0xfff"}
		if len(args) != len(expected) {
			t.Fatalf("expected args %+v, got %+v", expected, args)
		}
		for i := range args {
			if args[i] != expected[i] {
				t.Fatalf("args[%d] = %q, want %q", i, args[i], expected[i])
			}
		}
	})
}

func TestPqosCommandWithMockRunner(t *testing.T) {
	runner := &mockRunner{output: []byte("success")}
	factory, err := NewPqosCommandFactory("os")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	applyArgs := factory.BuildApplyArgs("0", "2", "0xF0", "4-7")
	out, err := runner.Run(context.Background(), "pqos", applyArgs...)
	if err != nil {
		t.Fatalf("unexpected runner error: %v", err)
	}
	if string(out) != "success" {
		t.Fatalf("expected output 'success', got %q", string(out))
	}
	if runner.lastCmd != "pqos" || len(runner.lastArgs) != len(applyArgs) {
		t.Fatalf("unexpected runner command: %s %v", runner.lastCmd, runner.lastArgs)
	}
}
