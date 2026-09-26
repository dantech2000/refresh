package upgrade

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/apidoc"
	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/common"
	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/services/addons"
	"github.com/dantech2000/refresh/internal/services/status"
)

// A version rollback moves the control plane back one minor version (N to
// N-1) within 7 days of an in-place upgrade. It is UpdateClusterVersion with
// the previous version; EKS types the update VersionRollback. EKS rolls back
// only the control plane (and EKS Auto Mode nodes), so the documented order
// is: managed nodegroups at N roll back to N-1, then add-ons that N-1 cannot
// run downgrade, then the control plane rolls back.
// https://docs.aws.amazon.com/eks/latest/userguide/rollback-cluster.html
//
// Like the upgrade plan, the rollback plan is derived from live state on
// every run, so a rerun continues where a stopped run ended.

// RollbackWindow is how long after an upgrade EKS accepts a rollback.
const RollbackWindow = 7 * 24 * time.Hour

// Rollback timeout bounds (RollbackConfig.TimeoutMinutes).
const (
	MinRollbackTimeout = 2 * time.Hour
	MaxRollbackTimeout = 7 * 24 * time.Hour
)

// ValidateRollbackTimeout checks a --rollback-timeout value: 0 (EKS's
// default, 12h) or a whole number of minutes from 2h to 7d.
func ValidateRollbackTimeout(d time.Duration) error {
	if d == 0 {
		return nil
	}
	if d < MinRollbackTimeout || d > MaxRollbackTimeout || d%time.Minute != 0 {
		return fmt.Errorf("--rollback-timeout must be whole minutes from 2h to 168h (7 days), got %s", d)
	}
	return nil
}

// InsightCounts counts insights by status.
type InsightCounts struct {
	Passing int `json:"passing" yaml:"passing"`
	Warning int `json:"warning" yaml:"warning"`
	Error   int `json:"error" yaml:"error"`
	Unknown int `json:"unknown" yaml:"unknown"`
}

// RollbackAvailability says that a cluster can roll back and until when.
type RollbackAvailability struct {
	// PreviousVersion is the version a rollback returns to.
	PreviousVersion string `json:"previousVersion" yaml:"previousVersion"`
	// UpgradedAt is when the upgrade to the current version started. EKS
	// counts the window from when it finished, so dates are approximate.
	UpgradedAt     time.Time `json:"upgradedAt" yaml:"upgradedAt"`
	AvailableUntil time.Time `json:"availableUntil" yaml:"availableUntil"`
	// Insights counts the ROLLBACK_READINESS insights; left out when they
	// could not be read.
	Insights *InsightCounts `json:"insights,omitempty" yaml:"insights,omitempty"`
}

// RollbackPlan is the ordered plan to roll a cluster back one minor version.
type RollbackPlan struct {
	ClusterName string `json:"clusterName" yaml:"clusterName"`
	// CurrentVersion is the live control-plane version.
	CurrentVersion string `json:"currentVersion" yaml:"currentVersion"`
	// TargetVersion is the version the rollback returns to (N-1).
	TargetVersion string `json:"targetVersion" yaml:"targetVersion"`
	// UpgradedAt and AvailableUntil bound the rollback window, when an
	// in-place upgrade to CurrentVersion was found.
	UpgradedAt     *time.Time `json:"upgradedAt,omitempty" yaml:"upgradedAt,omitempty"`
	AvailableUntil *time.Time `json:"availableUntil,omitempty" yaml:"availableUntil,omitempty"`
	// RolledBack is true when the control plane already rolled back to
	// TargetVersion: a rerun after a finished rollback.
	RolledBack bool `json:"rolledBack,omitempty" yaml:"rolledBack,omitempty"`
	// Steps run in order: the checks, the nodegroup rollbacks, the add-on
	// downgrades, then the control plane.
	Steps   []Step   `json:"steps" yaml:"steps"`
	Notices []string `json:"notices,omitempty" yaml:"notices,omitempty"`
	// Failures are the reads the planner could not make. [] when empty.
	Failures diag.List `json:"failures" yaml:"failures"`
}

// DocumentKind is RollbackPlan.
func (RollbackPlan) DocumentKind() apidoc.Kind { return apidoc.KindRollbackPlan }

