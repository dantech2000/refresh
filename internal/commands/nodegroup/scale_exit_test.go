package nodegroup

import (
	"errors"
	"fmt"
	"testing"

	"github.com/dantech2000/refresh/internal/commands/runner"
	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
)

// nodegroup scale maps its gates to the exit-code contract (REF-165): a
// refused or health-blocked scale exits 3 (nothing changed), a failed
// post-scaling health check exits 5, any other error exits 1.
func TestScaleExit(t *testing.T) {
	blocked := &nodegroupsvc.ScaleDownBlockedError{Cluster: "prod", Nodegroup: "web"}
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"ok", nil, runner.ExitOK},
		{"PDB gate refused", blocked, runner.ExitBlocked},
		{"PDB gate refused, wrapped", fmt.Errorf("scaling: %w", blocked), runner.ExitBlocked},
		{"pre-scaling health blocked", fmt.Errorf("%w: [capacity]", nodegroupsvc.ErrScaleHealthBlocked), runner.ExitBlocked},
		{"post-scaling health failed", fmt.Errorf("%w: [pods]", nodegroupsvc.ErrScaleVerifyFailed), runner.ExitVerifyFailed},
		{"other error", errors.New("AccessDeniedException"), runner.ExitError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := runner.ExitCodeOf(scaleExit(tc.err)); got != tc.want {
				t.Errorf("exit code = %d, want %d", got, tc.want)
			}
		})
	}
}
