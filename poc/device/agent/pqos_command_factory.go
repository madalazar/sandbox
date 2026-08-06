package main

import (
	"fmt"
	"strings"
)

const (
	pqosApplyCommandTemplate      = "modprobe msr >/dev/null 2>&1 || true;  pqos --iface=%s -e 'llc@%s:%s=%s' -a 'core:%s=%s'"
	pqosResetCommandTemplate      = "modprobe msr >/dev/null 2>&1 || true;  pqos --iface=%s -e '%s'"
	pqosResetCoreAssignmentFormat = " -a 'core:0=%s'"
)

type pqosCommandFactory interface {
	GetPQoSInterface() string
	BuildApplyCommand(cacheID, cosID, mask, cpuset string) string
	BuildResetCommand(resetSpec, classCPUSet string) string
}

type ifacePQoSCommandFactory struct {
	iface string
}

func (f ifacePQoSCommandFactory) GetPQoSInterface() string {
	return f.iface
}

func (f ifacePQoSCommandFactory) BuildApplyCommand(cacheID, cosID, mask, cpuset string) string {
	return fmt.Sprintf(
		pqosApplyCommandTemplate,
		f.iface,
		cacheID,
		cosID,
		mask,
		cosID,
		cpuset,
	)
}

func (f ifacePQoSCommandFactory) BuildResetCommand(resetSpec, classCPUSet string) string {
	base := fmt.Sprintf(
		pqosResetCommandTemplate,
		f.iface,
		resetSpec,
	)

	if classCPUSet != "" {
		return base + fmt.Sprintf(pqosResetCoreAssignmentFormat, classCPUSet)
	}

	return base
}

func newPQoSCommandFactory(rawIface string) (pqosCommandFactory, error) {
	iface := strings.ToLower(strings.TrimSpace(rawIface))
	switch iface {
	case "os", "msr":
		return ifacePQoSCommandFactory{iface: iface}, nil
	default:
		return nil, fmt.Errorf("invalid pqos interface %q (expected os or msr)", rawIface)
	}
}
