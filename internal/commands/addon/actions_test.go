package addon

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/smithy-go"

	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/mocks"
	"github.com/dantech2000/refresh/internal/services/addons"
)

// pagedAddons is an addons service over a mock whose ListAddons returns
// pages (one ListAddons call per page, linked by NextToken), like the SDK.
func pagedAddons(pages ...[]string) (*addons.ServiceImpl, *mocks.EKSAPI) {
	m := mocks.NewEKSAPI().Build()
	m.ListAddonsFn = func(_ context.Context, in *eks.ListAddonsInput, _ ...func(*eks.Options)) (*eks.ListAddonsOutput, error) {
		i := 0
		if in.NextToken != nil {
			i, _ = strconv.Atoi(*in.NextToken)
		}
		out := &eks.ListAddonsOutput{Addons: pages[i]}
		if i+1 < len(pages) {
			out.NextToken = aws.String(strconv.Itoa(i + 1))
		}
		return out, nil
	}
	return addons.NewService(m, slog.New(slog.DiscardHandler)), m
}

// Regression: when ListAddons fails (e.g. AccessDeniedException on
// eks:ListAddons) the SDK returns (nil, err). The resolver must surface the
// formatted API error instead of dereferencing the nil response.
func TestResolveAddonName_ListAddonsErrorReturnsFormattedError(t *testing.T) {
	m := mocks.NewEKSAPI().Build()
	m.ListAddonsFn = func(context.Context, *eks.ListAddonsInput, ...func(*eks.Options)) (*eks.ListAddonsOutput, error) {
		return nil, &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "not authorized to perform: eks:ListAddons"}
	}
	svc := addons.NewService(m, slog.New(slog.DiscardHandler))

	got, _, err := resolveAddonName(t.Context(), svc, "my-cluster", "vpc cni")
	if got != "" || err == nil {
		t.Fatalf("got %q, %v; want an error", got, err)
	}
	var ae smithy.APIError
	if !errors.As(err, &ae) || !strings.Contains(err.Error(), "listing add-ons") {
		t.Errorf("error should wrap the AWS failure and name the operation, got %v", err)
	}
}

func TestResolveAddonName_Matching(t *testing.T) {
	// Two pages: the resolver must see add-ons past the first page.
	svc, _ := pagedAddons(
		[]string{"coredns", "kube-proxy", "Vpc-Cni"},
		[]string{"vpc-cni", "aws-ebs-csi-driver", "aws-efs-csi-driver", "eks-pod-identity-agent"},
	)
	cases := []struct {
		in, want, wantErr string
		partial           bool
	}{
		{in: "vpc-cni", want: "vpc-cni"},                                      // exact wins over the case-insensitive twin
		{in: "COREDNS", want: "coredns"},                                      // case-insensitive exact
		{in: "pod-identity", want: "eks-pod-identity-agent", partial: true},   // unique substring, second page
		{in: "PROXY", want: "kube-proxy", partial: true},                      // case-insensitive substring
		{in: "csi-driver", wantErr: "ambiguous"},                              // two substring matches
		{in: "totally bogus", wantErr: "invalid add-on name 'totally bogus'"}, // no match
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, partial, err := resolveAddonName(t.Context(), svc, "my-cluster", tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("got %q, %v; want error containing %q", got, err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want || partial != tc.partial {
				t.Fatalf("got %q (partial %v), %v; want %q (partial %v)", got, partial, err, tc.want, tc.partial)
			}
		})
	}
}

func TestResolveAddonName_AmbiguousListsCandidates(t *testing.T) {
	svc, _ := pagedAddons([]string{"aws-ebs-csi-driver", "aws-efs-csi-driver", "coredns"})
	_, _, err := resolveAddonName(t.Context(), svc, "my-cluster", "csi")
	if err == nil || !strings.Contains(err.Error(), "aws-ebs-csi-driver") || !strings.Contains(err.Error(), "aws-efs-csi-driver") ||
		strings.Contains(err.Error(), "coredns") {
		t.Errorf("err = %v, want it to list exactly the two csi candidates", err)
	}
}

// failed returns an add-on result with status and a failure for reason.
func failed(name string, status addons.UpdateStatus, reason diag.Reason) addons.AddonUpdateResult {
	f := diag.New(diag.KindAddon, name, reason, "x")
	return addons.AddonUpdateResult{AddonName: name, Status: status, Failure: &f}
}

// Add-ons a parallel --all run never started (a deadline) count as failures,
// so the command exits 4. After Ctrl+C the run exits 1 (interrupted).
func TestUpdateAllFailureError_CountsNotAttempted(t *testing.T) {
	results := []addons.AddonUpdateResult{
		failed("vpc-cni", addons.StatusFailed, diag.ReasonTimeout),
		failed("coredns", addons.StatusNotAttempted, diag.ReasonNotAttempted),
		failed("kube-proxy", addons.StatusNotAttempted, diag.ReasonNotAttempted),
	}
	fs := resultFailures(results, "us-east-1")
	err := updateAllFailureError(t.Context(), results, fs)
	if err == nil || !strings.Contains(err.Error(), "3 failure(s) (3 addon)") {
		t.Fatalf("err = %v, want 3 addon failures", err)
	}
	if code := exitCodeOf(err); code != 4 {
		t.Errorf("exit code = %d, want 4", code)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if code := exitCodeOf(updateAllFailureError(ctx, results, fs)); code != 1 {
		t.Errorf("interrupted: exit code = %d, want 1", code)
	}
}

// A failure outranks post-update health issues: exit 4, not 5.
func TestUpdateAllFailureError_FailureOutranksIssues(t *testing.T) {
	results := []addons.AddonUpdateResult{
		{AddonName: "vpc-cni", Status: addons.StatusCompletedWithIssues},
		failed("coredns", addons.StatusWaitFailed, diag.ReasonUpdateFailed),
	}
	if code := exitCodeOf(updateAllFailureError(t.Context(), results, resultFailures(results, ""))); code != 4 {
		t.Errorf("exit code = %d, want 4", code)
	}
	if code := exitCodeOf(updateAllFailureError(t.Context(), results[:1], nil)); code != 5 {
		t.Errorf("issues only: exit code = %d, want 5", code)
	}
}

// resultFailures fills in the region the service does not know, and sorts.
func TestResultFailures(t *testing.T) {
	results := []addons.AddonUpdateResult{
		failed("vpc-cni", addons.StatusFailed, diag.ReasonThrottled),
		{AddonName: "kube-proxy", Status: addons.StatusCompleted},
		failed("coredns", addons.StatusWaitFailed, diag.ReasonUpdateFailed),
	}
	fs := resultFailures(results, "eu-west-1")
	if len(fs) != 2 || fs[0].Name != "coredns" || fs[1].Name != "vpc-cni" || fs[0].Region != "eu-west-1" {
		t.Errorf("failures = %+v, want coredns then vpc-cni in eu-west-1", fs)
	}
	if results[0].Failure.Region != "eu-west-1" {
		t.Error("the result's own failure must carry the region too")
	}
}
