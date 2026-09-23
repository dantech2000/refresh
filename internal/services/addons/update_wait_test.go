package addons

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/smithy-go"

	"github.com/dantech2000/refresh/internal/mocks"
)

const pollFast = time.Millisecond

// waitMock is a cluster with vpc-cni installed at installed, a catalog of
// available versions, and UpdateAddon returning update ID "u-1".
func waitMock(installed string, status ekstypes.AddonStatus, available ...string) *mocks.EKSAPIBuilder {
	return mocks.NewEKSAPI().
		WithCluster("prod", "1.32").
		WithAddon("vpc-cni", installed, status).
		WithAddonVersions("vpc-cni", available, "1.32").
		WithUpdateAddon("u-1")
}

func waitOpts(version string) UpdateOptions {
	return UpdateOptions{Version: version, Wait: true, WaitTimeout: 5 * time.Second, PollInterval: pollFast}
}

// The wait follows the update ID through DescribeUpdate (with the add-on
// name) and reports COMPLETED only once EKS says Successful and the add-on
// reports the target version.
func TestUpdateWait_FollowsUpdateIDToSuccess(t *testing.T) {
	m := waitMock("v1.18.0", ekstypes.AddonStatusActive, "v1.19.0", "v1.18.0").
		WithUpdateStatuses("u-1", ekstypes.UpdateStatusInProgress, ekstypes.UpdateStatusInProgress, ekstypes.UpdateStatusSuccessful).
		Build()
	var sawAddonName atomic.Bool
	describeUpdate := m.DescribeUpdateFn
	m.DescribeUpdateFn = func(ctx context.Context, in *eks.DescribeUpdateInput, o ...func(*eks.Options)) (*eks.DescribeUpdateOutput, error) {
		if aws.ToString(in.AddonName) == "vpc-cni" && aws.ToString(in.Name) == "prod" {
			sawAddonName.Store(true)
		}
		return describeUpdate(ctx, in, o...)
	}
	mocks.SettleAddonUpdates(m)

	res, err := NewService(m, logger()).Update(t.Context(), "prod", "vpc-cni", waitOpts("latest"))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if res.Status != StatusCompleted || res.UpdateID != "u-1" || res.NewVersion != "v1.19.0" {
		t.Fatalf("result = %+v, want COMPLETED u-1 -> v1.19.0", res)
	}
	if m.Calls.DescribeUpdate != 3 {
		t.Errorf("DescribeUpdate calls = %d, want 3 (InProgress, InProgress, Successful)", m.Calls.DescribeUpdate)
	}
	if !sawAddonName.Load() {
		t.Error("DescribeUpdate must name the cluster and the add-on")
	}
}

// Regression: an add-on that stays ACTIVE while its update is still
// InProgress is not COMPLETED; the wait runs to its timeout.
func TestUpdateWait_ActiveAddonWithUnfinishedUpdateIsNotCompleted(t *testing.T) {
	m := waitMock("v1.18.0", ekstypes.AddonStatusActive, "v1.19.0").
		WithUpdateStatuses("u-1", ekstypes.UpdateStatusInProgress).
		Build()
	opts := waitOpts("latest")
	opts.WaitTimeout = 30 * time.Millisecond

	res, err := NewService(m, logger()).Update(t.Context(), "prod", "vpc-cni", opts)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a deadline error", err)
	}
	if res == nil || res.Status != StatusWaitFailed || res.UpdateID != "u-1" {
		t.Fatalf("result = %+v, want WAIT_FAILED with update ID u-1", res)
	}
	if !strings.Contains(err.Error(), "last status InProgress") {
		t.Errorf("err = %v, want the last update status", err)
	}
}

// A Failed or Cancelled update is a failure with EKS's error details, even
// though the add-on itself stays ACTIVE.
func TestUpdateWait_FailedOrCancelledUpdate(t *testing.T) {
	for _, st := range []ekstypes.UpdateStatus{ekstypes.UpdateStatusFailed, ekstypes.UpdateStatusCancelled} {
		t.Run(string(st), func(t *testing.T) {
			m := waitMock("v1.18.0", ekstypes.AddonStatusActive, "v1.19.0").Build()
			m.DescribeUpdateFn = func(_ context.Context, in *eks.DescribeUpdateInput, _ ...func(*eks.Options)) (*eks.DescribeUpdateOutput, error) {
				return &eks.DescribeUpdateOutput{Update: &ekstypes.Update{
					Id:     in.UpdateId,
					Status: st,
					Errors: []ekstypes.ErrorDetail{{
						ErrorCode:    ekstypes.ErrorCodeConfigurationConflict,
						ErrorMessage: aws.String("conflicts found when trying to apply"),
						ResourceIds:  []string{"vpc-cni"},
					}},
				}}, nil
			}

			res, err := NewService(m, logger()).Update(t.Context(), "prod", "vpc-cni", waitOpts("latest"))
			if err == nil {
				t.Fatal("want an error for a failed update")
			}
			for _, want := range []string{string(st), "ConfigurationConflict", "conflicts found", "[vpc-cni]"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %v, want it to contain %q", err, want)
				}
			}
			if res == nil || res.Status != StatusWaitFailed || res.UpdateID != "u-1" || res.Error == "" {
				t.Fatalf("result = %+v, want WAIT_FAILED with update ID and error", res)
			}
		})
	}
}

