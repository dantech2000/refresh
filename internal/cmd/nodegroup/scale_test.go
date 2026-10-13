package nodegroup_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/cmd/cmdtest"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/kube/kubetest"
)

// scaleWorld returns a world with the cluster prod and the nodegroup web
// at desired 2, min 1, max 3: two nodes, a pod of one workload on each,
// and a budget that lets one of the two pods go. The vCPU quota has room.
// edit changes the cluster.
func scaleWorld(edit func(c *fleettest.Cluster)) *fleettest.World {
	world := fleettest.New()
	world.Edit(func(s *fleettest.State) {
		app := map[string]string{"app": "shop"}
		prod := &fleettest.Cluster{
			Name: "prod", Region: east, Version: "1.33",
			Nodegroups: []*fleettest.Nodegroup{
				{Name: "web", Version: "1.33", InstanceTypes: []string{"m5.large", "m7i.large"}, Subnets: []string{"subnet-a", "subnet-b"}},
				{Name: "api", Version: "1.33"},
			},
			Addons: []*fleettest.Addon{{Name: "vpc-cni", Version: "v1.20.0-eksbuild.1"}},
			Nodes:  []*fleettest.Node{{Name: "web-1", Nodegroup: "web"}, {Name: "web-2", Nodegroup: "web"}},
			Pods: []*fleettest.Pod{
				{Namespace: "default", Name: "shop-1", Node: "web-1", Labels: app},
				{Namespace: "default", Name: "shop-2", Node: "web-2", Labels: app},
			},
			PDBs: []fleettest.PDB{{Namespace: "default", Name: "shop", Selector: app, MinAvailable: 1, DisruptionsAllowed: 1, CurrentHealthy: 2, DesiredHealthy: 1, ExpectedPods: 2}},
		}
		if edit != nil {
			edit(prod)
		}
		s.Clusters = append(s.Clusters, prod)
		s.Quotas = []fleettest.Quota{{Region: east, ServiceCode: "ec2", QuotaCode: "L-1216C47A", Value: 128}}
		s.Metrics = []fleettest.Metric{{
			Region: east, Namespace: "AWS/Usage", Name: "ResourceCount", Values: []float64{40},
			Dimensions: map[string]string{"Service": "EC2", "Type": "Resource", "Resource": "vCPU", "Class": "Standard/OnDemand"},
		}}
		s.Subnets = []fleettest.Subnet{{ID: "subnet-a", AvailabilityZone: "us-east-1a"}, {ID: "subnet-b", AvailabilityZone: "us-east-1b"}}
		for _, az := range []string{"us-east-1a", "us-east-1b"} {
			for _, it := range []string{"m5.large", "m7i.large"} {
				s.Offerings = append(s.Offerings, fleettest.Offering{InstanceType: it, AvailabilityZone: az})
			}
		}
	})
	return world
}

// web returns the nodegroup that the tests scale.
func web(c *fleettest.Cluster) *fleettest.Nodegroup { return c.Nodegroup("web") }

// tightBudget makes the budget of the scale world allow no disruption.
func tightBudget(c *fleettest.Cluster) {
	c.PDBs[0].MinAvailable, c.PDBs[0].DesiredHealthy, c.PDBs[0].DisruptionsAllowed = 2, 2, 0
}

// scaleCase is one run of "refresh nodegroup scale web" against a world.
type scaleCase struct {
	name string
	// seed changes the cluster before the run, and prepare changes the
	// world after it is built, such as setting a fault.
	seed    func(c *fleettest.Cluster)
	prepare func(t *testing.T, w *fleettest.World)
	// kube gives the cluster a Kubernetes API that the command can reach.
	kube bool
	args []string
	// answer is what a person types at the prompt. Not empty means that
	// stdin is a terminal.
	answer string
	exit   int
	// sent is the scalingConfig of the request, or nil when the run must
	// send none.
	sent map[string]any
}

