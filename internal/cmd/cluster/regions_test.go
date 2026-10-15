package cluster_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/cmd/cmdtest"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/kube/kubetest"
)

// twins is a fleet with a cluster named prod in two regions, each with a
// version of its own.
func twins() *fleettest.World {
	world := fleettest.New()
	world.Edit(func(s *fleettest.State) {
		s.Clusters = append(s.Clusters,
			&fleettest.Cluster{Name: "prod", Region: east, Version: "1.33", Nodegroups: []*fleettest.Nodegroup{{Name: "web", Version: "1.33"}}},
			&fleettest.Cluster{Name: "prod", Region: west, Version: "1.31", Nodegroups: []*fleettest.Nodegroup{{Name: "web", Version: "1.31"}}},
		)
	})
	return world
}

// The same cluster name in two regions is two clusters: a sweep lists
// both, each with its region and version, and a command on one cluster
// reads the region it was given and no other.
func TestSameNameInTwoRegions(t *testing.T) {
	t.Run("list", func(t *testing.T) {
		e := cmdtest.NewFake(t, twins(), east)
		if err := e.Run(cmdutiltest.Context(t), "cluster", "list", "-r", east, "-r", west, "-o", "json"); err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Clusters []struct{ Name, Region, Version string }
		}
		if err := json.Unmarshal(e.Out.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, c := range doc.Clusters {
			got = append(got, c.Name+"@"+c.Region+"="+c.Version)
		}
		slices.Sort(got)
		if want := []string{"prod@us-east-1=1.33", "prod@us-west-2=1.31"}; !slices.Equal(got, want) {
			t.Errorf("rows = %v, want %v\n%s", got, want, e.Out)
		}
	})
	for _, region := range []string{east, west} {
		t.Run("describe in "+region, func(t *testing.T) {
			e := cmdtest.NewFake(t, twins(), east)
			if err := e.Run(cmdutiltest.Context(t), "cluster", "describe", "prod", "-r", region, "-o", "json"); err != nil {
				t.Fatal(err)
			}
			want := map[string]string{east: `"version": "1.33"`, west: `"version": "1.31"`}[region]
			if !strings.Contains(e.Out.String(), want) {
				t.Errorf("the document is not of prod in %s:\n%s", region, e.Out)
			}
			for _, r := range e.Server.Requests() {
				if strings.HasPrefix(string(r.Operation), "eks:") && r.Region != region {
					t.Errorf("%s went to %s, want %s only", r.Operation, r.Region, region)
				}
			}
		})
	}
}

