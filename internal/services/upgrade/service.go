package upgrade

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/services/addons"
	"github.com/dantech2000/refresh/internal/services/common"
)

// defaultPollInterval is how often in-flight EKS updates are re-checked.
const defaultPollInterval = 15 * time.Second

// EKSAPI abstracts the EKS client methods the upgrade orchestrator uses.
// It is a superset of addons.EKSAPI so one client (or mock) serves both.
type EKSAPI interface {
	DescribeCluster(ctx context.Context, params *eks.DescribeClusterInput, optFns ...func(*eks.Options)) (*eks.DescribeClusterOutput, error)
	UpdateClusterVersion(ctx context.Context, params *eks.UpdateClusterVersionInput, optFns ...func(*eks.Options)) (*eks.UpdateClusterVersionOutput, error)
	DescribeUpdate(ctx context.Context, params *eks.DescribeUpdateInput, optFns ...func(*eks.Options)) (*eks.DescribeUpdateOutput, error)
	DescribeClusterVersions(ctx context.Context, params *eks.DescribeClusterVersionsInput, optFns ...func(*eks.Options)) (*eks.DescribeClusterVersionsOutput, error)
	ListInsights(ctx context.Context, params *eks.ListInsightsInput, optFns ...func(*eks.Options)) (*eks.ListInsightsOutput, error)
	ListAddons(ctx context.Context, params *eks.ListAddonsInput, optFns ...func(*eks.Options)) (*eks.ListAddonsOutput, error)
	DescribeAddon(ctx context.Context, params *eks.DescribeAddonInput, optFns ...func(*eks.Options)) (*eks.DescribeAddonOutput, error)
	DescribeAddonVersions(ctx context.Context, params *eks.DescribeAddonVersionsInput, optFns ...func(*eks.Options)) (*eks.DescribeAddonVersionsOutput, error)
	UpdateAddon(ctx context.Context, params *eks.UpdateAddonInput, optFns ...func(*eks.Options)) (*eks.UpdateAddonOutput, error)
	ListNodegroups(ctx context.Context, params *eks.ListNodegroupsInput, optFns ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error)
	DescribeNodegroup(ctx context.Context, params *eks.DescribeNodegroupInput, optFns ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error)
	UpdateNodegroupVersion(ctx context.Context, params *eks.UpdateNodegroupVersionInput, optFns ...func(*eks.Options)) (*eks.UpdateNodegroupVersionOutput, error)
}

// ProgressFunc receives human-readable progress lines during execution.
type ProgressFunc func(format string, args ...any)

// Service builds and executes cluster upgrade plans.
type Service struct {
	eksClient EKSAPI
	logger    *slog.Logger

	// PollInterval is how often in-flight updates are re-checked.
	// Tests shrink it; defaults to defaultPollInterval.
	PollInterval time.Duration
}

// NewService creates the upgrade orchestrator service.
func NewService(eksClient EKSAPI, logger *slog.Logger) *Service {
	return &Service{
		eksClient:    eksClient,
		logger:       logger,
		PollInterval: defaultPollInterval,
	}
}

// addonsService returns a fresh addon service. Fresh per call (not cached)
// because addons.ServiceImpl memoizes the cluster's Kubernetes version, which
// goes stale between hops of a multi-minor upgrade.
func (s *Service) addonsService() *addons.ServiceImpl {
	return addons.NewService(s.eksClient, s.logger)
}

// describeCluster fetches the cluster with retry + error formatting.
func (s *Service) describeCluster(ctx context.Context, clusterName string) (*ekstypes.Cluster, error) {
	out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeClusterOutput, error) {
		return s.eksClient.DescribeCluster(rc, &eks.DescribeClusterInput{Name: aws.String(clusterName)})
	})
	if err != nil {
		return nil, awsinternal.FormatAWSError(err, fmt.Sprintf("describing cluster %s", clusterName))
	}
	if out.Cluster == nil {
		return nil, fmt.Errorf("cluster %s not found", clusterName)
	}
	return out.Cluster, nil
}

