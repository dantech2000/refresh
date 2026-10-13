package cluster_test

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
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
	"github.com/dantech2000/refresh/internal/cmd/fleetcmd"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

var update = flag.Bool("update", false, "write the golden files in testdata")

const (
	east = "us-east-1"
	west = "us-west-2"
	eu   = "eu-west-1"
)

// newWorld returns the fleet of the golden files: four clusters in
// us-east-1, one in eu-west-1, and none in us-west-2.
func newWorld() *fleettest.World {
	world := fleettest.New()
	world.Edit(func(s *fleettest.State) {
		s.Clusters = append(s.Clusters,
			&fleettest.Cluster{
				Name: "prod-payments", Region: east, Version: "1.33", SupportType: "EXTENDED",
				VPCID: "vpc-0a1", SubnetIDs: []string{"subnet-1", "subnet-2"}, SecurityGroupIDs: []string{"sg-1"},
				EndpointPublicAccess: true, PublicAccessCIDRs: []string{"0.0.0.0/0"},
				LogTypes: []string{"api", "audit"}, EncryptionKeyARN: "arn:aws:kms:us-east-1:123456789012:key/k1",
				DeletionProtection: true, Tags: map[string]string{"team": "payments"},
				HealthIssues: []fleettest.Issue{{Code: "Ec2SubnetInvalidConfiguration", Message: "a subnet is gone", ResourceIDs: []string{"subnet-9"}}},
			},
			&fleettest.Cluster{Name: "prod-search", Region: east, Version: "1.31", Status: fleettest.StatusUpdating},
			&fleettest.Cluster{Name: "dev", Region: east, Version: "1.9"},
			&fleettest.Cluster{Name: "staging-data-platform-shared-services-blue", Region: east, Version: "1.10"},
			&fleettest.Cluster{Name: "prod-eu", Region: eu, Version: "1.33", Status: fleettest.StatusFailed},
		)
	})
	return world
}

// env is the fake AWS of one test.
type env struct {
	*cmdtest.Fake
	world *fleettest.World
	srv   *awsxtest.Server
	f     *cmdutil.Factory
	out   *bytes.Buffer
	err   *bytes.Buffer
}

func newEnv(t *testing.T, world *fleettest.World) *env {
	t.Helper()
	fake := cmdtest.NewFake(t, world, east)
	return &env{Fake: fake, world: world, srv: fake.Server, f: fake.Factory, out: fake.Out, err: fake.Err}
}

// run executes "refresh cluster list" with args and returns the error of
// the run.
func (e *env) run(ctx context.Context, args ...string) error {
	return e.Run(ctx, append([]string{"cluster", "list"}, args...)...)
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	return cmdutiltest.Context(t)
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	cmdutiltest.Golden(t, name, got, *update)
}

func validate(t *testing.T, document []byte) {
	t.Helper()
	cmdutiltest.ValidateJSON(t, apidoc.KindClusterList, document)
}

var threeRegions = []string{"-r", east, "-r", eu, "-r", west}

func TestList_Formats(t *testing.T) {
	for _, tt := range []struct{ format, file string }{
		{"table", "list.txt"}, {"plain", "list.tsv"}, {"json", "list.json"}, {"yaml", "list.yaml"},
	} {
		t.Run(tt.format, func(t *testing.T) {
			e := newEnv(t, newWorld())
			if err := e.run(testContext(t), append(threeRegions, "-o", tt.format)...); err != nil {
				t.Fatalf("run: %v", err)
			}
			if e.err.Len() != 0 {
				t.Errorf("stderr = %q, want nothing", e.err.String())
			}
			golden(t, tt.file, e.out.String())
			switch tt.format {
			case "json":
				validate(t, e.out.Bytes())
			case "yaml":
				cmdutiltest.ValidateYAML(t, apidoc.KindClusterList, e.out.Bytes())
			}
		})
	}
}

