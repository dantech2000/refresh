package nodegroup_test

import (
	"context"
	"errors"
	"flag"
	"path/filepath"
	"strings"
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

var update = flag.Bool("update", false, "write the golden files in testdata")

const east = "us-east-1"

// newFake returns a fake AWS with the fleet of the golden files: the
// cluster prod on 1.33 with one nodegroup for each AMI status.
func newFake(t *testing.T) *cmdtest.Fake {
	t.Helper()
	world := fleettest.New()
	world.Edit(func(s *fleettest.State) {
		s.Clusters = append(s.Clusters, &fleettest.Cluster{Name: "prod", Region: east, Version: "1.33", Nodegroups: []*fleettest.Nodegroup{
			{Name: "web", Version: "1.33", AutoScalingGroups: []string{"asg-web"}, InstanceTypes: []string{"m5.large", "m5a.large"}, Desired: 6, Min: 2, Max: 12,
				HealthIssues: []fleettest.Issue{{Code: "AsgInstanceLaunchFailures", Message: "Could not launch an instance", ResourceIDs: []string{"asg-web"}}}},
			{Name: "api", Version: "1.33", AutoScalingGroups: []string{"asg-api"}, CapacityType: "SPOT", Desired: 3, Min: 3, Max: 3},
			{Name: "gpu", Version: "1.33", AMIType: "CUSTOM", InstanceTypes: []string{"g5.xlarge"}, LaunchTemplate: &fleettest.LaunchTemplate{ID: "lt-gpu", Version: "3"}, Desired: 1, Min: 0, Max: 2},
			{Name: "batch", Version: "1.33", Status: fleettest.StatusUpdating, AutoScalingGroups: []string{"asg-batch"}, Desired: 10, Min: 0, Max: 40},
			{Name: "empty", Version: "1.33", AutoScalingGroups: []string{"asg-empty"}, Desired: 0, Min: 0, Max: 5},
			{Name: "old", Version: "1.32", AutoScalingGroups: []string{"asg-old"}},
		}})
		s.AutoScalingGroups = []fleettest.AutoScalingGroup{
			{Name: "asg-web", InstanceIDs: []string{"i-0web1", "i-0web2"}},
			{Name: "asg-api", InstanceIDs: []string{"i-0api1"}},
			{Name: "asg-batch", InstanceIDs: []string{"i-0batch1"}},
			{Name: "asg-empty"},
			{Name: "asg-old", InstanceIDs: []string{"i-0old1"}},
		}
		launched := time.Date(2026, 3, 4, 5, 6, 0, 0, time.UTC)
		s.Instances = []fleettest.Instance{
			{ID: "i-0web1", ImageID: "ami-stale", Type: "m5.large", AvailabilityZone: "us-east-1a", LaunchTime: launched},
			{ID: "i-0web2", ImageID: "ami-stale", Type: "m5a.large", Lifecycle: "spot", AvailabilityZone: "us-east-1b", State: "pending", LaunchTime: launched},
			{ID: "i-0api1", ImageID: "ami-latest133"},
			{ID: "i-0batch1", ImageID: "ami-stale"},
			{ID: "i-0old1", ImageID: "ami-latest132"},
		}
		s.LaunchTemplateVersions = []fleettest.LaunchTemplateVersion{{TemplateID: "lt-gpu", Version: "3", ImageID: "ami-custom"}}
	})
	return cmdtest.NewFake(t, world, east)
}

func validate(t *testing.T, kind apidoc.Kind, format string, e *cmdtest.Fake) {
	t.Helper()
	switch format {
	case "json":
		cmdutiltest.ValidateJSON(t, kind, e.Out.Bytes())
	case "yaml":
		cmdutiltest.ValidateYAML(t, kind, e.Out.Bytes())
	}
}

func TestList_Formats(t *testing.T) {
	for _, tt := range []struct{ format, file string }{
		{"table", "list.txt"}, {"plain", "list.tsv"}, {"json", "list.json"}, {"yaml", "list.yaml"},
	} {
		t.Run(tt.format, func(t *testing.T) {
			e := newFake(t)
			if err := e.Run(cmdutiltest.Context(t), "nodegroup", "list", "-c", "prod", "-o", tt.format); err != nil {
				t.Fatalf("run: %v", err)
			}
			if e.Err.Len() != 0 {
				t.Errorf("stderr = %q, want nothing", e.Err.String())
			}
			cmdutiltest.Golden(t, tt.file, e.Out.String(), *update)
			validate(t, apidoc.KindNodegroupList, tt.format, e)
		})
	}
}

func TestDescribe_Formats(t *testing.T) {
	for _, tt := range []struct {
		format, file string
		args         []string
	}{
		{"table", "describe.txt", nil}, {"plain", "describe.tsv", nil},
		{"json", "describe.json", nil}, {"yaml", "describe.yaml", nil},
		{"table", "describe-instances.txt", []string{"--show-instances"}},
		{"plain", "describe-instances.tsv", []string{"--show-instances"}},
		{"json", "describe-instances.json", []string{"--show-instances"}},
	} {
		t.Run(tt.file, func(t *testing.T) {
			e := newFake(t)
			args := append([]string{"nodegroup", "describe", "web", "-c", "prod", "-o", tt.format}, tt.args...)
			if err := e.Run(cmdutiltest.Context(t), args...); err != nil {
				t.Fatalf("run: %v", err)
			}
			if e.Err.Len() != 0 {
				t.Errorf("stderr = %q, want nothing", e.Err.String())
			}
			cmdutiltest.Golden(t, tt.file, e.Out.String(), *update)
			validate(t, apidoc.KindNodegroupDescription, tt.format, e)
		})
	}
}

// A failed AMI lookup is a failure of its nodegroups: the list still shows
// them, their AMI is unknown, and the run exits 4.
func TestList_AMILookupFails(t *testing.T) {
	for _, tt := range []struct{ format, file string }{{"table", "partial.txt"}, {"json", "partial.json"}} {
		t.Run(tt.format, func(t *testing.T) {
			e := newFake(t)
			e.World.SetFault(fleettest.SSMGetParameter, fleettest.Fault{Deny: true})
			e.World.SetScopedFault(fleettest.Scope{Operation: fleettest.EKSDescribeNodegroup, Nodegroup: "api"}, fleettest.Fault{Deny: true})
			err := e.Run(cmdutiltest.Context(t), "nodegroup", "list", "-c", "prod", "-o", tt.format)
			if got := cmdutil.ExitCode(err); got != cmdutil.ExitIncomplete {
				t.Errorf("exit code = %d (%v), want %d", got, err, cmdutil.ExitIncomplete)
			}
			cmdutiltest.Golden(t, tt.file, e.Out.String(), *update)
			validate(t, apidoc.KindNodegroupList, tt.format, e)
		})
	}
}

// An instance that is gone during a roll costs one nodegroup its current
// AMI. It is not a failure, and the run exits 0.
func TestList_InstanceThatIsGone(t *testing.T) {
	e := newFake(t)
	e.World.Edit(func(s *fleettest.State) { s.Instances = s.Instances[:2] })
	if err := e.Run(cmdutiltest.Context(t), "nodegroup", "list", "-c", "prod", "-o", "plain"); err != nil {
		t.Fatalf("run: %v", err)
	}
	want := []string{"Unknown", "Updating", "Unknown", "Custom", "Unknown", "Outdated"}
	var got []string
	for _, line := range strings.Split(strings.TrimSpace(e.Out.String()), "\n")[1:] {
		got = append(got, strings.Split(line, "\t")[4])
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("AMI column (-want +got):\n%s", diff)
	}
}

// "nodegroup list" is four round trips and "nodegroup describe" three.
func TestWaves(t *testing.T) {
	t.Run("list", func(t *testing.T) {
		e := newFake(t)
		gate := awsxtest.NewGate(t, e.Server.Handler)
		done := make(chan error, 1)
		go func() { done <- e.Run(cmdutiltest.Context(t), "nodegroup", "list", "-c", "prod", "-o", "json") }()
		gate.Wave(fleettest.EKSDescribeCluster, fleettest.EKSListNodegroups)
		gate.Wave(fleettest.EKSDescribeNodegroup, fleettest.EKSDescribeNodegroup, fleettest.EKSDescribeNodegroup,
			fleettest.EKSDescribeNodegroup, fleettest.EKSDescribeNodegroup, fleettest.EKSDescribeNodegroup)
		// One SSM call for each of the two versions, the template of the
		// one nodegroup that has one, and every group in one call.
		gate.Wave(fleettest.SSMGetParameter, fleettest.SSMGetParameter, fleettest.EC2DescribeLaunchTemplateVersions,
			fleettest.AutoScalingDescribeAutoScalingGroups)
		gate.Wave(fleettest.EC2DescribeInstances)
		gate.Done()
		if err := <-done; err != nil {
			t.Fatalf("run: %v", err)
		}
	})
	t.Run("describe", func(t *testing.T) {
		e := newFake(t)
		gate := awsxtest.NewGate(t, e.Server.Handler)
		done := make(chan error, 1)
		go func() {
			done <- e.Run(cmdutiltest.Context(t), "nodegroup", "describe", "web", "-c", "prod", "--show-instances", "-o", "json")
		}()
		gate.Wave(fleettest.EKSDescribeCluster, fleettest.EKSDescribeNodegroup)
		gate.Wave(fleettest.SSMGetParameter, fleettest.AutoScalingDescribeAutoScalingGroups)
		gate.Wave(fleettest.EC2DescribeInstances)
		gate.Done()
		if err := <-done; err != nil {
			t.Fatalf("run: %v", err)
		}
		if n := e.Count(fleettest.EKSListNodegroups) + e.Count(fleettest.EKSListClusters); n != 0 {
			t.Errorf("list calls = %d, want none: the names are exact", n)
		}
	})
}

func TestList_SortAndFilter(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"by name", nil, []string{"api", "batch", "empty", "gpu", "old", "web"}},
		{"by nodes, descending", []string{"--sort", "nodes", "--desc"}, []string{"batch", "web", "api", "old", "gpu", "empty"}},
		{"by version", []string{"--sort", "version"}, []string{"old", "api", "batch", "empty", "gpu", "web"}},
		{"by AMI status", []string{"--sort", "amiStatus"}, []string{"gpu", "api", "old", "web", "empty", "batch"}},
		{"by instance type", []string{"--sort", "instanceType"}, []string{"gpu", "api", "batch", "empty", "old", "web"}},
		{"outdated AMIs", []string{"-f", "amiStatus=outdated"}, []string{"web"}},
		// One of several instance types is enough.
		{"an instance type", []string{"-f", "instancetype=m5a.*"}, []string{"web"}},
		{"status and version", []string{"-f", "status=ACTIVE", "-f", "version=1.33"}, []string{"api", "empty", "gpu", "web"}},
		{"name", []string{"-f", "name=*a*"}, []string{"api", "batch"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newFake(t)
			args := append([]string{"nodegroup", "list", "-c", "prod", "-o", "plain"}, tt.args...)
			if err := e.Run(cmdutiltest.Context(t), args...); err != nil {
				t.Fatalf("run: %v", err)
			}
			var got []string
			for _, line := range strings.Split(strings.TrimSpace(e.Out.String()), "\n")[1:] {
				got = append(got, strings.Split(line, "\t")[0])
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("nodegroups (-want +got):\n%s", diff)
			}
		})
	}
}

