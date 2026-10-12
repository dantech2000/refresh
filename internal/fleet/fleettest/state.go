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
	// Parameters are SSM parameters by name. See State.Parameter for the
	// EKS-optimized AMI parameters that need no entry.
	Parameters map[string]string

	Images                 []Image
	Instances              []Instance
	LaunchTemplateVersions []LaunchTemplateVersion
	Subnets                []Subnet
	VPCs                   []VPC
	// Offerings are the instance types offered per availability zone.
	Offerings         []Offering
	AutoScalingGroups []AutoScalingGroup
	// ScalingActivities are listed newest first, as Auto Scaling does.
	ScalingActivities []ScalingActivity
	Metrics           []Metric
	Quotas            []Quota

	// DefaultUpdate scripts every update whose resource has no UpdateScripts
	// left. The zero value succeeds at once.
	DefaultUpdate UpdateScript
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
	Status string
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
}

// Nodegroup is a managed nodegroup.
type Nodegroup struct {
	Name    string
	Version string
	// ReleaseVersion defaults to LatestRelease(Version).
	ReleaseVersion string
	// Status defaults to ACTIVE.
	Status string
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
	Status                string
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
	// Status is PASSING, WARNING, ERROR, or UNKNOWN.
	Status string
	Reason string
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
	Status string
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
	// Status is IN_PROGRESS, COMPLETED, or "" when none was started.
	Status    string
	StartedAt time.Time
	// EndsAt is when an IN_PROGRESS refresh becomes COMPLETED.
	EndsAt time.Time
}

// AddonVersion is one version of an add-on in the catalog.
type AddonVersion struct {
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
	Version string
	// Status defaults to STANDARD_SUPPORT.
	Status string
	// EndOfStandardSupport and EndOfExtendedSupport are left out of the
	// answer when zero.
	EndOfStandardSupport time.Time
	EndOfExtendedSupport time.Time
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
	return c.Region == "" || c.Region == region
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

// Parameter returns the SSM parameter with the name. A name that is not in
// Parameters but has a Kubernetes version in its path, as the EKS-optimized
// AMI parameters do, answers a derived value: LatestRelease for a
// release_version or image_version parameter, and an image ID such as
// ami-latest132 for the others.
func (s *State) Parameter(name string) (string, bool) {
	if v, ok := s.Parameters[name]; ok {
		return v, true
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
	return "ami-latest" + strings.ReplaceAll(version, ".", ""), true
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
		setDefault(&s.ClusterVersions[i].Status, "STANDARD_SUPPORT")
	}
	for _, c := range s.Clusters {
		c.applyDefaults(now)
	}
}

func (c *Cluster) applyDefaults(now time.Time) {
	setDefault(&c.Status, "ACTIVE")
	setDefault(&c.PlatformVersion, "eks.1")
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	if c.Insights == nil {
		c.Insights = []Insight{{ID: "insight-" + c.Name + "-skew", Name: "Kubelet version skew", Status: "PASSING"}}
	}
	for i := range c.Insights {
		setDefault(&c.Insights[i].Category, "UPGRADE_READINESS")
	}
	for _, ng := range c.Nodegroups {
		setDefault(&ng.Status, "ACTIVE")
		setDefault(&ng.ReleaseVersion, LatestRelease(ng.Version))
		setDefault(&ng.AMIType, "AL2023_x86_64_STANDARD")
		setDefault(&ng.CapacityType, "ON_DEMAND")
		if len(ng.InstanceTypes) == 0 {
			ng.InstanceTypes = []string{"m5.large"}
		}
		if ng.Desired == 0 && ng.Min == 0 && ng.Max == 0 {
			ng.Desired, ng.Min, ng.Max = 2, 1, 3
		}
	}
	for _, a := range c.Addons {
		setDefault(&a.Status, "ACTIVE")
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
