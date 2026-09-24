package nodegroup

import (
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/types"
)

// ListOptions controls nodegroup listing behavior
type ListOptions struct {
	Filters map[string]string `json:"filters"`
}

// DescribeOptions controls describe behavior for nodegroups
type DescribeOptions struct {
	ShowInstances bool `json:"showInstances" yaml:"showInstances"`
	ShowWorkloads bool `json:"showWorkloads" yaml:"showWorkloads"`
	// KubeClient reads workload placement for ShowWorkloads. It must point
	// at the nodegroup's cluster; nil marks the workloads unavailable.
	KubeClient kubernetes.Interface `json:"-" yaml:"-"`
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
	// AMILookupFailure is set when the latest recommended AMI could not be
	// resolved (for example, no ssm:GetParameter permission, or throttling).
	// AMIStatus is then Unknown because of the failure, not because nothing
	// is stale. It is advisory: `nodegroup list` does not count it as
	// incomplete data, while `status` does (see docs/concepts/output.md).
	AMILookupFailure *diag.Failure `json:"amiLookupFailure,omitempty" yaml:"amiLookupFailure,omitempty"`
}

// ListResult is the full outcome of a nodegroup listing.
type ListResult struct {
	// Summaries are the nodegroups that were described (and matched the
	// filters). A summary whose latest-AMI lookup failed carries the failure
	// in AMILookupFailure.
	Summaries []NodegroupSummary
	// Failures has one entry per listed nodegroup left out of Summaries: it
	// could not be described, or the listing stopped before it was reached.
	Failures []diag.Failure
}

// NodegroupList is the `nodegroup list -o json|yaml` document.
type NodegroupList struct {
	Cluster    string             `json:"cluster" yaml:"cluster"`
	Nodegroups []NodegroupSummary `json:"nodegroups" yaml:"nodegroups"`
	Count      int                `json:"count" yaml:"count"`
	// Failures are the nodegroups that could not be described; the list
	// leaves them out. [] when every nodegroup was read.
	Failures diag.List `json:"failures" yaml:"failures"`
}

// NodegroupDetails is the `nodegroup describe` result: AMI status, scaling, and
// optional instance/workload details.
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
	// AMILookupFailure is set when the latest recommended AMI could not be
	// resolved; see NodegroupSummary.AMILookupFailure.
	AMILookupFailure *diag.Failure `json:"amiLookupFailure,omitempty" yaml:"amiLookupFailure,omitempty"`

	Scaling ScalingConfig `json:"scaling" yaml:"scaling"`

	Instances []InstanceDetails `json:"instances" yaml:"instances"`
	Workloads WorkloadInfo      `json:"workloads" yaml:"workloads"`

	// Failures is always [] today: describe either reads the nodegroup or
	// fails. It is here so every document carries the same key.
	Failures diag.List `json:"failures" yaml:"failures"`
}
