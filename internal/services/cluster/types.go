package cluster

import (
	"time"

	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/services/status"
)

// ClusterDetails contains comprehensive cluster information
type ClusterDetails struct {
	// Basic cluster info
	Name            string    `json:"name" yaml:"name"`
	Status          string    `json:"status" yaml:"status"`
	Version         string    `json:"version" yaml:"version"`
	PlatformVersion string    `json:"platformVersion" yaml:"platformVersion"`
	Endpoint        string    `json:"endpoint" yaml:"endpoint"`
	CreatedAt       time.Time `json:"createdAt" yaml:"createdAt"`
	Region          string    `json:"region" yaml:"region"`

	// Support is the EKS version support posture (tier + days remaining),
	// resolved via the shared status resolver. Populated by the command layer.
	Support *status.SupportPosture `json:"support,omitempty" yaml:"support,omitempty"`

	// Health information (integration with existing health framework)
	Health *health.HealthSummary `json:"health,omitempty" yaml:"health,omitempty"`

	// HealthIssues are AWS-reported control-plane health issues (DescribeCluster's
	// Health.Issues) — degraded resources, IAM/permission failures, etc. Distinct
	// from the computed Health summary above. Always populated when present, even
	// without ShowHealth.
	HealthIssues []HealthIssue `json:"healthIssues,omitempty" yaml:"healthIssues,omitempty"`

	// Networking details
	Networking NetworkingInfo `json:"networking" yaml:"networking"`

	// Security configuration
	Security SecurityInfo `json:"security" yaml:"security"`

	// Add-ons and nodegroups
	Addons     []AddonInfo        `json:"addons" yaml:"addons"`
	Nodegroups []NodegroupSummary `json:"nodegroups" yaml:"nodegroups"`

	// Operational metadata
	Tags map[string]string `json:"tags" yaml:"tags"`

	// Warnings are partial failures (an add-on or nodegroup that could not be
	// read). The command layer reports them on stderr; they are not part of
	// the machine output.
	Warnings []string `json:"-" yaml:"-"`
}

// ClusterSummary is used for list operations
type ClusterSummary struct {
	Name      string                `json:"name" yaml:"name"`
	Status    string                `json:"status" yaml:"status"`
	Version   string                `json:"version" yaml:"version"`
	Region    string                `json:"region" yaml:"region"`
	Health    *health.HealthSummary `json:"health,omitempty" yaml:"health,omitempty"`
	NodeCount NodeCountInfo         `json:"nodeCount" yaml:"nodeCount"`
	CreatedAt time.Time             `json:"createdAt" yaml:"createdAt"`
	Tags      map[string]string     `json:"tags,omitempty" yaml:"tags,omitempty"`
	// Warnings are partial failures for this row (the cluster or its
	// nodegroups could not be read). The command layer reports them on
	// stderr; they are not part of the machine output.
	Warnings []string `json:"-" yaml:"-"`
}

// HealthIssue is one AWS-reported health issue on a cluster/nodegroup/addon —
// the common shape behind EKS's ClusterIssue / NodegroupIssue / AddonIssue.
type HealthIssue struct {
	Code        string   `json:"code" yaml:"code"`
	Message     string   `json:"message" yaml:"message"`
	ResourceIDs []string `json:"resourceIds,omitempty" yaml:"resourceIds,omitempty"`
}

// NetworkingInfo contains VPC and networking details
type NetworkingInfo struct {
	VpcID            string             `json:"vpcId" yaml:"vpcId"`
	VpcCidr          string             `json:"vpcCidr,omitempty" yaml:"vpcCidr,omitempty"`
	SubnetIDs        []string           `json:"subnetIds" yaml:"subnetIds"`
	SecurityGroupIDs []string           `json:"securityGroupIds" yaml:"securityGroupIds"`
	EndpointAccess   EndpointAccessInfo `json:"endpointAccess" yaml:"endpointAccess"`
}

// EndpointAccessInfo describes cluster endpoint configuration
type EndpointAccessInfo struct {
	PrivateAccess bool     `json:"privateAccess" yaml:"privateAccess"`
	PublicAccess  bool     `json:"publicAccess" yaml:"publicAccess"`
	PublicCidrs   []string `json:"publicCidrs,omitempty" yaml:"publicCidrs,omitempty"`
}

// SecurityInfo contains cluster security configuration
type SecurityInfo struct {
	EncryptionEnabled  bool     `json:"encryptionEnabled" yaml:"encryptionEnabled"`
	KmsKeyArn          string   `json:"kmsKeyArn,omitempty" yaml:"kmsKeyArn,omitempty"`
	ServiceRoleArn     string   `json:"serviceRoleArn" yaml:"serviceRoleArn"`
	LoggingEnabled     []string `json:"loggingEnabled" yaml:"loggingEnabled"`
	DeletionProtection bool     `json:"deletionProtection" yaml:"deletionProtection"`
}

// AddonInfo contains EKS add-on information
type AddonInfo struct {
	Name    string `json:"name" yaml:"name"`
	Version string `json:"version" yaml:"version"`
	Status  string `json:"status" yaml:"status"`
	Health  string `json:"health,omitempty" yaml:"health,omitempty"`
}

// NodegroupSummary contains basic nodegroup information
type NodegroupSummary struct {
	Name         string `json:"name" yaml:"name"`
	Status       string `json:"status" yaml:"status"`
	InstanceType string `json:"instanceType" yaml:"instanceType"`
	DesiredSize  int32  `json:"desiredSize" yaml:"desiredSize"`
	// ReadyNodes is valid only when ReadyKnown is true (a measured Kubernetes
	// Ready=True count). Otherwise readiness was not measured. (REF-130)
	ReadyNodes int32 `json:"readyNodes" yaml:"readyNodes"`
	ReadyKnown bool  `json:"readyKnown" yaml:"readyKnown"`
}

// NodeCountInfo aggregates node information across nodegroups. Ready is a
// measured count only when ReadyKnown is true; otherwise only Total (desired
// capacity) is meaningful. (REF-130)
type NodeCountInfo struct {
	Ready      int32 `json:"ready" yaml:"ready"`
	Total      int32 `json:"total" yaml:"total"`
	ReadyKnown bool  `json:"readyKnown" yaml:"readyKnown"`
}

// DescribeOptions controls what information to include in describe operations
type DescribeOptions struct {
	ShowHealth    bool `json:"showHealth"`
	ShowSecurity  bool `json:"showSecurity"`
	IncludeAddons bool `json:"includeAddons"`
	Detailed      bool `json:"detailed"`
}

// ListOptions controls cluster listing behavior
type ListOptions struct {
	Regions        []string          `json:"regions"`
	ShowHealth     bool              `json:"showHealth"`
	Filters        map[string]string `json:"filters"`
	AllRegions     bool              `json:"allRegions"`
	MaxConcurrency int               `json:"maxConcurrency"`
}