// A cluster and a region that cannot be read: the table lists them, the
// machine formats name them in the document and on stderr, and the run
// exits 4 after it printed what it read.
func TestList_Partial(t *testing.T) {
	for _, tt := range []struct{ format, file string }{
		{"table", "partial.txt"}, {"plain", "partial.tsv"}, {"json", "partial.json"},
	} {
		t.Run(tt.format, func(t *testing.T) {
			e := newEnv(t, newWorld())
			e.world.SetScopedFault(fleettest.Scope{Operation: fleettest.EKSDescribeCluster, Cluster: "prod-search"}, fleettest.Fault{Deny: true})
			e.world.SetScopedFault(fleettest.Scope{Region: eu}, fleettest.Fault{Unauthenticated: true})

			err := e.run(testContext(t), append(threeRegions, "-o", tt.format)...)
			if got := cmdutil.ExitCode(err); got != cmdutil.ExitIncomplete {
				t.Errorf("exit code = %d (%v), want %d", got, err, cmdutil.ExitIncomplete)
			}
			if want := "incomplete data: 2 failure(s) (1 cluster, 1 region)"; err == nil || err.Error() != want {
				t.Errorf("error = %v, want %s", err, want)
			}
			golden(t, tt.file, e.out.String())
			warnings := "warning: cluster prod-search (us-east-1): AccessDenied (grant eks:DescribeCluster):"
			if tt.format == "table" {
				if e.err.Len() != 0 {
					t.Errorf("stderr = %q, want nothing: the table lists the failures", e.err.String())
				}
				return
			}
			if !strings.Contains(e.err.String(), warnings) || !strings.Contains(e.err.String(), "warning: region eu-west-1: RegionUnavailable (eks:ListClusters):") {
				t.Errorf("stderr = %q, want one warning line for each failure", e.err.String())
			}
			if tt.format == "json" {
				validate(t, e.out.Bytes())
			}
		})
	}
}

func TestList_Empty(t *testing.T) {
	tests := []struct {
		format string
		want   string
	}{
		{"table", "CLUSTERS  0\n\nNo clusters found\n"},
		{"plain", "CLUSTER\tREGION\tSTATUS\tVERSION\n"},
		{"json", "{\n  \"apiVersion\": \"refresh.drod.dev/v2\",\n  \"kind\": \"ClusterList\",\n  \"clusters\": [],\n  \"failures\": []\n}\n"},
		{"yaml", "apiVersion: refresh.drod.dev/v2\nkind: ClusterList\nclusters: []\nfailures: []\n"},
	}
	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			e := newEnv(t, newWorld())
			if err := e.run(testContext(t), "-r", west, "-o", tt.format); err != nil {
				t.Fatalf("run: %v", err)
			}
			if diff := cmp.Diff(tt.want, e.out.String()); diff != "" {
				t.Errorf("stdout (-want +got):\n%s", diff)
			}
		})
	}
}

func operations(e *env) []string { return e.Operations() }

// The command makes one list call for each region and one describe call
// for each cluster, and nothing else.
func TestList_ExactCalls(t *testing.T) {
	e := newEnv(t, newWorld())
	// One slot, so that the order of the log is the order of the calls.
	if err := e.run(testContext(t), "--max-concurrency", "1", "-o", "json"); err != nil {
		t.Fatalf("run: %v", err)
	}
	want := []string{
		"eks:ListClusters us-east-1",
		"eks:DescribeCluster us-east-1",
		"eks:DescribeCluster us-east-1",
		"eks:DescribeCluster us-east-1",
		"eks:DescribeCluster us-east-1",
	}
	if diff := cmp.Diff(want, operations(e)); diff != "" {
		t.Errorf("requests (-want +got):\n%s", diff)
	}
}

