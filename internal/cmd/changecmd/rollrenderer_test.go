package changecmd

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/change"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/noderoll"
)

// terminal returns streams whose stderr is a terminal of 120 by 40, and
// the buffer that it writes to.
func terminal() (*cmdutil.IOStreams, *bytes.Buffer) {
	var errOut bytes.Buffer
	return &cmdutil.IOStreams{
		Out: &bytes.Buffer{}, ErrOut: &errOut, OutTTY: true, ErrTTY: true, NoColor: true,
		TermSize: func() (int, int) { return 120, 40 },
	}, &errOut
}

// rollRef returns the ref of the roll of a nodegroup of a cluster.
func rollRef(cluster, nodegroup string) change.StepRef {
	return change.StepRef{
		Phase: change.PhaseRef{Target: change.Target{Region: "us-east-1", Cluster: cluster}, Kind: change.PhaseRoll},
		Kind:  change.StepNodegroup, Name: nodegroup,
	}
}

// rollPlan returns a plan with one roll of the nodegroup web in each of
// the clusters, and the image that each one moves to.
func rollPlan(images map[string]string) *change.Plan {
	p := &change.Plan{Kind: change.NodegroupUpdate}
	for _, cluster := range []string{"prod", "staging"} {
		image, ok := images[cluster]
		if !ok {
			continue
		}
		ref := rollRef(cluster, "web")
		p.Clusters = append(p.Clusters, change.ClusterPlan{
			Target: ref.Phase.Target,
			Phases: []change.Phase{{Ref: ref.Phase, Batch: true, Steps: []change.Step{{
				Ref: ref, Action: change.Submit, Reason: change.AMIOutdated, From: "1.32", To: "1.32",
				FromRelease: "1.32.0-20250101", FromImage: "ami-old-" + cluster, ToImage: image,
			}}}},
		})
	}
	return p
}

// nodes returns the NodeProgress event of a roll with one old node.
func nodes(ref change.StepRef, at time.Time) change.Event {
	return change.Event{Kind: change.NodeProgress, At: at, Phase: ref.Phase, Step: ref, Nodes: &change.NodeView{
		Nodegroup: ref.Name, Watching: true, StartedAt: at.Add(-time.Minute),
		Snapshot: noderoll.Snapshot{Total: 1, Nodes: []noderoll.NodeView{{Name: "web-1", Ready: true, Phase: noderoll.PhaseReady}}},
	}}
}

// A launch that Auto Scaling refuses is a notice of the run. While the
// panel of the roll is up, the panel shows it at once, and not only when
// the roll ends and the held lines print.
func TestRollRenderer_NoticeShowsInThePanel(t *testing.T) {
	ios, errOut := terminal()
	r := NewRollRenderer(ios)
	ref := rollRef("prod", "web")
	at := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	r.Render(change.Event{Kind: change.PlanReady, Plan: rollPlan(map[string]string{"prod": "ami-new"})})
	r.Render(nodes(ref, at))
	errOut.Reset()

	const launch = "Auto Scaling group eks-prod-web: could not launch a node: the vCPU limit is reached"
	r.Render(change.Event{Kind: change.Notice, At: at, Text: launch})
	during := errOut.String()
	if !strings.Contains(during, launch) {
		t.Fatalf("the panel does not show the notice while the roll runs:\n%q", during)
	}
	if strings.Contains(during, "warning: ") {
		t.Errorf("the line of the notice was written under the panel, which a redraw erases:\n%q", during)
	}

	// The roll ends: the panel comes down, and the held line prints.
	errOut.Reset()
	res := change.StepResult{Ref: ref, Status: change.StepSucceeded}
	r.Render(change.Event{Kind: change.StepChanged, At: at, Phase: ref.Phase, Step: ref, Result: &res})
	if after := errOut.String(); !strings.Contains(after, "warning: "+launch) {
		t.Errorf("the notice is lost when the panel comes down:\n%q", after)
	}

	// A notice with no panel up is a line, as before.
	errOut.Reset()
	r.Render(change.Event{Kind: change.Notice, At: at, Text: "another"})
	if got := errOut.String(); got != "warning: another\n" {
		t.Errorf("a notice without a panel = %q, want one warning line", got)
	}
}

