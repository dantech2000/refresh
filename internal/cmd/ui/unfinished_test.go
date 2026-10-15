package ui

import (
	"bytes"
	"testing"

	"github.com/dantech2000/refresh/internal/change"
	"github.com/dantech2000/refresh/internal/tui/state"
)

// On the way out the UI names the changes of the session that had not
// ended, with their update IDs: EKS goes on with them.
func TestSayUnfinished(t *testing.T) {
	var out bytes.Buffer
	sayUnfinished(&out, nil)
	if out.Len() != 0 {
		t.Fatalf("nothing unfinished said %q", out.String())
	}
	target := change.Target{Region: "us-east-1", Cluster: "prod"}
	ref := change.StepRef{Phase: change.PhaseRef{Target: target, Kind: change.PhaseRoll}, Kind: change.StepNodegroup, Name: "web"}
	sayUnfinished(&out, []state.Change{
		{View: change.View{Kind: change.NodegroupUpdate, Target: target, State: change.Running, Steps: []change.StepResult{{Ref: ref, Status: change.StepStarted, UpdateID: "8f2c1a9e"}}}},
		{View: change.View{Kind: change.ClusterUpgrade, Target: change.Target{Region: "eu-west-1", Cluster: "staging"}, State: change.Running, Elsewhere: true}},
	})
	want := "2 changes still running in EKS; the UI no longer follows them:\n  roll of prod (us-east-1): update 8f2c1a9e\n  upgrade of staging (eu-west-1) (started elsewhere)\n"
	if out.String() != want {
		t.Fatalf("said %q, want %q", out.String(), want)
	}
}
