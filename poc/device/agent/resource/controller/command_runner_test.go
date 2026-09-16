package controller

import (
	"context"
	"testing"
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

func TestNsenterRunnerConstructors(t *testing.T) {
	var _ CommandRunner = (*nsenterRunner)(nil)

	runner := NewNsenterRunner()
	nr, ok := runner.(*nsenterRunner)
	if !ok || nr.targetPID != "1" {
		t.Fatalf("expected targetPID '1', got %+v", runner)
	}

	runner2 := NewNsenterRunnerWithPid("1234")
	nr2, ok := runner2.(*nsenterRunner)
	if !ok || nr2.targetPID != "1234" {
		t.Fatalf("expected targetPID '1234', got %+v", runner2)
	}

	runner3 := NewNsenterRunnerWithPid("")
	nr3, ok := runner3.(*nsenterRunner)
	if !ok || nr3.targetPID != "1" {
		t.Fatalf("expected default targetPID '1', got %+v", runner3)
	}
}
