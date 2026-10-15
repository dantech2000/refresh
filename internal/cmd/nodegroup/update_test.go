package nodegroup_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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

// The AMIs of the update world: what a nodegroup runs when it is behind.
// The latest one is what the SSM parameters of the fake recommend.
const oldAMI, oldRelease = "ami-old32x", "1.32.0-20250101"

// addRollNodegroup adds a nodegroup to c with its Auto Scaling group, two
// instances with CPU metrics, and two nodes with a pod of the workload of
// the nodegroup on each. A nodegroup that is not current runs an old AMI.
func addRollNodegroup(s *fleettest.State, c *fleettest.Cluster, name string, current bool) {
	group := "eks-" + c.Name + "-" + name
	ng := &fleettest.Nodegroup{Name: name, Version: "1.32", Subnets: []string{"subnet-a", "subnet-b"}, AutoScalingGroups: []string{group}}
	image := fleettest.LatestImage("1.32")
	if !current {
		ng.ReleaseVersion, image = oldRelease, oldAMI
	}
	c.Nodegroups = append(c.Nodegroups, ng)
	ids := []string{"i-" + group + "-1", "i-" + group + "-2"}
	s.AutoScalingGroups = append(s.AutoScalingGroups, fleettest.AutoScalingGroup{Name: group, Region: c.Region, InstanceIDs: ids})
	s.Images = append(s.Images, fleettest.Image{ID: image, Name: image, Region: c.Region})
	app := map[string]string{"app": name}
	for i, id := range ids {
		s.Instances = append(s.Instances, fleettest.Instance{ID: id, Region: c.Region, ImageID: image, Type: "m5.large", AvailabilityZone: "us-east-1a"})
		s.Metrics = append(s.Metrics, fleettest.Metric{
			Region: c.Region, Namespace: "AWS/EC2", Name: "CPUUtilization", Dimensions: map[string]string{"InstanceId": id}, Values: []float64{30, 35},
		})
		node := fmt.Sprintf("%s-%d", name, i+1)
		c.Nodes = append(c.Nodes, &fleettest.Node{Name: node, Nodegroup: name, ImageID: image, UsedMilliCPU: 400, UsedMemoryMi: 2048})
		c.Pods = append(c.Pods, &fleettest.Pod{Namespace: "default", Name: fmt.Sprintf("%s-%d", name, i+1), Node: node, Labels: app})
	}
	c.PDBs = append(c.PDBs, fleettest.PDB{Namespace: "default", Name: name, Selector: app, MinAvailable: 1, DisruptionsAllowed: 1, CurrentHealthy: 2, DesiredHealthy: 1, ExpectedPods: 2})
	c.Deployments = append(c.Deployments, fleettest.Deployment{Namespace: "default", Name: name, Replicas: 2, Selector: app})
}

// rollCluster adds a healthy cluster on Kubernetes 1.32 to s: the
// nodegroup web is on an old AMI, and api is on the latest.
func rollCluster(s *fleettest.State, name string) *fleettest.Cluster {
	c := &fleettest.Cluster{
		Name: name, Region: east, Version: "1.32", SubnetIDs: []string{"subnet-a", "subnet-b"},
		Addons: []*fleettest.Addon{{Name: "vpc-cni", Version: "v1.19.0-eksbuild.1"}},
	}
	s.Clusters = append(s.Clusters, c)
	addRollNodegroup(s, c, "web", false)
	addRollNodegroup(s, c, "api", true)
	dns := map[string]string{"k8s-app": "kube-dns"}
	c.Pods = append(c.Pods,
		&fleettest.Pod{Namespace: "kube-system", Name: "coredns-1", Node: "web-1", Labels: dns},
		&fleettest.Pod{Namespace: "kube-system", Name: "coredns-2", Node: "api-1", Labels: dns})
	c.Deployments = append(c.Deployments, fleettest.Deployment{Namespace: "kube-system", Name: "coredns", Replicas: 2, Selector: dns})
	return c
}