// runScale runs the case with the extra arguments and returns the fake
// and the output of the run as one text: the exit code, stdout, and
// stderr.
func runScale(t *testing.T, tc scaleCase, extra ...string) (*cmdtest.Fake, string) {
	t.Helper()
	world := scaleWorld(tc.seed)
	e := cmdtest.NewFake(t, world, east)
	e.UseWorldClock()
	if tc.kube {
		srv := kubetest.NewServer(world, east, "prod")
		t.Cleanup(srv.Close)
		path := filepath.Join(t.TempDir(), "kubeconfig")
		if err := srv.WriteKubeconfig(path); err != nil {
			t.Fatal(err)
		}
		t.Setenv("KUBECONFIG", path)
	}
	if tc.prepare != nil {
		tc.prepare(t, world)
	}
	if tc.answer != "" {
		e.Factory.IOStreams.InTTY = true
		e.Factory.IOStreams.In = strings.NewReader(tc.answer)
	}
	args := append([]string{"nodegroup", "scale", "web", "-c", "prod"}, tc.args...)
	err := e.Run(cmdutiltest.Context(t), append(args, extra...)...)
	if got := cmdutil.ExitCode(err); got != tc.exit {
		t.Errorf("exit code = %d, want %d (error: %v)", got, tc.exit, err)
	}
	var sent []map[string]any
	for _, r := range e.Server.Requests() {
		if r.Operation == fleettest.EKSUpdateNodegroupConfig {
			config, _ := r.Input["scalingConfig"].(map[string]any)
			sent = append(sent, config)
		}
	}
	var want []map[string]any
	if tc.sent != nil {
		want = []map[string]any{tc.sent}
	}
	if diff := cmp.Diff(want, sent); diff != "" {
		t.Errorf("scalingConfig of the UpdateNodegroupConfig requests (-want +got):\n%s", diff)
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	return e, fmt.Sprintf("exit: %d\nerror: %s\n--- stdout ---\n%s--- stderr ---\n%s", cmdutil.ExitCode(err), msg, e.Out, e.Err)
}

func scaleCases() []scaleCase {
	up, down := map[string]any{"desiredSize": float64(3)}, map[string]any{"desiredSize": float64(1)}
	slow := fleettest.UpdateScript{After: 30 * time.Second}
	script := func(s fleettest.UpdateScript) func(c *fleettest.Cluster) {
		return func(c *fleettest.Cluster) { web(c).UpdateScripts = []fleettest.UpdateScript{s} }
	}
	both := func(edits ...func(c *fleettest.Cluster)) func(c *fleettest.Cluster) {
		return func(c *fleettest.Cluster) {
			for _, edit := range edits {
				edit(c)
			}
		}
	}
	// rollElsewhere starts a roll of the nodegroup, as another process
	// would, so the target of the scale is busy with another kind of
	// update.
	rollElsewhere := func(t *testing.T, w *fleettest.World) {
		t.Helper()
		if _, err := w.UpdateNodegroupVersion(east, "prod", "web", fleettest.NodegroupVersionRequest{}); err != nil {
			t.Fatalf("starting the roll of web: %v", err)
		}
	}
	denyBudgets := func(_ *testing.T, w *fleettest.World) {
		w.SetFault(fleettest.KubeListPDBs, fleettest.Fault{Deny: true})
	}
	return []scaleCase{
		{name: "up", args: []string{"--desired", "3", "--yes"}, sent: up, seed: script(slow)},
		{name: "down-pdb-safe", kube: true, args: []string{"--desired", "1", "--yes"}, sent: down, seed: script(slow)},
		{name: "down-pdb-blocked", kube: true, args: []string{"--desired", "1", "--yes"}, exit: cmdutil.ExitBlocked, seed: tightBudget},
		{name: "down-pdb-forced", kube: true, args: []string{"--desired", "1", "--yes", "--force"}, sent: down, seed: tightBudget},
		{name: "down-pdbs-unreadable", kube: true, args: []string{"--desired", "1", "--yes"}, exit: cmdutil.ExitIncomplete, prepare: denyBudgets},
		{name: "down-pdbs-unreadable-forced", kube: true, args: []string{"--desired", "1", "--yes", "--force"}, exit: cmdutil.ExitIncomplete, sent: down, prepare: denyBudgets},
		{name: "down-no-kube-yes", args: []string{"--desired", "1", "--yes"}, sent: down},
		{name: "down-no-kube-prompt", args: []string{"--desired", "1"}, answer: "y\n", sent: down},
		{name: "down-no-kube-require-healthy", args: []string{"--desired", "1", "--yes", "--require-healthy"}, exit: cmdutil.ExitBlocked},
		{name: "down-skip-health-check", kube: true, args: []string{"--desired", "1", "--yes", "--skip-health-check"}, sent: down, seed: tightBudget},
		{name: "bounds", args: []string{"--min", "2", "--max", "6", "--yes"}, sent: map[string]any{"minSize": float64(2), "maxSize": float64(6)}},
		{name: "all-sizes", args: []string{"--desired", "4", "--min", "0", "--max", "8", "--yes", "--quiet"},
			sent: map[string]any{"desiredSize": float64(4), "minSize": float64(0), "maxSize": float64(8)}},
		{name: "cluster-busy", args: []string{"--desired", "3", "--yes"}, exit: cmdutil.ExitBlocked,
			seed: func(c *fleettest.Cluster) { c.Status = fleettest.StatusUpdating }},
		{name: "other-nodegroup-busy", args: []string{"--desired", "3", "--yes"}, exit: cmdutil.ExitBlocked,
			seed: func(c *fleettest.Cluster) { c.Nodegroup("api").Status = fleettest.StatusUpdating }},
		{name: "busy-target", args: []string{"--desired", "3", "--yes"}, exit: cmdutil.ExitIncomplete,
			seed: script(fleettest.UpdateScript{Never: true}), prepare: rollElsewhere},
		{name: "busy-target-lookup-failed", args: []string{"--desired", "3", "--yes"}, exit: cmdutil.ExitIncomplete,
			seed: script(fleettest.UpdateScript{Never: true}), prepare: func(t *testing.T, w *fleettest.World) {
				t.Helper()
				rollElsewhere(t, w)
				w.SetFault(fleettest.EKSListUpdates, fleettest.Fault{Deny: true})
			}},
		{name: "dry-run", args: []string{"--desired", "3", "--dry-run"}},
		{name: "dry-run-down", kube: true, args: []string{"--desired", "1", "--dry-run"}},
		{name: "dry-run-no-kube", args: []string{"--desired", "1", "--dry-run", "--yes"}, exit: cmdutil.ExitNeedsAttention},
		{name: "dry-run-blocked", kube: true, args: []string{"--desired", "1", "--dry-run"}, exit: cmdutil.ExitBlocked, seed: tightBudget},
		{name: "needs-yes", args: []string{"--desired", "3"}, exit: cmdutil.ExitFailure},
		{name: "prompt-yes", args: []string{"--desired", "3"}, answer: "y\n", sent: up},
		{name: "prompt-no", args: []string{"--desired", "3"}, answer: "n\n", exit: cmdutil.ExitFailure},
		{name: "no-wait", args: []string{"--desired", "3", "--yes", "--no-wait"}, sent: up, seed: script(slow)},
		{name: "no-change", args: []string{"--desired", "2", "--yes"}},
		{name: "sizes-cannot-hold", args: []string{"--desired", "9", "--yes"}, exit: cmdutil.ExitFailure},
		{name: "bounds-need-desired", args: []string{"--max", "1", "--yes"}, exit: cmdutil.ExitFailure},
		{name: "not-found", args: []string{"--desired", "3", "--yes"}, exit: cmdutil.ExitFailure,
			seed: func(c *fleettest.Cluster) { web(c).Name = "wbe" }},
		{name: "failed", args: []string{"--desired", "3", "--yes"}, exit: cmdutil.ExitFailure, sent: up,
			seed: script(fleettest.UpdateScript{
				After: 30 * time.Second, Result: fleettest.UpdateFailed,
				Errors: []fleettest.UpdateError{{Code: "AsgInstanceLaunchFailures", Message: "Could not launch an instance", ResourceIDs: []string{"asg-web"}}},
			})},
		{name: "degraded-after", args: []string{"--desired", "3", "--yes"}, exit: cmdutil.ExitFailure, sent: up,
			seed: script(fleettest.UpdateScript{ResourceStatus: fleettest.StatusDegraded})},
		{name: "deadline", args: []string{"--desired", "3", "--yes", "--wait-timeout", "2m"}, exit: cmdutil.ExitIncomplete, sent: up,
			seed: script(fleettest.UpdateScript{Never: true})},
		{name: "not-offered", args: []string{"--desired", "3", "--yes"}, sent: up,
			seed: both(script(slow), func(c *fleettest.Cluster) { web(c).InstanceTypes = []string{"m5.large", "r8g.large"} })},
	}
}

// TestScale runs every case in the table format and compares the exit
// code, stdout, and stderr with a golden file.
func TestScale(t *testing.T) {
	for _, tc := range scaleCases() {
		t.Run(tc.name, func(t *testing.T) {
			_, got := runScale(t, tc)
			cmdutiltest.Golden(t, "scale-"+tc.name+".txt", got, *update)
		})
	}
}

// TestScale_Formats runs the cases that print a document in the three
// other formats. A machine document matches the schema of its kind, and
// stdout holds nothing else.
func TestScale_Formats(t *testing.T) {
	kinds := map[string]apidoc.Kind{
		"up": apidoc.KindRun, "down-pdb-safe": apidoc.KindRun, "down-pdb-blocked": apidoc.KindPlan, "down-pdb-forced": apidoc.KindRun,
		"down-pdbs-unreadable": apidoc.KindPlan, "down-pdbs-unreadable-forced": apidoc.KindRun, "down-no-kube-yes": apidoc.KindRun,
		"down-no-kube-require-healthy": apidoc.KindPlan, "cluster-busy": apidoc.KindPlan, "busy-target": apidoc.KindRun,
		"dry-run": apidoc.KindPlan, "dry-run-down": apidoc.KindPlan, "dry-run-no-kube": apidoc.KindPlan, "dry-run-blocked": apidoc.KindPlan,
		"no-wait": apidoc.KindRun, "no-change": apidoc.KindPlan, "failed": apidoc.KindRun, "degraded-after": apidoc.KindRun,
		"deadline": apidoc.KindRun, "not-offered": apidoc.KindRun,
	}
	for _, tc := range scaleCases() {
		kind, ok := kinds[tc.name]
		if !ok {
			continue
		}
		for _, format := range []string{"json", "yaml", "plain"} {
			t.Run(tc.name+"/"+format, func(t *testing.T) {
				e, got := runScale(t, tc, "-o", format)
				cmdutiltest.Golden(t, "scale-"+tc.name+"."+format, got, *update)
				switch format {
				case "json":
					cmdutiltest.ValidateJSON(t, kind, e.Out.Bytes())
				case "yaml":
					cmdutiltest.ValidateYAML(t, kind, e.Out.Bytes())
				}
			})
		}
	}
}

// A run that is invalid, or that cannot prompt and has no --yes, refuses
// before it uses the network, and reads nothing from stdin. The order is
// complete, validate, and then the --yes check.
func TestScale_UsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no size", []string{"web", "--yes"}, "give at least one of --desired, --min, --max"},
		{"a negative size", []string{"web", "--min", "-1", "--yes"}, "--min must be 0 or more"},
		{"a max of zero", []string{"web", "--max", "0", "--yes"}, "--max must be 1 or more"},
		{"min above max", []string{"web", "--min", "5", "--max", "2", "--yes"}, "--min 5 is above --max 2"},
		{"desired below min", []string{"web", "--desired", "1", "--min", "2", "--yes"}, "--desired 1 is below --min 2"},
		{"desired above max", []string{"web", "--desired", "5", "--max", "3", "--yes"}, "--desired 5 is above --max 3"},
		{"no nodegroup", []string{"--desired", "3", "--yes"}, "accepts 1 arg(s), received 0"},
		{"two nodegroups", []string{"web", "api", "--desired", "3", "--yes"}, "accepts 1 arg(s), received 2"},
		{"dry run with no wait", []string{"web", "--desired", "3", "--dry-run", "--no-wait"}, "--dry-run changes nothing"},
		{"poll interval", []string{"web", "--desired", "3", "--yes", "--poll-interval", "0s"}, "invalid --poll-interval"},
		{"wait timeout", []string{"web", "--desired", "3", "--yes", "--wait-timeout", "-1s"}, "invalid --wait-timeout"},
		{"skip and require", []string{"web", "--desired", "3", "--yes", "--skip-health-check", "--require-healthy"}, "cannot be used together"},
		{"invalid before yes", []string{"web", "--min", "5", "--max", "2"}, "--min 5 is above --max 2"},
		{"json without yes", []string{"web", "--desired", "3", "-o", "json"}, "pass --yes to confirm the change"},
		{"no terminal without yes", []string{"web", "--desired", "3"}, "no terminal to confirm the change; pass --yes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := cmdtest.NewFake(t, scaleWorld(nil), east)
			in := strings.NewReader("y\n")
			e.Factory.IOStreams.In = in
			err := e.Run(cmdutiltest.Context(t), append([]string{"nodegroup", "scale", "-c", "prod"}, tt.args...)...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
			if got := cmdutil.ExitCode(err); got != cmdutil.ExitFailure {
				t.Errorf("exit code = %d, want 1", got)
			}
			if n := len(e.Server.Requests()); n != 0 {
				t.Errorf("%d requests, want none: %v", n, e.Operations())
			}
			if e.Out.Len() != 0 {
				t.Errorf("stdout = %q, want nothing", e.Out)
			}
			if in.Len() != 2 {
				t.Error("the command read from stdin")
			}
		})
	}
}