// A name filter and a region filter drop clusters before the describe
// calls. A status filter cannot: the status is what the call returns.
func TestList_NameFilterSavesCalls(t *testing.T) {
	e := newEnv(t, newWorld())
	if err := e.run(testContext(t), "-f", "name=prod-*", "-f", "status=ACTIVE", "-o", "plain"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := len(e.srv.Requests()); got != 3 {
		t.Errorf("requests = %v, want one list call and two describe calls", operations(e))
	}
	want := "CLUSTER\tREGION\tSTATUS\tVERSION\nprod-payments\tus-east-1\tACTIVE\t1.33\n"
	if diff := cmp.Diff(want, e.out.String()); diff != "" {
		t.Errorf("stdout (-want +got):\n%s", diff)
	}
}

// The command is two sequential round trips: the list call, then every
// describe call together.
func TestList_Waves(t *testing.T) {
	e := newEnv(t, newWorld())
	gate := awsxtest.NewGate(t, e.srv.Handler)
	done := make(chan error, 1)
	go func() { done <- e.run(testContext(t), "-o", "json") }()

	gate.Wave(fleettest.EKSListClusters)
	gate.Wave(fleettest.EKSDescribeCluster, fleettest.EKSDescribeCluster, fleettest.EKSDescribeCluster, fleettest.EKSDescribeCluster)
	gate.Done()
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
}

// The regions of a sweep run together: every region has its list call in
// flight before any is answered.
func TestList_RegionsRunTogether(t *testing.T) {
	e := newEnv(t, newWorld())
	gate := awsxtest.NewGate(t, e.srv.Handler)
	done := make(chan error, 1)
	go func() { done <- e.run(testContext(t), append(threeRegions, "-o", "json")...) }()

	gate.Wave(fleettest.EKSListClusters, fleettest.EKSListClusters, fleettest.EKSListClusters)
	gate.Wave(fleettest.EKSDescribeCluster, fleettest.EKSDescribeCluster, fleettest.EKSDescribeCluster, fleettest.EKSDescribeCluster, fleettest.EKSDescribeCluster)
	gate.Done()
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
}

// --max-concurrency is the number of requests in flight in a region: the
// run reaches it and does not pass it.
func TestList_RequestLimit(t *testing.T) {
	const limit = 4
	world := fleettest.New()
	world.Edit(func(s *fleettest.State) {
		for i := range 30 {
			s.Clusters = append(s.Clusters, &fleettest.Cluster{Name: fmt.Sprintf("c%02d", i), Region: east, Version: "1.33"})
		}
	})
	e := newEnv(t, world)
	var (
		mu       sync.Mutex
		inFlight int
	)
	reached := make(chan struct{})
	e.srv.OnRequest = func(ctx context.Context, r awsxtest.Request) {
		if r.Operation != fleettest.EKSDescribeCluster {
			return
		}
		mu.Lock()
		inFlight++
		if inFlight == limit {
			close(reached)
		}
		mu.Unlock()
		// Hold every describe call until the limit is reached. With a
		// lower real limit the run would stop here.
		select {
		case <-reached:
		case <-ctx.Done():
		}
	}
	if err := e.run(testContext(t), "--max-concurrency", "4", "-o", "json"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := e.srv.PeakConcurrency("", east); got != limit {
		t.Errorf("peak requests in flight = %d, want %d", got, limit)
	}
}

func TestList_SortAndFilter(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string // the cluster names, in order
	}{
		{"by name", nil, []string{"dev", "prod-eu", "prod-payments", "prod-search", "staging-data-platform-shared-services-blue"}},
		{"by name, descending", []string{"--desc"}, []string{"staging-data-platform-shared-services-blue", "prod-search", "prod-payments", "prod-eu", "dev"}},
		{"by region", []string{"--sort", "region"}, []string{"prod-eu", "dev", "prod-payments", "prod-search", "staging-data-platform-shared-services-blue"}},
		// Versions sort as numbers: 1.9 is older than 1.10. A tie keeps
		// the name order, also when the sort is descending.
		{"by version", []string{"--sort", "version"}, []string{"dev", "staging-data-platform-shared-services-blue", "prod-search", "prod-eu", "prod-payments"}},
		{"by version, descending", []string{"--sort", "version", "--desc"}, []string{"prod-eu", "prod-payments", "prod-search", "staging-data-platform-shared-services-blue", "dev"}},
		{"by status", []string{"--sort", "status"}, []string{"dev", "prod-payments", "staging-data-platform-shared-services-blue", "prod-eu", "prod-search"}},
		{"name pattern", []string{"-f", "name=prod-*"}, []string{"prod-eu", "prod-payments", "prod-search"}},
		{"an exact name is not a substring", []string{"-f", "name=prod"}, nil},
		{"region", []string{"-f", "region=EU-*"}, []string{"prod-eu"}},
		{"status and version", []string{"-f", "status=active", "-f", "version=1.3*"}, []string{"prod-payments"}},
		{"two patterns for one key", []string{"-f", "name=prod-*", "-f", "name=*-search"}, []string{"prod-search"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, newWorld())
			if err := e.run(testContext(t), append(append([]string{}, threeRegions...), append(tt.args, "-o", "plain")...)...); err != nil {
				t.Fatalf("run: %v", err)
			}
			var got []string
			for _, line := range strings.Split(strings.TrimSpace(e.out.String()), "\n")[1:] {
				got = append(got, strings.Split(line, "\t")[0])
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("clusters (-want +got):\n%s", diff)
			}
		})
	}
}

