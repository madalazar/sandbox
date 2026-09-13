package controller

import (
	"context"
)

// executes commands on the host or inside a namespace
type CommandRunner interface {
	Run(ctx context.Context, command string, args ...string) ([]byte, error)
}

var _ CommandRunner = (*directRunner)(nil)
var _ CommandRunner = (*nsenterRunner)(nil)

// executes commands directly on the host
type directRunner struct{}

func NewDirectRunner() *directRunner {
	return &directRunner{}
}

func (r *directRunner) Run(ctx context.Context, command string, args ...string) ([]byte, error) {
	return nil, errNotImplemented
}

// executes commands within the host namespace using nsenter
type nsenterRunner struct {
	targetPID string
}

func NewNsenterRunner() *nsenterRunner {
	return &nsenterRunner{targetPID: "1"}
}

func (r *nsenterRunner) Run(ctx context.Context, command string, args ...string) ([]byte, error) {
	return nil, errNotImplemented
}