// EKS reports Successful but the add-on is not at the target: a failure.
func TestUpdateWait_SuccessfulButVersionMismatch(t *testing.T) {
	m := waitMock("v1.18.0", ekstypes.AddonStatusActive, "v1.19.0").
		WithUpdateStatuses("u-1", ekstypes.UpdateStatusSuccessful).
		Build()

	res, err := NewService(m, logger()).Update(t.Context(), "prod", "vpc-cni", waitOpts("latest"))
	if err == nil || !strings.Contains(err.Error(), "is at v1.18.0, not v1.19.0") {
		t.Fatalf("err = %v, want a version mismatch", err)
	}
	if res.Status != StatusWaitFailed {
		t.Errorf("status = %s, want WAIT_FAILED", res.Status)
	}
}

// Regression: a DEGRADED add-on (allowed by the pre-check) is not failed by
// the wait just because it starts DEGRADED.
func TestUpdateWait_DegradedAddonCanBeRepaired(t *testing.T) {
	m := waitMock("v1.18.0", ekstypes.AddonStatusDegraded, "v1.19.0").
		WithUpdateStatuses("u-1", ekstypes.UpdateStatusInProgress, ekstypes.UpdateStatusSuccessful).
		Build()
	mocks.SettleAddonUpdates(m)
	opts := waitOpts("latest")
	opts.HealthCheck = true

	res, err := NewService(m, logger()).Update(t.Context(), "prod", "vpc-cni", opts)
	if err != nil || res.Status != StatusCompleted {
		t.Fatalf("Update = %+v, %v; want COMPLETED", res, err)
	}
}

// A permanent poll error (AccessDenied) fails at once with the formatted
// permission guidance, instead of a bare deadline error after the timeout.
func TestUpdateWait_PermanentPollErrorFailsFast(t *testing.T) {
	m := waitMock("v1.18.0", ekstypes.AddonStatusActive, "v1.19.0").Build()
	m.DescribeUpdateFn = func(context.Context, *eks.DescribeUpdateInput, ...func(*eks.Options)) (*eks.DescribeUpdateOutput, error) {
		return nil, &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "not authorized to perform: eks:DescribeUpdate"}
	}
	opts := waitOpts("latest")
	opts.WaitTimeout = time.Minute

	start := time.Now()
	_, err := NewService(m, logger()).Update(t.Context(), "prod", "vpc-cni", opts)
	if time.Since(start) > 10*time.Second {
		t.Fatalf("wait took %v; a permanent error must fail fast", time.Since(start))
	}
	var ae smithy.APIError
	if !errors.As(err, &ae) || ae.ErrorCode() != "AccessDeniedException" {
		t.Fatalf("err = %v, want the AccessDeniedException", err)
	}
	if !strings.Contains(err.Error(), "insufficient AWS permissions") {
		t.Errorf("err = %v, want FormatAWSError permission guidance", err)
	}
	if m.Calls.DescribeUpdate != 1 {
		t.Errorf("DescribeUpdate calls = %d, want 1", m.Calls.DescribeUpdate)
	}
}