// stepsWith returns "description: reason" of each step with status st.
func stepsWith(steps []Step, st StepStatus) []string {
	var out []string
	for _, s := range steps {
		if s.Status == st {
			out = append(out, fmt.Sprintf("%s: %s", s.Description, s.Reason))
		}
	}
	return out
}

// Blockers returns the blocked steps.
func (p *RollbackPlan) Blockers() []string { return stepsWith(p.Steps, StatusBlocked) }

// Blocked reports whether any step is blocked.
func (p *RollbackPlan) Blocked() bool { return len(p.Blockers()) > 0 }

// ManualSteps returns the steps left to the operator.
func (p *RollbackPlan) ManualSteps() []string { return stepsWith(p.Steps, StatusManual) }

// PendingSteps counts the mutating steps a run would execute.
func (p *RollbackPlan) PendingSteps() int {
	n := 0
	for _, s := range p.Steps {
		if s.Status == StatusPending && s.Type != StepReadiness {
			n++
		}
	}
	return n
}

func (p *RollbackPlan) addFailure(f diag.Failure) {
	for _, have := range p.Failures {
		if have == f {
			return
		}
	}
	p.Failures = append(p.Failures, f)
}

// RollbackOptions tunes the rollback plan.
type RollbackOptions struct {
	// SkipNodegroups are substring patterns for nodegroups to leave alone.
	SkipNodegroups []string
	// SkipInsightsCheck stops ERROR and UNKNOWN rollback-readiness insights
	// from blocking. The run then sets Force on UpdateClusterVersion, so EKS
	// skips its own insight checks too.
	SkipInsightsCheck bool
}

// versionHistory is what the cluster's update history says about its
// current version.
type versionHistory struct {
	// upgrade is the newest Successful VersionUpdate to the current version.
	upgrade *ekstypes.Update
	// rollback is the newest Successful VersionRollback to the current
	// version.
	rollback *ekstypes.Update
	// inFlight is the newest version update or rollback in progress.
	inFlight *ekstypes.Update
}

// rolledBack reports whether the cluster reached its current version by a
// rollback, after any upgrade to it.
func (h versionHistory) rolledBack() bool {
	return h.rollback != nil && (h.upgrade == nil || newerUpdate(h.rollback, h.upgrade))
}

// newerUpdate reports whether a was created after b.
func newerUpdate(a, b *ekstypes.Update) bool {
	return b == nil || aws.ToTime(a.CreatedAt).After(aws.ToTime(b.CreatedAt))
}

// updateVersion returns an update's Version parameter.
func updateVersion(u *ekstypes.Update) string {
	for _, p := range u.Params {
		if p.Type == ekstypes.UpdateParamTypeVersion {
			return aws.ToString(p.Value)
		}
	}
	return ""
}

// versionHistory reads the cluster-level update history. EKS does not
// promise an order, so every update is described.
func (s *Service) versionHistory(ctx context.Context, clusterName, current string) (versionHistory, error) {
	var h versionHistory
	ids, err := awsinternal.ListAllPages(ctx, fmt.Sprintf("listing updates of cluster %s", clusterName),
		func(rc context.Context, token *string) (*eks.ListUpdatesOutput, error) {
			return s.eksClient.ListUpdates(rc, &eks.ListUpdatesInput{Name: aws.String(clusterName), NextToken: token})
		},
		func(out *eks.ListUpdatesOutput) ([]string, *string) { return out.UpdateIds, out.NextToken },
	)
	if err != nil {
		return h, diag.WithOperation(diag.OpListUpdates, err)
	}
	type result struct {
		update *ekstypes.Update
		err    error
	}
	results := common.ForEachParallel(ctx, ids, common.DefaultItemConcurrency, func(ctx context.Context, id string) result {
		out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeUpdateOutput, error) {
			return s.eksClient.DescribeUpdate(rc, &eks.DescribeUpdateInput{Name: aws.String(clusterName), UpdateId: aws.String(id)})
		})
		if err != nil {
			return result{err: diag.WithOperation(diag.OpDescribeUpdate, awsinternal.FormatAWSError(err, fmt.Sprintf("describing update %s of cluster %s", id, clusterName)))}
		}
		return result{update: out.Update}
	})
	if err := ctx.Err(); err != nil {
		return h, err
	}
	for _, r := range results {
		if r.err != nil {
			return h, r.err
		}
		u := r.update
		if u == nil || (u.Type != ekstypes.UpdateTypeVersionUpdate && u.Type != ekstypes.UpdateTypeVersionRollback) {
			continue
		}
		switch {
		case u.Status == ekstypes.UpdateStatusInProgress:
			if newerUpdate(u, h.inFlight) {
				h.inFlight = u
			}
		case u.Status != ekstypes.UpdateStatusSuccessful || updateVersion(u) != current:
		case u.Type == ekstypes.UpdateTypeVersionUpdate:
			if newerUpdate(u, h.upgrade) {
				h.upgrade = u
			}
		default:
			if newerUpdate(u, h.rollback) {
				h.rollback = u
			}
		}
	}
	return h, nil
}

