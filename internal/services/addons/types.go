package addons

import (
	"time"
)

// AddonSummary contains basic addon info for listings
type AddonSummary struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Status  string `json:"status"`
	Health  string `json:"health"`
}

// AddonDetails contains expanded addon information
type AddonDetails struct {
	Name               string                 `json:"name"`
	Version            string                 `json:"version"`
	Status             string                 `json:"status"`
	Health             string                 `json:"health"`
	ARN                string                 `json:"arn"`
	ServiceAccountRole string                 `json:"serviceAccountRole,omitempty"`
	CreatedAt          *time.Time             `json:"createdAt,omitempty"`
	ModifiedAt         *time.Time             `json:"modifiedAt,omitempty"`
	Configuration      map[string]any `json:"configuration,omitempty"`
	Issues             []AddonIssue           `json:"issues,omitempty"`
	AvailableVersions  []string               `json:"availableVersions,omitempty"`
}

// AddonIssue represents an issue reported by an addon
type AddonIssue struct {
	Code        string   `json:"code"`
	Message     string   `json:"message"`
	ResourceIDs []string `json:"resourceIds,omitempty"`
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
	AddonName       string    `json:"addonName"`
	PreviousVersion string    `json:"previousVersion"`
	NewVersion      string    `json:"newVersion"`
	UpdateID        string    `json:"updateId"`
	Status          string    `json:"status"`
	HealthIssues    string    `json:"healthIssues,omitempty"`
	StartedAt       time.Time `json:"startedAt"`
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
}

// CompatibilityMatrix tracks addon version compatibility with Kubernetes versions
type CompatibilityMatrix struct {
	AddonName       string              `json:"addonName"`
	Versions        map[string][]string `json:"versions"`        // addon version -> k8s versions
	DefaultVersions map[string]string   `json:"defaultVersions"` // k8s version -> default addon version
}
