package diag_test

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"syscall"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/dantech2000/refresh/internal/aws/awserr"
	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/mocks"
)

func TestFromError(t *testing.T) {
	err := fmt.Errorf("describing nodegroup web: %w",
		awserr.FormatAWSError(mocks.AccessDenied(), "describing nodegroup web"))
	f := diag.FromError(diag.KindNodegroup, "web", diag.OpDescribeNodegroup, err)
	want := diag.Failure{
		Kind:         diag.KindNodegroup,
		Name:         "web",
		Operation:    "eks:DescribeNodegroup",
		Reason:       diag.ReasonAccessDenied,
		Retryable:    false,
		Error:        "AccessDeniedException: User: arn:aws:iam::123456789012:user/test is not authorized to perform this action",
		AWSErrorCode: "AccessDeniedException",
	}
	if f != want {
		t.Errorf("FromError =\n  %+v\nwant\n  %+v", f, want)
	}
}

func TestFromError_ErrorIsOneLine(t *testing.T) {
	for name, err := range map[string]error{
		"permission help":  awserr.FormatAWSError(mocks.AccessDenied(), "listing clusters"),
		"network help":     awserr.FormatAWSError(dialErr(syscall.ECONNREFUSED), "listing clusters"),
		"multi-line API":   mocks.APIError("ValidationException", "line one\nline two"),
		"multi-line plain": fmt.Errorf("first\nsecond"),
		"nil":              nil,
	} {
		f := diag.FromError(diag.KindCluster, "prod", diag.OpDescribeCluster, err)
		if f.Error == "" || strings.ContainsAny(f.Error, "\r\n") {
			t.Errorf("%s: Error = %q, want one non-empty line", name, f.Error)
		}
	}
}

func TestFromError_RegionNotEnabled(t *testing.T) {
	// A region that is not enabled for the account answers with a
	// credential-class code; for a region failure that is RegionUnavailable.
	for _, code := range []string{"UnrecognizedClientException", "InvalidClientTokenId", "AuthFailure"} {
		f := diag.FromError(diag.KindRegion, "ap-east-1", diag.OpListClusters, mocks.APIError(code, "x"))
		if f.Reason != diag.ReasonRegionUnavailable || f.Retryable || f.AWSErrorCode != code || f.Region != "ap-east-1" {
			t.Errorf("%s: got %s retryable=%v code=%q region=%q, want RegionUnavailable in ap-east-1", code, f.Reason, f.Retryable, f.AWSErrorCode, f.Region)
		}
	}
	// Other kinds keep CredentialError; AccessDenied stays AccessDenied.
	if f := diag.FromError(diag.KindCluster, "prod", diag.OpDescribeCluster, mocks.APIError("UnrecognizedClientException", "x")); f.Reason != diag.ReasonCredentialError {
		t.Errorf("cluster: reason = %s, want CredentialError", f.Reason)
	}
	if f := diag.FromError(diag.KindRegion, "us-east-1", diag.OpListClusters, mocks.AccessDenied()); f.Reason != diag.ReasonAccessDenied {
		t.Errorf("region AccessDenied: reason = %s, want AccessDenied", f.Reason)
	}
	// ExpiredToken is a credential problem in every region.
	if f := diag.FromError(diag.KindRegion, "us-east-1", diag.OpListClusters, mocks.APIError("ExpiredToken", "x")); f.Reason != diag.ReasonCredentialError {
		t.Errorf("region ExpiredToken: reason = %s, want CredentialError", f.Reason)
	}
}

func TestNew(t *testing.T) {
	f := diag.New(diag.KindUpdate, "web", diag.ReasonNotMonitored, "polling stopped:\n  throttled")
	if f.Kind != diag.KindUpdate || f.Name != "web" || f.Reason != diag.ReasonNotMonitored || !f.Retryable {
		t.Errorf("New = %+v", f)
	}
	if f.Error != "polling stopped: throttled" {
		t.Errorf("Error = %q, want one line", f.Error)
	}
	if f := diag.New(diag.KindUpdate, "web", diag.ReasonUpdateFailed, "AMI not found"); f.Retryable {
		t.Error("UpdateFailed must not be retryable")
	}
}

func TestKindNoun(t *testing.T) {
	cases := map[diag.Kind]string{
		diag.KindRegion:              "region",
		diag.KindCluster:             "cluster",
		diag.KindNodegroup:           "nodegroup",
		diag.KindAddon:               "addon",
		diag.KindInsight:             "insight",
		diag.KindUpdate:              "update",
		diag.KindPodDisruptionBudget: "pdb",
		diag.KindNode:                "node",
		diag.Kind("FutureKind"):      "futurekind",
	}
	for k, want := range cases {
		if got := k.Noun(); got != want {
			t.Errorf("%s.Noun() = %q, want %q", k, got, want)
		}
	}
}