// previousMinor returns the minor before v ("1.33" gives "1.32").
func previousMinor(v string) (string, error) {
	minor, err := minorVersion(v)
	if err != nil {
		return "", err
	}
	if minor == 0 {
		return "", fmt.Errorf("version %s has no previous minor", v)
	}
	return fmt.Sprintf("1.%d", minor-1), nil
}

// nextMinor returns the minor after v, or "" when v does not parse.
func nextMinor(v string) string {
	minor, err := minorVersion(v)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("1.%d", minor+1)
}

// clock returns the current time (tests pin it through now).
func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// aboutDate formats t as a day, for "about" dates.
func aboutDate(t time.Time) string { return t.UTC().Format("2006-01-02") }

// RollbackAvailability reports whether clusterName, running currentVersion,
// can roll back now: an in-place upgrade to currentVersion started less than
// RollbackWindow ago and the cluster has not rolled back since. It returns
// nil when no rollback is available. The insight counts are best effort.
func (s *Service) RollbackAvailability(ctx context.Context, clusterName, currentVersion string) (*RollbackAvailability, error) {
	prev, err := previousMinor(currentVersion)
	if err != nil {
		return nil, nil //nolint:nilerr // an unparsable version has no rollback
	}
	h, err := s.versionHistory(ctx, clusterName, currentVersion)
	if err != nil {
		return nil, err
	}
	if h.upgrade == nil || h.rolledBack() {
		return nil, nil
	}
	started := aws.ToTime(h.upgrade.CreatedAt)
	until := started.Add(RollbackWindow)
	if !s.clock().Before(until) {
		return nil, nil
	}
	a := &RollbackAvailability{PreviousVersion: prev, UpgradedAt: started, AvailableUntil: until}
	if insights, err := s.listRollbackInsights(ctx, clusterName); err == nil {
		c := countInsights(insights)
		a.Insights = &c
	}
	return a, nil
}

// listRollbackInsights lists the cluster's ROLLBACK_READINESS insights.
func (s *Service) listRollbackInsights(ctx context.Context, clusterName string) ([]ekstypes.InsightSummary, error) {
	return awsinternal.ListAllPages(ctx, fmt.Sprintf("listing rollback insights for cluster %s", clusterName),
		func(rc context.Context, token *string) (*eks.ListInsightsOutput, error) {
			return s.eksClient.ListInsights(rc, &eks.ListInsightsInput{
				ClusterName: aws.String(clusterName),
				Filter:      &ekstypes.InsightsFilter{Categories: []ekstypes.Category{ekstypes.CategoryRollbackReadiness}},
				NextToken:   token,
			})
		},
		func(out *eks.ListInsightsOutput) ([]ekstypes.InsightSummary, *string) {
			return out.Insights, out.NextToken
		},
	)
}

// countInsights counts insights by status. A missing or unknown status
// counts as UNKNOWN, as EKS treats it.
func countInsights(insights []ekstypes.InsightSummary) InsightCounts {
	var c InsightCounts
	for _, in := range insights {
		var v ekstypes.InsightStatusValue
		if in.InsightStatus != nil {
			v = in.InsightStatus.Status
		}
		switch v {
		case ekstypes.InsightStatusValuePassing:
			c.Passing++
		case ekstypes.InsightStatusValueWarning:
			c.Warning++
		case ekstypes.InsightStatusValueError:
			c.Error++
		default:
			c.Unknown++
		}
	}
	return c
}

