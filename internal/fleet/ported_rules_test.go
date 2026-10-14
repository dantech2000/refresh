package fleet_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// answers returns the clients of a server in front of the fake. answer
// returns the JSON body for a request that the front answers itself, and
// false for one that it passes on. It is how a test gets an answer that
// EKS can give and the fake does not.
func answers(t *testing.T, srv *awsxtest.Server, answer func(r *http.Request) (body string, ok bool)) *awsx.Clients {
	t.Helper()
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if body, ok := answer(r); ok {
			w.Header().Set("Content-Type", r.Header.Get("Content-Type"))
			_, _ = w.Write([]byte(body))
			return
		}
		srv.ServeHTTP(w, r)
	}))
	t.Cleanup(front.Close)
	cfg := srv.Config(east)
	cfg.BaseEndpoint = aws.String(front.URL)
	return awsx.NewProvider(cfg).Clients(east)
}

// emptyAnswers is answers with an empty JSON object for the requests that
// match: a describe answer with no item in it.
func emptyAnswers(t *testing.T, srv *awsxtest.Server, match func(r *http.Request) bool) *awsx.Clients {
	t.Helper()
	return answers(t, srv, func(r *http.Request) (string, bool) { return "{}", match(r) })
}

// get matches the GET request for a path.
func get(path string) func(r *http.Request) bool {
	return func(r *http.Request) bool { return r.Method == http.MethodGet && r.URL.Path == path }
}

