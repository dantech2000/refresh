package change

import (
	"context"
	"sync"

	"github.com/dantech2000/refresh/internal/health"
)

// submitTargets returns the names of the steps that would send a request.
func submitTargets(steps []Step) []string {
	var out []string
	for _, s := range steps {
		if s.Action == Submit {
			out = append(out, s.Ref.Name)
		}
	}
	return out
}

// gateRoll runs the health gate of a roll: health.Preflight, once, with
// every nodegroup that the plan would roll as a target. The whole batch
// is judged before any roll starts, so a blocker of the second nodegroup
// stops the first one too. A plan that submits nothing runs no gate.
//
// The checks read the Kubernetes API when the run has access to the
// cluster. Without access the gate has a warning about it, and the checks
// that need Kubernetes are skipped: the policy decides, as for every
// warning.
//
// Beside the gate it reads the instance-type offerings of the targets,
// also with --skip-health-check. What that finds is a notice, and never
// part of the verdict.
func (cp *ClusterPlan) gateRoll(ctx context.Context, d Deps, policy health.Policy) {
	cp.gateNodegroups(ctx, d, policy, submitTargets(cp.Steps()))
}

// gateNodegroups is the gate of gateRoll over the nodegroups in targets.
// The plan of an upgrade or a rollback calls it with every nodegroup that
// the plan would roll, in any hop.
func (cp *ClusterPlan) gateNodegroups(ctx context.Context, d Deps, policy health.Policy, targets []string) {
	if len(targets) == 0 {
		return
	}
	clients := d.AWS.Clients(cp.Region)
	// The offerings are read while the checks run, and joined before the
	// plan is returned.
	var (
		wg      sync.WaitGroup
		offered []string
	)
	wg.Go(func() { offered = availabilityNotices(ctx, clients, cp.snapshot, targets) })
	defer func() {
		wg.Wait()
		cp.Notices = append(cp.Notices, offered...)
	}()
	if policy.Skip {
		cp.Gate = health.Gate(nil, policy)
		cp.Notices = append(cp.Notices, "the health checks were skipped (--skip-health-check)")
		return
	}
	in := &health.Input{Snapshot: cp.snapshot, Targets: targets, Clients: clients, Now: cp.snapshot.FetchedAt}
	in.Kube, in.Metrics, in.NoKube = kubeAccess(d, cp.snapshot)
	cp.Checks = health.Run(ctx, health.Preflight(), in)
	cp.Gate = health.Gate(cp.Checks, policy)
}
