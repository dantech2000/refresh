package upgrade

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/smithy-go"

	"github.com/dantech2000/refresh/internal/mocks"
	"github.com/dantech2000/refresh/internal/services/common"
)

// planShape summarizes a plan as one line per hop: "from→to ng[pending
// nodegroup targets]", so a test can compare the whole plan at a glance.
func planShape(plan *Plan) []string {
	out := make([]string, 0, len(plan.Hops))
	for _, hop := range plan.Hops {
		var ngs []string
		for _, st := range hop.Steps {
			if st.Type == StepNodegroup && st.Status == StatusPending {
				ngs = append(ngs, st.Target)
			}
		}
		slices.Sort(ngs)
		out = append(out, fmt.Sprintf("%s→%s ng[%s]", hop.From, hop.To, strings.Join(ngs, " ")))
	}
	return out
}

// recordRolls wraps the mock's UpdateNodegroupVersion and records, per
// nodegroup, the versions it was rolled to, in order.
func recordRolls(m *mocks.EKSAPI) func() map[string][]string {
	var mu sync.Mutex
	rolls := map[string][]string{}
	next := m.UpdateNodegroupVersionFn
	m.UpdateNodegroupVersionFn = func(ctx context.Context, in *eks.UpdateNodegroupVersionInput, o ...func(*eks.Options)) (*eks.UpdateNodegroupVersionOutput, error) {
		mu.Lock()
		name := aws.ToString(in.NodegroupName)
		rolls[name] = append(rolls[name], aws.ToString(in.Version))
		mu.Unlock()
		return next(ctx, in, o...)
	}
	return func() map[string][]string {
		mu.Lock()
		defer mu.Unlock()
		out := make(map[string][]string, len(rolls))
		for k, v := range rolls {
			out[k] = slices.Clone(v)
		}
		return out
	}
}

func assertRolls(t *testing.T, got, want map[string][]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("rolls = %v, want %v", got, want)
	}
	for name, versions := range want {
		if !slices.Equal(got[name], versions) {
			t.Fatalf("rolls = %v, want %v", got, want)
		}
	}
}

// A nodegroup is rolled early (to the live control-plane version) only when
// the next control-plane step would push it beyond the kubelet skew, and only
// when that roll is itself within skew. The decision depends on the nodegroup
// alone, never on its siblings.
func TestBuildPlan_PreRollRule(t *testing.T) {
	cases := []struct {
		name        string
		cp, to      string
		ngs         map[string]string
		custom      []string
		skip        []string
		wantShape   []string
		wantBlocked string // nodegroup named in the blocker, "" = not blocked
	}{
		{
			name: "steady-state one-minor lag rolls once, straight to the target",
			cp:   "1.29", to: "1.30",
			ngs:       map[string]string{"workers-a": "1.28", "workers-b": "1.29"},
			wantShape: []string{"1.29→1.30 ng[workers-a workers-b]"},
		},
		{
			name: "two-minor lag is still within skew of the next hop",
			cp:   "1.29", to: "1.30",
			ngs:       map[string]string{"workers-a": "1.27"},
			wantShape: []string{"1.29→1.30 ng[workers-a]"},
		},
		{
			name: "three-minor lag is pre-rolled to the live control plane",
			cp:   "1.29", to: "1.30",
			ngs:       map[string]string{"old": "1.26", "cur": "1.29"},
			wantShape: []string{"1.29→1.29 ng[old]", "1.29→1.30 ng[cur old]"},
		},
		{
			name: "a one-minor-behind sibling changes nothing",
			cp:   "1.29", to: "1.30",
			ngs:       map[string]string{"old": "1.26", "mid": "1.28", "cur": "1.29"},
			wantShape: []string{"1.29→1.29 ng[old]", "1.29→1.30 ng[cur mid old]"},
		},
		{
			name: "already beyond skew of the live control plane blocks",
			cp:   "1.29", to: "1.30",
			ngs:         map[string]string{"ancient": "1.25", "mid": "1.28"},
			wantShape:   []string{"1.29→1.30 ng[ancient mid]"},
			wantBlocked: "ancient",
		},
		{
			name: "custom AMI is not pre-rolled and blocks",
			cp:   "1.29", to: "1.30",
			ngs:         map[string]string{"byo": "1.26"},
			custom:      []string{"byo"},
			wantShape:   []string{"1.29→1.30 ng[]"},
			wantBlocked: "byo",
		},
		{
			name: "skipped nodegroup is not pre-rolled and blocks",
			cp:   "1.29", to: "1.30",
			ngs:         map[string]string{"legacy": "1.26"},
			skip:        []string{"legacy"},
			wantShape:   []string{"1.29→1.30 ng[]"},
			wantBlocked: "legacy",
		},
		{
			name: "multi-hop pre-rolls only before the first hop",
			cp:   "1.29", to: "1.32",
			ngs: map[string]string{"a": "1.26", "b": "1.28", "c": "1.29"},
			wantShape: []string{
				"1.29→1.29 ng[a]",
				"1.29→1.30 ng[a b c]",
				"1.30→1.31 ng[a b c]",
				"1.31→1.32 ng[a b c]",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld()
			w.clusterVersion = tc.cp
			w.addonVersions = map[string]string{"vpc-cni": latestFor(tc.cp)}
			w.ngVersions = tc.ngs
			w.customAMI = map[string]bool{}
			for _, n := range tc.custom {
				w.customAMI[n] = true
			}
			svc := newTestService(newWorldMock(w))

			plan, err := svc.BuildPlan(context.Background(), "prod-east", tc.to, PlanOptions{SkipNodegroups: tc.skip})
			if err != nil {
				t.Fatalf("BuildPlan: %v", err)
			}
			if got := planShape(plan); !slices.Equal(got, tc.wantShape) {
				t.Fatalf("plan = %q, want %q", got, tc.wantShape)
			}
			blockers := plan.Blockers()
			if tc.wantBlocked == "" && len(blockers) > 0 {
				t.Fatalf("plan unexpectedly blocked: %v", blockers)
			}
			if tc.wantBlocked != "" && (len(blockers) != 1 || !strings.Contains(blockers[0], tc.wantBlocked)) {
				t.Fatalf("blockers = %v, want one naming %s", blockers, tc.wantBlocked)
			}
			// A pre-roll hop never updates addons that are compatible with
			// the live control plane.
			if len(plan.Hops) > 1 && plan.Hops[0].From == plan.Hops[0].To {
				for _, st := range plan.Hops[0].Steps {
					if st.Type == StepAddon {
						t.Fatalf("pre-roll hop has addon step %+v; addons are compatible with %s", st, tc.cp)
					}
				}
			}
		})
	}
}

