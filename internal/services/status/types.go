// Package status aggregates fleet-wide EKS patch posture — Kubernetes version,
// support window, nodegroup AMI staleness, and addons-behind-latest — for the
// `refresh status` command. It reuses the nodegroup and addons services and
// is single-region; multi-region fan-out lives in the command layer.
package status

import (
	"time"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/types"
)

// ComputeType describes how a cluster provisions its worker nodes. It exists so
// `refresh status` never renders a nodegroup-less cluster as an empty row.
type ComputeType string

const (
	// ComputeManaged is the normal case: one or more managed nodegroups.
	ComputeManaged ComputeType = "ManagedNodegroups"
	// ComputeAutoMode is EKS Auto Mode (AWS manages compute and AMIs).
	ComputeAutoMode ComputeType = "AutoMode"
	// ComputeKarpenter is a nodegroup-less cluster with Karpenter signals.
	ComputeKarpenter ComputeType = "Karpenter"
	// ComputeNone is a cluster with no managed nodegroups and no detected
	// alternative compute provider.
	ComputeNone ComputeType = "None"
)

// EnumValues lists every ComputeType.
func (ComputeType) EnumValues() []string {
	return []string{string(ComputeManaged), string(ComputeAutoMode), string(ComputeKarpenter), string(ComputeNone)}
}

// SupportTier is the EKS support posture for a cluster's Kubernetes version.
type SupportTier string

const (
	SupportStandard    SupportTier = "Standard"
	SupportExtended    SupportTier = "Extended"
	SupportUnsupported SupportTier = "Unsupported"
	SupportUnknown     SupportTier = "Unknown"
)

// EnumValues lists every SupportTier.
func (SupportTier) EnumValues() []string {
	return []string{string(SupportStandard), string(SupportExtended), string(SupportUnsupported), string(SupportUnknown)}
}

// SupportPosture is the resolved support window for a cluster's version.
type SupportPosture struct {
	Tier          SupportTier `json:"tier" yaml:"tier"`
	StandardUntil *time.Time  `json:"standardUntil,omitempty" yaml:"standardUntil,omitempty"`
	ExtendedUntil *time.Time  `json:"extendedUntil,omitempty" yaml:"extendedUntil,omitempty"`
	// DaysRemaining counts down to the end of the current tier (standard or
	// extended). Negative once the window has closed.
	DaysRemaining *int `json:"daysRemaining,omitempty" yaml:"daysRemaining,omitempty"`
	// ExtraCostUSDPerHour is the per-cluster premium of the current tier over
	// standard support (0 unless extended).
	ExtraCostUSDPerHour float64 `json:"extraCostUsdPerHour,omitempty" yaml:"extraCostUsdPerHour,omitempty"`
	// AutoUpgradeAtStandardEnd is true when the cluster's upgrade policy is
	// STANDARD: EKS auto-upgrades it at the end of standard support instead of
	// moving it to (paid) extended support.
	AutoUpgradeAtStandardEnd bool `json:"autoUpgradeAtStandardEnd,omitempty" yaml:"autoUpgradeAtStandardEnd,omitempty"`
	// Fallback is true when the posture came from the compiled-in calendar
	// because DescribeClusterVersions was unavailable.
	Fallback bool `json:"fallback,omitempty" yaml:"fallback,omitempty"`
}

// StaleAMISummary summarizes nodegroup AMI posture for a cluster.
type StaleAMISummary struct {
	Total  int `json:"total" yaml:"total"`
	Behind int `json:"behind" yaml:"behind"`
	// OldestDays is the age of the oldest stale AMI in days, when resolvable.
	OldestDays *int `json:"oldestDays,omitempty" yaml:"oldestDays,omitempty"`
}

// AddonsBehindSummary summarizes addon version posture for a cluster.
type AddonsBehindSummary struct {
	Total  int      `json:"total" yaml:"total"`
	Behind int      `json:"behind" yaml:"behind"`
	Names  []string `json:"names,omitempty" yaml:"names,omitempty"`
}

