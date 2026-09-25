package cluster

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/commands/factory"
	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/render"
	"github.com/dantech2000/refresh/internal/ui"
)

// healthGateChecker is the part of health.HealthChecker the pre-roll gate
// uses.
type healthGateChecker interface {
	SetTargetNodegroups(names []string)
	RunAllChecks(ctx context.Context, clusterName string) health.HealthSummary
	DrainBlockers(ctx context.Context, clusterName string, nodegroups []string) (health.DrainBlockerReport, error)
}

// nodegroupHealthGate is the pre-roll gate `cluster upgrade` runs before each
// nodegroup roll, after the orchestrator's built-in ACTIVE check. It fails
// the roll when a PodDisruptionBudget allows 0 disruptions or a pod is
// covered by more than one PDB on the nodegroup (unless --force) or the pre-flight health decision is BLOCK. Warnings
// follow the phase-confirmation rules: --yes proceeds, otherwise the user is
// asked. Without Kubernetes access the PDB check is skipped with a warning,
// as in `nodegroup update`.
type nodegroupHealthGate struct {
	cluster string
	skip    bool // --skip-health-check
	yes     bool
	force   bool
	quiet   bool

	// connect builds the checker once, on the first roll. hasKube reports
	// whether it has a Kubernetes client (needed for the PDB check).
	connect func(ctx context.Context) (checker healthGateChecker, hasKube bool)
	// confirm asks before rolling past health warnings; nil means no
	// prompt is possible, so warnings without --yes fail the roll.
	confirm func(label string) bool
	// progress receives status lines; nil prints nothing.
	progress func(format string, args ...any)
	// warn receives warnings (stderr).
	warn io.Writer

	once    sync.Once
	checker healthGateChecker
	hasKube bool
}

// newNodegroupHealthGate builds the gate from the command's flags. Nothing
// is resolved until the first nodegroup is about to roll, so a dry run or a
// run without nodegroup work never touches the Kubernetes API.
func newNodegroupHealthGate(cmd *cli.Command, awsCfg aws.Config, clusterName string) *nodegroupHealthGate {
	quiet := cmd.Bool("quiet")
	return &nodegroupHealthGate{
		cluster: clusterName,
		skip:    cmd.Bool("skip-health-check"),
		yes:     cmd.Bool("yes"),
		force:   cmd.Bool("force"),
		quiet:   quiet,
		warn:    ui.Stderr,
		connect: func(ctx context.Context) (healthGateChecker, bool) {
			kube, sel := runner.ResolveClusterKubeClient(ctx, runner.KubeRequest{
				API:         factory.NewEKSClient(awsCfg),
				Cluster:     clusterName,
				Region:      awsCfg.Region,
				Kubeconfig:  cmd.String("kubeconfig"),
				KubeContext: cmd.String("kube-context"),
				Verbose:     !quiet,
			})
			var metrics health.NodeMetricsLister
			if kube != nil {
				if m, err := health.BuildMetricsClient(sel); err == nil {
					metrics = m
				}
			}
			return health.NewCheckerForConfig(awsCfg, kube, metrics), kube != nil
		},
	}
}