type document struct {
	Failures diag.List `json:"failures" yaml:"failures"`
}

func TestEncoding(t *testing.T) {
	full := diag.Failure{
		Kind: diag.KindUpdate, Name: "web", Cluster: "prod", Region: "us-east-1",
		Operation: diag.OpDescribeUpdate, Reason: diag.ReasonThrottled, Retryable: true,
		Error: "ThrottlingException: Rate exceeded", AWSErrorCode: "ThrottlingException", UpdateID: "u-1",
	}
	minimal := diag.Failure{Kind: diag.KindCluster, Name: "prod", Reason: diag.ReasonNotFound, Error: "gone"}

	t.Run("json keys", func(t *testing.T) {
		b, err := json.Marshal(full)
		if err != nil {
			t.Fatal(err)
		}
		want := `{"kind":"Update","name":"web","cluster":"prod","region":"us-east-1","operation":"eks:DescribeUpdate","reason":"Throttled","retryable":true,"error":"ThrottlingException: Rate exceeded","awsErrorCode":"ThrottlingException","updateId":"u-1"}`
		if string(b) != want {
			t.Errorf("json =\n  %s\nwant\n  %s", b, want)
		}
	})
	t.Run("json omits empty optional fields, keeps retryable", func(t *testing.T) {
		b, err := json.Marshal(minimal)
		if err != nil {
			t.Fatal(err)
		}
		want := `{"kind":"Cluster","name":"prod","reason":"NotFound","retryable":false,"error":"gone"}`
		if string(b) != want {
			t.Errorf("json =\n  %s\nwant\n  %s", b, want)
		}
	})
	t.Run("yaml matches json keys", func(t *testing.T) {
		b, err := yaml.Marshal(minimal)
		if err != nil {
			t.Fatal(err)
		}
		want := "kind: Cluster\nname: prod\nreason: NotFound\nretryable: false\nerror: gone\n"
		if string(b) != want {
			t.Errorf("yaml =\n%s\nwant\n%s", b, want)
		}
		b, err = yaml.Marshal(full)
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"awsErrorCode: ThrottlingException", "updateId: u-1", "operation: eks:DescribeUpdate", "retryable: true"} {
			if !strings.Contains(string(b), key) {
				t.Errorf("yaml has no %q:\n%s", key, b)
			}
		}
	})
	t.Run("nil list encodes as []", func(t *testing.T) {
		b, err := json.Marshal(document{})
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != `{"failures":[]}` {
			t.Errorf("json = %s, want {\"failures\":[]}", b)
		}
		y, err := yaml.Marshal(document{})
		if err != nil {
			t.Fatal(err)
		}
		if string(y) != "failures: []\n" {
			t.Errorf("yaml = %q, want \"failures: []\\n\"", y)
		}
	})
	t.Run("list round trip", func(t *testing.T) {
		b, err := json.Marshal(document{Failures: diag.List{full, minimal}})
		if err != nil {
			t.Fatal(err)
		}
		var back document
		if err := json.Unmarshal(b, &back); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(back.Failures, diag.List{full, minimal}) {
			t.Errorf("round trip = %+v", back.Failures)
		}
	})
}

func TestSort(t *testing.T) {
	want := []diag.Failure{
		{Kind: diag.KindCluster, Region: "eu-west-1", Name: "a"},
		{Kind: diag.KindCluster, Region: "us-east-1", Name: "a"},
		{Kind: diag.KindNodegroup, Region: "us-east-1", Cluster: "prod", Name: "api"},
		{Kind: diag.KindNodegroup, Region: "us-east-1", Cluster: "prod", Name: "web", Operation: diag.OpDescribeNodegroup},
		{Kind: diag.KindNodegroup, Region: "us-east-1", Cluster: "prod", Name: "web", Operation: diag.OpUpdateNodegroupVersion},
		{Kind: diag.KindNodegroup, Region: "us-east-1", Cluster: "stage", Name: "api"},
		{Kind: diag.KindNodegroup, Region: "us-west-2", Cluster: "prod", Name: "api"},
		{Kind: diag.KindRegion, Region: "ap-east-1", Name: "ap-east-1", Error: "a"},
		{Kind: diag.KindRegion, Region: "ap-east-1", Name: "ap-east-1", Error: "b"},
	}
	for i := range 50 {
		got := slices.Clone(want)
		r := rand.New(rand.NewPCG(uint64(i), 1)) //nolint:gosec // G404: shuffling test input
		r.Shuffle(len(got), func(a, b int) { got[a], got[b] = got[b], got[a] })
		diag.Sort(got)
		if !slices.Equal(got, want) {
			t.Fatalf("shuffle %d: Sort =\n%+v\nwant\n%+v", i, got, want)
		}
	}
	diag.Sort(nil) // must not panic
}