// A change never takes its cluster from the kubeconfig, with or without
// --dry-run.
func TestScale_NoClusterFromKubeconfig(t *testing.T) {
	for _, args := range [][]string{{"--desired", "3", "--yes"}, {"--desired", "3", "--dry-run"}} {
		e := cmdtest.NewFake(t, scaleWorld(nil), east)
		srv := kubetest.NewServer(e.World, east, "prod")
		t.Cleanup(srv.Close)
		path := filepath.Join(t.TempDir(), "kubeconfig")
		if err := srv.WriteKubeconfig(path); err != nil {
			t.Fatal(err)
		}
		t.Setenv("KUBECONFIG", path)
		err := e.Run(cmdutiltest.Context(t), append([]string{"nodegroup", "scale", "web"}, args...)...)
		if err == nil || !strings.Contains(err.Error(), "no cluster specified") {
			t.Errorf("%v: error = %v, want the no-cluster error", args, err)
		}
		if n := len(e.Server.Requests()); n != 0 {
			t.Errorf("%v: %d requests, want none", args, n)
		}
	}
}

// The requests of a scale-up with --yes: the two waves of the plan, the
// reads of the gate and of the offerings, then a fresh read, the request,
// the polls of the update, and the read that confirms the sizes.
func TestScale_Requests(t *testing.T) {
	tc := scaleCase{args: []string{"--desired", "3", "--yes"}, sent: map[string]any{"desiredSize": float64(3)}}
	e, _ := runScale(t, tc)
	var got []string
	for _, r := range e.Server.Requests() {
		got = append(got, string(r.Operation))
	}
	if len(got) < 4 {
		t.Fatalf("requests = %v", got)
	}
	want := []string{"eks:DescribeNodegroup", "eks:UpdateNodegroupConfig", "eks:DescribeUpdate", "eks:DescribeNodegroup"}
	if diff := cmp.Diff(want, got[len(got)-4:]); diff != "" {
		t.Errorf("the requests after the gate (-want +got):\n%s\nall: %v", diff, got)
	}
	for op, n := range map[fleettest.Operation]int{
		fleettest.ServiceQuotasGetServiceQuota: 1, fleettest.EC2DescribeSubnets: 1, fleettest.EC2DescribeInstanceTypeOfferings: 1,
		fleettest.EKSListUpdates: 0, fleettest.EKSDescribeAddonVersions: 0,
	} {
		if c := e.Count(op); c != n {
			t.Errorf("%d %s requests, want %d", c, op, n)
		}
	}
	var sent map[string]any
	for _, r := range e.Server.Requests() {
		if r.Operation == fleettest.EKSUpdateNodegroupConfig {
			sent = r.Input
		}
	}
	delete(sent, "clientRequestToken")
	want2 := map[string]any{"clusterName": "prod", "nodegroupName": "web", "scalingConfig": map[string]any{"desiredSize": float64(3)}}
	if diff := cmp.Diff(want2, sent); diff != "" {
		t.Errorf("the UpdateNodegroupConfig request (-want +got):\n%s", diff)
	}
}

