package awsxtest

import (
	"net/http"
	"slices"
	"strings"

	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// service is one AWS service: how it speaks and what it answers.
type service struct {
	// name is the IAM prefix, which Request.Service reports.
	name     string
	protocol protocol
	// denied, throttled, and unauthenticated are the codes the service
	// answers for those faults. badToken is its code for a page token it
	// did not issue.
	denied, throttled, unauthenticated, badToken string
	// statuses holds the HTTP status of each error code the service does
	// not answer with 400. Codes in serverStatuses need no entry.
	statuses   map[string]int
	operations map[string]operation
}

type operation struct {
	mutates bool
	handle  func(c *call) (any, error)
}

// serverStatuses are the error codes that mean a failure on the AWS side,
// with the status every service uses for them.
var serverStatuses = map[string]int{
	"InternalFailure":             http.StatusInternalServerError,
	"InternalError":               http.StatusInternalServerError,
	"InternalServerError":         http.StatusInternalServerError,
	"InternalServerException":     http.StatusInternalServerError,
	"InternalServiceError":        http.StatusInternalServerError,
	"InternalServiceFault":        http.StatusInternalServerError,
	"ServerException":             http.StatusInternalServerError,
	"ServiceException":            http.StatusInternalServerError,
	"ServiceUnavailable":          http.StatusServiceUnavailable,
	"ServiceUnavailableException": http.StatusServiceUnavailable,
	"Unavailable":                 http.StatusServiceUnavailable,
	"RequestLimitExceeded":        http.StatusServiceUnavailable,
}

// status returns the HTTP status the service answers with an error code.
func (s *service) status(code string) int {
	if st, ok := s.statuses[code]; ok {
		return st
	}
	if st, ok := serverStatuses[code]; ok {
		return st
	}
	return http.StatusBadRequest
}

func (s *service) writeError(w http.ResponseWriter, e *apiError) {
	status := e.status
	if status == 0 {
		status = s.status(e.code)
	}
	s.protocol.writeError(w, status, e)
}

// faultError words a fault as the service does.
func (s *service) faultError(f *fleettest.FaultError) *apiError {
	switch f.Kind {
	case fleettest.Denied:
		return newError(s.denied, "User: arn:aws:iam::123456789012:user/fake is not authorized to perform this action")
	case fleettest.Throttled:
		return newError(s.throttled, "Rate exceeded")
	case fleettest.Unauthenticated:
		return newError(s.unauthenticated, "The security token included in the request is invalid.")
	default:
		return &apiError{code: f.Code, message: "awsxtest: injected " + f.Code, status: f.Status}
	}
}

// The statuses of the query-protocol services for the codes that are not
// 400. The JSON 1.1 services answer 400 for every client error.
var queryStatuses = map[string]int{
	"AccessDenied":          http.StatusForbidden,
	"InvalidClientTokenId":  http.StatusForbidden,
	"SignatureDoesNotMatch": http.StatusForbidden,
	"ExpiredToken":          http.StatusForbidden,
	"OptInRequired":         http.StatusForbidden,
}

// services is keyed by SigV4 signing name. It is read-only.
var services = map[string]*service{
	"eks": {
		name:     "eks",
		protocol: restJSON{routes: eksRoutes},
		denied:   "AccessDeniedException", throttled: "ThrottlingException",
		unauthenticated: "UnrecognizedClientException", badToken: "InvalidParameterException",
		statuses: map[string]int{
			"AccessDeniedException":       http.StatusForbidden,
			"UnrecognizedClientException": http.StatusForbidden,
			"ThrottlingException":         http.StatusTooManyRequests,
			"ResourceNotFoundException":   http.StatusNotFound,
			"NotFoundException":           http.StatusNotFound,
			"ResourceInUseException":      http.StatusConflict,
		},
		operations: eksOperations,
	},
	"sts": {
		name:     "sts",
		protocol: awsQuery{namespace: "https://sts.amazonaws.com/doc/2011-06-15/"},
		denied:   "AccessDenied", throttled: "Throttling",
		unauthenticated: "InvalidClientTokenId", badToken: "InvalidParameterValue",
		statuses:   queryStatuses,
		operations: stsOperations,
	},
	"ec2": {
		name:     "ec2",
		protocol: ec2Query{},
		denied:   "UnauthorizedOperation", throttled: "RequestLimitExceeded",
		unauthenticated: "AuthFailure", badToken: "InvalidParameterValue",
		statuses: map[string]int{
			"UnauthorizedOperation": http.StatusForbidden,
			"AuthFailure":           http.StatusUnauthorized,
			"OptInRequired":         http.StatusUnauthorized,
		},
		operations: ec2Operations,
	},
	"autoscaling": {
		name:     "autoscaling",
		protocol: awsQuery{namespace: "http://autoscaling.amazonaws.com/doc/2011-01-01/"},
		denied:   "AccessDenied", throttled: "Throttling",
		unauthenticated: "InvalidClientTokenId", badToken: "InvalidNextToken",
		statuses:   queryStatuses,
		operations: autoScalingOperations,
	},
	"ssm": {
		name:     "ssm",
		protocol: awsJSON{targetPrefix: "AmazonSSM."},
		denied:   "AccessDeniedException", throttled: "ThrottlingException",
		unauthenticated: "UnrecognizedClientException", badToken: "ValidationException",
		operations: ssmOperations,
	},
	"monitoring": {
		name:     "cloudwatch",
		protocol: rpcCBOR{namespace: "com.amazonaws.cloudwatch"},
		denied:   "AccessDenied", throttled: "Throttling",
		unauthenticated: "InvalidClientTokenId", badToken: "InvalidNextToken",
		statuses:   queryStatuses,
		operations: cloudWatchOperations,
	},
	"servicequotas": {
		name:     "servicequotas",
		protocol: awsJSON{targetPrefix: "ServiceQuotasV20190624.", typePrefix: "com.amazonaws.servicequotas#"},
		denied:   "AccessDeniedException", throttled: "TooManyRequestsException",
		unauthenticated: "UnrecognizedClientException", badToken: "InvalidPaginationTokenException",
		operations: serviceQuotasOperations,
	},
}

// OperationInfo describes one operation the fake answers.
type OperationInfo struct {
	Operation fleettest.Operation
	// Service is the IAM prefix of the service.
	Service string
	// Protocol is the wire protocol, as Smithy names it.
	Protocol string
	// Mutates reports whether the operation changes AWS state.
	Mutates bool
}

// Operations returns every operation the fake answers, sorted by name.
func Operations() []OperationInfo {
	var out []OperationInfo
	for _, svc := range services {
		for action, op := range svc.operations {
			out = append(out, OperationInfo{
				Operation: fleettest.Operation(svc.name + ":" + action),
				Service:   svc.name,
				Protocol:  svc.protocol.name(),
				Mutates:   op.mutates,
			})
		}
	}
	slices.SortFunc(out, func(a, b OperationInfo) int { return strings.Compare(string(a.Operation), string(b.Operation)) })
	return out
}
