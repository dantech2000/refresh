package addon_test

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/cmd/cmdtest"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
	"github.com/dantech2000/refresh/internal/contexts"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

var update = flag.Bool("update", false, "write the golden files in testdata")

const east = "us-east-1"

// newFake returns a fake AWS with the fleet of the golden files: the
// cluster prod with four add-ons, and the clusters prod-eu and dev with
// none.
func newFake(t *testing.T) *cmdtest.Fake {
	t.Helper()
	world := fleettest.New()
	world.Edit(func(s *fleettest.State) {
		s.Clusters = append(s.Clusters,
			&fleettest.Cluster{Name: "prod", Region: east, Version: "1.33", Addons: []*fleettest.Addon{
				{Name: "vpc-cni", Version: "v1.19.0-eksbuild.1", ServiceAccountRoleARN: "arn:aws:iam::123456789012:role/vpc-cni",
					ConfigurationValues: "env:\n  ENABLE_PREFIX_DELEGATION: \"true\"\n  WARM_PREFIX_TARGET: \"1\"\n"},
				{Name: "coredns", Version: "v1.11.3-eksbuild.2", Status: fleettest.StatusDegraded, HealthIssues: []fleettest.Issue{
					{Code: "InsufficientNumberOfReplicas", Message: "The add-on is unhealthy because it has no replicas", ResourceIDs: []string{"kube-system/coredns"}},
				}},
				{Name: "kube-proxy", Version: "v1.33.0-eksbuild.2", Status: fleettest.StatusUpdating},
				// ACTIVE, and EKS reports an issue for it: it is not healthy.
				{Name: "metrics-server", Version: "v0.7.2-eksbuild.1", HealthIssues: []fleettest.Issue{
					{Code: "InsufficientNumberOfReplicas", Message: "The add-on has fewer replicas than it needs", ResourceIDs: []string{"kube-system/metrics-server"}},
				}},
			}},
			&fleettest.Cluster{Name: "prod-eu", Region: east, Version: "1.33"},
			&fleettest.Cluster{Name: "dev", Region: east, Version: "1.32"},
		)
	})
	return cmdtest.NewFake(t, world, east)
}

func TestList_Formats(t *testing.T) {
	for _, tt := range []struct{ format, file string }{
		{"table", "list.txt"}, {"plain", "list.tsv"}, {"json", "list.json"}, {"yaml", "list.yaml"},
	} {
		t.Run(tt.format, func(t *testing.T) {
			e := newFake(t)
			if err := e.Run(cmdutiltest.Context(t), "addon", "list", "-c", "prod", "-o", tt.format); err != nil {
				t.Fatalf("run: %v", err)
			}
			if e.Err.Len() != 0 {
				t.Errorf("stderr = %q, want nothing", e.Err.String())
			}
			cmdutiltest.Golden(t, tt.file, e.Out.String(), *update)
			switch tt.format {
			case "json":
				cmdutiltest.ValidateJSON(t, apidoc.KindAddonList, e.Out.Bytes())
			case "yaml":
				cmdutiltest.ValidateYAML(t, apidoc.KindAddonList, e.Out.Bytes())
			}
		})
	}
}

func TestDescribe_Formats(t *testing.T) {
	for _, tt := range []struct{ addon, format, file string }{
		{"vpc-cni", "table", "describe.txt"}, {"vpc-cni", "plain", "describe.tsv"},
		{"vpc-cni", "json", "describe.json"}, {"vpc-cni", "yaml", "describe.yaml"},
		{"coredns", "table", "describe-issues.txt"}, {"coredns", "plain", "describe-issues.tsv"},
	} {
		t.Run(tt.file, func(t *testing.T) {
			e := newFake(t)
			if err := e.Run(cmdutiltest.Context(t), "addon", "describe", tt.addon, "-c", "prod", "-o", tt.format); err != nil {
				t.Fatalf("run: %v", err)
			}
			if e.Err.Len() != 0 {
				t.Errorf("stderr = %q, want nothing", e.Err.String())
			}
			cmdutiltest.Golden(t, tt.file, e.Out.String(), *update)
			switch tt.format {
			case "json":
				cmdutiltest.ValidateJSON(t, apidoc.KindAddonDescription, e.Out.Bytes())
			case "yaml":
				cmdutiltest.ValidateYAML(t, apidoc.KindAddonDescription, e.Out.Bytes())
			}
		})
	}
}

