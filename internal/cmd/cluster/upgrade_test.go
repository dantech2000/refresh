package cluster_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/cmd/cmdtest"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/kube/kubetest"
)

// The add-on builds of the engine world, by the Kubernetes version that
// runs them.
const (
	cni32, cni33     = "v1.19.0-eksbuild.1", "v1.20.0-eksbuild.1"
	proxy32, proxy33 = "v1.32.0-eksbuild.1", "v1.33.0-eksbuild.1"
)

// addEngineNodegroup adds a nodegroup on a Kubernetes version to c, with
// its Auto Scaling group, two instances with CPU metrics, two nodes with
// a pod of its own workload on each, and a budget that lets one go.
func addEngineNodegroup(s *fleettest.State, c *fleettest.Cluster, name, version string) {
	group := "eks-" + c.Name + "-" + name
	c.Nodegroups = append(c.Nodegroups, &fleettest.Nodegroup{Name: name, Version: version, Subnets: []string{"subnet-a"}, AutoScalingGroups: []string{group}})
	image := fleettest.LatestImage(version)
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
		c.Pods = append(c.Pods, &fleettest.Pod{Namespace: "default", Name: node, Node: node, Labels: app})
	}
	c.PDBs = append(c.PDBs, fleettest.PDB{Namespace: "default", Name: name, Selector: app, MinAvailable: 1, DisruptionsAllowed: 1, CurrentHealthy: 2, DesiredHealthy: 1, ExpectedPods: 2})
	c.Deployments = append(c.Deployments, fleettest.Deployment{Namespace: "default", Name: name, Replicas: 2, Selector: app})
}

// engineWorld returns a world with the cluster prod on a Kubernetes
// version: the nodegroups web and api, the add-on vpc-cni, which runs on
// one minor version only, and kube-proxy, which also runs on the next.
// EKS offers 1.31 to 1.34, and every update takes 30 seconds. upgraded
// puts the upgrade to that version in the history, a day ago.
func engineWorld(version string, upgraded bool, edit func(s *fleettest.State, c *fleettest.Cluster)) *fleettest.World {
	world := fleettest.New()
	at := world.Now().Add(-24 * time.Hour)
	world.Edit(func(s *fleettest.State) {
		cni, proxy := cni32, proxy32
		if version == "1.33" {
			cni, proxy = cni33, proxy33
		}
		c := &fleettest.Cluster{
			Name: "prod", Region: east, Version: version, SupportType: "EXTENDED", SubnetIDs: []string{"subnet-a"},
			Addons: []*fleettest.Addon{{Name: "vpc-cni", Version: cni}, {Name: "kube-proxy", Version: proxy}},
		}
		s.Clusters = append(s.Clusters, c)
		addEngineNodegroup(s, c, "web", version)
		addEngineNodegroup(s, c, "api", version)
		dns := map[string]string{"k8s-app": "kube-dns"}
		c.Pods = append(c.Pods, &fleettest.Pod{Namespace: "kube-system", Name: "coredns-1", Node: "web-1", Labels: dns})
		c.Deployments = append(c.Deployments, fleettest.Deployment{Namespace: "kube-system", Name: "coredns", Replicas: 1, Selector: dns})
		if upgraded {
			c.Updates = []*fleettest.Update{{
				ID: "11111111-1111-4111-8111-111111111111", Type: fleettest.TypeVersionUpdate,
				Params: []fleettest.UpdateParam{{Type: "Version", Value: version}}, CreatedAt: at,
			}}
		}
		s.ClusterVersions = []fleettest.ClusterVersion{{Version: "1.34"}, {Version: "1.33"}, {Version: "1.32"}, {Version: "1.31"}}
		s.AddonVersions = []fleettest.AddonVersion{
			{Addon: "vpc-cni", Version: cni33, ClusterVersions: []string{"1.33"}},
			{Addon: "vpc-cni", Version: cni32, ClusterVersions: []string{"1.31", "1.32"}},
			{Addon: "kube-proxy", Version: proxy33, ClusterVersions: []string{"1.33"}},
			{Addon: "kube-proxy", Version: proxy32, ClusterVersions: []string{"1.31", "1.32", "1.33"}},
		}
		s.Quotas = []fleettest.Quota{{Region: east, ServiceCode: "ec2", QuotaCode: "L-1216C47A", Value: 128}}
		s.Metrics = append(s.Metrics, fleettest.Metric{
			Region: east, Namespace: "AWS/Usage", Name: "ResourceCount", Values: []float64{40},
			Dimensions: map[string]string{"Service": "EC2", "Type": "Resource", "Resource": "vCPU", "Class": "Standard/OnDemand"},
		})
		s.Subnets = []fleettest.Subnet{{ID: "subnet-a", AvailabilityZone: "us-east-1a"}}
		s.Offerings = []fleettest.Offering{{InstanceType: "m5.large", AvailabilityZone: "us-east-1a"}}
		s.DefaultUpdate = fleettest.UpdateScript{After: 30 * time.Second}
		if edit != nil {
			edit(s, c)
		}
	})
	return world
}

