package cluster_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/cmd/cmdtest"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// planSteps is what a test reads of a Plan or a Run document.
type planSteps struct {
	Kind     string
	Clusters []struct {
		Decision string
		Steps    []struct{ Name, Kind, Action, Reason string }
		Blockers []struct{ Kind, Message string }
		Notices  []string
	}
}

func readPlan(t *testing.T, out []byte) planSteps {
	t.Helper()
	var p planSteps
	if err := json.Unmarshal(out, &p); err != nil || len(p.Clusters) != 1 {
		t.Fatalf("the document: %v\n%s", err, out)
	}
	return p
}

// An Amazon Linux 2 nodegroup cannot go past 1.32: EKS publishes no AL2
// AMI for 1.33. The upgrade is blocked at the plan, before the control
// plane moves, and the message points to AL2023 and to --skip-nodegroup.
// Nothing but the refresh of the insights is sent.
func TestUpgrade_AL2NodegroupBlocksThePlan(t *testing.T) {
	al2 := func(_ *fleettest.State, c *fleettest.Cluster) { c.Nodegroup("api").AMIType = "AL2_x86_64" }
	for _, args := range [][]string{{"--dry-run"}, {"--yes"}} {
		t.Run(args[0], func(t *testing.T) {
			e := cmdtest.NewFake(t, engineWorld("1.32", false, al2), east)
			e.UseWorldClock()
			withKube(t, e)
			err := e.Run(cmdutiltest.Context(t), append([]string{"cluster", "upgrade", "prod", "--to", "1.33", "-o", "json"}, args...)...)
			if got := cmdutil.ExitCode(err); got != cmdutil.ExitBlocked {
				t.Errorf("exit code = %d (%v), want 3", got, err)
			}
			for _, s := range sentBy(e) {
				if s != refresh {
					t.Errorf("%s was sent by a blocked upgrade", s)
				}
			}
			p := readPlan(t, e.Out.Bytes())
			if p.Clusters[0].Decision != "Blocked" {
				t.Errorf("decision = %s, want Blocked", p.Clusters[0].Decision)
			}
			found := false
			for _, b := range p.Clusters[0].Blockers {
				found = found || (strings.Contains(b.Message, "AL2023") && strings.Contains(b.Message, "--skip-nodegroup api"))
			}
			if !found {
				t.Errorf("no blocker points to AL2023 and --skip-nodegroup api: %+v", p.Clusters[0].Blockers)
			}
		})
	}
}

// Compute that is not a managed nodegroup: Auto Mode, Karpenter nodes, a
// self-managed node, a Fargate node, and a hybrid node. The plan of an
// upgrade has steps for the managed nodegroups only, the run rolls only
// them, and the checks after each roll count only the new nodes of that
// nodegroup, so the other nodes neither fail nor pass it.
func TestUpgrade_OtherCompute(t *testing.T) {
	other := func(s *fleettest.State, c *fleettest.Cluster) {
		c.AutoMode = true
		c.Nodes = append(c.Nodes,
			&fleettest.Node{Name: "karpenter-1", Labels: map[string]string{"karpenter.sh/nodepool": "default"}},
			&fleettest.Node{Name: "self-1"},
			&fleettest.Node{Name: "fargate-1", Labels: map[string]string{"eks.amazonaws.com/compute-type": "fargate"}},
			&fleettest.Node{Name: "hybrid-1", NotReady: true, Labels: map[string]string{"eks.amazonaws.com/compute-type": "hybrid"}},
		)
		c.Pods = append(c.Pods, &fleettest.Pod{Namespace: "default", Name: "batch-1", Node: "karpenter-1", Labels: map[string]string{"app": "batch"}})
		s.Instances = append(s.Instances, fleettest.Instance{ID: "i-karpenter-1", Region: east, ImageID: "ami-karpenter", Type: "m5.large", Tags: map[string]string{"karpenter.sh/nodepool": "default"}})
	}
	e := cmdtest.NewFake(t, engineWorld("1.32", false, other), east)
	e.UseWorldClock()
	withKube(t, e)
	err := e.Run(cmdutiltest.Context(t), "cluster", "upgrade", "prod", "--to", "1.33", "--dry-run", "-o", "json")
	t.Logf("dry run: exit %d (%v)", cmdutil.ExitCode(err), err)
	p := readPlan(t, e.Out.Bytes())
	for _, s := range p.Clusters[0].Steps {
		if s.Kind == "Nodegroup" && s.Name != "web" && s.Name != "api" {
			t.Errorf("a step for %s, which is no managed nodegroup", s.Name)
		}
	}

	run := cmdtest.NewFake(t, engineWorld("1.32", false, other), east)
	run.UseWorldClock()
	withKube(t, run)
	err = run.Run(cmdutiltest.Context(t), "cluster", "upgrade", "prod", "--to", "1.33", "--yes")
	if got := cmdutil.ExitCode(err); got == cmdutil.ExitFailure || got == cmdutil.ExitVerifyFailed {
		t.Errorf("exit code = %d (%v)", got, err)
	}
	want := []string{refresh, cp33, cniTo33, api33, web33, proxyTo3}
	got := sentBy(run)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("sent %v, want %v", got, want)
	}
}
