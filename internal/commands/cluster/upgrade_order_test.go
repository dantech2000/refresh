package cluster

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
	"github.com/dantech2000/refresh/internal/ui"
)

// orderWorld is a 1.31 cluster with kube-proxy, whose builds track the
// minor, and vpc-cni, whose build stays compatible with 1.32.
func orderWorld() *fakeaws.Cluster {
	return &fakeaws.Cluster{Name: "prod", Version: "1.31",
		Nodegroups: []*fakeaws.Nodegroup{{Name: "web", Version: "1.31"}},
		Addons: []*fakeaws.Addon{
			{Name: "vpc-cni", Version: "v1.19.0-eksbuild.1", Available: []string{"v1.20.0-eksbuild.1", "v1.19.0-eksbuild.1"}},
			{Name: "kube-proxy", Version: "v1.31.0-eksbuild.1", AvailableFor: map[string][]string{
				"1.31": {"v1.31.0-eksbuild.1"},
				"1.32": {"v1.32.0-eksbuild.1"},
			}},
		},
	}
}

// mutations lists the fake's mutating calls in order, by resource.
func mutations(calls []string) []string {
	var out []string
	for _, c := range calls {
		switch {
		case strings.HasSuffix(c, "POST /clusters/prod/updates"):
			out = append(out, "cp")
		case strings.HasSuffix(c, "/update-version"):
			out = append(out, "ng "+strings.Split(c, "/")[4])
		case strings.Contains(c, "POST /clusters/prod/addons/") && strings.HasSuffix(c, "/update"):
			out = append(out, "addon "+strings.Split(c, "/")[4])
		}
	}
	return out
}

// The dry run lists kube-proxy before the roll and vpc-cni after it, and the
// JSON plan marks kube-proxy beforeNodegroups.
func TestUpgrade_PlanOrdersAddonsAroundRolls(t *testing.T) {
	fakeaws.New(t, orderWorld())
	stdout, stderr, err := runCluster(t, "upgrade", "prod", "--to", "1.32", "--dry-run")
	if err != nil {
		t.Fatalf("dry run: %v\nstderr:\n%s", err, stderr)
	}
	out := ui.StripANSI(stdout)
	kp, ng, vc := strings.Index(out, "addon kube-proxy"), strings.Index(out, "nodegroup web"), strings.Index(out, "addon vpc-cni")
	if kp < 0 || ng < 0 || vc < 0 || kp > ng || ng > vc {
		t.Fatalf("plan order wrong, want kube-proxy, web, vpc-cni:\n%s", out)
	}
	if !strings.Contains(out, "v1.31.0-eksbuild.1 is not compatible with 1.32: updated before the nodegroup rolls") {
		t.Errorf("plan does not say why kube-proxy goes first:\n%s", out)
	}

	fakeaws.New(t, orderWorld())
	stdout, stderr, err = runCluster(t, "upgrade", "prod", "--to", "1.32", "--dry-run", "-o", "json")
	if err != nil {
		t.Fatalf("dry run -o json: %v\nstderr:\n%s", err, stderr)
	}
	plan := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
	steps := plan["hops"].([]any)[0].(map[string]any)["steps"].([]any)
	var got []string
	for _, s := range steps {
		m := s.(map[string]any)
		e := m["type"].(string)
		if tgt, _ := m["target"].(string); tgt != "" {
			e += " " + tgt
		}
		if b, _ := m["beforeNodegroups"].(bool); b {
			e += "!"
		}
		got = append(got, e)
	}
	want := "Readiness,ControlPlane,Addon kube-proxy!,Nodegroup web,Addon vpc-cni"
	if strings.Join(got, ",") != want {
		t.Fatalf("steps = %v, want %s", got, want)
	}
}

// A run changes the cluster in the new order: control plane, kube-proxy,
// the roll, then vpc-cni.
func TestUpgrade_RunOrdersAddonsAroundRolls(t *testing.T) {
	srv := fakeaws.New(t, orderWorld())
	_, stderr, err := runCluster(t, "upgrade", "prod", "--to", "1.32", "--yes", "--poll-interval", "5ms", "-o", "json")
	if err != nil {
		t.Fatalf("upgrade: %v\nstderr:\n%s", err, stderr)
	}
	want := "cp,addon kube-proxy,ng web,addon vpc-cni"
	if got := mutations(srv.Calls()); strings.Join(got, ",") != want {
		t.Fatalf("mutations = %v, want %s\ncalls:\n%s", got, want, strings.Join(srv.Calls(), "\n"))
	}
	c := srv.Cluster("prod")
	if c.Addons[0].Version != "v1.20.0-eksbuild.1" || c.Addons[1].Version != "v1.32.0-eksbuild.1" {
		t.Errorf("add-ons after upgrade: vpc-cni %s, kube-proxy %s", c.Addons[0].Version, c.Addons[1].Version)
	}
	i := strings.Index(stderr, "required addons for 1.32")
	j := strings.Index(stderr, "nodegroup rolls to 1.32")
	k := strings.Index(stderr, "addons for 1.32 (1 update(s), dependency order)")
	if i < 0 || j < i || k < j {
		t.Errorf("phase headers out of order in stderr:\n%s", stderr)
	}
}

// A run that stopped in the last add-on phase resumes with only that add-on.
func TestUpgrade_ResumeAfterRollsUpdatesTrailingAddon(t *testing.T) {
	w := orderWorld()
	w.Addons[0].UpdateStatus = "Failed"
	srv := fakeaws.New(t, w)
	_, stderr, err := runCluster(t, "upgrade", "prod", "--to", "1.32", "--yes", "--poll-interval", "5ms", "-o", "json")
	if err == nil {
		t.Fatalf("first run succeeded; want a failure in the last add-on phase\nstderr:\n%s", stderr)
	}
	if c := srv.Cluster("prod"); c.Version != "1.32" || c.Nodegroups[0].Version != "1.32" || c.Addons[1].Version != "v1.32.0-eksbuild.1" {
		t.Fatalf("after the failed run: cp %s, web %s, kube-proxy %s; want all moved", c.Version, c.Nodegroups[0].Version, c.Addons[1].Version)
	}
	before := len(mutations(srv.Calls()))

	srv.Cluster("prod").Addons[0].UpdateStatus = ""
	_, stderr, err = runCluster(t, "upgrade", "prod", "--to", "1.32", "--yes", "--poll-interval", "5ms", "-o", "json")
	if err != nil {
		t.Fatalf("rerun: %v\nstderr:\n%s", err, stderr)
	}
	if got := mutations(srv.Calls())[before:]; strings.Join(got, ",") != "addon vpc-cni" {
		t.Fatalf("rerun mutations = %v, want only vpc-cni", got)
	}
}
