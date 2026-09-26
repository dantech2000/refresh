package live

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/commands/factory"
	"github.com/dantech2000/refresh/internal/common"
)

// changesInProgress reads, right now, what EKS is changing on cluster: the
// cluster itself, and each nodegroup and add-on that is not settled. The
// fleet sweep can be a minute old, and between two steps of an upgrade run
// elsewhere (the CLI, the console) the cluster reads ACTIVE for a moment:
// Start checks this before it changes anything. EKS also rejects a second
// update while one is running (ResourceInUseException), but only after the
// user has confirmed; this says it first, and names what is running.
func changesInProgress(ctx context.Context, cfg aws.Config, cluster string) ([]string, error) {
	api := factory.NewEKSClient(cfg)
	var busy []string
	desc, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeClusterOutput, error) {
		return api.DescribeCluster(rc, &eks.DescribeClusterInput{Name: aws.String(cluster)})
	})
	if err != nil {
		return nil, awsinternal.FormatAWSError(err, "describing cluster "+cluster)
	}
	if desc.Cluster != nil && desc.Cluster.Status != ekstypes.ClusterStatusActive {
		busy = append(busy, "cluster "+string(desc.Cluster.Status))
	}
	ngs, err := awsinternal.ListAllPages(ctx, "listing nodegroups of "+cluster,
		func(rc context.Context, token *string) (*eks.ListNodegroupsOutput, error) {
			return api.ListNodegroups(rc, &eks.ListNodegroupsInput{ClusterName: aws.String(cluster), NextToken: token})
		},
		func(out *eks.ListNodegroupsOutput) ([]string, *string) { return out.Nodegroups, out.NextToken })
	if err != nil {
		return nil, err
	}
	for _, ng := range ngs {
		out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeNodegroupOutput, error) {
			return api.DescribeNodegroup(rc, &eks.DescribeNodegroupInput{ClusterName: aws.String(cluster), NodegroupName: aws.String(ng)})
		})
		if err != nil {
			return nil, awsinternal.FormatAWSError(err, "describing nodegroup "+ng)
		}
		if s := out.Nodegroup.Status; s != ekstypes.NodegroupStatusActive && s != ekstypes.NodegroupStatusDegraded {
			busy = append(busy, fmt.Sprintf("nodegroup %s %s", ng, s))
		}
	}
	addons, err := awsinternal.ListAllPages(ctx, "listing add-ons of "+cluster,
		func(rc context.Context, token *string) (*eks.ListAddonsOutput, error) {
			return api.ListAddons(rc, &eks.ListAddonsInput{ClusterName: aws.String(cluster), NextToken: token})
		},
		func(out *eks.ListAddonsOutput) ([]string, *string) { return out.Addons, out.NextToken })
	if err != nil {
		return nil, err
	}
	for _, a := range addons {
		out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeAddonOutput, error) {
			return api.DescribeAddon(rc, &eks.DescribeAddonInput{ClusterName: aws.String(cluster), AddonName: aws.String(a)})
		})
		if err != nil {
			return nil, awsinternal.FormatAWSError(err, "describing add-on "+a)
		}
		switch out.Addon.Status {
		case ekstypes.AddonStatusCreating, ekstypes.AddonStatusUpdating, ekstypes.AddonStatusDeleting:
			busy = append(busy, fmt.Sprintf("add-on %s %s", a, out.Addon.Status))
		}
	}
	return busy, nil
}