// updateWorld returns a world with the cluster prod, a quota with room,
// and the offerings of the instance type in both zones. edit changes it.
func updateWorld(edit func(s *fleettest.State, c *fleettest.Cluster)) *fleettest.World {
	world := fleettest.New()
	world.Edit(func(s *fleettest.State) {
		prod := rollCluster(s, "prod")
		s.Quotas = []fleettest.Quota{{Region: east, ServiceCode: "ec2", QuotaCode: "L-1216C47A", Value: 128}}
		s.Metrics = append(s.Metrics, fleettest.Metric{
			Region: east, Namespace: "AWS/Usage", Name: "ResourceCount", Values: []float64{40},
			Dimensions: map[string]string{"Service": "EC2", "Type": "Resource", "Resource": "vCPU", "Class": "Standard/OnDemand"},
		})
		s.Subnets = []fleettest.Subnet{{ID: "subnet-a", AvailabilityZone: "us-east-1a"}, {ID: "subnet-b", AvailabilityZone: "us-east-1b"}}
		for _, az := range []string{"us-east-1a", "us-east-1b"} {
			s.Offerings = append(s.Offerings, fleettest.Offering{InstanceType: "m5.large", AvailabilityZone: az})
		}
		s.DefaultUpdate = fleettest.UpdateScript{After: 30 * time.Second}
		if edit != nil {
			edit(s, prod)
		}
	})
	return world
}

// roll is one UpdateNodegroupVersion request, as the fake logged it.
type roll struct {
	Cluster, Nodegroup, Version string
	Force                       bool
}

// rollsOf returns the rolls that the fake was asked to start, in order.
func rollsOf(e *cmdtest.Fake) []roll {
	var out []roll
	for _, r := range e.Server.Requests() {
		if r.Operation != fleettest.EKSUpdateNodegroupVersion {
			continue
		}
		ro := roll{Cluster: fmt.Sprint(r.Input["clusterName"]), Nodegroup: fmt.Sprint(r.Input["nodegroupName"])}
		ro.Version, _ = r.Input["version"].(string)
		ro.Force, _ = r.Input["force"].(bool)
		out = append(out, ro)
	}
	return out
}

// updateCase is one run of "refresh nodegroup update" against a world.
type updateCase struct {
	name string
	// seed changes the world before the run, and prepare changes it after
	// it is built, such as setting a fault.
	seed    func(s *fleettest.State, c *fleettest.Cluster)
	prepare func(t *testing.T, w *fleettest.World)
	// noKube leaves the command without a kubeconfig for the cluster.
	noKube bool
	args   []string
	// answer is what a person types at the prompt. Not empty means that
	// stdin is a terminal.
	answer string
	exit   int
	// rolls are the nodegroups that the run must ask EKS to roll, in
	// order. Every roll must pin Kubernetes 1.32.
	rolls []string
	// force is the force flag that every roll must carry.
	force bool
}

// newUpdateFake returns a fake for the world, with a kubeconfig for each
// of the clusters when kube is set, and the clock of the world.
func newUpdateFake(t *testing.T, world *fleettest.World, kube bool, clusters ...string) *cmdtest.Fake {
	t.Helper()
	e := cmdtest.NewFake(t, world, east)
	e.UseWorldClock()
	if kube {
		var paths []string
		for _, cluster := range clusters {
			srv := kubetest.NewServer(world, east, cluster)
			t.Cleanup(srv.Close)
			path := filepath.Join(t.TempDir(), "kubeconfig-"+cluster)
			if err := srv.WriteKubeconfig(path); err != nil {
				t.Fatal(err)
			}
			paths = append(paths, path)
		}
		t.Setenv("KUBECONFIG", strings.Join(paths, string(filepath.ListSeparator)))
	}
	return e
}

