package controller

import (
	"fmt"
	"strings"
)

const (
	PqosInterfaceOs  = "os"
	PqosInterfaceMsr = "msr"

	pqosLlcAllocationFormat       = "llc@%s:%s=%s"
	pqosApplyCommandTemplate      = "modprobe msr >/dev/null 2>&1 || true; pqos --iface=%s -e '" + pqosLlcAllocationFormat + "' -a 'core:%s=%s'"
	pqosResetCommandTemplate      = "modprobe msr >/dev/null 2>&1 || true; pqos --iface=%s -e '%s'"
	pqosResetCoreAssignmentFormat = " -a 'core:" + defaultPqosCosId + "=%s'"
)

type PqosCommandFactory interface {
	PqosInterface() string
	BuildApplyCommand(cacheId, cosId, mask, cpuset string) string
	BuildResetCommand(cacheId, cosId, mask, cpuset string) string
}

type ifacePqosCommandFactory struct {
	iface string
}

func (f ifacePqosCommandFactory) PqosInterface() string {
	return f.iface
}

func (f ifacePqosCommandFactory) BuildApplyCommand(cacheId, cosId, mask, cpuset string) string {
	return fmt.Sprintf(
		pqosApplyCommandTemplate,
		f.iface,
		cacheId,
		cosId,
		mask,
		cosId,
		cpuset,
	)
}

func (f ifacePqosCommandFactory) BuildResetCommand(cacheId, cosId, mask, cpuset string) string {
	resetSpec := fmt.Sprintf(pqosLlcAllocationFormat, cacheId, cosId, mask)
	base := fmt.Sprintf(
		pqosResetCommandTemplate,
		f.iface,
		resetSpec,
	)

	if cpuset != "" {
		return base + fmt.Sprintf(pqosResetCoreAssignmentFormat, cpuset)
	}

	return base
}

func NewPqosCommandFactory(rawIface string) (PqosCommandFactory, error) {
	iface := strings.ToLower(strings.TrimSpace(rawIface))
	if iface == "" {
		iface = PqosInterfaceOs
	}
	switch iface {
	case PqosInterfaceOs, PqosInterfaceMsr:
		return ifacePqosCommandFactory{iface: iface}, nil
	default:
		return nil, fmt.Errorf("invalid pqos interface %q (expected %s or %s)", rawIface, PqosInterfaceOs, PqosInterfaceMsr)
	}
}
