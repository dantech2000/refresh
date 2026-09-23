// Package addons lists, describes, and updates EKS add-ons, including version
// compatibility and post-update health checks.
package addons

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"gopkg.in/yaml.v3"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/aws/awserr"
	"github.com/dantech2000/refresh/internal/services/common"
)

const (
	// maxParallelAddonUpdates caps concurrent UpdateAddon calls when
	// UpdateOptions.Parallel is set.
	maxParallelAddonUpdates = 3
	// addonUpdatePollInterval is how often waitForAddonUpdate re-checks an
	// in-flight addon update.
	addonUpdatePollInterval = 5 * time.Second
)

// EKSAPI abstracts the EKS client methods used for addons
type EKSAPI interface {
	ListAddons(ctx context.Context, params *eks.ListAddonsInput, optFns ...func(*eks.Options)) (*eks.ListAddonsOutput, error)
	DescribeAddon(ctx context.Context, params *eks.DescribeAddonInput, optFns ...func(*eks.Options)) (*eks.DescribeAddonOutput, error)
	DescribeAddonVersions(ctx context.Context, params *eks.DescribeAddonVersionsInput, optFns ...func(*eks.Options)) (*eks.DescribeAddonVersionsOutput, error)
	UpdateAddon(ctx context.Context, params *eks.UpdateAddonInput, optFns ...func(*eks.Options)) (*eks.UpdateAddonOutput, error)
	DescribeUpdate(ctx context.Context, params *eks.DescribeUpdateInput, optFns ...func(*eks.Options)) (*eks.DescribeUpdateOutput, error)
	DescribeCluster(ctx context.Context, params *eks.DescribeClusterInput, optFns ...func(*eks.Options)) (*eks.DescribeClusterOutput, error)
}

// ServiceImpl is the addon service.
type ServiceImpl struct {
	eksClient EKSAPI
	logger    *slog.Logger

	// k8sVersions memoizes cluster name -> Kubernetes version so UpdateAll
	// doesn't re-describe the cluster for every addon.
	k8sVersions sync.Map
}

// NewService creates a new addon service
func NewService(eksClient EKSAPI, logger *slog.Logger) *ServiceImpl {
	return &ServiceImpl{
		eksClient: eksClient,
		logger:    logger,
	}
}

// List returns all addons for a cluster. An add-on that could not be
// described (API error, or never reached because ctx ended) is still listed,
// by name, with Status UNKNOWN and no version, so callers that count
// installed add-ons see every one. Use ListDetailed to get the reasons.
func (s *ServiceImpl) List(ctx context.Context, clusterName string, options ListOptions) ([]AddonSummary, error) {
	results, err := s.listAddons(ctx, clusterName, options)
	if err != nil {
		return nil, err
	}
	summaries := make([]AddonSummary, 0, len(results))
	for _, r := range results {
		if r.summary != nil {
			summaries = append(summaries, *r.summary)
			continue
		}
		unknown := AddonSummary{Name: r.name, Status: "UNKNOWN"}
		if options.ShowHealth {
			unknown.Health = "UNKNOWN"
		}
		summaries = append(summaries, unknown)
	}
	return summaries, nil
}

// ListDetailed lists a cluster's add-ons and keeps the ones that could not be
// described apart from the ones that were (see ListResult).
func (s *ServiceImpl) ListDetailed(ctx context.Context, clusterName string, options ListOptions) (ListResult, error) {
	results, err := s.listAddons(ctx, clusterName, options)
	if err != nil {
		return ListResult{}, err
	}
	res := ListResult{Summaries: make([]AddonSummary, 0, len(results))}
	for _, r := range results {
		if r.summary != nil {
			res.Summaries = append(res.Summaries, *r.summary)
			continue
		}
		res.Failures = append(res.Failures, r.name+": "+r.failure)
	}
	return res, nil
}

// addonResult is one add-on's outcome in listAddons: summary is nil when the
// add-on could not be described, and failure then says why.
type addonResult struct {
	name    string
	summary *AddonSummary
	failure string
}

