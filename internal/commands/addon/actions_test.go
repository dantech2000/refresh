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

	got, err := resolveAddonName(t.Context(), svc, "my-cluster", "vpc cni")
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
	}{
		{in: "vpc-cni", want: "vpc-cni"},                                      // exact wins over the case-insensitive twin
		{in: "COREDNS", want: "coredns"},                                      // case-insensitive exact
		{in: "pod-identity", want: "eks-pod-identity-agent"},                  // unique substring, second page
		{in: "PROXY", want: "kube-proxy"},                                     // case-insensitive substring
		{in: "csi-driver", wantErr: "ambiguous"},                              // two substring matches
		{in: "totally bogus", wantErr: "invalid add-on name 'totally bogus'"}, // no match
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := resolveAddonName(t.Context(), svc, "my-cluster", tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("got %q, %v; want error containing %q", got, err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestResolveAddonName_AmbiguousListsCandidates(t *testing.T) {
	svc, _ := pagedAddons([]string{"aws-ebs-csi-driver", "aws-efs-csi-driver", "coredns"})
	_, err := resolveAddonName(t.Context(), svc, "my-cluster", "csi")
	if err == nil || !strings.Contains(err.Error(), "aws-ebs-csi-driver") || !strings.Contains(err.Error(), "aws-efs-csi-driver") ||
		strings.Contains(err.Error(), "coredns") {
		t.Errorf("err = %v, want it to list exactly the two csi candidates", err)
	}
}

// Add-ons a parallel --all run never started (deadline or Ctrl+C) count as
// failures, so the command exits non-zero.
func TestUpdateAllFailureError_CountsNotAttempted(t *testing.T) {
	results := []addons.AddonUpdateResult{
		{AddonName: "vpc-cni", Status: "FAILED: context deadline exceeded"},
		{AddonName: "coredns", Status: "FAILED: not attempted: context deadline exceeded"},
		{AddonName: "kube-proxy", Status: "FAILED: not attempted: context deadline exceeded"},
	}
	err := updateAllFailureError(results)
	if err == nil || !strings.Contains(err.Error(), "3 of 3") {
		t.Fatalf("err = %v, want 3 of 3 failed", err)
	}
}
