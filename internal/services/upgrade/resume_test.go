package upgrade

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
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
	"github.com/dantech2000/refresh/internal/services/addons"
)

// recordMutations wraps the world mock's mutating calls so a test can assert
// the order in which the engine changed the cluster.
func recordMutations(m *mocks.EKSAPI) *[]string {
	var (
		mu     sync.Mutex
		events []string
	)
	record := func(e string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
	}
	cp, addon, ng := m.UpdateClusterVersionFn, m.UpdateAddonFn, m.UpdateNodegroupVersionFn
	m.UpdateClusterVersionFn = func(ctx context.Context, in *eks.UpdateClusterVersionInput, o ...func(*eks.Options)) (*eks.UpdateClusterVersionOutput, error) {
		record("cp→" + aws.ToString(in.Version))
		return cp(ctx, in, o...)
	}
	m.UpdateAddonFn = func(ctx context.Context, in *eks.UpdateAddonInput, o ...func(*eks.Options)) (*eks.UpdateAddonOutput, error) {
		record("addon→" + aws.ToString(in.AddonVersion))
		return addon(ctx, in, o...)
	}
	m.UpdateNodegroupVersionFn = func(ctx context.Context, in *eks.UpdateNodegroupVersionInput, o ...func(*eks.Options)) (*eks.UpdateNodegroupVersionOutput, error) {
		record("ng→" + aws.ToString(in.Version))
		return ng(ctx, in, o...)
	}
	return &events
}

