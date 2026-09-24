// Package diag describes the failures of a command run: the items refresh
// could not read and the actions it attempted that did not complete.
//
// A command collects its failures as a List of Failure values and reports
// them in three places from that one list: the -o json/yaml document (under
// "failures"), one stderr line per failure, and the exit code. The runner
// package renders the stderr lines and the exit error.
//
// Each Failure carries a Reason from a closed set, so scripts can branch on
// it; the Error text is for people. Build a Failure from an error with
// FromError, which classifies the error by type (never by its text), or with
// New when there is no error value, such as an EKS update that ended Failed.
//
// diag is a leaf package: it imports only the standard library and
// internal/aws/awserr.
package diag

import (
	"cmp"
	"encoding/json"
	"slices"
	"strings"

	"github.com/dantech2000/refresh/internal/aws/awserr"
)

// Kind is the type of item a Failure is about.
type Kind string

// The item kinds. Add a kind only when a command needs it.
const (
	KindRegion              Kind = "Region"
	KindCluster             Kind = "Cluster"
	KindNodegroup           Kind = "Nodegroup"
	KindAddon               Kind = "Addon"
	KindInsight             Kind = "Insight"
	KindUpdate              Kind = "Update"
	KindPodDisruptionBudget Kind = "PodDisruptionBudget"
	KindNode                Kind = "Node"
)

// Noun returns the lower-case word for k in human text, such as "nodegroup"
// or "pdb". An unknown kind returns its value in lower case.
func (k Kind) Noun() string {
	if k == KindPodDisruptionBudget {
		return "pdb"
	}
	return strings.ToLower(string(k))
}

// Failure is one item refresh could not read, or one action it attempted
// that did not complete. The JSON and YAML keys are a public contract
// (docs/concepts/output.md): later versions only add fields.
type Failure struct {
	Kind Kind `json:"kind" yaml:"kind"`
	// Name identifies the item. For KindRegion it is the region name.
	Name    string `json:"name" yaml:"name"`
	Cluster string `json:"cluster,omitempty" yaml:"cluster,omitempty"`
	Region  string `json:"region,omitempty" yaml:"region,omitempty"`
	// Operation is the IAM action that failed, such as "eks:DescribeNodegroup".
	Operation string `json:"operation,omitempty" yaml:"operation,omitempty"`
	Reason    Reason `json:"reason" yaml:"reason"`
	// Retryable reports whether running the same command again, with no
	// other change, may succeed. It is always encoded.
	Retryable bool `json:"retryable" yaml:"retryable"`
	// Error is one line of human text. Do not parse it; use Reason.
	Error string `json:"error" yaml:"error"`
	// AWSErrorCode is the raw AWS API error code, such as
	// AccessDeniedException, when the failure came from an API response.
	AWSErrorCode string `json:"awsErrorCode,omitempty" yaml:"awsErrorCode,omitempty"`
	UpdateID     string `json:"updateId,omitempty" yaml:"updateId,omitempty"`
}

// FromError returns the Failure for err: kind and name identify the item, op
// is the IAM action that failed ("" when there is none), and the reason,
// retryability, and AWS error code come from Classify. Set Cluster, Region,
// and UpdateID on the result when they apply.
//
// For KindRegion, the error codes a region that is not enabled for the
// account returns (UnrecognizedClientException, InvalidClientTokenId,
// AuthFailure) are reported as ReasonRegionUnavailable: the credentials
// already passed the check that every command makes first. An AccessDenied
// code stays ReasonAccessDenied, because a missing IAM action is the more
// useful diagnosis.
func FromError(kind Kind, name, op string, err error) Failure {
	reason, retryable, code := Classify(err)
	if kind == KindRegion && reason != ReasonAccessDenied && awserr.IsRegionInaccessible(err) {
		reason, retryable = ReasonRegionUnavailable, ReasonRegionUnavailable.Retryable()
	}
	msg := awserr.Summary(err)
	if msg == "" {
		msg = "unknown error"
	}
	return Failure{
		Kind:         kind,
		Name:         name,
		Operation:    op,
		Reason:       reason,
		Retryable:    retryable,
		Error:        msg,
		AWSErrorCode: code,
	}
}

// New returns a Failure that has no error value behind it, such as an EKS
// update that ended Failed (ReasonUpdateFailed) or an item a stopped run
// never reached (ReasonNotAttempted). Retryable follows reason. msg is
// reduced to one line.
func New(kind Kind, name string, reason Reason, msg string) Failure {
	return Failure{
		Kind:      kind,
		Name:      name,
		Reason:    reason,
		Retryable: reason.Retryable(),
		Error:     strings.Join(strings.Fields(msg), " "),
	}
}

// List is the failures of one command run. A nil List encodes as [] in JSON
// (and in YAML, which the runner renders from the JSON), so a document always
// carries its "failures" key as a list.
type List []Failure

// MarshalJSON encodes l as a JSON array, [] when l is nil.
func (l List) MarshalJSON() ([]byte, error) {
	if l == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]Failure(l))
}

// Sort orders fs for output: by Kind, Region, Cluster, Name, and Operation,
// then by Reason, UpdateID, and Error so that equal keys still sort the same
// way on every run.
func Sort(fs []Failure) {
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
