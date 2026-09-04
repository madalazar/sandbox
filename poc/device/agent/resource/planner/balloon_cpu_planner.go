package planner

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

var _ CpuPlanner = BalloonCpuPlanner{}

var cpuIndexRegex = regexp.MustCompile(`cpu(\d+)`)

// places a component into an NRI balloon whose cpus are isolated. It is the helm
// runtime's cpu planner: it returns the balloon's cpus and the balloon name, and
// constructs no kubernetes annotations
type BalloonCpuPlanner struct {
	policies model.BalloonPolicyReader
	isolated []int
}

func NewBalloonCpuPlanner(policies model.BalloonPolicyReader, isolated []int) BalloonCpuPlanner {
	return BalloonCpuPlanner{policies: policies, isolated: isolated}
}

// selects the smallest free isolated balloon that satisfies the component's isolated
// core request and reserves its cpus on the ledger
func (p BalloonCpuPlanner) PlanCpu(request CpuPlanningRequest) (model.CpuPlan, error) {
	requirements := request.Requirements
	ref := requirements.Component

	if !requirements.HasIsolatedCores() || len(p.isolated) == 0 {
		return model.CpuPlan{Component: ref}, nil
	}

	requiredIsolatedCores := requirements.CountIsolatedCores()
	if requiredIsolatedCores > 1 {
		return model.CpuPlan{}, fmt.Errorf(
			"component %q requests %d isolated cores; only workloads requiring 1 isolated core are supported",
			ref, requiredIsolatedCores,
		)
	}

	if p.policies == nil {
		return model.CpuPlan{}, fmt.Errorf(
			"cannot resolve isolated CPU assignment for component %q: policy reader not configured",
			ref,
		)
	}

	policy := p.policies.Parsed()
	if policy == nil {
		return model.CpuPlan{}, fmt.Errorf(
			"cannot resolve isolated CPU assignment for component %q: no BalloonsPolicy snapshot available",
			ref,
		)
	}

	selectedBalloonName := ""
	selectedBalloonCpus := []int(nil)

	for _, balloon := range policy.BalloonTypes {
		if balloon.PreferIsolCpus != nil && !*balloon.PreferIsolCpus {
			continue
		}

		refs := uniqueSortedCpuRefs(balloon.PreferCloseToDevices)
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

		if len(selectedBalloonCpus) == 0 || len(refs) < len(selectedBalloonCpus) {
			selectedBalloonName = balloon.Name
			selectedBalloonCpus = refs
		}
	}

	if selectedBalloonName == "" || len(selectedBalloonCpus) == 0 {
		return model.CpuPlan{}, fmt.Errorf(
			"no free isolated balloon found for component %q (requiredIsolatedCores=%d)",
			ref, requiredIsolatedCores,
		)
	}

	if err := request.Ledger.ReserveCpus(ref, selectedBalloonCpus); err != nil {
		return model.CpuPlan{}, err
	}

	return model.CpuPlan{
		Component: ref,
		Cpus:      selectedBalloonCpus,
		Placement: model.CpuPlacement{Class: selectedBalloonName},
	}, nil
}

func uniqueSortedCpuRefs(paths []string) []int {
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
