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

func TestDirectRunnerSkeleton(t *testing.T) {
	var _ CommandRunner = (*directRunner)(nil)

	runner := NewDirectRunner()
	if runner == nil {
		t.Fatal("expected non-nil directRunner")
	}

	_, err := runner.Run(context.Background(), "echo", "test")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestNsenterRunnerSkeleton(t *testing.T) {
	var _ CommandRunner = (*nsenterRunner)(nil)

	runner := NewNsenterRunner()
	if runner == nil {
		t.Fatal("expected non-nil nsenterRunner")
	}

	_, err := runner.Run(context.Background(), "echo", "test")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
