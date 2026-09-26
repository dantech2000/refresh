package runner

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/ssocreds"
	"github.com/aws/smithy-go"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
	"github.com/dantech2000/refresh/internal/ui"
)

func TestReportSkippedRegions(t *testing.T) {
	var buf bytes.Buffer
	ReportSkippedRegions(&buf, nil)
	if buf.Len() != 0 {
		t.Errorf("no skipped regions wrote %q", buf.String())
	}
	ReportSkippedRegions(&buf, []string{"ap-east-1", "me-south-1"})
	want := "Skipped 2 region(s) not accessible to these credentials: ap-east-1, me-south-1 (scope with -r or REFRESH_EKS_REGIONS)\n"
	if got := ui.StripANSI(buf.String()); got != want {
		t.Errorf("wrote %q, want %q", got, want)
	}
}

func TestTableListsFailures(t *testing.T) {
	for format, want := range map[string]bool{
		"": true, "table": true, "Table": true, "tree": true,
		"json": false, "yaml": false, "plain": false,
	} {
		if got := TableListsFailures(format); got != want {
			t.Errorf("TableListsFailures(%q) = %v, want %v", format, got, want)
		}
	}
}

// A credential error with no API code (a SigV4 signing failure, a credential
// source failure) comes back as the original error: errors.As still finds its
// type, and the setup help appears once. The sweeps hand over the raw error
// (fleet discovery) or one FormatAWSError already formatted (status, cluster
// list); both must work.
func TestNoRegionAnswered_KeepsOriginalCredentialError(t *testing.T) {
	const help = "AWS credentials not configured or invalid"
	signing := func() error {
		return fmt.Errorf("operation error EKS: ListClusters, %w", &v4.SigningError{Err: errors.New("failed to sign request: bad key")})
	}
	source := func() error {
		return fmt.Errorf("operation error EKS: ListClusters, get identity: get credentials: failed to refresh cached credentials, %w",
			&ssocreds.InvalidTokenError{Err: errors.New("the SSO session has expired")})
	}
	for name, tc := range map[string]struct {
		err    func() error
		target func(error) bool
	}{
		"signing error": {signing, func(err error) bool { var e *v4.SigningError; return errors.As(err, &e) }},
		"credential source error": {source, func(err error) bool {
			var e *ssocreds.InvalidTokenError
			return errors.As(err, &e)
		}},
	} {
		for form, wrap := range map[string]func(error) error{
			"raw":       func(err error) error { return err },
			"formatted": func(err error) error { return awsinternal.FormatAWSError(err, "listing clusters in us-west-2") },
		} {
			t.Run(name+"/"+form, func(t *testing.T) {
				fakeaws.New(t)
				cfg, err := config.LoadDefaultConfig(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				throttled := &smithy.GenericAPIError{Code: "ThrottlingException", Message: "Rate exceeded"}
				for layout, regionErrs := range map[string][]error{
					"every region":          {wrap(tc.err()), wrap(tc.err())},
					"after another failure": {throttled, wrap(tc.err())},
				} {
					got := NoRegionAnswered(t.Context(), cfg, nil, regionErrs)
					if got == nil {
						t.Fatalf("%s: NoRegionAnswered = nil, want the credential error", layout)
					}
					if !tc.target(got) {
						t.Errorf("%s: errors.As does not find the original error type in %v", layout, got)
					}
					if n := strings.Count(got.Error(), help); n != 1 {
						t.Errorf("%s: setup help appears %d times, want 1:\n%v", layout, n, got)
					}
				}
			})
		}
	}
}

// NoRegionAnswered asks STS only when a region looked closed, and returns
// the credential error only when STS rejects the credentials.
func TestNoRegionAnswered(t *testing.T) {
	// Typed API errors, as a region's ListClusters returns them.
	apiErr := func(code string) error {
		return &smithy.GenericAPIError{Code: code, Message: "fake " + code}
	}
	unavailable := []error{apiErr("UnrecognizedClientException")}
	throttled := []error{apiErr("ThrottlingException")}
	for _, tc := range []struct {
		name     string
		badCreds bool
		skipped  []string
		failed   []error
		wantErr  bool
		closed   bool // want a RegionsClosedError, not the credential help
		stsCalls int
	}{
		{name: "expired token needs no STS call", failed: []error{apiErr("ExpiredTokenException")}, wantErr: true},
		{name: "expired token after another failure", failed: []error{
			apiErr("ThrottlingException"), apiErr("ExpiredTokenException"),
		}, wantErr: true},
		{name: "region-closed code, valid credentials", failed: unavailable, wantErr: true, closed: true, stsCalls: 1},
		{name: "skipped, bad credentials", badCreds: true, skipped: []string{"us-east-1"}, wantErr: true, stsCalls: 1},
		{name: "unavailable, bad credentials", badCreds: true, failed: unavailable, wantErr: true, stsCalls: 1},
		{name: "skipped, valid credentials", skipped: []string{"us-east-1"}, stsCalls: 1},
		{name: "throttled only", badCreds: true, failed: throttled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := fakeaws.New(t)
			if tc.badCreds {
				srv.FailCredentials("InvalidClientTokenId")
			}
			cfg, err := config.LoadDefaultConfig(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			err = NoRegionAnswered(t.Context(), cfg, tc.skipped, tc.failed)
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, want error %v", err, tc.wantErr)
			}
			var ce *RegionsClosedError
			if isClosed := errors.As(err, &ce); isClosed != tc.closed {
				t.Errorf("err = %v, want RegionsClosedError %v", err, tc.closed)
			}
			if tc.closed && (strings.Contains(err.Error(), "not configured") || !strings.Contains(err.Error(), "account has not enabled it")) {
				t.Errorf("a closed region reads as bad credentials:\n%v", err)
			}
			if err != nil && !tc.closed && !strings.Contains(err.Error(), "AWS credentials not configured or invalid") {
				t.Errorf("err lacks the credential help:\n%v", err)
			}
			if n := len(srv.Calls()); n != tc.stsCalls {
				t.Errorf("AWS calls = %d, want %d: %v", n, tc.stsCalls, srv.Calls())
			}
		})
	}
}
