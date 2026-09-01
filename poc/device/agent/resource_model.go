package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ComponentRef identifies the owner of a reservation within a deployment. The component
// name is the whole identity; it is unique within a manifest.
type ComponentRef string

// OwnerRef identifies the holder of a resource reservation: one component of one deployment.
type OwnerRef struct {
	Deployment string
	Component  ComponentRef
}

func NewOwnerRef(deploymentID string, componentName string) OwnerRef {
	return OwnerRef{
		Deployment: strings.TrimSpace(deploymentID),
		Component:  ComponentRef(strings.TrimSpace(componentName)),
	}
}

// String is the persisted owner encoding used by database.AllocatedCpus and AllocatedCaches.
func (o OwnerRef) String() string {
	if o.Component == "" {
		return o.Deployment
	}
	return o.Deployment + "/" + string(o.Component)
}

// CanTake reports whether o may claim a resource currently held by holder: free, or
// already owned by o itself. A sibling component's claim blocks.
func (o OwnerRef) CanTake(holder OwnerRef) bool {
	if holder.Deployment == "" {
		return true
	}
	// A record written without a component key encodes the bare deployment ID.
	if holder.Component == "" && holder.Deployment == o.Deployment {
		return true
	}
	return holder == o
}

// ParseOwnerRef decodes the persisted owner encoding.
func ParseOwnerRef(owner string) OwnerRef {
	deployment, ref, found := strings.Cut(strings.TrimSpace(owner), "/")
	if !found {
		return OwnerRef{Deployment: deployment}
	}
	return OwnerRef{Deployment: strings.TrimSpace(deployment), Component: ComponentRef(strings.TrimSpace(ref))}
}

// CpuPlacement is the runtime's placement directive for one assignment. Class names
// the balloon a Kubernetes workload is placed into; it is empty for direct pinning.
type CpuPlacement struct {
	Class string
}

// CpuAssignment is one component's decided CPU indices.
type CpuAssignment struct {
	Component ComponentRef
	Cpus      []int
	Placement CpuPlacement
}

// CpuPlan is what CPU planning decided for one component. It is deterministic and
// carries no runtime or persistence detail.
type CpuPlan struct {
	Assignments []CpuAssignment
}

func (p CpuPlan) HasCpus() bool { return len(p.Assignments) > 0 }

// CpuSet renders every assigned index as a Linux cpuset list.
func (p CpuPlan) CpuSet() string {
	cpus := make([]int, 0, len(p.Assignments))
	for _, assignment := range p.Assignments {
		cpus = append(cpus, assignment.Cpus...)
	}
	return formatCpuSet(cpus)
}

// formatCpuSet renders CPU indices as a Linux cpuset list, collapsing runs into ranges.
func formatCpuSet(cpus []int) string {
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

// PlacementClass returns the plan's placement class, empty when the runtime pins CPUs
// directly. One component deploys one unit, so at most one class is in play.
func (p CpuPlan) PlacementClass() string {
	for _, assignment := range p.Assignments {
		if assignment.Placement.Class != "" {
			return assignment.Placement.Class
		}
	}
	return ""
}

// AssignmentMap projects the plan onto the persisted map form, keyed by component.
func (p CpuPlan) AssignmentMap() map[string][]int {
	assignments := make(map[string][]int, len(p.Assignments))
	for _, assignment := range p.Assignments {
		assignments[string(assignment.Component)] = append([]int(nil), assignment.Cpus...)
	}
	return assignments
}
