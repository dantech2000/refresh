package nodegroup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/aws/awserr"
	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/render"
	"github.com/dantech2000/refresh/internal/ui"
)

// drainBlockers maps a nodegroup to what would stop EKS draining its nodes
// (health.DrainBlockerReport.Names). Nodegroups with none are left out.
type drainBlockers map[string][]string

// drainBlockersFn reads the drain blockers of each nodegroup. Without
// Kubernetes access it returns health.ErrNoKubeClient. A seam for tests.
var drainBlockersFn = readDrainBlockers

func readDrainBlockers(ctx context.Context, awsCfg aws.Config, eksClient *eks.Client, cluster string, nodegroups []string, flags updateAMIFlags, verbose bool) (drainBlockers, error) {
	kube, _ := resolveHealthKubeClient(ctx, eksClient, awsCfg.Region, cluster, flags.kubeconfig, flags.kubeContext, verbose)
	if kube == nil {
		return nil, health.ErrNoKubeClient
	}
	checker := health.NewCheckerForConfig(awsCfg, kube, nil)
	out := drainBlockers{}
	for _, ng := range nodegroups {
		report, err := checker.DrainBlockers(ctx, cluster, []string{ng})
		if err != nil {
			return nil, err
		}
		if names := report.Names(); len(names) > 0 {
			out[ng] = names
		}
	}
	return out, nil
}

// drainGate is the PDB drain-blocker gate of `nodegroup update`, run on the
// nodegroups the run would roll, before any of them starts. EKS stops a
// roll whose drain a PodDisruptionBudget blocks only after it has replaced
// nodes, so the gate refuses first, as `cluster upgrade` does.
//
// It returns the blockers, and checked=false when the check did not run:
// --skip-health-check, nothing to roll, or no Kubernetes access (the kube
// notice says so). PDBs that could not be read are a blocker on every
// nodegroup to roll: the roll is refused unless --force, as a PDB that
// allows no disruption is. With --force it warns on stderr about the
// blockers it rolls past. verbose prints the kube notice, for a dry run
// that has not printed it.
func drainGate(ctx context.Context, awsCfg aws.Config, eksClient *eks.Client, cluster string, toRoll []string, flags updateAMIFlags, verbose bool) (blockers drainBlockers, checked bool) {
	if flags.skipHealthCheck || len(toRoll) == 0 {
		return nil, false
	}
	blockers, err := drainBlockersFn(ctx, awsCfg, eksClient, cluster, toRoll, flags, verbose)
	switch {
	case errors.Is(err, health.ErrNoKubeClient):
		return nil, false
	case err != nil && ctx.Err() != nil:
		return nil, false
	case err != nil:
		// Unchecked is not clear: refuse, as for a blocker, unless --force.
		blockers = drainBlockers{}
		for _, ng := range toRoll {
			blockers[ng] = []string{"the PodDisruptionBudgets could not be read (" + awserr.Summary(err) + ")"}
		}
	}
	if len(blockers) > 0 && flags.force {
		verb := "rolling"
		if flags.dryRun {
			verb = "would roll"
		}
		render.Notef(ui.Stderr, render.Warn, "--force: %s %s despite these drain blockers: %s", verb, cluster, blockers.text(toRoll))
	}
	return blockers, true
}

// text names the blockers, nodegroup by nodegroup in order:
// "ng-a: PDB default/web allows 0 disruptions; ng-b: ...".
func (b drainBlockers) text(order []string) string {
	var parts []string
	for _, ng := range order {
		for _, name := range b[ng] {
			parts = append(parts, ng+": "+name)
		}
	}
	return strings.Join(parts, "; ")
}

// drainBlockedExit is the exit 3 error of a run the drain gate refused.
func drainBlockedExit(cluster string, order []string, blockers drainBlockers) error {
	return cli.Exit(fmt.Sprintf("the PDB drain gate stopped the nodegroups of %s (%s); nothing was started. Let the workloads recover, relax the PDBs (or narrow their selectors so each pod matches one PDB), fix the access to them, or pass --force to evict anyway",
		cluster, blockers.text(order)), runner.ExitBlocked)
}

// printDrainGate shows a dry run what the drain gate would decide.
func printDrainGate(w io.Writer, order []string, blockers drainBlockers, force bool) {
	th := render.Default(w)
	if len(blockers) == 0 {
		_, _ = fmt.Fprintln(w, "\n"+th.Line(render.Healthy, "PDB drain gate: no PodDisruptionBudget blocks draining the nodegroups to roll."))
		return
	}
	verdict, st := "would be REFUSED", render.Fail
	if force {
		verdict, st = "would be overridden by --force", render.Warn
	}
	_, _ = fmt.Fprintln(w, "\n"+th.Line(st, "PDB drain gate: %s. These would stop EKS draining the nodes:", verdict))
	for _, ng := range order {
		for _, name := range blockers[ng] {
			_, _ = fmt.Fprintf(w, "  - %s: %s\n", ng, name)
		}
	}
	if !force {
		_, _ = fmt.Fprintln(w, "Let the workloads recover, relax the PDBs, or pass --force to evict anyway.")
	}
}
