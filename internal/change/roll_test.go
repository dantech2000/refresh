package change

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/exitcode"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/health"
)

// oldImage is the AMI of a nodegroup of the roll world that is behind.
const oldImage = "ami-old32x"

// addRollNodegroup adds a nodegroup on Kubernetes 1.32 to c, with its Auto
// Scaling group, two instances with CPU metrics, two nodes, a pod of its
// own workload on each node, and a budget that lets one of the two go. A
// nodegroup that is not current runs an old AMI.
func addRollNodegroup(s *fleettest.State, c *fleettest.Cluster, name string, current bool) {
	group := "eks-" + c.Name + "-" + name
	ng := &fleettest.Nodegroup{Name: name, Version: "1.32", AutoScalingGroups: []string{group}}
	image := fleettest.LatestImage("1.32")
	if !current {
		ng.ReleaseVersion, image = "1.32.0-20250101", oldImage
	}
	c.Nodegroups = append(c.Nodegroups, ng)
	ids := []string{"i-" + group + "-1", "i-" + group + "-2"}
	s.AutoScalingGroups = append(s.AutoScalingGroups, fleettest.AutoScalingGroup{Name: group, InstanceIDs: ids})
	s.Images = append(s.Images, fleettest.Image{ID: image, Name: image})
	app := map[string]string{"app": name}
	for i, id := range ids {
		s.Instances = append(s.Instances, fleettest.Instance{ID: id, ImageID: image, Type: "m5.large", AvailabilityZone: "us-east-1a"})
		s.Metrics = append(s.Metrics, fleettest.Metric{Namespace: "AWS/EC2", Name: "CPUUtilization", Dimensions: map[string]string{"InstanceId": id}, Values: []float64{30, 35}})
		node := fmt.Sprintf("%s-%d", name, i+1)
		c.Nodes = append(c.Nodes, &fleettest.Node{Name: node, Nodegroup: name, ImageID: image, UsedMilliCPU: 400, UsedMemoryMi: 2048})
		c.Pods = append(c.Pods, &fleettest.Pod{Namespace: "default", Name: node, Node: node, Labels: app})
	}
	c.PDBs = append(c.PDBs, fleettest.PDB{Namespace: "default", Name: name, Selector: app, MinAvailable: 1, DisruptionsAllowed: 1, CurrentHealthy: 2, DesiredHealthy: 1, ExpectedPods: 2})
	c.Deployments = append(c.Deployments, fleettest.Deployment{Namespace: "default", Name: name, Replicas: 2, Selector: app})
}

// addRollCluster adds a healthy cluster to s: ng-a and ng-b are on an old
// AMI, and ng-c is on the latest.
func addRollCluster(s *fleettest.State, name string) *fleettest.Cluster {
	c := &fleettest.Cluster{Name: name, Region: east, Version: "1.32"}
	s.Clusters = append(s.Clusters, c)
	addRollNodegroup(s, c, "ng-a", false)
	addRollNodegroup(s, c, "ng-b", false)
	addRollNodegroup(s, c, "ng-c", true)
	dns := map[string]string{"k8s-app": "kube-dns"}
	c.Pods = append(c.Pods, &fleettest.Pod{Namespace: "kube-system", Name: "coredns-1", Node: "ng-c-1", Labels: dns})
	c.Deployments = append(c.Deployments, fleettest.Deployment{Namespace: "kube-system", Name: "coredns", Replicas: 1, Selector: dns})
	return c
}

// rollWorld returns a world with the cluster prod and a vCPU quota with
// room. Every update takes 30 seconds. edit changes the world.
func rollWorld(edit func(s *fleettest.State, c *fleettest.Cluster)) *fleettest.World {
	world := fleettest.New()
	world.Edit(func(s *fleettest.State) {
		prod := addRollCluster(s, "prod")
		s.Quotas = []fleettest.Quota{{ServiceCode: "ec2", QuotaCode: "L-1216C47A", Value: 128}}
		s.Metrics = append(s.Metrics, fleettest.Metric{
			Namespace: "AWS/Usage", Name: "ResourceCount", Values: []float64{40},
			Dimensions: map[string]string{"Service": "EC2", "Type": "Resource", "Resource": "vCPU", "Class": "Standard/OnDemand"},
		})
		s.DefaultUpdate = fleettest.UpdateScript{After: 30 * time.Second}
		if edit != nil {
			edit(s, prod)
		}
	})
	return world
}

// rollOf returns a roll of the named nodegroups of prod, or of all of
// them, with --yes.
func rollOf(names ...string) RollRequest {
	return RollRequest{
		Common:     Common{Policy: health.Policy{Yes: true}, Limits: Limits{Poll: 15 * time.Second}},
		Target:     Target{Cluster: "prod"},
		Nodegroups: names,
	}
}

// sentRoll is one UpdateNodegroupVersion request, as the fake logged it.
type sentRoll struct {
	Cluster, Nodegroup, Version string
	Force                       bool
}

