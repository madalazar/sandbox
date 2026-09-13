package controller

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// executes commands on the host or inside a namespace
type CommandRunner interface {
	Run(ctx context.Context, command string, args ...string) ([]byte, error)
}

var _ CommandRunner = (*directRunner)(nil)
var _ CommandRunner = (*nsenterRunner)(nil)

// executes commands directly on the host
type directRunner struct{}

func NewDirectRunner() CommandRunner {
	return &directRunner{}
}

func (r *directRunner) Run(ctx context.Context, command string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%s %s failed: %w: %s", command, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

// executes commands within the host namespace using nsenter
type nsenterRunner struct {
	targetPID string
}

func NewNsenterRunner() CommandRunner {
	return &nsenterRunner{targetPID: "1"}
}

func NewNsenterRunnerWithPid(targetPID string) *nsenterRunner {
	if targetPID == "" {
		targetPID = "1"
	}
	return &nsenterRunner{targetPID: targetPID}
}

func (r *nsenterRunner) Run(ctx context.Context, command string, args ...string) ([]byte, error) {
	nsArgs := []string{"-t", r.targetPID, "-m", "-u", "-i", "-n", "-p", "--", command}
	nsArgs = append(nsArgs, args...)
	cmd := exec.CommandContext(ctx, "nsenter", nsArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("nsenter %s failed: %w: %s", strings.Join(nsArgs, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}
