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
	"github.com/dantech2000/refresh/internal/services/common"
)

// EKSAPI abstracts the EKS client methods used for addons
type EKSAPI interface {
	ListAddons(ctx context.Context, params *eks.ListAddonsInput, optFns ...func(*eks.Options)) (*eks.ListAddonsOutput, error)
	DescribeAddon(ctx context.Context, params *eks.DescribeAddonInput, optFns ...func(*eks.Options)) (*eks.DescribeAddonOutput, error)
	DescribeAddonVersions(ctx context.Context, params *eks.DescribeAddonVersionsInput, optFns ...func(*eks.Options)) (*eks.DescribeAddonVersionsOutput, error)
	UpdateAddon(ctx context.Context, params *eks.UpdateAddonInput, optFns ...func(*eks.Options)) (*eks.UpdateAddonOutput, error)
	DescribeCluster(ctx context.Context, params *eks.DescribeClusterInput, optFns ...func(*eks.Options)) (*eks.DescribeClusterOutput, error)
}

// Service defines addon operations
type Service interface {
	List(ctx context.Context, clusterName string, options ListOptions) ([]AddonSummary, error)
	Describe(ctx context.Context, clusterName, addonName string, options DescribeOptions) (*AddonDetails, error)
	Update(ctx context.Context, clusterName, addonName string, options UpdateOptions) (*AddonUpdateResult, error)
	UpdateAll(ctx context.Context, clusterName string, options UpdateAllOptions) ([]AddonUpdateResult, error)
	GetAvailableVersions(ctx context.Context, addonName string, k8sVersion string) ([]AddonVersionInfo, error)
}

// ServiceImpl implements the addon Service
type ServiceImpl struct {
	eksClient EKSAPI
	logger    *slog.Logger
}

// NewService creates a new addon service
func NewService(eksClient EKSAPI, logger *slog.Logger) *ServiceImpl {
	return &ServiceImpl{
		eksClient: eksClient,
		logger:    logger,
	}
}

// List returns all addons for a cluster
func (s *ServiceImpl) List(ctx context.Context, clusterName string, options ListOptions) ([]AddonSummary, error) {
	s.logger.Info("listing addons", "cluster", clusterName)

	addonNames, err := common.Paginate(ctx, func(rc context.Context, token *string) ([]string, *string, error) {
		out, err := common.WithRetry(rc, common.DefaultRetryConfig, func(rrc context.Context) (*eks.ListAddonsOutput, error) {
			return s.eksClient.ListAddons(rrc, &eks.ListAddonsInput{
				ClusterName: aws.String(clusterName),
				NextToken:   token,
			})
		})
		if err != nil {
			return nil, nil, fmt.Errorf("listing addons: %w", err)
		}
		return out.Addons, out.NextToken, nil
	})
	if err != nil {
		return nil, err
	}

	summaries := make([]AddonSummary, 0, len(addonNames))
	for _, name := range addonNames {
		desc, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeAddonOutput, error) {
			return s.eksClient.DescribeAddon(rc, &eks.DescribeAddonInput{
				ClusterName: aws.String(clusterName),
				AddonName:   aws.String(name),
			})
		})
		if err != nil || desc.Addon == nil {
			summaries = append(summaries, AddonSummary{
				Name:   name,
				Status: "UNKNOWN",
				Health: "Unknown",
			})
			continue
		}

		health := ""
		if options.ShowHealth {
			health = mapAddonHealth(desc.Addon.Status)
		}

		summaries = append(summaries, AddonSummary{
			Name:    aws.ToString(desc.Addon.AddonName),
			Version: aws.ToString(desc.Addon.AddonVersion),
			Status:  string(desc.Addon.Status),
			Health:  health,
		})
	}

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
		details.Configuration = map[string]any{"raw": *addon.ConfigurationValues}
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

	targetVersion := options.Version
	if strings.EqualFold(targetVersion, "latest") || targetVersion == "" {
		versions, err := s.GetAvailableVersions(ctx, addonName, "")
		if err != nil {
			return nil, fmt.Errorf("resolving latest version: %w", err)
		}
		if len(versions) == 0 {
			return nil, fmt.Errorf("no versions available for addon %s", addonName)
		}
		targetVersion = versions[0].Version
	}

	// Validate the target version is compatible with the cluster's Kubernetes version.
	// For explicitly-specified versions this catches mismatches early; for "latest" it
	// confirms the resolved version is valid for this cluster.
	if err := s.validateVersionCompatibility(ctx, clusterName, addonName, targetVersion); err != nil {
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
		if err := s.waitForAddonUpdate(waitCtx, clusterName, addonName); err != nil {
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

	results := make([]AddonUpdateResult, 0, len(toUpdate))

	if options.Parallel {
		var mu sync.Mutex
		var wg sync.WaitGroup
		semaphore := make(chan struct{}, 3)

		for _, addon := range toUpdate {
			wg.Add(1)
			go func(a AddonSummary) {
				defer wg.Done()
				semaphore <- struct{}{}
				defer func() { <-semaphore }()

				result, err := s.Update(ctx, clusterName, a.Name, UpdateOptions{
					Version:     "latest",
					DryRun:      options.DryRun,
					HealthCheck: options.HealthCheck,
					Wait:        options.Wait,
					WaitTimeout: options.WaitTimeout,
				})

				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					results = append(results, AddonUpdateResult{
						AddonName:       a.Name,
						PreviousVersion: a.Version,
						Status:          fmt.Sprintf("FAILED: %v", err),
					})
				} else {
					results = append(results, *result)
				}
			}(addon)
		}
		wg.Wait()
	} else {
		for _, addon := range toUpdate {
			result, err := s.Update(ctx, clusterName, addon.Name, UpdateOptions{
				Version:     "latest",
				DryRun:      options.DryRun,
				HealthCheck: options.HealthCheck,
				Wait:        options.Wait,
				WaitTimeout: options.WaitTimeout,
			})
			if err != nil {
				results = append(results, AddonUpdateResult{
					AddonName:       addon.Name,
					PreviousVersion: addon.Version,
					Status:          fmt.Sprintf("FAILED: %v", err),
				})
			} else {
				results = append(results, *result)
			}
		}
	}

	return results, nil
}