// listAddons describes every installed add-on in parallel. Each result is
// named, including add-ons never dispatched because ctx ended first.
func (s *ServiceImpl) listAddons(ctx context.Context, clusterName string, options ListOptions) ([]addonResult, error) {
	s.logger.Info("listing addons", "cluster", clusterName)

	addonNames, err := s.ListAddonNames(ctx, clusterName)
	if err != nil {
		return nil, err
	}

	type outcome struct {
		done bool // false for items ForEachParallel never dispatched
		addonResult
	}
	outcomes := common.ForEachParallel(ctx, addonNames, common.DefaultItemConcurrency,
		func(fctx context.Context, name string) outcome {
			desc, err := common.WithRetry(fctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeAddonOutput, error) {
				return s.eksClient.DescribeAddon(rc, &eks.DescribeAddonInput{
					ClusterName: aws.String(clusterName),
					AddonName:   aws.String(name),
				})
			})
			if err != nil {
				s.logger.Warn("could not describe addon", "cluster", clusterName, "addon", name, "error", err)
				return outcome{done: true, addonResult: addonResult{name: name, failure: awserr.Summary(err)}}
			}
			if desc == nil || desc.Addon == nil {
				return outcome{done: true, addonResult: addonResult{name: name, failure: "empty DescribeAddon response"}}
			}

			health := ""
			if options.ShowHealth {
				health = mapAddonHealth(desc.Addon.Status)
			}
			return outcome{done: true, addonResult: addonResult{name: name, summary: &AddonSummary{
				Name:    aws.ToString(desc.Addon.AddonName),
				Version: aws.ToString(desc.Addon.AddonVersion),
				Status:  string(desc.Addon.Status),
				Health:  health,
			}}}
		})

	results := make([]addonResult, len(outcomes))
	for i, o := range outcomes {
		if !o.done {
			reason := "listing stopped early"
			if cerr := ctx.Err(); cerr != nil {
				reason = cerr.Error()
			}
			results[i] = addonResult{name: addonNames[i], failure: "not described: " + reason}
			continue
		}
		results[i] = o.addonResult
	}
	return results, nil
}

// ListAddonNames returns the names of every add-on installed on the cluster,
// following ListAddons pagination.
func (s *ServiceImpl) ListAddonNames(ctx context.Context, clusterName string) ([]string, error) {
	return awsinternal.ListAllPages(ctx, fmt.Sprintf("listing add-ons for cluster %s", clusterName),
		func(rc context.Context, token *string) (*eks.ListAddonsOutput, error) {
			return s.eksClient.ListAddons(rc, &eks.ListAddonsInput{
				ClusterName: aws.String(clusterName),
				NextToken:   token,
			})
		},
		func(out *eks.ListAddonsOutput) ([]string, *string) { return out.Addons, out.NextToken },
	)
}

// Describe returns detailed information about an addon
func (s *ServiceImpl) Describe(ctx context.Context, clusterName, addonName string, options DescribeOptions) (*AddonDetails, error) {
	s.logger.Info("describing addon", "cluster", clusterName, "addon", addonName)

	desc, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeAddonOutput, error) {
		return s.eksClient.DescribeAddon(rc, &eks.DescribeAddonInput{
			ClusterName: aws.String(clusterName),
			AddonName:   aws.String(addonName),
		})
	})
	if err != nil {
		return nil, fmt.Errorf("describing addon: %w", err)
	}
	if desc == nil || desc.Addon == nil {
		return nil, fmt.Errorf("describing addon %s: empty DescribeAddon response", addonName)
	}

	addon := desc.Addon
	details := &AddonDetails{
		Name:               aws.ToString(addon.AddonName),
		Version:            aws.ToString(addon.AddonVersion),
		Status:             string(addon.Status),
		Health:             mapAddonHealth(addon.Status),
		ARN:                aws.ToString(addon.AddonArn),
		ServiceAccountRole: aws.ToString(addon.ServiceAccountRoleArn),
		CreatedAt:          addon.CreatedAt,
		ModifiedAt:         addon.ModifiedAt,
	}

	if options.ShowConfiguration && addon.ConfigurationValues != nil && *addon.ConfigurationValues != "" {
		raw := *addon.ConfigurationValues
		var cfgMap map[string]any
		if err := yaml.Unmarshal([]byte(raw), &cfgMap); err == nil {
			details.Configuration = cfgMap
		} else {
			details.Configuration = map[string]any{"raw": raw}
		}
	}

	if addon.Health != nil && len(addon.Health.Issues) > 0 {
		details.Issues = make([]AddonIssue, 0, len(addon.Health.Issues))
		for _, issue := range addon.Health.Issues {
			details.Issues = append(details.Issues, AddonIssue{
				Code:        string(issue.Code),
				Message:     aws.ToString(issue.Message),
				ResourceIDs: issue.ResourceIds,
			})
		}
	}

	if options.ShowVersions {
		versions, err := s.GetAvailableVersions(ctx, addonName, "")
		if err == nil {
			for _, v := range versions {
				details.AvailableVersions = append(details.AvailableVersions, v.Version)
			}
		}
	}

	return details, nil
}