func TestNotFound(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "nodegroup",
			args: []string{"nodegroup", "describe", "wbe", "-c", "prod"},
			want: "nodegroup \"wbe\" not found in cluster prod\nDid you mean:\n  web\nList the nodegroups with: refresh nodegroup list -c prod",
		},
		{
			name: "cluster",
			args: []string{"nodegroup", "list", "-c", "prd"},
			want: "cluster \"prd\" not found in us-east-1\nDid you mean:\n  prod\nList the clusters with: refresh cluster list -r us-east-1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newFake(t)
			err := e.Run(cmdutiltest.Context(t), tt.args...)
			if err == nil {
				t.Fatal("no error")
			}
			if diff := cmp.Diff(tt.want, err.Error()); diff != "" {
				t.Errorf("error (-want +got):\n%s", diff)
			}
			if got := cmdutil.ExitCode(err); got != cmdutil.ExitFailure || e.Out.Len() != 0 {
				t.Errorf("exit code = %d, stdout = %q, want 1 and nothing", got, e.Out.String())
			}
		})
	}
}

// A describe whose AMI lookup fails prints the nodegroup, names the
// failure, and exits 4.
func TestDescribe_AMILookupFails(t *testing.T) {
	e := newFake(t)
	e.World.SetFault(fleettest.SSMGetParameter, fleettest.Fault{Deny: true})
	err := e.Run(cmdutiltest.Context(t), "nodegroup", "describe", "web", "-c", "prod", "-o", "json")
	if got := cmdutil.ExitCode(err); got != cmdutil.ExitIncomplete {
		t.Fatalf("exit code = %d (%v), want %d", got, err, cmdutil.ExitIncomplete)
	}
	cmdutiltest.ValidateJSON(t, apidoc.KindNodegroupDescription, e.Out.Bytes())
	for _, want := range []string{`"incomplete": true`, `"operation": "ssm:GetParameter"`, `"status": "ACTIVE"`} {
		if !strings.Contains(e.Out.String(), want) {
			t.Errorf("the document has no %s:\n%s", want, e.Out.String())
		}
	}
	if strings.Contains(e.Out.String(), `"ami": {`) {
		t.Errorf("the document has an ami key for a lookup that failed:\n%s", e.Out.String())
	}
}

