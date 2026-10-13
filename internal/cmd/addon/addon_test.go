package addon_test

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
// cluster prod with three add-ons, and the clusters prod-eu and dev with
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
				if !strings.Contains(e.Err.String(), "warning: addon prod/coredns (us-east-1): Throttled:") {
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
		gate.Wave(fleettest.EKSDescribeAddon, fleettest.EKSDescribeAddon, fleettest.EKSDescribeAddon)
		gate.Done()
		if err := <-done; err != nil {
			t.Fatalf("run: %v", err)
		}
		if n := len(e.Server.Requests()); n != 5 {
			t.Errorf("requests = %v, want 5", e.Operations())
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
// It passes at the moment one add-on is at the server.
func TestList_Deadline(t *testing.T) {
	e := newFake(t)
	ctx, expire := cmdutiltest.Deadline(cmdutiltest.Context(t))
	e.Server.OnRequest = func(hctx context.Context, r awsxtest.Request) {
		if r.Operation == fleettest.EKSDescribeAddon {
			expire()
			<-hctx.Done()
		}
	}
	err := e.Run(ctx, "addon", "list", "-c", "prod", "-o", "json")
	if got := cmdutil.ExitCode(err); got != cmdutil.ExitIncomplete {
		t.Fatalf("exit code = %d (%v), want %d", got, err, cmdutil.ExitIncomplete)
	}
	cmdutiltest.ValidateJSON(t, apidoc.KindAddonList, e.Out.Bytes())
	if n := strings.Count(e.Out.String(), `"incomplete": true`); n != 3 {
		t.Errorf("incomplete add-ons = %d, want 3:\n%s", n, e.Out.String())
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
	err := e.Run(ctx, "addon", "list", "-c", "prod", "-o", "json")
	if got := cmdutil.ExitCode(err); got != cmdutil.ExitFailure {
		t.Fatalf("exit code = %d (%v), want %d", got, err, cmdutil.ExitFailure)
	}
}
