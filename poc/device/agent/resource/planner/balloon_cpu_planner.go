package planner

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"

	"github.com/margo/sandbox/poc/device/agent/resource"
)

var _ CPUPlanner = BalloonCPUPlanner{}

var cpuIndexRegex = regexp.MustCompile(`cpu(\d+)`)

// BalloonCPUPlanner places a component into an NRI balloon whose CPUs are isolated.
// It is the Kubernetes runtime's CPU planner: it returns the balloon's CPUs and the
// balloon name, and constructs no Kubernetes annotations.
type BalloonCPUPlanner struct {
	policies resource.BalloonPolicyReader
	isolated []int
}

func NewBalloonCPUPlanner(policies resource.BalloonPolicyReader, isolated []int) BalloonCPUPlanner {
	return BalloonCPUPlanner{policies: policies, isolated: isolated}
}

func (p BalloonCPUPlanner) PlanCPU(request CPUPlanningRequest) (resource.CpuPlan, error) {
	requirements := request.Requirements
	ref := requirements.Component

	fmt.Println("balloon planner: resolved CPU requirements",
		"component:", ref,
		"shared:", len(requirements.Shared),
		"isolated:", len(requirements.Isolated),
	)

	if !requirements.HasIsolatedCores() || len(p.isolated) == 0 {
		fmt.Println(
			"balloon planner: component uses shared cores only; skipping isolated CPU allocation",
			"component:", ref,
		)
		return resource.CpuPlan{}, nil
	}

	requiredIsolatedCores := requirements.CountIsolatedCores()
	if requiredIsolatedCores > 1 {
		return resource.CpuPlan{}, fmt.Errorf(
			"component %q requests %d isolated cores; only workloads requiring 1 isolated core are supported",
			ref, requiredIsolatedCores,
		)
	}

	if p.policies == nil {
		return resource.CpuPlan{}, fmt.Errorf(
			"cannot resolve isolated CPU assignment for component %q: policy reader not configured",
			ref,
		)
	}

	policy := p.policies.Parsed()
	if policy == nil {
		return resource.CpuPlan{}, fmt.Errorf(
			"cannot resolve isolated CPU assignment for component %q: no BalloonsPolicy snapshot available",
			ref,
		)
	}

	selectedBalloonName := ""
	selectedBalloonCPUs := []int(nil)

	for _, balloon := range policy.BalloonTypes {
		if balloon.PreferIsolCpus != nil && !*balloon.PreferIsolCpus {
			continue
		}

		refs := uniqueSortedCPURefs(balloon.PreferCloseToDevices)
		if len(refs) < requiredIsolatedCores {
			continue
		}

		occupied := false
		for _, idx := range refs {
			if !request.Ledger.IsCpuAvailable(idx, ref) {
				occupied = true
				break
			}
		}
		if occupied {
			continue
		}

		if len(selectedBalloonCPUs) == 0 || len(refs) < len(selectedBalloonCPUs) {
			selectedBalloonName = balloon.Name
			selectedBalloonCPUs = refs
		}
	}

	if selectedBalloonName == "" || len(selectedBalloonCPUs) == 0 {
		return resource.CpuPlan{}, fmt.Errorf(
			"no free isolated balloon found for component %q (requiredIsolatedCores=%d)",
			ref, requiredIsolatedCores,
		)
	}

	if err := request.Ledger.ReserveCPUs(ref, selectedBalloonCPUs); err != nil {
		return resource.CpuPlan{}, err
	}

	fmt.Println("balloon planner: isolated balloon selected",
		"component:", ref,
		"balloon:", selectedBalloonName,
		"cpus:", selectedBalloonCPUs,
	)

	return resource.CpuPlan{Assignments: []resource.CpuAssignment{{
		Component: ref,
		Cpus:      selectedBalloonCPUs,
		Placement: resource.CpuPlacement{Class: selectedBalloonName},
	}}}, nil
}

func uniqueSortedCPURefs(paths []string) []int {
	if len(paths) == 0 {
		return nil
	}

	seen := map[int]struct{}{}
	for _, path := range paths {
		matches := cpuIndexRegex.FindAllStringSubmatch(path, -1)
		for _, m := range matches {
			if len(m) != 2 {
				continue
			}
			idx, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			seen[idx] = struct{}{}
		}
	}

	refs := make([]int, 0, len(seen))
	for idx := range seen {
		refs = append(refs, idx)
	}
	sort.Ints(refs)

	return refs
}