// nodegroupState is the per-nodegroup snapshot the planner works from.
type nodegroupState struct {
	Name      string
	Version   string
	AmiType   ekstypes.AMITypes
	Status    ekstypes.NodegroupStatus
	CustomAMI bool
}

// listNodegroupStates describes every nodegroup in the cluster.
func (s *Service) listNodegroupStates(ctx context.Context, clusterName string) ([]nodegroupState, error) {
	names, err := awsinternal.ListAllPages(ctx, fmt.Sprintf("listing nodegroups for cluster %s", clusterName),
		func(rc context.Context, token *string) (*eks.ListNodegroupsOutput, error) {
			return s.eksClient.ListNodegroups(rc, &eks.ListNodegroupsInput{
				ClusterName: aws.String(clusterName),
				NextToken:   token,
			})
		},
		func(out *eks.ListNodegroupsOutput) ([]string, *string) { return out.Nodegroups, out.NextToken },
	)
	if err != nil {
		return nil, err
	}

	states := make([]nodegroupState, 0, len(names))
	for _, name := range names {
		out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeNodegroupOutput, error) {
			return s.eksClient.DescribeNodegroup(rc, &eks.DescribeNodegroupInput{
				ClusterName:   aws.String(clusterName),
				NodegroupName: aws.String(name),
			})
		})
		if err != nil {
			return nil, awsinternal.FormatAWSError(err, fmt.Sprintf("describing nodegroup %s", name))
		}
		ng := out.Nodegroup
		if ng == nil {
			continue
		}
		states = append(states, nodegroupState{
			Name:      name,
			Version:   aws.ToString(ng.Version),
			AmiType:   ng.AmiType,
			Status:    ng.Status,
			CustomAMI: ng.AmiType == ekstypes.AMITypesCustom,
		})
	}
	return states, nil
}

// matchesAny reports whether name matches any of the substring patterns.
func matchesAny(name string, patterns []string) bool {
	for _, p := range patterns {
		if p != "" && strings.Contains(name, p) {
			return true
		}
	}
	return false
}

// waitForUpdate polls DescribeUpdate until the update succeeds, fails, or the
// context is cancelled. what labels the update in error messages.
func (s *Service) waitForUpdate(ctx context.Context, in *eks.DescribeUpdateInput, what string, progress ProgressFunc) error {
	interval := s.PollInterval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeUpdateOutput, error) {
				return s.eksClient.DescribeUpdate(rc, in)
			})
			if err != nil {
				// Transient describe failures shouldn't kill a long-running
				// upgrade watch; report and keep polling.
				progress("warning: checking %s: %v", what, err)
				continue
			}
			if out.Update == nil {
				continue
			}
			switch out.Update.Status {
			case ekstypes.UpdateStatusSuccessful:
				return nil
			case ekstypes.UpdateStatusFailed, ekstypes.UpdateStatusCancelled:
				return fmt.Errorf("%s %s: %s", what, strings.ToLower(string(out.Update.Status)), updateErrors(out.Update))
			}
		}
	}
}

// updateErrors flattens an update's error details for display.
func updateErrors(u *ekstypes.Update) string {
	if u == nil || len(u.Errors) == 0 {
		return "no error details reported"
	}
	msgs := make([]string, 0, len(u.Errors))
	for _, e := range u.Errors {
		msg := aws.ToString(e.ErrorMessage)
		if msg == "" {
			msg = string(e.ErrorCode)
		}
		msgs = append(msgs, msg)
	}
	return strings.Join(msgs, "; ")
}

// noopProgress is used when the caller passes a nil ProgressFunc.
func noopProgress(string, ...any) {}

func ensureProgress(p ProgressFunc) ProgressFunc {
	if p == nil {
		return noopProgress
	}
	return p
}
