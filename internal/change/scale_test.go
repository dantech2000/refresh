package change

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"k8s.io/client-go/kubernetes"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/kube"
	"github.com/dantech2000/refresh/internal/kube/kubetest"
)

// size returns a size that a request gives.
func size(n int32) *int32 { return &n }

// TestValidateSizes is the table of the checks before any read: the
// sizes that were given, against each other.
func TestValidateSizes(t *testing.T) {
	tests := []struct {
		name                  string
		desired, lowest, most *int32
		want                  string
	}{
		{name: "no size", want: "give at least one of --desired, --min, --max"},
		{name: "desired alone", desired: size(3)},
		{name: "min alone", lowest: size(0)},
		{name: "max alone", most: size(5)},
		{name: "all three in order", desired: size(3), lowest: size(1), most: size(5)},
		{name: "all three equal", desired: size(2), lowest: size(2), most: size(2)},
		{name: "desired of zero", desired: size(0), lowest: size(0)},
		{name: "a negative desired", desired: size(-1), want: "--desired must be 0 or more"},
		{name: "a negative min", lowest: size(-1), want: "--min must be 0 or more"},
		{name: "a negative max", most: size(-2), want: "--max must be 0 or more"},
		{name: "a max of zero", most: size(0), want: "--max must be 1 or more"},
		{name: "min above max", lowest: size(5), most: size(2), want: "--min 5 is above --max 2"},
		{name: "desired below min", desired: size(1), lowest: size(2), want: "--desired 1 is below --min 2"},
		{name: "desired above max", desired: size(5), most: size(3), want: "--desired 5 is above --max 3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSizes(tt.desired, tt.lowest, tt.most)
			if got := errText(err); got != tt.want {
				t.Errorf("error = %q, want %q", got, tt.want)
			}
		})
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// TestScaleRequestResolve is the table of the checks after the read: the
// sizes that were not given come from the nodegroup, and the result must
// hold. The first two rows are the old bounds check with its hint.
func TestScaleRequestResolve(t *testing.T) {
	cur := Sizes{Desired: 3, Min: 1, Max: 5}
	tests := []struct {
		name                  string
		desired, lowest, most *int32
		want                  Sizes
		wantErr               string
	}{
		{name: "max alone below the current desired", most: size(2), wantErr: "--max 2 is below the current desired size 3; pass --desired too"},
		{name: "min alone above the current desired", lowest: size(4), wantErr: "--min 4 is above the current desired size 3; pass --desired too"},
		{name: "min and max that exclude the current desired", lowest: size(4), most: size(6), wantErr: "--min 4 is above the current desired size 3; pass --desired too"},
		{name: "desired above the current max", desired: size(9), wantErr: "--desired 9 is above the current max size 5; pass --max too"},
		{name: "desired below the current min", desired: size(0), wantErr: "--desired 0 is below the current min size 1; pass --min too"},
		{name: "min above the current max", desired: size(7), lowest: size(6), wantErr: "--min 6 is above the current max size 5; pass --max too"},
		{name: "max below the current min", desired: size(0), most: size(0), wantErr: "--max 0 is below the current min size 1; pass --min too"},
		{name: "desired alone", desired: size(4), want: Sizes{Desired: 4, Min: 1, Max: 5}},
		{name: "desired at the current max", desired: size(5), want: Sizes{Desired: 5, Min: 1, Max: 5}},
		{name: "max alone at the current desired", most: size(3), want: Sizes{Desired: 3, Min: 1, Max: 3}},
		{name: "min alone at the current desired", lowest: size(3), want: Sizes{Desired: 3, Min: 3, Max: 5}},
		{name: "desired and max above the current max", desired: size(9), most: size(10), want: Sizes{Desired: 9, Min: 1, Max: 10}},
		{name: "all three", desired: size(0), lowest: size(0), most: size(1), want: Sizes{Desired: 0, Min: 0, Max: 1}},
		{name: "the current sizes", desired: size(3), lowest: size(1), most: size(5), want: cur},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ScaleRequest{Desired: tt.desired, Min: tt.lowest, Max: tt.most}.resolve(cur)
			if errText(err) != tt.wantErr || got != tt.want {
				t.Errorf("resolve = %+v, %q; want %+v, %q", got, errText(err), tt.want, tt.wantErr)
			}
		})
	}
}

// TestDecideScale is the step table of a scale, row for row.
func TestDecideScale(t *testing.T) {
	cur := Sizes{Desired: 2, Min: 1, Max: 3}
	same := ScaleSizes{Before: cur, After: cur}
	tests := []struct {
		name    string
		status  string
		req     ScaleRequest
		want    scaleDecision
		wantErr string
	}{
		{name: "1: UPDATING is busy", status: "UPDATING", req: ScaleRequest{Desired: size(3)}, want: scaleDecision{Action: Skip, Reason: InFlight, Sizes: same}},
		{name: "1: CREATING is busy", status: "CREATING", req: ScaleRequest{Desired: size(3)}, want: scaleDecision{Action: Skip, Reason: InFlight, Sizes: same}},
		{name: "1: DELETING is busy", status: "DELETING", req: ScaleRequest{Desired: size(1)}, want: scaleDecision{Action: Skip, Reason: InFlight, Sizes: same}},
		{name: "1: busy comes before the sizes", status: "UPDATING", req: ScaleRequest{Desired: size(9)}, want: scaleDecision{Action: Skip, Reason: InFlight, Sizes: same}},
		{name: "3: sizes that cannot hold", status: "ACTIVE", req: ScaleRequest{Desired: size(9)}, wantErr: "--desired 9 is above the current max size 3; pass --max too"},
		{name: "4: the current desired size", status: "ACTIVE", req: ScaleRequest{Desired: size(2)}, want: scaleDecision{Action: Skip, Reason: NoChange, Sizes: same}},
		{name: "4: every current size", status: "ACTIVE", req: ScaleRequest{Desired: size(2), Min: size(1), Max: size(3)}, want: scaleDecision{Action: Skip, Reason: NoChange, Sizes: same}},
		{name: "5: a scale-up", status: "ACTIVE", req: ScaleRequest{Desired: size(3)},
			want: scaleDecision{Action: Submit, Sizes: ScaleSizes{Before: cur, After: Sizes{Desired: 3, Min: 1, Max: 3}}}},
		{name: "5: a scale-down", status: "ACTIVE", req: ScaleRequest{Desired: size(1)},
			want: scaleDecision{Action: Submit, Sizes: ScaleSizes{Before: cur, After: Sizes{Desired: 1, Min: 1, Max: 3}}}},
		{name: "5: the bounds alone", status: "ACTIVE", req: ScaleRequest{Min: size(2), Max: size(6)},
			want: scaleDecision{Action: Submit, Sizes: ScaleSizes{Before: cur, After: Sizes{Desired: 2, Min: 2, Max: 6}}}},
		{name: "2: DEGRADED blocks a scale-up", status: "DEGRADED", req: ScaleRequest{Desired: size(3)},
			want: scaleDecision{Action: Blocked, Sizes: same, NotActive: "nodegroup web is DEGRADED, not ACTIVE; it was not scaled"}},
		{name: "2: DEGRADED blocks a scale-down", status: "DEGRADED", req: ScaleRequest{Desired: size(1)},
			want: scaleDecision{Action: Blocked, Sizes: same, NotActive: "nodegroup web is DEGRADED, not ACTIVE; it was not scaled"}},
		{name: "2: DEGRADED blocks the bounds alone", status: "DEGRADED", req: ScaleRequest{Min: size(2), Max: size(6)},
			want: scaleDecision{Action: Blocked, Sizes: same, NotActive: "nodegroup web is DEGRADED, not ACTIVE; it was not scaled"}},
		{name: "2: CREATE_FAILED blocks", status: "CREATE_FAILED", req: ScaleRequest{Desired: size(3)},
			want: scaleDecision{Action: Blocked, Sizes: same, NotActive: "nodegroup web is CREATE_FAILED, not ACTIVE; it was not scaled"}},
		{name: "2: DELETE_FAILED blocks", status: "DELETE_FAILED", req: ScaleRequest{Desired: size(3)},
			want: scaleDecision{Action: Blocked, Sizes: same, NotActive: "nodegroup web is DELETE_FAILED, not ACTIVE; it was not scaled"}},
		{name: "2: not ACTIVE comes before the sizes", status: "DEGRADED", req: ScaleRequest{Desired: size(9)},
			want: scaleDecision{Action: Blocked, Sizes: same, NotActive: "nodegroup web is DEGRADED, not ACTIVE; it was not scaled"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decideScale(fleet.Nodegroup{Name: "web", Status: tt.status, Desired: 2, Min: 1, Max: 3}, tt.req)
			if errText(got.Err) != tt.wantErr {
				t.Fatalf("error = %q, want %q", errText(got.Err), tt.wantErr)
			}
			got.Err = nil
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("decision (-want +got):\n%s", diff)
			}
		})
	}
}