// One add-on that cannot be described stays in the list, and the run exits
// 4 after it printed the list.
func TestList_Partial(t *testing.T) {
	for _, tt := range []struct{ format, file string }{{"table", "partial.txt"}, {"json", "partial.json"}} {
		t.Run(tt.format, func(t *testing.T) {
			e := newFake(t)
			e.World.SetScopedFault(fleettest.Scope{Operation: fleettest.EKSDescribeAddon, Addon: "coredns"}, fleettest.Fault{Throttle: 100})
			err := e.Run(cmdutiltest.Context(t), "addon", "list", "-c", "prod", "-o", tt.format)
			if got := cmdutil.ExitCode(err); got != cmdutil.ExitIncomplete {
				t.Errorf("exit code = %d (%v), want %d", got, err, cmdutil.ExitIncomplete)
			}
			cmdutiltest.Golden(t, tt.file, e.Out.String(), *update)
			if tt.format == "json" {
				cmdutiltest.ValidateJSON(t, apidoc.KindAddonList, e.Out.Bytes())
				if !strings.Contains(e.Err.String(), "warning: addon prod/coredns (us-east-1): Throttled (eks:DescribeAddon):") {
					t.Errorf("stderr = %q, want the warning line of the failure", e.Err.String())
				}
			}
		})
	}
}

func TestList_Empty(t *testing.T) {
	e := newFake(t)
	if err := e.Run(cmdutiltest.Context(t), "addon", "list", "-c", "dev", "-o", "json"); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, want := range []string{`"addons": []`, `"failures": []`, `"cluster": "dev"`} {
		if !strings.Contains(e.Out.String(), want) {
			t.Errorf("the document has no %s:\n%s", want, e.Out.String())
		}
	}
	cmdutiltest.ValidateJSON(t, apidoc.KindAddonList, e.Out.Bytes())
}

// "addon list" is two round trips and "addon describe" is one. No call
// lists clusters to check the name: a name is exact.
func TestWavesAndCalls(t *testing.T) {
	t.Run("list", func(t *testing.T) {
		e := newFake(t)
		gate := awsxtest.NewGate(t, e.Server.Handler)
		done := make(chan error, 1)
		go func() { done <- e.Run(cmdutiltest.Context(t), "addon", "list", "-c", "prod", "-o", "json") }()
		gate.Wave(fleettest.EKSDescribeCluster, fleettest.EKSListAddons)
		gate.Wave(fleettest.EKSDescribeAddon, fleettest.EKSDescribeAddon, fleettest.EKSDescribeAddon, fleettest.EKSDescribeAddon)
		gate.Done()
		if err := <-done; err != nil {
			t.Fatalf("run: %v", err)
		}
		if n := len(e.Server.Requests()); n != 6 {
			t.Errorf("requests = %v, want 6", e.Operations())
		}
	})
	t.Run("describe", func(t *testing.T) {
		e := newFake(t)
		gate := awsxtest.NewGate(t, e.Server.Handler)
		done := make(chan error, 1)
		go func() {
			done <- e.Run(cmdutiltest.Context(t), "addon", "describe", "vpc-cni", "-c", "prod", "-o", "json")
		}()
		gate.Wave(fleettest.EKSDescribeCluster, fleettest.EKSDescribeAddon)
		gate.Done()
		if err := <-done; err != nil {
			t.Fatalf("run: %v", err)
		}
		if n := len(e.Server.Requests()); n != 2 {
			t.Errorf("requests = %v, want 2", e.Operations())
		}
	})
}

