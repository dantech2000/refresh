package nodegroup

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/common"
)

// DescribeNodegroup returns the raw EKS nodegroup, retrying transient errors.
// `nodegroup update` uses it to apply its skip rules (custom AMI, already
// updating, already on the latest AMI) before StartVersionUpdate.
func (s *ServiceImpl) DescribeNodegroup(ctx context.Context, clusterName, nodegroupName string) (*ekstypes.Nodegroup, error) {
	out, err := common.WithRetry(ctx, common.DefaultRetryConfig,
		func(rc context.Context) (*eks.DescribeNodegroupOutput, error) {
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
	return out.Nodegroup, nil
}

// VersionUpdateOptions controls StartVersionUpdate.
type VersionUpdateOptions struct {
	// Force sets UpdateNodegroupVersion.Force: evict pods even when a
	// PodDisruptionBudget blocks the drain.
	Force bool
}

// StartVersionUpdate starts an UpdateNodegroupVersion roll to the latest AMI
// for the nodegroup's current Kubernetes version and returns the EKS update.
//
// The request pins Version to the nodegroup's current Kubernetes minor. The
// UpdateNodegroupVersion reference contradicts itself on an omitted Version
// (the operation text says it keeps the current minor, the parameter text
// says it moves to the cluster's minor), so an AMI patch sends it explicitly
// and can never turn into a minor-version upgrade.
//
// StartNodegroupRoll pins one idempotency token across the request's
// retries; a later run (or a fleet revisit) gets a new token.
func (s *ServiceImpl) StartVersionUpdate(ctx context.Context, clusterName, nodegroupName string, opts VersionUpdateOptions) (*ekstypes.Update, error) {
	ng, err := s.DescribeNodegroup(ctx, clusterName, nodegroupName)
	if err != nil {
		return nil, err
	}
	update, err := StartNodegroupRoll(ctx, s.eksClient, clusterName, nodegroupName, aws.ToString(ng.Version), opts.Force)
	if err != nil {
		return nil, awsinternal.FormatAWSError(err, fmt.Sprintf("starting the version update for nodegroup %s/%s", clusterName, nodegroupName))
	}
	return update, nil
}

// VersionUpdater is the EKS call that starts a nodegroup roll.
type VersionUpdater interface {
	UpdateNodegroupVersion(ctx context.Context, params *eks.UpdateNodegroupVersionInput, optFns ...func(*eks.Options)) (*eks.UpdateNodegroupVersionOutput, error)
}

// StartNodegroupRoll starts one UpdateNodegroupVersion roll: `nodegroup
// update` (an AMI patch at the nodegroup's own version) and `cluster upgrade`
// (a roll to the target version) both start rolls here. version "" omits
// Version from the request.
//
// One idempotency token is computed per call, outside the retry, so a retried
// request is the SAME request and can't start a second roll. The token does
// not span separate calls: a later run gets a new token. The error is the raw
// SDK error; the caller formats it for its own operation.
func StartNodegroupRoll(ctx context.Context, api VersionUpdater, clusterName, nodegroupName, version string, force bool) (*ekstypes.Update, error) {
	input := &eks.UpdateNodegroupVersionInput{
		ClusterName:        aws.String(clusterName),
		NodegroupName:      aws.String(nodegroupName),
		Force:              force,
		ClientRequestToken: aws.String(common.IdempotencyToken()), // stable across retries of this call
	}
	if version != "" {
		input.Version = aws.String(version)
	}
	out, err := common.WithRetry(ctx, common.DefaultRetryConfig,
		func(rc context.Context) (*eks.UpdateNodegroupVersionOutput, error) {
			return api.UpdateNodegroupVersion(rc, input)
		})
	if err != nil {
		return nil, err
	}
	return out.Update, nil
}