// A response that is lost after EKS accepted the update is retried with
// the same token, so one update starts.
func TestScale_LostResponse(t *testing.T) {
	world := scaleWorld(nil)
	world.SetFault(fleettest.EKSUpdateNodegroupConfig, fleettest.Fault{LoseResponse: 1})
	e := cmdtest.NewFake(t, world, east)
	e.UseWorldClock()
	if err := e.Run(cmdutiltest.Context(t), "nodegroup", "scale", "web", "-c", "prod", "--desired", "3", "--yes"); err != nil {
		t.Fatalf("run: %v", err)
	}
	e.World.View(func(s *fleettest.State) {
		c := s.Cluster(east, "prod")
		if n := len(c.Updates); n != 1 {
			t.Errorf("%d updates in the world, want 1", n)
		}
		if n := web(c); n.Desired != 3 || n.Status != fleettest.StatusActive {
			t.Errorf("web = desired %d %s, want 3 ACTIVE", n.Desired, n.Status)
		}
	})
	var tokens []string
	for _, r := range e.Server.Requests() {
		if r.Operation == fleettest.EKSUpdateNodegroupConfig {
			tokens = append(tokens, fmt.Sprint(r.Input["clientRequestToken"]))
		}
	}
	if len(tokens) != 2 || tokens[0] != tokens[1] || tokens[0] == "" {
		t.Errorf("tokens = %v, want the same token twice", tokens)
	}
}