// A name that does not exist costs one list call, for the names that are
// close to it.
func TestNotFound(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		fault    func(*fleettest.World)
		want     string
		wantList fleettest.Operation
	}{
		{
			name:     "cluster, in list",
			args:     []string{"addon", "list", "-c", "prd"},
			want:     "cluster \"prd\" not found in us-east-1\nDid you mean:\n  prod\n  prod-eu\nList the clusters with: refresh cluster list -r us-east-1",
			wantList: fleettest.EKSListClusters,
		},
		{
			name:     "cluster, in describe",
			args:     []string{"addon", "describe", "vpc-cni", "-c", "PROD"},
			want:     "cluster \"PROD\" not found in us-east-1\nDid you mean:\n  prod\n  prod-eu\nList the clusters with: refresh cluster list -r us-east-1",
			wantList: fleettest.EKSListClusters,
		},
		{
			name:     "nothing close",
			args:     []string{"addon", "list", "-c", "zzzzzzzz"},
			want:     "cluster \"zzzzzzzz\" not found in us-east-1\nList the clusters with: refresh cluster list -r us-east-1",
			wantList: fleettest.EKSListClusters,
		},
		{
			name:     "the list for the suggestions fails",
			args:     []string{"addon", "list", "-c", "prd"},
			fault:    func(w *fleettest.World) { w.SetFault(fleettest.EKSListClusters, fleettest.Fault{Deny: true}) },
			want:     "cluster \"prd\" not found in us-east-1\nList the clusters with: refresh cluster list -r us-east-1",
			wantList: fleettest.EKSListClusters,
		},
		{
			name:     "add-on",
			args:     []string{"addon", "describe", "vpc", "-c", "prod"},
			want:     "add-on \"vpc\" not found in cluster prod\nDid you mean:\n  vpc-cni\nList the add-ons with: refresh addon list -c prod",
			wantList: fleettest.EKSListAddons,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newFake(t)
			if tt.fault != nil {
				tt.fault(e.World)
			}
			err := e.Run(cmdutiltest.Context(t), tt.args...)
			if err == nil {
				t.Fatal("no error")
			}
			if diff := cmp.Diff(tt.want, err.Error()); diff != "" {
				t.Errorf("error (-want +got):\n%s", diff)
			}
			if got := cmdutil.ExitCode(err); got != cmdutil.ExitFailure {
				t.Errorf("exit code = %d, want %d", got, cmdutil.ExitFailure)
			}
			if e.Out.Len() != 0 {
				t.Errorf("stdout = %q, want nothing", e.Out.String())
			}
			if n := e.Count(tt.wantList); n != 1 {
				t.Errorf("%s calls = %d, want 1, on the failure path only", tt.wantList, n)
			}
		})
	}
}

// A cluster that cannot be read for another reason is an error with the
// AWS help text, and no list call.
func TestList_ClusterDenied(t *testing.T) {
	e := newFake(t)
	e.World.SetFault(fleettest.EKSDescribeCluster, fleettest.Fault{Deny: true})
	err := e.Run(cmdutiltest.Context(t), "addon", "list", "-c", "prod")
	if err == nil || !strings.Contains(err.Error(), "eks:DescribeCluster") {
		t.Fatalf("error = %v, want one that names eks:DescribeCluster", err)
	}
	if n := e.Count(fleettest.EKSListClusters); n != 0 {
		t.Errorf("ListClusters calls = %d, want none", n)
	}
}

