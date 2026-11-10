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
		{Check: supportReason, Outcome: Warn, Message: "the cluster is in extended support"},
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