// An interrupt during the wait exits 1, and the result says that the
// update may still be running.
func TestScale_Interrupt(t *testing.T) {
	world := scaleWorld(func(c *fleettest.Cluster) { web(c).UpdateScripts = []fleettest.UpdateScript{{Never: true}} })
	e := cmdtest.NewFake(t, world, east)
	e.UseWorldClock()
	ctx, cancel := context.WithCancel(cmdutiltest.Context(t))
	defer cancel()
	// The third poll is the handshake: the wait is under way.
	var polls atomic.Int32
	e.Server.OnRequest = func(_ context.Context, r awsxtest.Request) {
		if r.Operation == fleettest.EKSDescribeUpdate && polls.Add(1) == 3 {
			cancel()
		}
	}
	err := e.Run(ctx, "nodegroup", "scale", "web", "-c", "prod", "--desired", "3", "--yes", "-o", "json")
	if !errors.Is(err, context.Canceled) || cmdutil.ExitCode(err) != cmdutil.ExitFailure {
		t.Fatalf("error = %v (exit %d), want an interrupt with exit 1", err, cmdutil.ExitCode(err))
	}
	cmdutiltest.ValidateJSON(t, apidoc.KindRun, e.Out.Bytes())
	for _, want := range []string{`"status": "Interrupted"`, `"status": "InProgress"`, `"reason": "Interrupted"`} {
		if !strings.Contains(e.Out.String(), want) {
			t.Errorf("stdout has no %s:\n%s", want, e.Out)
		}
	}
}
