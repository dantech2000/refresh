package cluster

import (
	"context"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/common"
)

// BusyAPI is the part of the EKS API that ChangesInProgress reads.
type BusyAPI interface {
	DescribeCluster(ctx context.Context, params *eks.DescribeClusterInput, optFns ...func(*eks.Options)) (*eks.DescribeClusterOutput, error)
	ListNodegroups(ctx context.Context, params *eks.ListNodegroupsInput, optFns ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error)
	DescribeNodegroup(ctx context.Context, params *eks.DescribeNodegroupInput, optFns ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error)
	ListAddons(ctx context.Context, params *eks.ListAddonsInput, optFns ...func(*eks.Options)) (*eks.ListAddonsOutput, error)
	DescribeAddon(ctx context.Context, params *eks.DescribeAddonInput, optFns ...func(*eks.Options)) (*eks.DescribeAddonOutput, error)
}

// Change kinds.
const (
	ChangeCluster   = "cluster"
	ChangeNodegroup = "nodegroup"
	ChangeAddon     = "add-on"
)

// Change is one resource EKS is changing. Name is empty for the cluster.
type Change struct {
	Kind   string
	Name   string
	Status string
}

// String reads "nodegroup ng-a UPDATING", or "cluster UPDATING".
func (c Change) String() string {
	if c.Name == "" {
		return c.Kind + " " + c.Status
	}
	return c.Kind + " " + c.Name + " " + c.Status
}

// Changes is what ChangesInProgress found.
type Changes []Change

// String joins the changes with ", ".
func (cs Changes) String() string {
	parts := make([]string, len(cs))
	for i, c := range cs {
		parts[i] = c.String()
	}
	return strings.Join(parts, ", ")
}

// ChangesInProgress reads, right now, what EKS is changing on cluster: the
// cluster itself (status not ACTIVE), each nodegroup not ACTIVE or DEGRADED,
// and each add-on CREATING, UPDATING, or DELETING. EKS rejects a second
// update while one runs (ResourceInUseException), but only after the user
// has confirmed; callers check this first and name what is running.
func ChangesInProgress(ctx context.Context, api BusyAPI, cluster string) (Changes, error) {
	var busy Changes
	desc, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeClusterOutput, error) {
		return api.DescribeCluster(rc, &eks.DescribeClusterInput{Name: aws.String(cluster)})
	})
	if err != nil {
		return nil, awsinternal.FormatAWSError(err, "describing cluster "+cluster)
	}
	if desc.Cluster != nil && desc.Cluster.Status != ekstypes.ClusterStatusActive {
		busy = append(busy, Change{Kind: ChangeCluster, Status: string(desc.Cluster.Status)})
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
		if out.Nodegroup == nil {
			continue
		}
		if s := out.Nodegroup.Status; s != ekstypes.NodegroupStatusActive && s != ekstypes.NodegroupStatusDegraded {
			busy = append(busy, Change{Kind: ChangeNodegroup, Name: ng, Status: string(s)})
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
		if out.Addon == nil {
			continue
		}
		switch out.Addon.Status {
		case ekstypes.AddonStatusCreating, ekstypes.AddonStatusUpdating, ekstypes.AddonStatusDeleting:
			busy = append(busy, Change{Kind: ChangeAddon, Name: a, Status: string(out.Addon.Status)})
		}
	}
	return busy, nil
}
