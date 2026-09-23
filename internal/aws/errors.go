package aws

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/dantech2000/refresh/internal/aws/awserr"
	"github.com/dantech2000/refresh/internal/services/common"
	"github.com/fatih/color"
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

// CheckAWSCredentials validates AWS credentials and, on failure, prints a red
// error message followed by credential setup guidance. It returns a sentinel
// error so callers can return immediately without further decoration.
func CheckAWSCredentials(ctx context.Context, awsCfg aws.Config) error {
	if err := ValidateAWSCredentials(ctx, awsCfg); err != nil {
		color.Red("%v", err)
		fmt.Println()
		PrintCredentialHelp()
		return fmt.Errorf("AWS credential validation failed")
	}
	return nil
}

// PrintCredentialHelp displays helpful credential setup information.
func PrintCredentialHelp() {
	color.Yellow("AWS Credential Setup Help:")
	fmt.Println()
	fmt.Println("1. AWS CLI (recommended):")
	fmt.Println("   aws configure")
	fmt.Println()
	fmt.Println("2. Environment variables:")
	fmt.Println("   export AWS_ACCESS_KEY_ID=\"your-access-key\"")
	fmt.Println("   export AWS_SECRET_ACCESS_KEY=\"your-secret-key\"")
	fmt.Println("   export AWS_DEFAULT_REGION=\"us-west-2\"")
	fmt.Println()
	fmt.Println("3. AWS SSO:")
	fmt.Println("   aws sso login --profile your-profile")
	fmt.Println()
	fmt.Println("4. For EC2/EKS/Lambda: Use IAM roles")
	fmt.Println()
	color.Cyan("For more information: https://docs.aws.amazon.com/cli/latest/userguide/cli-configure-quickstart.html")
}
