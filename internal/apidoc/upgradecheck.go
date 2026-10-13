package apidoc

import "time"

// Readiness is the verdict of "refresh cluster upgrade-check".
type Readiness string

// The readiness values.
const (
	// ReadinessReady: nothing to report.
	ReadinessReady Readiness = "Ready"
	// ReadinessReview: warnings only.
	ReadinessReview Readiness = "Review"
	// ReadinessBlocked: something blocks the upgrade.
	ReadinessBlocked Readiness = "Blocked"
	// ReadinessIncomplete: nothing blocks, but a check could not read what
	// it needs, so the verdict does not cover everything.
	ReadinessIncomplete Readiness = "Incomplete"
)

// Values implements Enum.
func (Readiness) Values() []string {
	return []string{string(ReadinessReady), string(ReadinessReview), string(ReadinessBlocked), string(ReadinessIncomplete)}
}

// UpgradeCheck is the document of "refresh cluster upgrade-check".
type UpgradeCheck struct {
	Cluster string `json:"cluster"`
	Region  string `json:"region"`
	// Version is the Kubernetes minor version of the control plane.
	Version   string    `json:"version,omitzero"`
	Readiness Readiness `json:"readiness"`
	// Reasons are the findings behind the readiness, worst first.
	Reasons []string `json:"reasons"`
	Support *Support `json:"support,omitzero"`
	// Insights are the EKS cluster insights of the category, as --status
	// and --show-passing filter them.
	Insights []Insight `json:"insights"`
	// InsightsEvaluated is false when EKS returned no insight of the
	// category at all, passing ones included: it has not evaluated the
	// cluster yet.
	InsightsEvaluated bool `json:"insightsEvaluated"`
	// Checks are the readiness checks: the insights, the version skew, and
	// the control plane.
	Checks   []HealthResult `json:"checks"`
	Skew     *Skew          `json:"skew,omitzero"`
	Rollback *Rollback      `json:"rollback,omitzero"`
	Failures Failures       `json:"failures"`
}

// Kind implements Document.
func (UpgradeCheck) Kind() Kind { return KindUpgradeCheck }

// Insight is one EKS cluster insight.
type Insight struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
	// Status is the EKS value: PASSING, WARNING, ERROR, or UNKNOWN.
	Status       string `json:"status"`
	StatusReason string `json:"statusReason,omitzero"`
	// KubernetesVersion is the version that the insight is about.
	KubernetesVersion string    `json:"kubernetesVersion,omitzero"`
	LastRefreshTime   time.Time `json:"lastRefreshTime,omitzero"`
	Description       string    `json:"description,omitzero"`
}

// Skew is the version skew of a cluster: its nodegroups against the
// control plane, and its add-ons against their latest compatible version.
type Skew struct {
	// ControlPlaneVersion is the Kubernetes minor version of the cluster.
	ControlPlaneVersion string          `json:"controlPlaneVersion"`
	Nodegroups          []NodegroupSkew `json:"nodegroups"`
	Addons              []AddonSkew     `json:"addons"`
}

// NodegroupSkew is the version of one nodegroup against the control plane.
type NodegroupSkew struct {
	Name    string `json:"name"`
	Version string `json:"version,omitzero"`
	// MinorsBehind is how many minor versions the nodegroup trails the
	// control plane.
	MinorsBehind int `json:"minorsBehind"`
	// AtSkewLimit is true when the nodegroup is at the kubelet skew limit:
	// the next upgrade of the control plane would put it past it.
	AtSkewLimit bool `json:"atSkewLimit"`
	// Incomplete is true when the nodegroup could not be read.
	Incomplete bool `json:"incomplete,omitzero"`
}

// AddonSkew is the version of one add-on against its latest compatible
// version.
type AddonSkew struct {
	Name    string `json:"name"`
	Version string `json:"version,omitzero"`
	// Latest is the latest version compatible with the cluster. It is left
	// out when EKS publishes none.
	Latest string `json:"latest,omitzero"`
	Behind bool   `json:"behind"`
	// Incomplete is true when the add-on or its latest version could not
	// be read.
	Incomplete bool `json:"incomplete,omitzero"`
}

// Rollback says that the cluster can roll back to its previous minor
// version, and until when.
type Rollback struct {
	PreviousVersion string `json:"previousVersion"`
	// UpgradedAt is when the upgrade to the current version started. EKS
	// counts the window from when it finished, so the dates are
	// approximate.
	UpgradedAt     time.Time `json:"upgradedAt"`
	AvailableUntil time.Time `json:"availableUntil"`
	// Insights counts the ROLLBACK_READINESS insights by status. It is left
	// out when they could not be read.
	Insights *InsightCounts `json:"insights,omitzero"`
}

// InsightCounts counts insights by status.
type InsightCounts struct {
	Passing int `json:"passing"`
	Warning int `json:"warning"`
	Error   int `json:"error"`
	Unknown int `json:"unknown"`
}

// InsightDescription is the document of "refresh cluster upgrade-check
// --insight".
type InsightDescription struct {
	Cluster        string  `json:"cluster"`
	Region         string  `json:"region"`
	Insight        Insight `json:"insight"`
	Recommendation string  `json:"recommendation,omitzero"`
	// Resources are the ARNs or the Kubernetes resource URIs that the
	// insight names.
	Resources      []string          `json:"resources"`
	AdditionalInfo map[string]string `json:"additionalInfo,omitzero"`
	// Deprecations are the deprecated APIs that an upgrade-readiness
	// insight reports, with the clients that still call them.
	Deprecations []Deprecation `json:"deprecations"`
	Failures     Failures      `json:"failures"`
}

// Kind implements Document.
func (InsightDescription) Kind() Kind { return KindInsightDescription }

// Deprecation is one deprecated Kubernetes API and who still calls it.
type Deprecation struct {
	// Usage is the deprecated API, such as policy/v1beta1 PodDisruptionBudget.
	Usage string `json:"usage,omitzero"`
	// ReplacedWith is the API to move to.
	ReplacedWith string `json:"replacedWith,omitzero"`
	// StopServingVersion is the Kubernetes version that removes the API.
	StopServingVersion string `json:"stopServingVersion,omitzero"`
	// StartServingReplacementVersion is the version that introduced the
	// replacement.
	StartServingReplacementVersion string `json:"startServingReplacementVersion,omitzero"`
	// ClientStats are the callers of the API, most active first.
	ClientStats []ClientStat `json:"clientStats"`
}

// ClientStat is one client that still calls a deprecated API.
type ClientStat struct {
	UserAgent                  string    `json:"userAgent,omitzero"`
	LastRequestTime            time.Time `json:"lastRequestTime,omitzero"`
	NumberOfRequestsLast30Days int32     `json:"numberOfRequestsLast30Days"`
}