// TestScaleSettled is the confirmation of a scale after its update
// reported Successful: the sizes and the status together.
func TestScaleSettled(t *testing.T) {
	at := func(status string, desired, lowest, most int32) Settled {
		return Settled{Status: status, Sizes: Sizes{Desired: desired, Min: lowest, Max: most}}
	}
	tests := []struct {
		name     string
		req      ScaleRequest
		got      Settled
		wantDone bool
		wantErr  string
	}{
		{name: "the desired size and ACTIVE", req: ScaleRequest{Desired: size(3)}, got: at("ACTIVE", 3, 1, 3), wantDone: true},
		{name: "the desired size waits for ACTIVE", req: ScaleRequest{Desired: size(3)}, got: at("UPDATING", 3, 1, 3)},
		{name: "a desired size that does not match fails at once", req: ScaleRequest{Desired: size(3)}, got: at("UPDATING", 2, 1, 3),
			wantErr: "update u-1 of nodegroup web reported Successful, but the desired size of the nodegroup is 2, not 3"},
		{name: "a min size that does not match", req: ScaleRequest{Desired: size(3), Min: size(2)}, got: at("ACTIVE", 3, 1, 3),
			wantErr: "update u-1 of nodegroup web reported Successful, but the min size of the nodegroup is 1, not 2"},
		{name: "a max size that does not match", req: ScaleRequest{Max: size(6)}, got: at("ACTIVE", 3, 1, 3),
			wantErr: "update u-1 of nodegroup web reported Successful, but the max size of the nodegroup is 3, not 6"},
		{name: "a size that was not sent is not compared", req: ScaleRequest{Desired: size(3)}, got: at("ACTIVE", 3, 0, 9), wantDone: true},
		{name: "DEGRADED fails", req: ScaleRequest{Desired: size(3)}, got: at("DEGRADED", 3, 1, 3), wantErr: "nodegroup web is DEGRADED after the scale to 3 nodes"},
		{name: "DELETING fails", req: ScaleRequest{Desired: size(3)}, got: at("DELETING", 3, 1, 3), wantErr: "nodegroup web is DELETING after the scale to 3 nodes"},
		{name: "the bounds alone are done at the first answer", req: ScaleRequest{Min: size(1), Max: size(6)}, got: at("UPDATING", 2, 1, 6), wantDone: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			done, err := scaleSettled("web", "u-1", tt.req)(tt.got)
			if done != tt.wantDone || errText(err) != tt.wantErr {
				t.Errorf("done = %v, %q; want %v, %q", done, errText(err), tt.wantDone, tt.wantErr)
			}
		})
	}
}

// scaleLimits are the limits of the scale runs of these tests.
var scaleLimits = Limits{Read: time.Minute, Wait: 5 * time.Minute, Poll: 15 * time.Second}

// scaleTo returns a request to scale web, which has desired 2, min 1, and
// max 3, to a desired size, with --yes.
func scaleTo(desired int32) ScaleRequest {
	return ScaleRequest{
		Common: Common{Limits: scaleLimits, Policy: health.Policy{Yes: true}},
		Target: Target{Cluster: "prod"}, Nodegroup: "web", Desired: size(desired),
	}
}

// scaleWorld returns the world of newWorld with what a scale-down reads:
// the two nodes of web, a pod of one workload on each, and a budget that
// lets one of the two pods go.
func scaleWorld(edit func(c *fleettest.Cluster)) *fleettest.World {
	return newWorld(func(c *fleettest.Cluster) {
		app := map[string]string{"app": "shop"}
		c.Nodes = []*fleettest.Node{{Name: "web-1", Nodegroup: "web"}, {Name: "web-2", Nodegroup: "web"}}
		c.Pods = []*fleettest.Pod{
			{Namespace: "default", Name: "shop-1", Node: "web-1", Labels: app},
			{Namespace: "default", Name: "shop-2", Node: "web-2", Labels: app},
		}
		c.PDBs = []fleettest.PDB{{Namespace: "default", Name: "shop", Selector: app, MinAvailable: 1, DisruptionsAllowed: 1, CurrentHealthy: 2, DesiredHealthy: 1, ExpectedPods: 2}}
		if edit != nil {
			edit(c)
		}
	})
}

// tightBudget makes the budget of the scale world allow no disruption.
func tightBudget(c *fleettest.Cluster) {
	c.PDBs[0].MinAvailable, c.PDBs[0].DesiredHealthy, c.PDBs[0].DisruptionsAllowed = 2, 2, 0
}

// withKube gives the cluster of the env a Kubernetes API, and the
// dependencies a client that reaches it.
func (e *env) withKube(t *testing.T) *kubetest.Server {
	t.Helper()
	srv := kubetest.NewServer(e.world, east, "prod")
	t.Cleanup(srv.Close)
	client, err := kubernetes.NewForConfig(srv.Config())
	if err != nil {
		t.Fatal(err)
	}
	e.deps.Kube = func(kube.Target) (*kube.Client, error) { return &kube.Client{Clientset: client}, nil }
	return srv
}

// kubeOps returns the operations that the Kubernetes API got.
func kubeOps(srv *kubetest.Server) []string {
	var out []string
	for _, r := range srv.Requests() {
		out = append(out, string(r.Operation))
	}
	slices.Sort(out)
	return out
}

// web returns the nodegroup of the world as "status desired/min/max".
func (e *env) web() string {
	var out string
	e.world.View(func(s *fleettest.State) {
		n := s.Cluster(east, "prod").Nodegroup("web")
		out = fmt.Sprintf("%s %d/%d/%d", n.Status, n.Desired, n.Min, n.Max)
	})
	return out
}

// scaleRequests returns the scalingConfig of each UpdateNodegroupConfig
// request.
func (e *env) scaleRequests() []any {
	var out []any
	for _, r := range e.srv.Requests() {
		if r.Operation == fleettest.EKSUpdateNodegroupConfig {
			out = append(out, r.Input["scalingConfig"])
		}
	}
	return out
}

func checkNames(results []health.Result) []string {
	out := make([]string, len(results))
	for i, r := range results {
		out[i] = r.Name + " " + string(r.Outcome)
	}
	return out
}

