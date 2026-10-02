package nodegroup

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	asgtypes "github.com/aws/aws-sdk-go-v2/service/autoscaling/types"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/common"
	"github.com/dantech2000/refresh/internal/diag"
)

// ScalingActivitiesAPI is the Auto Scaling call ScalingFailures makes.
type ScalingActivitiesAPI interface {
	DescribeScalingActivities(ctx context.Context, in *autoscaling.DescribeScalingActivitiesInput, optFns ...func(*autoscaling.Options)) (*autoscaling.DescribeScalingActivitiesOutput, error)
}

// ScalingFailure is an Auto Scaling activity of a nodegroup's group that
// failed, such as a launch the EC2 vCPU quota refused. EKS reports a roll
// that waits on it only as an update in progress.
type ScalingFailure struct {
	// ID is the activity ID: the same failure is reported once.
	ID    string
	At    time.Time
	Group string
	// Message is what Auto Scaling said, on one line.
	Message string
}

// scalingActivitiesPage is how many of a group's newest activities one read
// covers. A roll that waits for capacity retries the launch every few
// minutes, so the newest ones hold the failure.
const scalingActivitiesPage = 20

// ScalingFailures returns the failed and cancelled activities of groups
// that started at or after since, oldest first. It reads the newest
// scalingActivitiesPage activities of each group.
func ScalingFailures(ctx context.Context, api ScalingActivitiesAPI, groups []string, since time.Time) ([]ScalingFailure, error) {
	var out []ScalingFailure
	for _, group := range groups {
		resp, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*autoscaling.DescribeScalingActivitiesOutput, error) {
			return api.DescribeScalingActivities(rc, &autoscaling.DescribeScalingActivitiesInput{
				AutoScalingGroupName: aws.String(group),
				MaxRecords:           aws.Int32(scalingActivitiesPage),
			})
		})
		if err != nil {
			return nil, diag.WithOperation(diag.OpDescribeScalingActivities, awsinternal.FormatAWSError(err, "reading the scaling activities of "+group))
		}
		for _, a := range resp.Activities {
			if a.StatusCode != asgtypes.ScalingActivityStatusCodeFailed && a.StatusCode != asgtypes.ScalingActivityStatusCodeCancelled {
				continue
			}
			at := aws.ToTime(a.StartTime)
			if at.Before(since) {
				continue
			}
			msg := strings.Join(strings.Fields(aws.ToString(a.StatusMessage)), " ")
			if msg == "" {
				msg = strings.Join(strings.Fields(aws.ToString(a.Description)), " ")
			}
			out = append(out, ScalingFailure{ID: aws.ToString(a.ActivityId), At: at, Group: group, Message: msg})
		}
	}
	// Oldest first, so a report reads in order.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].At.Before(out[j-1].At); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

// ScalingFailures reads the failed Auto Scaling activities of nodegroup's
// groups since a time (see ScalingFailures).
func (s *ServiceImpl) ScalingFailures(ctx context.Context, clusterName, nodegroupName string, since time.Time) ([]ScalingFailure, error) {
	ng, err := s.DescribeNodegroup(ctx, clusterName, nodegroupName)
	if err != nil {
		return nil, diag.WithOperation(diag.OpDescribeNodegroup, err)
	}
	var groups []string
	if ng.Resources != nil {
		for _, g := range ng.Resources.AutoScalingGroups {
			if name := aws.ToString(g.Name); name != "" {
				groups = append(groups, name)
			}
		}
	}
	if len(groups) == 0 {
		return nil, fmt.Errorf("nodegroup %s/%s has no Auto Scaling group yet", clusterName, nodegroupName)
	}
	return ScalingFailures(ctx, s.asgClient, groups, since)
}

// WatchScalingFailures reads failures with read every interval until ctx
// ends, and calls report once for each new one. It is best effort: a read
// that fails is tried again on the next tick, and a permanent error (such
// as a missing autoscaling:DescribeScalingActivities permission) ends the
// watch quietly.
func WatchScalingFailures(ctx context.Context, interval time.Duration, read func(context.Context) ([]ScalingFailure, error), report func(ScalingFailure)) {
	seen := map[string]bool{}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		failures, err := read(ctx)
		if err != nil {
			if common.IsPermanentAPIError(err) {
				return
			}
			continue
		}
		for _, f := range failures {
			if !seen[f.ID] {
				seen[f.ID] = true
				report(f)
			}
		}
	}
}