// engineCase is one run of "refresh cluster upgrade" or "refresh cluster
// rollback" against a world.
type engineCase struct {
	name string
	seed func(s *fleettest.State, c *fleettest.Cluster)
	// prepare changes the world after it is built.
	prepare func(t *testing.T, w *fleettest.World)
	// noKube leaves the command without a kubeconfig for the cluster.
	noKube bool
	args   []string
	// answer is what a person types at the prompts. Not empty means that
	// stdin is a terminal.
	answer string
	exit   int
	// sent are the requests that change the cluster, in order.
	sent []string
	// interruptAt, when set, interrupts the run when a request arrives
	// that it accepts. The request is the handshake: the run is at a
	// known point when the signal lands.
	interruptAt func(r awsxtest.Request) bool
	// emptyAnswer, when not "", is the end of the path of a POST request
	// that a server in front of the fake answers with an empty object: an
	// update that EKS accepted and gave no ID for. The fake never sees
	// that request, so it is not in sent, and the world does not change.
	emptyAnswer string
}

// sentBy returns the requests of the run that change something, each as
// its operation and the fields that a plan shows.
func sentBy(e *cmdtest.Fake) []string {
	var out []string
	for _, r := range e.Server.Requests() {
		if !r.Mutates {
			continue
		}
		line := strings.TrimPrefix(string(r.Operation), "eks:")
		for _, key := range []string{"nodegroupName", "addonName", "version", "addonVersion"} {
			if v, ok := r.Input[key]; ok {
				line += " " + fmt.Sprint(v)
			}
		}
		if force, _ := r.Input["force"].(bool); force {
			line += " force"
		}
		if cfg, ok := r.Input["rollbackConfig"].(map[string]any); ok {
			line += fmt.Sprintf(" timeout=%v", cfg["timeoutMinutes"])
		}
		out = append(out, line)
	}
	return out
}

// runEngine runs the case as the command with the extra arguments, and
// returns the fake and the output of the run as one text.
func runEngine(t *testing.T, command []string, world *fleettest.World, tc engineCase, extra ...string) (*cmdtest.Fake, string) {
	t.Helper()
	e := cmdtest.NewFake(t, world, east)
	e.UseWorldClock()
	if !tc.noKube {
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
	if tc.emptyAnswer != "" {
		front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, tc.emptyAnswer) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte("{}"))
				return
			}
			e.Server.ServeHTTP(w, r)
		}))
		t.Cleanup(front.Close)
		cfg := e.Server.Config(east)
		cfg.BaseEndpoint = aws.String(front.URL)
		cmdutiltest.UseAWS(e.Factory, cfg)
	}
	ctx, cancel := context.WithCancel(cmdutiltest.Context(t))
	defer cancel()
	if tc.interruptAt != nil {
		var once sync.Once
		e.Server.OnRequest = func(_ context.Context, r awsxtest.Request) {
			if tc.interruptAt(r) {
				once.Do(cancel)
			}
		}
	}
	if tc.answer != "" {
		e.Factory.IOStreams.InTTY = true
		e.Factory.IOStreams.In = strings.NewReader(tc.answer)
	}
	args := append(append([]string{}, command...), tc.args...)
	err := e.Run(ctx, append(args, extra...)...)
	if got := cmdutil.ExitCode(err); got != tc.exit {
		t.Errorf("exit code = %d, want %d (error: %v)", got, tc.exit, err)
	}
	if diff := cmp.Diff(tc.sent, sentBy(e)); diff != "" {
		t.Errorf("the requests that change the cluster (-want +got):\n%s", diff)
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	return e, fmt.Sprintf("exit: %d\nerror: %s\n--- stdout ---\n%s--- stderr ---\n%s", cmdutil.ExitCode(err), msg, e.Out, e.Err)
}