// AddonStatus returns the addon's currently installed version and raw EKS
// lifecycle status (no health mapping), for callers that need to make
// control-flow decisions — e.g. the upgrade orchestrator attaching to an
// in-flight update on resume rather than re-submitting it.
func (s *ServiceImpl) AddonStatus(ctx context.Context, clusterName, addonName string) (version string, status ekstypes.AddonStatus, err error) {
	out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeAddonOutput, error) {
		return s.eksClient.DescribeAddon(rc, &eks.DescribeAddonInput{
			ClusterName: aws.String(clusterName),
			AddonName:   aws.String(addonName),
		})
	})
	if err != nil {
		return "", "", awsinternal.FormatAWSError(err, fmt.Sprintf("describing addon %s", addonName))
	}
	if out.Addon == nil {
		return "", "", fmt.Errorf("describing addon %s: empty response", addonName)
	}
	return aws.ToString(out.Addon.AddonVersion), out.Addon.Status, nil
}

// Update updates an addon to a specified version
func (s *ServiceImpl) Update(ctx context.Context, clusterName, addonName string, options UpdateOptions) (*AddonUpdateResult, error) {
	s.logger.Info("updating addon", "cluster", clusterName, "addon", addonName, "version", options.Version)

	// Resolve the cluster's Kubernetes version once; it scopes "latest"
	// resolution to versions this cluster can actually run, and backs the
	// compatibility validation for pinned versions.
	k8sVersion := s.clusterK8sVersion(ctx, clusterName)

	targetVersion := options.Version
	resolvedLatest := strings.EqualFold(targetVersion, "latest") || targetVersion == ""
	if resolvedLatest {
		if k8sVersion == "" {
			// Without the cluster's Kubernetes version we can't scope "latest" to
			// versions the cluster can actually run. Falling back to the globally
			// newest version risks picking one incompatible with the cluster, so
			// refuse and ask for an explicit version instead of guessing.
			return nil, fmt.Errorf("cannot resolve latest addon version: cluster Kubernetes version could not be determined for %s; set an explicit --version", clusterName)
		}
		versions, err := s.GetAvailableVersions(ctx, addonName, k8sVersion)
		if err != nil {
			return nil, fmt.Errorf("resolving latest version: %w", err)
		}
		if len(versions) == 0 {
			return nil, fmt.Errorf("resolving latest version: no versions available for %s on Kubernetes %s", addonName, k8sVersion)
		}
		targetVersion = versions[0].Version
	} else if err := s.validateVersionCompatibility(ctx, k8sVersion, addonName, targetVersion); err != nil {
		// Explicitly-specified versions are validated against the cluster's
		// Kubernetes version to catch mismatches early. ("latest" is already
		// scoped above, so re-validating it would be redundant.)
		return nil, err
	}

	currentDesc, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeAddonOutput, error) {
		return s.eksClient.DescribeAddon(rc, &eks.DescribeAddonInput{
			ClusterName: aws.String(clusterName),
			AddonName:   aws.String(addonName),
		})
	})
	if err != nil {
		return nil, awsinternal.FormatAWSError(err, fmt.Sprintf("getting the current version of addon %s", addonName))
	}
	if currentDesc == nil || currentDesc.Addon == nil {
		return nil, fmt.Errorf("getting current addon version: empty DescribeAddon response for %s", addonName)
	}
	previousVersion := aws.ToString(currentDesc.Addon.AddonVersion)

	result := &AddonUpdateResult{
		AddonName:       addonName,
		PreviousVersion: previousVersion,
		NewVersion:      targetVersion,
		StartedAt:       time.Now(),
	}

	// Already-current and downgrade guard. A configuration change is a real
	// update even at the same version, so the guard applies only without one.
	if options.Configuration == "" && previousVersion != "" {
		cmp := CompareVersions(previousVersion, targetVersion)
		// "latest" never downgrades: an installed version newer than the
		// newest catalog entry counts as at target.
		atTarget := cmp == 0 || (cmp > 0 && resolvedLatest)
		switch status := currentDesc.Addon.Status; {
		case atTarget && status == ekstypes.AddonStatusActive:
			s.logger.Info("addon already up to date", "addon", addonName, "installed", previousVersion, "target", targetVersion)
			result.NewVersion = previousVersion
			result.Status = StatusUpToDate
			return result, nil
		case atTarget && (status == ekstypes.AddonStatusUpdating || status == ekstypes.AddonStatusCreating):
			// An operation is already in flight at the target: attach to it
			// (with Wait) instead of submitting another update.
			result.NewVersion = previousVersion
			return s.attachInFlight(ctx, clusterName, addonName, result, options)
		case atTarget && cmp > 0:
			// Not ACTIVE, but re-applying "latest" would downgrade: leave it.
			result.NewVersion = previousVersion
			result.Status = StatusUpToDate
			result.Warning = fmt.Sprintf("%s is %s at %s, newer than the latest catalog version %s; not re-applied",
				addonName, status, previousVersion, targetVersion)
			return result, nil
		case atTarget:
			// DEGRADED / *_FAILED at the target: re-apply it, the documented
			// repair path (see preUpdateHealthCheck).
			s.logger.Info("re-applying addon at its current version", "addon", addonName, "version", previousVersion, "status", status)
		case cmp > 0:
			result.Warning = fmt.Sprintf("downgrading %s from %s to %s", addonName, previousVersion, targetVersion)
			s.logger.Warn("addon downgrade requested", "addon", addonName, "installed", previousVersion, "target", targetVersion)
		}
	}

	// Pre-update health check: refuse to update while the addon is mid-operation.
	if options.HealthCheck {
		if err := s.preUpdateHealthCheck(ctx, clusterName, addonName); err != nil {
			return nil, err
		}
	}

	if options.DryRun {
		result.Status = StatusDryRun
		result.UpdateID = "dry-run"
		return result, nil
	}

	input := &eks.UpdateAddonInput{
		ClusterName:  aws.String(clusterName),
		AddonName:    aws.String(addonName),
		AddonVersion: aws.String(targetVersion),
		// Pin the idempotency token so WithRetry re-issues the SAME request
		// instead of submitting a fresh update per attempt.
		ClientRequestToken: aws.String(common.IdempotencyToken()),
	}
	if options.Configuration != "" {
		input.ConfigurationValues = aws.String(options.Configuration)
	}

	out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.UpdateAddonOutput, error) {
		return s.eksClient.UpdateAddon(rc, input)
	})
	if err != nil {
		return nil, awsinternal.FormatAWSError(err, fmt.Sprintf("updating addon %s", addonName))
	}
	if out == nil || out.Update == nil {
		return nil, fmt.Errorf("updating addon: empty Update in UpdateAddon response for %s", addonName)
	}

	result.UpdateID = aws.ToString(out.Update.Id)
	result.Status = string(out.Update.Status)

	if options.Wait {
		waitCtx := ctx
		if options.WaitTimeout > 0 {
			var cancel context.CancelFunc
			waitCtx, cancel = context.WithTimeout(ctx, options.WaitTimeout)
			defer cancel()
		}
		if err := s.waitForAddonUpdate(waitCtx, clusterName, addonName, result.UpdateID, targetVersion, options.PollInterval); err != nil {
			result.Status = StatusWaitFailed
			result.Error = awserr.Summary(err)
			return result, err
		}
		result.Status = StatusCompleted
		if err := s.postUpdateHealthCheck(ctx, clusterName, addonName); err != nil {
			result.Status = StatusCompletedWithIssues
			result.HealthIssues = err.Error()
			s.logger.Warn("post-update health check found issues", "addon", addonName, "issues", err)
		}
	}

	return result, nil
}

