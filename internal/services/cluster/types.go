package cluster

import (
	"time"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/services/status"
)

// ClusterDetails contains comprehensive cluster information
type ClusterDetails struct {
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
	// SupportType is the cluster's upgrade policy (STANDARD or EXTENDED). With
	// STANDARD, EKS auto-upgrades the cluster at the end of standard support.
	SupportType string `json:"supportType,omitempty" yaml:"supportType,omitempty"`

	// Health is the pre-flight health verdict, with --show-health.
	Health *health.HealthSummary `json:"health,omitempty" yaml:"health,omitempty"`

	// HealthIssues are AWS-reported control-plane health issues (DescribeCluster's
	// Health.Issues) — degraded resources, IAM/permission failures, etc. Distinct
	// from the computed Health summary above. Always populated when present, even
	// without ShowHealth.
	HealthIssues []HealthIssue `json:"healthIssues,omitempty" yaml:"healthIssues,omitempty"`

	// Networking is the cluster's VPC, subnets, security groups, and
	// endpoint access.
	Networking NetworkingInfo `json:"networking" yaml:"networking"`

	// Security is the cluster's encryption, IAM role, logging, and deletion
	// protection.
	Security SecurityInfo `json:"security" yaml:"security"`

	// Addons are the installed add-ons. Failures says when it is null.
	Addons []AddonInfo `json:"addons" yaml:"addons"`
	// Nodegroups are the managed nodegroups. Failures says when it is null.
	Nodegroups []NodegroupSummary `json:"nodegroups" yaml:"nodegroups"`

	// Tags are the cluster's AWS tags.
	Tags map[string]string `json:"tags" yaml:"tags"`

	// Failures are the parts of the cluster that could not be read: the
	// add-on or nodegroup list, or one add-on or nodegroup. Addons and
	// Nodegroups are null when not requested or not collected, and [] when
	// collected and empty, so a failure never reads as "none". [] when
	// everything was read.
	Failures diag.List `json:"failures" yaml:"failures"`
}

// DocumentKind is ClusterDescription.
func (ClusterDetails) DocumentKind() apidoc.Kind { return apidoc.KindClusterDescription }

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
	// Incomplete is true when some of the row's data could not be read (the
	// cluster, its nodegroup list, or a nodegroup). Its failures are in the
	// ClusterList document's "failures", identified by cluster (or, for the
	// cluster itself, name).
	Incomplete bool `json:"incomplete,omitempty" yaml:"incomplete,omitempty"`
	// Failures are the row's failures. The command lists them in the
	// ClusterList document, not on the row.
	Failures []diag.Failure `json:"-" yaml:"-"`
}

// addFailure records f against the row and marks it incomplete.
func (s *ClusterSummary) addFailure(f diag.Failure) {
	s.Failures = append(s.Failures, f)
	s.Incomplete = true
}

// ClusterList is the `cluster list -o json|yaml` document.
type ClusterList struct {
	Clusters []ClusterSummary `json:"clusters" yaml:"clusters"`
	Count    int              `json:"count" yaml:"count"`
	// Failures are the regions that could not be listed and the parts of
	// cluster rows that could not be read. [] when everything was read.
	Failures diag.List `json:"failures" yaml:"failures"`
}

// DocumentKind is ClusterList.
func (ClusterList) DocumentKind() apidoc.Kind { return apidoc.KindClusterList }

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

// AddonHealth is an add-on's health in `cluster describe`, derived from its
// EKS status.
type AddonHealth string

// The add-on health values of `cluster describe`.
const (
	// AddonHealthy: the add-on is ACTIVE.
	AddonHealthy AddonHealth = "Healthy"
	// AddonIssues: the add-on is DEGRADED.
	AddonIssues AddonHealth = "Issues"
	// AddonFailed: a create, update, or delete of the add-on failed.
	AddonFailed AddonHealth = "Failed"
	// AddonUpdating: the add-on is CREATING, UPDATING, or DELETING.
	AddonUpdating AddonHealth = "Updating"
	// AddonHealthUnknown: any other status.
	AddonHealthUnknown AddonHealth = "Unknown"
)

// EnumValues lists every AddonHealth.
func (AddonHealth) EnumValues() []string {
	return []string{string(AddonHealthy), string(AddonIssues), string(AddonFailed), string(AddonUpdating), string(AddonHealthUnknown)}
}

// AddonInfo contains EKS add-on information
type AddonInfo struct {
	Name    string      `json:"name" yaml:"name"`
	Version string      `json:"version" yaml:"version"`
	Status  string      `json:"status" yaml:"status"`
	Health  AddonHealth `json:"health,omitempty" yaml:"health,omitempty"`
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
