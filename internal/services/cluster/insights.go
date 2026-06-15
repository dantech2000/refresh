package cluster

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/services/addons"
	"github.com/dantech2000/refresh/internal/services/common"
	"github.com/dantech2000/refresh/internal/services/status"
)

// Insight status values (the flattened InsightStatus.Status enum).
const (
	InsightStatusPassing = "PASSING"
	InsightStatusWarning = "WARNING"
	InsightStatusError   = "ERROR"
	InsightStatusUnknown = "UNKNOWN"
)

// kubeletSkewLimit is the maximum number of minor versions a node's kubelet may
// trail the control plane (EKS supports a 3-version skew). A nodegroup at or
// beyond this skew blocks further control-plane upgrades until the nodes catch
// up.
const kubeletSkewLimit = 3

// InsightSummary is a flattened EKS Cluster Insight — the nested
// InsightStatus.Status/.Reason are pulled up into plain fields.
type InsightSummary struct {
	ID                string     `json:"id" yaml:"id"`
	Name              string     `json:"name" yaml:"name"`
	Category          string     `json:"category" yaml:"category"`
	Status            string     `json:"status" yaml:"status"`
	StatusReason      string     `json:"statusReason,omitempty" yaml:"statusReason,omitempty"`
	KubernetesVersion string     `json:"kubernetesVersion,omitempty" yaml:"kubernetesVersion,omitempty"`
	LastRefreshTime   *time.Time `json:"lastRefreshTime,omitempty" yaml:"lastRefreshTime,omitempty"`
	Description       string     `json:"description,omitempty" yaml:"description,omitempty"`
}

// InsightDetail is the DescribeInsight detail view (recommendation + resources).
type InsightDetail struct {
	InsightSummary
	Recommendation string              `json:"recommendation,omitempty" yaml:"recommendation,omitempty"`
	Resources      []string            `json:"resources,omitempty" yaml:"resources,omitempty"`
	AdditionalInfo map[string]string   `json:"additionalInfo,omitempty" yaml:"additionalInfo,omitempty"`
	Deprecations   []DeprecationDetail `json:"deprecations,omitempty" yaml:"deprecations,omitempty"`
}

// DeprecationDetail describes one deprecated Kubernetes API surfaced by an
// UPGRADE_READINESS insight, plus the clients still calling it. EKS derives this
// from the control-plane audit log on a 30-day rolling window.
type DeprecationDetail struct {
	// Usage is the deprecated resource/API path (e.g. policy/v1beta1 PodDisruptionBudget).
	Usage string `json:"usage,omitempty" yaml:"usage,omitempty"`
	// ReplacedWith is the API to migrate to, if applicable.
	ReplacedWith string `json:"replacedWith,omitempty" yaml:"replacedWith,omitempty"`
	// StopServingVersion is the Kubernetes version that removes the deprecated API.
	StopServingVersion string `json:"stopServingVersion,omitempty" yaml:"stopServingVersion,omitempty"`
	// StartServingReplacementVersion is the version where the replacement became available.
	StartServingReplacementVersion string `json:"startServingReplacementVersion,omitempty" yaml:"startServingReplacementVersion,omitempty"`
	// ClientStats are the callers (most-active first) still hitting the deprecated API.
	ClientStats []ClientStat `json:"clientStats,omitempty" yaml:"clientStats,omitempty"`
}

// ClientStat identifies one Kubernetes client still calling a deprecated API.
type ClientStat struct {
	UserAgent                  string     `json:"userAgent,omitempty" yaml:"userAgent,omitempty"`
	LastRequestTime            *time.Time `json:"lastRequestTime,omitempty" yaml:"lastRequestTime,omitempty"`
	NumberOfRequestsLast30Days int32      `json:"numberOfRequestsLast30Days" yaml:"numberOfRequestsLast30Days"`
}

