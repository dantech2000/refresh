package runner

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/mocks"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
)

func TestRefuseIfBusy(t *testing.T) {
	api := mocks.NewEKSAPI().
		WithCluster("prod", "1.32").
		WithAddon("vpc-cni", "v1.18.0", ekstypes.AddonStatusUpdating).
		Build()
	err := RefuseIfBusy(t.Context(), api, "prod", nil)
	if code := exitCode(err); code != ExitBlocked || !strings.Contains(err.Error(), "prod is busy (add-on vpc-cni UPDATING); nothing was started") {
		t.Fatalf("err = %v (exit %d), want exit 3 naming the add-on", err, code)
	}
	ignoreAddon := func(c clustersvc.Change) bool { return c.Kind == clustersvc.ChangeAddon }
	if err := RefuseIfBusy(t.Context(), api, "prod", ignoreAddon); err != nil {
		t.Fatalf("ignored change refused: %v", err)
	}
}

// A check that cannot read the cluster refuses the run (exit 3): an add-on
// update EKS is running could go unseen.
func TestRefuseIfBusyReadFailureRefuses(t *testing.T) {
	api := mocks.NewEKSAPI().WithCluster("prod", "1.32").Build()
	api.ListAddonsFn = func(context.Context, *eks.ListAddonsInput, ...func(*eks.Options)) (*eks.ListAddonsOutput, error) {
		return nil, mocks.AccessDenied()
	}
	if _, err := ClusterChanges(t.Context(), api, "prod", nil); err == nil {
		t.Fatal("ClusterChanges read failure returned no error")
	}
	err := RefuseIfBusy(t.Context(), api, "prod", nil)
	if code := exitCode(err); code != ExitBlocked || !strings.Contains(err.Error(), "could not check what EKS is changing on prod; nothing was started") {
		t.Fatalf("err = %v (exit %d), want exit 3", err, code)
	}
}

// exitCode is err's exit code: an unwrapped cli.ExitCoder's, else 1.
func exitCode(err error) int {
	if ec, ok := err.(cli.ExitCoder); ok { //nolint:errorlint // mirrors cli.HandleExitCoder's unwrapped check
		return ec.ExitCode()
	}
	return 1
}
