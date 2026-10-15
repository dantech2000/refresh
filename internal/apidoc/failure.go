package apidoc

import (
	"cmp"
	"encoding/json"
	"slices"
	"strings"
)

// FailureKind is the type of item a Failure is about.
type FailureKind string

// The failure kinds.
const (
	FailureRegion              FailureKind = "Region"
	FailureCluster             FailureKind = "Cluster"
	FailureNodegroup           FailureKind = "Nodegroup"
	FailureAddon               FailureKind = "Addon"
	FailureInsight             FailureKind = "Insight"
	FailureUpdate              FailureKind = "Update"
	FailurePodDisruptionBudget FailureKind = "PodDisruptionBudget"
	FailureNode                FailureKind = "Node"
)

// Values implements Enum.
func (FailureKind) Values() []string {
	return []string{
		string(FailureRegion), string(FailureCluster), string(FailureNodegroup), string(FailureAddon),
		string(FailureInsight), string(FailureUpdate), string(FailurePodDisruptionBudget), string(FailureNode),
	}
}

// Noun returns the lower-case word for k in text for people, such as
// "nodegroup" or "pdb".
func (k FailureKind) Noun() string {
	if k == FailurePodDisruptionBudget {
		return "pdb"
	}
	return strings.ToLower(string(k))
}

// Reason is why a Failure happened, from a closed set. A script branches on
// it. A later release may add a reason, so a consumer must accept a value
// it does not know.
type Reason string

// The failure reasons.
const (
	// ReasonAccessDenied: IAM, or an SCP, denied the call.
	ReasonAccessDenied Reason = "AccessDenied"
	// ReasonCredentialError: the credentials are missing, expired, or
	// invalid.
	ReasonCredentialError Reason = "CredentialError" //nolint:gosec // G101: a reason name, not a credential
	// ReasonThrottled: AWS rate-limited the call, after the retries.
	ReasonThrottled Reason = "Throttled"
	// ReasonNotFound: the resource does not exist.
	ReasonNotFound Reason = "NotFound"
	// ReasonRegionUnavailable: the region is not enabled for the account,
	// or it cannot be reached.
	ReasonRegionUnavailable Reason = "RegionUnavailable"
	// ReasonInvalidRequest: AWS rejected the request as malformed.
	ReasonInvalidRequest Reason = "InvalidRequest"
	// ReasonServiceError: AWS failed on its side.
	ReasonServiceError Reason = "ServiceError"
	// ReasonNetworkError: the request got no response from AWS.
	ReasonNetworkError Reason = "NetworkError"
	// ReasonTimeout: the deadline of the run passed.
	ReasonTimeout Reason = "Timeout"
	// ReasonInterrupted: the user stopped the run.
	ReasonInterrupted Reason = "Interrupted"
	// ReasonUpdateFailed: an EKS update ended with status Failed.
	ReasonUpdateFailed Reason = "UpdateFailed"
	// ReasonUpdateCancelled: an EKS update ended with status Cancelled.
	ReasonUpdateCancelled Reason = "UpdateCancelled"
	// ReasonNotMonitored: status polling stopped; the EKS update may still
	// be running.
	ReasonNotMonitored Reason = "NotMonitored"
	// ReasonNotAttempted: the run stopped before this item started.
	ReasonNotAttempted Reason = "NotAttempted"
	// ReasonBusy: the item was already changing, so the run left it alone.
	ReasonBusy Reason = "Busy"
	// ReasonUnknown: any other error.
	ReasonUnknown Reason = "Unknown"
)

// Reasons returns every reason, in documentation order.
func Reasons() []Reason {
	return []Reason{
		ReasonAccessDenied, ReasonCredentialError, ReasonThrottled, ReasonNotFound,
		ReasonRegionUnavailable, ReasonInvalidRequest, ReasonServiceError, ReasonNetworkError,
		ReasonTimeout, ReasonInterrupted, ReasonUpdateFailed, ReasonUpdateCancelled,
		ReasonNotMonitored, ReasonNotAttempted, ReasonBusy, ReasonUnknown,
	}
}

// Values implements Enum.
func (Reason) Values() []string {
	reasons := Reasons()
	out := make([]string, len(reasons))
	for i, r := range reasons {
		out[i] = string(r)
	}
	return out
}

// Retryable reports whether the same command, run again with no other
// change, may succeed after a failure for reason r. A reason that is not
// known is not retryable.
func (r Reason) Retryable() bool {
	switch r {
	case ReasonThrottled, ReasonServiceError, ReasonNetworkError, ReasonTimeout,
		ReasonInterrupted, ReasonNotMonitored, ReasonNotAttempted, ReasonBusy:
		return true
	}
	return false
}

// Failure is one item that refresh could not read, or one action that it
// tried and that did not complete.
type Failure struct {
	Kind FailureKind `json:"kind"`
	// Name identifies the item. For a Region failure it is the region.
	Name string `json:"name"`
	// Cluster is the cluster that the item belongs to.
	Cluster string `json:"cluster,omitzero"`
	Region  string `json:"region,omitzero"`
	// Operation is the IAM action that failed, such as
	// "eks:DescribeNodegroup".
	Operation string `json:"operation,omitzero"`
	Reason    Reason `json:"reason"`
	// Retryable reports whether the same command, run again with no other
	// change, may succeed.
	Retryable bool `json:"retryable"`
	// Error is one line of text for people. Do not parse it; use Reason.
	Error string `json:"error"`
	// AWSErrorCode is the AWS API error code, such as
	// AccessDeniedException, when AWS answered.
	AWSErrorCode string `json:"awsErrorCode,omitzero"`
	// UpdateID is the EKS update ID, for a failure of a started update.
	UpdateID string `json:"updateId,omitzero"`
}

// NewFailure returns a Failure for reason. Retryable follows the reason,
// and msg is reduced to one line.
func NewFailure(kind FailureKind, name string, reason Reason, msg string) Failure {
	return Failure{
		Kind:      kind,
		Name:      name,
		Reason:    reason,
		Retryable: reason.Retryable(),
		Error:     strings.Join(strings.Fields(msg), " "),
	}
}

// Failures is the failure list of a document. A nil list encodes as [], so
// a document always has its failures key as a list.
type Failures []Failure

// MarshalJSON encodes l as a JSON array, [] when l is nil.
func (l Failures) MarshalJSON() ([]byte, error) {
	if l == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]Failure(l))
}

// SortFailures orders fs for output: by kind, region, cluster, name, and
// operation, then by reason, update ID, and error, so that the order is the
// same on every run.
func SortFailures(fs []Failure) {
	slices.SortFunc(fs, func(a, b Failure) int {
		return cmp.Or(
			cmp.Compare(a.Kind, b.Kind),
			cmp.Compare(a.Region, b.Region),
			cmp.Compare(a.Cluster, b.Cluster),
			cmp.Compare(a.Name, b.Name),
			cmp.Compare(a.Operation, b.Operation),
			cmp.Compare(a.Reason, b.Reason),
			cmp.Compare(a.UpdateID, b.UpdateID),
			cmp.Compare(a.Error, b.Error),
		)
	})
}
