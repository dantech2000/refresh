package nodegroup

import (
	"time"

	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/types"
)

// ListOptions controls nodegroup listing behavior
type ListOptions struct {
	Filters map[string]string `json:"filters"`
}

// DescribeOptions controls describe behavior for nodegroups
type DescribeOptions struct {
	ShowInstances bool `json:"showInstances"`
	ShowWorkloads bool `json:"showWorkloads"`
}

// ScaleOptions controls intelligent scaling behavior
type ScaleOptions struct {
	HealthCheck bool          `json:"healthCheck"`
	CheckPDBs   bool          `json:"checkPdbs"`
	Wait        bool          `json:"wait"`
	Timeout     time.Duration `json:"timeout"`
	DryRun      bool          `json:"dryRun"`
}

// ScalingConfig models the EKS managed nodegroup scaling configuration
type ScalingConfig struct {
	DesiredSize int32 `json:"desiredSize"`
	MinSize     int32 `json:"minSize"`
	MaxSize     int32 `json:"maxSize"`
	AutoScaling bool  `json:"autoScaling"`
}

// InstanceDetails describes an EC2 instance backing a nodegroup.
type InstanceDetails struct {
	InstanceID   string    `json:"instanceId"`
	InstanceType string    `json:"instanceType"`
	LaunchTime   time.Time `json:"launchTime"`
	Lifecycle    string    `json:"lifecycle"` // on-demand, spot
	State        string    `json:"state"`
	AZ           string    `json:"availabilityZone"`
}

// WorkloadInfo summarizes pods/workloads placed on a nodegroup
type WorkloadInfo struct {
	TotalPods     int    `json:"totalPods"`
	CriticalPods  int    `json:"criticalPods"`
	PodDisruption string `json:"podDisruption"` // summarized for now
}

// NodegroupSummary contains basic nodegroup info for listings
type NodegroupSummary struct {
	Name         string `json:"name"`
	Status       string `json:"status"`
	InstanceType string `json:"instanceType"`
	DesiredSize  int32  `json:"desiredSize"`
	// ReadyNodes is a measured count of Kubernetes Ready=True nodes, valid only
	// when ReadyKnown is true. When ReadyKnown is false the readiness was not
	// measured (no --check-readiness, or the cluster API was unreachable) and
	// ReadyNodes must not be read as a real count. (REF-130)
	ReadyNodes int32 `json:"readyNodes"`
	ReadyKnown bool  `json:"readyKnown"`
	// AMI information - core functionality of refresh tool
	CurrentAMI string          `json:"currentAmi"`
	AMIStatus  types.AMIStatus `json:"amiStatus"`
	// K8sVersion is the nodegroup's Kubernetes minor. VersionBehind is true
	// when it trails the control plane. AMIStatus is judged against the
	// nodegroup's own minor, so a lagging nodegroup can be AMILatest and still
	// need a version upgrade; VersionBehind carries that signal.
	K8sVersion    string `json:"k8sVersion" yaml:"k8sVersion"`
	VersionBehind bool   `json:"versionBehind" yaml:"versionBehind"`
	// AMILookupError is set when the latest recommended AMI could not be
	// resolved (e.g. no ssm:GetParameter permission or throttling). AMIStatus
	// is then Unknown because of the failure, not because nothing is stale.
	AMILookupError string `json:"amiLookupError,omitempty" yaml:"amiLookupError,omitempty"`
}

// ListResult is the full outcome of a nodegroup listing.
type ListResult struct {
	// Summaries are the nodegroups that were described (and matched the filters).
	Summaries []NodegroupSummary
	// Failures has a "name: reason" entry for every listed nodegroup left out
	// of Summaries because it could not be described.
	Failures []string
	// AMILookupFailures has a "name: reason" entry for every nodegroup in
	// Summaries whose latest recommended AMI could not be resolved.
	AMILookupFailures []string
	// AMILookupErr is the first latest-AMI lookup error, kept unflattened so
	// the command layer can format it (e.g. name the missing IAM permission).
	AMILookupErr error
}

// NodegroupDetails extends summary with health and optional instance/workload details
type NodegroupDetails struct {
	Name         string `json:"name"`
	Status       string `json:"status"`
	InstanceType string `json:"instanceType"`
	AmiType      string `json:"amiType"`
	CapacityType string `json:"capacityType"` // ON_DEMAND, SPOT

	// AMI information - core functionality of refresh tool
	CurrentAMI string          `json:"currentAmi"`
	LatestAMI  string          `json:"latestAmi"`
	AMIStatus  types.AMIStatus `json:"amiStatus"`
	// AMILookupError is set when the latest recommended AMI could not be
	// resolved; see NodegroupSummary.AMILookupError.
	AMILookupError string `json:"amiLookupError,omitempty" yaml:"amiLookupError,omitempty"`
	// amiLookupErr is the unflattened lookup error, for LatestAMILookupErr.
	amiLookupErr error

	Scaling ScalingConfig        `json:"scaling"`
	Health  *health.HealthStatus `json:"health,omitempty"`

	Instances []InstanceDetails `json:"instances"`
	Workloads WorkloadInfo      `json:"workloads"`
}

// LatestAMILookupErr returns the error from resolving the latest recommended
// AMI, or nil when the lookup succeeded or was not needed.
func (d *NodegroupDetails) LatestAMILookupErr() error {
	return d.amiLookupErr
}
