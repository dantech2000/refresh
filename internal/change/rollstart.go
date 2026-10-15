package change

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"

	"github.com/dantech2000/refresh/internal/awsx"
)

// StartNodegroupRoll asks EKS to roll the nodegroup onto the latest AMI of
// version, and returns the ID of the update. The ID is "" when EKS
// accepted the request and returned no update.
//
// version is always sent. The EKS reference contradicts itself about a
// request with no version: the operation text says that the nodegroup
// keeps its minor version, and the parameter text says that it moves to
// the version of the cluster. So a roll with no version could become a
// minor upgrade, and an empty version is refused here before any request.
//
// The request carries the version, and force only when it is true. It
// carries no release version and no launch template: EKS then picks the
// latest AMI release of the version.
//
// The SDK sets the client request token once for the call, in the input,
// so every retry of the call carries the same token and EKS starts one
// update, also when the answer to the first attempt was lost. A later
// call is a new request with a new token. The error is the error of the
// SDK: the caller says what it was doing.
func StartNodegroupRoll(ctx context.Context, c *awsx.Clients, t Target, nodegroup, version string, force bool) (updateID string, err error) {
	if version == "" {
		return "", fmt.Errorf("nodegroup %s/%s: no Kubernetes version to pin the roll to; a roll without one could become a version upgrade, so nothing was sent", t.Cluster, nodegroup)
	}
	out, err := c.EKS.UpdateNodegroupVersion(ctx, &eks.UpdateNodegroupVersionInput{
		ClusterName:   aws.String(t.Cluster),
		NodegroupName: aws.String(nodegroup),
		Version:       aws.String(version),
		Force:         force,
	})
	if err != nil {
		return "", err
	}
	if out.Update == nil {
		return "", nil
	}
	return aws.ToString(out.Update.Id), nil
}