// rolls returns the rolls that the fake was asked to start, in order. It
// fails the test for a request with a release version or a launch
// template: a roll sends neither.
func (e *env) rolls() []sentRoll {
	e.t.Helper()
	var out []sentRoll
	for _, r := range e.srv.Requests() {
		if r.Operation != fleettest.EKSUpdateNodegroupVersion {
			continue
		}
		for _, key := range []string{"releaseVersion", "launchTemplate"} {
			if _, ok := r.Input[key]; ok {
				e.t.Errorf("the roll of %v carries %s", r.Input["nodegroupName"], key)
			}
		}
		ro := sentRoll{Cluster: fmt.Sprint(r.Input["clusterName"]), Nodegroup: fmt.Sprint(r.Input["nodegroupName"])}
		ro.Version, _ = r.Input["version"].(string)
		ro.Force, _ = r.Input["force"].(bool)
		out = append(out, ro)
	}
	return out
}

// clusterStatuses returns the status of each step of the result, by
// cluster and nodegroup.
func clusterStatuses(res Result) map[string]StepStatus {
	out := map[string]StepStatus{}
	for _, c := range res.Clusters {
		for _, s := range c.Steps {
			out[c.Cluster+"/"+s.Ref.Name] = s.Status
		}
	}
	return out
}