// Resume after a 1.31→1.33 run was interrupted once the control plane reached
// 1.32: the rerun must finish 1.32's addons and nodegroups before moving the
// control plane to 1.33.
func TestResume_MidHopCatchUpBeforeNextControlPlane(t *testing.T) {
	w := newWorld()
	w.clusterVersion = "1.32" // hop 1's control plane finished; addons/nodegroups did not
	m := newWorldMock(w)
	events := recordMutations(m)
	svc := newTestService(m)
	ctx := context.Background()

	plan, err := svc.BuildPlan(ctx, "prod-east", "1.33", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Hops) != 2 {
		t.Fatalf("hops = %+v, want a 1.32 catch-up hop then 1.32→1.33", plan.Hops)
	}
	catchUp := plan.Hops[0]
	if catchUp.From != "1.32" || catchUp.To != "1.32" {
		t.Fatalf("first hop = %s→%s, want 1.32→1.32 catch-up", catchUp.From, catchUp.To)
	}
	if s := findStep(t, catchUp.Steps, StepAddon, "vpc-cni"); s.Status != StatusPending || s.Version != latestFor("1.32") {
		t.Fatalf("catch-up addon step = %+v, want pending → %s", s, latestFor("1.32"))
	}
	if s := findStep(t, catchUp.Steps, StepNodegroup, "workers-a"); s.Status != StatusPending || s.Version != "1.32" {
		t.Fatalf("catch-up nodegroup step = %+v, want pending → 1.32", s)
	}
	if plan.Hops[1].From != "1.32" || plan.Hops[1].To != "1.33" {
		t.Fatalf("second hop = %s→%s, want 1.32→1.33", plan.Hops[1].From, plan.Hops[1].To)
	}

	if _, err := svc.Execute(ctx, plan, ExecuteOptions{Yes: true}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	want := []string{
		"addon→" + latestFor("1.32"), "ng→1.32", // catch-up for the live control plane
		"cp→1.33", "addon→" + latestFor("1.33"), "ng→1.33",
	}
	if strings.Join(*events, ",") != strings.Join(want, ",") {
		t.Fatalf("mutation order = %v, want %v", *events, want)
	}
}

// Without nodegroups the addon compatibility signal alone triggers the
// catch-up hop.
func TestBuildPlan_CatchUpFromIncompatibleAddon(t *testing.T) {
	w := newWorld()
	w.clusterVersion = "1.32"
	w.ngVersions = map[string]string{}
	svc := newTestService(newWorldMock(w))

	plan, err := svc.BuildPlan(context.Background(), "prod-east", "1.33", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Hops) != 2 || plan.Hops[0].To != "1.32" {
		t.Fatalf("hops = %+v, want a 1.32 catch-up hop first", plan.Hops)
	}
}

// A cluster whose addons and nodegroups match its control plane gets no
// catch-up hop.
func TestBuildPlan_NoCatchUpWhenCurrent(t *testing.T) {
	svc := newTestService(newWorldMock(newWorld()))

	plan, err := svc.BuildPlan(context.Background(), "prod-east", "1.33", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Hops) != 2 || plan.Hops[0].From != "1.31" || plan.Hops[0].To != "1.32" {
		t.Fatalf("hops = %+v, want exactly 1.31→1.32→1.33", plan.Hops)
	}
}

// EKS evaluates upgrade insights against the next minor only. A blocker for
// 1.33 appears once the cluster runs 1.32, so the engine must re-check
// readiness against live state before the second hop's control plane moves.
func TestExecute_PerHopReadinessBlocksLaterHop(t *testing.T) {
	w := newWorld()
	m := newWorldMock(w)
	m.ListInsightsFn = func(_ context.Context, in *eks.ListInsightsInput, _ ...func(*eks.Options)) (*eks.ListInsightsOutput, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		out := &eks.ListInsightsOutput{}
		if w.clusterVersion == "1.32" && in.Filter != nil && len(in.Filter.KubernetesVersions) == 1 && in.Filter.KubernetesVersions[0] == "1.33" {
			out.Insights = []ekstypes.InsightSummary{{
				Name:          aws.String("Deprecated APIs removed in 1.33"),
				InsightStatus: &ekstypes.InsightStatus{Status: ekstypes.InsightStatusValueError},
			}}
		}
		return out, nil
	}
	svc := newTestService(m)
	ctx := context.Background()

	plan, err := svc.BuildPlan(ctx, "prod-east", "1.33", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if plan.Blocked() {
		t.Fatalf("plan-time readiness cannot see the 1.33 blocker yet: %v", plan.Blockers())
	}
	if r := plan.Hops[1].Steps[0]; r.Type != StepReadiness || !strings.Contains(r.Reason, "live state") {
		t.Fatalf("hop 2 readiness = %+v, want a deferred live check (no insights claim)", r)
	}

	report, err := svc.Execute(ctx, plan, ExecuteOptions{Yes: true})
	if err == nil {
		t.Fatal("Execute must stop before the blocked 1.33 control-plane step")
	}
	if !strings.Contains(err.Error(), "Deprecated APIs removed in 1.33") {
		t.Fatalf("err = %v, want the blocking insight named", err)
	}
	if !strings.Contains(report.FailedAt, "1.32 → 1.33") {
		t.Fatalf("failedAt = %q, want the 1.33 control-plane phase", report.FailedAt)
	}
	if w.clusterVersion != "1.32" {
		t.Fatalf("cluster version = %s, want 1.32 (1.33 must not start)", w.clusterVersion)
	}
	if m.Calls.UpdateClusterVersion != 1 {
		t.Fatalf("UpdateClusterVersion calls = %d, want 1 (hop 1 only)", m.Calls.UpdateClusterVersion)
	}
}

// Ctrl+C mid-roll, then rerun: the nodegroup is still UPDATING at the old
// version. The phase attaches, waits for it to settle, and skips the roll
// once the version reached the target.
func TestUpgradeNodegroups_AttachesToInFlightRoll(t *testing.T) {
	cases := []struct {
		name         string
		settledAt    string
		wantNewRolls int
	}{
		{name: "in-flight roll reached target", settledAt: "1.32", wantNewRolls: 0},
		{name: "in-flight update was something else", settledAt: "1.31", wantNewRolls: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := mocks.NewEKSAPI().
				WithCluster("prod-east", "1.32").
				WithNodegroup("workers-a", "1.31", ekstypes.AMITypesAl2023X8664Standard).
				WithDescribeUpdate(ekstypes.UpdateStatusSuccessful).
				Build()
			var mu sync.Mutex
			describes := 0
			m.DescribeNodegroupFn = func(_ context.Context, in *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
				mu.Lock()
				defer mu.Unlock()
				describes++
				ng := &ekstypes.Nodegroup{
					NodegroupName: in.NodegroupName,
					Version:       aws.String("1.31"),
					AmiType:       ekstypes.AMITypesAl2023X8664Standard,
					Status:        ekstypes.NodegroupStatusUpdating,
				}
				if describes > 3 { // listing + two polls still UPDATING
					ng.Version = aws.String(tc.settledAt)
					ng.Status = ekstypes.NodegroupStatusActive
				}
				return &eks.DescribeNodegroupOutput{Nodegroup: ng}, nil
			}
			rolls := captureNodegroupRolls(m)
			svc := newTestService(m)

			if err := svc.UpgradeNodegroups(context.Background(), "prod-east", "1.32", NodegroupRollOptions{}, nil); err != nil {
				t.Fatalf("UpgradeNodegroups: %v (must attach, not fail the ACTIVE gate)", err)
			}
			if len(*rolls) != tc.wantNewRolls {
				t.Fatalf("new rolls = %d, want %d", len(*rolls), tc.wantNewRolls)
			}
		})
	}
}

// Waiting on an in-flight roll honors ctx.
func TestUpgradeNodegroups_AttachHonorsContext(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithCluster("prod-east", "1.32").
		Build()
	m.ListNodegroupsFn = func(_ context.Context, _ *eks.ListNodegroupsInput, _ ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
		return &eks.ListNodegroupsOutput{Nodegroups: []string{"workers-a"}}, nil
	}
	m.DescribeNodegroupFn = func(_ context.Context, in *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
		return &eks.DescribeNodegroupOutput{Nodegroup: &ekstypes.Nodegroup{
			NodegroupName: in.NodegroupName,
			Version:       aws.String("1.31"),
			Status:        ekstypes.NodegroupStatusUpdating,
		}}, nil
	}
	svc := newTestService(m)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := svc.UpgradeNodegroups(ctx, "prod-east", "1.32", NodegroupRollOptions{}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context deadline", err)
	}
}

// A permanent DescribeUpdate error (AccessDenied) fails the watch at once
// instead of warning every poll for hours.
func TestWaitForUpdate_PermanentErrorFailsFast(t *testing.T) {
	m := mocks.NewEKSAPI().Build()
	m.DescribeUpdateFn = func(_ context.Context, _ *eks.DescribeUpdateInput, _ ...func(*eks.Options)) (*eks.DescribeUpdateOutput, error) {
		return nil, &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "not authorized to perform eks:DescribeUpdate"}
	}
	svc := newTestService(m)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var warnings int
	err := svc.waitForUpdate(ctx, &eks.DescribeUpdateInput{Name: aws.String("prod-east"), UpdateId: aws.String("u-1")},
		"control plane upgrade to 1.32", func(string, ...any) { warnings++ })
	if err == nil {
		t.Fatal("expected an error")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v: watch kept polling a permanent error until the deadline", err)
	}
	if !strings.Contains(err.Error(), "DescribeUpdate") {
		t.Fatalf("err = %v, want the formatted AWS error", err)
	}
	if m.Calls.DescribeUpdate != 1 || warnings != 0 {
		t.Fatalf("DescribeUpdate calls = %d, warnings = %d; want 1 and 0", m.Calls.DescribeUpdate, warnings)
	}
}

