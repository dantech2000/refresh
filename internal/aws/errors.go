package aws

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
	"github.com/dantech2000/refresh/internal/services/common"
	"github.com/fatih/color"
)

// Error classification patterns for better user experience.
var (
	credentialErrorPatterns = []string{
		"no ec2 imds role found",
		"failed to refresh cached credentials",
		"unable to locate credentials",
		"no credential providers",
		"credentials not found",
		"invalid credentials",
		"access denied",
		"unauthorized",
		"sigv4",
		"request signature",
		"the security token included in the request is invalid",
		"the security token included in the request is expired",
		"get identity",
		"get credentials",
	}

	networkErrorPatterns = []string{
		"no such host",
		"connection refused",
		"connection timeout",
		"network is unreachable",
		"context deadline exceeded",
		"request canceled",
		"dial",
		"timeout",
	}

	permissionErrorPatterns = []string{
		"accessdenied",
		"forbidden",
		"unauthorized",
	}
)

// AWSError wraps an error with AWS-specific context.
type AWSError struct {
	Operation string
	Err       error
	Category  ErrorCategory
}

// ErrorCategory classifies the type of AWS error.
type ErrorCategory int

const (
	ErrorCategoryUnknown ErrorCategory = iota
	ErrorCategoryCredential
	ErrorCategoryNetwork
	ErrorCategoryPermission
	ErrorCategoryRegion
	ErrorCategoryAPI
)

func (e *AWSError) Error() string {
	return fmt.Sprintf("error while %s: %v", e.Operation, e.Err)
}

func (e *AWSError) Unwrap() error {
	return e.Err
}

// IsCredentialError checks if the error is related to AWS credentials.
func IsCredentialError(err error) bool {
	if err == nil {
		return false
	}
	return matchesPatterns(err.Error(), credentialErrorPatterns)
}

// IsNetworkError checks if the error is network-related.
func IsNetworkError(err error) bool {
	if err == nil {
		return false
	}
	return matchesPatterns(err.Error(), networkErrorPatterns)
}

// IsPermissionError checks if the error is permissions-related.
func IsPermissionError(err error) bool {
	if err == nil {
		return false
	}
	return matchesPatterns(err.Error(), permissionErrorPatterns)
}

// IsRegionError checks if the error is related to AWS region configuration.
func IsRegionError(err error) bool {
	if err == nil {
		return false
	}
	errText := strings.ToLower(err.Error())
	return strings.Contains(errText, "region") ||
		(strings.Contains(errText, "no such host") && strings.Contains(errText, ".amazonaws.com"))
}

// matchesPatterns checks if the error text matches any of the given patterns.
func matchesPatterns(errText string, patterns []string) bool {
	errTextLower := strings.ToLower(errText)
	for _, pattern := range patterns {
		if strings.Contains(errTextLower, pattern) {
			return true
		}
	}
	return false
}

// FormatAWSError provides user-friendly error messages for AWS errors.
func FormatAWSError(err error, operation string) error {
	if err == nil {
		return nil
	}

	// Check for region-related errors first
	if IsRegionError(err) {
		return formatRegionError(err, operation)
	}

	// Check for credential errors
	if IsCredentialError(err) {
		return formatCredentialError(err)
	}

	// Check for network errors
	if IsNetworkError(err) {
		return formatNetworkError(err, operation)
	}

	// Check for permission errors
	if IsPermissionError(err) {
		return formatPermissionError(err, operation)
	}

	// For other AWS API errors, try to extract meaningful information
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return fmt.Errorf("AWS API error while %s: %s (%s)", operation, ae.ErrorMessage(), ae.ErrorCode())
	}

	// Default case - return the error with context
	return fmt.Errorf("error while %s: %v", operation, err)
}

func formatRegionError(err error, operation string) error {
	return fmt.Errorf(`AWS region configuration issue while %s.

Please verify:
- AWS_DEFAULT_REGION environment variable is set to a valid region
- Region in ~/.aws/config matches an AWS region
- Common regions: us-east-1, us-west-2, eu-west-1, ap-southeast-1

Current error indicates an invalid or unsupported region.

Current error: %v`, operation, err)
}

func formatCredentialError(err error) error {
	return fmt.Errorf(`AWS credentials not configured or invalid.

Please set up your AWS credentials using one of these methods:

1. AWS CLI configuration:
   aws configure

2. Environment variables:
   export AWS_ACCESS_KEY_ID="your-access-key"
   export AWS_SECRET_ACCESS_KEY="your-secret-key"
   export AWS_DEFAULT_REGION="us-west-2"

3. IAM role (if running on EC2/EKS/Lambda)

4. AWS SSO:
   aws sso login

Current error: %s`, err)
}

func formatNetworkError(err error, operation string) error {
	return fmt.Errorf(`network connectivity issue while %s.

Please check:
- Internet connection
- AWS service endpoints are accessible
- VPC/Security group settings (if running in private network)
- Regional service availability

Current error: %v`, operation, err)
}

func formatPermissionError(err error, operation string) error {
	return fmt.Errorf(`insufficient AWS permissions while %s.

Required permissions for refresh tool:
- eks:ListClusters
- eks:DescribeCluster
- eks:ListNodegroups
- eks:DescribeNodegroup
- eks:UpdateNodegroupVersion
- cloudwatch:GetMetricStatistics (for health checks)

Current error: %v`, operation, err)
}

// ValidateAWSCredentials performs a basic validation of AWS credentials.
func ValidateAWSCredentials(ctx context.Context, awsCfg aws.Config) error {
	// Create a short timeout context for validation
	validationCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
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
