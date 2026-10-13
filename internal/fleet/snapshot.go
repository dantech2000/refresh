package fleet

import (
	"time"

	"github.com/dantech2000/refresh/internal/apidoc"
)

// State says how much of a field group the fetcher read. A field group is
// the set of fields that one call, or one short chain of calls, fills. A
// field is valid only when the state of its group is Collected.
type State uint8

// The states of a field group.
const (
	// NotRequested: the caller did not ask for the group.
	NotRequested State = iota
	// Collected: every call answered.
	Collected
	// Partial: part was read; the failures of the snapshot name what is
	// missing.
	Partial
	// Failed: a call failed and nothing was read.
	Failed
	// NotAttempted: the run ended before the call was sent.
	NotAttempted
)

func (s State) String() string {
	switch s {
	case NotRequested:
		return "NotRequested"
	case Collected:
		return "Collected"
	case Partial:
		return "Partial"
	case Failed:
		return "Failed"
	case NotAttempted:
		return "NotAttempted"
	}
	return "State(?)"
}

// Snapshot is one cluster as one Fetch read it.
type Snapshot struct {
	Cluster Cluster
	// Addons are the installed add-ons, sorted by name. An add-on that was
	// listed and could not be described is in the list with its name and
	// its state.
	Addons []Addon
	// Nodegroups are the managed nodegroups, sorted by name, with the same
	// rule for one that could not be described.
	Nodegroups []Nodegroup
	Have       Have
	// Failures are the reads that failed, for this cluster.
	Failures  []apidoc.Failure
	FetchedAt time.Time
}

// Have holds the state of each cluster-level field group.
type Have struct {
	// Cluster is the state of the fields that DescribeCluster fills.
	Cluster State
	// Addons is the state of the add-on list. Each add-on has its own
	// state: the list is Partial when one of them is not Collected.
	Addons State
	// Nodegroups is the state of the nodegroup list, with the same rule.
	Nodegroups State
}

// Incomplete reports whether a read of the snapshot failed.
func (s *Snapshot) Incomplete() bool {
	return len(s.Failures) > 0
}

// Cluster is an EKS cluster.
type Cluster struct {
	// Name and Region are always set.
	Name, Region string

	// The fields below come from DescribeCluster. They are valid when
	// Have.Cluster is Collected.

	ARN string
	// Status is the EKS status, such as ACTIVE.
	Status          string
	Version         Version
	PlatformVersion string
	Endpoint        string
	CreatedAt       time.Time
	Tags            map[string]string
	// UpgradePolicy is the EKS support type: STANDARD, EXTENDED, or "".
	UpgradePolicy string
	// AutoMode reports EKS Auto Mode compute.
	AutoMode     bool
	HealthIssues []Issue
	Network      Network
	Security     Security
}

// Issue is a health issue that EKS reports for a resource.
type Issue struct {
	Code, Message string
	ResourceIDs   []string
}

// Network is the VPC configuration of a cluster.
type Network struct {
	VPCID                       string
	SubnetIDs, SecurityGroupIDs []string
	PublicAccess, PrivateAccess bool
	PublicAccessCIDRs           []string
}

// Security is the security configuration of a cluster.
type Security struct {
	RoleARN string
	// KMSKeyARN is "" when secrets are not encrypted with a KMS key.
	KMSKeyARN          string
	LogTypes           []string
	DeletionProtection bool
}

// Addon is an installed EKS add-on.
type Addon struct {
	Name string
	// State is the state of the fields below, which DescribeAddon fills.
	State State

	Version string
	// Status is the EKS status, such as ACTIVE.
	Status                     string
	ARN, ServiceAccountRoleARN string
	// Configuration is the configuration values, as EKS returns them.
	Configuration         string
	Issues                []Issue
	CreatedAt, ModifiedAt time.Time
}

// Health returns the health of the add-on, from its status.
func (a Addon) Health() apidoc.AddonHealth {
	switch a.Status {
	case "ACTIVE":
		return apidoc.AddonHealthy
	case "DEGRADED", "CREATE_FAILED", "UPDATE_FAILED", "DELETE_FAILED":
		return apidoc.AddonFailed
	case "CREATING", "UPDATING", "DELETING":
		return apidoc.AddonInProgress
	}
	return apidoc.AddonUnknown
}

// Nodegroup is an EKS managed nodegroup.
type Nodegroup struct {
	Name string
	// State is the state of the fields that DescribeNodegroup fills: all
	// but AMI, Instances, and InstancesState.
	State State

	// Status, AMIType, and CapacityType are the EKS values.
	Status, AMIType, CapacityType string
	// ReleaseVersion is the AMI release version, such as 1.33.0-20260101.
	ReleaseVersion    string
	Version           Version
	InstanceTypes     []string
	Desired, Min, Max int32
	AutoScalingGroups []string
	// LaunchTemplate is the zero value when the nodegroup has none.
	LaunchTemplate        LaunchTemplate
	HealthIssues          []Issue
	CreatedAt, ModifiedAt time.Time

	AMI AMI
	// Instances are the EC2 instances of the first Auto Scaling group.
	// They are valid when InstancesState is Collected.
	Instances      []Instance
	InstancesState State
}

// LaunchTemplate names one version of an EC2 launch template.
type LaunchTemplate struct {
	ID, Version string
}

// AMI is the AMI group of a nodegroup.
type AMI struct {
	State State
	// Current is the ID of the AMI that the nodes run, and Latest the ID
	// of the latest recommended one. Either is "" when there is none to
	// read: a nodegroup with no instance, or an AMI type with no
	// recommended AMI.
	Current, Latest string
}

// Instance is an EC2 instance of a nodegroup.
type Instance struct {
	ID, ImageID, Type string
	// State is the EC2 state name, and Lifecycle is on-demand or spot.
	State, Lifecycle string
	AvailabilityZone string
	LaunchTime       time.Time
}

// AMIStatus compares the AMI that the nodes run with the latest
// recommended one. known is false when the comparison could not be made,
// because the nodegroup or its AMIs were not read.
//
// A nodegroup that is updating, or that has a custom AMI, has its status
// from the nodegroup alone. A nodegroup with no instance to read, or with
// an AMI type that has no recommended AMI, is Unknown and known: nothing
// failed.
func (n Nodegroup) AMIStatus() (status apidoc.AMIStatus, known bool) {
	switch {
	case n.State != Collected:
		return apidoc.AMIUnknown, false
	case n.Status == "UPDATING":
		return apidoc.AMIUpdating, true
	case n.AMIType == "CUSTOM":
		return apidoc.AMICustom, true
	case n.AMI.State != Collected:
		return apidoc.AMIUnknown, false
	case n.AMI.Current == "" || n.AMI.Latest == "":
		return apidoc.AMIUnknown, true
	case n.AMI.Current == n.AMI.Latest:
		return apidoc.AMILatest, true
	}
	return apidoc.AMIOutdated, true
}

// VersionBehind reports whether n runs an older Kubernetes minor version
// than the control plane. known is false when the nodegroup or the cluster
// was not read, or a version is not known.
func (s *Snapshot) VersionBehind(n Nodegroup) (behind, known bool) {
	if n.State != Collected || s.Have.Cluster != Collected || n.Version.IsZero() || s.Cluster.Version.IsZero() {
		return false, false
	}
	return n.Version.Compare(s.Cluster.Version) < 0, true
}