// The gate of a scale follows its direction. A scale that lowers the
// desired size reads the nodes and the budgets. Any other scale reads the
// quota and asks nothing of Kubernetes.
func TestPlanScale_Gate(t *testing.T) {
	yes := health.Policy{Yes: true}
	noAccess := func(e *env) {
		e.deps.Kube = func(kube.Target) (*kube.Client, error) {
			return nil, errors.New("no kubeconfig context points at prod")
		}
	}
	tests := []struct {
		name string
		seed func(c *fleettest.Cluster)
		// kube is false for a run with no Kubernetes access.
		kube bool
		req  ScaleRequest
		// policy is what the flags say to the gate. The zero value is a
		// run with a person to ask.
		policy   health.Policy
		checks   []string
		decision Decision
		gate     health.Decision
		exit     int
		kubeOps  []string
	}{
		{
			name: "a scale-up reads the quota and no Kubernetes", kube: true, req: scaleTo(3), seed: tightBudget, policy: yes,
			checks: []string{"Quotas Skipped"}, decision: Proceed, gate: health.Go,
		},
		{
			name: "the bounds alone are not a scale-down", kube: true, req: ScaleRequest{Nodegroup: "web", Min: size(2), Max: size(6)}, seed: tightBudget, policy: yes,
			checks: []string{"Quotas Skipped"}, decision: Proceed, gate: health.Go,
		},
		{
			name: "a scale-down with a budget that has room", kube: true, req: scaleTo(1), policy: yes,
			checks: []string{"Nodes Pass", "DrainBlockers Pass", "Kubernetes Pass"}, decision: Proceed, gate: health.Go,
			kubeOps: []string{"kube:ListNodes", "kube:ListPodDisruptionBudgets", "kube:ListPods"},
		},
		{
			name: "a scale-down that a budget blocks", kube: true, req: scaleTo(1), seed: tightBudget, policy: yes,
			checks: []string{"Nodes Pass", "DrainBlockers Fail", "Kubernetes Pass"}, decision: DecisionBlocked, gate: health.Block, exit: 3,
			kubeOps: []string{"kube:ListNodes", "kube:ListPodDisruptionBudgets", "kube:ListPods"},
		},
		{
			name: "--force goes past the budget", kube: true, req: scaleTo(1), seed: tightBudget, policy: health.Policy{Yes: true, Force: true},
			checks: []string{"Nodes Pass", "DrainBlockers Fail", "Kubernetes Pass"}, decision: Ask, gate: health.Go, exit: 2,
			kubeOps: []string{"kube:ListNodes", "kube:ListPodDisruptionBudgets", "kube:ListPods"},
		},
		{
			name: "a scale-down without Kubernetes access asks", req: scaleTo(1),
			checks: []string{"Nodes Pass", "DrainBlockers Skipped", "Kubernetes Warn"}, decision: Ask, gate: health.Ask, exit: 2,
		},
		{
			name: "--yes accepts the missing access", req: scaleTo(1), policy: yes,
			checks: []string{"Nodes Pass", "DrainBlockers Skipped", "Kubernetes Warn"}, decision: Ask, gate: health.Go, exit: 2,
		},
		{
			name: "--require-healthy does not", req: scaleTo(1), policy: health.Policy{Yes: true, RequireHealthy: true},
			checks: []string{"Nodes Pass", "DrainBlockers Skipped", "Kubernetes Warn"}, decision: DecisionBlocked, gate: health.Block, exit: 3,
		},
		{
			// The step table blocks the target before any gate.
			name: "a target that is not ACTIVE blocks a scale-down", kube: true, req: scaleTo(1), policy: yes,
			seed:     func(c *fleettest.Cluster) { c.Nodegroup("web").Status = fleettest.StatusDegraded },
			decision: DecisionBlocked, exit: 3,
		},
		{
			name: "--skip-health-check runs no check", kube: true, req: scaleTo(1), seed: tightBudget, policy: health.Policy{Yes: true, Skip: true},
			decision: Proceed, gate: health.Go,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, scaleWorld(tt.seed))
			var srv *kubetest.Server
			if tt.kube {
				srv = e.withKube(t)
			} else {
				noAccess(e)
			}
			req := tt.req
			req.Cluster, req.Policy = "prod", tt.policy
			p, err := NewPlan(testContext(t), e.deps, req)
			if err != nil {
				t.Fatal(err)
			}
			cp := &p.Clusters[0]
			if diff := cmp.Diff(tt.checks, checkNames(cp.Checks), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("checks (-want +got):\n%s", diff)
			}
			if cp.Decision() != tt.decision || cp.Gate.Decision != tt.gate || p.ExitCode() != tt.exit {
				t.Errorf("decision %s, gate %s, exit %d; want %s, %s, %d", cp.Decision(), cp.Gate.Decision, p.ExitCode(), tt.decision, tt.gate, tt.exit)
			}
			if srv != nil {
				if diff := cmp.Diff(tt.kubeOps, kubeOps(srv)); diff != "" {
					t.Errorf("Kubernetes requests (-want +got):\n%s", diff)
				}
			}
			if n := e.count(fleettest.EKSUpdateNodegroupConfig); n != 0 {
				t.Errorf("a plan sent %d requests to scale", n)
			}
		})
	}
}

// What a plan says before any gate: a name that does not exist, sizes
// that cannot hold, a scale to the current sizes, and a target that is
// already changing.
func TestPlanScale_Steps(t *testing.T) {
	t.Run("a nodegroup that does not exist", func(t *testing.T) {
		e := newEnv(t, scaleWorld(nil))
		req := scaleTo(3)
		req.Nodegroup = "wbe"
		_, err := NewPlan(testContext(t), e.deps, req)
		var missing *NotFoundError
		if !errors.As(err, &missing) || err.Error() != `nodegroup "wbe" not found in cluster prod` || !slices.Contains(missing.Names, "web") {
			t.Errorf("error = %v, want not found with the names of the cluster", err)
		}
	})
	t.Run("sizes that cannot hold after the read", func(t *testing.T) {
		e := newEnv(t, scaleWorld(nil))
		_, err := NewPlan(testContext(t), e.deps, scaleTo(9))
		if errText(err) != "--desired 9 is above the current max size 3; pass --max too" {
			t.Errorf("error = %v", err)
		}
	})
	t.Run("the current sizes", func(t *testing.T) {
		e := newEnv(t, scaleWorld(nil))
		p, err := NewPlan(testContext(t), e.deps, scaleTo(2))
		if err != nil {
			t.Fatal(err)
		}
		s := p.Clusters[0].Steps()[0]
		if s.Action != Skip || s.Reason != NoChange || p.Decision() != DecisionNothingToDo || p.ExitCode() != 0 || len(p.Clusters[0].Checks) != 0 {
			t.Errorf("step = %s %s, decision %s, exit %d; want Skip NoChange, NothingToDo, 0, and no gate", s.Action, s.Reason, p.Decision(), p.ExitCode())
		}
	})
	t.Run("a target that is already changing", func(t *testing.T) {
		e := newEnv(t, scaleWorld(func(c *fleettest.Cluster) { c.Nodegroup("web").Status = fleettest.StatusUpdating }))
		p, err := NewPlan(testContext(t), e.deps, scaleTo(3))
		if err != nil {
			t.Fatal(err)
		}
		cp := &p.Clusters[0]
		if s := cp.Steps()[0]; !s.busy() || len(cp.Blockers) != 0 {
			t.Errorf("step = %s %s with blockers %v; want a busy target and no refusal", s.Action, s.Reason, cp.Blockers)
		}
		if len(cp.Failures) != 1 || cp.Failures[0].Reason != apidoc.ReasonBusy || cp.Failures[0].Kind != apidoc.FailureNodegroup || p.ExitCode() != 4 {
			t.Errorf("failures = %+v, exit %d; want one Busy failure of the nodegroup and 4", cp.Failures, p.ExitCode())
		}
	})
	t.Run("something else in the cluster is changing", func(t *testing.T) {
		e := newEnv(t, scaleWorld(func(c *fleettest.Cluster) { c.Addon("coredns").Status = fleettest.StatusUpdating }))
		p, err := NewPlan(testContext(t), e.deps, scaleTo(3))
		if err != nil {
			t.Fatal(err)
		}
		cp := &p.Clusters[0]
		if len(cp.Blockers) != 1 || cp.Blockers[0].Kind != BlockerBusy || p.ExitCode() != 3 {
			t.Errorf("blockers = %+v, exit %d; want one Busy blocker and 3", cp.Blockers, p.ExitCode())
		}
	})
}