// The requests of the upgrade of the engine world to 1.33.
const (
	refresh  = "StartInsightsRefresh"
	cp33     = "UpdateClusterVersion 1.33"
	cniTo33  = "UpdateAddon vpc-cni " + cni33
	web33    = "UpdateNodegroupVersion web 1.33"
	api33    = "UpdateNodegroupVersion api 1.33"
	proxyTo3 = "UpdateAddon kube-proxy " + proxy33
)

func upgradeCases() []engineCase {
	whole := []string{refresh, cp33, cniTo33, api33, web33, proxyTo3}
	failing := fleettest.UpdateScript{After: 30 * time.Second, Result: fleettest.UpdateFailed, Errors: []fleettest.UpdateError{{Code: "ConfigurationConflict", Message: "conflicts found"}}}
	tight := func(_ *fleettest.State, c *fleettest.Cluster) {
		c.PDBs[1].MinAvailable, c.PDBs[1].DesiredHealthy, c.PDBs[1].DisruptionsAllowed = 2, 2, 0
	}
	// smallQuota is the account of the roll that stalled: 4 of a quota of
	// quota vCPUs in use, and nodegroups of t3.medium nodes in Auto
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
	return []engineCase{
		{name: "yes", args: []string{"--yes"}, sent: whole},
		{name: "quiet", args: []string{"--yes", "--quiet"}, sent: whole},
		{name: "dry-run", args: []string{"--dry-run"}},
		{name: "dry-run-ask", noKube: true, args: []string{"--dry-run"}, exit: cmdutil.ExitNeedsAttention},
		{name: "dry-run-blocked", args: []string{"--dry-run"}, exit: cmdutil.ExitBlocked, seed: tight},
		{name: "quota-surge", args: []string{"--dry-run"}, exit: cmdutil.ExitNeedsAttention, seed: smallQuota(8)},
		{name: "quota-surge-require-healthy", args: []string{"--yes", "--require-healthy"}, exit: cmdutil.ExitBlocked, seed: smallQuota(8), sent: []string{refresh}},
		// The engine rolls one nodegroup after another: the 3 to 6 nodes of
		// one roll count, not the 6 to 12 of two, so the likely surge fits a
		// quota of 14 with 4 in use and the most does not.
		{name: "quota-one-at-a-time", args: []string{"--dry-run"}, exit: cmdutil.ExitNeedsAttention, seed: smallQuota(14)},
		{name: "pdb-blocked", args: []string{"--yes"}, exit: cmdutil.ExitBlocked, seed: tight, sent: []string{refresh}},
		{name: "pdb-forced", args: []string{"--yes", "--force"}, seed: tight, sent: []string{refresh, cp33, cniTo33, api33 + " force", web33 + " force", proxyTo3}},
		{name: "insight-blocked", args: []string{"--yes"}, exit: cmdutil.ExitBlocked, sent: []string{refresh},
			seed: func(_ *fleettest.State, c *fleettest.Cluster) {
				c.Insights = []fleettest.Insight{{ID: "in-1", Name: "Deprecated APIs removed in Kubernetes v1.33", Status: fleettest.InsightError, KubernetesVersion: "1.33"}}
			}},
		{name: "prompt-yes", answer: "y\ny\ny\ny\n", sent: whole},
		{name: "prompt-no", answer: "n\n", exit: cmdutil.ExitFailure, sent: []string{refresh}},
		{name: "addon-failed", args: []string{"--yes"}, exit: cmdutil.ExitFailure, sent: []string{refresh, cp33, cniTo33},
			seed: func(_ *fleettest.State, c *fleettest.Cluster) {
				c.Addon("vpc-cni").UpdateScripts = []fleettest.UpdateScript{failing}
			}},
		{name: "deadline", args: []string{"--yes", "--wait-timeout", "2m"}, exit: cmdutil.ExitIncomplete, sent: []string{refresh, cp33},
			seed: func(s *fleettest.State, _ *fleettest.Cluster) { s.DefaultUpdate = fleettest.UpdateScript{Never: true} }},
		{name: "only-control-plane", args: []string{"--yes", "--only", "control-plane"}, sent: []string{refresh, cp33}},
		{name: "one-nodegroup", args: []string{"--yes", "-n", "web", "--skip-addon", "kube-proxy"}, sent: []string{refresh, cp33, cniTo33, web33}},
		{name: "nothing-to-do", args: []string{"--yes"}, seed: func(_ *fleettest.State, c *fleettest.Cluster) {
			c.Version = "1.33"
			c.Addon("vpc-cni").Version, c.Addon("kube-proxy").Version = cni33, proxy33
			for _, ng := range c.Nodegroups {
				ng.Version = "1.33"
			}
		}},
	}
}