// UpgradeCheckOptions filters an upgrade-check query.
type UpgradeCheckOptions struct {
	Category    string   // default UPGRADE_READINESS
	Statuses    []string // optional status filter (PASSING/WARNING/ERROR/UNKNOWN)
	ShowPassing bool     // include PASSING insights (hidden by default)
}

// NodegroupSkew is a managed nodegroup's Kubernetes version relative to the
// control plane.
type NodegroupSkew struct {
	Name         string `json:"name" yaml:"name"`
	Version      string `json:"version" yaml:"version"`
	MinorsBehind int    `json:"minorsBehind" yaml:"minorsBehind"`
	Blocking     bool   `json:"blocking" yaml:"blocking"`
}

// AddonSkew is an installed addon's version vs the latest compatible with the
// cluster's Kubernetes version.
type AddonSkew struct {
	Name      string `json:"name" yaml:"name"`
	Installed string `json:"installed" yaml:"installed"`
	Latest    string `json:"latest" yaml:"latest"`
	Behind    bool   `json:"behind" yaml:"behind"`
}

// SkewReport is the local version-skew picture: control plane vs nodegroups vs
// addons, with ordered, actionable findings.
type SkewReport struct {
	ControlPlaneVersion string          `json:"controlPlaneVersion" yaml:"controlPlaneVersion"`
	Nodegroups          []NodegroupSkew `json:"nodegroups" yaml:"nodegroups"`
	Addons              []AddonSkew     `json:"addons" yaml:"addons"`
	Findings            []string        `json:"findings,omitempty" yaml:"findings,omitempty"`
}

// UpgradeReport combines AWS Cluster Insights with the local version-skew view —
// the full `cluster upgrade-check` result.
type UpgradeReport struct {
	Cluster      string                 `json:"cluster" yaml:"cluster"`
	Support      *status.SupportPosture `json:"support,omitempty" yaml:"support,omitempty"`
	ControlPlane *health.HealthResult   `json:"controlPlane,omitempty" yaml:"controlPlane,omitempty"`
	Insights     []InsightSummary       `json:"insights" yaml:"insights"`
	Skew         SkewReport             `json:"skew" yaml:"skew"`
}

// ListInsights returns the cluster's EKS Cluster Insights filtered per opts.
// PASSING insights are dropped unless opts.ShowPassing.
func (s *ServiceImpl) ListInsights(ctx context.Context, clusterName string, opts UpgradeCheckOptions) ([]InsightSummary, error) {
	category := opts.Category
	if category == "" {
		category = string(ekstypes.CategoryUpgradeReadiness)
	}
	filter := &ekstypes.InsightsFilter{Categories: []ekstypes.Category{ekstypes.Category(category)}}
	for _, st := range opts.Statuses {
		filter.Statuses = append(filter.Statuses, ekstypes.InsightStatusValue(strings.ToUpper(strings.TrimSpace(st))))
	}

	raw, err := awsinternal.ListAllPages(ctx, "listing cluster insights",
		func(rc context.Context, token *string) (*eks.ListInsightsOutput, error) {
			return s.eksClient.ListInsights(rc, &eks.ListInsightsInput{
				ClusterName: aws.String(clusterName),
				Filter:      filter,
				NextToken:   token,
			})
		},
		func(out *eks.ListInsightsOutput) ([]ekstypes.InsightSummary, *string) {
			return out.Insights, out.NextToken
		},
	)
	if err != nil {
		return nil, err
	}

	result := make([]InsightSummary, 0, len(raw))
	for _, in := range raw {
		is := InsightSummary{
			ID:                aws.ToString(in.Id),
			Name:              aws.ToString(in.Name),
			Category:          string(in.Category),
			KubernetesVersion: aws.ToString(in.KubernetesVersion),
			Description:       aws.ToString(in.Description),
			LastRefreshTime:   in.LastRefreshTime,
			Status:            InsightStatusUnknown,
		}
		if in.InsightStatus != nil {
			is.Status = string(in.InsightStatus.Status)
			is.StatusReason = aws.ToString(in.InsightStatus.Reason)
		}
		if !opts.ShowPassing && is.Status == InsightStatusPassing {
			continue
		}
		result = append(result, is)
	}
	return result, nil
}

