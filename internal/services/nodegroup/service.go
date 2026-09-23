// Package nodegroup lists, describes, scales, and updates EKS managed
// nodegroups, including AMI staleness checks.
package nodegroup

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/services/common"
	"github.com/dantech2000/refresh/internal/types"
)

// classifyAMI compares the nodegroup's current AMI against the latest available
// for its type and returns the appropriate status. Returns AMIUpdating while an
// update is in flight, AMICustom for custom-AMI nodegroups (whose AMI is managed
// via the user's launch template, not by EKS, so there's no recommended AMI to
// compare against), regardless of AMI identities.
func classifyAMI(amiType ekstypes.AMITypes, status ekstypes.NodegroupStatus, currentAmiID, latestAmiID string) types.AMIStatus {
	switch {
	case status == ekstypes.NodegroupStatusUpdating:
		return types.AMIUpdating
	case amiType == ekstypes.AMITypesCustom:
		return types.AMICustom
	case currentAmiID == "" || latestAmiID == "":
		return types.AMIUnknown
	case currentAmiID == latestAmiID:
		return types.AMILatest
	default:
		return types.AMIOutdated
	}
}

// EKSAPI abstracts the subset of EKS client methods used for nodegroups.
type EKSAPI interface {
	ListNodegroups(ctx context.Context, params *eks.ListNodegroupsInput, optFns ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error)
	DescribeNodegroup(ctx context.Context, params *eks.DescribeNodegroupInput, optFns ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error)
	DescribeCluster(ctx context.Context, params *eks.DescribeClusterInput, optFns ...func(*eks.Options)) (*eks.DescribeClusterOutput, error)
	UpdateNodegroupConfig(ctx context.Context, params *eks.UpdateNodegroupConfigInput, optFns ...func(*eks.Options)) (*eks.UpdateNodegroupConfigOutput, error)
	UpdateNodegroupVersion(ctx context.Context, params *eks.UpdateNodegroupVersionInput, optFns ...func(*eks.Options)) (*eks.UpdateNodegroupVersionOutput, error)
}

// ServiceImpl is the nodegroup service.
type ServiceImpl struct {
	eksClient     EKSAPI
	logger        *slog.Logger
	awsConfig     aws.Config
	healthChecker *health.HealthChecker
	cache         *Cache
	asgClient     *autoscaling.Client
	ec2Client     *ec2.Client
	ssmClient     *ssm.Client

	// Test seams; nil in production (the real EC2/ASG/SSM lookups are used).
	currentAMIFn func(context.Context, *ekstypes.Nodegroup) string
	latestAMIFn  awsinternal.AMILookupFunc
}

// NewService creates a new nodegroup service.
func NewService(awsConfig aws.Config, healthChecker *health.HealthChecker, logger *slog.Logger) *ServiceImpl {
	cache := NewCache()
	return &ServiceImpl{
		eksClient:     eks.NewFromConfig(awsConfig),
		logger:        logger,
		awsConfig:     awsConfig,
		healthChecker: healthChecker,
		cache:         cache,
		asgClient:     autoscaling.NewFromConfig(awsConfig),
		ec2Client:     ec2.NewFromConfig(awsConfig),
		ssmClient:     ssm.NewFromConfig(awsConfig),
	}
}

// nodegroupReadyCounts returns measured Kubernetes Ready=True counts per
// nodegroup, or (nil, false) when no cluster-connected health checker is wired
// (the common case for a plain `nodegroup list`). (REF-130)
func (s *ServiceImpl) nodegroupReadyCounts(ctx context.Context) (map[string]int32, bool) {
	if s.healthChecker == nil {
		return nil, false
	}
	return s.healthChecker.NodegroupReadyCounts(ctx)
}

// supportedFilterKeys maps normalized --filter keys to a matcher against a
// built summary. Keys are matched case-insensitively.
var supportedFilterKeys = map[string]func(s NodegroupSummary, want string) bool{
	"name": func(s NodegroupSummary, want string) bool {
		return strings.Contains(strings.ToLower(s.Name), strings.ToLower(want))
	},
	"status":       func(s NodegroupSummary, want string) bool { return strings.EqualFold(s.Status, want) },
	"instancetype": func(s NodegroupSummary, want string) bool { return strings.EqualFold(s.InstanceType, want) },
	"amistatus":    func(s NodegroupSummary, want string) bool { return strings.EqualFold(s.AMIStatus.PlainString(), want) },
}

// validateFilters rejects unknown filter keys up front so a typo'd
// --filter doesn't silently match everything.
func validateFilters(filters map[string]string) error {
	for k := range filters {
		if _, ok := supportedFilterKeys[normalizeFilterKey(k)]; !ok {
			return fmt.Errorf("unsupported filter key %q (supported: name, status, instanceType, amiStatus)", k)
		}
	}
	return nil
}