// The decision table of a roll, row by row. It is the old table less the
// row in which --force rolled a nodegroup on the latest AMI: the function
// does not take the flag at all.
func TestDecideAMIUpdate(t *testing.T) {
	ami := func(current, latest string) fleet.AMI {
		return fleet.AMI{State: fleet.Collected, Current: current, Latest: latest}
	}
	managed := func(status string, a fleet.AMI) fleet.Nodegroup {
		return fleet.Nodegroup{Name: "ng", State: fleet.Collected, Status: status, AMIType: "AL2023_x86_64_STANDARD", AMI: a}
	}
	custom := managed("ACTIVE", ami("ami-old", "ami-new"))
	custom.AMIType = "CUSTOM"
	tests := []struct {
		name   string
		ng     fleet.Nodegroup
		reroll bool
		want   RollDecision
	}{
		{"1 custom", custom, false, RollDecision{Manual, CustomAMI}},
		{"1 custom, with reroll", custom, true, RollDecision{Manual, CustomAMI}},
		{"2 updating", managed("UPDATING", ami("ami-old", "ami-new")), false, RollDecision{Skip, InFlight}},
		{"2 updating, with reroll", managed("UPDATING", ami("ami-new", "ami-new")), true, RollDecision{Skip, InFlight}},
		{"2 creating", managed("CREATING", ami("", "ami-new")), false, RollDecision{Skip, InFlight}},
		{"2 deleting", managed("DELETING", ami("ami-old", "ami-new")), false, RollDecision{Skip, InFlight}},
		{"3 unknown current", managed("ACTIVE", ami("", "ami-new")), false, RollDecision{Submit, AMIUnknown}},
		{"3 unknown latest", managed("ACTIVE", ami("ami-old", "")), false, RollDecision{Submit, AMIUnknown}},
		{"3 AMIs not read", managed("ACTIVE", fleet.AMI{State: fleet.Failed, Current: "ami-new", Latest: "ami-new"}), false, RollDecision{Submit, AMIUnknown}},
		{"4 on latest, with reroll", managed("ACTIVE", ami("ami-new", "ami-new")), true, RollDecision{Submit, Reroll}},
		{"5 on latest", managed("ACTIVE", ami("ami-new", "ami-new")), false, RollDecision{Skip, AlreadyLatest}},
		{"5 on latest and degraded", managed("DEGRADED", ami("ami-new", "ami-new")), false, RollDecision{Skip, AlreadyLatest}},
		{"6 outdated", managed("ACTIVE", ami("ami-old", "ami-new")), false, RollDecision{Submit, AMIOutdated}},
		{"6 outdated, with reroll", managed("ACTIVE", ami("ami-old", "ami-new")), true, RollDecision{Submit, AMIOutdated}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DecideAMIUpdate(tt.ng, tt.reroll); got != tt.want {
				t.Errorf("DecideAMIUpdate = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// Safety rule 5. --force alone never rolls a nodegroup that is on the
// latest AMI, and nothing rolls a nodegroup with a custom AMI.
func TestRoll_ForceNeverRollsLatestOrCustom(t *testing.T) {
	tests := []struct {
		name          string
		force, reroll bool
		custom        bool
		want          []sentRoll
		action        Action
	}{
		{name: "force on the latest AMI", force: true, action: Skip},
		{name: "reroll on the latest AMI", reroll: true, action: Submit, want: []sentRoll{{"prod", "ng-c", "1.32", false}}},
		{name: "force and reroll on the latest AMI", force: true, reroll: true, action: Submit, want: []sentRoll{{"prod", "ng-c", "1.32", true}}},
		{name: "custom AMI", custom: true, action: Manual},
		{name: "custom AMI with force and reroll", custom: true, force: true, reroll: true, action: Manual},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, rollWorld(func(_ *fleettest.State, c *fleettest.Cluster) {
				if tt.custom {
					c.Nodegroup("ng-c").AMIType = "CUSTOM"
				}
			}))
			req := rollOf("ng-c")
			req.Policy.Force, req.Reroll, req.Policy.Skip = tt.force, tt.reroll, true
			res, err := Run(testContext(t), e.deps, req, Hooks{})
			if err != nil {
				t.Fatal(err)
			}
			if got := res.Plan.Clusters[0].Steps()[0].Action; got != tt.action {
				t.Errorf("action = %s, want %s", got, tt.action)
			}
			if diff := cmp.Diff(tt.want, e.rolls()); diff != "" {
				t.Errorf("rolls (-want +got):\n%s", diff)
			}
		})
	}
}

// Safety rule 1. Nothing is sent in a dry run, after a no, after a
// blocking verdict, after a refused plan, or with nobody to ask and no
// --yes.
func TestRoll_NothingSentWithoutConsent(t *testing.T) {
	interactive := func(r RollRequest) RollRequest { r.Policy.Yes = false; return r }
	tests := []struct {
		name string
		run  func(t *testing.T, e *env) (Result, error)
		// status and exit are the ones of the result. A dry run has none.
		status RunStatus
		exit   int
	}{
		{name: "dry run", run: func(t *testing.T, e *env) (Result, error) {
			t.Helper()
			p, err := NewPlan(testContext(t), e.deps, rollOf())
			if err == nil && p.Decision() != Ask {
				t.Errorf("decision = %s, want Ask", p.Decision())
			}
			return Result{Status: Refused}, err
		}, status: Refused},
		{name: "answered no", status: Declined, exit: exitcode.Failure, run: func(t *testing.T, e *env) (Result, error) {
			t.Helper()
			s := &script{t: t, answers: []bool{false}}
			return Run(testContext(t), e.deps, interactive(rollOf()), s.hooks())
		}},
		{name: "blocking verdict", status: Refused, exit: exitcode.Blocked, run: func(t *testing.T, e *env) (Result, error) {
			t.Helper()
			e.withKube(t)
			e.world.Edit(func(s *fleettest.State) {
				pdb := &s.Cluster(east, "prod").PDBs[1]
				pdb.MinAvailable, pdb.DesiredHealthy, pdb.DisruptionsAllowed = 2, 2, 0
			})
			return Run(testContext(t), e.deps, rollOf(), Hooks{})
		}},
		{name: "refused plan", status: Refused, exit: exitcode.Blocked, run: func(t *testing.T, e *env) (Result, error) {
			t.Helper()
			// The Kubernetes version of a target changes while the
			// question is open.
			s := &script{t: t, answers: []bool{true}, onAsk: func(int) {
				e.world.Edit(func(s *fleettest.State) { s.Cluster(east, "prod").Nodegroup("ng-a").Version = "1.31" })
			}}
			return Run(testContext(t), e.deps, interactive(rollOf()), s.hooks())
		}},
		{name: "nobody to ask", status: Refused, exit: exitcode.Failure, run: func(t *testing.T, e *env) (Result, error) {
			t.Helper()
			return Run(testContext(t), e.deps, interactive(rollOf()), Hooks{})
		}},
		{name: "nobody to ask, clean gate", status: Refused, exit: exitcode.Failure, run: func(t *testing.T, e *env) (Result, error) {
			t.Helper()
			e.withKube(t)
			return Run(testContext(t), e.deps, interactive(rollOf()), Hooks{})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, rollWorld(nil))
			res, err := tt.run(t, e)
			if err != nil {
				t.Fatal(err)
			}
			if res.Status != tt.status || res.ExitCode() != tt.exit {
				t.Errorf("status = %s, exit %d, want %s, exit %d (%v)", res.Status, res.ExitCode(), tt.status, tt.exit, res.Err)
			}
			if got := e.mutations(); len(got) != 0 {
				t.Errorf("mutating requests = %v, want none", got)
			}
		})
	}
}

// Safety rule 2. The whole batch is gated before the first roll starts: a
// blocker that is about the second target stops the first one too, and in
// a run that goes on, no read of the gate comes after the first roll.
func TestRoll_WholeBatchGatedBeforeFirstStart(t *testing.T) {
	t.Run("a blocker of the second target", func(t *testing.T) {
		e := newEnv(t, rollWorld(func(_ *fleettest.State, c *fleettest.Cluster) {
			// The budget of the pods on ng-b allows no disruption.
			c.PDBs[1].MinAvailable, c.PDBs[1].DesiredHealthy, c.PDBs[1].DisruptionsAllowed = 2, 2, 0
		}))
		e.withKube(t)
		res, err := Run(testContext(t), e.deps, rollOf("ng-a", "ng-b"), Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != Refused || res.ExitCode() != exitcode.Blocked {
			t.Errorf("status = %s, exit %d, want Refused, exit 3", res.Status, res.ExitCode())
		}
		if got := e.rolls(); len(got) != 0 {
			t.Errorf("rolls = %v, want none: the blocker of ng-b stops ng-a too", got)
		}
		gate := res.Plan.Clusters[0].Gate
		if len(gate.Blockers) != 1 || gate.Blockers[0].Check != "DrainBlockers" {
			t.Errorf("blockers = %v, want the drain blocker", gate.Blockers)
		}
	})
	t.Run("the gate ends before the first roll", func(t *testing.T) {
		e := newEnv(t, rollWorld(nil))
		srv := e.withKube(t)
		res, err := Run(testContext(t), e.deps, rollOf("ng-a", "ng-b"), Hooks{})
		if err != nil || res.ExitCode() != 0 {
			t.Fatalf("run: %v, exit %d (%v)", err, res.ExitCode(), res.Err)
		}
		ops := e.operations(0)
		first := slices.Index(ops, string(fleettest.EKSUpdateNodegroupVersion))
		for _, gateRead := range []fleettest.Operation{fleettest.CloudWatchGetMetricData, fleettest.ServiceQuotasGetServiceQuota} {
			last := -1
			for i, op := range ops {
				if op == string(gateRead) {
					last = i
				}
			}
			if last < 0 || last > first {
				t.Errorf("%s at %d, the first roll at %d: the gate must end before any roll", gateRead, last, first)
			}
		}
		// One list of the budgets: the gate ran once, for both targets.
		if n := strings.Count(strings.Join(kubeOps(srv), " "), string(fleettest.KubeListPDBs)); n != 1 {
			t.Errorf("%d lists of the budgets, want 1", n)
		}
		if got := res.Plan.Clusters[0].Checks; len(got) == 0 {
			t.Error("the plan has no gate results")
		}
	})
}

// Safety rule 3. The payload that is sent equals the plan that was shown:
// the target, the version, and the force.
func TestRoll_PayloadEqualsPlan(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprintf("force=%v", force), func(t *testing.T) {
			e := newEnv(t, rollWorld(func(_ *fleettest.State, c *fleettest.Cluster) {
				// A nodegroup behind the control plane: its roll must pin
				// its own version, and not the one of the cluster.
				c.Version = "1.33"
			}))
			req := rollOf()
			req.Policy.Force = force
			shown, err := NewPlan(testContext(t), e.deps, req)
			if err != nil {
				t.Fatal(err)
			}
			res, err := Run(testContext(t), e.deps, req, Hooks{})
			if err != nil || res.ExitCode() != 0 {
				t.Fatalf("run: %v, exit %d (%v)", err, res.ExitCode(), res.Err)
			}
			var want []sentRoll
			for _, c := range shown.Document().Clusters {
				for _, s := range c.Steps {
					if s.Action == Submit {
						want = append(want, sentRoll{Cluster: c.Cluster, Nodegroup: s.Name, Version: s.To, Force: s.Force})
					}
				}
			}
			if len(want) != 2 || want[0].Version != "1.32" || want[0].Force != force {
				t.Fatalf("the plan document has the rolls %+v, want ng-a and ng-b at 1.32 with force %v", want, force)
			}
			if diff := cmp.Diff(want, e.rolls()); diff != "" {
				t.Errorf("the requests against the plan document (-plan +sent):\n%s", diff)
			}
		})
	}
}

