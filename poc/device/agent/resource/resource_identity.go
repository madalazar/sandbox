package resource

import "strings"

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