// runUpdate runs the case with the extra arguments and returns the fake
// and the output of the run as one text: the exit code, stdout, and
// stderr.
func runUpdate(t *testing.T, tc updateCase, extra ...string) (*cmdtest.Fake, string) {
	t.Helper()
	world := updateWorld(tc.seed)
	e := newUpdateFake(t, world, !tc.noKube, "prod")
	if tc.prepare != nil {
		tc.prepare(t, world)
	}
	if tc.answer != "" {
		e.Factory.IOStreams.InTTY = true
		e.Factory.IOStreams.In = strings.NewReader(tc.answer)
	}
	args := append([]string{"nodegroup", "update", "-c", "prod"}, tc.args...)
	err := e.Run(cmdutiltest.Context(t), append(args, extra...)...)
	if got := cmdutil.ExitCode(err); got != tc.exit {
		t.Errorf("exit code = %d, want %d (error: %v)", got, tc.exit, err)
	}
	var want []roll
	for _, name := range tc.rolls {
		want = append(want, roll{Cluster: "prod", Nodegroup: name, Version: "1.32", Force: tc.force})
	}
	if diff := cmp.Diff(want, rollsOf(e)); diff != "" {
		t.Errorf("the UpdateNodegroupVersion requests (-want +got):\n%s", diff)
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	return e, fmt.Sprintf("exit: %d\nerror: %s\n--- stdout ---\n%s--- stderr ---\n%s", cmdutil.ExitCode(err), msg, e.Out, e.Err)
}

func updateCases() []updateCase {
	script := func(name string, sc fleettest.UpdateScript) func(*fleettest.State, *fleettest.Cluster) {
		return func(_ *fleettest.State, c *fleettest.Cluster) {
			c.Nodegroup(name).UpdateScripts = []fleettest.UpdateScript{sc}
		}
	}
	both := func(edits ...func(*fleettest.State, *fleettest.Cluster)) func(*fleettest.State, *fleettest.Cluster) {
		return func(s *fleettest.State, c *fleettest.Cluster) {
			for _, edit := range edits {
				edit(s, c)
			}
		}
	}
	// stale puts api on the old AMI too, so the cluster has two rolls.
	stale := func(s *fleettest.State, c *fleettest.Cluster) {
		c.Nodegroup("api").ReleaseVersion = oldRelease
		for i := range s.Instances {
			if strings.HasPrefix(s.Instances[i].ID, "i-eks-"+c.Name+"-api") {
				s.Instances[i].ImageID = oldAMI
			}
		}
	}
	tightWeb := func(_ *fleettest.State, c *fleettest.Cluster) {
		c.PDBs[0].MinAvailable, c.PDBs[0].DesiredHealthy, c.PDBs[0].DisruptionsAllowed = 2, 2, 0
	}
	failing := fleettest.UpdateScript{
		After: 30 * time.Second, Result: fleettest.UpdateFailed,
		Errors: []fleettest.UpdateError{{Code: "PodEvictionFailure", Message: "Reached max retries while trying to evict pods", ResourceIDs: []string{"api-1"}}},
	}
	never := fleettest.UpdateScript{Never: true}
	// smallQuota is the account of the roll that stalled: 4 of a quota
	// of quota vCPUs in use, and nodegroups of t3.medium nodes in Auto
	// Scaling groups in 3 Availability Zones.
	smallQuota := func(quota float64) func(*fleettest.State, *fleettest.Cluster) {
		return func(s *fleettest.State, c *fleettest.Cluster) {
			s.Quotas[0].Value = quota
			for i := range s.Metrics {
				if s.Metrics[i].Namespace == "AWS/Usage" {
					s.Metrics[i].Values = []float64{4}
				}
			}
			for _, ng := range c.Nodegroups {
				ng.InstanceTypes = []string{"t3.medium"}
			}
			for i := range s.AutoScalingGroups {
				s.AutoScalingGroups[i].AvailabilityZones = []string{"us-east-1a", "us-east-1b", "us-east-1c"}
			}
		}
	}
	scaleElsewhere := func(t *testing.T, w *fleettest.World) {
		t.Helper()
		three := int32(3)
		if _, err := w.UpdateNodegroupConfig(east, "prod", "web", fleettest.NodegroupConfigRequest{Scaling: &fleettest.ScalingRequest{Desired: &three}}); err != nil {
			t.Fatalf("scaling web: %v", err)
		}
	}
	return []updateCase{
		{name: "yes", args: []string{"web", "--yes"}, rolls: []string{"web"}},
		{name: "all", args: []string{"--yes"}, rolls: []string{"web"}},
		{name: "several", args: []string{"--yes"}, rolls: []string{"api", "web"}, seed: stale},
		{name: "one-failed", args: []string{"--yes"}, rolls: []string{"api", "web"}, exit: cmdutil.ExitIncomplete, seed: both(stale, script("api", failing))},
		{name: "failed", args: []string{"web", "--yes"}, rolls: []string{"web"}, exit: cmdutil.ExitFailure, seed: script("web", failing)},
		{name: "no-wait", args: []string{"web", "--yes", "--no-wait"}, rolls: []string{"web"}},
		{name: "quiet", args: []string{"web", "--yes", "--quiet"}, rolls: []string{"web"}},
		{name: "skip-verify", args: []string{"web", "--yes", "--skip-verify"}, rolls: []string{"web"}},
		{name: "dry-run", args: []string{"--dry-run"}},
		{name: "dry-run-ask", noKube: true, args: []string{"--dry-run", "--yes"}, exit: cmdutil.ExitNeedsAttention},
		{name: "dry-run-blocked", args: []string{"--dry-run"}, exit: cmdutil.ExitBlocked, seed: tightWeb},
		{name: "quota-surge", args: []string{"web", "--dry-run"}, exit: cmdutil.ExitBlocked, seed: smallQuota(8)},
		{name: "quota-surge-blocked", args: []string{"web", "--yes"}, exit: cmdutil.ExitBlocked, seed: smallQuota(8)},
		{name: "quota-surge-forced", args: []string{"web", "--yes", "--force"}, rolls: []string{"web"}, force: true, seed: smallQuota(8)},
		{name: "quota-surge-require-healthy", args: []string{"web", "--yes", "--require-healthy"}, exit: cmdutil.ExitBlocked, seed: smallQuota(8)},
		// One roll of 3 to 6 nodes fits a quota of 16 with 4 in use; two
		// at once may launch 12 to 24 vCPU.
		{name: "quota-together", args: []string{"--dry-run"}, exit: cmdutil.ExitNeedsAttention, seed: both(stale, smallQuota(16))},
		{name: "needs-yes", args: []string{"web"}, exit: cmdutil.ExitFailure},
		{name: "prompt-yes", args: []string{"web"}, answer: "y\n", rolls: []string{"web"}},
		{name: "prompt-no", args: []string{"web"}, answer: "n\n", exit: cmdutil.ExitFailure},
		{name: "prompt-warnings", noKube: true, args: []string{"web"}, answer: "y\n", rolls: []string{"web"}},
		{name: "no-kube-yes", noKube: true, args: []string{"web", "--yes"}, rolls: []string{"web"}},
		{name: "require-healthy", noKube: true, args: []string{"web", "--yes", "--require-healthy"}, exit: cmdutil.ExitBlocked},
		{name: "pdb-blocked", args: []string{"web", "--yes"}, exit: cmdutil.ExitBlocked, seed: tightWeb},
		{name: "pdb-forced", args: []string{"web", "--yes", "--force"}, rolls: []string{"web"}, force: true, seed: tightWeb},
		{name: "skip-health-check", args: []string{"web", "--yes", "--skip-health-check"}, rolls: []string{"web"}, seed: tightWeb},
		{name: "cluster-busy", args: []string{"web", "--yes"}, exit: cmdutil.ExitBlocked,
			seed: func(_ *fleettest.State, c *fleettest.Cluster) { c.Status = fleettest.StatusUpdating }},
		{name: "busy-target", args: []string{"web", "--yes"}, exit: cmdutil.ExitIncomplete, seed: script("web", never), prepare: scaleElsewhere},
		{name: "degraded-target", args: []string{"web", "--yes"}, exit: cmdutil.ExitBlocked,
			seed: func(_ *fleettest.State, c *fleettest.Cluster) { c.Nodegroup("web").Status = fleettest.StatusDegraded }},
		{name: "deadline", args: []string{"web", "--yes", "--wait-timeout", "2m"}, rolls: []string{"web"}, exit: cmdutil.ExitIncomplete, seed: script("web", never)},
		{name: "reroll", args: []string{"api", "--yes", "--reroll"}, rolls: []string{"api"}},
		{name: "force-current", args: []string{"api", "--yes", "--force"}},
		{name: "up-to-date", args: []string{"api", "--yes"}},
		{name: "custom-ami", args: []string{"web", "--yes", "--force", "--reroll"},
			seed: func(_ *fleettest.State, c *fleettest.Cluster) { c.Nodegroup("web").AMIType = "CUSTOM" }},
		{name: "filter", args: []string{"-f", "name=w*", "--yes"}, rolls: []string{"web"}},
		{name: "filter-prompt", args: []string{"-f", "version=1.32"}, answer: "y\n", rolls: []string{"api", "web"}, seed: stale},
		{name: "filter-no-match", args: []string{"-f", "name=zz*", "--yes"}, exit: cmdutil.ExitFailure},
		{name: "not-found", args: []string{"wbe", "--yes"}, exit: cmdutil.ExitFailure},
		{name: "verify-not-ready", args: []string{"web", "--yes"}, rolls: []string{"web"}, exit: cmdutil.ExitVerifyFailed,
			seed: script("web", fleettest.UpdateScript{After: 30 * time.Second, Roll: fleettest.RollScript{NodesNeverReady: true}})},
		{name: "verify-unreadable", args: []string{"web", "--yes", "--skip-health-check"}, rolls: []string{"web"}, exit: cmdutil.ExitIncomplete,
			prepare: func(_ *testing.T, w *fleettest.World) {
				w.SetFault(fleettest.KubeListPods, fleettest.Fault{Deny: true})
			}},
	}
}

// TestUpdate runs every case in the table format and compares the exit
// code, stdout, and stderr with a golden file.
func TestUpdate(t *testing.T) {
	for _, tc := range updateCases() {
		t.Run(tc.name, func(t *testing.T) {
			_, got := runUpdate(t, tc)
			cmdutiltest.Golden(t, "update-"+tc.name+".txt", got, *update)
		})
	}
}

// TestUpdate_Formats runs the cases that print a document in the three
// other formats. A machine document matches the schema of its kind, and
// stdout holds nothing else.
func TestUpdate_Formats(t *testing.T) {
	kinds := map[string]apidoc.Kind{
		"yes": apidoc.KindRun, "several": apidoc.KindRun, "one-failed": apidoc.KindRun, "failed": apidoc.KindRun, "no-wait": apidoc.KindRun,
		"dry-run": apidoc.KindPlan, "dry-run-ask": apidoc.KindPlan, "dry-run-blocked": apidoc.KindPlan, "no-kube-yes": apidoc.KindRun,
		"require-healthy": apidoc.KindPlan, "pdb-blocked": apidoc.KindPlan, "pdb-forced": apidoc.KindRun, "cluster-busy": apidoc.KindPlan,
		"busy-target": apidoc.KindRun, "deadline": apidoc.KindRun, "force-current": apidoc.KindPlan, "custom-ami": apidoc.KindPlan,
		"filter": apidoc.KindRun, "verify-not-ready": apidoc.KindRun, "verify-unreadable": apidoc.KindRun,
	}
	for _, tc := range updateCases() {
		kind, ok := kinds[tc.name]
		if !ok {
			continue
		}
		for _, format := range []string{"json", "yaml", "plain"} {
			t.Run(tc.name+"/"+format, func(t *testing.T) {
				e, got := runUpdate(t, tc, "-o", format)
				cmdutiltest.Golden(t, "update-"+tc.name+"."+format, got, *update)
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
func TestUpdate_UsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"fleet with a cluster", []string{"--all-clusters", "-c", "prod", "--yes"}, "--all-clusters and -c/--cluster cannot be used together"},
		{"fleet with a nodegroup", []string{"web", "--all-clusters", "--yes"}, "--all-clusters takes no nodegroup name"},
		{"fleet with a kube context", []string{"--all-clusters", "--kube-context", "prod", "--yes"}, "--all-clusters and --kube-context cannot be used together"},
		{"changelog without a dry run", []string{"-c", "prod", "--changelog", "--yes"}, "pass --dry-run with it"},
		{"live with json", []string{"-c", "prod", "--live", "--yes", "-o", "json"}, "--live draws the nodes"},
		{"live with quiet", []string{"-c", "prod", "--live", "--yes", "--quiet"}, "--live and --quiet cannot be used together"},
		{"a filter that does not parse", []string{"-c", "prod", "-f", "web", "--yes"}, "invalid filter"},
		{"a filter key that is not known", []string{"-c", "prod", "-f", "color=red", "--yes"}, "unknown key"},
		{"dry run with no wait", []string{"-c", "prod", "--dry-run", "--no-wait"}, "--dry-run changes nothing"},
		{"poll interval", []string{"-c", "prod", "--yes", "--poll-interval", "0s"}, "invalid --poll-interval"},
		{"skip and require", []string{"-c", "prod", "--yes", "--skip-health-check", "--require-healthy"}, "cannot be used together"},
		{"invalid before yes", []string{"-c", "prod", "--changelog"}, "pass --dry-run with it"},
		{"no cluster", []string{"web", "--yes"}, "no cluster specified"},
		{"json without yes", []string{"-c", "prod", "-o", "json"}, "pass --yes to confirm the change"},
		{"no terminal without yes", []string{"-c", "prod"}, "no terminal to confirm the change; pass --yes"},
		{"fleet without yes", []string{"--all-clusters"}, "no terminal to confirm the change; pass --yes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := cmdtest.NewFake(t, updateWorld(nil), east)
			in := strings.NewReader("y\n")
			e.Factory.IOStreams.In = in
			err := e.Run(cmdutiltest.Context(t), append([]string{"nodegroup", "update"}, tt.args...)...)
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

// A change never takes its cluster from the kubeconfig, and a fleet takes
// none from REFRESH_CLUSTER.
func TestUpdate_ClusterSources(t *testing.T) {
	for _, args := range [][]string{{"--yes"}, {"--dry-run"}} {
		e := newUpdateFake(t, updateWorld(nil), true, "prod")
		err := e.Run(cmdutiltest.Context(t), append([]string{"nodegroup", "update"}, args...)...)
		if err == nil || !strings.Contains(err.Error(), "no cluster specified") {
			t.Errorf("%v: error = %v, want the no-cluster error", args, err)
		}
		if n := len(e.Server.Requests()); n != 0 {
			t.Errorf("%v: %d requests, want none", args, n)
		}
	}
	// REFRESH_CLUSTER names a cluster that the world does not have. A
	// fleet ignores it and plans the cluster of the sweep.
	e := newUpdateFake(t, updateWorld(nil), true, "prod")
	t.Setenv(cmdutil.EnvCluster, "elsewhere")
	if err := e.Run(cmdutiltest.Context(t), "nodegroup", "update", "--all-clusters", "-r", east, "--dry-run", "-o", "json"); err != nil {
		t.Fatalf("fleet dry run: %v", err)
	}
	if !strings.Contains(e.Out.String(), `"cluster": "prod"`) || strings.Contains(e.Out.String()+e.Err.String(), "elsewhere") {
		t.Errorf("the fleet did not plan prod alone:\n%s%s", e.Out, e.Err)
	}
}

// The requests of a roll with --yes, after the reads of the plan and the
// gate: the fresh read, the request, the polls, and the read of the
// verification. The request pins the version and carries nothing else.
func TestUpdate_Requests(t *testing.T) {
	e, _ := runUpdate(t, updateCase{args: []string{"web", "--yes"}, rolls: []string{"web"}})
	var got []string
	for _, r := range e.Server.Requests() {
		if op := string(r.Operation); strings.HasPrefix(op, "eks:") {
			got = append(got, op)
		}
	}
	want := []string{"eks:DescribeNodegroup", "eks:UpdateNodegroupVersion", "eks:DescribeUpdate", "eks:DescribeUpdate", "eks:DescribeCluster", "eks:DescribeNodegroup"}
	if len(got) < len(want) {
		t.Fatalf("requests = %v", got)
	}
	// The two reads of the verification start together.
	tail := got[len(got)-len(want):]
	slices.Sort(tail[4:])
	if diff := cmp.Diff(want, tail); diff != "" {
		t.Errorf("the EKS requests after the gate (-want +got):\n%s\nall: %v", diff, got)
	}
	var sent map[string]any
	for _, r := range e.Server.Requests() {
		if r.Operation == fleettest.EKSUpdateNodegroupVersion {
			sent = r.Input
		}
	}
	delete(sent, "clientRequestToken")
	if diff := cmp.Diff(map[string]any{"clusterName": "prod", "nodegroupName": "web", "version": "1.32"}, sent); diff != "" {
		t.Errorf("the UpdateNodegroupVersion request (-want +got):\n%s", diff)
	}
	if n := e.Count(fleettest.EKSListUpdates); n != 0 {
		t.Errorf("%d ListUpdates requests, want none", n)
	}
}

// A response that is lost after EKS accepted the roll is retried with the
// same token, so one update starts.
func TestUpdate_LostResponse(t *testing.T) {
	world := updateWorld(nil)
	world.SetFault(fleettest.EKSUpdateNodegroupVersion, fleettest.Fault{LoseResponse: 1})
	e := newUpdateFake(t, world, true, "prod")
	if err := e.Run(cmdutiltest.Context(t), "nodegroup", "update", "web", "-c", "prod", "--yes"); err != nil {
		t.Fatalf("run: %v", err)
	}
	e.World.View(func(s *fleettest.State) {
		c := s.Cluster(east, "prod")
		if n := len(c.Updates); n != 1 {
			t.Errorf("%d updates in the world, want 1", n)
		}
		if n := c.Nodegroup("web"); n.ReleaseVersion == oldRelease || n.Status != fleettest.StatusActive {
			t.Errorf("web = %s %s, want the new release and ACTIVE", n.ReleaseVersion, n.Status)
		}
	})
	var tokens []string
	for _, r := range e.Server.Requests() {
		if r.Operation == fleettest.EKSUpdateNodegroupVersion {
			tokens = append(tokens, fmt.Sprint(r.Input["clientRequestToken"]))
		}
	}
	if len(tokens) != 2 || tokens[0] != tokens[1] || tokens[0] == "" {
		t.Errorf("tokens = %v, want the same token twice", tokens)
	}
}

// An interrupt during the wait exits 1, and the result says that the
// roll may still be running.
func TestUpdate_Interrupt(t *testing.T) {
	world := updateWorld(func(s *fleettest.State, _ *fleettest.Cluster) { s.DefaultUpdate = fleettest.UpdateScript{Never: true} })
	e := newUpdateFake(t, world, true, "prod")
	ctx, cancel := context.WithCancel(cmdutiltest.Context(t))
	defer cancel()
	// The third poll is the handshake: the wait is under way.
	var polls atomic.Int32
	e.Server.OnRequest = func(_ context.Context, r awsxtest.Request) {
		if r.Operation == fleettest.EKSDescribeUpdate && polls.Add(1) == 3 {
			cancel()
		}
	}
	err := e.Run(ctx, "nodegroup", "update", "web", "-c", "prod", "--yes", "-o", "json")
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

// fleetWorld returns the update world with the clusters dev and staging
// beside prod. edit changes them.
func fleetWorld(edit func(dev, prod, staging *fleettest.Cluster)) *fleettest.World {
	return updateWorld(func(s *fleettest.State, prod *fleettest.Cluster) {
		dev, staging := rollCluster(s, "dev"), rollCluster(s, "staging")
		if edit != nil {
			edit(dev, prod, staging)
		}
	})
}

// A fleet: one plan and one confirmation for every cluster, the clusters
// one after another, and a cluster that is blocked or fails beside one
// that goes through.
func TestUpdate_Fleet(t *testing.T) {
	mixed := func(_, prod, staging *fleettest.Cluster) {
		prod.Nodegroup("web").Status = fleettest.StatusDegraded
		staging.Nodegroup("web").UpdateScripts = []fleettest.UpdateScript{{After: 30 * time.Second, Result: fleettest.UpdateFailed}}
	}
	tests := []struct {
		name   string
		seed   func(dev, prod, staging *fleettest.Cluster)
		args   []string
		answer string
		exit   int
		rolls  []string
		kind   apidoc.Kind
	}{
		{name: "yes", args: []string{"--yes"}, rolls: []string{"dev", "prod", "staging"}, kind: apidoc.KindRun},
		{name: "dry-run", args: []string{"--dry-run"}, kind: apidoc.KindPlan},
		{name: "prompt", args: nil, answer: "y\n", rolls: []string{"dev", "prod", "staging"}},
		{name: "prompt-no", args: nil, answer: "n\n", exit: cmdutil.ExitFailure},
		// dev goes through, prod is blocked, and the roll of staging fails.
		{name: "mixed", seed: mixed, args: []string{"--yes"}, rolls: []string{"dev", "staging"}, exit: cmdutil.ExitIncomplete, kind: apidoc.KindRun},
		{name: "mixed-dry-run", seed: mixed, args: []string{"--dry-run"}, exit: cmdutil.ExitBlocked, kind: apidoc.KindPlan},
		{name: "filter", args: []string{"--yes", "-f", "name=api", "--reroll"}, rolls: []string{"dev", "prod", "staging"}},
	}
	for _, tt := range tests {
		formats := []string{"table"}
		if tt.kind != "" {
			formats = append(formats, "json")
		}
		for _, format := range formats {
			t.Run(tt.name+"/"+format, func(t *testing.T) {
				e := newUpdateFake(t, fleetWorld(tt.seed), true, "dev", "prod", "staging")
				if tt.answer != "" {
					e.Factory.IOStreams.InTTY = true
					e.Factory.IOStreams.In = strings.NewReader(tt.answer)
				}
				args := append([]string{"nodegroup", "update", "--all-clusters", "-r", east, "-o", format}, tt.args...)
				err := e.Run(cmdutiltest.Context(t), args...)
				if got := cmdutil.ExitCode(err); got != tt.exit {
					t.Errorf("exit code = %d, want %d (error: %v)", got, tt.exit, err)
				}
				var clusters []string
				for _, r := range rollsOf(e) {
					clusters = append(clusters, r.Cluster)
					if r.Version != "1.32" {
						t.Errorf("the roll of %s/%s pins %q, want 1.32", r.Cluster, r.Nodegroup, r.Version)
					}
				}
				if diff := cmp.Diff(tt.rolls, clusters); diff != "" {
					t.Errorf("the clusters of the rolls, in order (-want +got):\n%s", diff)
				}
				msg := ""
				if err != nil {
					msg = err.Error()
				}
				got := fmt.Sprintf("exit: %d\nerror: %s\n--- stdout ---\n%s--- stderr ---\n%s", cmdutil.ExitCode(err), msg, e.Out, e.Err)
				ext := map[string]string{"table": "txt", "json": "json"}[format]
				cmdutiltest.Golden(t, "update-fleet-"+tt.name+"."+ext, got, *update)
				if format == "json" {
					cmdutiltest.ValidateJSON(t, tt.kind, e.Out.Bytes())
				}
			})
		}
	}
}

// An interrupt in the middle of a fleet leaves the later clusters alone:
// their steps are NotAttempted, and nothing is sent for them.
func TestUpdate_FleetInterrupt(t *testing.T) {
	world := fleetWorld(nil)
	world.Edit(func(s *fleettest.State) { s.DefaultUpdate = fleettest.UpdateScript{Never: true} })
	e := newUpdateFake(t, world, true, "dev", "prod", "staging")
	ctx, cancel := context.WithCancel(cmdutiltest.Context(t))
	defer cancel()
	var polls atomic.Int32
	e.Server.OnRequest = func(_ context.Context, r awsxtest.Request) {
		if r.Operation == fleettest.EKSDescribeUpdate && polls.Add(1) == 2 {
			cancel()
		}
	}
	err := e.Run(ctx, "nodegroup", "update", "--all-clusters", "-r", east, "--yes", "-o", "json")
	if !errors.Is(err, context.Canceled) || cmdutil.ExitCode(err) != cmdutil.ExitFailure {
		t.Fatalf("error = %v (exit %d), want an interrupt with exit 1", err, cmdutil.ExitCode(err))
	}
	if diff := cmp.Diff([]roll{{Cluster: "dev", Nodegroup: "web", Version: "1.32"}}, rollsOf(e)); diff != "" {
		t.Errorf("rolls (-want +got):\n%s", diff)
	}
	cmdutiltest.ValidateJSON(t, apidoc.KindRun, e.Out.Bytes())
	if n := strings.Count(e.Out.String(), `"status": "NotAttempted"`); n != 4 {
		t.Errorf("%d steps are NotAttempted, want the 4 of prod and staging:\n%s", n, e.Out)
	}
}

// roundTrip answers every request of a client, so no test reaches the
// network.
type roundTrip func(r *http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// --changelog adds the AMI release notes to a dry run, on stderr. A fetch
// that fails is a warning, and the plan and its exit code stay.
func TestUpdate_Changelog(t *testing.T) {
	releases := `[{"tag_name":"v20260101","body":"* Update containerd to 2.1 by @a in https://github.com/awslabs/amazon-eks-ami/pull/2400\n"},
		{"tag_name":"v20250601","body":"- kernel 6.12.40\n"},{"tag_name":"v20241201","body":"- kernel 6.1\n"}]`
	tests := []struct {
		name   string
		client *http.Client
		want   []string
	}{
		{name: "fetched", want: []string{"AMI release notes", "nodegroup web", "v20260101", "Update containerd to 2.1 (#2400)", "v20250601"},
			client: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(releases)), Request: r}, nil
			})}},
		{name: "failed", want: []string{"warning: release notes unavailable"},
			client: &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("no route to host")
			})}},
		{name: "no client", want: []string{"warning: release notes unavailable"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newUpdateFake(t, updateWorld(nil), true, "prod")
			e.Factory.HTTPClient = tt.client
			err := e.Run(cmdutiltest.Context(t), "nodegroup", "update", "-c", "prod", "--dry-run", "--changelog", "-o", "json")
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			cmdutiltest.ValidateJSON(t, apidoc.KindPlan, e.Out.Bytes())
			for _, want := range tt.want {
				if !strings.Contains(e.Err.String(), want) {
					t.Errorf("stderr has no %q:\n%s", want, e.Err)
				}
			}
			if strings.Contains(e.Err.String(), "v20241201") {
				t.Errorf("stderr has a release that is older than the one of the nodegroup:\n%s", e.Err)
			}
			if len(rollsOf(e)) != 0 {
				t.Error("a dry run sent a roll")
			}
		})
	}
}

