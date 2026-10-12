package awsxtest_test

import (
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/smithy-go"
	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// TestRequestLog checks that the log keeps the order of the requests and
// decodes the input of each protocol.
func TestRequestLog(t *testing.T) {
	f := newFixture(t)
	ctx := testContext(t)
	c := f.clients

	if _, err := c.eks.ListNodegroups(ctx, &eks.ListNodegroupsInput{ClusterName: aws.String("prod"), MaxResults: aws.Int32(50)}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.eks.UpdateNodegroupVersion(ctx, &eks.UpdateNodegroupVersionInput{
		ClusterName: aws.String("prod"), NodegroupName: aws.String("ng-a"), Version: aws.String("1.32"),
		Force: true, ClientRequestToken: aws.String(token),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		MaxResults: aws.Int32(5),
		Filters: []ec2types.Filter{
			{Name: aws.String("tag:eks:eks-cluster-name"), Values: []string{"prod"}},
			{Name: aws.String("tag-key"), Values: []string{"karpenter.sh/nodepool", "karpenter.sh/provisioner-name"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.asg.DescribeAutoScalingGroups(ctx, &autoscaling.DescribeAutoScalingGroupsInput{AutoScalingGroupNames: []string{"eks-ng-a"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ssm.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String("/custom")}); err == nil {
		t.Fatal("GetParameter of an unknown name succeeded")
	}

	want := []awsxtest.Request{
		{
			Operation: fleettest.EKSListNodegroups, Service: "eks", Region: region,
			Input: map[string]any{"clusterName": "prod", "maxResults": "50"},
		},
		{
			Operation: fleettest.EKSUpdateNodegroupVersion, Service: "eks", Region: region, Mutates: true,
			Input: map[string]any{"clusterName": "prod", "nodegroupName": "ng-a", "version": "1.32", "force": true, "clientRequestToken": token},
		},
		{
			Operation: fleettest.EC2DescribeInstances, Service: "ec2", Region: region,
			Input: map[string]any{
				"MaxResults":       "5",
				"Filter.1.Name":    "tag:eks:eks-cluster-name",
				"Filter.1.Value.1": "prod",
				"Filter.2.Name":    "tag-key",
				"Filter.2.Value.1": "karpenter.sh/nodepool",
				"Filter.2.Value.2": "karpenter.sh/provisioner-name",
			},
		},
		{
			Operation: fleettest.AutoScalingDescribeAutoScalingGroups, Service: "autoscaling", Region: region,
			Input: map[string]any{"AutoScalingGroupNames.member.1": "eks-ng-a"},
		},
		{
			Operation: fleettest.SSMGetParameter, Service: "ssm", Region: region,
			Input: map[string]any{"Name": "/custom"},
		},
	}
	if diff := cmp.Diff(want, f.server.Requests()); diff != "" {
		t.Errorf("request log (-want +got):\n%s", diff)
	}
}

// TestUpdateFollowsTheClock drives a nodegroup roll through the SDK: the
// update and the nodegroup change only when the world's clock passes the
// scripted duration.
func TestUpdateFollowsTheClock(t *testing.T) {
	tests := []struct {
		name        string
		script      fleettest.UpdateScript
		wantStatus  ekstypes.UpdateStatus
		wantRelease string
		wantErrors  int
	}{
		{name: "successful", script: fleettest.UpdateScript{After: 10 * time.Minute}, wantStatus: ekstypes.UpdateStatusSuccessful, wantRelease: "1.32.0-20260101"},
		{name: "failed", script: fleettest.UpdateScript{After: 10 * time.Minute, Result: fleettest.UpdateFailed}, wantStatus: ekstypes.UpdateStatusFailed, wantRelease: "1.32.0-20250101", wantErrors: 1},
		{name: "never", script: fleettest.UpdateScript{Never: true}, wantStatus: ekstypes.UpdateStatusInProgress, wantRelease: "1.32.0-20250101"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			ctx := testContext(t)
			f.world.Edit(func(s *fleettest.State) {
				s.Cluster(region, "prod").Nodegroup("ng-a").UpdateScripts = []fleettest.UpdateScript{tt.script}
			})
			describe := func() (ekstypes.UpdateStatus, int, *ekstypes.Nodegroup) {
				t.Helper()
				u, err := f.clients.eks.DescribeUpdate(ctx, &eks.DescribeUpdateInput{
					Name: aws.String("prod"), NodegroupName: aws.String("ng-a"), UpdateId: aws.String(firstUpdate),
				})
				if err != nil {
					t.Fatal(err)
				}
				ng, err := f.clients.eks.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{ClusterName: aws.String("prod"), NodegroupName: aws.String("ng-a")})
				if err != nil {
					t.Fatal(err)
				}
				return u.Update.Status, len(u.Update.Errors), ng.Nodegroup
			}

			started, err := f.clients.eks.UpdateNodegroupVersion(ctx, &eks.UpdateNodegroupVersionInput{ClusterName: aws.String("prod"), NodegroupName: aws.String("ng-a")})
			if err != nil {
				t.Fatal(err)
			}
			if got := aws.ToString(started.Update.Id); got != firstUpdate {
				t.Errorf("update ID = %q, want %q", got, firstUpdate)
			}

			f.world.Advance(10*time.Minute - time.Second)
			status, _, ng := describe()
			if status != ekstypes.UpdateStatusInProgress || ng.Status != ekstypes.NodegroupStatusUpdating {
				t.Errorf("before the update is due: update %s, nodegroup %s; want InProgress, UPDATING", status, ng.Status)
			}

			f.world.Advance(time.Second)
			status, errs, ng := describe()
			if status != tt.wantStatus || errs != tt.wantErrors {
				t.Errorf("update = %s with %d errors, want %s with %d", status, errs, tt.wantStatus, tt.wantErrors)
			}
			if got := aws.ToString(ng.ReleaseVersion); got != tt.wantRelease {
				t.Errorf("release version = %q, want %q", got, tt.wantRelease)
			}
			wantNodegroup := ekstypes.NodegroupStatusActive
			if tt.script.Never {
				wantNodegroup = ekstypes.NodegroupStatusUpdating
			}
			if ng.Status != wantNodegroup {
				t.Errorf("nodegroup status = %s, want %s", ng.Status, wantNodegroup)
			}
		})
	}
}

// TestAPIErrors checks the errors the server answers on its own, without a
// fault.
func TestAPIErrors(t *testing.T) {
	f := newFixture(t)
	ctx := testContext(t)
	c := f.clients
	f.world.Edit(func(s *fleettest.State) { s.DefaultUpdate = fleettest.UpdateScript{Never: true} })
	roll := func(tok string) error {
		_, err := c.eks.UpdateNodegroupVersion(ctx, &eks.UpdateNodegroupVersionInput{
			ClusterName: aws.String("prod"), NodegroupName: aws.String("ng-a"), ClientRequestToken: aws.String(tok),
		})
		return err
	}
	if err := roll(token); err != nil {
		t.Fatal(err)
	}
	if err := roll(token); err != nil {
		t.Errorf("a retry with the same client request token: %v", err)
	}

	tests := []struct {
		name string
		call func() error
		want string
	}{
		{"cluster in another region", func() error {
			_, err := c.eks.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String("elsewhere")})
			return err
		}, "ResourceNotFoundException"},
		{"unknown nodegroup", func() error {
			_, err := c.eks.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{ClusterName: aws.String("prod"), NodegroupName: aws.String("nope")})
			return err
		}, "ResourceNotFoundException"},
		{"nodegroup update described without its nodegroup", func() error {
			_, err := c.eks.DescribeUpdate(ctx, &eks.DescribeUpdateInput{Name: aws.String("prod"), UpdateId: aws.String(firstUpdate)})
			return err
		}, "ResourceNotFoundException"},
		{"second update of a busy nodegroup", func() error { return roll(token + "x") }, "ResourceInUseException"},
		{"short client request token", func() error {
			_, err := c.eks.UpdateClusterVersion(ctx, &eks.UpdateClusterVersionInput{
				Name: aws.String("prod"), Version: aws.String("1.33"), ClientRequestToken: aws.String("0123456789abcdef0123456789abcdef"),
			})
			return err
		}, "InvalidParameterException"},
		{"unknown instance", func() error {
			_, err := c.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{"i-gone"}})
			return err
		}, "InvalidInstanceID.NotFound"},
		{"filter the fake does not model", func() error {
			_, err := c.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
				Filters: []ec2types.Filter{{Name: aws.String("vpc-id"), Values: []string{"vpc-1"}}},
			})
			return err
		}, "InvalidParameterValue"},
		{"operation the fake does not model", func() error {
			_, err := c.eks.ListFargateProfiles(ctx, &eks.ListFargateProfilesInput{ClusterName: aws.String("prod")})
			return err
		}, "UnsupportedOperation"},
		{"operation of another protocol the fake does not model", func() error {
			_, err := c.ec2.DescribeRegions(ctx, &ec2.DescribeRegionsInput{})
			return err
		}, "UnsupportedOperation"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call()
			var apiErr smithy.APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("got %v, want a smithy.APIError", err)
			}
			if apiErr.ErrorCode() != tt.want {
				t.Errorf("error code = %q, want %q (%v)", apiErr.ErrorCode(), tt.want, err)
			}
		})
	}

	updates, err := c.eks.ListUpdates(ctx, &eks.ListUpdatesInput{Name: aws.String("prod"), NodegroupName: aws.String("ng-a")})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{firstUpdate}, updates.UpdateIds); diff != "" {
		t.Errorf("the retried and the refused calls started updates (-want +got):\n%s", diff)
	}
}

// TestCloseReleasesHeldRequests checks that Close does not wait for a
// request that a fault still holds.
func TestCloseReleasesHeldRequests(t *testing.T) {
	f := newFixture(t)
	ctx := testContext(t)
	f.world.SetFault(fleettest.EKSListClusters, fleettest.Fault{Hang: true})

	done := make(chan error, 1)
	go func() {
		_, err := f.once.eks.ListClusters(ctx, &eks.ListClustersInput{})
		done <- err
	}()
	if err := f.world.AwaitBlocked(ctx, 1); err != nil {
		t.Fatal(err)
	}
	f.server.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Error("the held request succeeded")
		}
	case <-ctx.Done():
		t.Fatal("the held request did not return after Close")
	}
}
