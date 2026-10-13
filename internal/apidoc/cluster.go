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
	// Nodegroups are the managed nodegroups, in a document that reads
	// them.
	Nodegroups []Nodegroup `json:"nodegroups,omitzero"`
	// Addons are the installed add-ons, in a document that reads them.
	Addons []Addon `json:"addons,omitzero"`
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
	VPCID            string   `json:"vpcId"`
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