// attachInFlight handles an add-on that is already CREATING/UPDATING at the
// target version. Without Wait (or on a dry run) it reports IN_PROGRESS.
// With Wait it waits for the add-on to settle ACTIVE, confirms the version,
// and runs the post-update health check, like a normal waited update.
func (s *ServiceImpl) attachInFlight(ctx context.Context, clusterName, addonName string, result *AddonUpdateResult, options UpdateOptions) (*AddonUpdateResult, error) {
	s.logger.Info("addon update already in progress at target", "addon", addonName, "version", result.NewVersion)
	result.Status = StatusInProgress
	if !options.Wait || options.DryRun {
		return result, nil
	}
	fail := func(err error) (*AddonUpdateResult, error) {
		result.Status = StatusWaitFailed
		result.Error = awserr.Summary(err)
		return result, err
	}
	if err := s.WaitUntilActive(ctx, clusterName, addonName, options.WaitTimeout, options.PollInterval); err != nil {
		return fail(err)
	}
	current, _, err := s.AddonStatus(ctx, clusterName, addonName)
	if err != nil {
		return fail(err)
	}
	if CompareVersions(current, result.NewVersion) != 0 {
		return fail(fmt.Errorf("addon %s settled at %s, not %s", addonName, current, result.NewVersion))
	}
	result.Status = StatusCompleted
	if err := s.postUpdateHealthCheck(ctx, clusterName, addonName); err != nil {
		result.Status = StatusCompletedWithIssues
		result.HealthIssues = err.Error()
	}
	return result, nil
}

