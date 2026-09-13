package controller

import (
	"fmt"
	"strings"
)

const (
	PqosInterfaceOs  = "os"
	PqosInterfaceMsr = "msr"

	pqosLlcAllocationFormat  = "llc@%s:%s=%s"
	pqosCoreAssignmentFormat = "core:%s=%s"
)

type PqosCommandFactory interface {
	PqosInterface() string
	BuildApplyArgs(cacheId, cosId, mask, cpuset string) []string
	BuildResetArgs(cacheId, cosId, mask, cpuset string) []string
}

type ifacePqosCommandFactory struct {
	iface string
}

func (f ifacePqosCommandFactory) PqosInterface() string {
	return f.iface
}

func (f ifacePqosCommandFactory) BuildApplyArgs(cacheId, cosId, mask, cpuset string) []string {
	return []string{
		"--iface=" + f.iface,
		"-e",
		fmt.Sprintf(pqosLlcAllocationFormat, cacheId, cosId, mask),
		"-a",
		fmt.Sprintf(pqosCoreAssignmentFormat, cosId, cpuset),
	}
}

func (f ifacePqosCommandFactory) BuildResetArgs(cacheId, cosId, mask, cpuset string) []string {
	args := []string{
		"--iface=" + f.iface,
		"-e",
		fmt.Sprintf(pqosLlcAllocationFormat, cacheId, cosId, mask),
	}

	if cpuset != "" {
		args = append(args, "-a", fmt.Sprintf(pqosCoreAssignmentFormat, defaultPqosCosId, cpuset))
	}

	return args
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