// Bad input fails before any AWS call.
func TestList_InvalidInput(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"-A with -r", []string{"-A", "-r", east}, "-A/--all-regions and -r/--region cannot be used together"},
		{"a filter that does not parse", []string{"-f", "prod"}, `invalid filter "prod"`},
		{"an unknown filter key", []string{"-f", "health=ok"}, `unknown key "health"; the keys are name, region, status, version`},
		{"an unknown sort key", []string{"--sort", "age"}, "must be one of: name, region, status, version"},
		{"an unknown format", []string{"-o", "tree"}, "must be one of: table, json, yaml, plain"},
		{"an argument", []string{"prod"}, `unknown command "prod"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, newWorld())
			err := e.run(testContext(t), tt.args...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want one that contains %q", err, tt.want)
			}
			if got := cmdutil.ExitCode(err); got != cmdutil.ExitFailure {
				t.Errorf("exit code = %d, want %d", got, cmdutil.ExitFailure)
			}
			if n := len(e.srv.Requests()); n != 0 {
				t.Errorf("requests = %v, want none", operations(e))
			}
			if e.out.Len() != 0 {
				t.Errorf("stdout = %q, want nothing", e.out.String())
			}
		})
	}
}

// -A with no region list is a default sweep: a region that is closed to the
// credentials is skipped with one notice, and it is not a failure.
func TestList_DefaultSweepSkipsAClosedRegion(t *testing.T) {
	e := newEnv(t, newWorld())
	e.world.SetScopedFault(fleettest.Scope{Region: eu}, fleettest.Fault{Unauthenticated: true})
	if err := e.run(testContext(t), "-A", "-o", "json"); err != nil {
		t.Fatalf("run: %v", err)
	}
	wantNotice := "Skipped 1 region(s) not accessible to these credentials: eu-west-1 (scope with -r or REFRESH_EKS_REGIONS)\n"
	if diff := cmp.Diff(wantNotice, e.err.String()); diff != "" {
		t.Errorf("stderr (-want +got):\n%s", diff)
	}
	for _, want := range []string{`"skippedRegions": [`, `"eu-west-1"`, `"failures": []`, `"name": "prod-payments"`} {
		if !strings.Contains(e.out.String(), want) {
			t.Errorf("the document has no %s:\n%s", want, e.out.String())
		}
	}
	if strings.Contains(e.out.String(), `"name": "prod-eu"`) {
		t.Error("the document has a cluster of the skipped region")
	}
	validate(t, e.out.Bytes())
	lists := 0
	for _, r := range e.srv.Requests() {
		if r.Operation == fleettest.EKSListClusters {
			lists++
		}
	}
	if lists != 16 {
		t.Errorf("ListClusters calls = %d, want one for each of the 16 regions of the default sweep", lists)
	}
}

// REFRESH_EKS_REGIONS names the regions of -A, and a named region is never
// skipped.
func TestList_RegionsFromTheEnvironmentAreNamed(t *testing.T) {
	e := newEnv(t, newWorld())
	t.Setenv(fleetcmd.EnvRegions, east+", "+eu)
	e.world.SetScopedFault(fleettest.Scope{Region: eu}, fleettest.Fault{Unauthenticated: true})
	err := e.run(testContext(t), "-A", "-o", "plain")
	if got := cmdutil.ExitCode(err); got != cmdutil.ExitIncomplete {
		t.Errorf("exit code = %d (%v), want %d", got, err, cmdutil.ExitIncomplete)
	}
	if !strings.Contains(e.err.String(), "warning: region eu-west-1: RegionUnavailable") || strings.Contains(e.err.String(), "Skipped") {
		t.Errorf("stderr = %q, want the region as a failure and no skip notice", e.err.String())
	}
	if n := len(strings.Split(strings.TrimSpace(e.out.String()), "\n")); n != 5 {
		t.Errorf("stdout has %d lines, want the header and the 4 clusters of us-east-1:\n%s", n, e.out.String())
	}
}

// When no region answers there is no result: the command prints no
// document and fails with exit 1.
func TestList_NoRegionAnswers(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		fault func(*fleettest.World)
		want  string
	}{
		{
			name:  "a denial in every region",
			args:  []string{"-r", east, "-r", eu},
			fault: func(w *fleettest.World) { w.SetFault(fleettest.EKSListClusters, fleettest.Fault{Deny: true}) },
			want:  "could not list clusters in any of 2 region(s); the first error, for us-east-1:",
		},
		{
			name:  "one region, denied",
			args:  nil,
			fault: func(w *fleettest.World) { w.SetFault(fleettest.EKSListClusters, fleettest.Fault{Deny: true}) },
			want:  "eks:ListClusters",
		},
		{
			name: "the regions refuse credentials that STS accepts",
			args: []string{"-r", eu, "-r", west},
			fault: func(w *fleettest.World) {
				w.SetScopedFault(fleettest.Scope{Region: eu}, fleettest.Fault{Unauthenticated: true})
				w.SetScopedFault(fleettest.Scope{Region: west}, fleettest.Fault{Unauthenticated: true})
			},
			want: "no region answered, but STS in us-east-1 accepts these credentials",
		},
		{
			name:  "AWS does not accept the credentials",
			args:  []string{"-r", east, "-r", eu},
			fault: func(w *fleettest.World) { w.SetScopedFault(fleettest.Scope{}, fleettest.Fault{Unauthenticated: true}) },
			want:  "AWS credentials not configured or invalid\nCause: InvalidClientTokenId",
		},
		{
			name: "every region of a default sweep is closed, and STS accepts the credentials",
			args: []string{"-A"},
			fault: func(w *fleettest.World) {
				w.SetFault(fleettest.EKSListClusters, fleettest.Fault{Unauthenticated: true})
			},
			want: "could not list clusters in any of 16 region(s): none is accessible to these credentials",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, newWorld())
			tt.fault(e.world)
			err := e.run(testContext(t), append(tt.args, "-o", "json")...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want one that contains %q", err, tt.want)
			}
			if got := cmdutil.ExitCode(err); got != cmdutil.ExitFailure {
				t.Errorf("exit code = %d, want %d", got, cmdutil.ExitFailure)
			}
			if e.out.Len() != 0 {
				t.Errorf("stdout = %q, want nothing", e.out.String())
			}
		})
	}
}

// Ctrl+C is not a partial result: the command prints what it read, and the
// run fails with exit 1, not 4.
func TestList_Interrupt(t *testing.T) {
	e := newEnv(t, newWorld())
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()
	e.srv.OnRequest = func(hctx context.Context, r awsxtest.Request) {
		if r.Operation == fleettest.EKSDescribeCluster && r.Input["name"] == "prod-search" {
			cancel()
			<-hctx.Done()
		}
	}
	// One slot: the clusters before prod-search in name order are read.
	err := e.run(ctx, "--max-concurrency", "1", "-o", "json")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want the interrupt", err)
	}
	if got := cmdutil.ExitCode(err); got != cmdutil.ExitFailure {
		t.Errorf("exit code = %d, want %d", got, cmdutil.ExitFailure)
	}
	validate(t, e.out.Bytes())
	for _, want := range []string{`"name": "dev"`, `"version": "1.9"`, `"reason": "Interrupted"`, `"reason": "NotAttempted"`, `"incomplete": true`} {
		if !strings.Contains(e.out.String(), want) {
			t.Errorf("the document has no %s:\n%s", want, e.out.String())
		}
	}
}

// A deadline is a partial result: the rows that were read are complete,
// the rest are failures, and the run exits 4. The deadline here passes at
// the moment the third cluster is at the server, so no test waits for a
// clock.
func TestList_Deadline(t *testing.T) {
	e := newEnv(t, newWorld())
	ctx, expire := awsxtest.Deadline(testContext(t))
	e.srv.OnRequest = func(hctx context.Context, r awsxtest.Request) {
		if r.Operation == fleettest.EKSDescribeCluster && r.Input["name"] == "prod-search" {
			expire()
			<-hctx.Done()
		}
	}
	// One slot: the clusters before prod-search in name order are read.
	err := e.run(ctx, "--max-concurrency", "1", "-o", "json")
	if got := cmdutil.ExitCode(err); got != cmdutil.ExitIncomplete {
		t.Fatalf("exit code = %d (%v), want %d", got, err, cmdutil.ExitIncomplete)
	}
	validate(t, e.out.Bytes())
	for _, want := range []string{`"name": "prod-payments"`, `"version": "1.33"`, `"reason": "Timeout"`, `"reason": "NotAttempted"`} {
		if !strings.Contains(e.out.String(), want) {
			t.Errorf("the document has no %s:\n%s", want, e.out.String())
		}
	}
}

// On a terminal the table appears as the regions answer and is redrawn in
// place; the final table is printed once, after the last redraw is erased.
func TestList_TerminalRedrawsAsRegionsAnswer(t *testing.T) {
	e := newEnv(t, newWorld())
	e.f.IOStreams.OutTTY = true
	e.f.IOStreams.ErrTTY = true
	e.f.IOStreams.TermSize = func() (int, int) { return 100, 40 }
	e.f.IOStreams.Unicode = true
	if err := e.run(testContext(t), threeRegions...); err != nil {
		t.Fatalf("run: %v", err)
	}
	out := e.out.String()
	if n := strings.Count(out, "\x1b["); n < 3 {
		t.Errorf("stdout has %d cursor codes, want a redraw for each region:\n%q", n, out)
	}
	final := out[strings.LastIndex(out, "\x1b[J")+len("\x1b[J"):]
	want, err := os.ReadFile(filepath.Join("testdata", "list-100.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(string(want), final); diff != "" {
		t.Errorf("the final table (-golden +got):\n%s", diff)
	}
	if e.err.Len() != 0 {
		t.Errorf("stderr = %q, want nothing", e.err.String())
	}
}

// A table that is taller than the terminal cannot be redrawn in place: a
// status line on stderr stands in, and the table prints once.
func TestList_TerminalTooShortShowsAStatusLine(t *testing.T) {
	e := newEnv(t, newWorld())
	e.f.IOStreams.OutTTY = true
	e.f.IOStreams.ErrTTY = true
	e.f.IOStreams.TermSize = func() (int, int) { return 100, 4 }
	if err := e.run(testContext(t), "-r", east); err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.Contains(e.out.String(), "\x1b[") {
		t.Errorf("stdout has cursor codes, want the table alone:\n%q", e.out.String())
	}
	if !strings.Contains(e.err.String(), "1 of 1 regions, 4 clusters") {
		t.Errorf("stderr = %q, want the status line", e.err.String())
	}
}

// A watch reads again after each interval and redraws the table. A read
// with a failure names it and the watch goes on. Ctrl+C ends the watch
// with ErrStopped, which is exit 0.
func TestList_Watch(t *testing.T) {
	e := newEnv(t, newWorld())
	e.f.IOStreams.OutTTY = true
	e.f.IOStreams.TermSize = func() (int, int) { return 100, 40 }
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()

	pauses := 0
	tick := make(chan time.Time, 1)
	e.f.After = func(d time.Duration) <-chan time.Time {
		if d != 5*time.Second {
			t.Errorf("pause = %s, want the --watch-interval of 5s", d)
		}
		pauses++
		switch pauses {
		case 1:
			// The second read cannot describe one cluster.
			e.world.SetScopedFault(fleettest.Scope{Operation: fleettest.EKSDescribeCluster, Cluster: "dev"}, fleettest.Fault{Deny: true})
		case 2:
			e.world.SetScopedFault(fleettest.Scope{Operation: fleettest.EKSDescribeCluster, Cluster: "dev"}, fleettest.Fault{})
		case 3:
			cancel()
			return make(chan time.Time)
		}
		tick <- time.Time{}
		return tick
	}
	err := e.run(ctx, "-w", "--watch-interval", "5s")
	if !errors.Is(err, cmdutil.ErrStopped) {
		t.Fatalf("error = %v, want ErrStopped", err)
	}
	frames := strings.Split(e.out.String(), "\x1b[H\x1b[2J")[1:]
	if len(frames) != 3 {
		t.Fatalf("the screen was drawn %d times, want 3:\n%q", len(frames), e.out.String())
	}
	if frames[0] != frames[2] || !strings.Contains(frames[0], "dev") || strings.Contains(frames[0], "INCOMPLETE DATA") {
		t.Errorf("the first and the third read are not the whole table:\n%s\n%s", frames[0], frames[2])
	}
	if !strings.Contains(frames[1], "INCOMPLETE DATA") || !strings.Contains(frames[1], "cluster dev (us-east-1): AccessDenied") {
		t.Errorf("the second read does not name what it could not read:\n%s", frames[1])
	}
	if strings.Contains(e.out.String(), "\x1b[J") && strings.Count(e.out.String(), "\x1b[") != 6 {
		t.Errorf("stdout has cursor codes beyond the three clears:\n%q", e.out.String())
	}
}

func TestList_WatchNeedsATableOnATerminal(t *testing.T) {
	tests := []struct {
		name string
		tty  bool
		args []string
		want string
	}{
		{"json", true, []string{"-w", "-o", "json"}, "--watch cannot be used with -o json"},
		{"not a terminal", false, []string{"-w"}, "--watch needs a terminal on stdout"},
		{"a short interval", true, []string{"-w", "--watch-interval", "10ms"}, "must be at least 1s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, newWorld())
			e.f.IOStreams.OutTTY = tt.tty
			err := e.run(testContext(t), tt.args...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want one that contains %q", err, tt.want)
			}
			if n := len(e.srv.Requests()); n != 0 {
				t.Errorf("requests = %v, want none", operations(e))
			}
		})
	}
}