func normalizeFilterKey(k string) string {
	return strings.ToLower(strings.ReplaceAll(k, "-", ""))
}

func matchesFilters(s NodegroupSummary, filters map[string]string) bool {
	for k, want := range filters {
		if match := supportedFilterKeys[normalizeFilterKey(k)]; match != nil && !match(s, want) {
			return false
		}
	}
	return true
}

// List returns basic nodegroup summaries for a cluster. Nodegroups that can't
// be described are logged and left out; use ListWithFailures to learn which.
func (s *ServiceImpl) List(ctx context.Context, clusterName string, options ListOptions) ([]NodegroupSummary, error) {
	summaries, _, err := s.ListWithFailures(ctx, clusterName, options)
	return summaries, err
}

// ngResult is one nodegroup's outcome in ListDetailed. done stays false
// for items ForEachParallel never dispatched (ctx ended first).
type ngResult struct {
	done         bool
	summary      *NodegroupSummary // nil when filtered out or failed
	failure      string            // non-empty when the nodegroup couldn't be described
	amiLookupErr error             // latest-AMI lookup failure that left AMIStatus Unknown
}

// ListWithFailures is List plus a "name: reason" entry for every listed
// nodegroup whose data is incomplete: left out of the summaries because it
// could not be described (API error, empty response, or never reached because
// ctx ended), or summarized with an AMI status that is Unknown only because
// its latest recommended AMI could not be resolved. Nodegroups excluded by
// options.Filters are not failures.
func (s *ServiceImpl) ListWithFailures(ctx context.Context, clusterName string, options ListOptions) ([]NodegroupSummary, []string, error) {
	res, err := s.ListDetailed(ctx, clusterName, options)
	if err != nil {
		return nil, nil, err
	}
	var failures []string
	failures = append(failures, res.Failures...)
	failures = append(failures, res.AMILookupFailures...)
	return res.Summaries, failures, nil
}

// latestAMILookupMatters reports whether a failed latest-AMI lookup leaves
// the nodegroup's AMI status undetermined. Custom-AMI nodegroups have no
// recommended AMI, and an updating nodegroup reports Updating either way.
func latestAMILookupMatters(ng *ekstypes.Nodegroup) bool {
	return ng.AmiType != ekstypes.AMITypesCustom && ng.Status != ekstypes.NodegroupStatusUpdating
}

