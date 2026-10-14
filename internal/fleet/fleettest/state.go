package fleettest

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// State is everything in a World. A test reads and changes it through
// World.View and World.Edit.
//
// After each Edit, empty fields that AWS never leaves empty get a default: a
// status becomes ACTIVE, a nodegroup with no sizes gets desired 2, min 1,
// max 3, and so on. The field comments name each default.
//
// Everything that lives in a region has a Region field. An empty Region
// means the item answers in every region, which keeps a one-region test
// short.
type State struct {
	// Account, CallerARN, and UserID answer STS GetCallerIdentity. They
	// default to account 123456789012 and a user named fake.
	Account   string
	CallerARN string
	UserID    string

	Clusters []*Cluster
	// AddonVersions is the add-on catalog, newest version first per add-on.
	AddonVersions []AddonVersion
	// ClusterVersions are the Kubernetes versions EKS offers.
	ClusterVersions []ClusterVersion
	// Parameters are SSM parameters. See State.Parameter for the
	// EKS-optimized AMI parameters that need no entry.
	Parameters []Parameter

	Images                 []Image
	Instances              []Instance
	LaunchTemplateVersions []LaunchTemplateVersion
	Subnets                []Subnet
	VPCs                   []VPC
	// Offerings are the instance types offered per availability zone.
	Offerings []Offering
	// InstanceTypes is the EC2 instance-type catalog. Nil becomes
	// DefaultInstanceTypes. Set an empty, non-nil slice for none.
	InstanceTypes     []InstanceType
	AutoScalingGroups []AutoScalingGroup
	// ScalingActivities are listed newest first, as Auto Scaling does.
	ScalingActivities []ScalingActivity
	Metrics           []Metric
	Quotas            []Quota

	// DefaultUpdate scripts every update whose resource has no UpdateScripts
	// left. The zero value succeeds at once. DefaultNodegroupUpdate and
	// DefaultAddonUpdate, when set, script the updates of nodegroups and
	// of add-ons in its place, so a fleet can give a roll, an add-on
	// update, and a control-plane upgrade each its own duration.
	DefaultUpdate          UpdateScript
	DefaultNodegroupUpdate *UpdateScript
	DefaultAddonUpdate     *UpdateScript
	// InsightsRefresh is how long an insights refresh stays IN_PROGRESS.
	InsightsRefresh time.Duration
}

// Cluster is an EKS cluster.
type Cluster struct {
	Name    string
	Version string
	// Region is the only region that lists and answers for the cluster. ""
	// answers in every region.
	Region string
	// Status defaults to ACTIVE.
	Status ResourceStatus
	// PlatformVersion defaults to eks.1.
	PlatformVersion string
	// CreatedAt defaults to the world's time at the Edit that added the
	// cluster.
	CreatedAt time.Time
	RoleARN   string
	// SupportType is the upgrade policy, STANDARD or EXTENDED. "" reports no
	// policy.
	SupportType string
	// AutoMode reports EKS Auto Mode compute as enabled.
	AutoMode bool

	VPCID                 string
	SubnetIDs             []string
	SecurityGroupIDs      []string
	EndpointPublicAccess  bool
	EndpointPrivateAccess bool
	PublicAccessCIDRs     []string
	// LogTypes are the enabled control-plane log types.
	LogTypes []string
	// EncryptionKeyARN, when set, reports secrets encryption with that key.
	EncryptionKeyARN   string
	DeletionProtection bool
	HealthIssues       []Issue
	Tags               map[string]string

	Nodegroups []*Nodegroup
	Addons     []*Addon
	// Insights are the cluster's insights. Nil becomes one PASSING
	// upgrade-readiness insight, as EKS reports for a healthy cluster. Set
	// an empty, non-nil slice for none.
	Insights []Insight
	// Updates are the cluster's updates, oldest first: the seeded history
	// and every update started since.
	Updates []*Update
	// UpdateScripts script the next UpdateClusterVersion calls, one each.
	UpdateScripts []UpdateScript
	// InsightsRefresh is the state of the last insights refresh. The zero
	// value means none was started.
	InsightsRefresh InsightsRefresh

	// NoMetricsServer makes the metrics API answer 404, as a cluster
	// without metrics-server does.
	NoMetricsServer bool
	// Endpoint is the URL of the cluster's Kubernetes API, which
	// DescribeCluster reports. It defaults to a host that does not resolve.
	// A test that serves the cluster's Kubernetes API sets it to that
	// server, because refresh uses a kubeconfig context only when its
	// server is this endpoint.
	Endpoint string
	// Nodes, Pods, PDBs, Deployments, and Events are what the cluster's
	// Kubernetes API holds. The namespaces are those of the pods, the PDBs,
	// and the deployments, plus default and the kube-* ones. A version
	// update of a nodegroup moves its nodes and their pods (see
	// RollScript).
	Nodes       []*Node
	Pods        []*Pod
	PDBs        []PDB
	Deployments []Deployment
	Events      []Event
}

