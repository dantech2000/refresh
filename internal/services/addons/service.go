package addons

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
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
	SecurityScan(ctx context.Context, clusterName string, options SecurityScanOptions) (*SecurityScanResult, error)
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

	var addonNames []string
	var nextToken *string
	for {
		out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.ListAddonsOutput, error) {
			return s.eksClient.ListAddons(rc, &eks.ListAddonsInput{
				ClusterName: aws.String(clusterName),
				NextToken:   nextToken,
			})
		})
		if err != nil {
			return nil, fmt.Errorf("listing addons: %w", err)
		}
		addonNames = append(addonNames, out.Addons...)
		if out.NextToken == nil || aws.ToString(out.NextToken) == "" {
			break
		}
		nextToken = out.NextToken
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

	// Parse configuration if requested
	if options.ShowConfiguration && addon.ConfigurationValues != nil && *addon.ConfigurationValues != "" {
		details.Configuration = map[string]interface{}{"raw": *addon.ConfigurationValues}
	}

	// Collect issues if present
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

	// Get available versions if requested
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

	// Resolve target version
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

	// Get current version
	currentDesc, err := s.eksClient.DescribeAddon(ctx, &eks.DescribeAddonInput{
		ClusterName: aws.String(clusterName),
		AddonName:   aws.String(addonName),
	})
	if err != nil {
		return nil, fmt.Errorf("getting current addon version: %w", err)
	}
	previousVersion := aws.ToString(currentDesc.Addon.AddonVersion)

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

	// Perform the update
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

	// Optionally wait for completion
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
	}

	return result, nil
}

// UpdateAll updates all addons to their latest versions
func (s *ServiceImpl) UpdateAll(ctx context.Context, clusterName string, options UpdateAllOptions) ([]AddonUpdateResult, error) {
	s.logger.Info("updating all addons", "cluster", clusterName)

	// Get list of addons
	addons, err := s.List(ctx, clusterName, ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing addons: %w", err)
	}

	// Filter out skipped addons
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

	results := make([]AddonUpdateResult, 0, len(toUpdate))

	if options.Parallel {
		// Update in parallel
		var mu sync.Mutex
		var wg sync.WaitGroup
		semaphore := make(chan struct{}, 3) // Limit concurrency to 3

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
		// Update sequentially
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

// SecurityScan performs a security analysis of cluster addons
func (s *ServiceImpl) SecurityScan(ctx context.Context, clusterName string, options SecurityScanOptions) (*SecurityScanResult, error) {
	s.logger.Info("scanning addons for security issues", "cluster", clusterName)

	// Get cluster info for Kubernetes version
	clusterDesc, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeClusterOutput, error) {
		return s.eksClient.DescribeCluster(rc, &eks.DescribeClusterInput{
			Name: aws.String(clusterName),
		})
	})
	if err != nil {
		return nil, fmt.Errorf("describing cluster: %w", err)
	}
	k8sVersion := aws.ToString(clusterDesc.Cluster.Version)

	// Get list of addons
	addons, err := s.List(ctx, clusterName, ListOptions{ShowHealth: true})
	if err != nil {
		return nil, fmt.Errorf("listing addons: %w", err)
	}

	result := &SecurityScanResult{
		ClusterName: clusterName,
		ScannedAt:   time.Now(),
		Findings:    make([]AddonSecurityFinding, 0),
		Summary: SecuritySummary{
			TotalAddons:   len(addons),
			ScannedAddons: len(addons),
		},
	}

	for _, addon := range addons {
		// Check for outdated addons
		if options.CheckOutdated {
			findings := s.checkOutdated(ctx, addon, k8sVersion)
			result.Findings = append(result.Findings, findings...)
		}

		// Check for known vulnerabilities (placeholder for future CVE database integration)
		if options.CheckVulnerabilities {
			findings := s.checkVulnerabilities(ctx, addon)
			result.Findings = append(result.Findings, findings...)
		}

		// Check for misconfigurations
		if options.CheckMisconfigurations {
			details, err := s.Describe(ctx, clusterName, addon.Name, DescribeOptions{ShowConfiguration: true})
			if err == nil {
				findings := s.checkMisconfigurations(ctx, addon, details)
				result.Findings = append(result.Findings, findings...)
			}
		}
	}

	// Filter by minimum severity
	if options.MinSeverity != "" {
		result.Findings = filterBySeverity(result.Findings, options.MinSeverity)
	}

	// Calculate summary
	for _, finding := range result.Findings {
		switch finding.Severity {
		case "critical":
			result.Summary.CriticalCount++
		case "high":
			result.Summary.HighCount++
		case "medium":
			result.Summary.MediumCount++
		case "low":
			result.Summary.LowCount++
		case "info":
			result.Summary.InfoCount++
		}
		if finding.Category == "outdated" {
			result.Summary.OutdatedCount++
		}
	}

	return result, nil
}