// insightName is an insight's name, or its ID when it has none.
func insightName(in ekstypes.InsightSummary) string {
	if n := aws.ToString(in.Name); n != "" {
		return n
	}
	return aws.ToString(in.Id)
}

// BuildRollbackPlan derives the plan to roll clusterName back one minor
// version from live state. Steps that live state already satisfies are
// completed, so a rerun after a stopped run continues, and a rerun after a
// finished rollback has nothing to do.
func (s *Service) BuildRollbackPlan(ctx context.Context, clusterName string, opts RollbackOptions) (*RollbackPlan, error) {
	cluster, err := s.describeCluster(ctx, clusterName)
	if err != nil {
		return nil, err
	}
	current := aws.ToString(cluster.Version)
	h, err := s.versionHistory(ctx, clusterName, current)
	if err != nil {
		return nil, err
	}

	plan := &RollbackPlan{ClusterName: clusterName, CurrentVersion: current, Steps: []Step{}, Failures: diag.List{}}
	eligibility := Step{Type: StepReadiness, Description: "rollback eligibility", Status: StatusPending}
	inFlightRollback := h.inFlight != nil && h.inFlight.Type == ekstypes.UpdateTypeVersionRollback

	if h.rolledBack() && !inFlightRollback {
		// A rerun after a finished rollback: the control plane is back at
		// its target. Only work left at the target (if any) remains.
		plan.RolledBack = true
		plan.TargetVersion = current
		eligibility.Status = StatusCompleted
		eligibility.Reason = fmt.Sprintf("the control plane rolled back from %s to %s on about %s (update %s)",
			nextMinor(current), current, aboutDate(aws.ToTime(h.rollback.CreatedAt)), aws.ToString(h.rollback.Id))
		eligibility.Version = current
		plan.Steps = append(plan.Steps, eligibility)
		if err := s.rollbackWorkSteps(ctx, plan, cluster, opts); err != nil {
			return nil, err
		}
		return plan, nil
	}

	target, err := previousMinor(current)
	if err != nil {
		return nil, err
	}
	plan.TargetVersion = target
	eligibility.Version = target

	var blockers, reasons []string
	switch {
	case inFlightRollback:
		reasons = append(reasons, fmt.Sprintf("the rollback to %s is in progress (update %s); refresh waits for it", updateVersion(h.inFlight), aws.ToString(h.inFlight.Id)))
	case cluster.Status != ekstypes.ClusterStatusActive || h.inFlight != nil:
		blockers = append(blockers, fmt.Sprintf("the cluster is %s with an update in progress; EKS rolls back only an ACTIVE cluster with no update in progress: wait for it to finish, then rerun", cluster.Status))
	case h.upgrade == nil:
		blockers = append(blockers, fmt.Sprintf("the update history has no in-place upgrade to %s: a cluster created at its version cannot roll back", current))
	default:
		started := aws.ToTime(h.upgrade.CreatedAt)
		until := started.Add(RollbackWindow)
		plan.UpgradedAt, plan.AvailableUntil = &started, &until
		if !s.clock().Before(until) {
			blockers = append(blockers, fmt.Sprintf("the upgrade to %s started about %s; the 7-day rollback window closed about %s", current, aboutDate(started), aboutDate(until)))
		} else {
			reasons = append(reasons, fmt.Sprintf("upgraded to %s about %s; rollback available until about %s", current, aboutDate(started), aboutDate(until)))
		}
	}
	if !inFlightRollback {
		blockers = append(blockers, s.rollbackTargetBlockers(ctx, plan, cluster, target)...)
	}
	if len(blockers) > 0 {
		eligibility.Status = StatusBlocked
		eligibility.Reason = strings.Join(blockers, "; ")
	} else {
		eligibility.Reason = strings.Join(reasons, "; ")
	}
	plan.Steps = append(plan.Steps, eligibility)
	if !inFlightRollback {
		plan.Steps = append(plan.Steps, s.rollbackInsightsStep(ctx, plan, opts))
	}

	if err := s.rollbackWorkSteps(ctx, plan, cluster, opts); err != nil {
		return nil, err
	}

	cp := Step{
		Type:        StepControlPlane,
		Description: fmt.Sprintf("control plane rollback %s → %s", current, target),
		Version:     target,
		Status:      StatusPending,
	}
	switch {
	case inFlightRollback:
		cp.Reason = "the rollback is in progress; refresh attaches and waits"
	case opts.SkipInsightsCheck:
		cp.Reason = "--skip-insights-check: EKS skips its rollback-readiness insight checks (force)"
	}
	plan.Steps = append(plan.Steps, cp)

	plan.Notices = append(plan.Notices, fmt.Sprintf(
		"self-managed and hybrid nodes are not rolled back by EKS or refresh: move them to %s yourself before the control plane rolls back. Fargate pods at %s trigger the kubelet skew insight: delete them first, and they come back at %s", target, current, target))
	if err := ctx.Err(); err != nil {
		return nil, stopped(ctx, "while building the rollback plan", "", err)
	}
	return plan, nil
}