// Nodegroup is a managed nodegroup.
type Nodegroup struct {
	Name    string
	Version string
	// ReleaseVersion defaults to LatestRelease(Version).
	ReleaseVersion string
	// Status defaults to ACTIVE.
	Status ResourceStatus
	// CreatedAt and ModifiedAt default to the world's time at the Edit that
	// added the nodegroup. An update that succeeds sets ModifiedAt.
	CreatedAt  time.Time
	ModifiedAt time.Time
	// AMIType defaults to AL2023_x86_64_STANDARD.
	AMIType string
	// CapacityType defaults to ON_DEMAND.
	CapacityType string
	// InstanceTypes defaults to m5.large.
	InstanceTypes []string
	Subnets       []string
	// Desired, Min, and Max are the scaling config. All zero becomes
	// desired 2, min 1, max 3.
	Desired, Min, Max int32
	// MaxUnavailable and MaxUnavailablePercentage are the update config.
	// Both zero becomes MaxUnavailable 1, as EKS sets it.
	MaxUnavailable, MaxUnavailablePercentage int32
	// UpdateStrategy is DEFAULT or MINIMAL. "" reports none.
	UpdateStrategy string
	// AutoScalingGroups are the names of the nodegroup's groups.
	AutoScalingGroups []string
	LaunchTemplate    *LaunchTemplate
	Labels            map[string]string
	HealthIssues      []Issue
	// UpdateScripts script the next updates of this nodegroup, one each.
	UpdateScripts []UpdateScript
}

// LaunchTemplate names the launch template version of a nodegroup.
type LaunchTemplate struct {
	ID      string
	Version string
}

// Addon is an installed EKS add-on.
type Addon struct {
	Name    string
	Version string
	// Status defaults to ACTIVE.
	Status ResourceStatus
	// CreatedAt and ModifiedAt default to the world's time at the Edit that
	// added the add-on. An update that succeeds sets ModifiedAt.
	CreatedAt             time.Time
	ModifiedAt            time.Time
	ServiceAccountRoleARN string
	ConfigurationValues   string
	HealthIssues          []Issue
	// UpdateScripts script the next UpdateAddon calls, one each.
	UpdateScripts []UpdateScript
}

// Issue is a health issue of a cluster, nodegroup, or add-on.
type Issue struct {
	Code        string
	Message     string
	ResourceIDs []string
}

// Insight is an EKS cluster insight.
type Insight struct {
	ID   string
	Name string
	// Category defaults to UPGRADE_READINESS.
	Category string
	Status   InsightStatus
	Reason   string
	// KubernetesVersion is the version the insight is about. "" means the
	// minor after the cluster's version at the time of the request.
	KubernetesVersion string
	Description       string
	Recommendation    string
	AdditionalInfo    map[string]string
	Resources         []InsightResource
	Deprecations      []Deprecation
}

// InsightResource is a Kubernetes resource an insight names.
type InsightResource struct {
	URI    string
	ARN    string
	Status InsightStatus
	Reason string
}

// Deprecation is one deprecated API an upgrade-readiness insight reports.
type Deprecation struct {
	Usage                          string
	ReplacedWith                   string
	StopServingVersion             string
	StartServingReplacementVersion string
	Clients                        []ClientStat
}

// ClientStat is the recent use of a deprecated API by one client.
type ClientStat struct {
	UserAgent   string
	Requests    int32
	LastRequest time.Time
}

// InsightsRefresh is the state of a cluster's last insights refresh.
type InsightsRefresh struct {
	// Status is "" when none was started.
	Status    RefreshStatus
	StartedAt time.Time
	// EndsAt is when an IN_PROGRESS refresh becomes COMPLETED.
	EndsAt time.Time
	// EndedAt is when it became COMPLETED; zero before that.
	EndedAt time.Time
}

