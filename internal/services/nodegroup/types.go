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
	// Force skips the CheckPDBs scale-down refusal. The caller is expected
	// to have run CheckScaleDownPDBs itself to warn about the blockers.
	Force bool `json:"force"`
}

// ScalingConfig models the EKS managed nodegroup scaling configuration
type ScalingConfig struct {
	DesiredSize int32 `json:"desiredSize" yaml:"desiredSize"`
	MinSize     int32 `json:"minSize" yaml:"minSize"`
	MaxSize     int32 `json:"maxSize" yaml:"maxSize"`
	AutoScaling bool  `json:"autoScaling" yaml:"autoScaling"`
}

// InstanceDetails describes an EC2 instance backing a nodegroup.
type InstanceDetails struct {
	InstanceID   string    `json:"instanceId" yaml:"instanceId"`
	InstanceType string    `json:"instanceType" yaml:"instanceType"`
	LaunchTime   time.Time `json:"launchTime" yaml:"launchTime"`
	Lifecycle    string    `json:"lifecycle" yaml:"lifecycle"` // on-demand, spot
	State        string    `json:"state" yaml:"state"`
	AZ           string    `json:"availabilityZone" yaml:"availabilityZone"`
}

// WorkloadInfo summarizes pods/workloads placed on a nodegroup
type WorkloadInfo struct {
	TotalPods     int    `json:"totalPods" yaml:"totalPods"`
	CriticalPods  int    `json:"criticalPods" yaml:"criticalPods"`
	PodDisruption string `json:"podDisruption" yaml:"podDisruption"` // summarized for now
}

// NodegroupSummary contains basic nodegroup info for listings
type NodegroupSummary struct {
	Name         string `json:"name" yaml:"name"`
	Status       string `json:"status" yaml:"status"`
	InstanceType string `json:"instanceType" yaml:"instanceType"`
	DesiredSize  int32  `json:"desiredSize" yaml:"desiredSize"`
	// ReadyNodes is a measured count of Kubernetes Ready=True nodes, valid only
	// when ReadyKnown is true. When ReadyKnown is false the readiness was not
	// measured (no --check-readiness, or the cluster API was unreachable) and
	// ReadyNodes must not be read as a real count. (REF-130)
	ReadyNodes int32 `json:"readyNodes" yaml:"readyNodes"`
	ReadyKnown bool  `json:"readyKnown" yaml:"readyKnown"`
	// AMI information - core functionality of refresh tool
	CurrentAMI string          `json:"currentAmi" yaml:"currentAmi"`
	AMIStatus  types.AMIStatus `json:"amiStatus" yaml:"amiStatus"`
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
	Name         string `json:"name" yaml:"name"`
	Status       string `json:"status" yaml:"status"`
	InstanceType string `json:"instanceType" yaml:"instanceType"`
	AmiType      string `json:"amiType" yaml:"amiType"`
	CapacityType string `json:"capacityType" yaml:"capacityType"` // ON_DEMAND, SPOT

	// AMI information - core functionality of refresh tool
	CurrentAMI string          `json:"currentAmi" yaml:"currentAmi"`
	LatestAMI  string          `json:"latestAmi" yaml:"latestAmi"`
	AMIStatus  types.AMIStatus `json:"amiStatus" yaml:"amiStatus"`
	// AMILookupError is set when the latest recommended AMI could not be
	// resolved; see NodegroupSummary.AMILookupError.
	AMILookupError string `json:"amiLookupError,omitempty" yaml:"amiLookupError,omitempty"`
	// amiLookupErr is the unflattened lookup error, for LatestAMILookupErr.
	amiLookupErr error

	Scaling ScalingConfig        `json:"scaling" yaml:"scaling"`
	Health  *health.HealthStatus `json:"health,omitempty" yaml:"health,omitempty"`

	Instances []InstanceDetails `json:"instances" yaml:"instances"`
	Workloads WorkloadInfo      `json:"workloads" yaml:"workloads"`
}

// LatestAMILookupErr returns the error from resolving the latest recommended
// AMI, or nil when the lookup succeeded or was not needed.
func (d *NodegroupDetails) LatestAMILookupErr() error {
	return d.amiLookupErr
}
