package model

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// will contain the nri policy's balloon name when agent runs on helm/k3s device
// it is empty for direct pinning
type CpuPlacement struct {
	Class string
}

// represents what cpu planning decided for one component. It is deterministic and
// carries no runtime or persistence detail
// IMPORTANT: for the poc we assume that we will resolve (via NormalizeCpuRequirements) to one set
// of cpu assignments per component, this has two implications:
// 1. We can assume the component name as our identifier
// 2. We will have exactly one set of cpu assignments per component
type CpuPlan struct {
	Component ComponentRef
	// if at any point we will have mutiple pairs of cpu assignments for the same component
	// we might need to move the pairs into a separate struct and reference them here as
	// a slice
	Cpus      []int
	Placement CpuPlacement
}

func (p CpuPlan) HasCpus() bool { return len(p.Cpus) > 0 }

// renders every assigned index as a Linux cpuset list.
func (p CpuPlan) CpuSet() string {
	cpus := make([]int, 0, len(p.Cpus))
	cpus = append(cpus, p.Cpus...)
	return FormatCpuSet(cpus)
}

// renders cpu indices as a Linux cpuset list, collapsing runs into ranges
func FormatCpuSet(cpus []int) string {
	if len(cpus) == 0 {
		return ""
	}

	sorted := make([]int, len(cpus))
	copy(sorted, cpus)
	sort.Ints(sorted)

	compact := make([]string, 0, len(sorted))
	start := sorted[0]
	prev := sorted[0]

	flush := func(s int, e int) {
		if s == e {
			compact = append(compact, strconv.Itoa(s))
			return
		}
		compact = append(compact, fmt.Sprintf("%d-%d", s, e))
	}

	for i := 1; i < len(sorted); i++ {
		current := sorted[i]
		if current == prev+1 {
			prev = current
			continue
		}
		flush(start, prev)
		start = current
		prev = current
	}
	flush(start, prev)

	return strings.Join(compact, ",")
}

// returns the plan's placement class, empty when the runtime pins cpus
// directly. One component deploys one unit, so at most one class is in play
func (p CpuPlan) PlacementClass() string {
	if p.Placement.Class != "" {
		return p.Placement.Class
	}
	return ""
}

// projects the plan onto the persisted map form, keyed by component
func (p CpuPlan) AssignmentMap() map[string][]int {
	assignments := make(map[string][]int, 1)
	assignments[string(p.Component)] = append([]int(nil), p.Cpus...)
	return assignments
}
