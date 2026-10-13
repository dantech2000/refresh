package apidoc

import "time"

// AMIStatus compares the AMI that a nodegroup runs with the latest
// recommended AMI for its Kubernetes version and AMI type.
type AMIStatus string

// The AMI status values.
const (
	// AMILatest: the nodegroup runs the latest recommended AMI.
	AMILatest AMIStatus = "Latest"
	// AMIOutdated: a newer recommended AMI exists.
	AMIOutdated AMIStatus = "Outdated"
	// AMIUpdating: the nodegroup is being updated.
	AMIUpdating AMIStatus = "Updating"
	// AMICustom: the user manages the AMI through a launch template, so
	// there is no recommended AMI to compare with.
	AMICustom AMIStatus = "Custom"
	// AMIUnknown: there is nothing to compare, such as a nodegroup with no
	// instance.
	AMIUnknown AMIStatus = "Unknown"
)

// Values implements Enum.
func (AMIStatus) Values() []string {
	return []string{string(AMILatest), string(AMIOutdated), string(AMIUpdating), string(AMICustom), string(AMIUnknown)}
}

// Nodegroup is one EKS managed nodegroup. A key is left out when its data
// was not read: a nodegroup that could not be described has its name and
// incomplete.
type Nodegroup struct {
	Name string `json:"name"`
	// Status is the EKS status, such as ACTIVE or UPDATING.
	Status string `json:"status,omitzero"`
	// Version is the Kubernetes minor version of the nodegroup.
	Version string `json:"version,omitzero"`
	// VersionBehind is true when the nodegroup runs an older minor version
	// than the control plane. It is left out when one of the two versions
	// is not known.
	VersionBehind *bool `json:"versionBehind,omitzero"`
	// AMIType is the EKS AMI type, such as AL2023_x86_64_STANDARD.
	AMIType string `json:"amiType,omitzero"`
	// ReleaseVersion is the AMI release version of the nodegroup.
	ReleaseVersion string `json:"releaseVersion,omitzero"`
	// CapacityType is the EKS capacity type, such as ON_DEMAND or SPOT.
	CapacityType  string        `json:"capacityType,omitzero"`
	InstanceTypes []string      `json:"instanceTypes,omitzero"`
	Scaling       *Scaling      `json:"scaling,omitzero"`
	AMI           *NodegroupAMI `json:"ami,omitzero"`
	// HealthIssues are the issues that EKS reports for the nodegroup.
	HealthIssues []Issue `json:"healthIssues,omitzero"`
	// Instances are the EC2 instances of the nodegroup, in a document that
	// reads them.
	Instances  []Instance `json:"instances,omitzero"`
	CreatedAt  time.Time  `json:"createdAt,omitzero"`
	ModifiedAt time.Time  `json:"modifiedAt,omitzero"`
	// Incomplete is true when part of the nodegroup could not be read. Its
	// failures are in the failures list of the document.
	Incomplete bool `json:"incomplete,omitzero"`
}

// Scaling is the scaling configuration of a nodegroup.
type Scaling struct {
	Desired int32 `json:"desired"`
	Min     int32 `json:"min"`
	Max     int32 `json:"max"`
}

// NodegroupAMI is the AMI of a nodegroup and how it compares with the
// latest recommended one.
type NodegroupAMI struct {
	Status AMIStatus `json:"status"`
	// Current is the ID of the AMI that the nodes run. It is left out for
	// a nodegroup with no instance to read.
	Current string `json:"current,omitzero"`
	// Latest is the ID of the latest recommended AMI. It is left out for
	// an AMI type that has none.
	Latest string `json:"latest,omitzero"`
	// CurrentCreatedAt is when the current AMI was created. "refresh
	// status" reads it for an outdated AMI.
	CurrentCreatedAt time.Time `json:"currentCreatedAt,omitzero"`
}

// Instance is an EC2 instance of a nodegroup.
type Instance struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	// State is the EC2 instance state, such as running.
	State string `json:"state"`
	// Lifecycle is on-demand or spot.
	Lifecycle        string    `json:"lifecycle"`
	AvailabilityZone string    `json:"availabilityZone"`
	ImageID          string    `json:"imageId"`
	LaunchTime       time.Time `json:"launchTime,omitzero"`
}

// NodegroupList is the document of "refresh nodegroup list".
type NodegroupList struct {
	Cluster    string      `json:"cluster"`
	Region     string      `json:"region"`
	Nodegroups []Nodegroup `json:"nodegroups"`
	Failures   Failures    `json:"failures"`
}

// Kind implements Document.
func (NodegroupList) Kind() Kind { return KindNodegroupList }

// NodegroupDescription is the document of "refresh nodegroup describe".
type NodegroupDescription struct {
	Cluster   string    `json:"cluster"`
	Region    string    `json:"region"`
	Nodegroup Nodegroup `json:"nodegroup"`
	Failures  Failures  `json:"failures"`
}

// Kind implements Document.
func (NodegroupDescription) Kind() Kind { return KindNodegroupDescription }
