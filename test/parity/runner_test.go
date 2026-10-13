//go:build unix

package parity

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// stand runs the test binary as a stand-in for refresh (see helper).
func stand(mode string) (Runner, []string) {
	return Runner{Binary: os.Args[0], Grace: 200 * time.Millisecond}, []string{helperArg, mode}
}

// TestChildEnvironment checks, in a real child, that nothing of the
// machine's AWS, refresh, Kubernetes, or proxy setup gets through.
func TestChildEnvironment(t *testing.T) {
	for _, name := range []string{
		"AWS_PROFILE", "AWS_SESSION_TOKEN", "AWS_DEFAULT_REGION", "REFRESH_CONTEXT", "REFRESH_EKS_REGIONS", "EKS_CLUSTER_NAME",
		"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "KUBE_CONFIG_PATH",
	} {
		t.Setenv(name, "from-the-machine")
	}
	t.Setenv("KUBECONFIG", "/from/the/machine")
	t.Setenv("AWS_CONFIG_FILE", "/from/the/machine")

	r, args := stand("env")
	dir := t.TempDir()
	trace, err := r.Run(t.Context(), Scenario{Seed: fleet()}, args, dir)
	if err != nil {
		t.Fatal(err)
	}
	set := regexp.MustCompile(`(?i)^(AWS_|REFRESH_|EKS_|KUBE|[A-Z]+_PROXY)`)
	got := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(trace.Stdout), "\n") {
		name, value, _ := strings.Cut(line, "=")
		if strings.Contains(value, "machine") {
			t.Errorf("the child has %s from the machine", line)
		}
		if set.MatchString(name) {
			got[name] = value
		}
	}
	want := map[string]string{
		"AWS_ENDPOINT_URL":            "http://fake",
		"AWS_ACCESS_KEY_ID":           "AKIDFAKEFAKEFAKE",
		"AWS_SECRET_ACCESS_KEY":       "fake-secret",
		"AWS_REGION":                  Region,
		"AWS_CONFIG_FILE":             "$DIR/empty",
		"AWS_SHARED_CREDENTIALS_FILE": "$DIR/empty",
		"AWS_EC2_METADATA_DISABLED":   "true",
		"KUBECONFIG":                  "$DIR/empty",
		"REFRESH_CONFIG_HOME":         "$DIR/refresh",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("AWS, refresh, Kubernetes, and proxy variables of the child (-want +got):\n%s", diff)
	}
	if !strings.Contains(trace.Stdout, "HOME=$DIR\n") {
		t.Errorf("the child's HOME is not the run's directory:\n%s", trace.Stdout)
	}
}