// check is the upgrade.NodegroupGate.
func (g *nodegroupHealthGate) check(ctx context.Context, nodegroup string) error {
	g.once.Do(func() {
		if g.skip {
			g.warnf("Warning: --skip-health-check: nodegroups roll without PDB drain-blocker or pre-flight health checks.\n")
			return
		}
		g.checker, g.hasKube = g.connect(ctx)
		if !g.hasKube {
			g.warnf("Warning: no Kubernetes access to %s: PDB drain-blocker checks before nodegroup rolls are skipped.\n", g.cluster)
		}
	})
	if g.skip {
		return nil
	}

	g.progressf("pre-flight health checks for nodegroup %s", nodegroup)
	if g.hasKube {
		report, err := g.checker.DrainBlockers(ctx, g.cluster, []string{nodegroup})
		if err != nil {
			return fmt.Errorf("checking PodDisruptionBudgets for nodegroup %s: %w (pass --skip-health-check to roll without this check)", nodegroup, err)
		}
		if blockers := drainBlockerNames(report); len(blockers) > 0 {
			if !g.force {
				return fmt.Errorf("%d drain blocker(s) would stop draining nodegroup %s: %s; let the workloads recover, relax the PDBs, or narrow PDB selectors so each pod matches one PDB, or pass --force to evict anyway",
					len(blockers), nodegroup, strings.Join(blockers, "; "))
			}
			g.warnf("Warning: --force: rolling nodegroup %s despite these drain blockers: %s\n", nodegroup, strings.Join(blockers, "; "))
		}
		// Scope the PDB part of the full health check to this nodegroup.
		g.checker.SetTargetNodegroups([]string{nodegroup})
	}

	summary := g.checker.RunAllChecks(ctx, g.cluster)
	switch summary.Decision {
	case health.DecisionBlock:
		return fmt.Errorf("pre-flight health checks failed before rolling nodegroup %s: %s (pass --skip-health-check to roll anyway, not recommended)",
			nodegroup, healthGateProblems(summary))
	case health.DecisionWarn:
		problems := healthGateProblems(summary)
		if g.yes {
			if !g.quiet {
				g.warnf("Health checks reported warnings before rolling nodegroup %s; proceeding (--yes): %s\n", nodegroup, problems)
			}
			return nil
		}
		if g.confirm == nil {
			return fmt.Errorf("health checks reported warnings before rolling nodegroup %s (%s); re-run with --yes to proceed", nodegroup, problems)
		}
		if !g.confirm(fmt.Sprintf("the roll of nodegroup %s despite health warnings (%s)", nodegroup, problems)) {
			return fmt.Errorf("roll of nodegroup %s declined after health warnings: %s", nodegroup, problems)
		}
	}
	return nil
}

// drainBlockerNames lists what in report would refuse an eviction: each PDB
// that allows 0 disruptions, and each pod that more than one PDB selects (the
// eviction API refuses it), as in the pre-flight PDB check.
func drainBlockerNames(report health.DrainBlockerReport) []string {
	names := make([]string, 0, len(report.Blockers)+len(report.MultiPDBPods))
	for _, b := range report.Blockers {
		names = append(names, "PDB "+b.Namespace+"/"+b.Name+" allows 0 disruptions")
	}
	for _, p := range report.MultiPDBPods {
		names = append(names, fmt.Sprintf("pod %s/%s is covered by %d PDBs (%s)", p.Namespace, p.Name, len(p.PDBs), strings.Join(p.PDBs, ", ")))
	}
	return names
}

func (g *nodegroupHealthGate) warnf(format string, args ...any) {
	if g.warn == nil {
		return
	}
	th := render.Default(g.warn)
	_, _ = fmt.Fprint(g.warn, th.Paint(th.Pal.Yellow, fmt.Sprintf(format, args...)))
}

func (g *nodegroupHealthGate) progressf(format string, args ...any) {
	if g.progress != nil {
		g.progress(format, args...)
	}
}

// healthGateProblems names the checks behind a verdict: the blocking
// failures for BLOCK, else the checks that warned or failed. Each is
// "Name: message", joined with "; ".
func healthGateProblems(summary health.HealthSummary) string {
	var out []string
	for _, r := range summary.Results {
		if r.Skipped {
			continue
		}
		failed := r.Status == health.StatusFail
		if summary.Decision == health.DecisionBlock && (!failed || !r.IsBlocking) {
			continue
		}
		if !failed && r.Status != health.StatusWarn {
			continue
		}
		out = append(out, r.Name+": "+r.Message)
	}
	if len(out) == 0 {
		return "no details reported"
	}
	return strings.Join(out, "; ")
}