// TestUpgrade runs every case in the table format and compares the exit
// code, stdout, and stderr with a golden file.
func TestUpgrade(t *testing.T) {
	for _, tc := range upgradeCases() {
		t.Run(tc.name, func(t *testing.T) {
			_, got := runEngine(t, []string{"cluster", "upgrade", "prod", "--to", "1.33"}, engineWorld("1.32", false, tc.seed), tc)
			cmdutiltest.Golden(t, "upgrade-"+tc.name+".txt", got, *update)
		})
	}
}

// TestUpgrade_Formats runs the cases that print a document in the three
// other formats. A machine document matches the schema of its kind, and
// no FIELD of a plain document is on two rows.
func TestUpgrade_Formats(t *testing.T) {
	kinds := map[string]apidoc.Kind{
		"yes": apidoc.KindRun, "dry-run": apidoc.KindPlan, "pdb-blocked": apidoc.KindPlan, "addon-failed": apidoc.KindRun, "deadline": apidoc.KindRun,
	}
	for _, tc := range upgradeCases() {
		kind, ok := kinds[tc.name]
		if !ok {
			continue
		}
		for _, format := range []string{"json", "yaml", "plain"} {
			t.Run(tc.name+"/"+format, func(t *testing.T) {
				e, got := runEngine(t, []string{"cluster", "upgrade", "prod", "--to", "1.33"}, engineWorld("1.32", false, tc.seed), tc, "-o", format)
				cmdutiltest.Golden(t, "upgrade-"+tc.name+"."+format, got, *update)
				switch format {
				case "json":
					cmdutiltest.ValidateJSON(t, kind, e.Out.Bytes())
				case "yaml":
					cmdutiltest.ValidateYAML(t, kind, e.Out.Bytes())
				case "plain":
					noRepeatedField(t, e.Out.String())
				}
			})
		}
	}
}

// noRepeatedField fails the test for a FIELD that a plain document has on
// two rows.
func noRepeatedField(t *testing.T, plain string) {
	t.Helper()
	seen := map[string]bool{}
	for _, line := range strings.Split(plain, "\n") {
		field, _, _ := strings.Cut(line, "\t")
		if field == "" {
			continue
		}
		if seen[field] {
			t.Errorf("FIELD %q is on two rows", field)
		}
		seen[field] = true
	}
}

