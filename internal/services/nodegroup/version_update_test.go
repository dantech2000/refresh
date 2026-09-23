package nodegroup

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/smithy-go"

	"github.com/dantech2000/refresh/internal/mocks"
)

// A throttled start is retried with the SAME idempotency token, so EKS sees
// one logical request and starts one roll.
func TestStartVersionUpdate_RetriesThrottlingWithOneToken(t *testing.T) {
	var tokens []string
	m := &mocks.EKSAPI{
		DescribeNodegroupFn: describeNodegroupAt("1.31"),
		UpdateNodegroupVersionFn: func(_ context.Context, in *eks.UpdateNodegroupVersionInput, _ ...func(*eks.Options)) (*eks.UpdateNodegroupVersionOutput, error) {
			tokens = append(tokens, aws.ToString(in.ClientRequestToken))
			if !in.Force {
				t.Error("Force was not passed through")
			}
			if len(tokens) <= 2 {
				return nil, &smithy.GenericAPIError{Code: "ThrottlingException", Message: "Rate exceeded"}
			}
			return &eks.UpdateNodegroupVersionOutput{Update: &ekstypes.Update{
				Id:     aws.String("upd-1"),
				Status: ekstypes.UpdateStatusInProgress,
			}}, nil
		},
	}
	svc := newTestService(m)

	upd, err := svc.StartVersionUpdate(context.Background(), "prod", "ng-a", VersionUpdateOptions{Force: true})
	if err != nil {
		t.Fatalf("StartVersionUpdate: %v", err)
	}
	if aws.ToString(upd.Id) != "upd-1" || upd.Status != ekstypes.UpdateStatusInProgress {
		t.Errorf("update = %+v, want upd-1 InProgress", upd)
	}
	if len(tokens) != 3 {
		t.Fatalf("UpdateNodegroupVersion calls = %d, want 3", len(tokens))
	}
	if tokens[0] == "" || tokens[0] != tokens[1] || tokens[1] != tokens[2] {
		t.Errorf("tokens = %q, want one non-empty token reused across retries", tokens)
	}
}

// AccessDenied is permanent: no retry, and the error is formatted with
// permission guidance that names the denied IAM action.
func TestStartVersionUpdate_AccessDeniedIsFormattedNotRetried(t *testing.T) {
	m := &mocks.EKSAPI{
		DescribeNodegroupFn: describeNodegroupAt("1.31"),
		UpdateNodegroupVersionFn: func(context.Context, *eks.UpdateNodegroupVersionInput, ...func(*eks.Options)) (*eks.UpdateNodegroupVersionOutput, error) {
			return nil, &smithy.GenericAPIError{
				Code:    "AccessDeniedException",
				Message: "User arn:aws:iam::123:user/x is not authorized to perform eks:UpdateNodegroupVersion",
			}
		},
	}
	svc := newTestService(m)

	_, err := svc.StartVersionUpdate(context.Background(), "prod", "ng-a", VersionUpdateOptions{})
	if err == nil {
		t.Fatal("want an error")
	}
	if m.Calls.UpdateNodegroupVersion != 1 {
		t.Errorf("UpdateNodegroupVersion calls = %d, want 1 (AccessDenied is not retryable)", m.Calls.UpdateNodegroupVersion)
	}
	msg := err.Error()
	for _, want := range []string{"permissions", "eks:UpdateNodegroupVersion", "prod/ng-a"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error missing %q:\n%s", want, msg)
		}
	}
}

func TestDescribeNodegroup_RetriesAndFormats(t *testing.T) {
	calls := 0
	m := &mocks.EKSAPI{
		DescribeNodegroupFn: func(context.Context, *eks.DescribeNodegroupInput, ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
			calls++
			if calls == 1 {
				return nil, &smithy.GenericAPIError{Code: "ThrottlingException", Message: "Rate exceeded"}
			}
			return &eks.DescribeNodegroupOutput{Nodegroup: stubNodegroup("ng-a", ekstypes.NodegroupStatusActive)}, nil
		},
	}
	ng, err := newTestService(m).DescribeNodegroup(context.Background(), "prod", "ng-a")
	if err != nil || aws.ToString(ng.NodegroupName) != "ng-a" || calls != 2 {
		t.Fatalf("DescribeNodegroup = %+v, %v after %d calls; want ng-a, nil, 2", ng, err, calls)
	}

	empty := &mocks.EKSAPI{
		DescribeNodegroupFn: func(context.Context, *eks.DescribeNodegroupInput, ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
			return &eks.DescribeNodegroupOutput{}, nil
		},
	}
	if _, err := newTestService(empty).DescribeNodegroup(context.Background(), "prod", "ng-a"); err == nil || !strings.Contains(err.Error(), "empty response") {
		t.Errorf("empty describe: err = %v, want empty response", err)
	}
}

// describeNodegroupAt answers DescribeNodegroup with a nodegroup on version.
func describeNodegroupAt(version string) func(context.Context, *eks.DescribeNodegroupInput, ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
	return func(_ context.Context, in *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
		ng := stubNodegroup(aws.ToString(in.NodegroupName), ekstypes.NodegroupStatusActive)
		ng.Version = aws.String(version)
		return &eks.DescribeNodegroupOutput{Nodegroup: ng}, nil
	}
}

// An AMI patch pins Version to the nodegroup's current minor, so it can never
// become a minor-version upgrade to the cluster's version.
func TestStartVersionUpdate_PinsCurrentVersion(t *testing.T) {
	var got *string
	m := &mocks.EKSAPI{
		DescribeNodegroupFn: describeNodegroupAt("1.31"),
		UpdateNodegroupVersionFn: func(_ context.Context, in *eks.UpdateNodegroupVersionInput, _ ...func(*eks.Options)) (*eks.UpdateNodegroupVersionOutput, error) {
			got = in.Version
			return &eks.UpdateNodegroupVersionOutput{Update: &ekstypes.Update{Id: aws.String("upd-1")}}, nil
		},
	}
	if _, err := newTestService(m).StartVersionUpdate(context.Background(), "prod", "ng-a", VersionUpdateOptions{}); err != nil {
		t.Fatalf("StartVersionUpdate: %v", err)
	}
	if aws.ToString(got) != "1.31" {
		t.Errorf("UpdateNodegroupVersion Version = %v, want 1.31", aws.ToString(got))
	}
}

// If the nodegroup can't be described, no update is started: sending the
// request without a Version could upgrade the minor.
func TestStartVersionUpdate_DescribeFailureStartsNothing(t *testing.T) {
	m := &mocks.EKSAPI{
		DescribeNodegroupFn: func(context.Context, *eks.DescribeNodegroupInput, ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
			return nil, mocks.AccessDenied()
		},
	}
	if _, err := newTestService(m).StartVersionUpdate(context.Background(), "prod", "ng-a", VersionUpdateOptions{}); err == nil {
		t.Fatal("want an error")
	}
	if m.Calls.UpdateNodegroupVersion != 0 {
		t.Errorf("UpdateNodegroupVersion calls = %d, want 0", m.Calls.UpdateNodegroupVersion)
	}
}
