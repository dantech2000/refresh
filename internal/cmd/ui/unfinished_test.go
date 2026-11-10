package ui

import (
	"bytes"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/change"
	"github.com/dantech2000/refresh/internal/tui/state"
)

// On the way out the UI names the changes of the session that had not
// ended. An update in flight goes on in EKS, with its ID. A change of
// this UI stops with it: the steps it had not started do not run, and
// the UI names them with the command of the dry run. Found in review:
// every unfinished change was "still running in EKS", also one that only
// asked or paused.
func TestSayUnfinished(t *testing.T) {
	prod := change.Target{Region: "us-east-1", Cluster: "prod"}
	staging := change.Target{Region: "eu-west-1", Cluster: "staging"}
	ref := func(phase change.PhaseKind, kind change.StepKind, name string) change.StepRef {
		return change.StepRef{Phase: change.PhaseRef{Target: prod, Kind: phase}, Kind: kind, Name: name}
	}
	roll := ref(change.PhaseRoll, change.StepNodegroup, "web")
	cp := ref(change.PhaseControlPlane, change.StepControlPlane, "")
	api := ref(change.PhaseNodegroups, change.StepNodegroup, "api")
	web := ref(change.PhaseNodegroups, change.StepNodegroup, "web")
	const upgradeCmd = "refresh --region us-east-1 cluster upgrade prod --to 1.34"

	for _, tc := range []struct {
		name    string
		changes []state.Change
		want    string
	}{
		{"nothing", nil, ""},
		{
			"a roll in EKS and a watch",
			[]state.Change{
				{View: change.View{Kind: change.NodegroupUpdate, Target: prod, State: change.Running, Steps: []change.StepResult{{Ref: roll, Status: change.StepStarted, UpdateID: "8f2c1a9e"}}}, Command: "refresh nodegroup update web -c prod"},
				{View: change.View{Kind: change.ClusterUpgrade, Target: staging, State: change.Running, Elsewhere: true}},
			},
			"2 changes still running in EKS; the UI no longer follows them:\n  roll of prod (us-east-1): update 8f2c1a9e\n  upgrade of staging (eu-west-1) (started elsewhere)\n",
		},
		{
			"an upgrade paused between phases",
			[]state.Change{{View: change.View{Kind: change.ClusterUpgrade, Target: prod, State: change.Paused, Steps: []change.StepResult{
				{Ref: cp, Status: change.StepSucceeded, UpdateID: "cp-1", FinishedAt: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)},
				{Ref: api, Status: change.StepNotAttempted},
				{Ref: web, Status: change.StepNotAttempted},
			}}, Command: upgradeCmd}},
			"1 change of this UI stopped with it:\n  upgrade of prod (us-east-1): did not start nodegroup api, nodegroup web\n    the same change from the command line: " + upgradeCmd + "\n",
		},
		{
			"an upgrade with the control plane in flight",
			[]state.Change{{View: change.View{Kind: change.ClusterUpgrade, Target: prod, State: change.Running, Steps: []change.StepResult{
				{Ref: cp, Status: change.StepStarted, UpdateID: "cp-1"},
				{Ref: api, Status: change.StepNotAttempted},
			}}, Command: upgradeCmd}},
			"1 change still running in EKS; the UI no longer follows it:\n  upgrade of prod (us-east-1): update cp-1\n" +
				"1 change of this UI stopped with it:\n  upgrade of prod (us-east-1): did not start nodegroup api\n    the same change from the command line: " + upgradeCmd + "\n",
		},
		{
			"an add-on update that asks before anything",
			[]state.Change{{View: change.View{Kind: change.AddonUpdate, Target: prod, State: change.Asking}, Command: "refresh addon update --all -c prod"}},
			"1 change of this UI stopped with it:\n  add-on update of prod (us-east-1): nothing was started\n    the same change from the command line: refresh addon update --all -c prod\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			sayUnfinished(&out, tc.changes)
			if out.String() != tc.want {
				t.Fatalf("said\n%s\nwant\n%s", out.String(), tc.want)
			}
		})
	}
}