// ResolveInsightID turns a user-supplied reference — a full insight ID, a short
// ID prefix (as shown in the upgrade-check table), or a case-insensitive name
// substring — into a canonical insight ID. It lists all insights (including
// PASSING) so anything visible in the table can be drilled into. An exact ID
// wins outright; a single prefix/name match resolves; an ambiguous query errors
// with the candidates so the user can narrow it down.
func (s *ServiceImpl) ResolveInsightID(ctx context.Context, clusterName, query string) (string, error) {
	all, err := s.ListInsights(ctx, clusterName, UpgradeCheckOptions{ShowPassing: true})
	if err != nil {
		return "", err
	}
	q := strings.ToLower(strings.TrimSpace(query))
	var matches []InsightSummary
	for _, in := range all {
		if strings.EqualFold(in.ID, query) {
			return in.ID, nil
		}
		if (len(q) >= 4 && strings.HasPrefix(strings.ToLower(in.ID), q)) || strings.Contains(strings.ToLower(in.Name), q) {
			matches = append(matches, in)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0].ID, nil
	case 0:
		return "", fmt.Errorf("no insight matches %q; run with --show-passing to list them", query)
	default:
		var b strings.Builder
		fmt.Fprintf(&b, "%q matches %d insights — narrow it down:", query, len(matches))
		for _, m := range matches {
			sid := m.ID
			if len(sid) > 8 {
				sid = sid[:8]
			}
			fmt.Fprintf(&b, "\n  %-9s %s", sid, m.Name)
		}
		return "", errors.New(b.String())
	}
}

// DescribeInsight returns the detail view (recommendation, affected resources)
// for a single insight.
func (s *ServiceImpl) DescribeInsight(ctx context.Context, clusterName, id string) (*InsightDetail, error) {
	out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeInsightOutput, error) {
		return s.eksClient.DescribeInsight(rc, &eks.DescribeInsightInput{
			ClusterName: aws.String(clusterName),
			Id:          aws.String(id),
		})
	})
	if err != nil {
		return nil, awsinternal.FormatAWSError(err, "describing cluster insight")
	}
	if out == nil || out.Insight == nil {
		return nil, fmt.Errorf("insight %q not found on cluster %q", id, clusterName)
	}
	ins := out.Insight
	detail := &InsightDetail{
		InsightSummary: InsightSummary{
			ID:                aws.ToString(ins.Id),
			Name:              aws.ToString(ins.Name),
			Category:          string(ins.Category),
			KubernetesVersion: aws.ToString(ins.KubernetesVersion),
			Description:       aws.ToString(ins.Description),
			LastRefreshTime:   ins.LastRefreshTime,
			Status:            InsightStatusUnknown,
		},
		Recommendation: aws.ToString(ins.Recommendation),
		AdditionalInfo: ins.AdditionalInfo,
	}
	if ins.InsightStatus != nil {
		detail.Status = string(ins.InsightStatus.Status)
		detail.StatusReason = aws.ToString(ins.InsightStatus.Reason)
	}
	for _, r := range ins.Resources {
		switch {
		case r.Arn != nil:
			detail.Resources = append(detail.Resources, aws.ToString(r.Arn))
		case r.KubernetesResourceUri != nil:
			detail.Resources = append(detail.Resources, aws.ToString(r.KubernetesResourceUri))
		}
	}
	if css := ins.CategorySpecificSummary; css != nil {
		for _, d := range css.DeprecationDetails {
			dd := DeprecationDetail{
				Usage:                          aws.ToString(d.Usage),
				ReplacedWith:                   aws.ToString(d.ReplacedWith),
				StopServingVersion:             aws.ToString(d.StopServingVersion),
				StartServingReplacementVersion: aws.ToString(d.StartServingReplacementVersion),
			}
			for _, c := range d.ClientStats {
				dd.ClientStats = append(dd.ClientStats, ClientStat{
					UserAgent:                  aws.ToString(c.UserAgent),
					LastRequestTime:            c.LastRequestTime,
					NumberOfRequestsLast30Days: c.NumberOfRequestsLast30Days,
				})
			}
			// Most-active callers first — that's who to chase down.
			sort.SliceStable(dd.ClientStats, func(i, j int) bool {
				return dd.ClientStats[i].NumberOfRequestsLast30Days > dd.ClientStats[j].NumberOfRequestsLast30Days
			})
			detail.Deprecations = append(detail.Deprecations, dd)
		}
	}
	return detail, nil
}

