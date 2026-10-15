package awsx_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/aws/smithy-go"

	"github.com/dantech2000/refresh/internal/awsx"
)

func apiError(code string) error {
	return &smithy.GenericAPIError{Code: code, Message: "message"}
}

func TestClassify(t *testing.T) {
	dial := &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	tests := []struct {
		name string
		err  error
		want awsx.ErrorClass
	}{
		{"nil", nil, awsx.ClassUnknown},
		{"interrupt", fmt.Errorf("call: %w", context.Canceled), awsx.ClassInterrupted},
		{"interrupt before the request left", fmt.Errorf("%w: %w", awsx.ErrNotSent, context.Canceled), awsx.ClassNotAttempted},
		{"deadline before the request left", fmt.Errorf("%w: %w", awsx.ErrNotSent, context.DeadlineExceeded), awsx.ClassNotAttempted},
		{"deadline", context.DeadlineExceeded, awsx.ClassTimeout},
		{"dial failure", dial, awsx.ClassNetworkError},
		{"connect timeout", fmt.Errorf("%w: %w", dial, context.DeadlineExceeded), awsx.ClassNetworkError},
		{"expired token", apiError("ExpiredToken"), awsx.ClassCredentialError},
		{"access denied", apiError("AccessDeniedException"), awsx.ClassAccessDenied},
		{"region not enabled", apiError("OptInRequired"), awsx.ClassRegionUnavailable},
		{"throttled", apiError("ThrottlingException"), awsx.ClassThrottled},
		{"not found", apiError("ResourceNotFoundException"), awsx.ClassNotFound},
		{"not found, EC2 style", apiError("InvalidInstanceID.NotFound"), awsx.ClassNotFound},
		{"validation", apiError("InvalidParameterException"), awsx.ClassInvalidRequest},
		{"server", apiError("InternalFailure"), awsx.ClassServiceError},
		{"another API code", apiError("ResourceInUseException"), awsx.ClassUnknown},
		{"DNS for an AWS host", &net.DNSError{Name: "eks.xx-nowhere-1.amazonaws.com", IsNotFound: true}, awsx.ClassRegionUnavailable},
		{"reset", &net.OpError{Op: "read", Err: errors.New("connection reset")}, awsx.ClassNetworkError},
		{"anything else", errors.New("boom"), awsx.ClassUnknown},
		{"wrapped", fmt.Errorf("listing: %w", apiError("ThrottlingException")), awsx.ClassThrottled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := awsx.Classify(tt.err); got != tt.want {
				t.Errorf("Classify(%v) = %s, want %s", tt.err, got, tt.want)
			}
		})
	}
}

func TestOperation(t *testing.T) {
	op := func(service, name string) error {
		return &smithy.OperationError{ServiceID: service, OperationName: name, Err: apiError("ThrottlingException")}
	}
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"EKS", op("EKS", "DescribeCluster"), "eks:DescribeCluster"},
		{"Auto Scaling", op("Auto Scaling", "DescribeAutoScalingGroups"), "autoscaling:DescribeAutoScalingGroups"},
		{"wrapped", fmt.Errorf("reading: %w", op("SSM", "GetParameter")), "ssm:GetParameter"},
		{"a service that refresh does not use", op("S3", "GetObject"), ""},
		{"not a call", errors.New("boom"), ""},
		{"nil", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := awsx.Operation(tt.err); got != tt.want {
				t.Errorf("Operation = %q, want %q", got, tt.want)
			}
		})
	}
}