// AddonVersion is one version of an add-on in the catalog.
type AddonVersion struct {
	Region  string
	Addon   string
	Version string
	// ClusterVersions are the Kubernetes versions the build supports.
	ClusterVersions []string
	// Default marks the version EKS installs by default for those
	// Kubernetes versions.
	Default bool
}

// ClusterVersion is a Kubernetes version EKS offers.
type ClusterVersion struct {
	Region  string
	Version string
	// Status defaults to STANDARD_SUPPORT.
	Status VersionStatus
	// EndOfStandardSupport and EndOfExtendedSupport are left out of the
	// answer when zero.
	EndOfStandardSupport time.Time
	EndOfExtendedSupport time.Time
}

// Parameter is an SSM parameter.
type Parameter struct {
	Region string
	Name   string
	Value  string
}

// ResourceStatus is the status of a cluster, a nodegroup, or an add-on. The
// values are the ones EKS defines; the three resources share most of them.
type ResourceStatus string

// The EKS resource statuses the world sets or that tests commonly seed.
const (
	StatusActive       ResourceStatus = "ACTIVE"
	StatusCreating     ResourceStatus = "CREATING"
	StatusUpdating     ResourceStatus = "UPDATING"
	StatusDeleting     ResourceStatus = "DELETING"
	StatusDegraded     ResourceStatus = "DEGRADED"
	StatusFailed       ResourceStatus = "FAILED"
	StatusCreateFailed ResourceStatus = "CREATE_FAILED"
	StatusUpdateFailed ResourceStatus = "UPDATE_FAILED"
)

// InsightStatus is the status of an EKS insight.
type InsightStatus string

// The EKS insight statuses.
const (
	InsightPassing InsightStatus = "PASSING"
	InsightWarning InsightStatus = "WARNING"
	InsightError   InsightStatus = "ERROR"
	InsightUnknown InsightStatus = "UNKNOWN"
)

// RefreshStatus is the status of an insights refresh.
type RefreshStatus string

// The EKS insights refresh statuses.
const (
	RefreshInProgress RefreshStatus = "IN_PROGRESS"
	RefreshCompleted  RefreshStatus = "COMPLETED"
	RefreshFailed     RefreshStatus = "FAILED"
)

// VersionStatus is the support status of a Kubernetes version.
type VersionStatus string

// The EKS version statuses.
const (
	StandardSupport VersionStatus = "STANDARD_SUPPORT"
	ExtendedSupport VersionStatus = "EXTENDED_SUPPORT"
	Unsupported     VersionStatus = "UNSUPPORTED"
)

// Visible reports whether an item with the given Region field answers in
// region.
func Visible(itemRegion, region string) bool {
	return itemRegion == "" || itemRegion == region
}

// LatestRelease is the newest AMI release version the world knows for a
// Kubernetes version. A nodegroup rolled without a release version gets it.
func LatestRelease(version string) string {
	return version + ".0-20260101"
}

// Cluster returns the named cluster if it answers in region, or nil.
func (s *State) Cluster(region, name string) *Cluster {
	for _, c := range s.Clusters {
		if c.Name == name && c.InRegion(region) {
			return c
		}
	}
	return nil
}

// InRegion reports whether the cluster answers in region.
func (c *Cluster) InRegion(region string) bool {
	return Visible(c.Region, region)
}

// Nodegroup returns the named nodegroup, or nil.
func (c *Cluster) Nodegroup(name string) *Nodegroup {
	for _, ng := range c.Nodegroups {
		if ng.Name == name {
			return ng
		}
	}
	return nil
}

// Addon returns the named add-on, or nil.
func (c *Cluster) Addon(name string) *Addon {
	for _, a := range c.Addons {
		if a.Name == name {
			return a
		}
	}
	return nil
}

// Update returns the update with the ID, or nil.
func (c *Cluster) Update(id string) *Update {
	for _, u := range c.Updates {
		if u.ID == id {
			return u
		}
	}
	return nil
}

// NextMinor returns the Kubernetes minor after the cluster's version, or ""
// when the version does not parse.
func (c *Cluster) NextMinor() string {
	major, minor, ok := parseVersion(c.Version)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d.%d", major, minor+1)
}