// The cluster comes from the flag, then REFRESH_CLUSTER, then the active
// context, then the current kubeconfig context.
func TestClusterResolution(t *testing.T) {
	kubeconfig := "apiVersion: v1\nkind: Config\ncurrent-context: c\n" +
		"clusters:\n- name: arn:aws:eks:us-east-1:123456789012:cluster/prod-eu\n  cluster: {server: https://example.invalid}\n" +
		"contexts:\n- name: c\n  context: {cluster: \"arn:aws:eks:us-east-1:123456789012:cluster/prod-eu\", user: u}\n" +
		"users:\n- name: u\n  user: {}\n"
	tests := []struct {
		name       string
		flag       string
		env        string
		context    string
		kubeconfig bool
		want       string // the cluster in the document
		wantErr    string
	}{
		{name: "flag wins", flag: "prod", env: "dev", context: "dev", kubeconfig: true, want: "prod"},
		{name: "environment", env: "dev", context: "prod", kubeconfig: true, want: "dev"},
		{name: "context", context: "prod", kubeconfig: true, want: "prod"},
		{name: "kubeconfig", kubeconfig: true, want: "prod-eu"},
		{name: "nothing", wantErr: "no cluster specified\nSet one with:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newFake(t)
			t.Setenv(cmdutil.EnvCluster, tt.env)
			if tt.context != "" {
				cmdutiltest.NewContextStore(t, e.Factory, &contexts.File{
					Current:  "ctx",
					Contexts: map[string]contexts.Context{"ctx": {Cluster: tt.context, Region: east}},
				})
			}
			if tt.kubeconfig {
				path := filepath.Join(t.TempDir(), "config")
				if err := os.WriteFile(path, []byte(kubeconfig), 0o600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("KUBECONFIG", path)
			}
			args := []string{"addon", "list", "-o", "plain"}
			if tt.flag != "" {
				args = append(args, "-c", tt.flag)
			}
			err := e.Run(cmdutiltest.Context(t), args...)
			if tt.wantErr != "" {
				if err == nil || !strings.HasPrefix(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want one that starts with %q", err, tt.wantErr)
				}
				if n := len(e.Server.Requests()); n != 0 {
					t.Errorf("requests = %v, want none", e.Operations())
				}
				return
			}
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			var described string
			for _, r := range e.Server.Requests() {
				if r.Operation == fleettest.EKSDescribeCluster {
					described, _ = r.Input["name"].(string)
				}
			}
			if described != tt.want {
				t.Errorf("the command read cluster %q, want %q", described, tt.want)
			}
		})
	}
}

func TestInvalidInput(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"describe without a name", []string{"addon", "describe", "-c", "prod"}, "accepts 1 arg(s), received 0"},
		{"describe with two names", []string{"addon", "describe", "a", "b", "-c", "prod"}, "accepts 1 arg(s), received 2"},
		{"list with an argument", []string{"addon", "list", "prod"}, `unknown command "prod"`},
		{"two regions", []string{"addon", "list", "-c", "prod", "-r", "us-east-1,eu-west-1"}, "one cluster in one region"},
		{"an unknown format", []string{"addon", "list", "-c", "prod", "-o", "wide"}, "must be one of: table, json, yaml, plain"},
		{"a watch of a machine format", []string{"addon", "list", "-c", "prod", "-w", "-o", "json"}, "--watch cannot be used with -o json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newFake(t)
			err := e.Run(cmdutiltest.Context(t), tt.args...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want one that contains %q", err, tt.want)
			}
			if n := len(e.Server.Requests()); n != 0 {
				t.Errorf("requests = %v, want none", e.Operations())
			}
		})
	}
}

// A deadline while the add-ons are read is a partial result with exit 4.
// It passes when the cluster is read and two add-ons are at the server.
//
// Two request slots make the order certain. The add-on list waits at the
// server until the cluster call is there too, so the cluster holds the
// other slot. The first add-on call then waits at the server for the
// second one, and the second one gets a slot only when the cluster call
// has given its slot back: with its answer read.
func TestList_Deadline(t *testing.T) {
	e := newFake(t)
	ctx, expire := awsxtest.Deadline(cmdutiltest.Context(t))
	clusterSent := make(chan struct{})
	secondAddon := make(chan struct{})
	addons := 0
	var mu sync.Mutex
	e.Server.OnRequest = func(hctx context.Context, r awsxtest.Request) {
		switch r.Operation {
		case fleettest.EKSDescribeCluster:
			close(clusterSent)
		case fleettest.EKSListAddons:
			select {
			case <-clusterSent:
			case <-hctx.Done():
			}
		case fleettest.EKSDescribeAddon:
			mu.Lock()
			if addons++; addons == 2 {
				close(secondAddon)
			}
			mu.Unlock()
			select {
			case <-secondAddon:
				expire()
			case <-hctx.Done():
			}
			<-hctx.Done()
		}
	}
	err := e.Run(ctx, "addon", "list", "-c", "prod", "-o", "json", "--max-concurrency", "2")
	if got := cmdutil.ExitCode(err); got != cmdutil.ExitIncomplete {
		t.Fatalf("exit code = %d (%v), want %d", got, err, cmdutil.ExitIncomplete)
	}
	cmdutiltest.ValidateJSON(t, apidoc.KindAddonList, e.Out.Bytes())
	if n := strings.Count(e.Out.String(), `"incomplete": true`); n != 4 {
		t.Errorf("incomplete add-ons = %d, want 4:\n%s", n, e.Out.String())
	}
}