// Safety rule 4. A fresh describe of the target comes right before every
// submit, and a target that changed since the plan is not rolled.
func TestRoll_FreshDescribeBeforeEverySubmit(t *testing.T) {
	t.Run("the read before each request", func(t *testing.T) {
		e := newEnv(t, rollWorld(nil))
		if res, err := Run(testContext(t), e.deps, rollOf(), Hooks{}); err != nil || res.ExitCode() != 0 {
			t.Fatalf("run: %v, exit %d", err, res.ExitCode())
		}
		reqs := e.srv.Requests()
		rolled := 0
		for i, r := range reqs {
			if r.Operation != fleettest.EKSUpdateNodegroupVersion {
				continue
			}
			rolled++
			prev := reqs[i-1]
			if prev.Operation != fleettest.EKSDescribeNodegroup || prev.Input["nodegroupName"] != r.Input["nodegroupName"] {
				t.Errorf("before the roll of %v came %s %v, want a describe of that nodegroup", r.Input["nodegroupName"], prev.Operation, prev.Input["nodegroupName"])
			}
		}
		if rolled != 2 {
			t.Errorf("%d rolls, want 2", rolled)
		}
	})
	// Each case changes ng-a after the plan read it, while the gate runs.
	tests := []struct {
		name   string
		change func(t *testing.T, w *fleettest.World)
		status StepStatus
		// inFlight is the type of the update that a Busy row names.
		inFlight string
		exit     int
		message  string
	}{
		{name: "in flight", status: StepBusy, inFlight: "ConfigUpdate", exit: exitcode.Incomplete, message: "nodegroup ng-a is UPDATING (ConfigUpdate)",
			change: func(t *testing.T, w *fleettest.World) {
				t.Helper()
				three := int32(3)
				if _, err := w.UpdateNodegroupConfig(east, "prod", "ng-a", fleettest.NodegroupConfigRequest{Scaling: &fleettest.ScalingRequest{Desired: &three}}); err != nil {
					t.Errorf("scaling ng-a: %v", err)
				}
			}},
		{name: "another version", status: StepFailed, exit: exitcode.Incomplete, message: "changed to Kubernetes 1.33 since the plan, which pins 1.32",
			change: func(_ *testing.T, w *fleettest.World) {
				w.Edit(func(s *fleettest.State) { s.Cluster(east, "prod").Nodegroup("ng-a").Version = "1.33" })
			}},
		{name: "not ACTIVE", status: StepBlocked, exit: exitcode.Blocked, message: "nodegroup ng-a is DEGRADED, not ACTIVE",
			change: func(_ *testing.T, w *fleettest.World) {
				w.Edit(func(s *fleettest.State) { s.Cluster(east, "prod").Nodegroup("ng-a").Status = fleettest.StatusDegraded })
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, rollWorld(func(s *fleettest.State, _ *fleettest.Cluster) {
				s.DefaultUpdate = fleettest.UpdateScript{After: 30 * time.Second}
			}))
			e.onRequest(fleettest.ServiceQuotasGetServiceQuota, func(n int) {
				if n == 1 {
					tt.change(t, e.world)
				}
			})
			req := rollOf()
			req.SkipVerify = true
			res, err := Run(testContext(t), e.deps, req, Hooks{})
			if err != nil {
				t.Fatal(err)
			}
			// ng-a is left alone, and ng-b goes on.
			if diff := cmp.Diff([]sentRoll{{"prod", "ng-b", "1.32", false}}, e.rolls()); diff != "" {
				t.Errorf("rolls (-want +got):\n%s", diff)
			}
			a := res.Clusters[0].Steps[0]
			if a.Status != tt.status || a.InFlight != tt.inFlight || a.Failure == nil || !strings.Contains(a.Failure.Error, tt.message) {
				t.Errorf("ng-a = %s, in flight %q, failure %+v; want %s, %q, %q", a.Status, a.InFlight, a.Failure, tt.status, tt.inFlight, tt.message)
			}
			if got := statuses(res)["ng-b"]; got != StepSucceeded {
				t.Errorf("ng-b = %s, want Succeeded", got)
			}
			if res.ExitCode() != tt.exit {
				t.Errorf("exit = %d, want %d", res.ExitCode(), tt.exit)
			}
		})
	}
}