// An API failure while looking up addon versions is reported as such, not
// as "no compatible version".
func TestBuildPlan_AddonVersionLookupErrorIsNotIncompatibility(t *testing.T) {
	m := twoHopMock()
	m.DescribeAddonVersionsFn = func(_ context.Context, _ *eks.DescribeAddonVersionsInput, _ ...func(*eks.Options)) (*eks.DescribeAddonVersionsOutput, error) {
		return nil, &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "not authorized to perform eks:DescribeAddonVersions"}
	}
	svc := newTestService(m)

	plan, err := svc.BuildPlan(context.Background(), "prod-east", "1.32", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	step := findStep(t, plan.Hops[0].Steps, StepAddon, "vpc-cni")
	if step.Status != StatusBlocked {
		t.Fatalf("status = %s, want blocked (can't plan the addon safely)", step.Status)
	}
	if strings.Contains(step.Reason, "is compatible") || !strings.Contains(step.Reason, "could not look up") {
		t.Fatalf("reason = %q, want an API-error explanation, not an incompatibility claim", step.Reason)
	}
}

// A genuinely empty catalogue still reads as an incompatibility.
func TestBuildPlan_EmptyAddonCatalogueIsIncompatibility(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithCluster("prod-east", "1.31").
		WithAddon("legacy-addon", "v0.9.0", ekstypes.AddonStatusActive).
		WithNodegroup("workers-a", "1.31", ekstypes.AMITypesAl2023X8664Standard).
		Build()
	svc := newTestService(m)

	plan, err := svc.BuildPlan(context.Background(), "prod-east", "1.32", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	step := findStep(t, plan.Hops[0].Steps, StepAddon, "legacy-addon")
	if step.Status != StatusBlocked || !strings.Contains(step.Reason, "no version of legacy-addon is compatible") {
		t.Fatalf("step = %+v, want blocked as incompatible", step)
	}
	if _, err := addons.NewService(m, testLogger()).GetAvailableVersions(context.Background(), "legacy-addon", "1.32"); !errors.Is(err, addons.ErrNoVersionsFound) {
		t.Fatalf("GetAvailableVersions err = %v, want ErrNoVersionsFound", err)
	}
}

// Network-class DescribeUpdate failures (DNS, refused connection, EOF) are
// transient from the watch's point of view: a VPN drop or laptop sleep must
// not fail a long-running phase.
func TestWaitForUpdate_NetworkErrorsKeepPolling(t *testing.T) {
	transient := []error{
		&net.DNSError{Err: "no such host", Name: "eks.us-east-1.amazonaws.com", IsNotFound: true},
		io.ErrUnexpectedEOF,
		&url.Error{Op: "Post", URL: "https://eks.us-east-1.amazonaws.com", Err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}},
	}
	m := mocks.NewEKSAPI().Build()
	var mu sync.Mutex
	calls := 0
	m.DescribeUpdateFn = func(_ context.Context, in *eks.DescribeUpdateInput, _ ...func(*eks.Options)) (*eks.DescribeUpdateOutput, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls <= len(transient) {
			return nil, transient[calls-1]
		}
		return &eks.DescribeUpdateOutput{Update: &ekstypes.Update{Id: in.UpdateId, Status: ekstypes.UpdateStatusSuccessful}}, nil
	}
	svc := newTestService(m)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	warnings := 0
	err := svc.waitForUpdate(ctx, &eks.DescribeUpdateInput{UpdateId: aws.String("u-1")}, "nodegroup roll",
		func(string, ...any) { warnings++ })
	if err != nil {
		t.Fatalf("waitForUpdate: %v (network errors must keep the watch alive)", err)
	}
	if warnings != len(transient) {
		t.Fatalf("warnings = %d, want %d", warnings, len(transient))
	}
}

