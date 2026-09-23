package aws

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/smithy-go"
)

func TestCurrentAmiIDEmptyNodegroupPaths(t *testing.T) {
	if got := CurrentAmiID(context.Background(), &types.Nodegroup{}, nil, nil); got != "" {
		t.Fatalf("CurrentAmiID empty nodegroup = %q, want empty", got)
	}
	if got := CurrentAmiID(context.Background(), &types.Nodegroup{
		LaunchTemplate: &types.LaunchTemplateSpecification{},
	}, nil, nil); got != "" {
		t.Fatalf("CurrentAmiID incomplete launch template = %q, want empty", got)
	}
	if got := CurrentAmiID(context.Background(), &types.Nodegroup{
		Resources: &types.NodegroupResources{AutoScalingGroups: []types.AutoScalingGroup{{}}},
	}, nil, nil); got != "" {
		t.Fatalf("CurrentAmiID incomplete ASG = %q, want empty", got)
	}
}

func TestLatestAmiIDForCustomSkipsSSM(t *testing.T) {
	if got, err := LatestAmiIDForType(context.Background(), nil, "1.30", types.AMITypesCustom); got != "" || err != nil {
		t.Fatalf("LatestAmiIDForType custom = %q, %v; want \"\", nil", got, err)
	}
}

// ssmStubDoer answers every SSM request with a fixed status and JSON body, so
// LatestAmiIDForType runs against a real *ssm.Client and real SDK error
// deserialization.
type ssmStubDoer struct {
	status int
	body   string
}

func (d ssmStubDoer) Do(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: d.status,
		Header:     http.Header{"Content-Type": []string{"application/x-amz-json-1.1"}},
		Body:       io.NopCloser(strings.NewReader(d.body)),
	}, nil
}

func stubSSMClient(status int, body string) *ssm.Client {
	return ssm.New(ssm.Options{
		Region:           "us-east-1",
		Credentials:      aws.AnonymousCredentials{},
		HTTPClient:       ssmStubDoer{status: status, body: body},
		RetryMaxAttempts: 1,
	})
}

func TestLatestAmiIDForType_ReturnsValue(t *testing.T) {
	c := stubSSMClient(200, `{"Parameter":{"Name":"p","Value":"ami-0123"}}`)
	got, err := LatestAmiIDForType(context.Background(), c, "1.31", types.AMITypesAl2023X8664Standard)
	if err != nil || got != "ami-0123" {
		t.Fatalf("LatestAmiIDForType = %q, %v; want ami-0123, nil", got, err)
	}
}

// A denied SSM call must surface as an error (not ""), and FormatAWSError
// must name the missing ssm:GetParameter permission.
func TestLatestAmiIDForType_AccessDeniedIsError(t *testing.T) {
	c := stubSSMClient(400, `{"__type":"AccessDeniedException","message":"User is not authorized to perform: ssm:GetParameter"}`)
	got, err := LatestAmiIDForType(context.Background(), c, "1.31", types.AMITypesAl2023X8664Standard)
	if err == nil || got != "" {
		t.Fatalf("LatestAmiIDForType = %q, %v; want \"\", error", got, err)
	}
	var ae smithy.APIError
	if !errors.As(err, &ae) || ae.ErrorCode() != "AccessDeniedException" {
		t.Fatalf("error %v does not unwrap to the AccessDeniedException API error", err)
	}
	if msg := FormatAWSError(err, "looking up the latest AMI").Error(); !strings.Contains(msg, "ssm:GetParameter") {
		t.Errorf("formatted error does not name ssm:GetParameter:\n%s", msg)
	}
}

