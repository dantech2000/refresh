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

func TestCheckBusy(t *testing.T) {
	api := mocks.NewEKSAPI().
		WithCluster("prod", "1.32").
		WithAddon("vpc-cni", "v1.18.0", ekstypes.AddonStatusUpdating).
		Build()
	busy := CheckBusy(t.Context(), api, "prod", "us-east-1", nil)
	if busy == nil {
		t.Fatal("a busy cluster was not refused")
	}
	if code := exitCode(busy.Exit); code != ExitBlocked || !strings.Contains(busy.Exit.Error(), "prod is busy (add-on vpc-cni UPDATING); nothing was started") {
		t.Fatalf("err = %v (exit %d), want exit 3 naming the add-on", busy.Exit, code)
	}
	if len(busy.Changes) != 1 || busy.Changes[0] != "add-on vpc-cni UPDATING" || busy.Failure != nil {
		t.Errorf("busy = %+v, want the one change and no failure", busy)
	}
	ignoreAddon := func(c clustersvc.Change) bool { return c.Kind == clustersvc.ChangeAddon }
	if busy := CheckBusy(t.Context(), api, "prod", "us-east-1", ignoreAddon); busy != nil {
		t.Fatalf("ignored change refused: %+v", busy)
	}
}

// A check that cannot read the cluster refuses the run (exit 3): an add-on
// update EKS is running could go unseen.
func TestCheckBusyReadFailureRefuses(t *testing.T) {
	api := mocks.NewEKSAPI().WithCluster("prod", "1.32").Build()
	api.ListAddonsFn = func(context.Context, *eks.ListAddonsInput, ...func(*eks.Options)) (*eks.ListAddonsOutput, error) {
		return nil, mocks.AccessDenied()
	}
	if _, err := ClusterChanges(t.Context(), api, "prod", nil); err == nil {
		t.Fatal("ClusterChanges read failure returned no error")
	}
	busy := CheckBusy(t.Context(), api, "prod", "us-east-1", nil)
	if busy == nil || busy.Failure == nil || busy.Failure.Region != "us-east-1" {
		t.Fatalf("busy = %+v, want the failed read for the document", busy)
	}
	if code := exitCode(busy.Exit); code != ExitBlocked || !strings.Contains(busy.Exit.Error(), "could not check what EKS is changing on prod; nothing was started") {
		t.Fatalf("err = %v (exit %d), want exit 3", busy.Exit, code)
	}
}

// exitCode is err's exit code: an unwrapped cli.ExitCoder's, else 1.
func exitCode(err error) int {
	if ec, ok := err.(cli.ExitCoder); ok { //nolint:errorlint // mirrors cli.HandleExitCoder's unwrapped check
		return ec.ExitCode()
	}
	return 1
}
