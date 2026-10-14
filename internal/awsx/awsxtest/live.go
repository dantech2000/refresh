//go:build livecheck

package awsxtest

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/ec2rolecreds"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
)

// liveCredentialLimit bounds the search for credentials of a live check.
// A profile that assumes a role or reads SSO makes a call or two; nothing
// that works takes this long.
const liveCredentialLimit = 20 * time.Second

// LiveConfig returns the configuration of a live check: the default
// credential chain (AWS_PROFILE, the environment, ~/.aws, then the instance
// role) in region. It resolves the credentials before it returns, and with
// none it fails the test at once with one message, so that no check spends
// minutes on a retry of each call.
//
// The instance role is the last link of the chain, and away from EC2 it
// is the slow one: with the HTTP client of the configuration the metadata
// service gets no timeout of its own. Here it has the client of the imds
// package (a dial timeout of 250 ms, 500 ms for the headers) and two
// attempts. The whole search ends after liveCredentialLimit.
//
// The clients share one transport, whose idle connections close when the
// test ends: a kept-alive connection is not a leak for goleak.
//
// It makes no AWS API call. A profile that assumes a role calls STS
// AssumeRole, and an SSO profile calls the SSO portal, as any command does.
// An empty region leaves the region of the profile.
func LiveConfig(t testing.TB, region string) aws.Config {
	t.Helper()
	tr := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(tr.CloseIdleConnections)
	ctx, cancel := context.WithTimeout(t.Context(), liveCredentialLimit)
	defer cancel()
	opts := []func(*config.LoadOptions) error{
		config.WithHTTPClient(&http.Client{Transport: tr}),
		config.WithEC2RoleCredentialOptions(func(o *ec2rolecreds.Options) {
			o.Client = imds.New(imds.Options{Retryer: retry.AddWithMaxAttempts(retry.NewStandard(), 2)})
		}),
	}
	if region != "" {
		opts = append(opts, config.WithRegion(region))
	}
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		t.Fatalf("loading the AWS configuration: %v", err)
	}
	if _, err := cfg.Credentials.Retrieve(ctx); err != nil {
		t.Fatalf("could not resolve AWS credentials for the live checks: %v\n"+
			"Set AWS_PROFILE to a profile with ReadOnlyAccess (run aws sso login for an SSO profile), then run the checks again.", err)
	}
	return cfg
}
