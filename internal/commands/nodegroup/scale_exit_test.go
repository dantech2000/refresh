package nodegroup

import (
	"errors"
	"fmt"
	"testing"

	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/health"
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

// A --check-pdbs dry run exits as the real run would at the gate: 3 when it
// would refuse the scale-down, 1 when the PDBs could not be read, 0 when it
// would pass.
func TestScaleDryRunGateErr(t *testing.T) {
	refused := &nodegroupsvc.ScaleDownPDBCheck{
		CurrentDesired: 3, RequestedDesired: 1, ScaleDown: true, Scoped: true,
		Blockers: []health.PDBInfo{{Name: "web", Namespace: "default"}},
	}
	for _, tc := range []struct {
		name     string
		check    *nodegroupsvc.ScaleDownPDBCheck
		checkErr error
		want     int
	}{
		{"passes", &nodegroupsvc.ScaleDownPDBCheck{CurrentDesired: 3, RequestedDesired: 1, ScaleDown: true}, nil, runner.ExitOK},
		{"not a scale-down", &nodegroupsvc.ScaleDownPDBCheck{}, nil, runner.ExitOK},
		{"refused", refused, nil, runner.ExitBlocked},
		{"PDBs unreadable", nil, errors.New("PDB validation for prod/web: no kubeconfig"), runner.ExitError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := runner.ExitCodeOf(scaleExit(scaleDryRunGateErr("prod", "web", tc.check, tc.checkErr))); got != tc.want {
				t.Errorf("exit code = %d, want %d", got, tc.want)
			}
		})
	}
}