// An upgrade on a terminal asks once for each phase that has work. The
// first question is also the start question.
func TestUpgrade_PromptCount(t *testing.T) {
	tc := engineCase{name: "prompts", answer: "y\ny\ny\ny\ny\ny\n", sent: []string{refresh, cp33, cniTo33, api33, web33, proxyTo3}}
	e, _ := runEngine(t, []string{"cluster", "upgrade", "prod", "--to", "1.33"}, engineWorld("1.32", false, nil), tc)
	if n := strings.Count(e.Err.String(), "[y/N]"); n != 4 {
		t.Errorf("%d prompts, want 4: the control plane, the add-ons before the rolls, the nodegroups, and the other add-ons\n%s", n, e.Err)
	}
}

// The upgrade of a two-hop plan names each step with its hop, so no row
// of the plain document repeats.
func TestUpgrade_TwoHopsPlain(t *testing.T) {
	world := engineWorld("1.32", false, func(s *fleettest.State, _ *fleettest.Cluster) {
		s.AddonVersions = append(s.AddonVersions,
			fleettest.AddonVersion{Addon: "vpc-cni", Version: "v1.21.0-eksbuild.1", ClusterVersions: []string{"1.34"}},
			fleettest.AddonVersion{Addon: "kube-proxy", Version: "v1.34.0-eksbuild.1", ClusterVersions: []string{"1.34"}})
	})
	e, _ := runEngine(t, []string{"cluster", "upgrade", "prod", "--to", "1.34"}, world, engineCase{args: []string{"--dry-run", "-o", "plain"}})
	noRepeatedField(t, e.Out.String())
	if !strings.Contains(e.Out.String(), "web@1.33") || !strings.Contains(e.Out.String(), "web@1.34") {
		t.Errorf("the plain plan does not name the steps of web by their hop:\n%s", e.Out)
	}
}

// A run that is invalid, or that cannot prompt and has no --yes, refuses
// before it uses the network. The order is complete, validate, and then
// the --yes check. The cluster never comes from the kubeconfig, also not
// for a dry run.
func TestEngineCommands_UsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"upgrade without --to", []string{"cluster", "upgrade", "prod", "--yes"}, "--to is required"},
		{"upgrade to no version", []string{"cluster", "upgrade", "prod", "--to", "latest", "--yes"}, "invalid --to"},
		{"upgrade with an unknown part", []string{"cluster", "upgrade", "prod", "--to", "1.33", "--only", "nodes", "--yes"}, "--only"},
		{"upgrade with two clusters", []string{"cluster", "upgrade", "prod", "dev", "--to", "1.33", "--yes"}, "accepts at most 1 arg"},
		{"upgrade with no cluster", []string{"cluster", "upgrade", "--to", "1.33", "--yes"}, "no cluster specified"},
		{"upgrade dry run with no cluster", []string{"cluster", "upgrade", "--to", "1.33", "--dry-run"}, "no cluster specified"},
		{"upgrade has no --no-wait", []string{"cluster", "upgrade", "prod", "--to", "1.33", "--yes", "--no-wait"}, "unknown flag: --no-wait"},
		{"upgrade json without yes", []string{"cluster", "upgrade", "prod", "--to", "1.33", "-o", "json"}, "pass --yes to confirm the change"},
		{"upgrade without a terminal", []string{"cluster", "upgrade", "prod", "--to", "1.33"}, "no terminal to confirm the change; pass --yes"},
		{"upgrade invalid before yes", []string{"cluster", "upgrade", "prod", "--to", "1.33", "--poll-interval", "0s"}, "invalid --poll-interval"},
		{"rollback timeout too short", []string{"cluster", "rollback", "prod", "--yes", "--rollback-timeout", "30m"}, "--rollback-timeout must be whole minutes"},
		{"rollback timeout not whole minutes", []string{"cluster", "rollback", "prod", "--yes", "--rollback-timeout", "2h30s"}, "--rollback-timeout must be whole minutes"},
		{"rollback with no cluster", []string{"cluster", "rollback", "--dry-run"}, "no cluster specified"},
		{"rollback has no --no-wait", []string{"cluster", "rollback", "prod", "--yes", "--no-wait"}, "unknown flag: --no-wait"},
		{"rollback without a terminal", []string{"cluster", "rollback", "prod"}, "no terminal to confirm the change; pass --yes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := cmdtest.NewFake(t, engineWorld("1.32", false, nil), east)
			e.Factory.IOStreams.In = strings.NewReader("y\n")
			err := e.Run(cmdutiltest.Context(t), tt.args...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
			if got := cmdutil.ExitCode(err); got != cmdutil.ExitFailure {
				t.Errorf("exit code = %d, want 1", got)
			}
			if n := len(e.Server.Requests()); n != 0 {
				t.Errorf("%d request(s) before the refusal, want none", n)
			}
		})
	}
}