// A describe answer with no cluster, nodegroup, or add-on in it is an
// error or a failure of that item (REF-57). It is not a nil dereference,
// and not an item with empty fields.
func TestFetch_EmptyDescribeAnswers(t *testing.T) {
	seed := func(t *testing.T) *awsxtest.Server {
		t.Helper()
		world, srv, _ := nodegroupWorld(t)
		world.Edit(func(s *fleettest.State) {
			s.Clusters[0].Addons = []*fleettest.Addon{{Name: "vpc-cni", Version: "v1.19.0-eksbuild.1"}, {Name: "coredns", Version: "v1.11.4-eksbuild.2"}}
		})
		return srv
	}
	t.Run("the cluster", func(t *testing.T) {
		clients := emptyAnswers(t, seed(t), get("/clusters/prod"))
		var fetcher fleet.Fetcher
		snap, err := fetcher.Fetch(testContext(t), clients, "prod", fleet.Need{Nodegroups: true})
		if err == nil || !strings.Contains(err.Error(), "describing cluster prod: empty answer") {
			t.Fatalf("error = %v, want the empty answer of DescribeCluster", err)
		}
		if snap == nil || snap.Cluster.Name != "prod" || snap.Have.Cluster == fleet.Collected {
			t.Errorf("snapshot = %+v; want the name, and a cluster that is not collected", snap)
		}
	})
	t.Run("a nodegroup", func(t *testing.T) {
		clients := emptyAnswers(t, seed(t), get("/clusters/prod/node-groups/web"))
		var fetcher fleet.Fetcher
		snap, err := fetcher.Fetch(testContext(t), clients, "prod", fleet.Need{Nodegroups: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(snap.Failures) != 1 || snap.Failures[0].Kind != apidoc.FailureNodegroup || snap.Failures[0].Name != "web" ||
			!strings.Contains(snap.Failures[0].Error, "empty answer") {
			t.Errorf("failures = %+v, want the empty answer for web", snap.Failures)
		}
		if snap.Have.Nodegroups != fleet.Partial || len(snap.Nodegroups) != 6 {
			t.Errorf("Have.Nodegroups = %v with %d nodegroups, want Partial and a row for each of the 6", snap.Have.Nodegroups, len(snap.Nodegroups))
		}
		for _, n := range snap.Nodegroups {
			if (n.State == fleet.Collected) == (n.Name == "web") {
				t.Errorf("%s: state %v; only web is not collected", n.Name, n.State)
			}
		}
	})
	t.Run("an add-on", func(t *testing.T) {
		clients := emptyAnswers(t, seed(t), get("/clusters/prod/addons/coredns"))
		var fetcher fleet.Fetcher
		snap, err := fetcher.Fetch(testContext(t), clients, "prod", fleet.Need{Addons: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(snap.Failures) != 1 || snap.Failures[0].Kind != apidoc.FailureAddon || snap.Failures[0].Name != "coredns" ||
			!strings.Contains(snap.Failures[0].Error, "empty answer") {
			t.Errorf("failures = %+v, want the empty answer for coredns", snap.Failures)
		}
		if snap.Have.Addons != fleet.Partial || len(snap.Addons) != 2 {
			t.Errorf("Have.Addons = %v with %d add-ons, want Partial and a row for each of the 2", snap.Have.Addons, len(snap.Addons))
		}
	})
}

// An SSM parameter with no value is an error: the latest AMI is unknown.
// It is not "no newer AMI", which would show a stale nodegroup as current.
func TestFetch_EmptySSMParameter(t *testing.T) {
	_, srv, _ := nodegroupWorld(t)
	clients := emptyAnswers(t, srv, func(r *http.Request) bool { return r.Header.Get("X-Amz-Target") == "AmazonSSM.GetParameter" })
	var fetcher fleet.Fetcher
	snap, err := fetcher.Fetch(testContext(t), clients, "prod", fleet.Need{AMI: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range snap.Nodegroups {
		status, known := n.AMIStatus()
		// The status of batch and gpu does not need the lookup.
		if n.Name == "batch" || n.Name == "gpu" {
			continue
		}
		if known || n.AMI.Latest != "" {
			t.Errorf("%s: AMI status %s, known %t, latest %q; want unknown", n.Name, status, known, n.AMI.Latest)
		}
		if !slices.ContainsFunc(snap.Failures, func(f apidoc.Failure) bool {
			return f.Name == n.Name && strings.Contains(f.Error, "empty answer")
		}) {
			t.Errorf("%s: no failure for the empty parameter in %+v", n.Name, snap.Failures)
		}
	}
	if snap.Have.Nodegroups != fleet.Partial {
		t.Errorf("Have.Nodegroups = %v, want Partial", snap.Have.Nodegroups)
	}
}

// When the launch template of a nodegroup cannot be read and no instance
// shows an AMI, the failure is reported, and it names the template call.
func TestFetch_TemplateDeniedAndNoInstance(t *testing.T) {
	world, _, provider := nodegroupWorld(t)
	world.Edit(func(s *fleettest.State) {
		// The nodegroup has a launch template, and its group has no
		// instance that could show the AMI.
		s.Clusters[0].Nodegroups = append(s.Clusters[0].Nodegroups, &fleettest.Nodegroup{
			Name: "templated", Version: "1.33", LaunchTemplate: &fleettest.LaunchTemplate{ID: "lt-app", Version: "1"}, AutoScalingGroups: []string{"asg-templated"},
		})
		s.AutoScalingGroups = append(s.AutoScalingGroups, fleettest.AutoScalingGroup{Name: "asg-templated"})
		s.LaunchTemplateVersions = append(s.LaunchTemplateVersions, fleettest.LaunchTemplateVersion{TemplateID: "lt-app", Version: "1", ImageID: "ami-stale"})
	})
	world.SetFault(fleettest.EC2DescribeLaunchTemplateVersions, fleettest.Fault{Deny: true})
	var fetcher fleet.Fetcher
	snap, err := fetcher.Fetch(testContext(t), provider.Clients(east), "prod", fleet.Need{AMI: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Nodegroup templated AccessDenied ec2:DescribeLaunchTemplateVersions"}
	if diff := cmp.Diff(want, failureKeys(snap.Failures)); diff != "" {
		t.Errorf("Failures (-want +got):\n%s", diff)
	}
	i := slices.IndexFunc(snap.Nodegroups, func(n fleet.Nodegroup) bool { return n.Name == "templated" })
	if status, known := snap.Nodegroups[i].AMIStatus(); known {
		t.Errorf("templated: AMI status %s is known; its current AMI was not read", status)
	}
}

// The newest version of an add-on can be on the last page of
// DescribeAddonVersions. The read follows the tokens: a read of the first
// page alone reports an old version as the latest.
func TestFetch_AddonCatalogueIsPaged(t *testing.T) {
	world, srv, _ := newFleet(t, 8, east+"/prod")
	world.Edit(func(s *fleettest.State) {
		s.Clusters[0].Addons = []*fleettest.Addon{{Name: "vpc-cni", Version: "v1.19.0-eksbuild.1"}}
	})
	// EKS answers in three pages, and the newest version is on the last.
	pages := map[string]string{
		"":   `{"addons":[{"addonName":"vpc-cni","addonVersions":[{"addonVersion":"v1.19.0-eksbuild.1"}]}],"nextToken":"p2"}`,
		"p2": `{"addons":[{"addonName":"vpc-cni","addonVersions":[{"addonVersion":"v1.19.2-eksbuild.1"}]}],"nextToken":"p3"}`,
		"p3": `{"addons":[{"addonName":"vpc-cni","addonVersions":[{"addonVersion":"v1.20.0-eksbuild.3"},{"addonVersion":"v1.20.0-eksbuild.1"}]}]}`,
	}
	var asked atomic.Int32
	clients := answers(t, srv, func(r *http.Request) (string, bool) {
		if r.Method != http.MethodGet || r.URL.Path != "/addons/supported-versions" {
			return "", false
		}
		asked.Add(1)
		if q := r.URL.Query(); q.Get("addonName") != "vpc-cni" || q.Get("kubernetesVersion") != "1.33" {
			t.Errorf("the catalogue was asked for %s", r.URL.RawQuery)
		}
		return pages[r.URL.Query().Get("nextToken")], true
	})
	var fetcher fleet.Fetcher
	version, err := fleet.ParseVersion("1.33")
	if err != nil {
		t.Fatal(err)
	}
	got, err := fetcher.AddonVersions(testContext(t), clients, "vpc-cni", version)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"v1.20.0-eksbuild.3", "v1.20.0-eksbuild.1", "v1.19.2-eksbuild.1", "v1.19.0-eksbuild.1"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("versions (-want +got):\n%s", diff)
	}
	if n := asked.Load(); n != 3 {
		t.Errorf("%d DescribeAddonVersions requests, want one for each of the 3 pages", n)
	}
	snap, err := fetcher.Fresh().Fetch(testContext(t), clients, "prod", fleet.Need{LatestAddons: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Addons) != 1 || snap.Addons[0].Latest != want[0] {
		t.Errorf("add-ons = %+v, want vpc-cni with the latest version %s", snap.Addons, want[0])
	}
}

// When the context ends in the middle of the describes, every nodegroup
// that was listed is a row, and each one that was not read has a failure.
// None vanishes.
func TestFetch_ContextEndsWhileNodegroupsAreDescribed(t *testing.T) {
	_, srv, _ := nodegroupWorld(t)
	// One describe at a time, so the cancel lands between two of them.
	provider := awsx.NewProvider(srv.Config(east), func(o *awsx.ProviderOptions) { o.MaxInFlight = 1 })
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()
	var described atomic.Int32
	srv.OnRequest = func(_ context.Context, r awsxtest.Request) {
		if r.Operation == fleettest.EKSDescribeNodegroup && described.Add(1) == 3 {
			cancel()
		}
	}
	var fetcher fleet.Fetcher
	snap, _ := fetcher.Fetch(ctx, provider.Clients(east), "prod", fleet.Need{Nodegroups: true})
	var names []string
	unread := 0
	for _, n := range snap.Nodegroups {
		names = append(names, n.Name)
		if n.State == fleet.Collected {
			continue
		}
		unread++
		if !slices.ContainsFunc(snap.Failures, func(f apidoc.Failure) bool { return f.Kind == apidoc.FailureNodegroup && f.Name == n.Name }) {
			t.Errorf("%s was not read and has no failure in %+v", n.Name, snap.Failures)
		}
	}
	slices.Sort(names)
	if diff := cmp.Diff([]string{"api", "batch", "empty", "gpu", "old", "web"}, names); diff != "" {
		t.Errorf("nodegroups (-want +got):\n%s", diff)
	}
	if unread == 0 || snap.Have.Nodegroups == fleet.Collected {
		t.Errorf("%d nodegroups not read, Have.Nodegroups = %v; the context ended before every describe", unread, snap.Have.Nodegroups)
	}
}
