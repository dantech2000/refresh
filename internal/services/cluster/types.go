package cluster

import (
	"time"

	"github.com/dantech2000/refresh/internal/health"
)

// ClusterDetails contains comprehensive cluster information
type ClusterDetails struct {
	// Basic cluster info
	Name            string    `json:"name"`
	Status          string    `json:"status"`
	Version         string    `json:"version"`
	PlatformVersion string    `json:"platformVersion"`
	Endpoint        string    `json:"endpoint"`
	CreatedAt       time.Time `json:"createdAt"`
	Region          string    `json:"region"`

	// Health information (integration with existing health framework)
	Health *health.HealthSummary `json:"health,omitempty"`

	// Networking details
	Networking NetworkingInfo `json:"networking"`

	// Security configuration
	Security SecurityInfo `json:"security"`

	// Add-ons and nodegroups
	Addons     []AddonInfo        `json:"addons"`
	Nodegroups []NodegroupSummary `json:"nodegroups"`

	// Operational metadata
	Tags map[string]string `json:"tags"`
}

// ClusterSummary is used for list operations
type ClusterSummary struct {
	Name      string                `json:"name"`
	Status    string                `json:"status"`
	Version   string                `json:"version"`
	Region    string                `json:"region"`
	Health    *health.HealthSummary `json:"health,omitempty"`
	NodeCount NodeCountInfo         `json:"nodeCount"`
	CreatedAt time.Time             `json:"createdAt"`
	Tags      map[string]string     `json:"tags,omitempty"`
}

// NetworkingInfo contains VPC and networking details
type NetworkingInfo struct {
	VpcId            string             `json:"vpcId"`
	VpcCidr          string             `json:"vpcCidr,omitempty"`
	SubnetIds        []string           `json:"subnetIds"`
	SecurityGroupIds []string           `json:"securityGroupIds"`
	EndpointAccess   EndpointAccessInfo `json:"endpointAccess"`
}

// EndpointAccessInfo describes cluster endpoint configuration
type EndpointAccessInfo struct {
	PrivateAccess bool     `json:"privateAccess"`
	PublicAccess  bool     `json:"publicAccess"`
	PublicCidrs   []string `json:"publicCidrs,omitempty"`
}

// SecurityInfo contains cluster security configuration
type SecurityInfo struct {
	EncryptionEnabled bool     `json:"encryptionEnabled"`
	KmsKeyArn         string   `json:"kmsKeyArn,omitempty"`
	ServiceRoleArn    string   `json:"serviceRoleArn"`
	LoggingEnabled    []string `json:"loggingEnabled"`
}

// AddonInfo contains EKS add-on information
type AddonInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Status  string `json:"status"`
	Health  string `json:"health,omitempty"`
}

// NodegroupSummary contains basic nodegroup information
type NodegroupSummary struct {
	Name         string `json:"name"`
	Status       string `json:"status"`
	InstanceType string `json:"instanceType"`
	DesiredSize  int32  `json:"desiredSize"`
	ReadyNodes   int32  `json:"readyNodes"`
}

// NodeCountInfo aggregates node information across nodegroups
type NodeCountInfo struct {
	Ready int32 `json:"ready"`
	Total int32 `json:"total"`
}

// ClusterComparison contains results of comparing multiple clusters
type ClusterComparison struct {
	Clusters    []ClusterDetails  `json:"clusters"`
	Differences []Difference      `json:"differences"`
	Summary     ComparisonSummary `json:"summary"`
}

// Difference represents a configuration difference between clusters
type Difference struct {
	Field       string      `json:"field"`
	Description string      `json:"description"`
	Values      []ValuePair `json:"values"`
	Severity    string      `json:"severity"` // info, warning, critical
}

// ValuePair represents different values for the same field
type ValuePair struct {
	ClusterName string      `json:"clusterName"`
	Value       interface{} `json:"value"`
}

// ComparisonSummary provides overview of cluster comparison
type ComparisonSummary struct {
	TotalDifferences      int  `json:"totalDifferences"`
	CriticalDifferences   int  `json:"criticalDifferences"`
	WarningDifferences    int  `json:"warningDifferences"`
	InfoDifferences       int  `json:"infoDifferences"`
	ClustersAreEquivalent bool `json:"clustersAreEquivalent"`
}

// DescribeOptions controls what information to include in describe operations
type DescribeOptions struct {
	ShowHealth    bool `json:"showHealth"`
	ShowSecurity  bool `json:"showSecurity"`
	ShowCosts     bool `json:"showCosts"`
	IncludeAddons bool `json:"includeAddons"`
	Detailed      bool `json:"detailed"`
}

// ListOptions controls cluster listing behavior
type ListOptions struct {
	Regions    []string          `json:"regions"`
	ShowHealth bool              `json:"showHealth"`
	ShowCosts  bool              `json:"showCosts"`
	Filters    map[string]string `json:"filters"`
	AllRegions bool              `json:"allRegions"`
}

// CompareOptions controls cluster comparison behavior
type CompareOptions struct {
	ShowDifferencesOnly bool     `json:"showDifferencesOnly"`
	Include             []string `json:"include"` // networking, security, addons, versions
	Format              string   `json:"format"`
}
