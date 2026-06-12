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
	ReadyNodes   int32  `json:"readyNodes"`
	// AMI information - core functionality of refresh tool
	CurrentAMI string          `json:"currentAmi"`
	AMIStatus  types.AMIStatus `json:"amiStatus"`
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

	Scaling ScalingConfig        `json:"scaling"`
	Health  *health.HealthStatus `json:"health,omitempty"`

	Instances []InstanceDetails `json:"instances"`
	Workloads WorkloadInfo      `json:"workloads"`
}
