package runner

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/mocks"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/ui"
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

// A check that cannot read the cluster warns and lets the run go on: EKS
// still refuses a second update itself.
func TestClusterChangesReadFailureWarns(t *testing.T) {
	var buf bytes.Buffer
	orig := ui.Stderr
	ui.Stderr = &buf
	t.Cleanup(func() { ui.Stderr = orig })

	api := mocks.NewEKSAPI().WithCluster("prod", "1.32").Build()
	api.ListAddonsFn = func(context.Context, *eks.ListAddonsInput, ...func(*eks.Options)) (*eks.ListAddonsOutput, error) {
		return nil, mocks.AccessDenied()
	}
	if got := ClusterChanges(t.Context(), api, "prod", nil); len(got) != 0 {
		t.Fatalf("changes = %v, want none", got)
	}
	if !strings.Contains(buf.String(), "Could not check what EKS is changing on prod") {
		t.Errorf("stderr = %q, want the warning", buf.String())
	}
}

// exitCode is err's exit code: an unwrapped cli.ExitCoder's, else 1.
func exitCode(err error) int {
	if ec, ok := err.(cli.ExitCoder); ok { //nolint:errorlint // mirrors cli.HandleExitCoder's unwrapped check
		return ec.ExitCode()
	}
	return 1
}