// updateAllBudget returns the deadline for an UpdateAll run over n add-ons:
// Timeout, plus WaitTimeout for each serial step when waiting (n steps, or
// n/maxParallelAddonUpdates rounded up when Parallel). Zero means no deadline.
func updateAllBudget(options UpdateAllOptions, n int) time.Duration {
	if options.Timeout <= 0 {
		return 0
	}
	if !options.Wait || options.WaitTimeout <= 0 {
		return options.Timeout
	}
	steps := n
	if options.Parallel {
		steps = (n + maxParallelAddonUpdates - 1) / maxParallelAddonUpdates
	}
	return options.Timeout + time.Duration(steps)*options.WaitTimeout
}

// notAttempted is the result for an add-on UpdateAll never started because
// ctx ended first.
func notAttempted(ctx context.Context, a AddonSummary) AddonUpdateResult {
	reason := "update run stopped early"
	if err := ctx.Err(); err != nil {
		reason = err.Error()
	}
	return AddonUpdateResult{
		AddonName:       a.Name,
		PreviousVersion: a.Version,
		Status:          "FAILED: not attempted: " + reason,
	}
}

// UpdateAll updates all addons to their latest versions
func (s *ServiceImpl) UpdateAll(ctx context.Context, clusterName string, options UpdateAllOptions) ([]AddonUpdateResult, error) {
	s.logger.Info("updating all addons", "cluster", clusterName)

	listCtx, cancelList := ctx, context.CancelFunc(func() {})
	if options.Timeout > 0 {
		listCtx, cancelList = context.WithTimeout(ctx, options.Timeout)
	}
	addons, err := s.List(listCtx, clusterName, ListOptions{})
	cancelList()
	if err != nil {
		return nil, fmt.Errorf("listing addons: %w", err)
	}

	skipSet := make(map[string]bool)
	for _, name := range options.SkipAddons {
		skipSet[strings.ToLower(name)] = true
	}

	var toUpdate []AddonSummary
	for _, addon := range addons {
		if skipSet[strings.ToLower(addon.Name)] {
			s.logger.Info("skipping addon", "addon", addon.Name)
			continue
		}
		toUpdate = append(toUpdate, addon)
	}

	if options.DependencyOrder {
		toUpdate = sortByDependency(toUpdate)
		s.logger.Info("addon update order resolved", "order", addonNames(toUpdate))
	}

	if budget := updateAllBudget(options, len(toUpdate)); budget > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, budget)
		defer cancel()
	}

	updateOne := func(a AddonSummary) AddonUpdateResult {
		result, err := s.Update(ctx, clusterName, a.Name, UpdateOptions{
			Version:     "latest",
			DryRun:      options.DryRun,
			HealthCheck: options.HealthCheck,
			Wait:        options.Wait,
			WaitTimeout: options.WaitTimeout,
		})
		if err != nil && result != nil {
			// The update was submitted but its wait failed: keep the result
			// so the update ID and the reason reach the output.
			return *result
		}
		if err != nil {
			// One line: the status lands in a table cell, and a formatted
			// AWS error carries multi-line remediation text.
			return AddonUpdateResult{
				AddonName:       a.Name,
				PreviousVersion: a.Version,
				Status:          "FAILED: " + awserr.Summary(err),
			}
		}
		return *result
	}

	results := make([]AddonUpdateResult, len(toUpdate))
	if options.Parallel {
		var wg sync.WaitGroup
		semaphore := make(chan struct{}, maxParallelAddonUpdates)
		dispatched := 0
	dispatch:
		for i, addon := range toUpdate {
			// Acquire BEFORE spawning so the cap limits live goroutines, not
			// just in-flight API calls. Observe cancellation at the dispatch
			// point so Ctrl+C stops starting new addon updates promptly. (REF-56)
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				break dispatch
			}
			dispatched++
			wg.Add(1)
			go func(i int, a AddonSummary) {
				defer wg.Done()
				defer func() { <-semaphore }()
				results[i] = updateOne(a)
			}(i, addon)
		}
		wg.Wait()
		// The deadline or Ctrl+C stopped dispatch early. Give every add-on
		// that was never started a named FAILED row, so no blank row renders
		// and the failure count (and exit code) includes it.
		for i := dispatched; i < len(toUpdate); i++ {
			results[i] = notAttempted(ctx, toUpdate[i])
		}
	} else {
		for i, addon := range toUpdate {
			results[i] = updateOne(addon)
		}
	}

	return results, nil
}