// --live shows the nodes of the one roll, also when stderr is no
// terminal: frames of the panel are appended. The result is the same.
func TestUpdate_Live(t *testing.T) {
	e := newUpdateFake(t, updateWorld(nil), true, "prod")
	// The observer asks its clock for a pause after its first snapshot.
	// That is the handshake: the pauses of the wait are held until then,
	// so the roll does not end before the panel has a frame.
	shown := make(chan struct{})
	var once sync.Once
	e.Factory.Tick = func(time.Duration) <-chan time.Time {
		once.Do(func() { close(shown) })
		return nil
	}
	ctx := cmdutiltest.Context(t)
	after := e.Factory.After
	e.Factory.After = func(d time.Duration) <-chan time.Time {
		select {
		case <-shown:
		case <-ctx.Done():
		}
		return after(d)
	}
	if err := e.Run(ctx, "nodegroup", "update", "web", "-c", "prod", "--yes", "--live"); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, want := range []string{"rolling web", "NODE", "web-1", "web: Succeeded"} {
		if !strings.Contains(e.Err.String(), want) {
			t.Errorf("stderr has no %q:\n%s", want, e.Err)
		}
	}
	if strings.Contains(e.Err.String(), "\x1b[") {
		t.Errorf("stderr has cursor codes without a terminal:\n%q", e.Err)
	}
	if !strings.Contains(e.Out.String(), "Succeeded") || len(rollsOf(e)) != 1 {
		t.Errorf("stdout = %s, rolls = %v", e.Out, rollsOf(e))
	}
}