// A scale-up names the instance types that an availability zone of the
// nodegroup does not offer. The reads are the ones of the old check, and
// one that fails is dropped.
func TestPlanScale_Availability(t *testing.T) {
	seed := func(c *fleettest.Cluster) {
		n := c.Nodegroup("web")
		n.InstanceTypes, n.Subnets = []string{"m5.large", "m7i.large"}, []string{"subnet-a", "subnet-b"}
	}
	offer := func(w *fleettest.World, offerings ...fleettest.Offering) {
		w.Edit(func(s *fleettest.State) {
			s.Subnets = []fleettest.Subnet{{ID: "subnet-a", AvailabilityZone: "us-east-1a"}, {ID: "subnet-b", AvailabilityZone: "us-east-1b"}}
			s.Offerings = offerings
		})
	}
	everywhere := []fleettest.Offering{
		{InstanceType: "m5.large", AvailabilityZone: "us-east-1a"}, {InstanceType: "m5.large", AvailabilityZone: "us-east-1b"},
		{InstanceType: "m7i.large", AvailabilityZone: "us-east-1a"}, {InstanceType: "m7i.large", AvailabilityZone: "us-east-1b"},
	}
	const notice = "EC2 does not offer every instance type of nodegroup web in each of its availability zones (m7i.large in us-east-1b); " +
		"new nodes may fail to launch there. This checks the offering, and not the capacity that is free now"
	tests := []struct {
		name      string
		req       ScaleRequest
		offerings []fleettest.Offering
		deny      fleettest.Operation
		want      []string
		reads     int
	}{
		{name: "a type that one zone does not offer", req: scaleTo(3), offerings: everywhere[:3], want: []string{notice}, reads: 1},
		{name: "every type in every zone", req: scaleTo(3), offerings: everywhere, reads: 1},
		{name: "a scale-down reads no offering", req: scaleTo(1), offerings: everywhere[:3]},
		{name: "the offerings cannot be read", req: scaleTo(3), offerings: everywhere[:3], deny: fleettest.EC2DescribeInstanceTypeOfferings, reads: 1},
		{name: "the subnets cannot be read", req: scaleTo(3), offerings: everywhere[:3], deny: fleettest.EC2DescribeSubnets},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, scaleWorld(seed))
			offer(e.world, tt.offerings...)
			if tt.deny != "" {
				e.world.SetFault(tt.deny, fleettest.Fault{Deny: true})
			}
			p, err := NewPlan(testContext(t), e.deps, tt.req)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.want, p.Clusters[0].Notices); diff != "" {
				t.Errorf("notices (-want +got):\n%s", diff)
			}
			if len(p.allFailures()) != 0 {
				t.Errorf("failures = %+v, want none: a read that fails is dropped", p.allFailures())
			}
			if n := e.count(fleettest.EC2DescribeInstanceTypeOfferings); n != tt.reads {
				t.Errorf("%d DescribeInstanceTypeOfferings requests, want %d", n, tt.reads)
			}
		})
	}
}

// The events of a scale, and the request: a scaling config with the
// sizes that were given, and no other.
func TestRunScale_EventsAndRequest(t *testing.T) {
	tests := []struct {
		name string
		req  func() ScaleRequest
		sent map[string]any
		want string
	}{
		{name: "the desired size alone", req: func() ScaleRequest { return scaleTo(3) }, sent: map[string]any{"desiredSize": float64(3)}, want: "ACTIVE 3/1/3"},
		{
			name: "the bounds alone",
			req: func() ScaleRequest {
				r := scaleTo(0)
				r.Desired, r.Min, r.Max = nil, size(2), size(6)
				return r
			},
			sent: map[string]any{"minSize": float64(2), "maxSize": float64(6)}, want: "ACTIVE 2/2/6",
		},
		{
			name: "all three",
			req: func() ScaleRequest {
				r := scaleTo(4)
				r.Min, r.Max = size(0), size(8)
				return r
			},
			sent: map[string]any{"desiredSize": float64(4), "minSize": float64(0), "maxSize": float64(8)}, want: "ACTIVE 4/0/8",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, scaleWorld(func(c *fleettest.Cluster) {
				c.Nodegroup("web").UpdateScripts = []fleettest.UpdateScript{{After: 20 * time.Second}}
			}))
			s := &script{t: t}
			res, err := Run(testContext(t), e.deps, tt.req(), s.hooks())
			if err != nil || res.Status != Succeeded || res.ExitCode() != 0 {
				t.Fatalf("result = %s, exit %d, %v (%v)", res.Status, res.ExitCode(), err, res.Err)
			}
			want := []string{
				"PlanReady Proceed",
				"GateReport Go",
				"PhaseStarted prod/Scale",
				"StepChanged web Started",
				"UpdateProgress web InProgress",
				"UpdateProgress web Successful",
				"UpdateProgress web ACTIVE",
				"StepChanged web Succeeded",
				"PhaseEnded prod/Scale",
			}
			if diff := cmp.Diff(want, s.events); diff != "" {
				t.Errorf("events (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff([]any{tt.sent}, e.scaleRequests()); diff != "" {
				t.Errorf("scalingConfig of the request (-want +got):\n%s", diff)
			}
			if got := e.web(); got != tt.want {
				t.Errorf("nodegroup = %s, want %s", got, tt.want)
			}
		})
	}
}

// afterSubmit runs f when the request to scale arrives.
func afterSubmit(e *env, f func()) {
	e.onRequest(fleettest.EKSUpdateNodegroupConfig, func(int) { f() })
}

// oneStep returns the result of the one step of a scale.
func oneStep(t *testing.T, res Result) StepResult {
	t.Helper()
	if len(res.Clusters) != 1 || len(res.Clusters[0].Steps) != 1 {
		t.Fatalf("result = %+v, want one step", res)
	}
	return res.Clusters[0].Steps[0]
}