// The header of the panel names each end of the roll for what it is: an
// image ID is an AMI, and not a release version.
func TestRollRenderer_HeaderNamesWhatItShows(t *testing.T) {
	tests := []struct {
		name string
		step change.Step
		want string
	}{
		{name: "an AMI roll", step: change.Step{From: "1.32", To: "1.32", FromRelease: "1.32.0-20250101", FromImage: "ami-old", ToImage: "ami-new"},
			want: "rolling web   AMI ami-old -> AMI ami-new"},
		{name: "an AMI roll with no image read for the nodes", step: change.Step{From: "1.32", To: "1.32", FromRelease: "1.32.0-20250101", ToImage: "ami-new"},
			want: "rolling web   AMI release 1.32.0-20250101 -> AMI ami-new"},
		{name: "the roll of an upgrade", step: change.Step{From: "1.32", To: "1.33"}, want: "rolling web   Kubernetes 1.32 -> Kubernetes 1.33"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ios, errOut := terminal()
			r := NewRollRenderer(ios)
			ref := rollRef("prod", "web")
			tt.step.Ref, tt.step.Action = ref, change.Submit
			plan := &change.Plan{Kind: change.NodegroupUpdate, Clusters: []change.ClusterPlan{{
				Target: ref.Phase.Target, Phases: []change.Phase{{Ref: ref.Phase, Batch: true, Steps: []change.Step{tt.step}}},
			}}}
			r.quiet = true // the plan lines are not what this test reads
			r.Render(change.Event{Kind: change.PlanReady, Plan: plan})
			r.Render(nodes(ref, time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)))
			if got := errOut.String(); !strings.Contains(got, tt.want) {
				t.Errorf("the header lacks %q:\n%s", tt.want, got)
			}
		})
	}
}

// Two clusters of a fleet each have a nodegroup of the same name. The
// panel of each shows its own image.
func TestRollRenderer_KeysByCluster(t *testing.T) {
	ios, errOut := terminal()
	r := NewRollRenderer(ios)
	r.quiet = true
	at := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	r.Render(change.Event{Kind: change.PlanReady, Plan: rollPlan(map[string]string{"prod": "ami-prod", "staging": "ami-staging"})})

	prod := rollRef("prod", "web")
	r.Render(nodes(prod, at))
	if got := errOut.String(); !strings.Contains(got, "AMI ami-old-prod -> AMI ami-prod") {
		t.Errorf("the panel of prod/web does not show its own images:\n%s", got)
	}
	// The roll of staging/web ends while the panel of prod/web is up: the
	// panel stays, since it is another roll.
	errOut.Reset()
	done := change.StepResult{Ref: rollRef("staging", "web"), Status: change.StepSucceeded}
	r.Render(change.Event{Kind: change.StepChanged, At: at, Phase: done.Ref.Phase, Step: done.Ref, Result: &done})
	if r.up == nil || *r.up != prod {
		t.Errorf("the panel of prod/web came down for the roll of staging/web")
	}
	res := change.StepResult{Ref: prod, Status: change.StepSucceeded}
	r.Render(change.Event{Kind: change.StepChanged, At: at, Phase: prod.Phase, Step: prod, Result: &res})

	errOut.Reset()
	r.Render(nodes(rollRef("staging", "web"), at))
	if got := errOut.String(); !strings.Contains(got, "AMI ami-old-staging -> AMI ami-staging") {
		t.Errorf("the panel of staging/web does not show its own images:\n%s", got)
	}
}

// TERM=dumb cannot move the cursor, so the panel is appended there, as it
// is without a terminal, and no cursor code is written.
func TestRollRenderer_DumbTerminalAppendsFrames(t *testing.T) {
	ios, errOut := terminal()
	ios.Dumb = true
	r := NewRollRenderer(ios)
	ref := rollRef("prod", "web")
	at := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	r.Render(change.Event{Kind: change.PlanReady, Plan: rollPlan(map[string]string{"prod": "ami-new"})})
	r.Render(nodes(ref, at))
	res := change.StepResult{Ref: ref, Status: change.StepSucceeded}
	r.Render(change.Event{Kind: change.StepChanged, At: at, Phase: ref.Phase, Step: ref, Result: &res})

	got := errOut.String()
	if !strings.Contains(got, "web-1") {
		t.Errorf("the frame of the roll is missing:\n%q", got)
	}
	if strings.Contains(got, "\x1b[") {
		t.Errorf("a dumb terminal got cursor codes:\n%q", got)
	}
}
