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