// The fresh read before a submit decides again with everything that the
// decision table read and one describe returns. A nodegroup that someone
// else rolled since the plan is on another AMI release and ACTIVE: it is
// not rolled a second time, with and without --reroll, and neither is one
// that has a custom AMI now. One that did not change is rolled.
func TestRoll_FreshReadDecidesAgain(t *testing.T) {
	ng := func(s *fleettest.State, name string) *fleettest.Nodegroup {
		return s.Cluster(east, "prod").Nodegroup(name)
	}
	tests := []struct {
		name string
		// change runs after the plan read the cluster, while the gate runs.
		change func(s *fleettest.State)
		edit   func(r *RollRequest)
		rolls  []sentRoll
		// skipped is the nodegroup that the fresh read leaves alone, with
		// the reason that its step of the plan has and keeps, and say a
		// text of the notice and of the note of its result.
		skipped string
		reason  Reason
		say     string
		status  RunStatus
	}{
		{name: "unchanged", change: func(*fleettest.State) {}, status: Succeeded,
			rolls: []sentRoll{{"prod", "ng-a", "1.32", false}, {"prod", "ng-b", "1.32", false}}},
		{name: "rolled elsewhere", status: Succeeded,
			change:  func(s *fleettest.State) { ng(s, "ng-a").ReleaseVersion = "1.32.0-20260101" },
			rolls:   []sentRoll{{"prod", "ng-b", "1.32", false}},
			skipped: "ng-a", reason: AMIOutdated,
			say: "nodegroup ng-a of prod runs the AMI release 1.32.0-20260101, and the plan read 1.32.0-20250101: it was rolled elsewhere since the plan, and is not rolled again"},
		{name: "rolled elsewhere, with --reroll", status: NothingToDo,
			edit:    func(r *RollRequest) { r.Nodegroups, r.Reroll = []string{"ng-c"}, true },
			change:  func(s *fleettest.State) { ng(s, "ng-c").ReleaseVersion = "1.32.0-20270101" },
			skipped: "ng-c", reason: Reroll, say: "it was rolled elsewhere since the plan, and is not rolled again"},
		{name: "a custom AMI now", status: Succeeded,
			change:  func(s *fleettest.State) { ng(s, "ng-a").AMIType = amiTypeCustom },
			rolls:   []sentRoll{{"prod", "ng-b", "1.32", false}},
			skipped: "ng-a", reason: AMIOutdated, say: "nodegroup ng-a of prod has a custom AMI (AMI type CUSTOM) since the plan, and is not rolled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, rollWorld(nil))
			e.onRequest(fleettest.ServiceQuotasGetServiceQuota, func(n int) {
				if n == 1 {
					e.world.Edit(tt.change)
				}
			})
			req := rollOf()
			req.SkipVerify = true
			if tt.edit != nil {
				tt.edit(&req)
			}
			s := &script{t: t}
			res, err := Run(testContext(t), e.deps, req, s.hooks())
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.rolls, e.rolls()); diff != "" {
				t.Errorf("rolls (-want +got):\n%s", diff)
			}
			if res.ExitCode() != exitcode.OK || res.Status != tt.status {
				t.Errorf("status = %s, exit %d (%v); want %s, exit 0", res.Status, res.ExitCode(), res.Err, tt.status)
			}
			if tt.skipped == "" {
				return
			}
			if got := statuses(res)[tt.skipped]; got != StepSkipped {
				t.Errorf("%s = %s, want Skipped", tt.skipped, got)
			}
			leftAlone(t, res, s.events, tt.skipped, tt.reason, tt.say)
		})
	}
}

