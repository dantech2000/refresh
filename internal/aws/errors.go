package aws

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/dantech2000/refresh/internal/aws/awserr"
	"github.com/dantech2000/refresh/internal/awsconfig"
	"github.com/dantech2000/refresh/internal/common"
)

// FormatAWSError provides user-friendly error messages for AWS errors. It
// forwards to awserr.FormatAWSError, which holds the implementation so leaf
// packages (such as internal/health) can use it without importing this one.
func FormatAWSError(err error, operation string) error {
	return awserr.FormatAWSError(err, operation)
}

// credentialValidationTimeout bounds the STS GetCallerIdentity call used to
// validate credentials, so a misconfigured environment fails fast.
const credentialValidationTimeout = 10 * time.Second

// ValidateAWSCredentials performs a basic validation of AWS credentials.
func ValidateAWSCredentials(ctx context.Context, awsCfg aws.Config) error {
	// Create a short timeout context for validation
	validationCtx, cancel := context.WithTimeout(ctx, credentialValidationTimeout)
	defer cancel()

	stsClient := sts.NewFromConfig(awsCfg)

	_, err := common.WithRetry(validationCtx, common.DefaultRetryConfig, func(rc context.Context) (*sts.GetCallerIdentityOutput, error) {
		return stsClient.GetCallerIdentity(rc, &sts.GetCallerIdentityInput{})
	})
	if err != nil {
		return FormatAWSError(err, "validating AWS credentials")
	}

	return nil
}

// CheckAWSCredentials validates AWS credentials with STS GetCallerIdentity and
// returns one error for the caller to report. Command setup does not call it
// (see ResolveAWSCredentials); a region sweep calls it through
// runner.NoRegionAnswered when no region answered. It prints nothing, so stdout stays clean for -o json/yaml
// and main prints the error once, on stderr. The message comes from
// FormatAWSError: it carries the credential setup help only when the typed
// classifiers see a credential problem, not on a cancel, a timeout, or a
// network failure (those get their own guidance).
func CheckAWSCredentials(ctx context.Context, awsCfg aws.Config) error {
	if err := ValidateAWSCredentials(ctx, awsCfg); err != nil {
		return fmt.Errorf("AWS credential validation failed: %w", err)
	}
	return nil
}

// NoteKeysShadowProfile adds a note to a credential error when the
// credentials came from AWS_ACCESS_KEY_ID while AWS_PROFILE is set: the SDK
// uses the keys, as the AWS CLI does, and ignores AWS_PROFILE. A profile
// given with --profile or a context wins over the keys, and then nothing is
// added. It reads the cached credentials, so it makes no request.
func NoteKeysShadowProfile(ctx context.Context, cfg aws.Config, err error) error {
	if err == nil || !awserr.IsCredentialError(err) || cfg.Credentials == nil ||
		strings.TrimSpace(os.Getenv("AWS_PROFILE")) == "" || strings.TrimSpace(os.Getenv("AWS_ACCESS_KEY_ID")) == "" {
		return err
	}
	creds, rerr := cfg.Credentials.Retrieve(ctx)
	if rerr != nil || creds.Source != config.CredentialsSourceName {
		return err
	}
	return awserr.WithNote(err, awserr.KeysShadowProfileNote)
}

// ResolveAWSCredentials resolves the configured credentials without an API
// call of its own, and returns one error for the caller to report (formatted
// like CheckAWSCredentials). It catches missing credentials, a failing
// credential source, and an expired SSO session. It cannot tell that keys
// which resolve are revoked or expired server side: the first AWS call reports
// that, and FormatAWSError adds the setup help.
//
// The resolved credentials stay in the config's credential cache, so the
// first real call reuses them. A source that needs the network (SSO role
// credentials, AssumeRole, IMDS) makes here the call that the first AWS call
// would otherwise make.
func ResolveAWSCredentials(ctx context.Context, awsCfg aws.Config) error {
	if awsCfg.Credentials == nil {
		return nil // anonymous: nothing to resolve
	}
	if _, err := awsCfg.Credentials.Retrieve(ctx); err != nil {
		if awserr.IsNoCredentials(err) {
			return fmt.Errorf("AWS credential validation failed: %w", awserr.FormatNoCredentials(err, awsconfig.SSOProfiles()))
		}
		return fmt.Errorf("AWS credential validation failed: %w", FormatAWSError(err, "loading AWS credentials"))
	}
	return nil
}
