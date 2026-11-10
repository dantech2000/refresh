package health

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
)

// The reasons of a readiness report carry the outcome of the check
// behind each, and a support window that is not standard is a Warn
// reason of its own: a view reads the outcome and parses no text.
func TestReadinessOfReasons(t *testing.T) {
	results := []Result{
		{Name: "Insights", Outcome: Warn, Blocking: true, Message: "1 insight warns"},
		{Name: "VersionSkew", Outcome: Fail, Blocking: true, FailClosed: true, Message: "1 nodegroup at the kubelet skew limit"},
		{Name: "ControlPlane", Outcome: Pass, Message: "healthy"},
	}
	readiness, reasons := readinessOf(results, &apidoc.Support{Tier: apidoc.SupportExtended})
	want := []Reason{
		{Check: "VersionSkew", Outcome: Fail, Message: "1 nodegroup at the kubelet skew limit"},
		{Check: "Insights", Outcome: Warn, Message: "1 insight warns"},
		{Check: SupportReason, Outcome: Warn, Message: "the cluster is in extended support"},
	}
	if diff := cmp.Diff(want, reasons); diff != "" {
		t.Errorf("reasons (-want +got):\n%s", diff)
	}
	if readiness != apidoc.ReadinessBlocked {
		t.Errorf("readiness = %s, want Blocked", readiness)
	}
	texts := ReadinessReport{Reasons: reasons}.ReasonTexts()
	wantTexts := []string{"VersionSkew: 1 nodegroup at the kubelet skew limit", "Insights: 1 insight warns", "Support: the cluster is in extended support"}
	if diff := cmp.Diff(wantTexts, texts); diff != "" {
		t.Errorf("texts (-want +got):\n%s", diff)
	}
	if got := (ReadinessReport{}).ReasonTexts(); got == nil {
		t.Error("the texts of no reason are nil; the document needs []")
	}
}

// The verdict of cluster upgrade-check: a Fail blocks, a block from data
// that could not be read alone is Incomplete, a warning is Review, and a
// support window that is not standard turns Ready into Review.
func TestReadinessOf(t *testing.T) {
	pass := Result{Name: "Insights", Outcome: Pass, Blocking: true, FailClosed: true}
	warn := Result{Name: "Insights", Outcome: Warn, Blocking: true, FailClosed: true}
	fail := Result{Name: "VersionSkew", Outcome: Fail, Blocking: true, FailClosed: true}
	unread := Result{Name: "ControlPlane", Outcome: Unreadable, Blocking: true, FailClosed: true}
	tests := []struct {
		name    string
		results []Result
		support *apidoc.Support
		want    apidoc.Readiness
	}{
		{name: "every check passes", results: []Result{pass}, want: apidoc.ReadinessReady},
		{name: "a warning", results: []Result{warn}, want: apidoc.ReadinessReview},
		{name: "a failure", results: []Result{fail, unread}, want: apidoc.ReadinessBlocked},
		{name: "unread data alone", results: []Result{pass, unread}, want: apidoc.ReadinessIncomplete},
		{name: "extended support", results: []Result{pass}, support: &apidoc.Support{Tier: apidoc.SupportExtended}, want: apidoc.ReadinessReview},
		{name: "extended support does not lift a block", results: []Result{fail}, support: &apidoc.Support{Tier: apidoc.SupportExtended}, want: apidoc.ReadinessBlocked},
		{name: "standard support", results: []Result{pass}, support: &apidoc.Support{Tier: apidoc.SupportStandard}, want: apidoc.ReadinessReady},
		{name: "support not known", results: []Result{pass}, support: &apidoc.Support{Tier: apidoc.SupportUnknown}, want: apidoc.ReadinessReady},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, _ := readinessOf(tt.results, tt.support); got != tt.want {
				t.Errorf("readiness = %s, want %s", got, tt.want)
			}
		})
	}
}
