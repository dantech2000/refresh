package addons

import (
	"time"
)

// AddonSummary contains basic addon info for listings
type AddonSummary struct {
	Name    string `json:"name" yaml:"name"`
	Version string `json:"version" yaml:"version"`
	Status  string `json:"status" yaml:"status"`
	Health  string `json:"health" yaml:"health"`
}

// AddonDetails contains expanded addon information
type AddonDetails struct {
	Name               string         `json:"name" yaml:"name"`
	Version            string         `json:"version" yaml:"version"`
	Status             string         `json:"status" yaml:"status"`
	Health             string         `json:"health" yaml:"health"`
	ARN                string         `json:"arn" yaml:"arn"`
	ServiceAccountRole string         `json:"serviceAccountRole,omitempty" yaml:"serviceAccountRole,omitempty"`
	CreatedAt          *time.Time     `json:"createdAt,omitempty" yaml:"createdAt,omitempty"`
	ModifiedAt         *time.Time     `json:"modifiedAt,omitempty" yaml:"modifiedAt,omitempty"`
	Configuration      map[string]any `json:"configuration,omitempty" yaml:"configuration,omitempty"`
	Issues             []AddonIssue   `json:"issues,omitempty" yaml:"issues,omitempty"`
	AvailableVersions  []string       `json:"availableVersions,omitempty" yaml:"availableVersions,omitempty"`
}

// AddonIssue represents an issue reported by an addon
type AddonIssue struct {
	Code        string   `json:"code" yaml:"code"`
	Message     string   `json:"message" yaml:"message"`
	ResourceIDs []string `json:"resourceIds,omitempty" yaml:"resourceIds,omitempty"`
}

// AddonVersionInfo contains version-specific information
type AddonVersionInfo struct {
	Version           string   `json:"version"`
	Compatibilities   []string `json:"compatibilities"`
	Architecture      []string `json:"architecture,omitempty"`
	DefaultVersion    bool     `json:"defaultVersion"`
	RequiresIAMPolicy bool     `json:"requiresIamPolicy"`
}

// AddonUpdateResult contains the result of an addon update
type AddonUpdateResult struct {
	AddonName       string    `json:"addonName" yaml:"addonName"`
	PreviousVersion string    `json:"previousVersion" yaml:"previousVersion"`
	NewVersion      string    `json:"newVersion" yaml:"newVersion"`
	UpdateID        string    `json:"updateId" yaml:"updateId"`
	Status          string    `json:"status" yaml:"status"`
	HealthIssues    string    `json:"healthIssues,omitempty" yaml:"healthIssues,omitempty"`
	StartedAt       time.Time `json:"startedAt" yaml:"startedAt"`
}

// ListOptions controls addon listing behavior
type ListOptions struct {
	ShowHealth bool `json:"showHealth"`
}

// DescribeOptions controls addon describe behavior
type DescribeOptions struct {
	ShowVersions      bool `json:"showVersions"`
	ShowConfiguration bool `json:"showConfiguration"`
}

// UpdateOptions controls addon update behavior
type UpdateOptions struct {
	Version       string        `json:"version"`
	DryRun        bool          `json:"dryRun"`
	HealthCheck   bool          `json:"healthCheck"`
	Wait          bool          `json:"wait"`
	WaitTimeout   time.Duration `json:"waitTimeout"`
	PollInterval  time.Duration `json:"pollInterval,omitempty"` // re-check cadence while waiting (default 5s)
	Configuration string        `json:"configuration,omitempty"`
}

// UpdateAllOptions controls bulk addon update behavior
type UpdateAllOptions struct {
	DryRun          bool          `json:"dryRun"`
	Parallel        bool          `json:"parallel"`
	HealthCheck     bool          `json:"healthCheck"`
	Wait            bool          `json:"wait"`
	WaitTimeout     time.Duration `json:"waitTimeout"`
	SkipAddons      []string      `json:"skipAddons,omitempty"`
	DependencyOrder bool          `json:"dependencyOrder"` // update in dependency-safe order (vpc-cni before coredns/kube-proxy, etc.)
	// Timeout, when > 0, is the base deadline for the run. With Wait and a
	// WaitTimeout, the run's deadline grows by WaitTimeout per add-on (per
	// batch when Parallel), so one overall timeout can't truncate the
	// per-add-on waits. Zero leaves the caller's context untouched.
	Timeout time.Duration `json:"timeout,omitempty"`
}

// CompatibilityMatrix tracks addon version compatibility with Kubernetes versions
type CompatibilityMatrix struct {
	AddonName       string              `json:"addonName"`
	Versions        map[string][]string `json:"versions"`        // addon version -> k8s versions
	DefaultVersions map[string]string   `json:"defaultVersions"` // k8s version -> default addon version
}
