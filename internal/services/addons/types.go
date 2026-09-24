package addons

import (
	"time"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/diag"
)

// Health is an add-on's health, derived from its EKS status.
type Health string

// The add-on health values.
const (
	// HealthPass: the add-on is ACTIVE.
	HealthPass Health = "Pass"
	// HealthFail: the add-on is DEGRADED, or a create, update, or delete
	// failed.
	HealthFail Health = "Fail"
	// HealthInProgress: the add-on is CREATING, UPDATING, or DELETING.
	HealthInProgress Health = "InProgress"
	// HealthUnknown: any other status, or the add-on could not be read.
	HealthUnknown Health = "Unknown"
)

// EnumValues lists every Health.
func (Health) EnumValues() []string {
	return []string{string(HealthPass), string(HealthFail), string(HealthInProgress), string(HealthUnknown)}
}

// AddonSummary contains basic addon info for listings
type AddonSummary struct {
	Name    string `json:"name" yaml:"name"`
	Version string `json:"version" yaml:"version"`
	Status  string `json:"status" yaml:"status"`
	// Health is set with --show-health.
	Health Health `json:"health,omitempty" yaml:"health,omitempty"`
}

// ListResult is the full outcome of an add-on listing.
type ListResult struct {
	// Summaries are the add-ons that were described.
	Summaries []AddonSummary
	// Failures has one entry per installed add-on left out of Summaries: it
	// could not be described, or the listing stopped before it was reached.
	// Region is not set; the caller knows it.
	Failures []diag.Failure
}

// AddonList is the `addon list -o json|yaml` document.
type AddonList struct {
	Cluster string         `json:"cluster" yaml:"cluster"`
	Addons  []AddonSummary `json:"addons" yaml:"addons"`
	Count   int            `json:"count" yaml:"count"`
	// Failures are the add-ons that could not be described; the list leaves
	// them out. [] when every add-on was read.
	Failures diag.List `json:"failures" yaml:"failures"`
}

// DocumentKind is AddonList.
func (AddonList) DocumentKind() apidoc.Kind { return apidoc.KindAddonList }

// AddonDetails contains expanded addon information
type AddonDetails struct {
	Name               string         `json:"name" yaml:"name"`
	Version            string         `json:"version" yaml:"version"`
	Status             string         `json:"status" yaml:"status"`
	Health             Health         `json:"health" yaml:"health"`
	ARN                string         `json:"arn" yaml:"arn"`
	ServiceAccountRole string         `json:"serviceAccountRole,omitempty" yaml:"serviceAccountRole,omitempty"`
	CreatedAt          *time.Time     `json:"createdAt,omitempty" yaml:"createdAt,omitempty"`
	ModifiedAt         *time.Time     `json:"modifiedAt,omitempty" yaml:"modifiedAt,omitempty"`
	Configuration      map[string]any `json:"configuration,omitempty" yaml:"configuration,omitempty"`
	Issues             []AddonIssue   `json:"issues,omitempty" yaml:"issues,omitempty"`
	AvailableVersions  []string       `json:"availableVersions,omitempty" yaml:"availableVersions,omitempty"`
	// Failures is always [] today: describe either reads the add-on or
	// fails. It is here so every document carries the same key.
	Failures diag.List `json:"failures" yaml:"failures"`
}

// DocumentKind is AddonDescription.
func (AddonDetails) DocumentKind() apidoc.Kind { return apidoc.KindAddonDescription }

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

// UpdateStatus is the outcome of one add-on update.
type UpdateStatus string

// The statuses of an add-on update result. docs/concepts/output.md
// documents them; later versions may add values.
const (
	// StatusDryRun: --dry-run; nothing was sent.
	StatusDryRun UpdateStatus = "DryRun"
	// StatusUpToDate: the add-on is already at (or, for "latest", above) the
	// target version; no UpdateAddon call was made.
	StatusUpToDate UpdateStatus = "UpToDate"
	// StatusInProgress: the add-on is already CREATING/UPDATING at the
	// target version; no new update was submitted (without Wait).
	StatusInProgress UpdateStatus = "InProgress"
	// StatusStarted: the update was submitted, and the run did not wait for
	// it (no Wait).
	StatusStarted UpdateStatus = "Started"
	// StatusCompleted: the EKS update succeeded, the add-on reports the
	// target version, and the post-update health check passed.
	StatusCompleted UpdateStatus = "Completed"
	// StatusCompletedWithIssues: the update landed but the post-update health
	// check found problems (see HealthIssues).
	StatusCompletedWithIssues UpdateStatus = "CompletedWithIssues"
	// StatusUnverified: the update landed, but the post-update health check
	// could not read the add-on (see Failure).
	StatusUnverified UpdateStatus = "Unverified"
	// StatusWaitFailed: the update was submitted but did not complete: it
	// failed or was cancelled in EKS, the add-on ended at another version, or
	// the wait timed out (see Failure).
	StatusWaitFailed UpdateStatus = "WaitFailed"
	// StatusFailed: the update could not be submitted (see Failure).
	StatusFailed UpdateStatus = "Failed"
	// StatusNotAttempted: an UpdateAll run stopped before this add-on (see
	// Failure).
	StatusNotAttempted UpdateStatus = "NotAttempted"
)

// EnumValues lists every UpdateStatus.
func (UpdateStatus) EnumValues() []string {
	return []string{
		string(StatusDryRun), string(StatusUpToDate), string(StatusInProgress), string(StatusStarted),
		string(StatusCompleted), string(StatusCompletedWithIssues), string(StatusUnverified),
		string(StatusWaitFailed), string(StatusFailed), string(StatusNotAttempted),
	}
}

// AddonUpdateResult contains the result of an addon update
type AddonUpdateResult struct {
	AddonName       string       `json:"addonName" yaml:"addonName"`
	PreviousVersion string       `json:"previousVersion" yaml:"previousVersion"`
	NewVersion      string       `json:"newVersion" yaml:"newVersion"`
	UpdateID        string       `json:"updateId" yaml:"updateId"`
	Status          UpdateStatus `json:"status" yaml:"status"`
	HealthIssues    string       `json:"healthIssues,omitempty" yaml:"healthIssues,omitempty"`
	// Warning is set when the update needs the user's attention but still
	// proceeds, e.g. a pinned version older than the installed one.
	Warning string `json:"warning,omitempty" yaml:"warning,omitempty"`
	// Failure is set for the statuses Unverified, WaitFailed, Failed, and
	// NotAttempted.
	Failure   *diag.Failure `json:"failure,omitempty" yaml:"failure,omitempty"`
	StartedAt time.Time     `json:"startedAt" yaml:"startedAt"`
}

// Failed reports whether the result has a failure: the update could not be
// submitted, did not complete, could not be verified, or was not attempted.
func (r AddonUpdateResult) Failed() bool { return r.Failure != nil }

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