// ListDetailed lists a cluster's nodegroups and keeps describe failures and
// latest-AMI lookup failures apart (see ListResult).
func (s *ServiceImpl) ListDetailed(ctx context.Context, clusterName string, options ListOptions) (ListResult, error) {
	s.logger.Info("listing nodegroups", "cluster", clusterName, "options", options)

	if err := validateFilters(options.Filters); err != nil {
		return ListResult{}, err
	}

	clusterDesc, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeClusterOutput, error) {
		return s.eksClient.DescribeCluster(rc, &eks.DescribeClusterInput{Name: aws.String(clusterName)})
	})
	if err != nil {
		return ListResult{}, awsinternal.FormatAWSError(err, fmt.Sprintf("describing cluster %s for version info", clusterName))
	}
	if clusterDesc.Cluster == nil {
		return ListResult{}, fmt.Errorf("empty DescribeCluster response for %s", clusterName)
	}
	k8sVersion := aws.ToString(clusterDesc.Cluster.Version)

	nodegroupNames, err := awsinternal.ListAllPages(ctx, fmt.Sprintf("listing nodegroups for cluster %s", clusterName),
		func(rc context.Context, token *string) (*eks.ListNodegroupsOutput, error) {
			return s.eksClient.ListNodegroups(rc, &eks.ListNodegroupsInput{
				ClusterName: aws.String(clusterName),
				NextToken:   token,
			})
		},
		func(out *eks.ListNodegroupsOutput) ([]string, *string) { return out.Nodegroups, out.NextToken },
	)
	if err != nil {
		return ListResult{}, err
	}

	// The latest AMI is constant per (nodegroup version, AMI type); memoize
	// the SSM lookup across the (concurrent) per-nodegroup work.
	latestAMI := s.newLatestAMICache()

	// Measured Kubernetes Ready counts per nodegroup, fetched once (one node
	// LIST) when a cluster-connected health checker is wired (--check-readiness).
	// haveReady is false otherwise, so readiness renders as honestly unknown
	// instead of the old "ready = desired when ACTIVE" tautology. (REF-130)
	readyByNG, haveReady := s.nodegroupReadyCounts(ctx)

	results := common.ForEachParallel(ctx, nodegroupNames, common.DefaultItemConcurrency,
		func(fctx context.Context, name string) ngResult {
			desc, err := common.WithRetry(fctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeNodegroupOutput, error) {
				return s.eksClient.DescribeNodegroup(rc, &eks.DescribeNodegroupInput{
					ClusterName:   aws.String(clusterName),
					NodegroupName: aws.String(name),
				})
			})
			if err != nil {
				s.logger.Debug("failed to describe nodegroup", "cluster", clusterName, "nodegroup", name, "error", err)
				return ngResult{done: true, failure: fmt.Sprintf("%s: %v", name, err)}
			}
			ng := desc.Nodegroup
			if ng == nil {
				s.logger.Debug("empty DescribeNodegroup response", "cluster", clusterName, "nodegroup", name)
				return ngResult{done: true, failure: name + ": empty DescribeNodegroup response"}
			}
			var desiredSize int32
			if ng.ScalingConfig != nil {
				desiredSize = aws.ToInt32(ng.ScalingConfig.DesiredSize)
			}
			readyNodes := int32(0)
			readyKnown := false
			if haveReady {
				readyNodes = readyByNG[name]
				readyKnown = true
			}
			instanceType := "Unknown"
			if len(ng.InstanceTypes) > 0 {
				instanceType = ng.InstanceTypes[0]
			}

			currentAmiID := s.currentAMI(fctx, ng)
			latestAmiID, lookupErr := latestAMI.ForNodegroup(fctx, ng, k8sVersion)
			amiStatus := classifyAMI(ng.AmiType, ng.Status, currentAmiID, latestAmiID)
			if lookupErr != nil {
				s.logger.Debug("failed to resolve latest AMI", "cluster", clusterName, "nodegroup", name, "error", lookupErr)
				if !latestAMILookupMatters(ng) {
					lookupErr = nil
				}
			}

			summary := NodegroupSummary{
				Name:         aws.ToString(ng.NodegroupName),
				Status:       string(ng.Status),
				InstanceType: instanceType,
				DesiredSize:  desiredSize,
				ReadyNodes:   readyNodes,
				ReadyKnown:   readyKnown,
				CurrentAMI:   currentAmiID,
				AMIStatus:    amiStatus,
				K8sVersion:   aws.ToString(ng.Version),
			}
			summary.VersionBehind = minorBehind(summary.K8sVersion, k8sVersion)
			if lookupErr != nil {
				summary.AMILookupError = lookupErr.Error()
			}
			if !matchesFilters(summary, options.Filters) {
				return ngResult{done: true}
			}
			return ngResult{done: true, summary: &summary, amiLookupErr: lookupErr}
		})

	res := ListResult{Summaries: make([]NodegroupSummary, 0, len(results))}
	var failures []string
	for i, r := range results {
		switch {
		case !r.done:
			reason := "sweep stopped early"
			if cerr := ctx.Err(); cerr != nil {
				reason = cerr.Error()
			}
			failures = append(failures, nodegroupNames[i]+": not evaluated: "+reason)
		case r.failure != "":
			failures = append(failures, r.failure)
		case r.summary != nil:
			res.Summaries = append(res.Summaries, *r.summary)
			if r.amiLookupErr != nil {
				res.AMILookupFailures = append(res.AMILookupFailures,
					nodegroupNames[i]+": latest AMI lookup failed: "+r.amiLookupErr.Error())
				if res.AMILookupErr == nil {
					res.AMILookupErr = r.amiLookupErr
				}
			}
		}
	}
	res.Failures = failures
	return res, nil
}

// minorBehind reports whether Kubernetes version v ("1.31") is an older
// major.minor than ref. Unparseable or empty versions are never "behind".
func minorBehind(v, ref string) bool {
	parse := func(s string) (int, int, bool) {
		parts := strings.SplitN(strings.TrimPrefix(strings.TrimSpace(s), "v"), ".", 3)
		if len(parts) < 2 {
			return 0, 0, false
		}
		major, err1 := strconv.Atoi(parts[0])
		minor, err2 := strconv.Atoi(parts[1])
		return major, minor, err1 == nil && err2 == nil
	}
	vMaj, vMin, ok1 := parse(v)
	rMaj, rMin, ok2 := parse(ref)
	if !ok1 || !ok2 {
		return false
	}
	return vMaj < rMaj || (vMaj == rMaj && vMin < rMin)
}

// newLatestAMICache returns a concurrency-safe, memoized resolver for the
// latest recommended AMI per (Kubernetes version, AMI type). Callers key it by
// the nodegroup's own version: an AMI-only update keeps the nodegroup on its
// current minor, so the cluster's minor is the wrong baseline.
func (s *ServiceImpl) newLatestAMICache() *awsinternal.LatestAMICache {
	if s.latestAMIFn != nil {
		return awsinternal.NewLatestAMICache(s.latestAMIFn)
	}
	return awsinternal.NewLatestAMIIDCache(s.ssmClient)
}

