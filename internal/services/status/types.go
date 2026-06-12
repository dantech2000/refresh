// Package status aggregates fleet-wide EKS patch posture — Kubernetes version,
// support window, nodegroup AMI staleness, and addons-behind-latest — for the
// `refresh status` command. It reuses the cluster/nodegroup/addons services and
// is single-region; multi-region fan-out lives in the command layer.
package status

import "time"

// ComputeType describes how a cluster provisions its worker nodes. It exists so
// `refresh status` never renders a nodegroup-less cluster as an empty row.
type ComputeType string

const (
	// ComputeManaged is the normal case: one or more managed nodegroups.
	ComputeManaged ComputeType = "managed-nodegroups"
	// ComputeAutoMode is EKS Auto Mode (AWS manages compute and AMIs).
	ComputeAutoMode ComputeType = "auto-mode"
	// ComputeKarpenter is a nodegroup-less cluster with Karpenter signals.
	ComputeKarpenter ComputeType = "karpenter"
	// ComputeNone is a cluster with no managed nodegroups and no detected
	// alternative compute provider.
	ComputeNone ComputeType = "none"
)

// SupportTier is the EKS support posture for a cluster's Kubernetes version.
type SupportTier string

const (
	SupportStandard    SupportTier = "standard"
	SupportExtended    SupportTier = "extended"
	SupportUnsupported SupportTier = "unsupported"
	SupportUnknown     SupportTier = "unknown"
)

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
	// Errors holds non-fatal, per-cluster failures so a partial row still
	// renders instead of dropping the cluster entirely.
	Errors []string `json:"errors,omitempty" yaml:"errors,omitempty"`
}

// NeedsAttention reports whether the cluster has any stale AMIs or addons
// behind latest (drives the exit-code "something stale" signal).
func (c ClusterStatus) NeedsAttention() bool {
	return c.StaleAMI.Behind > 0 || c.AddonsBehind.Behind > 0
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
}