// UpgradeCheck assembles the full readiness report: AWS Cluster Insights plus
// the local control-plane/nodegroup/addon version-skew picture.
func (s *ServiceImpl) UpgradeCheck(ctx context.Context, clusterName string, opts UpgradeCheckOptions) (*UpgradeReport, error) {
	desc, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeClusterOutput, error) {
		return s.eksClient.DescribeCluster(rc, &eks.DescribeClusterInput{Name: aws.String(clusterName)})
	})
	if err != nil {
		return nil, awsinternal.FormatAWSError(err, "describing cluster")
	}
	cpVersion := ""
	if desc != nil && desc.Cluster != nil {
		cpVersion = aws.ToString(desc.Cluster.Version)
	}

	insights, err := s.ListInsights(ctx, clusterName, opts)
	if err != nil {
		return nil, err
	}

	skew, err := s.computeSkew(ctx, clusterName, cpVersion)
	if err != nil {
		return nil, err
	}

	return &UpgradeReport{Cluster: clusterName, Insights: insights, Skew: skew}, nil
}

// computeSkew builds the local version-skew report and ordered findings.
func (s *ServiceImpl) computeSkew(ctx context.Context, clusterName, cpVersion string) (SkewReport, error) {
	report := SkewReport{ControlPlaneVersion: cpVersion}
	cpMinor, cpOK := minorVersion(cpVersion)

	// Nodegroups.
	ngNames, err := awsinternal.ListAllPages(ctx, "listing nodegroups",
		func(rc context.Context, token *string) (*eks.ListNodegroupsOutput, error) {
			return s.eksClient.ListNodegroups(rc, &eks.ListNodegroupsInput{ClusterName: aws.String(clusterName), NextToken: token})
		},
		func(out *eks.ListNodegroupsOutput) ([]string, *string) { return out.Nodegroups, out.NextToken },
	)
	if err != nil {
		return report, err
	}
	for _, name := range ngNames {
		ngDesc, derr := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeNodegroupOutput, error) {
			return s.eksClient.DescribeNodegroup(rc, &eks.DescribeNodegroupInput{ClusterName: aws.String(clusterName), NodegroupName: aws.String(name)})
		})
		if derr != nil || ngDesc == nil || ngDesc.Nodegroup == nil {
			continue
		}
		ngVersion := aws.ToString(ngDesc.Nodegroup.Version)
		skew := NodegroupSkew{Name: name, Version: ngVersion}
		if cpOK {
			if ngMinor, ok := minorVersion(ngVersion); ok {
				skew.MinorsBehind = cpMinor - ngMinor
				if skew.MinorsBehind < 0 {
					skew.MinorsBehind = 0
				}
				skew.Blocking = skew.MinorsBehind >= kubeletSkewLimit
			}
		}
		report.Nodegroups = append(report.Nodegroups, skew)
	}

	// Addons.
	addonNames, err := awsinternal.ListAllPages(ctx, "listing addons",
		func(rc context.Context, token *string) (*eks.ListAddonsOutput, error) {
			return s.eksClient.ListAddons(rc, &eks.ListAddonsInput{ClusterName: aws.String(clusterName), NextToken: token})
		},
		func(out *eks.ListAddonsOutput) ([]string, *string) { return out.Addons, out.NextToken },
	)
	if err != nil {
		return report, err
	}
	for _, name := range addonNames {
		adDesc, derr := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeAddonOutput, error) {
			return s.eksClient.DescribeAddon(rc, &eks.DescribeAddonInput{ClusterName: aws.String(clusterName), AddonName: aws.String(name)})
		})
		if derr != nil || adDesc == nil || adDesc.Addon == nil {
			continue
		}
		installed := aws.ToString(adDesc.Addon.AddonVersion)
		latest, lerr := s.latestAddonVersion(ctx, name, cpVersion)
		skew := AddonSkew{Name: name, Installed: installed, Latest: latest}
		if lerr == nil && latest != "" && installed != "" && addons.CompareVersions(installed, latest) < 0 {
			skew.Behind = true
		}
		report.Addons = append(report.Addons, skew)
	}

	report.Findings = skewFindings(report)
	return report, nil
}

