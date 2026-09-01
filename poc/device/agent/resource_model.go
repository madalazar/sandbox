package main

import "strings"

// RequirementRef identifies the owner of a reservation within a deployment.
// The component name is the whole identity; it is unique within a manifest.
type RequirementRef string

// OwnerRef identifies the holder of a resource reservation: one component of one deployment.
type OwnerRef struct {
	Deployment string
	Ref        RequirementRef
}

func NewOwnerRef(deploymentID string, componentName string) OwnerRef {
	return OwnerRef{
		Deployment: strings.TrimSpace(deploymentID),
		Ref:        RequirementRef(strings.TrimSpace(componentName)),
	}
}

// String is the persisted owner encoding used by database.AllocatedCpus and AllocatedCaches.
func (o OwnerRef) String() string {
	if o.Ref == "" {
		return o.Deployment
	}
	return o.Deployment + "/" + string(o.Ref)
}

// CanTake reports whether o may claim a resource currently held by holder: free, or
// already owned by o itself. A sibling component's claim blocks.
func (o OwnerRef) CanTake(holder OwnerRef) bool {
	if holder.Deployment == "" {
		return true
	}
	// A record written without a requirement key encodes the bare deployment ID.
	if holder.Ref == "" && holder.Deployment == o.Deployment {
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
	return OwnerRef{Deployment: strings.TrimSpace(deployment), Ref: RequirementRef(strings.TrimSpace(ref))}
}