// Transient poll errors are polled through; a timeout after them names the
// last one.
func TestUpdateWait_TransientPollErrors(t *testing.T) {
	throttle := &smithy.GenericAPIError{Code: "ThrottlingException", Message: "Rate exceeded"}

	t.Run("polled through", func(t *testing.T) {
		m := waitMock("v1.18.0", ekstypes.AddonStatusActive, "v1.19.0").
			WithUpdateStatuses("u-1", ekstypes.UpdateStatusSuccessful).
			Build()
		scripted := m.DescribeUpdateFn
		var n atomic.Int32
		m.DescribeUpdateFn = func(ctx context.Context, in *eks.DescribeUpdateInput, o ...func(*eks.Options)) (*eks.DescribeUpdateOutput, error) {
			if n.Add(1) <= 2 {
				return nil, throttle
			}
			return scripted(ctx, in, o...)
		}
		mocks.SettleAddonUpdates(m)

		res, err := NewService(m, logger()).Update(t.Context(), "prod", "vpc-cni", waitOpts("latest"))
		if err != nil || res.Status != StatusCompleted {
			t.Fatalf("Update = %+v, %v; want COMPLETED after throttling", res, err)
		}
	})

	t.Run("timeout names the last error", func(t *testing.T) {
		m := waitMock("v1.18.0", ekstypes.AddonStatusActive, "v1.19.0").Build()
		m.DescribeUpdateFn = func(context.Context, *eks.DescribeUpdateInput, ...func(*eks.Options)) (*eks.DescribeUpdateOutput, error) {
			return nil, throttle
		}
		opts := waitOpts("latest")
		opts.WaitTimeout = 30 * time.Millisecond

		_, err := NewService(m, logger()).Update(t.Context(), "prod", "vpc-cni", opts)
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "last poll error: ThrottlingException") {
			t.Fatalf("err = %v, want a deadline error naming the throttling", err)
		}
	})
}

// WaitUntilActive (the upgrade orchestrator's resume path) also fails fast on
// a permanent error instead of swallowing it until the timeout.
func TestWaitUntilActive_PermanentErrorFailsFast(t *testing.T) {
	m := mocks.NewEKSAPI().WithCluster("prod", "1.32").Build()
	m.DescribeAddonFn = func(context.Context, *eks.DescribeAddonInput, ...func(*eks.Options)) (*eks.DescribeAddonOutput, error) {
		return nil, &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "not authorized to perform: eks:DescribeAddon"}
	}
	err := NewService(m, logger()).WaitUntilActive(t.Context(), "prod", "vpc-cni", time.Minute, pollFast)
	if err == nil || !strings.Contains(err.Error(), "insufficient AWS permissions") {
		t.Fatalf("err = %v, want formatted permission error", err)
	}
	if m.Calls.DescribeAddon != 1 {
		t.Errorf("DescribeAddon calls = %d, want 1", m.Calls.DescribeAddon)
	}
}

// Already-current and downgrade guard.
func TestUpdate_VersionGuard(t *testing.T) {
	cases := []struct {
		name, installed, version string
		available                []string
		wantStatus               string
		wantUpdate               bool
		wantWarning              string
	}{
		{name: "latest equals installed", installed: "v1.19.0", version: "latest", available: []string{"v1.19.0", "v1.18.0"}, wantStatus: StatusUpToDate},
		{name: "latest older than installed", installed: "v1.11.4", version: "latest", available: []string{"v1.11.3"}, wantStatus: StatusUpToDate},
		{name: "pinned equals installed", installed: "v1.18.0", version: "v1.18.0", available: []string{"v1.19.0", "v1.18.0"}, wantStatus: StatusUpToDate},
		{name: "pinned downgrade proceeds with warning", installed: "v1.19.0", version: "v1.18.0", available: []string{"v1.19.0", "v1.18.0"},
			wantStatus: string(ekstypes.UpdateStatusInProgress), wantUpdate: true, wantWarning: "downgrading vpc-cni from v1.19.0 to v1.18.0"},
		{name: "upgrade", installed: "v1.18.0", version: "latest", available: []string{"v1.19.0", "v1.18.0"},
			wantStatus: string(ekstypes.UpdateStatusInProgress), wantUpdate: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := waitMock(tc.installed, ekstypes.AddonStatusActive, tc.available...).Build()
			res, err := NewService(m, logger()).Update(t.Context(), "prod", "vpc-cni", UpdateOptions{Version: tc.version})
			if err != nil {
				t.Fatalf("Update: %v", err)
			}
			if res.Status != tc.wantStatus || res.Warning != tc.wantWarning {
				t.Fatalf("result = %+v, want status %s warning %q", res, tc.wantStatus, tc.wantWarning)
			}
			if got := m.Calls.UpdateAddon == 1; got != tc.wantUpdate {
				t.Errorf("UpdateAddon calls = %d, want update %v", m.Calls.UpdateAddon, tc.wantUpdate)
			}
			if tc.wantStatus == StatusUpToDate && res.NewVersion != tc.installed {
				t.Errorf("NewVersion = %s, want the installed %s", res.NewVersion, tc.installed)
			}
		})
	}
}