// Catch-up must not plan a roll across a gap already beyond the kubelet
// skew, and the next hop's readiness must still block on that nodegroup.
func TestBuildPlan_CatchUpKeepsSkewBlocker(t *testing.T) {
	w := newWorld()
	w.clusterVersion = "1.31"
	w.ngVersions = map[string]string{"ng-a": "1.30", "ng-b": "1.27"}
	svc := newTestService(newWorldMock(w))

	plan, err := svc.BuildPlan(context.Background(), "prod-east", "1.32", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Hops) != 2 || plan.Hops[0].To != "1.31" {
		t.Fatalf("hops = %+v, want a 1.31 catch-up hop (ng-a lags) then 1.31→1.32", plan.Hops)
	}
	if s := findStep(t, plan.Hops[0].Steps, StepNodegroup, "ng-a"); s.Status != StatusPending {
		t.Fatalf("ng-a catch-up step = %+v, want pending", s)
	}
	if s := findStep(t, plan.Hops[0].Steps, StepNodegroup, "ng-b"); s.Status != StatusBlocked {
		t.Fatalf("ng-b catch-up step = %+v, want blocked (no direct 1.27→1.31 roll)", s)
	}
	readiness := plan.Hops[1].Steps[0]
	if readiness.Type != StepReadiness || readiness.Status != StatusBlocked || !strings.Contains(readiness.Reason, "ng-b") {
		t.Fatalf("1.32 readiness = %+v, want blocked on ng-b's kubelet skew", readiness)
	}
	if strings.Contains(readiness.Reason, "ng-a") {
		t.Fatalf("1.32 readiness = %+v: ng-a is caught up to 1.31 and within skew", readiness)
	}
}

// Addon --skip uses exact, case-insensitive names when deciding on a
// catch-up hop; a substring does not skip.
func TestBuildPlan_CatchUpAddonSkipIsExact(t *testing.T) {
	cases := []struct {
		skip        []string
		wantCatchUp bool
	}{
		{skip: []string{"VPC-CNI"}, wantCatchUp: false},
		{skip: []string{"vpc"}, wantCatchUp: true},
	}
	for _, tc := range cases {
		w := newWorld()
		w.clusterVersion = "1.32"
		w.ngVersions = map[string]string{}
		svc := newTestService(newWorldMock(w))

		plan, err := svc.BuildPlan(context.Background(), "prod-east", "1.33", PlanOptions{SkipAddons: tc.skip})
		if err != nil {
			t.Fatalf("BuildPlan: %v", err)
		}
		if gotCatchUp := len(plan.Hops) == 2; gotCatchUp != tc.wantCatchUp {
			t.Fatalf("skip %v: catch-up = %v, want %v (hops %+v)", tc.skip, gotCatchUp, tc.wantCatchUp, plan.Hops)
		}
	}
}
