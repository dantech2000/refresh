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
	"gopkg.in/yaml.v3"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
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
// EKS returns the underlying EKS client abstraction so the command layer can do
// lightweight lookups (e.g. resolving an add-on name) without building a second
// client. EKSAPI includes ListAddons, so it satisfies the resolver's interface.
func (s *ServiceImpl) EKS() EKSAPI { return s.eksClient }

func NewService(eksClient EKSAPI, logger *slog.Logger) *ServiceImpl {
	return &ServiceImpl{
		eksClient: eksClient,
		logger:    logger,
	}
}

// List returns all addons for a cluster
func (s *ServiceImpl) List(ctx context.Context, clusterName string, options ListOptions) ([]AddonSummary, error) {
	s.logger.Info("listing addons", "cluster", clusterName)

	addonNames, err := awsinternal.ListAllPages(ctx, fmt.Sprintf("listing add-ons for cluster %s", clusterName),
		func(rc context.Context, token *string) (*eks.ListAddonsOutput, error) {
			return s.eksClient.ListAddons(rc, &eks.ListAddonsInput{
				ClusterName: aws.String(clusterName),
				NextToken:   token,
			})
		},
		func(out *eks.ListAddonsOutput) ([]string, *string) { return out.Addons, out.NextToken },
	)
	if err != nil {
		return nil, err
	}

	summaries := common.ForEachParallel(ctx, addonNames, common.DefaultItemConcurrency,
		func(fctx context.Context, name string) AddonSummary {
			desc, err := common.WithRetry(fctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeAddonOutput, error) {
				return s.eksClient.DescribeAddon(rc, &eks.DescribeAddonInput{
					ClusterName: aws.String(clusterName),
					AddonName:   aws.String(name),
				})
			})
			if err != nil || desc.Addon == nil {
				return AddonSummary{
					Name:   name,
					Status: "UNKNOWN",
					Health: "Unknown",
				}
			}

			health := ""
			if options.ShowHealth {
				health = mapAddonHealth(desc.Addon.Status)
			}

			return AddonSummary{
				Name:    aws.ToString(desc.Addon.AddonName),
				Version: aws.ToString(desc.Addon.AddonVersion),
				Status:  string(desc.Addon.Status),
				Health:  health,
			}
		})

	return summaries, nil
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

// Update updates an addon to a specified version
func (s *ServiceImpl) Update(ctx context.Context, clusterName, addonName string, options UpdateOptions) (*AddonUpdateResult, error) {
	s.logger.Info("updating addon", "cluster", clusterName, "addon", addonName, "version", options.Version)

	// Resolve the cluster's Kubernetes version once; it scopes "latest"
	// resolution to versions this cluster can actually run, and backs the
	// compatibility validation for pinned versions.
	k8sVersion := s.clusterK8sVersion(ctx, clusterName)

	targetVersion := options.Version
	if strings.EqualFold(targetVersion, "latest") || targetVersion == "" {
		versions, err := s.GetAvailableVersions(ctx, addonName, k8sVersion)
		if err != nil {
			return nil, fmt.Errorf("resolving latest version: %w", err)
		}
		targetVersion = versions[0].Version
	} else if err := s.validateVersionCompatibility(ctx, k8sVersion, addonName, targetVersion); err != nil {
		// Explicitly-specified versions are validated against the cluster's
		// Kubernetes version to catch mismatches early. ("latest" is already
		// scoped above, so re-validating it would be redundant.)
		return nil, err
	}

	currentDesc, err := s.eksClient.DescribeAddon(ctx, &eks.DescribeAddonInput{
		ClusterName: aws.String(clusterName),
		AddonName:   aws.String(addonName),
	})
	if err != nil {
		return nil, fmt.Errorf("getting current addon version: %w", err)
	}
	previousVersion := aws.ToString(currentDesc.Addon.AddonVersion)

	// Pre-update health check: refuse to update while the addon is mid-operation.
	if options.HealthCheck {
		if err := s.preUpdateHealthCheck(ctx, clusterName, addonName); err != nil {
			return nil, err
		}
	}

	result := &AddonUpdateResult{
		AddonName:       addonName,
		PreviousVersion: previousVersion,
		NewVersion:      targetVersion,
		StartedAt:       time.Now(),
	}

	if options.DryRun {
		result.Status = "DRY_RUN"
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
		return nil, fmt.Errorf("updating addon: %w", err)
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
		if err := s.waitForAddonUpdate(waitCtx, clusterName, addonName, options.PollInterval); err != nil {
			result.Status = "WAIT_FAILED"
			return result, err
		}
		result.Status = "COMPLETED"
		if err := s.postUpdateHealthCheck(ctx, clusterName, addonName); err != nil {
			result.Status = "COMPLETED_WITH_ISSUES"
			result.HealthIssues = err.Error()
			s.logger.Warn("post-update health check found issues", "addon", addonName, "issues", err)
		}
	}

	return result, nil
}

// UpdateAll updates all addons to their latest versions
func (s *ServiceImpl) UpdateAll(ctx context.Context, clusterName string, options UpdateAllOptions) ([]AddonUpdateResult, error) {
	s.logger.Info("updating all addons", "cluster", clusterName)

	addons, err := s.List(ctx, clusterName, ListOptions{})
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

	updateOne := func(a AddonSummary) AddonUpdateResult {
		result, err := s.Update(ctx, clusterName, a.Name, UpdateOptions{
			Version:     "latest",
			DryRun:      options.DryRun,
			HealthCheck: options.HealthCheck,
			Wait:        options.Wait,
			WaitTimeout: options.WaitTimeout,
		})
		if err != nil {
			return AddonUpdateResult{
				AddonName:       a.Name,
				PreviousVersion: a.Version,
				Status:          fmt.Sprintf("FAILED: %v", err),
			}
		}
		return *result
	}

	results := make([]AddonUpdateResult, len(toUpdate))
	if options.Parallel {
		var wg sync.WaitGroup
		semaphore := make(chan struct{}, maxParallelAddonUpdates)
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
			wg.Add(1)
			go func(i int, a AddonSummary) {
				defer wg.Done()
				defer func() { <-semaphore }()
				results[i] = updateOne(a)
			}(i, addon)
		}
		wg.Wait()
	} else {
		for i, addon := range toUpdate {
			results[i] = updateOne(addon)
		}
	}

	return results, nil
}
