package aws

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/dantech2000/refresh/internal/aws/awserr"
	"github.com/dantech2000/refresh/internal/services/common"
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

// CheckAWSCredentials validates AWS credentials and returns one error for the
// caller to report. It prints nothing, so stdout stays clean for -o json/yaml
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