// rollbackTargetBlockers checks that EKS offers target and that the upgrade
// policy allows it: a version in extended support needs the EXTENDED policy.
func (s *Service) rollbackTargetBlockers(ctx context.Context, plan *RollbackPlan, cluster *ekstypes.Cluster, target string) []string {
	out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeClusterVersionsOutput, error) {
		return s.eksClient.DescribeClusterVersions(rc, &eks.DescribeClusterVersionsInput{ClusterVersions: []string{target}})
	})
	if err != nil {
		plan.addFailure(diag.FromError(diag.KindCluster, plan.ClusterName, diag.OpDescribeClusterVersions, err))
		plan.Notices = append(plan.Notices, fmt.Sprintf("could not check the support status of %s; EKS rejects the rollback if it is not supported", target))
		return nil
	}
	for _, v := range out.ClusterVersions {
		if aws.ToString(v.ClusterVersion) != target {
			continue
		}
		switch {
		case v.VersionStatus == ekstypes.VersionStatusUnsupported || v.Status == ekstypes.ClusterVersionStatusUnsupported:
			return []string{fmt.Sprintf("%s is no longer supported by EKS", target)}
		case v.VersionStatus == ekstypes.VersionStatusExtendedSupport || v.Status == ekstypes.ClusterVersionStatusExtendedSupport:
			if status.SupportTypeOf(cluster) != ekstypes.SupportTypeExtended {
				return []string{fmt.Sprintf("%s is in extended support, and the cluster's upgrade policy is %s: change it to EXTENDED first (aws eks update-cluster-config --name %s --upgrade-policy supportType=EXTENDED); extended support charges apply",
					target, orUnset(string(status.SupportTypeOf(cluster))), plan.ClusterName)}
			}
			plan.Notices = append(plan.Notices, fmt.Sprintf("%s is in extended support: extended support charges apply after the rollback", target))
		}
		return nil
	}
	return []string{fmt.Sprintf("EKS does not offer %s", target)}
}

func orUnset(s string) string {
	if s == "" {
		return "not set"
	}
	return s
}

// rollbackInsightsStep reads the ROLLBACK_READINESS insights. ERROR and
// UNKNOWN block unless opts.SkipInsightsCheck; WARNING is a notice. EKS
// refreshes stale insights itself when the rollback starts, so refresh
// starts no refresh, and a failed read does not block.
func (s *Service) rollbackInsightsStep(ctx context.Context, plan *RollbackPlan, opts RollbackOptions) Step {
	step := Step{Type: StepReadiness, Description: "rollback readiness insights", Version: plan.TargetVersion, Status: StatusPending}
	insights, err := s.listRollbackInsights(ctx, plan.ClusterName)
	if err != nil {
		plan.addFailure(diag.FromError(diag.KindCluster, plan.ClusterName, diag.OpListInsights, err))
		step.Reason = "could not read the rollback readiness insights; EKS checks them when the rollback starts"
		return step
	}
	if len(insights) == 0 {
		step.Reason = "none reported yet; EKS evaluates them when the rollback starts"
		return step
	}
	var blocking, warnings []string
	for _, in := range insights {
		var v ekstypes.InsightStatusValue
		if in.InsightStatus != nil {
			v = in.InsightStatus.Status
		}
		switch v {
		case ekstypes.InsightStatusValuePassing:
		case ekstypes.InsightStatusValueWarning:
			warnings = append(warnings, insightName(in))
		case ekstypes.InsightStatusValueError:
			blocking = append(blocking, insightName(in)+" (ERROR)")
		default:
			blocking = append(blocking, insightName(in)+" (UNKNOWN)")
		}
	}
	if len(warnings) > 0 {
		plan.Notices = append(plan.Notices, "rollback insight warnings: "+strings.Join(warnings, ", "))
	}
	switch {
	case len(blocking) > 0 && opts.SkipInsightsCheck:
		step.Reason = fmt.Sprintf("%d blocking insight(s) bypassed with --skip-insights-check: %s", len(blocking), strings.Join(blocking, ", "))
		plan.Notices = append(plan.Notices, step.Reason+"; EKS cannot guarantee a safe rollback")
	case len(blocking) > 0:
		step.Status = StatusBlocked
		step.Reason = fmt.Sprintf("%d blocking insight(s): %s; resolve them (refresh cluster upgrade-check --category ROLLBACK_READINESS), or pass --skip-insights-check to roll back anyway (not recommended)",
			len(blocking), strings.Join(blocking, ", "))
	default:
		step.Reason = fmt.Sprintf("%d insight(s), none blocking", len(insights))
	}
	return step
}