// Parameter returns the value of the SSM parameter with the name in region.
// A name that is not in Parameters but has a Kubernetes version in its path,
// as the EKS-optimized AMI parameters do, answers a derived value in every
// region: LatestRelease for a release_version or image_version parameter,
// and an image ID such as ami-latest132 for the others.
func (s *State) Parameter(region, name string) (string, bool) {
	for _, p := range s.Parameters {
		if p.Name == name && Visible(p.Region, region) {
			return p.Value, true
		}
	}
	version := ""
	for part := range strings.SplitSeq(name, "/") {
		part = strings.TrimPrefix(part, "aws-k8s-") // Bottlerocket: aws-k8s-1.34[-variant]
		part, _, _ = strings.Cut(part, "-")
		if _, _, ok := parseVersion(part); ok {
			version = part
			break
		}
	}
	if version == "" {
		return "", false
	}
	if strings.HasSuffix(name, "/release_version") || strings.HasSuffix(name, "/image_version") {
		return LatestRelease(version), true
	}
	return LatestImage(version), true
}

// LatestImage is the newest AMI the world knows for a Kubernetes version:
// what the derived SSM parameters recommend, and what a rolled nodegroup
// runs.
func LatestImage(version string) string {
	return "ami-latest" + strings.ReplaceAll(version, ".", "")
}

// parseVersion reads a "major.minor" Kubernetes version.
func parseVersion(v string) (major, minor int, ok bool) {
	a, b, found := strings.Cut(v, ".")
	if !found {
		return 0, 0, false
	}
	major, errMajor := strconv.Atoi(a)
	minor, errMinor := strconv.Atoi(b)
	if errMajor != nil || errMinor != nil || major < 0 || minor < 0 {
		return 0, 0, false
	}
	return major, minor, true
}

// applyDefaults fills the fields that have a documented default.
func (s *State) applyDefaults(now time.Time) {
	if s.Account == "" {
		s.Account = "123456789012"
	}
	if s.CallerARN == "" {
		s.CallerARN = "arn:aws:iam::" + s.Account + ":user/fake"
	}
	if s.UserID == "" {
		s.UserID = "AIDAFAKE"
	}
	for i := range s.ClusterVersions {
		setDefault(&s.ClusterVersions[i].Status, StandardSupport)
	}
	if s.InstanceTypes == nil {
		s.InstanceTypes = DefaultInstanceTypes()
	}
	for _, c := range s.Clusters {
		c.applyDefaults(now)
	}
}

func (c *Cluster) applyDefaults(now time.Time) {
	c.applyKubeDefaults(now)
	setDefault(&c.Status, StatusActive)
	setDefault(&c.PlatformVersion, "eks.1")
	setDefault(&c.CreatedAt, now)
	if c.Insights == nil {
		c.Insights = []Insight{{ID: "insight-" + c.Name + "-skew", Name: "Kubelet version skew", Status: InsightPassing}}
	}
	for i := range c.Insights {
		setDefault(&c.Insights[i].Category, "UPGRADE_READINESS")
	}
	for _, ng := range c.Nodegroups {
		setDefault(&ng.Status, StatusActive)
		setDefault(&ng.CreatedAt, now)
		setDefault(&ng.ModifiedAt, now)
		setDefault(&ng.ReleaseVersion, LatestRelease(ng.Version))
		setDefault(&ng.AMIType, "AL2023_x86_64_STANDARD")
		setDefault(&ng.CapacityType, "ON_DEMAND")
		if len(ng.InstanceTypes) == 0 {
			ng.InstanceTypes = []string{"m5.large"}
		}
		if ng.Desired == 0 && ng.Min == 0 && ng.Max == 0 {
			ng.Desired, ng.Min, ng.Max = 2, 1, 3
		}
		if ng.MaxUnavailable == 0 && ng.MaxUnavailablePercentage == 0 {
			ng.MaxUnavailable = 1
		}
	}
	for _, a := range c.Addons {
		setDefault(&a.Status, StatusActive)
		setDefault(&a.CreatedAt, now)
		setDefault(&a.ModifiedAt, now)
	}
	for _, u := range c.Updates {
		setDefault(&u.Status, UpdateSuccessful)
	}
}

func setDefault[T comparable](field *T, value T) {
	var zero T
	if *field == zero {
		*field = value
	}
}
