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
	Have    Have
	// Failures are the reads that failed, for this cluster.
	Failures  []apidoc.Failure
	FetchedAt time.Time
}

// Have holds the state of each cluster-level field group.
type Have struct {
	// Cluster is the state of the fields that DescribeCluster fills.
	Cluster State
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
