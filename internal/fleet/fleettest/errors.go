package fleettest

import "fmt"

// NotFoundError says that a cluster, nodegroup, or add-on does not exist.
type NotFoundError struct {
	// Kind is "cluster", "nodegroup", or "addon".
	Kind string
	Name string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("no %s named %q", e.Kind, e.Name)
}

// InUseError says that the resource cannot be updated now: it has an update
// in progress, or its status is not one that accepts an update.
type InUseError struct {
	// Kind is "cluster", "nodegroup", or "addon".
	Kind string
	Name string
	// UpdateID is the update in progress, or "" when Status is the reason.
	UpdateID string
	Status   ResourceStatus
}

func (e *InUseError) Error() string {
	if e.UpdateID == "" {
		return fmt.Sprintf("%s %s is %s, not in a status that accepts an update", e.Kind, e.Name, e.Status)
	}
	return fmt.Sprintf("%s has update %s in progress", e.Name, e.UpdateID)
}

// InvalidError says that a request is not valid.
type InvalidError struct {
	Message string
}

func (e *InvalidError) Error() string { return e.Message }