// rollbackWorkSteps appends one step per nodegroup, then one per add-on, for
// the move to plan.TargetVersion, and the EKS Auto Mode notice.
func (s *Service) rollbackWorkSteps(ctx context.Context, plan *RollbackPlan, cluster *ekstypes.Cluster, opts RollbackOptions) error {
	target := plan.TargetVersion
	if cluster.ComputeConfig != nil && aws.ToBool(cluster.ComputeConfig.Enabled) {
		plan.Notices = append(plan.Notices, "EKS Auto Mode: EKS rolls back the Auto Mode nodes itself before the control plane, so the rollback takes longer")
	}

	nodegroups, err := s.listNodegroupStates(ctx, plan.ClusterName)
	if err != nil {
		return err
	}
	for _, ng := range nodegroups {
		step := Step{
			Type:        StepNodegroup,
			Target:      ng.Name,
			Description: fmt.Sprintf("nodegroup %s %s → %s", ng.Name, ng.Version, target),
			Version:     target,
			Status:      StatusPending,
		}
		switch {
		case versionAtLeast(target, ng.Version):
			step.Status = StatusCompleted
			step.Description = fmt.Sprintf("nodegroup %s → %s", ng.Name, target)
			step.Reason = fmt.Sprintf("already at %s", ng.Version)
		case matchesAny(ng.Name, opts.SkipNodegroups):
			step.Status = StatusManual
			step.Reason = fmt.Sprintf("skipped via --skip-nodegroup: move it to %s yourself before the control plane rolls back", target)
		case ng.CustomAMI:
			step.Status = StatusManual
			step.Reason = fmt.Sprintf("custom AMI nodegroup: roll it to a %s AMI yourself before the control plane rolls back", target)
		case ng.Status == ekstypes.NodegroupStatusUpdating:
			step.Reason = "an update is already in progress; refresh attaches and waits for it"
		}
		plan.Steps = append(plan.Steps, step)
	}

	svc := s.addonsService()
	addonList, err := svc.List(ctx, plan.ClusterName, addons.ListOptions{})
	if err != nil {
		return err
	}
	for _, a := range addons.SortByDependency(addonList) {
		step := Step{
			Type:        StepAddon,
			Target:      a.Name,
			Description: fmt.Sprintf("addon %s → compatible with %s", a.Name, target),
			Status:      StatusPending,
		}
		versions, err := svc.GetAvailableVersions(ctx, a.Name, target)
		if err != nil {
			step.Status = StatusBlocked
			if errors.Is(err, addons.ErrNoVersionsFound) {
				step.Reason = fmt.Sprintf("no version of %s is compatible with %s: %v", a.Name, target, err)
			} else {
				step.Reason = fmt.Sprintf("could not look up versions of %s compatible with %s (rerun to retry): %v", a.Name, target, err)
				f := diag.FromError(diag.KindAddon, a.Name, diag.OpDescribeAddonVersions, err)
				f.Cluster = plan.ClusterName
				plan.addFailure(f)
			}
			plan.Steps = append(plan.Steps, step)
			continue
		}
		if versionListed(a.Version, versions) {
			step.Status = StatusCompleted
			step.Version = a.Version
			step.Reason = fmt.Sprintf("%s is compatible with %s", a.Version, target)
		} else {
			step.Version = versions[0].Version
			step.Description = fmt.Sprintf("addon %s %s → %s (newest compatible with %s)", a.Name, a.Version, step.Version, target)
			step.Reason = fmt.Sprintf("%s is not compatible with %s", a.Version, target)
		}
		plan.Steps = append(plan.Steps, step)
	}
	return nil
}

