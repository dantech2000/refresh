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
	// denied and throttled are how the service words those two errors.
	denied, throttled apiError
	operations        map[string]operation
}

type operation struct {
	mutates bool
	handle  func(c *call) (any, error)
}

func (s *service) faultError(f *fleettest.FaultError) *apiError {
	switch f.Kind {
	case fleettest.Denied:
		e := s.denied
		return &e
	case fleettest.Throttled:
		e := s.throttled
		return &e
	default:
		status := http.StatusBadRequest
		if strings.HasPrefix(f.Code, "AccessDenied") || f.Code == "UnauthorizedOperation" {
			status = http.StatusForbidden
		}
		return &apiError{status: status, code: f.Code, message: "awsxtest: injected " + f.Code}
	}
}

// services is keyed by SigV4 signing name. It is read-only.
var services = map[string]*service{
	"eks": {
		name:       "eks",
		protocol:   restJSON{routes: eksRoutes},
		denied:     apiError{http.StatusForbidden, "AccessDeniedException", "User: arn:aws:iam::123456789012:user/fake is not authorized to perform this action"},
		throttled:  apiError{http.StatusTooManyRequests, "ThrottlingException", "Rate exceeded"},
		operations: eksOperations,
	},
	"sts": {
		name:       "sts",
		protocol:   awsQuery{namespace: "https://sts.amazonaws.com/doc/2011-06-15/"},
		denied:     apiError{http.StatusForbidden, "AccessDenied", "User is not authorized to perform this action"},
		throttled:  apiError{http.StatusBadRequest, "Throttling", "Rate exceeded"},
		operations: stsOperations,
	},
	"ec2": {
		name:       "ec2",
		protocol:   ec2Query{},
		denied:     apiError{http.StatusForbidden, "UnauthorizedOperation", "You are not authorized to perform this operation."},
		throttled:  apiError{http.StatusServiceUnavailable, "RequestLimitExceeded", "Request limit exceeded."},
		operations: ec2Operations,
	},
	"autoscaling": {
		name:       "autoscaling",
		protocol:   awsQuery{namespace: "http://autoscaling.amazonaws.com/doc/2011-01-01/"},
		denied:     apiError{http.StatusForbidden, "AccessDenied", "User is not authorized to perform this action"},
		throttled:  apiError{http.StatusBadRequest, "Throttling", "Rate exceeded"},
		operations: autoScalingOperations,
	},
	"ssm": {
		name:       "ssm",
		protocol:   awsJSON{targetPrefix: "AmazonSSM."},
		denied:     apiError{http.StatusBadRequest, "AccessDeniedException", "User is not authorized to perform this action"},
		throttled:  apiError{http.StatusBadRequest, "ThrottlingException", "Rate exceeded"},
		operations: ssmOperations,
	},
	"monitoring": {
		name:       "cloudwatch",
		protocol:   rpcCBOR{},
		denied:     apiError{http.StatusForbidden, "AccessDenied", "User is not authorized to perform this action"},
		throttled:  apiError{http.StatusBadRequest, "Throttling", "Rate exceeded"},
		operations: cloudWatchOperations,
	},
	"servicequotas": {
		name:       "servicequotas",
		protocol:   awsJSON{targetPrefix: "ServiceQuotasV20190624."},
		denied:     apiError{http.StatusBadRequest, "AccessDeniedException", "User is not authorized to perform this action"},
		throttled:  apiError{http.StatusBadRequest, "TooManyRequestsException", "Rate exceeded"},
		operations: serviceQuotasOperations,
	},
}

// OperationInfo describes one operation the server answers.
type OperationInfo struct {
	Operation fleettest.Operation
	// Service is the IAM prefix of the service.
	Service string
	// Protocol is the wire protocol, as Smithy names it.
	Protocol string
	// Mutates reports whether the operation changes AWS state.
	Mutates bool
}

// Operations returns every operation the server answers, sorted by name.
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
