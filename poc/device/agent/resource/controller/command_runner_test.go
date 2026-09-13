package controller

import (
	"context"
	"strings"
	"testing"
	"time"
)

type dummyCommandRunner struct{}

func (d *dummyCommandRunner) Run(ctx context.Context, command string, args ...string) ([]byte, error) {
	return []byte("ok"), nil
}

func TestCommandRunnerInterface(t *testing.T) {
	var _ CommandRunner = (*dummyCommandRunner)(nil)

	runner := &dummyCommandRunner{}
	ctx := context.Background()

	out, err := runner.Run(ctx, "echo", "hello")
	if err != nil {
		t.Fatalf("unexpected Run error: %v", err)
	}
	if string(out) != "ok" {
		t.Fatalf("expected 'ok', got %q", string(out))
	}
}

func TestDirectRunnerExecution(t *testing.T) {
	var _ CommandRunner = (*directRunner)(nil)

	runner := NewDirectRunner()
	if runner == nil {
		t.Fatal("expected non-nil directRunner")
	}

	out, err := runner.Run(context.Background(), "echo", "hello-world")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(string(out)) != "hello-world" {
		t.Fatalf("expected 'hello-world', got %q", string(out))
	}
}

func TestDirectRunnerTimeout(t *testing.T) {
	runner := NewDirectRunner()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := runner.Run(ctx, "sleep", "2")
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestDirectRunnerCommandError(t *testing.T) {
	runner := NewDirectRunner()
	_, err := runner.Run(context.Background(), "non-existent-command-xyz")
	if err == nil {
		t.Fatal("expected error for nonexistent command, got nil")
	}
}

func TestNsenterRunnerConstructors(t *testing.T) {
	var _ CommandRunner = (*nsenterRunner)(nil)

	runner := NewNsenterRunner()
	if runner == nil || runner.targetPID != "1" {
		t.Fatalf("expected targetPID '1', got %+v", runner)
	}

	runner2 := NewNsenterRunnerWithPid("1234")
	if runner2 == nil || runner2.targetPID != "1234" {
		t.Fatalf("expected targetPID '1234', got %+v", runner2)
	}

	runner3 := NewNsenterRunnerWithPid("")
	if runner3 == nil || runner3.targetPID != "1" {
		t.Fatalf("expected default targetPID '1', got %+v", runner3)
	}
}