// A kubeconfig context that is named like the cluster but whose server
// is another API server (a cluster rebuilt under the same name, or the
// twin in another region) is never used: a change does not read the
// Kubernetes API of the wrong cluster, and says that no context matches.
func TestKubeconfigContextOfTheWrongEndpoint(t *testing.T) {
	e := cmdtest.NewFake(t, engineWorld("1.32", false, nil), east)
	e.UseWorldClock()
	// A server of another cluster, which the context names as prod.
	other := fleettest.New()
	other.Edit(func(s *fleettest.State) {
		s.Clusters = []*fleettest.Cluster{{Name: "prod", Region: west, Version: "1.32", Nodes: []*fleettest.Node{{Name: "imposter-1", Nodegroup: "web"}}}}
	})
	wrong := kubetest.NewServer(other, west, "prod")
	t.Cleanup(wrong.Close)
	kubeconfig := "apiVersion: v1\nkind: Config\ncurrent-context: prod\n" +
		"clusters:\n- name: prod\n  cluster: {server: " + wrong.URL + "}\n" +
		"contexts:\n- name: prod\n  context: {cluster: prod, user: prod}\n" +
		"users:\n- name: prod\n  user: {token: fake-token}\n"
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(kubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", path)
	err := e.Run(cmdutiltest.Context(t), "cluster", "upgrade", "prod", "--to", "1.33", "--dry-run", "-o", "json")
	if got := cmdutil.ExitCode(err); got == cmdutil.ExitOK {
		t.Errorf("exit code = 0: a dry run without the Kubernetes checks would not ask")
	}
	if n := len(wrong.Requests()); n != 0 {
		t.Errorf("%d requests reached the API server of another cluster", n)
	}
	if strings.Contains(e.Out.String(), "imposter-1") {
		t.Errorf("the plan read the nodes of another cluster:\n%s", e.Out)
	}
	if !strings.Contains(e.Out.String()+e.Err.String(), "no Kubernetes access") {
		t.Errorf("the run does not say that it has no Kubernetes access:\n%s\n%s", e.Out, e.Err)
	}
}

// partition is the fleet of one partition: a cluster in each of its
// regions, home is the region of the run, and sts is where a credential
// check must go.
type partition struct {
	home    string
	regions []string
	sts     string
}

func (p partition) world() *fleettest.World {
	world := fleettest.New()
	world.Edit(func(s *fleettest.State) {
		for _, r := range p.regions {
			s.Clusters = append(s.Clusters, &fleettest.Cluster{Name: "prod-" + r, Region: r, Version: "1.33"})
		}
	})
	return world
}

// In GovCloud and China a sweep covers the regions of that partition, and
// the credential check after a sweep that no region answered goes to STS
// in a region of that partition that every account has, not us-east-1.
func TestPartitions(t *testing.T) {
	for _, p := range []partition{
		{home: "us-gov-east-1", regions: []string{"us-gov-east-1", "us-gov-west-1"}, sts: "us-gov-west-1"},
		{home: "cn-northwest-1", regions: []string{"cn-north-1", "cn-northwest-1"}, sts: "cn-north-1"},
	} {
		t.Run(p.home+"/sweep", func(t *testing.T) { testPartitionSweep(t, p) })
		t.Run(p.home+"/credential check", func(t *testing.T) { testPartitionCredentialCheck(t, p) })
	}
}

func testPartitionSweep(t *testing.T, p partition) {
	t.Helper()
	e := cmdtest.NewFake(t, p.world(), p.home)
	if err := e.Run(cmdutiltest.Context(t), "cluster", "list", "-A", "-o", "json"); err != nil {
		t.Fatal(err)
	}
	var swept []string
	for _, r := range e.Server.Requests() {
		if r.Operation == fleettest.EKSListClusters && !slices.Contains(swept, r.Region) {
			swept = append(swept, r.Region)
		}
	}
	slices.Sort(swept)
	if !slices.Equal(swept, p.regions) {
		t.Errorf("swept %v, want %v", swept, p.regions)
	}
	for _, r := range p.regions {
		if !strings.Contains(e.Out.String(), `"prod-`+r+`"`) {
			t.Errorf("no cluster of %s in the list:\n%s", r, e.Out)
		}
	}
}

func testPartitionCredentialCheck(t *testing.T, p partition) {
	t.Helper()
	world := p.world()
	for _, r := range p.regions {
		world.SetScopedFault(fleettest.Scope{Region: r, Service: "eks"}, fleettest.Fault{Unauthenticated: true})
	}
	e := cmdtest.NewFake(t, world, p.home)
	err := e.Run(cmdutiltest.Context(t), append([]string{"cluster", "list", "-o", "json"}, regionFlags(p.regions)...)...)
	if got := cmdutil.ExitCode(err); got != cmdutil.ExitFailure {
		t.Errorf("exit code = %d (%v), want 1", got, err)
	}
	var sts []string
	for _, r := range e.Server.Requests() {
		if r.Operation == fleettest.STSGetCallerIdentity {
			sts = append(sts, r.Region)
		}
	}
	if !slices.Equal(sts, []string{p.sts}) {
		t.Errorf("STS was asked in %v, want %s once", sts, p.sts)
	}
	if err == nil || !strings.Contains(err.Error(), "STS in "+p.sts) {
		t.Errorf("error = %v, want one that names STS in %s", err, p.sts)
	}
}

func regionFlags(regions []string) []string {
	var out []string
	for _, r := range regions {
		out = append(out, "-r", r)
	}
	return out
}