// The confirmation of a scale. The wait follows the update first, and
// then judges the nodegroup on its sizes and its status together.
func TestRunScale_Confirm(t *testing.T) {
	t.Run("sizes that do not match after Successful fail at once", func(t *testing.T) {
		e := newEnv(t, scaleWorld(nil))
		// The plan and the fresh read are the first two describes. The
		// third is the first one after the update, and it finds another
		// desired size than the update set.
		e.onRequest(fleettest.EKSDescribeNodegroup, func(n int) {
			if n == 3 {
				e.world.Edit(func(s *fleettest.State) { s.Cluster(east, "prod").Nodegroup("web").Desired = 2 })
			}
		})
		res, err := Run(testContext(t), e.deps, scaleTo(3), Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		s := oneStep(t, res)
		if s.Status != StepFailed || s.Failure == nil || s.Failure.Reason != apidoc.ReasonUpdateFailed ||
			!strings.Contains(s.Failure.Error, "reported Successful, but the desired size of the nodegroup is 2, not 3") {
			t.Errorf("step = %s, failure %+v; want Failed with the mismatch", s.Status, s.Failure)
		}
		if n := e.count(fleettest.EKSDescribeNodegroup); n != 3 || res.ExitCode() != 1 {
			t.Errorf("%d describes, exit %d; want 3 and 1: the wait ends at the first answer", n, res.ExitCode())
		}
	})

	t.Run("the desired size waits for ACTIVE", func(t *testing.T) {
		e := newEnv(t, scaleWorld(nil))
		// The update ends at once, and the nodegroup still says UPDATING
		// for two reads, as EKS can.
		afterSubmit(e, func() { e.world.ScriptStatus(east, "prod", "web", fleettest.StatusUpdating, fleettest.StatusUpdating) })
		s := &script{t: t}
		res, err := Run(testContext(t), e.deps, scaleTo(3), s.hooks())
		if err != nil || res.Status != Succeeded {
			t.Fatalf("result = %s, %v (%v)", res.Status, err, res.Err)
		}
		want := []string{"UpdateProgress web Successful", "UpdateProgress web UPDATING", "UpdateProgress web UPDATING", "UpdateProgress web ACTIVE", "StepChanged web Succeeded"}
		if got := s.events[len(s.events)-6 : len(s.events)-1]; !slices.Equal(got, want) {
			t.Errorf("the end of the events = %q, want %q", got, want)
		}
	})

	t.Run("the bounds alone do not wait for ACTIVE", func(t *testing.T) {
		e := newEnv(t, scaleWorld(nil))
		afterSubmit(e, func() { e.world.ScriptStatus(east, "prod", "web", fleettest.StatusUpdating, fleettest.StatusUpdating) })
		req := scaleTo(0)
		req.Desired, req.Max = nil, size(6)
		res, err := Run(testContext(t), e.deps, req, Hooks{})
		if err != nil || res.Status != Succeeded {
			t.Fatalf("result = %s, %v (%v)", res.Status, err, res.Err)
		}
		if n := e.count(fleettest.EKSDescribeNodegroup); n != 3 {
			t.Errorf("%d describes, want 3: the first answer after the update is enough", n)
		}
	})

	t.Run("DEGRADED after the update fails", func(t *testing.T) {
		e := newEnv(t, scaleWorld(func(c *fleettest.Cluster) {
			c.Nodegroup("web").UpdateScripts = []fleettest.UpdateScript{{ResourceStatus: fleettest.StatusDegraded}}
		}))
		res, err := Run(testContext(t), e.deps, scaleTo(3), Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		s := oneStep(t, res)
		if s.Status != StepFailed || s.Failure == nil || !strings.Contains(s.Failure.Error, "nodegroup web is DEGRADED after the scale to 3 nodes") || res.ExitCode() != 1 {
			t.Errorf("step = %s, failure %+v, exit %d; want Failed, 1", s.Status, s.Failure, res.ExitCode())
		}
		if n := e.count(fleettest.EKSDescribeNodegroup); n != 3 {
			t.Errorf("%d describes, want 3: a DEGRADED nodegroup is not polled until the deadline", n)
		}
	})

	t.Run("a stale ACTIVE read before UPDATING does not end the wait", func(t *testing.T) {
		e := newEnv(t, scaleWorld(func(c *fleettest.Cluster) {
			c.Nodegroup("web").UpdateScripts = []fleettest.UpdateScript{{After: 40 * time.Second}}
		}))
		// The nodegroup is UPDATING with the old sizes, and its next
		// describe would still say ACTIVE. The wait follows the update,
		// and reads the nodegroup only after the update ended.
		before := 0
		afterSubmit(e, func() {
			before = len(e.srv.Requests())
			e.world.ScriptStatus(east, "prod", "web", fleettest.StatusActive)
		})
		res, err := Run(testContext(t), e.deps, scaleTo(3), Hooks{})
		if err != nil || res.Status != Succeeded {
			t.Fatalf("result = %s, %v (%v)", res.Status, err, res.Err)
		}
		want := []string{"eks:DescribeUpdate", "eks:DescribeUpdate", "eks:DescribeUpdate", "eks:DescribeNodegroup"}
		if diff := cmp.Diff(want, e.operations(before)); diff != "" {
			t.Errorf("the requests after the submit (-want +got):\n%s", diff)
		}
		if got := e.web(); got != "ACTIVE 3/1/3" {
			t.Errorf("nodegroup = %s, want ACTIVE 3/1/3", got)
		}
	})
}

// How the wait of a scale ends when the update does not succeed, or
// cannot be followed to its end.
func TestRunScale_WaitEnds(t *testing.T) {
	t.Run("an answer with no update ID cannot be followed", func(t *testing.T) {
		e := newEnv(t, scaleWorld(nil))
		deps := e.intercept(t, func(r *http.Request) bool {
			return r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/node-groups/web/update-config")
		})
		res, err := Run(testContext(t), deps, scaleTo(3), Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		s := oneStep(t, res)
		if s.Status != StepInProgress || s.Failure == nil || s.Failure.Reason != apidoc.ReasonNotMonitored {
			t.Errorf("step = %s, failure %+v; want InProgress with NotMonitored", s.Status, s.Failure)
		}
		if res.ExitCode() != 4 || res.Status != Incomplete || e.count(fleettest.EKSDescribeUpdate) != 0 {
			t.Errorf("result = %s, exit %d, %d polls; want Incomplete, 4, and no poll", res.Status, res.ExitCode(), e.count(fleettest.EKSDescribeUpdate))
		}
	})

	t.Run("an update that fails", func(t *testing.T) {
		e := newEnv(t, scaleWorld(func(c *fleettest.Cluster) {
			c.Nodegroup("web").UpdateScripts = []fleettest.UpdateScript{{
				After: 30 * time.Second, Result: fleettest.UpdateFailed,
				Errors: []fleettest.UpdateError{{Code: "AsgInstanceLaunchFailures", Message: "Could not launch an instance", ResourceIDs: []string{"asg-web"}}},
			}}
		}))
		res, err := Run(testContext(t), e.deps, scaleTo(3), Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		s := oneStep(t, res)
		if s.Status != StepFailed || s.Failure == nil || s.Failure.Reason != apidoc.ReasonUpdateFailed || s.Failure.Kind != apidoc.FailureUpdate ||
			!strings.Contains(s.Failure.Error, "ended Failed: AsgInstanceLaunchFailures: Could not launch an instance (asg-web)") {
			t.Errorf("step = %s, failure %+v", s.Status, s.Failure)
		}
		// No confirmation follows a wait that failed.
		if n := e.count(fleettest.EKSDescribeNodegroup); n != 2 || res.ExitCode() != 1 || res.Status != Failed {
			t.Errorf("%d describes, %s, exit %d; want 2, Failed, 1", n, res.Status, res.ExitCode())
		}
	})
}

// The wait deadline of a scale bounds both waits, and what it ends is
// incomplete, exit 4.
func TestRunScale_Deadline(t *testing.T) {
	t.Run("the wait deadline", func(t *testing.T) {
		e := newEnv(t, scaleWorld(func(c *fleettest.Cluster) { c.Nodegroup("web").UpdateScripts = []fleettest.UpdateScript{{Never: true}} }))
		res, err := Run(testContext(t), e.deps, scaleTo(3), Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		s := oneStep(t, res)
		if s.Status != StepInProgress || s.Failure == nil || s.Failure.Reason != apidoc.ReasonTimeout || s.UpdateID == "" ||
			!strings.Contains(s.Failure.Error, "--wait-timeout passed before the update of nodegroup web finished; it may still be running") {
			t.Errorf("step = %s, failure %+v; want InProgress with Timeout", s.Status, s.Failure)
		}
		// 5 minutes at 15 seconds: 20 polls, and none after the deadline.
		if n := e.count(fleettest.EKSDescribeUpdate); n != 20 || res.ExitCode() != 4 || res.Status != Incomplete {
			t.Errorf("%d polls, %s, exit %d; want 20, Incomplete, 4", n, res.Status, res.ExitCode())
		}
	})

	t.Run("the deadline covers the wait for ACTIVE too", func(t *testing.T) {
		e := newEnv(t, scaleWorld(func(c *fleettest.Cluster) {
			c.Nodegroup("web").UpdateScripts = []fleettest.UpdateScript{{ResourceStatus: fleettest.StatusUpdating}}
		}))
		res, err := Run(testContext(t), e.deps, scaleTo(3), Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		s := oneStep(t, res)
		if s.Status != StepInProgress || s.Failure == nil || s.Failure.Reason != apidoc.ReasonTimeout || res.ExitCode() != 4 {
			t.Errorf("step = %s, failure %+v, exit %d; want InProgress with Timeout, 4", s.Status, s.Failure, res.ExitCode())
		}
	})
}

// An interrupt, a lost response, and --no-wait: the ways a scale ends
// that are not the end of its update.
func TestRunScale_InterruptLostResponseNoWait(t *testing.T) {
	t.Run("an interrupt", func(t *testing.T) {
		e := newEnv(t, scaleWorld(func(c *fleettest.Cluster) { c.Nodegroup("web").UpdateScripts = []fleettest.UpdateScript{{Never: true}} }))
		ctx, cancel := context.WithCancel(testContext(t))
		defer cancel()
		// The third poll is the handshake: the wait is under way.
		e.onRequest(fleettest.EKSDescribeUpdate, func(n int) {
			if n == 3 {
				cancel()
			}
		})
		res, err := Run(ctx, e.deps, scaleTo(3), Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		s := oneStep(t, res)
		if s.Status != StepInProgress || s.Failure == nil || s.Failure.Reason != apidoc.ReasonInterrupted {
			t.Errorf("step = %s, failure %+v; want InProgress with Interrupted", s.Status, s.Failure)
		}
		if res.ExitCode() != 1 || res.Status != Interrupted || !errors.Is(res.Err, context.Canceled) {
			t.Errorf("result = %s, exit %d, %v; want Interrupted, 1", res.Status, res.ExitCode(), res.Err)
		}
	})

	t.Run("a response that is lost after the commit starts one update", func(t *testing.T) {
		e := newEnv(t, scaleWorld(nil))
		e.world.SetFault(fleettest.EKSUpdateNodegroupConfig, fleettest.Fault{LoseResponse: 1})
		res, err := Run(testContext(t), e.deps, scaleTo(3), Hooks{})
		if err != nil || res.Status != Succeeded {
			t.Fatalf("result = %s, %v (%v)", res.Status, err, res.Err)
		}
		var tokens []string
		for _, r := range e.srv.Requests() {
			if r.Operation == fleettest.EKSUpdateNodegroupConfig {
				tokens = append(tokens, fmt.Sprint(r.Input["clientRequestToken"]))
			}
		}
		if len(tokens) != 2 || tokens[0] != tokens[1] || tokens[0] == "" {
			t.Errorf("tokens = %v, want the same token twice", tokens)
		}
		e.world.View(func(s *fleettest.State) {
			if n := len(s.Cluster(east, "prod").Updates); n != 1 {
				t.Errorf("%d updates in the world, want 1", n)
			}
		})
		if got := e.web(); got != "ACTIVE 3/1/3" {
			t.Errorf("nodegroup = %s, want ACTIVE 3/1/3", got)
		}
	})

	t.Run("--no-wait ends at the accepted request", func(t *testing.T) {
		e := newEnv(t, scaleWorld(func(c *fleettest.Cluster) { c.Nodegroup("web").UpdateScripts = []fleettest.UpdateScript{{Never: true}} }))
		req := scaleTo(3)
		req.NoWait = true
		res, err := Run(testContext(t), e.deps, req, Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		s := oneStep(t, res)
		if s.Status != StepStarted || s.UpdateID == "" || res.Status != Started || res.ExitCode() != 0 || e.count(fleettest.EKSDescribeUpdate) != 0 {
			t.Errorf("step = %s, result %s, exit %d, %d polls; want Started, Started, 0, and no poll", s.Status, res.Status, res.ExitCode(), e.count(fleettest.EKSDescribeUpdate))
		}
	})
}

// The fresh read before the request: a nodegroup that started to change,
// that got other sizes, or that already has the sizes of the plan is not
// sent a request.
func TestRunScale_FreshReadBeforeSubmit(t *testing.T) {
	// atFreshRead runs f before the second describe of the nodegroup: the
	// first is the one of the plan.
	atFreshRead := func(e *env, f func(n *fleettest.Nodegroup)) {
		e.onRequest(fleettest.EKSDescribeNodegroup, func(n int) {
			if n == 2 {
				e.world.Edit(func(s *fleettest.State) { f(s.Cluster(east, "prod").Nodegroup("web")) })
			}
		})
	}
	t.Run("the nodegroup started to change", func(t *testing.T) {
		e := newEnv(t, scaleWorld(func(c *fleettest.Cluster) { c.Nodegroup("web").UpdateScripts = []fleettest.UpdateScript{{Never: true}} }))
		e.onRequest(fleettest.EKSDescribeNodegroup, func(n int) {
			if n != 2 {
				return
			}
			if _, err := e.world.UpdateNodegroupConfig(east, "prod", "web", fleettest.NodegroupConfigRequest{Scaling: &fleettest.ScalingRequest{Max: size(9)}}); err != nil {
				t.Errorf("starting the other update: %v", err)
			}
		})
		res, err := Run(testContext(t), e.deps, scaleTo(3), Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		s := res.Clusters[0].Steps[0]
		if s.Status != StepBusy || s.InFlight != "ConfigUpdate" || s.Failure == nil || s.Failure.Reason != apidoc.ReasonBusy ||
			!strings.Contains(s.Failure.Error, "nodegroup web is UPDATING (ConfigUpdate); it was not changed") {
			t.Errorf("step = %s, in flight %q, failure %+v; want Busy with the type of the update", s.Status, s.InFlight, s.Failure)
		}
		if res.ExitCode() != 4 || e.count(fleettest.EKSUpdateNodegroupConfig) != 0 {
			t.Errorf("exit %d with %d requests to scale; want 4 and none", res.ExitCode(), e.count(fleettest.EKSUpdateNodegroupConfig))
		}
	})
	t.Run("the sizes changed", func(t *testing.T) {
		e := newEnv(t, scaleWorld(nil))
		atFreshRead(e, func(n *fleettest.Nodegroup) { n.Desired, n.Max = 5, 6 })
		res, err := Run(testContext(t), e.deps, scaleTo(3), Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		s := res.Clusters[0].Steps[0]
		if s.Status != StepFailed || s.Failure == nil || !strings.Contains(s.Failure.Error, "nodegroup web changed to desired 5, min 1, max 6 since the plan; nothing was sent") {
			t.Errorf("step = %s, failure %+v; want Failed and nothing sent", s.Status, s.Failure)
		}
		if res.ExitCode() != 1 || e.count(fleettest.EKSUpdateNodegroupConfig) != 0 {
			t.Errorf("exit %d with %d requests to scale; want 1 and none", res.ExitCode(), e.count(fleettest.EKSUpdateNodegroupConfig))
		}
	})
	t.Run("the nodegroup has the sizes of the plan already", func(t *testing.T) {
		e := newEnv(t, scaleWorld(nil))
		atFreshRead(e, func(n *fleettest.Nodegroup) { n.Desired = 3 })
		s := &script{t: t}
		res, err := Run(testContext(t), e.deps, scaleTo(3), s.hooks())
		if err != nil {
			t.Fatal(err)
		}
		if got := res.Clusters[0].Steps[0].Status; got != StepSkipped || res.ExitCode() != 0 || e.count(fleettest.EKSUpdateNodegroupConfig) != 0 {
			t.Errorf("step = %s, exit %d; want Skipped, 0, and no request", got, res.ExitCode())
		}
		if !slices.Contains(s.events, "Notice nodegroup web has desired 3, min 1, max 3 now; nothing was sent") {
			t.Errorf("events = %q, want the notice", s.events)
		}
	})
}

// rolling returns a world in which a roll of the nodegroup web was started
// elsewhere and does not end.
func rolling(t *testing.T) *env {
	t.Helper()
	e := newEnv(t, scaleWorld(func(c *fleettest.Cluster) { c.Nodegroup("web").UpdateScripts = []fleettest.UpdateScript{{Never: true}} }))
	if _, err := e.world.UpdateNodegroupVersion(east, "prod", "web", fleettest.NodegroupVersionRequest{}); err != nil {
		t.Fatalf("starting the roll: %v", err)
	}
	return e
}

// A target that the plan finds changing already: the step reports it,
// with the type of the update in flight when the lookup gives one.
func TestRunScale_BusyTarget(t *testing.T) {
	// The only target is busy in the plan already. Its step reports it as
	// the step of a run with other targets does: nothing is asked, the
	// update in flight is looked up once, and the document is the run.
	t.Run("a target that the plan found changing is looked up and left alone", func(t *testing.T) {
		e := rolling(t)
		req := scaleTo(3)
		req.Policy.Yes = false
		sc := &script{t: t}
		res, err := Run(testContext(t), e.deps, req, sc.hooks())
		if err != nil {
			t.Fatal(err)
		}
		s := res.Clusters[0].Steps[0]
		if s.Status != StepBusy || s.InFlight != "VersionUpdate" || s.Failure == nil || s.Failure.Reason != apidoc.ReasonBusy ||
			s.Failure.Error != "nodegroup web is UPDATING (VersionUpdate); it was not changed. Run the command again once that finishes" {
			t.Errorf("step = %s, in flight %q, failure %+v; want Busy with the type of the update", s.Status, s.InFlight, s.Failure)
		}
		if res.Status != Incomplete || res.ExitCode() != 4 || e.count(fleettest.EKSListUpdates) != 1 || e.count(fleettest.EKSUpdateNodegroupConfig) != 0 {
			t.Errorf("result = %s, exit %d, %d lookups; want Incomplete, 4, and one", res.Status, res.ExitCode(), e.count(fleettest.EKSListUpdates))
		}
		if len(sc.kinds()) != 0 {
			t.Errorf("questions = %v, want none: nothing would be submitted", sc.kinds())
		}
		doc, ok := res.Document().(apidoc.Run)
		if !ok {
			t.Fatalf("document = %T, want the run", res.Document())
		}
		if got := doc.Clusters[0].Results[0].InFlight; got != "VersionUpdate" || len(doc.Failures) != 1 {
			t.Errorf("row in flight = %q with failures %+v; want VersionUpdate and the one failure", got, doc.Failures)
		}
	})
	// The lookup has two reads, and either can fail. The target is still
	// reported as busy, and the failure says that the type is not known.
	for _, op := range []fleettest.Operation{fleettest.EKSListUpdates, fleettest.EKSDescribeUpdate} {
		t.Run("the lookup fails at "+string(op), func(t *testing.T) {
			e := rolling(t)
			e.world.SetFault(op, fleettest.Fault{Deny: true})
			res, err := Run(testContext(t), e.deps, scaleTo(3), Hooks{})
			if err != nil {
				t.Fatal(err)
			}
			s := res.Clusters[0].Steps[0]
			if s.Status != StepBusy || s.InFlight != "" || s.Failure == nil || s.Failure.Reason != apidoc.ReasonBusy ||
				s.Failure.Error != "nodegroup web is UPDATING (an update of unknown type); it was not changed. Run the command again once that finishes" {
				t.Errorf("step = %s, in flight %q, failure %+v; want Busy with an update of unknown type", s.Status, s.InFlight, s.Failure)
			}
			if res.ExitCode() != 4 || e.count(fleettest.EKSUpdateNodegroupConfig) != 0 {
				t.Errorf("exit %d with %d requests to scale; want 4 and none", res.ExitCode(), e.count(fleettest.EKSUpdateNodegroupConfig))
			}
		})
	}
	// EKS lists no update for a nodegroup that it is deleting, so the
	// failure names the status alone.
	t.Run("a target with no update in progress", func(t *testing.T) {
		e := newEnv(t, scaleWorld(func(c *fleettest.Cluster) { c.Nodegroup("web").Status = fleettest.StatusDeleting }))
		res, err := Run(testContext(t), e.deps, scaleTo(3), Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		s := res.Clusters[0].Steps[0]
		if s.Status != StepBusy || s.InFlight != "" || s.Failure == nil ||
			s.Failure.Error != "nodegroup web is DELETING; it was not changed. Run the command again once that finishes" {
			t.Errorf("step = %s, in flight %q, failure %+v; want Busy with the status alone", s.Status, s.InFlight, s.Failure)
		}
	})
}

// The questions of a scale: one to confirm the start, with the sizes in
// it, and none for a scale that changes nothing or cannot hold.
func TestRunScale_Questions(t *testing.T) {
	ask := func(req ScaleRequest) ScaleRequest {
		req.Policy.Yes = false
		return req
	}
	t.Run("yes", func(t *testing.T) {
		e := newEnv(t, scaleWorld(nil))
		s := &script{t: t, answers: []bool{true}}
		res, err := Run(testContext(t), e.deps, ask(scaleTo(3)), s.hooks())
		if err != nil || res.Status != Succeeded {
			t.Fatalf("result = %s, %v (%v)", res.Status, err, res.Err)
		}
		want := "Scale nodegroup web in prod from desired 2, min 1, max 3 to desired 3, min 1, max 3?"
		if len(s.asked) != 1 || s.asked[0].Kind != ConfirmStart || s.asked[0].Text != want {
			t.Errorf("questions = %+v, want one ConfirmStart: %s", s.asked, want)
		}
	})
	t.Run("no", func(t *testing.T) {
		e := newEnv(t, scaleWorld(nil))
		s := &script{t: t, answers: []bool{false}}
		res, err := Run(testContext(t), e.deps, ask(scaleTo(3)), s.hooks())
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != Declined || res.ExitCode() != 1 || e.count(fleettest.EKSUpdateNodegroupConfig) != 0 {
			t.Errorf("result = %s, exit %d; want Declined, 1, and no request", res.Status, res.ExitCode())
		}
	})
	t.Run("a scale to the current sizes asks nothing and sends nothing", func(t *testing.T) {
		e := newEnv(t, scaleWorld(nil))
		s := &script{t: t}
		res, err := Run(testContext(t), e.deps, ask(scaleTo(2)), s.hooks())
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != NothingToDo || res.ExitCode() != 0 || len(s.asked) != 0 || e.count(fleettest.EKSUpdateNodegroupConfig) != 0 {
			t.Errorf("result = %s, exit %d, %d questions; want NothingToDo, 0, none", res.Status, res.ExitCode(), len(s.asked))
		}
	})
	t.Run("sizes that cannot hold are an error of the run", func(t *testing.T) {
		e := newEnv(t, scaleWorld(nil))
		req := ask(scaleTo(0))
		req.Desired, req.Max = nil, size(1)
		res, err := Run(testContext(t), e.deps, req, (&script{t: t}).hooks())
		if errText(err) != "--max 1 is below the current desired size 2; pass --desired too" {
			t.Errorf("error = %v", err)
		}
		if res.ExitCode() != 1 || res.Document() != nil || e.count(fleettest.EKSUpdateNodegroupConfig) != 0 {
			t.Errorf("exit %d, document %v; want 1, none, and no request", res.ExitCode(), res.Document())
		}
	})
	t.Run("a scale-down without Kubernetes access asks about it", func(t *testing.T) {
		e := newEnv(t, scaleWorld(nil))
		s := &script{t: t, answers: []bool{true}}
		res, err := Run(testContext(t), e.deps, ask(scaleTo(1)), s.hooks())
		if err != nil || res.Status != Succeeded || res.ExitCode() != 0 {
			t.Fatalf("result = %s, exit %d, %v (%v)", res.Status, res.ExitCode(), err, res.Err)
		}
		if diff := cmp.Diff([]QuestionKind{GateWarnings}, s.kinds()); diff != "" {
			t.Errorf("questions (-want +got):\n%s", diff)
		}
	})
}

// The scale that changes direction while the prompt is open. The plan
// said 2 to 3, a scale-up, and its gate read the quota. Someone raises
// the desired size to 5. The plan after the answer says 5 to 3, a
// scale-down: its gate reads the nodes and the budgets, and the person is
// asked about the new plan, or the gate blocks.
func TestRunScale_BecomesAScaleDownWhileAsking(t *testing.T) {
	// raise makes the nodegroup five nodes, with a pod of the workload on
	// each new one.
	raise := func(e *env) {
		e.world.Edit(func(s *fleettest.State) {
			c := s.Cluster(east, "prod")
			n := c.Nodegroup("web")
			n.Desired, n.Max = 5, 6
			for _, i := range []string{"3", "4", "5"} {
				c.Nodes = append(c.Nodes, &fleettest.Node{Name: "web-" + i, Nodegroup: "web"})
				c.Pods = append(c.Pods, &fleettest.Pod{Namespace: "default", Name: "shop-" + i, Node: "web-" + i, Labels: map[string]string{"app": "shop"}})
			}
		})
	}
	run := func(t *testing.T, seed func(c *fleettest.Cluster), answers ...bool) (*env, *script, *kubetest.Server, Result) {
		t.Helper()
		e := newEnv(t, scaleWorld(seed))
		srv := e.withKube(t)
		s := &script{t: t, answers: answers}
		s.onAsk = func(i int) {
			if i != 0 {
				return
			}
			// The first plan was a scale-up: it read nothing of Kubernetes.
			if ops := kubeOps(srv); len(ops) != 0 {
				t.Errorf("the scale-up read %v from Kubernetes", ops)
			}
			raise(e)
		}
		req := scaleTo(3)
		req.Policy.Yes = false
		res, err := Run(testContext(t), e.deps, req, s.hooks())
		if err != nil {
			t.Fatal(err)
		}
		return e, s, srv, res
	}
	drainReads := []string{"kube:ListNodes", "kube:ListPodDisruptionBudgets", "kube:ListPods"}

	t.Run("the budget has room: the person is asked about the new plan", func(t *testing.T) {
		// The budget lets two of the five pods go, which is what removing
		// two nodes can take.
		roomy := func(c *fleettest.Cluster) { c.PDBs[0].DisruptionsAllowed = 2 }
		e, s, srv, res := run(t, roomy, true, true)
		if diff := cmp.Diff([]QuestionKind{ConfirmStart, PlanChanged}, s.kinds()); diff != "" {
			t.Fatalf("questions (-want +got):\n%s", diff)
		}
		if q := s.asked[1]; !strings.Contains(q.Text, "nodegroup web now has desired 5, min 1, max 6") {
			t.Errorf("the second question = %q, want the new sizes", q.Text)
		}
		if diff := cmp.Diff(drainReads, slices.Compact(kubeOps(srv))); diff != "" {
			t.Errorf("Kubernetes requests of the new direction (-want +got):\n%s", diff)
		}
		if res.Status != Succeeded || res.ExitCode() != 0 {
			t.Errorf("result = %s, exit %d (%v); want Succeeded", res.Status, res.ExitCode(), res.Err)
		}
		if diff := cmp.Diff([]any{map[string]any{"desiredSize": float64(3)}}, e.scaleRequests()); diff != "" {
			t.Errorf("requests (-want +got):\n%s", diff)
		}
		if got := e.web(); got != "ACTIVE 3/1/6" {
			t.Errorf("nodegroup = %s, want ACTIVE 3/1/6", got)
		}
		cp := res.Plan.Clusters[0]
		if diff := cmp.Diff([]string{"Nodes Pass", "DrainBlockers Pass", "Kubernetes Pass"}, checkNames(cp.Checks)); diff != "" {
			t.Errorf("the checks of the plan that ran (-want +got):\n%s", diff)
		}
	})

	t.Run("the person says no to the new plan", func(t *testing.T) {
		roomy := func(c *fleettest.Cluster) { c.PDBs[0].DisruptionsAllowed = 2 }
		e, s, _, res := run(t, roomy, true, false)
		if len(s.asked) != 2 || res.Status != Declined || res.ExitCode() != 1 || len(e.scaleRequests()) != 0 {
			t.Errorf("%d questions, result %s, exit %d, %d requests; want 2, Declined, 1, none", len(s.asked), res.Status, res.ExitCode(), len(e.scaleRequests()))
		}
	})

	t.Run("the budget has no room: the gate blocks", func(t *testing.T) {
		e, s, srv, res := run(t, nil, true)
		if diff := cmp.Diff([]QuestionKind{ConfirmStart}, s.kinds()); diff != "" {
			t.Errorf("questions (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(drainReads, slices.Compact(kubeOps(srv))); diff != "" {
			t.Errorf("Kubernetes requests of the new direction (-want +got):\n%s", diff)
		}
		if res.Status != Refused || res.ExitCode() != 3 || len(e.scaleRequests()) != 0 {
			t.Errorf("result = %s, exit %d, %d requests; want Refused, 3, none", res.Status, res.ExitCode(), len(e.scaleRequests()))
		}
		if res.Err == nil || !strings.Contains(res.Err.Error(), "DrainBlockers: removing 2 nodes from web could evict more pods than PDB ") {
			t.Errorf("message = %v, want the check that blocks", res.Err)
		}
		if got := res.Plan.Clusters[0].Gate.Blockers; len(got) != 1 || got[0].Check != "DrainBlockers" {
			t.Errorf("blockers of the gate = %+v, want DrainBlockers", got)
		}
		if got := e.web(); got != "ACTIVE 5/1/6" {
			t.Errorf("nodegroup = %s, want ACTIVE 5/1/6: nothing was sent", got)
		}
	})
}

// A confirmed plan asks about the warnings that are new to it. The plan
// said 2 to 3, a scale-up, and its gate read the quota. Someone raises the
// desired size to 5, so the plan is now a scale-down, whose gate needs
// Kubernetes, and there is no access. Nobody has seen that warning: the
// question names it, and the Run document says that it was accepted.
func TestRunScale_NewWarningsOfAChangedPlan(t *testing.T) {
	const noKube = "Kubernetes: "
	raise := func(e *env) {
		e.world.Edit(func(s *fleettest.State) { n := s.Cluster(east, "prod").Nodegroup("web"); n.Desired, n.Max = 5, 6 })
	}
	check := func(t *testing.T, e *env, s *script, res Result, last int) {
		t.Helper()
		q := s.asked[last]
		if q.Kind != PlanChanged || !strings.Contains(q.Text, "nodegroup web now has desired 5, min 1, max 6") || !strings.Contains(q.Text, noKube) {
			t.Errorf("question = %s %q; want the changed plan with the new sizes and the Kubernetes warning", q.Kind, q.Text)
		}
		if !slices.ContainsFunc(q.Reasons, func(r string) bool { return strings.HasPrefix(r, noKube) }) {
			t.Errorf("reasons = %q, want the Kubernetes warning among them", q.Reasons)
		}
		if res.Status != Succeeded || res.ExitCode() != 0 || e.count(fleettest.EKSUpdateNodegroupConfig) != 1 {
			t.Fatalf("result = %s, exit %d (%v); want Succeeded", res.Status, res.ExitCode(), res.Err)
		}
		doc, ok := res.Document().(apidoc.Run)
		if !ok || doc.Clusters[0].Gate == nil {
			t.Fatalf("document = %T with no gate", res.Document())
		}
		if !slices.ContainsFunc(doc.Clusters[0].Gate.Accepted, func(r string) bool { return strings.HasPrefix(r, noKube) }) {
			t.Errorf("accepted = %q, want the Kubernetes warning that the answer accepted", doc.Clusters[0].Gate.Accepted)
		}
	}
	t.Run("after the answer to the start question", func(t *testing.T) {
		e := newEnv(t, scaleWorld(nil))
		s := &script{t: t, answers: []bool{true, true}}
		s.onAsk = func(i int) {
			if i == 0 {
				raise(e)
			}
		}
		req := scaleTo(3)
		req.Policy.Yes = false
		res, err := Run(testContext(t), e.deps, req, s.hooks())
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff([]QuestionKind{ConfirmStart, PlanChanged}, s.kinds()); diff != "" {
			t.Fatalf("questions (-want +got):\n%s", diff)
		}
		check(t, e, s, res, 1)
	})
	t.Run("with a reviewed plan", func(t *testing.T) {
		e := newEnv(t, scaleWorld(nil))
		req := scaleTo(3)
		req.Policy.Yes = false
		reviewed, err := NewPlan(testContext(t), e.deps, req)
		if err != nil {
			t.Fatal(err)
		}
		raise(e)
		req.Expect = reviewed
		s := &script{t: t, answers: []bool{true}}
		res, err := Run(testContext(t), e.deps, req, s.hooks())
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff([]QuestionKind{PlanChanged}, s.kinds()); diff != "" {
			t.Fatalf("questions (-want +got):\n%s", diff)
		}
		check(t, e, s, res, 0)
	})
	// The same change with nobody to ask is refused, and nothing is sent.
	t.Run("with a reviewed plan and nobody to ask", func(t *testing.T) {
		e := newEnv(t, scaleWorld(nil))
		req := scaleTo(3)
		req.Policy.Yes = false
		reviewed, err := NewPlan(testContext(t), e.deps, req)
		if err != nil {
			t.Fatal(err)
		}
		raise(e)
		req.Expect = reviewed
		res, err := Run(testContext(t), e.deps, req, Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != Refused || e.count(fleettest.EKSUpdateNodegroupConfig) != 0 {
			t.Errorf("result = %s with %d requests to scale; want Refused and none", res.Status, e.count(fleettest.EKSUpdateNodegroupConfig))
		}
	})
}
