package controller

import (
	"context"
)

// executes commands on the host or inside a namespace
type CommandRunner interface {
	Run(ctx context.Context, command string, args ...string) ([]byte, error)
}