// A deadline while the cluster is read is an error, exit 1, as any other
// failure to read the cluster is: the list has no cluster to belong to.
func TestList_DeadlineAtTheCluster(t *testing.T) {
	e := newFake(t)
	ctx, expire := awsxtest.Deadline(cmdutiltest.Context(t))
	e.Server.OnRequest = func(hctx context.Context, r awsxtest.Request) {
		if r.Operation == fleettest.EKSDescribeCluster {
			expire()
			<-hctx.Done()
		}
	}
	err := e.Run(ctx, "addon", "list", "-c", "prod", "-o", "json")
	if got := cmdutil.ExitCode(err); got != cmdutil.ExitFailure {
		t.Fatalf("exit code = %d (%v), want %d", got, err, cmdutil.ExitFailure)
	}
	if !strings.Contains(err.Error(), "timed out while describing cluster prod") {
		t.Errorf("error = %v, want one that names the cluster read", err)
	}
	if e.Out.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", e.Out.String())
	}
}

// Ctrl+C is exit 1, also after the list was printed.
func TestList_Interrupt(t *testing.T) {
	e := newFake(t)
	ctx, cancel := context.WithCancel(cmdutiltest.Context(t))
	defer cancel()
	e.Server.OnRequest = func(hctx context.Context, r awsxtest.Request) {
		if r.Operation == fleettest.EKSDescribeAddon {
			cancel()
			<-hctx.Done()
		}
	}
	// One request slot: the calls go out one at a time, in the order that
	// they asked for the slot. The cluster asked before any call of the
	// second wave, so it is answered before the run is cut short.
	err := e.Run(ctx, "addon", "list", "-c", "prod", "-o", "json", "--max-concurrency", "1")
	if got := cmdutil.ExitCode(err); got != cmdutil.ExitFailure {
		t.Fatalf("exit code = %d (%v), want %d", got, err, cmdutil.ExitFailure)
	}
}

// A watch reads again after each interval and redraws the table. A read
// with a failure names it and the watch goes on. Ctrl+C ends the watch
// with ErrStopped, which is exit 0.
func TestList_Watch(t *testing.T) {
	e := newFake(t)
	e.Factory.IOStreams.OutTTY = true
	e.Factory.IOStreams.TermSize = func() (int, int) { return 120, 40 }
	ctx, cancel := context.WithCancel(cmdutiltest.Context(t))
	defer cancel()

	scope := fleettest.Scope{Operation: fleettest.EKSDescribeAddon, Addon: "coredns"}
	pauses := 0
	tick := make(chan time.Time, 1)
	e.Factory.After = func(time.Duration) <-chan time.Time {
		pauses++
		switch pauses {
		case 1:
			// The second read cannot describe one item.
			e.World.SetScopedFault(scope, fleettest.Fault{Deny: true})
		case 2:
			e.World.SetScopedFault(scope, fleettest.Fault{})
		case 3:
			cancel()
			return make(chan time.Time)
		}
		tick <- time.Time{}
		return tick
	}
	err := e.Run(ctx, "addon", "list", "-c", "prod", "-w")
	if !errors.Is(err, cmdutil.ErrStopped) {
		t.Fatalf("error = %v, want ErrStopped", err)
	}
	frames := strings.Split(e.Out.String(), "\x1b[H\x1b[2J")[1:]
	if len(frames) != 3 {
		t.Fatalf("the screen was drawn %d times, want 3:\n%q", len(frames), e.Out.String())
	}
	if frames[0] != frames[2] || !strings.Contains(frames[0], "coredns") || strings.Contains(frames[0], "INCOMPLETE DATA") {
		t.Errorf("the first and the third read are not the whole table:\n%s\n%s", frames[0], frames[2])
	}
	if !strings.Contains(frames[1], "INCOMPLETE DATA") || !strings.Contains(frames[1], "prod/coredns (us-east-1): AccessDenied") {
		t.Errorf("the second read does not name what it could not read:\n%s", frames[1])
	}
}