// ClusterStatus is the fleet-status row for a single cluster.
type ClusterStatus struct {
	Name           string              `json:"name" yaml:"name"`
	Region         string              `json:"region" yaml:"region"`
	Version        string              `json:"version" yaml:"version"`
	Support        SupportPosture      `json:"support" yaml:"support"`
	Compute        ComputeType         `json:"compute" yaml:"compute"`
	NodegroupCount int                 `json:"nodegroupCount" yaml:"nodegroupCount"`
	StaleAMI       StaleAMISummary     `json:"staleAmi" yaml:"staleAmi"`
	AddonsBehind   AddonsBehindSummary `json:"addonsBehind" yaml:"addonsBehind"`
	// NodegroupsBehindControlPlane counts nodegroups on an older Kubernetes
	// minor than the control plane (a half-finished upgrade). StaleAMI can't
	// show this: AMI freshness is judged against each nodegroup's own minor.
	NodegroupsBehindControlPlane int `json:"nodegroupsBehindControlPlane" yaml:"nodegroupsBehindControlPlane"`
	// HealthIssues is the count of AWS-reported control-plane health issues
	// (DescribeCluster Health.Issues) — degraded resources, IAM failures, etc.
	HealthIssues int `json:"healthIssues,omitempty" yaml:"healthIssues,omitempty"`
	// Incomplete is true when some of the row's data could not be read (a
	// failed AWS call, or a sweep that stopped first). The row still renders
	// with what was read; its failures are in the document's top-level
	// "failures", identified by cluster (or, for the cluster itself, name).
	Incomplete bool `json:"incomplete,omitempty" yaml:"incomplete,omitempty"`
	// Failures are the row's failures. The command lists them in the
	// FleetStatus document, not on the row.
	Failures []diag.Failure `json:"-" yaml:"-"`
	// State is the EKS cluster status (ACTIVE, UPDATING, ...). It is not
	// part of the FleetStatus document.
	State string `json:"-" yaml:"-"`
	// Nodegroups and Addons are the per-item rows the counts above come
	// from. They are filled only with ListOptions.Detail, and are not part of
	// the FleetStatus document.
	Nodegroups []NodegroupPosture `json:"-" yaml:"-"`
	Addons     []AddonPosture     `json:"-" yaml:"-"`
}

// NodegroupPosture is one managed nodegroup's patch state.
type NodegroupPosture struct {
	Name    string
	Status  string
	Version string
	// VersionBehind is true when the nodegroup trails the control plane.
	VersionBehind bool
	CurrentAMI    string
	AMIStatus     types.AMIStatus
	DesiredSize   int32
}

// AddonPosture is one installed add-on and the newest version compatible
// with the cluster's Kubernetes version ("" when it could not be read, or
// none is published).
type AddonPosture struct {
	Name    string
	Status  string
	Version string
	Latest  string
	Behind  bool
}

// addFailure records f against the row and marks it incomplete.
func (c *ClusterStatus) addFailure(f diag.Failure) {
	c.Failures = append(c.Failures, f)
	c.Incomplete = true
}

// NeedsAttention reports whether the cluster has any stale AMIs, nodegroups
// behind the control plane, addons behind latest, or AWS-reported
// control-plane health issues (drives the exit-code "something stale" signal).
func (c ClusterStatus) NeedsAttention() bool {
	return c.StaleAMI.Behind > 0 || c.NodegroupsBehindControlPlane > 0 || c.AddonsBehind.Behind > 0 || c.HealthIssues > 0
}

// SupportRisk reports whether the cluster is on extended or unsupported EKS
// (drives the exit-code "support risk" signal).
func (c ClusterStatus) SupportRisk() bool {
	return c.Support.Tier == SupportExtended || c.Support.Tier == SupportUnsupported
}

// FleetStatus is the aggregate posture across clusters and regions — the
// payload serialized for json/yaml output.
type FleetStatus struct {
	Clusters []ClusterStatus `json:"clusters" yaml:"clusters"`
	// Failures are the regions whose clusters could not be listed and the
	// parts of cluster rows that could not be read, so a partial fleet is
	// never mistaken for the whole one. [] when everything was read.
	Failures diag.List `json:"failures" yaml:"failures"`
}

// DocumentKind is FleetStatus.
func (FleetStatus) DocumentKind() apidoc.Kind { return apidoc.KindFleetStatus }