// UpdateAll: current add-ons are UP_TO_DATE with no UpdateAddon call, and a
// failed wait keeps its update ID and reason.
func TestUpdateAll_UpToDateAndWaitFailure(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithCluster("prod", "1.32").
		WithAddon("coredns", "v1.11.4", ekstypes.AddonStatusActive).
		WithAddon("vpc-cni", "v1.18.0", ekstypes.AddonStatusActive).
		WithAddonVersions("coredns", []string{"v1.11.3"}, "1.32").
		WithAddonVersions("vpc-cni", []string{"v1.19.0"}, "1.32").
		WithUpdateAddon("u-vpc").
		WithUpdateStatuses("u-vpc", ekstypes.UpdateStatusFailed).
		Build()

	results, err := NewService(m, logger()).UpdateAll(t.Context(), "prod", UpdateAllOptions{Wait: true, WaitTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("UpdateAll: %v", err)
	}
	byName := map[string]AddonUpdateResult{}
	for _, r := range results {
		byName[r.AddonName] = r
	}
	if r := byName["coredns"]; r.Status != StatusUpToDate || r.UpdateID != "" {
		t.Errorf("coredns = %+v, want UP_TO_DATE with no update", r)
	}
	if r := byName["vpc-cni"]; r.Status != StatusWaitFailed || r.UpdateID != "u-vpc" || !strings.Contains(r.Error, "Failed") {
		t.Errorf("vpc-cni = %+v, want WAIT_FAILED keeping update ID u-vpc and the reason", r)
	}
	if m.Calls.UpdateAddon != 1 {
		t.Errorf("UpdateAddon calls = %d, want 1 (coredns is current)", m.Calls.UpdateAddon)
	}
}

// ListDetailed keeps add-ons that could not be described apart, by name and
// reason; List still returns a named UNKNOWN row for each.
func TestListDetailed_DescribeFailures(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithCluster("prod", "1.32").
		WithAddon("coredns", "v1.11.4", ekstypes.AddonStatusActive).
		WithAddon("kube-proxy", "v1.32.0", ekstypes.AddonStatusActive).
		WithAddon("vpc-cni", "v1.19.0", ekstypes.AddonStatusActive).
		Build()
	describe := m.DescribeAddonFn
	m.DescribeAddonFn = func(ctx context.Context, in *eks.DescribeAddonInput, o ...func(*eks.Options)) (*eks.DescribeAddonOutput, error) {
		if aws.ToString(in.AddonName) != "coredns" {
			return nil, &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "denied"}
		}
		return describe(ctx, in, o...)
	}
	svc := NewService(m, logger())

	res, err := svc.ListDetailed(t.Context(), "prod", ListOptions{})
	if err != nil {
		t.Fatalf("ListDetailed: %v", err)
	}
	if len(res.Summaries) != 1 || res.Summaries[0].Name != "coredns" || res.Summaries[0].Health != "" {
		t.Errorf("summaries = %+v, want only coredns with no health", res.Summaries)
	}
	if len(res.Failures) != 2 || !strings.HasPrefix(res.Failures[0], "kube-proxy: AccessDeniedException") {
		t.Errorf("failures = %q, want kube-proxy and vpc-cni with the reason", res.Failures)
	}

	rows, err := svc.List(t.Context(), "prod", ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, r := range rows {
		if r.Name == "" {
			t.Errorf("List returned an unnamed row: %+v", rows)
		}
	}
}

// Add-ons never described because ctx ended are failures with the cause,
// never zero-value rows.
func TestListDetailed_UndispatchedAreNamedFailures(t *testing.T) {
	names := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l"}
	b := mocks.NewEKSAPI().WithCluster("prod", "1.32")
	for _, n := range names {
		b.WithAddon(n, "v1.0.0", ekstypes.AddonStatusActive)
	}
	m := b.Build()
	ctx, cancel := context.WithCancel(t.Context())
	m.DescribeAddonFn = func(fctx context.Context, _ *eks.DescribeAddonInput, _ ...func(*eks.Options)) (*eks.DescribeAddonOutput, error) {
		cancel()
		<-fctx.Done()
		return nil, fctx.Err()
	}

	res, err := NewService(m, logger()).ListDetailed(ctx, "prod", ListOptions{})
	if err != nil {
		t.Fatalf("ListDetailed: %v", err)
	}
	if len(res.Summaries) != 0 || len(res.Failures) != len(names) {
		t.Fatalf("got %d summaries, %d failures; want 0 and %d", len(res.Summaries), len(res.Failures), len(names))
	}
	notDescribed := 0
	for i, f := range res.Failures {
		if !strings.HasPrefix(f, names[i]+": ") {
			t.Errorf("failure %d = %q, want it named %s", i, f, names[i])
		}
		if strings.Contains(f, "not described: context canceled") {
			notDescribed++
		}
	}
	if notDescribed == 0 {
		t.Errorf("failures = %q, want undispatched add-ons reported with the context cause", res.Failures)
	}
}