// leftAlone holds the result of a run to a nodegroup that the fresh read
// before its request left alone: its step of the plan is unchanged, with
// the reason that the plan had, and its result says what the read found,
// in the events, in its note, and in the document.
func leftAlone(t *testing.T, res Result, events []string, name string, reason Reason, say string) {
	t.Helper()
	// The plan stays the plan that was confirmed: the step still
	// says Submit, and the result of the step says what the fresh
	// read found.
	cp := &res.Plan.Clusters[0]
	for _, step := range cp.Steps() {
		if step.Ref.Name == name && (step.Action != Submit || step.Reason != reason) {
			t.Errorf("the step of %s in the plan is %s, %s; want it unchanged: Submit, %s", name, step.Action, step.Reason, reason)
		}
	}
	if slices.ContainsFunc(cp.Notices, func(n string) bool { return strings.Contains(n, say) }) {
		t.Errorf("the run wrote into the notices of the plan: %q", cp.Notices)
	}
	note := ""
	for _, row := range res.Clusters[0].Steps {
		if row.Ref.Name == name {
			note = row.Note
		}
	}
	if said := strings.Join(events, "\n"); !strings.Contains(said, say) || !strings.Contains(note, say) {
		t.Errorf("the events and the note of the result do not both say %q:\n%s\n%q", say, said, note)
	}
	doc, ok := res.Document().(apidoc.Run)
	if !ok {
		t.Fatalf("document = %T, want a Run", res.Document())
	}
	for i, row := range doc.Clusters[0].Results {
		if row.Name == name && (row.Note == "" || doc.Clusters[0].Steps[i].Action != Submit) {
			t.Errorf("the document has the step %+v and the result %+v; want the plan as confirmed and a note", doc.Clusters[0].Steps[i], row)
		}
	}
}

// Safety rule 6. A response that is lost after EKS accepted the roll is
// retried with the same token, so one update starts.
func TestRoll_LostResponseStartsOneUpdate(t *testing.T) {
	world := rollWorld(nil)
	world.SetFault(fleettest.EKSUpdateNodegroupVersion, fleettest.Fault{LoseResponse: 1})
	e := newEnv(t, world)
	res, err := Run(testContext(t), e.deps, rollOf("ng-a"), Hooks{})
	if err != nil || res.ExitCode() != 0 {
		t.Fatalf("run: %v, exit %d (%v)", err, res.ExitCode(), res.Err)
	}
	var tokens []string
	for _, r := range e.srv.Requests() {
		if r.Operation == fleettest.EKSUpdateNodegroupVersion {
			tokens = append(tokens, fmt.Sprint(r.Input["clientRequestToken"]))
		}
	}
	if len(tokens) != 2 || tokens[0] != tokens[1] || tokens[0] == "" || tokens[0] == "<nil>" {
		t.Errorf("tokens = %v, want the same token twice", tokens)
	}
	e.world.View(func(s *fleettest.State) {
		if n := len(s.Cluster(east, "prod").Updates); n != 1 {
			t.Errorf("%d updates in the world, want 1", n)
		}
	})
}

// The start call, as the old tests held it: no request without a version,
// the version and the force in the request, and a new token for each call.
func TestStartNodegroupRoll(t *testing.T) {
	e := newEnv(t, rollWorld(nil))
	clients := e.deps.AWS.Clients(east)
	target := Target{Region: east, Cluster: "prod"}
	t.Run("refuses an empty version", func(t *testing.T) {
		if _, err := StartNodegroupRoll(testContext(t), clients, target, "ng-a", "", false); err == nil {
			t.Fatal("no error for a roll with no version")
		}
		if got := e.rolls(); len(got) != 0 {
			t.Errorf("requests = %v, want none", got)
		}
	})
	t.Run("a new token per call", func(t *testing.T) {
		for _, ng := range []string{"ng-a", "ng-b"} {
			if id, err := StartNodegroupRoll(testContext(t), clients, target, ng, "1.32", ng == "ng-b"); err != nil || id == "" {
				t.Fatalf("roll of %s: id %q, %v", ng, id, err)
			}
		}
		if diff := cmp.Diff([]sentRoll{{"prod", "ng-a", "1.32", false}, {"prod", "ng-b", "1.32", true}}, e.rolls()); diff != "" {
			t.Errorf("rolls (-want +got):\n%s", diff)
		}
		var tokens []string
		for _, r := range e.srv.Requests() {
			if r.Operation == fleettest.EKSUpdateNodegroupVersion {
				tokens = append(tokens, fmt.Sprint(r.Input["clientRequestToken"]))
				// force is sent only when it is true.
				if _, has := r.Input["force"]; has != (r.Input["nodegroupName"] == "ng-b") {
					t.Errorf("the roll of %v: force in the request = %v", r.Input["nodegroupName"], has)
				}
			}
		}
		if len(tokens) != 2 || tokens[0] == tokens[1] {
			t.Errorf("tokens = %v, want two different ones", tokens)
		}
	})
}

// A plan step of a nodegroup with no Kubernetes version is blocked: the
// roll could not be pinned.
func TestRoll_NoVersionIsBlocked(t *testing.T) {
	cp := &ClusterPlan{Target: Target{Cluster: "prod"}, snapshot: &fleet.Snapshot{Nodegroups: []fleet.Nodegroup{
		{Name: "ng-a", State: fleet.Collected, Status: "ACTIVE", AMI: fleet.AMI{State: fleet.Collected, Current: "a", Latest: "b"}},
	}}}
	step := cp.rollStep(PhaseRef{}, "ng-a", RollRequest{})
	if step.Action != Blocked || len(cp.Blockers) != 1 || cp.Blockers[0].Kind != BlockerUnread || len(cp.Failures) != 1 {
		t.Errorf("step = %+v, blockers %+v, failures %+v; want a Blocked step with an Unread blocker and a failure", step, cp.Blockers, cp.Failures)
	}
}