// latestAddonVersion returns the newest addon version compatible with the given
// Kubernetes version, using the cluster service's (mockable) EKS client.
func (s *ServiceImpl) latestAddonVersion(ctx context.Context, addonName, k8sVersion string) (string, error) {
	infos, err := awsinternal.ListAllPages(ctx, fmt.Sprintf("describing versions for addon %s", addonName),
		func(rc context.Context, token *string) (*eks.DescribeAddonVersionsOutput, error) {
			in := &eks.DescribeAddonVersionsInput{AddonName: aws.String(addonName), NextToken: token}
			if k8sVersion != "" {
				in.KubernetesVersion = aws.String(k8sVersion)
			}
			return s.eksClient.DescribeAddonVersions(rc, in)
		},
		func(out *eks.DescribeAddonVersionsOutput) ([]ekstypes.AddonInfo, *string) {
			return out.Addons, out.NextToken
		},
	)
	if err != nil {
		return "", err
	}
	var versions []string
	for _, info := range infos {
		for _, v := range info.AddonVersions {
			if v.AddonVersion != nil {
				versions = append(versions, *v.AddonVersion)
			}
		}
	}
	if len(versions) == 0 {
		return "", nil
	}
	sort.SliceStable(versions, func(i, j int) bool { return addons.CompareVersions(versions[i], versions[j]) > 0 })
	return versions[0], nil
}

// skewFindings renders ordered, actionable findings: blocking nodegroups first,
// then lagging nodegroups, then addons behind latest.
func skewFindings(r SkewReport) []string {
	var blocking, behind, addonsBehind []string
	for _, ng := range r.Nodegroups {
		switch {
		case ng.Blocking:
			blocking = append(blocking, fmt.Sprintf("nodegroup %s (%s) is %d minor versions behind control plane %s — upgrade these nodes before upgrading the control plane further (kubelet skew limit is %d)", ng.Name, ng.Version, ng.MinorsBehind, r.ControlPlaneVersion, kubeletSkewLimit))
		case ng.MinorsBehind > 0:
			behind = append(behind, fmt.Sprintf("nodegroup %s (%s) is %d minor version(s) behind control plane %s", ng.Name, ng.Version, ng.MinorsBehind, r.ControlPlaneVersion))
		}
	}
	for _, a := range r.Addons {
		if a.Behind {
			addonsBehind = append(addonsBehind, fmt.Sprintf("addon %s is behind latest compatible (%s → %s)", a.Name, a.Installed, a.Latest))
		}
	}
	findings := make([]string, 0, len(blocking)+len(behind)+len(addonsBehind))
	findings = append(findings, blocking...)
	findings = append(findings, behind...)
	findings = append(findings, addonsBehind...)
	return findings
}

// minorVersion extracts the Kubernetes minor version (the N in "1.N") from a
// version string such as "1.31" or "v1.31.2".
func minorVersion(v string) (int, bool) {
	parts := strings.SplitN(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".", 3)
	if len(parts) < 2 {
		return 0, false
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, false
	}
	return n, true
}