// GetAvailableVersions returns available versions for an addon
func (s *ServiceImpl) GetAvailableVersions(ctx context.Context, addonName string, k8sVersion string) ([]AddonVersionInfo, error) {
	input := &eks.DescribeAddonVersionsInput{
		AddonName: aws.String(addonName),
	}
	if k8sVersion != "" {
		input.KubernetesVersion = aws.String(k8sVersion)
	}

	out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeAddonVersionsOutput, error) {
		return s.eksClient.DescribeAddonVersions(rc, input)
	})
	if err != nil {
		return nil, fmt.Errorf("describing addon versions: %w", err)
	}

	if len(out.Addons) == 0 || len(out.Addons[0].AddonVersions) == 0 {
		return nil, fmt.Errorf("no versions found for addon %s", addonName)
	}

	versions := make([]AddonVersionInfo, 0, len(out.Addons[0].AddonVersions))
	for _, v := range out.Addons[0].AddonVersions {
		var compatibilities []string
		for _, c := range v.Compatibilities {
			if c.ClusterVersion != nil {
				compatibilities = append(compatibilities, *c.ClusterVersion)
			}
		}

		architectures := append([]string{}, v.Architecture...)

		versions = append(versions, AddonVersionInfo{
			Version:           aws.ToString(v.AddonVersion),
			Compatibilities:   compatibilities,
			Architecture:      architectures,
			RequiresIAMPolicy: v.RequiresIamPermissions,
		})
	}

	return versions, nil
}

// waitForAddonUpdate polls until addon update completes
func (s *ServiceImpl) waitForAddonUpdate(ctx context.Context, clusterName, addonName string) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			desc, err := s.eksClient.DescribeAddon(ctx, &eks.DescribeAddonInput{
				ClusterName: aws.String(clusterName),
				AddonName:   aws.String(addonName),
			})
			if err != nil {
				continue
			}
			if desc.Addon.Status == ekstypes.AddonStatusActive {
				return nil
			}
			if desc.Addon.Status == ekstypes.AddonStatusDegraded ||
				desc.Addon.Status == ekstypes.AddonStatusCreateFailed {
				return fmt.Errorf("addon update failed: status %s", desc.Addon.Status)
			}
		}
	}
}

// checkOutdated checks if an addon is outdated
func (s *ServiceImpl) checkOutdated(ctx context.Context, addon AddonSummary, k8sVersion string) []AddonSecurityFinding {
	var findings []AddonSecurityFinding

	versions, err := s.GetAvailableVersions(ctx, addon.Name, k8sVersion)
	if err != nil || len(versions) == 0 {
		return findings
	}

	latestVersion := versions[0].Version
	if addon.Version != latestVersion {
		// Determine severity based on version gap
		severity := "low"
		versionsBehind := countVersionsBehind(addon.Version, versions)
		if versionsBehind >= 3 {
			severity = "high"
		} else if versionsBehind >= 2 {
			severity = "medium"
		}

		findings = append(findings, AddonSecurityFinding{
			AddonName:   addon.Name,
			Severity:    severity,
			Category:    "outdated",
			Title:       fmt.Sprintf("Addon %s is %d version(s) behind", addon.Name, versionsBehind),
			Description: fmt.Sprintf("Current version: %s, Latest version: %s", addon.Version, latestVersion),
			Remediation: fmt.Sprintf("Update to version %s using: refresh addon update %s --version %s", latestVersion, addon.Name, latestVersion),
		})
	}

	return findings
}