// currentAMI resolves the AMI the nodegroup's nodes currently run.
func (s *ServiceImpl) currentAMI(ctx context.Context, ng *ekstypes.Nodegroup) string {
	if s.currentAMIFn != nil {
		return s.currentAMIFn(ctx, ng)
	}
	return awsinternal.CurrentAmiID(ctx, ng, s.ec2Client, s.asgClient)
}

// Describe returns expanded details for a single nodegroup.
func (s *ServiceImpl) Describe(ctx context.Context, clusterName, nodegroupName string, options DescribeOptions) (*NodegroupDetails, error) {
	s.logger.Info("describing nodegroup", "cluster", clusterName, "nodegroup", nodegroupName, "options", options)

	clusterDesc, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeClusterOutput, error) {
		return s.eksClient.DescribeCluster(rc, &eks.DescribeClusterInput{Name: aws.String(clusterName)})
	})
	if err != nil {
		return nil, awsinternal.FormatAWSError(err, fmt.Sprintf("describing cluster %s for version info", clusterName))
	}
	if clusterDesc.Cluster == nil {
		return nil, fmt.Errorf("empty DescribeCluster response for %s", clusterName)
	}
	k8sVersion := aws.ToString(clusterDesc.Cluster.Version)

	out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeNodegroupOutput, error) {
		return s.eksClient.DescribeNodegroup(rc, &eks.DescribeNodegroupInput{
			ClusterName:   aws.String(clusterName),
			NodegroupName: aws.String(nodegroupName),
		})
	})
	if err != nil {
		return nil, awsinternal.FormatAWSError(err, fmt.Sprintf("describing nodegroup %s/%s", clusterName, nodegroupName))
	}
	if out.Nodegroup == nil {
		return nil, fmt.Errorf("describing nodegroup %s/%s: empty response", clusterName, nodegroupName)
	}
	ng := out.Nodegroup

	currentAmiID := s.currentAMI(ctx, ng)
	latestAmiID, lookupErr := s.newLatestAMICache().ForNodegroup(ctx, ng, k8sVersion)
	amiStatus := classifyAMI(ng.AmiType, ng.Status, currentAmiID, latestAmiID)
	if lookupErr != nil && !latestAMILookupMatters(ng) {
		lookupErr = nil
	}

	var scaling ScalingConfig
	if sc := ng.ScalingConfig; sc != nil {
		scaling = ScalingConfig{
			DesiredSize: aws.ToInt32(sc.DesiredSize),
			MinSize:     aws.ToInt32(sc.MinSize),
			MaxSize:     aws.ToInt32(sc.MaxSize),
			AutoScaling: true,
		}
	}

	details := &NodegroupDetails{
		Name:         aws.ToString(ng.NodegroupName),
		Status:       string(ng.Status),
		InstanceType: firstInstanceType(ng.InstanceTypes),
		AmiType:      string(ng.AmiType),
		CapacityType: string(ng.CapacityType),
		CurrentAMI:   currentAmiID,
		LatestAMI:    latestAmiID,
		AMIStatus:    amiStatus,
		Scaling:      scaling,
		amiLookupErr: lookupErr,
	}
	if lookupErr != nil {
		details.AMILookupError = lookupErr.Error()
	}
	// Resolve backing instances once from the nodegroup we already described;
	// workloads and instance details reuse the result.
	var instanceIDs []string
	if options.ShowWorkloads || options.ShowInstances {
		instanceIDs, _ = s.instanceIDsForNodegroup(ctx, ng)
	}

	if options.ShowWorkloads {
		details.Workloads.PodDisruption = "no data"
		target := health.TargetCluster{
			Name:     clusterName,
			Region:   s.awsConfig.Region,
			ARN:      aws.ToString(clusterDesc.Cluster.Arn),
			Endpoint: aws.ToString(clusterDesc.Cluster.Endpoint),
		}
		if wi, ok := s.analyzeWorkloads(ctx, target, aws.ToString(ng.NodegroupName), instanceIDs); ok {
			details.Workloads = wi
		} else {
			details.Workloads.PodDisruption = "unavailable: Kubernetes API not accessible for this cluster (check the kubeconfig context) or no matching nodes"
		}
	}
	if options.ShowInstances && len(instanceIDs) > 0 {
		if insts, ok := s.getInstanceDetails(ctx, instanceIDs); ok {
			details.Instances = insts
		}
	}
	return details, nil
}
