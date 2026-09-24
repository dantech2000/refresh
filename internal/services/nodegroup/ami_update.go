package nodegroup

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/common"
	"github.com/dantech2000/refresh/internal/types"
)

// AMIUpdateOptions are the `nodegroup update` flags that change what happens
// to a nodegroup.
type AMIUpdateOptions struct {
	// Force rolls every nodegroup EKS can roll (--force).
	Force bool
	// Reroll rolls a nodegroup already on the latest AMI (--reroll).
	Reroll bool
	// Preview resolves the current and latest AMIs even when the action does
	// not depend on them (Force, Reroll), so a dry run can show them.
	Preview bool
}

// AMIUpdateDecision is what `nodegroup update` does with one nodegroup, and
// why. CurrentAMI and LatestAMI are "" when they were not looked up or the
// lookup failed.
type AMIUpdateDecision struct {
	Action     types.DryRunAction
	Reason     string
	CurrentAMI string
	LatestAMI  string
}

// Starts reports whether the decision starts a roll.
func (d AMIUpdateDecision) Starts() bool {
	return d.Action == types.ActionUpdate || d.Action == types.ActionForceUpdate
}

// DecideAMIUpdate is the one decision table behind `nodegroup update` and its
// --dry-run preview, so the preview always names the action the real run
// takes. amis returns the nodegroup's current and latest recommended AMI
// ("" when unknown); it is called at most once, and only when the action
// depends on it or opts.Preview asks for it.
func DecideAMIUpdate(ctx context.Context, ng *ekstypes.Nodegroup, opts AMIUpdateOptions, amis func(context.Context, *ekstypes.Nodegroup) (current, latest string)) AMIUpdateDecision {
	// Custom-AMI nodegroups are skipped even with --force: the AMI lives in
	// the launch template, so EKS can't select a recommended AMI.
	if ng.AmiType == ekstypes.AMITypesCustom {
		return AMIUpdateDecision{Action: types.ActionSkipCustom, Reason: "custom AMI (AmiType=CUSTOM); roll it by publishing a new launch template version"}
	}
	if ng.Status == ekstypes.NodegroupStatusUpdating {
		return AMIUpdateDecision{Action: types.ActionSkipUpdating, Reason: "already updating"}
	}

	var d AMIUpdateDecision
	if (!opts.Force && !opts.Reroll) || opts.Preview {
		d.CurrentAMI, d.LatestAMI = amis(ctx, ng)
	}
	switch {
	case opts.Force:
		d.Action, d.Reason = types.ActionForceUpdate, "force flag specified"
	case d.CurrentAMI == "" || d.LatestAMI == "":
		d.Action, d.Reason = types.ActionUpdate, "AMI status unknown, update recommended"
		if opts.Reroll && !opts.Preview {
			d.Reason = "--reroll rolls it whatever its AMI"
		}
	case d.CurrentAMI == d.LatestAMI && opts.Reroll:
		d.Action, d.Reason = types.ActionUpdate, "already on latest AMI; --reroll rolls it anyway"
	case d.CurrentAMI == d.LatestAMI:
		d.Action, d.Reason = types.ActionSkipLatest, "already on latest AMI"
	default:
		d.Action, d.Reason = types.ActionUpdate, "AMI is outdated"
	}
	return d
}

// AMIUpdateDecider returns DecideAMIUpdate for one cluster's nodegroups. The
// latest AMI is looked up at each nodegroup's own Kubernetes version (the
// update keeps the nodegroup on its minor), memoized across nodegroups; the
// cluster version, needed only as a fallback, is described once, on first
// use. If it can't be described, the AMI status is unknown, so the nodegroup
// is rolled rather than skipped. A describe cut short by its caller's ctx is
// not remembered: the next caller describes the cluster again.
func (s *ServiceImpl) AMIUpdateDecider(clusterName string, opts AMIUpdateOptions) func(context.Context, *ekstypes.Nodegroup) AMIUpdateDecision {
	latest := s.NewLatestAMICache()
	// The one key is the cluster; "" records a cluster that could not be
	// described, so a persistent failure is not retried per nodegroup.
	var versions common.Memo[struct{}, string]
	clusterVersion := func(ctx context.Context) string {
		v, err := versions.Get(ctx, struct{}{}, func(ctx context.Context) (string, error) {
			out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeClusterOutput, error) {
				return s.eksClient.DescribeCluster(rc, &eks.DescribeClusterInput{Name: aws.String(clusterName)})
			})
			if cerr := ctx.Err(); cerr != nil {
				return "", cerr
			}
			if err == nil && out.Cluster != nil {
				return aws.ToString(out.Cluster.Version), nil
			}
			// Remember "unknown" (a success for the memo), so a cluster that
			// can't be described is not described again per nodegroup.
			return "", nil
		})
		if err != nil {
			return ""
		}
		return v
	}
	amis := func(ctx context.Context, ng *ekstypes.Nodegroup) (string, string) {
		v := clusterVersion(ctx)
		if v == "" {
			return "", ""
		}
		l, err := latest.ForNodegroup(ctx, ng, v)
		if err != nil || l == "" {
			return "", ""
		}
		return s.currentAMI(ctx, ng), l
	}
	return func(ctx context.Context, ng *ekstypes.Nodegroup) AMIUpdateDecision {
		return DecideAMIUpdate(ctx, ng, opts, amis)
	}
}