func rollbackCases() []engineCase {
	whole := []string{"UpdateNodegroupVersion api 1.32", "UpdateNodegroupVersion web 1.32", "UpdateAddon vpc-cni " + cni32, "UpdateAddon kube-proxy " + proxy32, "UpdateClusterVersion 1.32"}
	forced := append(append([]string{}, whole[:4]...), "UpdateClusterVersion 1.32 force timeout=180")
	return []engineCase{
		{name: "yes", args: []string{"--yes"}, sent: whole},
		{name: "dry-run", args: []string{"--dry-run"}},
		{name: "prompt-yes", answer: "y\n", sent: whole},
		{name: "prompt-no", answer: "n\n", exit: cmdutil.ExitFailure},
		{name: "force-and-timeout", args: []string{"--yes", "--skip-insights-check", "--rollback-timeout", "3h"}, sent: forced},
		{name: "control-plane-failed", args: []string{"--yes"}, exit: cmdutil.ExitFailure, sent: whole,
			seed: func(_ *fleettest.State, c *fleettest.Cluster) {
				c.UpdateScripts = []fleettest.UpdateScript{{After: 30 * time.Second, Result: fleettest.UpdateFailed}}
			}},
	}
}

// TestRollback runs every case in the table format and compares the exit
// code, stdout, and stderr with a golden file.
func TestRollback(t *testing.T) {
	for _, tc := range rollbackCases() {
		t.Run(tc.name, func(t *testing.T) {
			_, got := runEngine(t, []string{"cluster", "rollback", "prod"}, engineWorld("1.33", true, tc.seed), tc)
			cmdutiltest.Golden(t, "rollback-"+tc.name+".txt", got, *update)
		})
	}
	t.Run("no upgrade to roll back", func(t *testing.T) {
		tc := engineCase{args: []string{"--yes"}, exit: cmdutil.ExitBlocked}
		_, got := runEngine(t, []string{"cluster", "rollback", "prod"}, engineWorld("1.33", false, nil), tc)
		cmdutiltest.Golden(t, "rollback-blocked.txt", got, *update)
	})
}

// TestRollback_Formats runs the cases that print a document in the three
// other formats.
func TestRollback_Formats(t *testing.T) {
	kinds := map[string]apidoc.Kind{"yes": apidoc.KindRun, "dry-run": apidoc.KindPlan, "control-plane-failed": apidoc.KindRun}
	for _, tc := range rollbackCases() {
		kind, ok := kinds[tc.name]
		if !ok {
			continue
		}
		for _, format := range []string{"json", "yaml", "plain"} {
			t.Run(tc.name+"/"+format, func(t *testing.T) {
				e, got := runEngine(t, []string{"cluster", "rollback", "prod"}, engineWorld("1.33", true, tc.seed), tc, "-o", format)
				cmdutiltest.Golden(t, "rollback-"+tc.name+"."+format, got, *update)
				switch format {
				case "json":
					cmdutiltest.ValidateJSON(t, kind, e.Out.Bytes())
				case "yaml":
					cmdutiltest.ValidateYAML(t, kind, e.Out.Bytes())
				case "plain":
					noRepeatedField(t, e.Out.String())
				}
			})
		}
	}
}
