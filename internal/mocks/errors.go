package mocks

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/smithy-go"
)

// APIError returns a typed AWS API error with the given code and message, the
// same shape the SDK returns for a service error. Code classification in the
// code under test (retryable, permission, not found) works on it through
// errors.As(err, &smithy.APIError), exactly as it would against real AWS.
//
// The fault is FaultServer for 5xx-style codes and FaultClient otherwise.
func APIError(code, msg string) error {
	fault := smithy.FaultClient
	switch code {
	case "InternalFailure", "InternalServerException", "ServerException", "ServiceUnavailableException":
		fault = smithy.FaultServer
	}
	return &smithy.GenericAPIError{Code: code, Message: msg, Fault: fault}
}

// AccessDenied returns the typed error an IAM principal without the needed
// permission gets back.
func AccessDenied() error {
	return APIError("AccessDeniedException",
		"User: arn:aws:iam::123456789012:user/test is not authorized to perform this action")
}

// Throttling returns a typed throttling error, which the retry layer treats
// as transient.
func Throttling() error {
	return APIError("ThrottlingException", "Rate exceeded")
}

// NotFound returns the EKS-modelled ResourceNotFoundException, the concrete
// type the EKS client returns for an unknown cluster, nodegroup, addon or
// update.
func NotFound() error {
	return notFound("The requested resource was not found.")
}

func notFound(msg string) error {
	return &ekstypes.ResourceNotFoundException{Message: aws.String(msg)}
}

// invalidParameter mirrors the EKS InvalidParameterException, returned for a
// malformed request such as an unknown pagination token.
func invalidParameter(msg string) error {
	return &ekstypes.InvalidParameterException{Message: aws.String(msg)}
}
