package nodegroup

import (
	"time"

	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/types"
)

// ListOptions controls nodegroup listing behavior
type ListOptions struct {
	ShowHealth      bool              `json:"showHealth"`
	ShowCosts       bool              `json:"showCosts"`
	ShowUtilization bool              `json:"showUtilization"`
	ShowInstances   bool              `json:"showInstances"`
	Filters         map[string]string `json:"filters"`
	Timeframe       string            `json:"timeframe"`
}

// DescribeOptions controls describe behavior for nodegroups
type DescribeOptions struct {
	ShowInstances    bool   `json:"showInstances"`
	ShowUtilization  bool   `json:"showUtilization"`
	ShowWorkloads    bool   `json:"showWorkloads"`
	ShowCosts        bool   `json:"showCosts"`
	ShowOptimization bool   `json:"showOptimization"`
	Timeframe        string `json:"timeframe"`
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

// UtilizationData represents a simple utilization snapshot/trend
type UtilizationData struct {
	Current float64 `json:"current"`
	Average float64 `json:"average"`
	Peak    float64 `json:"peak"`
	Trend   string  `json:"trend"` // increasing, stable, decreasing
}

// UtilizationMetrics groups major resource utilization metrics
type UtilizationMetrics struct {
	CPU       UtilizationData `json:"cpu"`
	Memory    UtilizationData `json:"memory"`
	Network   UtilizationData `json:"network"`
	Storage   UtilizationData `json:"storage"`
	TimeRange string          `json:"timeRange"`
}

// CostBreakdown is a placeholder for future detailed cost components
type CostBreakdown struct{}

// CostAnalysis captures high-level cost data and potential
type CostAnalysis struct {
	CurrentMonthlyCost    float64       `json:"currentMonthlyCost"`
	ProjectedMonthlyCost  float64       `json:"projectedMonthlyCost"`
	CostPerNode           float64       `json:"costPerNode"`
	CostBreakdown         CostBreakdown `json:"costBreakdown"`
	OptimizationPotential float64       `json:"optimizationPotential"`
}

// InstanceDetails describes an instance in a nodegroup (placeholder for now)
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

// Recommendation proposes an optimization or action
type Recommendation struct {
	Type            string  `json:"type"`     // right-size, spot-integration, scaling
	Priority        string  `json:"priority"` // high, medium, low
	Impact          string  `json:"impact"`   // cost, performance, reliability
	Description     string  `json:"description"`
	Implementation  string  `json:"implementation"`
	ExpectedSavings float64 `json:"expectedSavings"`
	RiskLevel       string  `json:"riskLevel"`
}

// RecommendationOptions controls recommendation analysis
type RecommendationOptions struct {
	Nodegroup               string `json:"nodegroup"`
	CostOptimization        bool   `json:"costOptimization"`
	PerformanceOptimization bool   `json:"performanceOptimization"`
	SpotAnalysis            bool   `json:"spotAnalysis"`
	RightSizing             bool   `json:"rightSizing"`
	Timeframe               string `json:"timeframe"` // 7d, 30d, 90d
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
	// Optional enrichments for list output
	Metrics SummaryMetrics `json:"metrics,omitempty"`
	Cost    SummaryCost    `json:"cost,omitempty"`
}

// SummaryMetrics contains lightweight metrics for list views
type SummaryMetrics struct {
	CPU float64 `json:"cpu"` // percent 0-100
}

// SummaryCost contains lightweight cost info for list views
type SummaryCost struct {
	Monthly float64 `json:"monthly"`
}

// NodegroupDetails extends summary with health, cost, and optional instance/workload details
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

	Scaling     ScalingConfig        `json:"scaling"`
	Health      *health.HealthStatus `json:"health,omitempty"`
	Utilization UtilizationMetrics   `json:"utilization"`
	Cost        CostAnalysis         `json:"costAnalysis"`

	Instances []InstanceDetails `json:"instances"`
	Workloads WorkloadInfo      `json:"workloads"`

	Recommendations []Recommendation `json:"recommendations,omitempty"`
}