// ExecuteRollback runs the plan: nodegroup rollbacks, then add-on
// downgrades, then the control-plane rollback, with a confirmation before
// each phase unless opts.Yes. opts.Force is the nodegroup rolls' Force;
// opts.SkipInsightsCheck sets Force on UpdateClusterVersion;
// opts.RollbackTimeout sets RollbackConfig.TimeoutMinutes.
func (s *Service) ExecuteRollback(ctx context.Context, plan *RollbackPlan, opts ExecuteOptions) (*Report, error) {
	report := NewReport()
	if plan.Blocked() {
		report.Status = RunBlocked
		return report, fmt.Errorf("rollback plan has unresolved blockers; refusing to execute:\n  %s", joinLines(plan.Blockers()))
	}
	if err := ValidateRollbackTimeout(opts.RollbackTimeout); err != nil {
		report.Status = RunFailed
		return report, err
	}
	return runPhases(ctx, plan.ClusterName, s.rollbackPhases(plan, opts), opts, report)
}

// rollbackPhases lists the rollback phases in the documented order.
func (s *Service) rollbackPhases(plan *RollbackPlan, opts ExecuteOptions) []phase {
	target := plan.TargetVersion
	var ngSteps, addonSteps, cpSteps []Step
	var ngNames, addonNames []string
	for _, st := range plan.Steps {
		if st.Status != StatusPending {
			continue
		}
		switch st.Type {
		case StepNodegroup:
			ngSteps = append(ngSteps, st)
			ngNames = append(ngNames, st.Target)
		case StepAddon:
			addonSteps = append(addonSteps, st)
			addonNames = append(addonNames, st.Target)
		case StepControlPlane:
			cpSteps = append(cpSteps, st)
		}
	}
	atOrBelow := func(version string) bool { return versionAtLeast(target, version) }
	return []phase{
		{
			label: fmt.Sprintf("nodegroup rollbacks to %s (%d nodegroup(s))", target, len(ngSteps)),
			steps: ngSteps,
			run: func(ctx context.Context) error {
				return s.rollNodegroupsTo(ctx, plan.ClusterName, target, NodegroupRollOptions{
					SkipPatterns: opts.SkipNodegroups,
					Only:         ngNames,
					Force:        opts.Force,
					Gate:         opts.NodegroupGate,
					Observer:     opts.NodegroupObserver,
				}, atOrBelow, opts.Progress)
			},
		},
		{
			label: fmt.Sprintf("addon downgrades for %s (%d addon(s), dependency order)", target, len(addonSteps)),
			steps: addonSteps,
			run: func(ctx context.Context) error {
				return s.updateAddonsFor(ctx, plan.ClusterName, target, nil, addonNames,
					func(current string, versions []addons.AddonVersionInfo) (bool, string) {
						if versionListed(current, versions) {
							return true, fmt.Sprintf("at %s, compatible with %s", current, target)
						}
						return false, ""
					}, opts.Progress)
			},
		},
		{
			label: fmt.Sprintf("control plane rollback %s → %s", plan.CurrentVersion, target),
			steps: cpSteps,
			run: func(ctx context.Context) error {
				return s.moveControlPlane(ctx, plan.ClusterName, target, "rollback", atOrBelow,
					func(in *eks.UpdateClusterVersionInput) {
						in.Force = opts.SkipInsightsCheck
						if opts.RollbackTimeout > 0 {
							in.RollbackConfig = &ekstypes.RollbackConfig{TimeoutMinutes: aws.Int32(int32(opts.RollbackTimeout / time.Minute))} //nolint:gosec // ValidateRollbackTimeout bounds it to 120..10080
						}
					}, opts.Progress)
			},
		},
	}
}