// checkVulnerabilities checks for known vulnerabilities (placeholder)
func (s *ServiceImpl) checkVulnerabilities(ctx context.Context, addon AddonSummary) []AddonSecurityFinding {
	// Placeholder: In production, this would query a CVE database
	// For now, we check for known vulnerable versions of common addons
	var findings []AddonSecurityFinding

	// Known vulnerable versions (example - would be from a real CVE database)
	knownVulnerableVersions := map[string][]struct {
		version     string
		severity    string
		description string
	}{
		"vpc-cni": {
			{version: "v1.11.0", severity: "high", description: "CVE-XXXX-XXXX: Network policy bypass vulnerability"},
		},
		"coredns": {
			{version: "v1.8.0", severity: "medium", description: "CVE-XXXX-XXXX: DNS cache poisoning vulnerability"},
		},
	}

	if vulns, exists := knownVulnerableVersions[addon.Name]; exists {
		for _, vuln := range vulns {
			if strings.HasPrefix(addon.Version, vuln.version) {
				findings = append(findings, AddonSecurityFinding{
					AddonName:        addon.Name,
					Severity:         vuln.severity,
					Category:         "vulnerability",
					Title:            fmt.Sprintf("Known vulnerability in %s %s", addon.Name, addon.Version),
					Description:      vuln.description,
					Remediation:      "Update to a newer version of the addon",
					AffectedVersions: []string{vuln.version},
				})
			}
		}
	}

	return findings
}

// checkMisconfigurations checks for addon misconfigurations
func (s *ServiceImpl) checkMisconfigurations(ctx context.Context, addon AddonSummary, details *AddonDetails) []AddonSecurityFinding {
	var findings []AddonSecurityFinding

	// Check for missing service account role (important for IRSA)
	if details.ServiceAccountRole == "" && requiresIRSA(addon.Name) {
		findings = append(findings, AddonSecurityFinding{
			AddonName:   addon.Name,
			Severity:    "medium",
			Category:    "misconfiguration",
			Title:       fmt.Sprintf("Addon %s is not using IRSA", addon.Name),
			Description: "The addon is not configured with an IAM Role for Service Accounts (IRSA), which means it may be using node IAM role with excessive permissions",
			Remediation: "Configure a dedicated IAM role for the addon service account",
		})
	}

	// Check for health issues
	if addon.Health != "PASS" && addon.Health != "" {
		findings = append(findings, AddonSecurityFinding{
			AddonName:   addon.Name,
			Severity:    "high",
			Category:    "misconfiguration",
			Title:       fmt.Sprintf("Addon %s is in unhealthy state", addon.Name),
			Description: fmt.Sprintf("The addon reports status: %s, health: %s", addon.Status, addon.Health),
			Remediation: "Review addon logs and fix configuration issues",
		})
	}

	return findings
}

// Helper functions

func mapAddonHealth(status ekstypes.AddonStatus) string {
	switch status {
	case ekstypes.AddonStatusActive:
		return "PASS"
	case ekstypes.AddonStatusDegraded:
		return "FAIL"
	case ekstypes.AddonStatusCreateFailed, ekstypes.AddonStatusDeleteFailed:
		return "FAIL"
	case ekstypes.AddonStatusCreating, ekstypes.AddonStatusDeleting, ekstypes.AddonStatusUpdating:
		return "IN_PROGRESS"
	default:
		return "UNKNOWN"
	}
}

func countVersionsBehind(current string, versions []AddonVersionInfo) int {
	for i, v := range versions {
		if v.Version == current {
			return i
		}
	}
	return len(versions)
}

func filterBySeverity(findings []AddonSecurityFinding, minSeverity string) []AddonSecurityFinding {
	severityOrder := map[string]int{
		"critical": 0,
		"high":     1,
		"medium":   2,
		"low":      3,
		"info":     4,
	}

	minLevel, ok := severityOrder[minSeverity]
	if !ok {
		return findings
	}

	filtered := make([]AddonSecurityFinding, 0)
	for _, f := range findings {
		if level, exists := severityOrder[f.Severity]; exists && level <= minLevel {
			filtered = append(filtered, f)
		}
	}

	// Sort by severity
	sort.Slice(filtered, func(i, j int) bool {
		return severityOrder[filtered[i].Severity] < severityOrder[filtered[j].Severity]
	})

	return filtered
}

func requiresIRSA(addonName string) bool {
	// Addons that benefit from IRSA
	irsaAddons := map[string]bool{
		"vpc-cni":                         true,
		"aws-ebs-csi-driver":              true,
		"aws-efs-csi-driver":              true,
		"aws-mountpoint-s3-csi-driver":    true,
		"adot":                            true,
		"amazon-cloudwatch-observability": true,
	}
	return irsaAddons[addonName]
}