// Steady state (nodegroups one minor behind) and a one-hop upgrade: every
// nodegroup rolls exactly once, straight to the target.
func TestExecute_SteadyStateLagRollsEachNodegroupOnce(t *testing.T) {
	w := newWorld()
	w.clusterVersion = "1.29"
	w.addonVersions = map[string]string{"vpc-cni": latestFor("1.29")}
	w.ngVersions = map[string]string{"workers-a": "1.28", "workers-b": "1.29"}
	m := newWorldMock(w)
	rolls := recordRolls(m)
	svc := newTestService(m)
	ctx := context.Background()

	plan, err := svc.BuildPlan(ctx, "prod-east", "1.30", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if _, err := svc.Execute(ctx, plan, ExecuteOptions{Yes: true}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	assertRolls(t, rolls(), map[string][]string{"workers-a": {"1.30"}, "workers-b": {"1.30"}})
}

// 1.29 → 1.32 with nodegroups at 1.26 / 1.28 / 1.29: only the 1.26 nodegroup
// is pre-rolled (to 1.29, before the control plane leaves 1.29); after that
// every nodegroup rolls once per hop.
func TestExecute_MultiHopPreRollsOnlyTheNodegroupThatNeedsIt(t *testing.T) {
	w := newWorld()
	w.clusterVersion = "1.29"
	w.addonVersions = map[string]string{"vpc-cni": latestFor("1.29")}
	w.ngVersions = map[string]string{"a": "1.26", "b": "1.28", "c": "1.29"}
	m := newWorldMock(w)
	events := recordMutations(m)
	rolls := recordRolls(m)
	svc := newTestService(m)
	ctx := context.Background()

	plan, err := svc.BuildPlan(ctx, "prod-east", "1.32", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if _, err := svc.Execute(ctx, plan, ExecuteOptions{Yes: true}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	want := []string{"ng→1.29"}
	for _, v := range []string{"1.30", "1.31", "1.32"} {
		want = append(want, "cp→"+v, "addon→"+latestFor(v), "ng→"+v, "ng→"+v, "ng→"+v)
	}
	if !slices.Equal(*events, want) {
		t.Fatalf("mutation order = %v, want %v", *events, want)
	}
	assertRolls(t, rolls(), map[string][]string{
		"a": {"1.29", "1.30", "1.31", "1.32"},
		"b": {"1.30", "1.31", "1.32"},
		"c": {"1.30", "1.31", "1.32"},
	})
}

// A 1.29 → 1.31 run fails in the 1.30 addon phase, after the control plane
// moved. The rerun re-derives the plan from live state: addons catch up to
// 1.30, the 1.27 nodegroup (now beyond skew of the next step, 1.31) is
// pre-rolled to 1.30, the 1.29 nodegroup is not, and the run converges.
func TestResume_InterruptedHopPreRollsFromLiveState(t *testing.T) {
	w := newWorld()
	w.clusterVersion = "1.29"
	w.addonVersions = map[string]string{"vpc-cni": latestFor("1.29")}
	w.ngVersions = map[string]string{"a": "1.27", "b": "1.29"}
	w.failAddons = true
	m := newWorldMock(w)
	rolls := recordRolls(m)
	svc := newTestService(m)
	ctx := context.Background()

	plan, err := svc.BuildPlan(ctx, "prod-east", "1.31", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if got, want := planShape(plan), []string{"1.29→1.30 ng[a b]", "1.30→1.31 ng[a b]"}; !slices.Equal(got, want) {
		t.Fatalf("first plan = %q, want %q (1.27 is within skew of 1.30)", got, want)
	}
	if _, err := svc.Execute(ctx, plan, ExecuteOptions{Yes: true}); err == nil {
		t.Fatal("first Execute must fail in the addon phase")
	}
	if w.clusterVersion != "1.30" {
		t.Fatalf("cluster version = %s, want 1.30 (control plane moved before the failure)", w.clusterVersion)
	}

	w.mu.Lock()
	w.failAddons = false
	w.mu.Unlock()
	events := recordMutations(m)

	plan, err = svc.BuildPlan(ctx, "prod-east", "1.31", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan (resume): %v", err)
	}
	if got, want := planShape(plan), []string{"1.30→1.30 ng[a]", "1.30→1.31 ng[a b]"}; !slices.Equal(got, want) {
		t.Fatalf("resume plan = %q, want %q", got, want)
	}
	if s := findStep(t, plan.Hops[0].Steps, StepAddon, "vpc-cni"); s.Status != StatusPending || s.Version != latestFor("1.30") {
		t.Fatalf("catch-up addon step = %+v, want pending → %s", s, latestFor("1.30"))
	}
	if _, err := svc.Execute(ctx, plan, ExecuteOptions{Yes: true}); err != nil {
		t.Fatalf("Execute (resume): %v", err)
	}
	want := []string{"addon→" + latestFor("1.30"), "ng→1.30", "cp→1.31", "addon→" + latestFor("1.31"), "ng→1.31", "ng→1.31"}
	if !slices.Equal(*events, want) {
		t.Fatalf("resume mutation order = %v, want %v", *events, want)
	}
	assertRolls(t, rolls(), map[string][]string{"a": {"1.30", "1.31"}, "b": {"1.31"}})

	plan, err = svc.BuildPlan(ctx, "prod-east", "1.31", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan (after resume): %v", err)
	}
	if n := plan.PendingSteps(); n != 0 {
		t.Fatalf("pending steps after resume = %d, want 0 (%q)", n, planShape(plan))
	}
}

// transientNetErrors are failures that never produced an API response; a
// long wait must poll through them.
func transientNetErrors() []error {
	return []error{
		&net.DNSError{Err: "no such host", Name: "eks.us-east-1.amazonaws.com", IsNotFound: true},
		io.ErrUnexpectedEOF,
		&url.Error{Op: "Post", URL: "https://eks.us-east-1.amazonaws.com", Err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}},
	}
}

func accessDenied(action string) error {
	return &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "not authorized to perform " + action}
}

// unabsorbedErrors is how many of errs reach the wait loop as a warning: the
// retryable ones (EOF, refused dial) are absorbed by common.WithRetry around
// each poll, the rest (an NXDOMAIN) surface and the loop polls through them.
func unabsorbedErrors(errs []error) int {
	n := 0
	for _, err := range errs {
		if !common.IsRetryable(err) {
			n++
		}
	}
	return n
}

// warningCounter counts "warning:" progress lines.
func warningCounter() (ProgressFunc, func() int) {
	var mu sync.Mutex
	n := 0
	return func(format string, _ ...any) {
			if strings.HasPrefix(format, "warning:") {
				mu.Lock()
				n++
				mu.Unlock()
			}
		}, func() int {
			mu.Lock()
			defer mu.Unlock()
			return n
		}
}

// clusterSequenceMock serves DescribeCluster from a script: call 1 returns
// the initial state, the next len(errs) calls fail with errs, and later calls
// return the cluster ACTIVE at settledVersion.
func clusterSequenceMock(initialStatus ekstypes.ClusterStatus, settledVersion string, errs []error) *mocks.EKSAPI {
	m := mocks.NewEKSAPI().Build()
	var mu sync.Mutex
	calls := 0
	m.DescribeClusterFn = func(_ context.Context, in *eks.DescribeClusterInput, _ ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		switch {
		case calls == 1:
			return &eks.DescribeClusterOutput{Cluster: &ekstypes.Cluster{Name: in.Name, Version: aws.String("1.31"), Status: initialStatus}}, nil
		case calls-1 <= len(errs):
			return nil, errs[calls-2]
		default:
			return &eks.DescribeClusterOutput{Cluster: &ekstypes.Cluster{Name: in.Name, Version: aws.String(settledVersion), Status: ekstypes.ClusterStatusActive}}, nil
		}
	}
	m.UpdateClusterVersionFn = func(_ context.Context, _ *eks.UpdateClusterVersionInput, _ ...func(*eks.Options)) (*eks.UpdateClusterVersionOutput, error) {
		return &eks.UpdateClusterVersionOutput{Update: &ekstypes.Update{Id: aws.String("u-cp"), Status: ekstypes.UpdateStatusInProgress}}, nil
	}
	m.DescribeUpdateFn = func(_ context.Context, in *eks.DescribeUpdateInput, _ ...func(*eks.Options)) (*eks.DescribeUpdateOutput, error) {
		return &eks.DescribeUpdateOutput{Update: &ekstypes.Update{Id: in.UpdateId, Status: ekstypes.UpdateStatusSuccessful}}, nil
	}
	return m
}

// Network errors while waiting for the cluster to go ACTIVE (after the EKS
// update succeeded, or when attaching to an in-flight update) are reported
// and polled through; the phase still succeeds.
func TestUpgradeControlPlane_ActiveWaitSurvivesNetworkErrors(t *testing.T) {
	cases := []struct {
		name        string
		initial     ekstypes.ClusterStatus
		wantUpdates int
	}{
		{name: "after the update", initial: ekstypes.ClusterStatusActive, wantUpdates: 1},
		{name: "attach to in-flight update", initial: ekstypes.ClusterStatusUpdating, wantUpdates: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := transientNetErrors()
			m := clusterSequenceMock(tc.initial, "1.32", errs)
			svc := newTestService(m)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			progress, warnings := warningCounter()

			if err := svc.UpgradeControlPlane(ctx, "prod-east", "1.32", progress); err != nil {
				t.Fatalf("UpgradeControlPlane: %v (network errors must keep the wait alive)", err)
			}
			if got, want := warnings(), unabsorbedErrors(errs); got != want {
				t.Fatalf("warnings = %d, want %d", got, want)
			}
			if m.Calls.UpdateClusterVersion != tc.wantUpdates {
				t.Fatalf("UpdateClusterVersion calls = %d, want %d", m.Calls.UpdateClusterVersion, tc.wantUpdates)
			}
		})
	}
}

// A permanent error while waiting for ACTIVE fails the phase at once.
func TestUpgradeControlPlane_ActiveWaitPermanentErrorFailsFast(t *testing.T) {
	m := clusterSequenceMock(ekstypes.ClusterStatusUpdating, "1.32", []error{
		accessDenied("eks:DescribeCluster"), accessDenied("eks:DescribeCluster"),
	})
	svc := newTestService(m)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	progress, warnings := warningCounter()

	err := svc.UpgradeControlPlane(ctx, "prod-east", "1.32", progress)
	if err == nil {
		t.Fatal("expected an error")
	}
	if errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "DescribeCluster") {
		t.Fatalf("err = %v, want the formatted AccessDenied error", err)
	}
	if m.Calls.DescribeCluster != 2 || warnings() != 0 {
		t.Fatalf("DescribeCluster calls = %d, warnings = %d; want 2 and 0", m.Calls.DescribeCluster, warnings())
	}
}

// nodegroupSequenceMock lists one nodegroup and serves DescribeNodegroup from
// a script: call 1 (the listing) returns it UPDATING at 1.31, the next
// len(errs) calls fail with errs, and later calls return it ACTIVE at 1.32.
func nodegroupSequenceMock(errs []error) *mocks.EKSAPI {
	m := mocks.NewEKSAPI().Build()
	m.ListNodegroupsFn = func(_ context.Context, _ *eks.ListNodegroupsInput, _ ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
		return &eks.ListNodegroupsOutput{Nodegroups: []string{"workers-a"}}, nil
	}
	var mu sync.Mutex
	calls := 0
	m.DescribeNodegroupFn = func(_ context.Context, in *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		ng := &ekstypes.Nodegroup{NodegroupName: in.NodegroupName, AmiType: ekstypes.AMITypesAl2023X8664Standard}
		switch {
		case calls == 1:
			ng.Version, ng.Status = aws.String("1.31"), ekstypes.NodegroupStatusUpdating
		case calls-1 <= len(errs):
			return nil, errs[calls-2]
		default:
			ng.Version, ng.Status = aws.String("1.32"), ekstypes.NodegroupStatusActive
		}
		return &eks.DescribeNodegroupOutput{Nodegroup: ng}, nil
	}
	return m
}

// Network errors while attaching to an in-flight nodegroup update are
// reported and polled through; the phase still succeeds.
func TestUpgradeNodegroups_AttachSurvivesNetworkErrors(t *testing.T) {
	errs := transientNetErrors()
	m := nodegroupSequenceMock(errs)
	rolls := captureNodegroupRolls(m)
	svc := newTestService(m)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	progress, warnings := warningCounter()

	if err := svc.UpgradeNodegroups(ctx, "prod-east", "1.32", NodegroupRollOptions{}, progress); err != nil {
		t.Fatalf("UpgradeNodegroups: %v (network errors must keep the attach wait alive)", err)
	}
	if got, want := warnings(), unabsorbedErrors(errs); got != want {
		t.Fatalf("warnings = %d, want %d", got, want)
	}
	if len(*rolls) != 0 {
		t.Fatalf("rolls = %d, want 0 (the in-flight roll reached 1.32)", len(*rolls))
	}
}

// A permanent error while attaching to an in-flight nodegroup update fails
// the phase at once.
func TestUpgradeNodegroups_AttachPermanentErrorFailsFast(t *testing.T) {
	m := nodegroupSequenceMock([]error{accessDenied("eks:DescribeNodegroup"), accessDenied("eks:DescribeNodegroup")})
	svc := newTestService(m)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	progress, warnings := warningCounter()

	err := svc.UpgradeNodegroups(ctx, "prod-east", "1.32", NodegroupRollOptions{}, progress)
	if err == nil {
		t.Fatal("expected an error")
	}
	if errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "DescribeNodegroup") {
		t.Fatalf("err = %v, want the formatted AccessDenied error", err)
	}
	if m.Calls.DescribeNodegroup != 2 || warnings() != 0 {
		t.Fatalf("DescribeNodegroup calls = %d, warnings = %d; want 2 and 0", m.Calls.DescribeNodegroup, warnings())
	}
}

// The nodegroup phase touches only the nodegroups the plan lists, so a
// pre-roll hop leaves lagging siblings for the regular hop.
func TestUpgradeNodegroups_OnlyLimitsThePhase(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithCluster("prod-east", "1.32").
		WithNodegroup("old", "1.29", ekstypes.AMITypesAl2023X8664Standard).
		WithNodegroup("mid", "1.31", ekstypes.AMITypesAl2023X8664Standard).
		WithUpdateStatuses("u-old", ekstypes.UpdateStatusInProgress, ekstypes.UpdateStatusSuccessful).
		WithUpdateStatuses("u-mid", ekstypes.UpdateStatusInProgress, ekstypes.UpdateStatusSuccessful).
		Build()
	rolls := captureNodegroupRolls(m)
	svc := newTestService(m)

	if err := svc.UpgradeNodegroups(context.Background(), "prod-east", "1.32", NodegroupRollOptions{Only: []string{"old"}}, nil); err != nil {
		t.Fatalf("UpgradeNodegroups: %v", err)
	}
	if len(*rolls) != 1 || aws.ToString((*rolls)[0].NodegroupName) != "old" {
		t.Fatalf("rolls = %+v, want only nodegroup old", *rolls)
	}
}