// Safety rule 9. A wait that outlasts --wait-timeout exits 4 with
// InProgress rows, and an interrupt exits 1.
func TestRoll_DeadlineAndInterrupt(t *testing.T) {
	never := func(s *fleettest.State, _ *fleettest.Cluster) { s.DefaultUpdate = fleettest.UpdateScript{Never: true} }
	t.Run("deadline", func(t *testing.T) {
		e := newEnv(t, rollWorld(never))
		req := rollOf()
		req.Limits.Wait = 2 * time.Minute
		res, err := Run(testContext(t), e.deps, req, Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		if res.ExitCode() != exitcode.Incomplete || res.Status != Incomplete {
			t.Errorf("status = %s, exit %d, want Incomplete, exit 4", res.Status, res.ExitCode())
		}
		for _, name := range []string{"ng-a", "ng-b"} {
			s := res.Clusters[0].Steps[slices.IndexFunc(res.Clusters[0].Steps, func(s StepResult) bool { return s.Ref.Name == name })]
			if s.Status != StepInProgress || s.Failure == nil || s.Failure.Reason != apidoc.ReasonTimeout {
				t.Errorf("%s = %s, failure %+v; want InProgress with the reason Timeout", name, s.Status, s.Failure)
			}
		}
		if len(res.Clusters[0].Verification) != 0 {
			t.Error("a run that timed out was verified")
		}
	})
	t.Run("interrupt", func(t *testing.T) {
		e := newEnv(t, rollWorld(never))
		ctx, cancel := context.WithCancel(testContext(t))
		defer cancel()
		// The third poll is the handshake: the wait is under way.
		e.onRequest(fleettest.EKSDescribeUpdate, func(n int) {
			if n == 3 {
				cancel()
			}
		})
		res, err := Run(ctx, e.deps, rollOf(), Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		if res.ExitCode() != exitcode.Failure || res.Status != Interrupted || !errors.Is(res.Err, context.Canceled) {
			t.Errorf("status = %s, exit %d, err %v; want Interrupted, exit 1", res.Status, res.ExitCode(), res.Err)
		}
		for _, s := range res.Clusters[0].Steps[:2] {
			if s.Status != StepInProgress || s.Failure == nil || s.Failure.Reason != apidoc.ReasonInterrupted {
				t.Errorf("%s = %s, failure %+v; want InProgress with the reason Interrupted", s.Ref.Name, s.Status, s.Failure)
			}
		}
	})
}

// The wait of a batch: an update that fails does not end the wait for the
// other, a submit that EKS refuses does not stop the next one, and one
// failed roll beside one that succeeded is a partial failure.
func TestRoll_BatchGoesOnAfterAFailure(t *testing.T) {
	t.Run("an update fails", func(t *testing.T) {
		e := newEnv(t, rollWorld(func(_ *fleettest.State, c *fleettest.Cluster) {
			c.Nodegroup("ng-a").UpdateScripts = []fleettest.UpdateScript{{After: 15 * time.Second, Result: fleettest.UpdateFailed}}
			c.Nodegroup("ng-b").UpdateScripts = []fleettest.UpdateScript{{After: 60 * time.Second}}
		}))
		res, err := Run(testContext(t), e.deps, rollOf(), Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]StepStatus{"ng-a": StepFailed, "ng-b": StepSucceeded, "ng-c": StepSkipped}
		if diff := cmp.Diff(want, statuses(res)); diff != "" {
			t.Errorf("statuses (-want +got):\n%s", diff)
		}
		if res.ExitCode() != exitcode.Incomplete {
			t.Errorf("exit = %d, want 4: one failed beside one that succeeded", res.ExitCode())
		}
	})
	t.Run("a submit is refused", func(t *testing.T) {
		world := rollWorld(nil)
		e := newEnv(t, world)
		// EKS refuses the first roll, and takes the second.
		e.onRequest(fleettest.EKSUpdateNodegroupVersion, func(n int) {
			if n == 1 {
				world.SetFault(fleettest.EKSUpdateNodegroupVersion, fleettest.Fault{Code: "InvalidRequestException"})
			} else {
				world.SetFault(fleettest.EKSUpdateNodegroupVersion, fleettest.Fault{})
			}
		})
		res, err := Run(testContext(t), e.deps, rollOf(), Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		if got := statuses(res); got["ng-b"] != StepSucceeded || got["ng-a"] != StepFailed {
			t.Errorf("statuses = %v, want ng-a Failed and ng-b Succeeded", got)
		}
	})
}

// --no-wait ends the batch after the submits: every roll is Started, and
// nothing is polled or verified.
func TestRoll_NoWait(t *testing.T) {
	e := newEnv(t, rollWorld(nil))
	req := rollOf()
	req.NoWait = true
	res, err := Run(testContext(t), e.deps, req, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != Started || res.ExitCode() != 0 {
		t.Errorf("status = %s, exit %d, want Started, exit 0", res.Status, res.ExitCode())
	}
	if n := e.count(fleettest.EKSDescribeUpdate); n != 0 {
		t.Errorf("%d polls, want none", n)
	}
	if len(e.rolls()) != 2 || len(res.Clusters[0].Verification) != 0 {
		t.Errorf("rolls = %v, verification = %v", e.rolls(), res.Clusters[0].Verification)
	}
}

// After a person answers, the plan is computed again and compared: a new
// AMI asks again, and a target that is done needs no roll.
func TestRoll_PlanAgainAfterAnAnswer(t *testing.T) {
	interactive := rollOf("ng-a")
	interactive.Policy = health.Policy{Skip: true}
	t.Run("a new AMI asks again", func(t *testing.T) {
		e := newEnv(t, rollWorld(nil))
		s := &script{t: t, answers: []bool{true, true}, onAsk: func(i int) {
			if i > 0 {
				return
			}
			// AWS publishes a new AMI: the parameter that the plan read
			// now names another image.
			var path string
			for _, r := range e.srv.Requests() {
				if r.Operation == fleettest.SSMGetParameter {
					path = fmt.Sprint(r.Input["Name"])
				}
			}
			e.world.Edit(func(s *fleettest.State) {
				s.Parameters = append(s.Parameters, fleettest.Parameter{Name: path, Value: "ami-newer"})
			})
		}}
		res, err := Run(testContext(t), e.deps, interactive, s.hooks())
		if err != nil || res.ExitCode() != 0 {
			t.Fatalf("run: %v, exit %d (%v)", err, res.ExitCode(), res.Err)
		}
		if len(s.asked) != 2 || s.asked[1].Kind != PlanChanged || !strings.Contains(s.asked[1].Text, "ami-newer") {
			t.Errorf("questions = %+v, want a second one about the AMI ami-newer", s.asked)
		}
		if len(e.rolls()) != 1 {
			t.Errorf("rolls = %v, want one", e.rolls())
		}
	})
	t.Run("a second no sends nothing", func(t *testing.T) {
		e := newEnv(t, rollWorld(nil))
		s := &script{t: t, answers: []bool{true, false}, onAsk: func(i int) {
			if i == 0 {
				e.world.Edit(func(s *fleettest.State) {
					for _, r := range e.srv.Requests() {
						if r.Operation == fleettest.SSMGetParameter {
							s.Parameters = append(s.Parameters, fleettest.Parameter{Name: fmt.Sprint(r.Input["Name"]), Value: "ami-newer"})
							return
						}
					}
				})
			}
		}}
		res, err := Run(testContext(t), e.deps, interactive, s.hooks())
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != Declined || len(e.mutations()) != 0 {
			t.Errorf("status = %s, mutations %v; want Declined and none", res.Status, e.mutations())
		}
	})
}

// A selection keeps the nodegroups that it matches, the plan says which,
// and one that matches nothing is an error.
func TestRoll_Selection(t *testing.T) {
	e := newEnv(t, rollWorld(nil))
	req := rollOf()
	req.Policy.Skip = true
	req.Select = &Selector{Text: "-f name=ng-b", Match: func(n fleet.Nodegroup) bool { return n.Name == "ng-b" }}
	p, err := NewPlan(testContext(t), e.deps, req)
	if err != nil {
		t.Fatal(err)
	}
	steps := p.Clusters[0].Steps()
	if len(steps) != 1 || steps[0].Ref.Name != "ng-b" || !slices.Contains(p.Clusters[0].Notices, "-f name=ng-b selects the nodegroups ng-b") {
		t.Errorf("steps = %+v, notices = %v", steps, p.Clusters[0].Notices)
	}
	req.Select = &Selector{Text: "-f name=zz", Match: func(fleet.Nodegroup) bool { return false }}
	var none *NoMatchError
	if _, err := NewPlan(testContext(t), e.deps, req); !errors.As(err, &none) || len(none.Names) != 3 {
		t.Errorf("error = %v, want a NoMatchError with the three nodegroups", err)
	}
	var missing *NotFoundError
	if _, err := NewPlan(testContext(t), e.deps, rollOf("ng-z")); !errors.As(err, &missing) {
		t.Errorf("error = %v, want a NotFoundError", err)
	}
}

// A reviewed plan is consent to its own request: a roll with another
// flag is another request.
func TestRollRequest_Differs(t *testing.T) {
	base := rollOf("ng-a")
	sel := &Selector{Text: "-f name=a"}
	tests := []struct {
		name string
		edit func(r *RollRequest)
		want string
	}{
		{"the same", func(*RollRequest) {}, ""},
		{"yes", func(r *RollRequest) { r.Policy.Yes = false }, ""},
		{"skip verify", func(r *RollRequest) { r.SkipVerify = true }, ""},
		{"nodegroups", func(r *RollRequest) { r.Nodegroups = []string{"ng-b"} }, "the nodegroups"},
		{"filter", func(r *RollRequest) { r.Select = sel }, "the filter"},
		{"reroll", func(r *RollRequest) { r.Reroll = true }, "--reroll"},
		{"force", func(r *RollRequest) { r.Policy.Force = true }, "--force"},
		{"cluster", func(r *RollRequest) { r.Cluster = "staging" }, "the cluster"},
		{"fleet", func(r *RollRequest) { r.Fleet = &FleetScope{} }, "--all-clusters"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			other := base
			tt.edit(&other)
			if got := other.differs(base); got != tt.want {
				t.Errorf("differs = %q, want %q", got, tt.want)
			}
		})
	}
	if got := base.differs(ScaleRequest{}); got != "the kind of change" {
		t.Errorf("differs from a scale = %q", got)
	}
}
