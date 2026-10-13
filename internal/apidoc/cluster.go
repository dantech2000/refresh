package apidoc

import "time"

// Cluster is one EKS cluster. A key is left out when its data was not
// read: a row that could not be read at all has its name, its region, and
// incomplete.
type Cluster struct {
	Name   string `json:"name"`
	Region string `json:"region"`
	ARN    string `json:"arn,omitzero"`
	// Status is the EKS status, such as ACTIVE or UPDATING.
	Status string `json:"status,omitzero"`
	// Version is the Kubernetes minor version, such as "1.33".
	Version         string    `json:"version,omitzero"`
	PlatformVersion string    `json:"platformVersion,omitzero"`
	Endpoint        string    `json:"endpoint,omitzero"`
	CreatedAt       time.Time `json:"createdAt,omitzero"`
	// UpgradePolicy is the EKS support type, STANDARD or EXTENDED.
	UpgradePolicy string            `json:"upgradePolicy,omitzero"`
	Tags          map[string]string `json:"tags,omitzero"`
	// HealthIssues are the control-plane issues that EKS reports.
	HealthIssues []Issue   `json:"healthIssues,omitzero"`
	Network      *Network  `json:"network,omitzero"`
	Security     *Security `json:"security,omitzero"`
	// Support is the support window of the Kubernetes version, in a
	// document that reads it.
	Support *Support `json:"support,omitzero"`
	// Compute says how the cluster runs its nodes, in a document that
	// reads it. It is left out when that could not be found out.
	Compute ComputeType `json:"compute,omitzero"`
	// Nodegroups are the managed nodegroups, in a document that reads
	// them.
	Nodegroups []Nodegroup `json:"nodegroups,omitzero"`
	// Addons are the installed add-ons, in a document that reads them.
	Addons []Addon `json:"addons,omitzero"`
	// Health is the result of the health checks, in a document that runs
	// them. The Kubernetes checks run with --check-readiness.
	Health *Health `json:"health,omitzero"`
	// Incomplete is true when part of the row could not be read. The
	// failures of the row are in the failures list of the document.
	Incomplete bool `json:"incomplete,omitzero"`
}

// Issue is a health issue that EKS reports for a resource.
type Issue struct {
	Code        string   `json:"code"`
	Message     string   `json:"message"`
	ResourceIDs []string `json:"resourceIds"`
}

// Network is the VPC configuration of a cluster.
type Network struct {
	VPCID string `json:"vpcId"`
	// VPCCIDR is the CIDR block of the VPC, in a document that reads it.
	VPCCIDR          string   `json:"vpcCidr,omitzero"`
	SubnetIDs        []string `json:"subnetIds"`
	SecurityGroupIDs []string `json:"securityGroupIds"`
	// PublicAccess and PrivateAccess say how the API server endpoint can
	// be reached.
	PublicAccess      bool     `json:"publicAccess"`
	PrivateAccess     bool     `json:"privateAccess"`
	PublicAccessCIDRs []string `json:"publicAccessCidrs"`
}

// Security is the security configuration of a cluster.
type Security struct {
	RoleARN string `json:"roleArn"`
	// KMSKeyARN is the key that encrypts secrets. It is left out when
	// secrets are not encrypted with a KMS key.
	KMSKeyARN string `json:"kmsKeyArn,omitzero"`
	// LogTypes are the enabled control-plane log types.
	LogTypes           []string `json:"logTypes"`
	DeletionProtection bool     `json:"deletionProtection"`
}

// ClusterList is the document of "refresh cluster list".
type ClusterList struct {
	Clusters []Cluster `json:"clusters"`
	// SkippedRegions are the regions of a default sweep that are closed to
	// these credentials. They are not failures. The key is left out when
	// the regions were named.
	SkippedRegions []string `json:"skippedRegions,omitzero"`
	Failures       Failures `json:"failures"`
}

// Kind implements Document.
func (ClusterList) Kind() Kind { return KindClusterList }

// SupportTier is where the Kubernetes version of a cluster is in its EKS
// support window.
type SupportTier string

// The support tiers.
const (
	// SupportStandard: in standard support.
	SupportStandard SupportTier = "Standard"
	// SupportExtended: in extended support, which costs more.
	SupportExtended SupportTier = "Extended"
	// SupportUnsupported: past the end of extended support.
	SupportUnsupported SupportTier = "Unsupported"
	// SupportUnknown: the dates are not known.
	SupportUnknown SupportTier = "Unknown"
)

// Values implements Enum.
func (SupportTier) Values() []string {
	return []string{string(SupportStandard), string(SupportExtended), string(SupportUnsupported), string(SupportUnknown)}
}

// Support is the support window of the Kubernetes version of a cluster.
type Support struct {
	Tier          SupportTier `json:"tier"`
	StandardUntil time.Time   `json:"standardUntil,omitzero"`
	ExtendedUntil time.Time   `json:"extendedUntil,omitzero"`
	// DaysRemaining is the days until the tier ends, with a part of a day
	// as a whole day. It is left out when no window is open.
	DaysRemaining int `json:"daysRemaining,omitzero"`
	// AutoUpgrade is true when the upgrade policy of the cluster is
	// STANDARD: EKS upgrades it at the end of standard support, and it
	// never enters extended support.
	AutoUpgrade bool `json:"autoUpgrade"`
	// BuiltIn is true when the dates are the ones that refresh ships
	// with, because EKS did not report them.
	BuiltIn bool `json:"builtIn"`
}

// ComputeType says how a cluster runs its nodes.
type ComputeType string

// The compute types.
const (
	// ComputeManagedNodegroups: one or more managed nodegroups.
	ComputeManagedNodegroups ComputeType = "ManagedNodegroups"
	// ComputeAutoMode: EKS Auto Mode, where AWS manages the nodes.
	ComputeAutoMode ComputeType = "AutoMode"
	// ComputeKarpenter: no managed nodegroup, and instances that
	// Karpenter started.
	ComputeKarpenter ComputeType = "Karpenter"
	// ComputeNone: no managed nodegroup and nothing else that refresh
	// looks for.
	ComputeNone ComputeType = "None"
)

// Values implements Enum.
func (ComputeType) Values() []string {
	return []string{string(ComputeManagedNodegroups), string(ComputeAutoMode), string(ComputeKarpenter), string(ComputeNone)}
}

// ClusterDescription is the document of "refresh cluster describe".
type ClusterDescription struct {
	Cluster  Cluster  `json:"cluster"`
	Failures Failures `json:"failures"`
}

// Kind implements Document.
func (ClusterDescription) Kind() Kind { return KindClusterDescription }

// FleetStatus is the document of "refresh status".
type FleetStatus struct {
	Clusters []Cluster `json:"clusters"`
	// SkippedRegions are the regions of a default sweep that are closed to
	// these credentials. They are not failures. The key is left out when
	// the regions were named.
	SkippedRegions []string `json:"skippedRegions,omitzero"`
	Failures       Failures `json:"failures"`
}

// Kind implements Document.
func (FleetStatus) Kind() Kind { return KindFleetStatus }