func TestInvalidInput(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"describe without a name", []string{"nodegroup", "describe", "-c", "prod"}, "accepts 1 arg(s), received 0"},
		{"an unknown filter key", []string{"nodegroup", "list", "-c", "prod", "-f", "ami=old"}, `unknown key "ami"; the keys are name, status, instanceType, amiStatus, version`},
		{"an unknown sort key", []string{"nodegroup", "list", "-c", "prod", "--sort", "age"}, "must be one of: name, status, instanceType, nodes, version, amiStatus"},
		{"no cluster", []string{"nodegroup", "list"}, "no cluster specified"},
		{"a watch of a machine format", []string{"nodegroup", "list", "-c", "prod", "-w", "-o", "json"}, "--watch cannot be used with -o json"},
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

// A deadline while the AMIs are read is a partial result with exit 4: the
// nodegroups are there, and their AMIs are failures. The deadline passes
// at the moment the group call is at the server.
func TestList_Deadline(t *testing.T) {
	e := newFake(t)
	ctx, expire := awsxtest.Deadline(cmdutiltest.Context(t))
	e.Server.OnRequest = func(hctx context.Context, r awsxtest.Request) {
		if r.Operation == fleettest.AutoScalingDescribeAutoScalingGroups {
			expire()
			<-hctx.Done()
		}
	}
	// One request slot: the calls go out one at a time, in the order that
	// they asked for the slot. The cluster asked before any call of the
	// second wave, so it is answered before the run is cut short.
	err := e.Run(ctx, "nodegroup", "list", "-c", "prod", "-o", "json", "--max-concurrency", "1")
	if got := cmdutil.ExitCode(err); got != cmdutil.ExitIncomplete {
		t.Fatalf("exit code = %d (%v), want %d", got, err, cmdutil.ExitIncomplete)
	}
	cmdutiltest.ValidateJSON(t, apidoc.KindNodegroupList, e.Out.Bytes())
	if !strings.Contains(e.Out.String(), `"name": "web"`) || !strings.Contains(e.Out.String(), `"scaling": {`) {
		t.Errorf("the document lost the nodegroups that were read:\n%s", e.Out.String())
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

	scope := fleettest.Scope{Operation: fleettest.EKSDescribeNodegroup, Nodegroup: "web"}
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
	err := e.Run(ctx, "nodegroup", "list", "-c", "prod", "-w")
	if !errors.Is(err, cmdutil.ErrStopped) {
		t.Fatalf("error = %v, want ErrStopped", err)
	}
	frames := strings.Split(e.Out.String(), "\x1b[H\x1b[2J")[1:]
	if len(frames) != 3 {
		t.Fatalf("the screen was drawn %d times, want 3:\n%q", len(frames), e.Out.String())
	}
	if frames[0] != frames[2] || !strings.Contains(frames[0], "web") || strings.Contains(frames[0], "INCOMPLETE DATA") {
		t.Errorf("the first and the third read are not the whole table:\n%s\n%s", frames[0], frames[2])
	}
	if !strings.Contains(frames[1], "INCOMPLETE DATA") || !strings.Contains(frames[1], "prod/web (us-east-1): AccessDenied") {
		t.Errorf("the second read does not name what it could not read:\n%s", frames[1])
	}
}

// withKube gives the cluster prod a Kubernetes API and points $KUBECONFIG
// at a file whose one context names it.
func withKube(t *testing.T, e *cmdtest.Fake) *kubetest.Server {
	t.Helper()
	e.World.Edit(func(s *fleettest.State) {
		c := s.Cluster(east, "prod")
		c.Nodes = []*fleettest.Node{
			{Name: "web-1", Nodegroup: "web"}, {Name: "web-2", Nodegroup: "web"}, {Name: "web-3", Nodegroup: "web", NotReady: true},
			{Name: "api-1", Nodegroup: "api"},
		}
		c.Pods = []*fleettest.Pod{
			{Namespace: "kube-system", Name: "coredns-1", Node: "web-1"},
			{Namespace: "kube-system", Name: "kube-proxy-1", Node: "web-2"},
			{Namespace: "default", Name: "app-1", Node: "web-1"},
			{Namespace: "default", Name: "job-1", Node: "web-1", Phase: "Succeeded"},
			{Namespace: "default", Name: "api-1", Node: "api-1"},
		}
	})
	srv := kubetest.NewServer(e.World, east, "prod")
	t.Cleanup(srv.Close)
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := srv.WriteKubeconfig(path); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", path)
	return srv
}

// --check-readiness reads the nodes once and shows ready/desired.
func TestList_CheckReadiness(t *testing.T) {
	e := newFake(t)
	withKube(t, e)
	if err := e.Run(cmdutiltest.Context(t), "nodegroup", "list", "-c", "prod", "--check-readiness", "-o", "plain"); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, want := range []string{"web\tACTIVE\tm5.large,m5a.large\t1.33\tOutdated\t2/6\n", "api\tACTIVE\tm5.large\t1.33\tLatest\t1/3\n", "empty\tACTIVE\tm5.large\t1.33\tUnknown\t0/0\n"} {
		if !strings.Contains(e.Out.String(), want) {
			t.Errorf("stdout has no %q:\n%s", want, e.Out.String())
		}
	}
	if e.Err.Len() != 0 {
		t.Errorf("stderr = %q, want nothing", e.Err.String())
	}

	// Without access the count is not invented: the column shows the
	// desired size, and stderr says why.
	e = newFake(t)
	if err := e.Run(cmdutiltest.Context(t), "nodegroup", "list", "-c", "prod", "--check-readiness", "-o", "json"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.Contains(e.Out.String(), "readyNodes") || !strings.HasPrefix(e.Err.String(), "warning: no Kubernetes access to prod") {
		t.Errorf("stdout:\n%s\nstderr:\n%s", e.Out.String(), e.Err.String())
	}
}

// A --kube-context that the kubeconfig does not have stops both commands
// before any AWS call.
func TestKubeContextNotFound(t *testing.T) {
	for _, args := range [][]string{
		{"nodegroup", "list", "-c", "prod", "--check-readiness", "--kube-context", "pord"},
		{"nodegroup", "describe", "web", "-c", "prod", "--show-workloads", "--kube-context", "pord"},
	} {
		e := newFake(t)
		withKube(t, e)
		err := e.Run(cmdutiltest.Context(t), args...)
		if err == nil || !strings.Contains(err.Error(), `kubeconfig context "pord" not found in `) || len(e.Server.Requests()) != 0 {
			t.Errorf("%v: error = %v, requests = %v", args, err, e.Operations())
		}
	}
}

// --show-workloads counts the pods on the nodes of the nodegroup from one
// pod list of the cluster.
func TestDescribe_ShowWorkloads(t *testing.T) {
	e := newFake(t)
	srv := withKube(t, e)
	if err := e.Run(cmdutiltest.Context(t), "nodegroup", "describe", "web", "-c", "prod", "--show-workloads", "-o", "plain"); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, want := range []string{"workloads.pods\t3\n", "workloads.system_pods\t2\n"} {
		if !strings.Contains(e.Out.String(), want) {
			t.Errorf("stdout has no %q:\n%s", want, e.Out.String())
		}
	}
	if n := len(srv.Requests()); n != 2 {
		t.Errorf("Kubernetes requests = %d, want the node list and one pod list", n)
	}
	e = newFake(t)
	withKube(t, e)
	if err := e.Run(cmdutiltest.Context(t), "nodegroup", "describe", "web", "-c", "prod", "--show-workloads", "-o", "json"); err != nil {
		t.Fatalf("run: %v", err)
	}
	validate(t, apidoc.KindNodegroupDescription, "json", e)
	if !strings.Contains(e.Out.String(), `"workloads": {`) {
		t.Errorf("stdout:\n%s", e.Out.String())
	}
}