func TestLatestAmiIDForType_EmptyValueIsError(t *testing.T) {
	c := stubSSMClient(200, `{"Parameter":{"Name":"p"}}`)
	if got, err := LatestAmiIDForType(context.Background(), c, "1.31", types.AMITypesAl2023X8664Standard); err == nil || got != "" {
		t.Fatalf("LatestAmiIDForType = %q, %v; want \"\", error", got, err)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// buildSSMParameterPath / buildReleaseVersionParameterPath
// ──────────────────────────────────────────────────────────────────────────────

// Every AMI type the SDK knows must map to the SSM parameters AWS documents:
//   - https://docs.aws.amazon.com/eks/latest/userguide/retrieve-ami-id.html
//   - https://docs.aws.amazon.com/eks/latest/userguide/retrieve-ami-id-bottlerocket.html
//   - https://docs.aws.amazon.com/eks/latest/userguide/retrieve-windows-ami-id.html
func TestBuildSSMParameterPath_AllAMITypes(t *testing.T) {
	const (
		al  = "/aws/service/eks/optimized-ami/1.33/"
		br  = "/aws/service/bottlerocket/aws-k8s-1.33"
		win = "/aws/service/ami-windows-latest/Windows_Server-"
	)
	cases := map[types.AMITypes]struct{ image, release string }{
		types.AMITypesAl2X8664:    {al + "amazon-linux-2/recommended/image_id", al + "amazon-linux-2/recommended/release_version"},
		types.AMITypesAl2Arm64:    {al + "amazon-linux-2-arm64/recommended/image_id", al + "amazon-linux-2-arm64/recommended/release_version"},
		types.AMITypesAl2X8664Gpu: {al + "amazon-linux-2-gpu/recommended/image_id", al + "amazon-linux-2-gpu/recommended/release_version"},

		types.AMITypesAl2023X8664Standard: {al + "amazon-linux-2023/x86_64/standard/recommended/image_id", al + "amazon-linux-2023/x86_64/standard/recommended/release_version"},
		types.AMITypesAl2023Arm64Standard: {al + "amazon-linux-2023/arm64/standard/recommended/image_id", al + "amazon-linux-2023/arm64/standard/recommended/release_version"},
		types.AMITypesAl2023X8664Nvidia:   {al + "amazon-linux-2023/x86_64/nvidia/recommended/image_id", al + "amazon-linux-2023/x86_64/nvidia/recommended/release_version"},
		types.AMITypesAl2023Arm64Nvidia:   {al + "amazon-linux-2023/arm64/nvidia/recommended/image_id", al + "amazon-linux-2023/arm64/nvidia/recommended/release_version"},
		types.AMITypesAl2023X8664Neuron:   {al + "amazon-linux-2023/x86_64/neuron/recommended/image_id", al + "amazon-linux-2023/x86_64/neuron/recommended/release_version"},

		types.AMITypesBottlerocketX8664:           {br + "/x86_64/latest/image_id", br + "/x86_64/latest/image_version"},
		types.AMITypesBottlerocketArm64:           {br + "/arm64/latest/image_id", br + "/arm64/latest/image_version"},
		types.AMITypesBottlerocketX8664Nvidia:     {br + "-nvidia/x86_64/latest/image_id", br + "-nvidia/x86_64/latest/image_version"},
		types.AMITypesBottlerocketArm64Nvidia:     {br + "-nvidia/arm64/latest/image_id", br + "-nvidia/arm64/latest/image_version"},
		types.AMITypesBottlerocketX8664Fips:       {br + "-fips/x86_64/latest/image_id", br + "-fips/x86_64/latest/image_version"},
		types.AMITypesBottlerocketArm64Fips:       {br + "-fips/arm64/latest/image_id", br + "-fips/arm64/latest/image_version"},
		types.AMITypesBottlerocketX8664NvidiaFips: {br + "-nvidia-fips/x86_64/latest/image_id", br + "-nvidia-fips/x86_64/latest/image_version"},
		types.AMITypesBottlerocketArm64NvidiaFips: {br + "-nvidia-fips/arm64/latest/image_id", br + "-nvidia-fips/arm64/latest/image_version"},

		// Windows publishes no release-version parameter.
		types.AMITypesWindowsCore2019X8664: {win + "2019-English-Core-EKS_Optimized-1.33/image_id", ""},
		types.AMITypesWindowsFull2019X8664: {win + "2019-English-Full-EKS_Optimized-1.33/image_id", ""},
		types.AMITypesWindowsCore2022X8664: {win + "2022-English-Core-EKS_Optimized-1.33/image_id", ""},
		types.AMITypesWindowsFull2022X8664: {win + "2022-English-Full-EKS_Optimized-1.33/image_id", ""},
		types.AMITypesWindowsCore2025X8664: {win + "2025-English-Core-EKS_Optimized-1.33/image_id", ""},
		types.AMITypesWindowsFull2025X8664: {win + "2025-English-Full-EKS_Optimized-1.33/image_id", ""},

		// A custom AMI has no recommended AMI: no lookup at all.
		types.AMITypesCustom: {"", ""},
	}

	for _, amiType := range types.AMITypes("").Values() {
		want, ok := cases[amiType]
		if !ok {
			t.Errorf("AMI type %s has no expected SSM path in this table", amiType)
			continue
		}
		if got := buildSSMParameterPath("1.33", amiType); got != want.image {
			t.Errorf("image path for %s = %q, want %q", amiType, got, want.image)
		}
		if got := buildReleaseVersionParameterPath("1.33", amiType); got != want.release {
			t.Errorf("release path for %s = %q, want %q", amiType, got, want.release)
		}
		// Only the Amazon Linux families use amazon-eks-ami release notes.
		url, eksAMI := AMIReleaseNotes(amiType)
		if eksAMI != strings.HasPrefix(want.image, al) || (url == "") != (amiType == types.AMITypesCustom) {
			t.Errorf("AMIReleaseNotes(%s) = %q, %v", amiType, url, eksAMI)
		}
	}
}

// AMI types newer than the SDK enum fall back to the base variant of their
// family, in that family's own parameter tree. Anything else is "unknown".
func TestBuildSSMParameterPath_InfersUnknownTypes(t *testing.T) {
	cases := map[string]string{
		"AL2023_ARM64_CUSTOMISH": "/aws/service/eks/optimized-ami/1.30/amazon-linux-2023/arm64/standard/recommended/image_id",
		"AL2023_x86_64_FUTURE":   "/aws/service/eks/optimized-ami/1.30/amazon-linux-2023/x86_64/standard/recommended/image_id",
		"BOTTLEROCKET_ARM_64_X":  "/aws/service/bottlerocket/aws-k8s-1.30/arm64/latest/image_id",
		"BOTTLEROCKET_x86_64_X":  "/aws/service/bottlerocket/aws-k8s-1.30/x86_64/latest/image_id",
		"AL2_ARM_64_X":           "/aws/service/eks/optimized-ami/1.30/amazon-linux-2-arm64/recommended/image_id",
		"WINDOWS_CORE_2030_X":    "",
		"UNKNOWN_TYPE":           "",
	}
	for amiType, want := range cases {
		if got := buildSSMParameterPath("1.30", types.AMITypes(amiType)); got != want {
			t.Errorf("buildSSMParameterPath(%s) = %q, want %q", amiType, got, want)
		}
	}
	if got := buildReleaseVersionParameterPath("1.30", "BOTTLEROCKET_x86_64_X"); got != "/aws/service/bottlerocket/aws-k8s-1.30/x86_64/latest/image_version" {
		t.Errorf("inferred Bottlerocket release path = %q", got)
	}
}