// TestStubbornChildIsKilled runs a child that ignores the interrupt, has a
// child of its own, and waits on a request the runner holds. When the run's
// context ends the runner must kill the whole group, release the request,
// and return. TestMain's leak check sees a handler that was not released.
func TestStubbornChildIsKilled(t *testing.T) {
	t.Parallel()
	r, args := stand("stubborn")
	sc := Scenario{
		Seed:   fleet(),
		Signal: &Signal{Signal: syscall.SIGINT, Nth: 1, Match: func(r awsxtest.Request) bool { return r.Operation == fleettest.EKSListClusters }},
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	started := time.Now()
	_, err := r.Run(ctx, sc, args, t.TempDir())

	var runErr *RunError
	if !errors.As(err, &runErr) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run returned %v, want a RunError for the deadline", err)
	}
	if took := time.Since(started); took > 20*time.Second {
		t.Errorf("Run took %s to give up on a child with a 2s deadline and a 200ms grace", took)
	}
	m := regexp.MustCompile(`grandchild (\d+)`).FindStringSubmatch(runErr.Stdout)
	if m == nil {
		t.Fatalf("the child did not report its own child:\n%s", runErr.Stdout)
	}
	pid, _ := strconv.Atoi(m[1])
	// The kernel has no event to wait on for a process that is not our
	// child. Poll for its end, which follows the kill within moments.
	deadline := time.Now().Add(10 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("the child's own child (pid %d) outlived the run", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestTerminal runs a child on a pseudo-terminal and answers its prompt.
func TestTerminal(t *testing.T) {
	t.Parallel()
	r, args := stand("prompt")
	for _, reply := range []string{"y\n", "n\n"} {
		sc := Scenario{Seed: fleet(), Terminal: &Terminal{Expect: "[y/N]", Replies: []string{reply}}}
		trace, err := r.Run(t.Context(), sc, args, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if want := "answer=" + strconv.Quote(reply); !strings.Contains(trace.Terminal, want) {
			t.Errorf("the terminal shows %q, want it to contain %s", trace.Terminal, want)
		}
		if trace.Stdout != "" || trace.Stderr != "" {
			t.Errorf("a run on a terminal has stdout %q and stderr %q, want both empty", trace.Stdout, trace.Stderr)
		}
	}

	// A prompt that never comes is an error, not a silent pass.
	r, args = stand("env")
	sc := Scenario{Seed: fleet(), Terminal: &Terminal{Expect: "[y/N]", Replies: []string{"y\n"}}}
	if _, err := r.Run(t.Context(), sc, args, t.TempDir()); err == nil || !strings.Contains(err.Error(), "never printed") {
		t.Errorf("Run = %v, want an error for the missing prompt", err)
	}
}

func TestNormaliserText(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 12, 9, 15, 0, 0, time.UTC)
	n := newNormaliser("http://127.0.0.1:54321", "/tmp/run-1", now)
	n.token("c0ffee00-aaaa-bbbb-cccc-000000000002")
	n.token("c0ffee00-aaaa-bbbb-cccc-000000000001")
	tests := []struct{ in, want string }{
		{"endpoint http://127.0.0.1:54321/clusters", "endpoint http://fake/clusters"},
		{"wrote /tmp/run-1/refresh/config.yaml", "wrote $DIR/refresh/config.yaml"},
		{"token c0ffee00-aaaa-bbbb-cccc-000000000001 then c0ffee00-aaaa-bbbb-cccc-000000000002", "token token-2 then token-1"},
		// Taken from the real clock during the run.
		{`"startedAt": "2026-10-12T09:15:02.123456Z"`, `"startedAt": "<now>"`},
		{"time=2026-10-12T11:15:02.123+02:00 level=WARN", "time=<now> level=WARN"},
		// Seeded relative to the real clock: the distance stays.
		{`"upgradedAt": "2026-10-11T09:15:00Z", "availableUntil": "2026-10-18T09:15:00Z"`, `"upgradedAt": "<now-1d>", "availableUntil": "<now+6d>"`},
		{"upgraded about 2026-10-11; until about 2026-10-18", "upgraded about <now-1d>; until about <now+6d>"},
		{"as of 2026-10-12", "as of <now>"},
		// The world's clock, and dates far from the run, are data.
		{`"createdAt": "2026-01-01T00:03:00Z"`, `"createdAt": "2026-01-01T00:03:00Z"`},
		{"standard support ends 2027-03-23", "standard support ends 2027-03-23"},
		// Durations are never touched: these are configuration.
		{"Timeout: 5m0s | Poll interval: 20ms", "Timeout: 5m0s | Poll interval: 20ms"},
		{"waited 300ms of --wait-timeout 300ms; took 1.5s", "waited 300ms of --wait-timeout 300ms; took 1.5s"},
		{"release 1.32.0-20260101 of v1.19.0-eksbuild.1", "release 1.32.0-20260101 of v1.19.0-eksbuild.1"},
		{"update 00000000-0000-4000-8000-000000000001", "update 00000000-0000-4000-8000-000000000001"},
	}
	for _, tt := range tests {
		if got := n.text(tt.in); got != tt.want {
			t.Errorf("text(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestNormaliserMutation(t *testing.T) {
	t.Parallel()
	n := newNormaliser("", "", time.Now())
	request := func(token string) awsxtest.Request {
		return awsxtest.Request{
			Operation: fleettest.EKSUpdateNodegroupVersion, Region: Region, Mutates: true,
			Input: map[string]any{"clusterName": "prod", "nodegroupName": "ng-a", "version": "1.33", "clientRequestToken": token},
		}
	}
	var got []Mutation
	for i, token := range []string{"aaa", "bbb", "aaa"} {
		outcome := []string{"lost", "update 1", "error=ThrottlingException"}[i]
		got = append(got, *n.mutation(request(token), "prod/ng-a", outcome))
	}
	want := func(token, outcome string) Mutation {
		return Mutation{
			Operation: fleettest.EKSUpdateNodegroupVersion, Region: Region, Target: "prod/ng-a",
			Payload: map[string]any{"version": "1.33", "clientRequestToken": token}, Outcome: outcome,
		}
	}
	wantAll := []Mutation{want("token-1", "lost"), want("token-2", "accepted update 1"), want("token-1", "error=ThrottlingException")}
	if diff := cmp.Diff(wantAll, got); diff != "" {
		t.Errorf("mutations (-want +got):\n%s", diff)
	}
}

// TestPreconditions checks which reads of a step count for its mutation.
func TestPreconditions(t *testing.T) {
	t.Parallel()
	reads := []Read{
		{Operation: "cloudwatch:GetMetricData", Region: Region},
		{Operation: "eks:DescribeAddon", Region: Region, Target: "prod/coredns", Observed: "v1 ACTIVE"},
		{Operation: "eks:DescribeCluster", Region: Region, Target: "prod", Observed: "1.33 ACTIVE"},
		{Operation: "eks:DescribeNodegroup", Region: Region, Target: "prod/ng-a", Observed: "1.32 r1 ACTIVE 2/1/3"},
		{Operation: "eks:DescribeNodegroup", Region: Region, Target: "prod/ng-b", Observed: "1.32 r1 ACTIVE 2/1/3"},
		{Operation: "eks:DescribeNodegroup", Region: Region, Target: "staging/ng-a", Observed: "1.32 r1 ACTIVE 2/1/3"},
		{Operation: "eks:DescribeUpdate", Region: Region, Target: "prod/vpc-cni", Observed: "update 3 AddonUpdate Successful"},
	}
	got := preconditions(reads, "prod/ng-a", "prod/vpc-cni")
	want := []string{
		"eks:DescribeCluster prod: 1.33 ACTIVE",
		"eks:DescribeNodegroup prod/ng-a: 1.32 r1 ACTIVE 2/1/3",
		"eks:DescribeUpdate prod/vpc-cni: update 3 AddonUpdate Successful",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("preconditions of a roll of prod/ng-a after an update of prod/vpc-cni (-want +got):\n%s", diff)
	}
}
