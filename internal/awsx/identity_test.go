package awsx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials/ssocreds"
	"github.com/aws/aws-sdk-go-v2/service/eks"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// This file is not named credentials_test.go: .gitignore has *credentials*.

// The answers of a fake STS.
const (
	stsThrottled = `<ErrorResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">` +
		`<Error><Type>Sender</Type><Code>Throttling</Code><Message>Rate exceeded</Message></Error>` +
		`<RequestId>1</RequestId></ErrorResponse>`
	stsDenied = `<ErrorResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">` +
		`<Error><Type>Sender</Type><Code>AccessDenied</Code><Message>no</Message></Error>` +
		`<RequestId>1</RequestId></ErrorResponse>`
	// roleConfig is a role profile that gets its credentials from STS.
	roleConfig = "[profile base]\nregion = us-east-1\naws_access_key_id = AKIDPROFILEFAKE\naws_secret_access_key = fake-secret\n" +
		"[profile role]\nregion = us-east-1\nrole_arn = arn:aws:iam::111122223333:role/R\nsource_profile = base\n"
)

// TestCredentialChainFollowsTheStandardPolicy counts the requests of the
// AssumeRole call that the credential chain makes for a role profile. The
// chain builds its own STS client from the loaded configuration, so it must
// get the Standard retryer there: 5 attempts, whatever AWS_MAX_ATTEMPTS
// says. With the SDK default it would be 3, or the number in the variable.
func TestCredentialChainFollowsTheStandardPolicy(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		status      int
		maxAttempts string // AWS_MAX_ATTEMPTS
		want        int32
		wantErr     func(error) bool
	}{
		{name: "throttled forever", body: stsThrottled, status: 400, want: 5, wantErr: IsThrottling},
		{name: "throttled forever, AWS_MAX_ATTEMPTS=2", body: stsThrottled, status: 400, maxAttempts: "2", want: 5, wantErr: IsThrottling},
		{name: "throttled forever, AWS_MAX_ATTEMPTS=9", body: stsThrottled, status: 400, maxAttempts: "9", want: 5, wantErr: IsThrottling},
		{name: "access denied", body: stsDenied, status: 403, want: 1, wantErr: IsPermissionError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateAWS(t)
			var requests atomic.Int32
			sts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "text/xml")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer sts.Close()
			if err := os.WriteFile(os.Getenv("AWS_CONFIG_FILE"), []byte(roleConfig), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("AWS_ENDPOINT_URL_STS", sts.URL)
			t.Setenv("AWS_MAX_ATTEMPTS", tt.maxAttempts)

			_, err := load(context.Background(), Options{Profile: "role"}, NewHTTPClient(8), zeroBackoff{})

			if err == nil || !tt.wantErr(err) {
				t.Errorf("load() error = %v, which is not the kind this answer gives", err)
			}
			if got := requests.Load(); got != tt.want {
				t.Errorf("AssumeRole requests = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestSSOExpiryMidRunNamesTheProfile: the SSO login expires after Load, so
// an API call fails when it refreshes the credentials. The formatted error
// names the profile to log in with, which is the SSO profile at the end of
// the source_profile chain, not the role profile of the run.
func TestSSOExpiryMidRunNamesTheProfile(t *testing.T) {
	isolateAWS(t)
	config := "[profile sso-base]\nsso_session = acme\nsso_account_id = 111122223333\nsso_role_name = R\nregion = us-east-1\n" +
		"[sso-session acme]\nsso_start_url = https://acme.awsapps.com/start\nsso_region = us-east-1\n" +
		"[profile app]\nregion = us-east-1\nrole_arn = arn:aws:iam::111122223333:role/App\nsource_profile = sso-base\n"
	if err := os.WriteFile(os.Getenv("AWS_CONFIG_FILE"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	// The keys of the environment let Load succeed; AWS_PROFILE names the
	// profile of the run.
	t.Setenv("AWS_PROFILE", "app")
	t.Setenv("AWS_ENDPOINT_URL", "http://127.0.0.1:1") // no request is sent
	cfg, err := Load(context.Background(), Options{})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	// From here on the login is gone.
	expired := &ssocreds.InvalidTokenError{Err: errors.New("the token expired")}
	cfg.Credentials = aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{}, expired
	})
	provider := NewProvider(cfg, func(o *ProviderOptions) { o.Backoff = zeroBackoff{} })

	for _, clients := range []*Clients{provider.Clients(""), provider.SweepClients("").Clients, provider.PollClients("").Clients} {
		_, err = clients.EKS.ListClusters(context.Background(), &eks.ListClustersInput{})

		if !errors.Is(err, expired) {
			t.Fatalf("%v: ListClusters error = %v, want the expired login", clients.Policy, err)
		}
		msg := FormatAWSError(err, "listing clusters").Error()
		if !strings.Contains(msg, "not logged in to IAM Identity Center") || !strings.Contains(msg, "aws sso login --profile sso-base") {
			t.Errorf("%v: the message does not name the profile to log in with:\n%s", clients.Policy, msg)
		}
	}
}

// An SSO failure that did not come through a client of Load has no profile:
// the message keeps its placeholder.
func TestSSOErrorWithoutAProfile(t *testing.T) {
	msg := FormatAWSError(&ssocreds.InvalidTokenError{Err: errors.New("expired")}, "x").Error()
	if !strings.Contains(msg, "aws sso login --profile <name>") {
		t.Errorf("message = %q, want the placeholder profile", msg)
	}
}

// expiringSTS is a fake STS whose first AssumeRole answers with credentials
// that have already expired, so the next API call refreshes them. Every
// later request gets the throttling error. It counts the requests.
func expiringSTS(t *testing.T) (url string, requests *atomic.Int32) {
	t.Helper()
	const expired = `<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleResult>` +
		`<Credentials><AccessKeyId>AKIDROLEFAKE</AccessKeyId><SecretAccessKey>fake-secret</SecretAccessKey>` +
		`<SessionToken>token</SessionToken><Expiration>2020-01-01T00:00:00Z</Expiration></Credentials>` +
		`<AssumedRoleUser><Arn>arn:aws:sts::111122223333:assumed-role/R/s</Arn><AssumedRoleId>AROAFAKE:s</AssumedRoleId></AssumedRoleUser>` +
		`</AssumeRoleResult><ResponseMetadata><RequestId>1</RequestId></ResponseMetadata></AssumeRoleResponse>`
	requests = &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		if requests.Add(1) == 1 {
			_, _ = w.Write([]byte(expired))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(stsThrottled))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, requests
}

// TestCredentialRefreshIsNotRetriedByTheOperation: the credentials expire
// during a run and STS throttles the refresh. The credential client makes
// its 5 attempts, and that is all: the service operation that needed the
// credentials does not start the refresh again for each attempt of its own,
// which would be 25 requests.
func TestCredentialRefreshIsNotRetriedByTheOperation(t *testing.T) {
	for _, policy := range []Policy{Standard, Sweep, Poll} {
		t.Run(policy.String(), func(t *testing.T) {
			isolateAWS(t)
			world := fleettest.New()
			srv := awsxtest.NewServer(world)
			defer srv.Close()
			stsURL, stsRequests := expiringSTS(t)
			if err := os.WriteFile(os.Getenv("AWS_CONFIG_FILE"), []byte(roleConfig), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("AWS_ENDPOINT_URL", srv.URL)
			t.Setenv("AWS_ENDPOINT_URL_STS", stsURL)
			cfg, err := load(context.Background(), Options{Profile: "role"}, NewHTTPClient(8), zeroBackoff{})
			if err != nil {
				t.Fatalf("load() error = %v", err)
			}
			if got := stsRequests.Load(); got != 1 {
				t.Fatalf("AssumeRole requests after load = %d, want 1", got)
			}
			provider := NewProvider(cfg, func(o *ProviderOptions) {
				o.Backoff = zeroBackoff{}
				o.MaxInFlight = 2
			})

			_, err = provider.clientsFor("", policy).EKS.ListClusters(context.Background(), &eks.ListClustersInput{})

			if !IsThrottling(err) {
				t.Fatalf("ListClusters error = %v, want the throttling of the refresh", err)
			}
			if got := stsRequests.Load() - 1; got != 5 {
				t.Errorf("AssumeRole requests for the refresh = %d, want 5: the attempts of the credential client alone", got)
			}
			for _, r := range srv.Requests() {
				t.Errorf("%s reached the service without credentials", r.Operation)
			}
			// A wait loop goes on through this failure: it is transient.
			if IsPermanentAPIError(err) || !IsRetryable(err) {
				t.Errorf("the failed refresh is permanent = %t, retryable = %t; want a transient failure", IsPermanentAPIError(err), IsRetryable(err))
			}
			if !isIdentityFailure(err) {
				t.Error("the error is not marked as a failure to get credentials")
			}
			// The call gave its slot back.
			if held := len(provider.limiters[cfg.Region].slots); held != 0 {
				t.Errorf("slots held after the call = %d, want 0", held)
			}
		})
	}
}
