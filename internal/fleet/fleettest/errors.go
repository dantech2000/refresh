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

// InUseError says that the resource has an update in progress.
type InUseError struct {
	Name     string
	UpdateID string
}

func (e *InUseError) Error() string {
	return fmt.Sprintf("%s has update %s in progress", e.Name, e.UpdateID)
}

// InvalidError says that a request is not valid.
type InvalidError struct {
	Message string
}

func (e *InvalidError) Error() string { return e.Message }
