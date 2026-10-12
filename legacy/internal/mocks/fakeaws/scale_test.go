package fakeaws

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/smithy-go"
)

func eksIn(s *Server, region string) *eks.Client {
	return eks.New(eks.Options{Region: region, BaseEndpoint: aws.String(s.URL), RetryMaxAttempts: 1,
		Credentials: aws.NewCredentialsCache(aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "AKIDFAKE", SecretAccessKey: "fake"}, nil
		}))})
}

func TestStartServesClustersByRegion(t *testing.T) {
	s, stop := Start(&Cluster{Name: "east", Version: "1.34", Region: "us-east-1"}, &Cluster{Name: "west", Version: "1.34", Region: "us-west-2"}, &Cluster{Name: "any", Version: "1.34"})
	defer stop()
	out, err := eksIn(s, "us-east-1").ListClusters(t.Context(), &eks.ListClustersInput{})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(out.Clusters)
	if !slices.Equal(out.Clusters, []string{"any", "east"}) {
		t.Errorf("us-east-1 lists %v, want any and east", out.Clusters)
	}
	var nf interface{ ErrorCode() string }
	if _, err := eksIn(s, "us-east-1").DescribeCluster(t.Context(), &eks.DescribeClusterInput{Name: aws.String("west")}); !errors.As(err, &nf) || nf.ErrorCode() != "ResourceNotFoundException" {
		t.Errorf("describing a cluster of another region = %v, want ResourceNotFoundException", err)
	}
}

func TestLatencyAndThrottle(t *testing.T) {
	s, stop := Start(&Cluster{Name: "c", Version: "1.34"})
	defer stop()
	s.SetLatency(func(region, service, _ string) time.Duration {
		if region == "us-east-1" && service == "eks" {
			return 50 * time.Millisecond
		}
		return 0
	})
	start := time.Now()
	if _, err := eksIn(s, "us-east-1").ListClusters(t.Context(), &eks.ListClustersInput{}); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < 50*time.Millisecond {
		t.Errorf("call took %v, want at least the 50ms latency", d)
	}
	s.SetThrottle(func(string, string) bool { return true })
	var api smithy.APIError
	if _, err := eksIn(s, "us-east-1").ListClusters(t.Context(), &eks.ListClustersInput{}); !errors.As(err, &api) || api.ErrorCode() != "ThrottlingException" {
		t.Errorf("throttled call = %v, want ThrottlingException", err)
	}
}

func TestSSMGetParameter(t *testing.T) {
	s, stop := Start()
	defer stop()
	c := ssm.New(ssm.Options{Region: "us-east-1", BaseEndpoint: aws.String(s.URL), RetryMaxAttempts: 1,
		Credentials: aws.NewCredentialsCache(aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "AKIDFAKE", SecretAccessKey: "fake"}, nil
		}))})
	for name, want := range map[string]string{
		"/aws/service/eks/optimized-ami/1.34/amazon-linux-2023/x86_64/standard/recommended/image_id":        "ami-latest134",
		"/aws/service/eks/optimized-ami/1.34/amazon-linux-2023/x86_64/standard/recommended/release_version": "1.34.0-20260101",
		"/aws/service/bottlerocket/aws-k8s-1.35/x86_64/latest/image_id":                                     "ami-latest135",
	} {
		out, err := c.GetParameter(t.Context(), &ssm.GetParameterInput{Name: aws.String(name)})
		if err != nil || aws.ToString(out.Parameter.Value) != want {
			t.Errorf("GetParameter(%s) = %v, %v; want %s", name, out, err, want)
		}
	}
	s.FailSSM("AccessDeniedException")
	var api smithy.APIError
	if _, err := c.GetParameter(t.Context(), &ssm.GetParameterInput{Name: aws.String("/aws/service/eks/optimized-ami/1.34/x/image_id")}); !errors.As(err, &api) || api.ErrorCode() != "AccessDeniedException" {
		t.Errorf("FailSSM: %v, want AccessDeniedException", err)
	}
}

// From review: when the fake's latest release differed from the release its
// nodegroups report, human dry runs in command tests fetched release notes
// from GitHub. The two must match, so no release delta is ever shown.
func TestSSMLatestReleaseMatchesTheNodegroups(t *testing.T) {
	s, stop := Start(&Cluster{Name: "c", Version: "1.34", Nodegroups: []*Nodegroup{{Name: "ng", Version: "1.34"}}})
	defer stop()
	ng, err := eksIn(s, "us-east-1").DescribeNodegroup(t.Context(), &eks.DescribeNodegroupInput{ClusterName: aws.String("c"), NodegroupName: aws.String("ng")})
	if err != nil {
		t.Fatal(err)
	}
	c := ssm.New(ssm.Options{Region: "us-east-1", BaseEndpoint: aws.String(s.URL), RetryMaxAttempts: 1,
		Credentials: aws.NewCredentialsCache(aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "AKIDFAKE", SecretAccessKey: "fake"}, nil
		}))})
	out, err := c.GetParameter(t.Context(), &ssm.GetParameterInput{Name: aws.String("/aws/service/eks/optimized-ami/1.34/amazon-linux-2023/x86_64/standard/recommended/release_version")})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := aws.ToString(out.Parameter.Value), aws.ToString(ng.Nodegroup.ReleaseVersion); got != want {
		t.Errorf("latest release %q, nodegroup release %q: a delta makes commands fetch release notes", got, want)
	}
}
