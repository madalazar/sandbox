package controller

import (
	"context"
	"testing"
)

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
