package cluster

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/legacy/internal/aws/awserr"
	"github.com/dantech2000/refresh/legacy/internal/commands/factory"
	"github.com/dantech2000/refresh/legacy/internal/commands/runner"
	"github.com/dantech2000/refresh/legacy/internal/diag"
	"github.com/dantech2000/refresh/legacy/internal/health"
	"github.com/dantech2000/refresh/legacy/internal/render"
	"github.com/dantech2000/refresh/legacy/internal/ui"
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
// the roll when a PodDisruptionBudget allows 0 disruptions, a pod is
// covered by more than one PDB on the nodegroup, or the PDBs could not be
// read (each unless --force), or the pre-flight health decision is BLOCK. Warnings
// follow the phase-confirmation rules: --yes proceeds, otherwise the user is
// asked. Without Kubernetes access the PDB check is skipped with a warning,
// as in `nodegroup update`.
type nodegroupHealthGate struct {
	cluster string
	region  string
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

	// failures are the reads the gate could not make: PodDisruptionBudgets
	// that --force rolled past, and what the health checks could not read.
	// A roll can go on without them (--force, --yes), so the command lists
	// them in its document and exits 4 after an otherwise successful run.
	failures diag.List
}

// gateConnect builds the gate's checker and reports whether it has a
// Kubernetes client. A seam for tests.
var gateConnect = func(ctx context.Context, cmd *cli.Command, awsCfg aws.Config, clusterName string) (healthGateChecker, bool) {
	kube, sel := runner.ResolveClusterKubeClient(ctx, runner.KubeRequest{
		API:         factory.NewEKSClient(awsCfg),
		Cluster:     clusterName,
		Region:      awsCfg.Region,
		Kubeconfig:  cmd.String("kubeconfig"),
		KubeContext: cmd.String("kube-context"),
		Verbose:     !cmd.Bool("quiet"),
	})
	var metrics health.NodeMetricsLister
	if kube != nil {
		if m, err := health.BuildMetricsClient(sel); err == nil {
			metrics = m
		}
	}
	return health.NewCheckerForConfig(awsCfg, kube, metrics), kube != nil
}

// addFailures records reads the gate could not make, each once, with the
// region set.
func (g *nodegroupHealthGate) addFailures(fs ...diag.Failure) {
	for _, f := range fs {
		if f.Region == "" {
			f.Region = g.region
		}
		if !slices.Contains(g.failures, f) {
			g.failures = append(g.failures, f)
		}
	}
}

// newNodegroupHealthGate builds the gate from the command's flags. Nothing
// is resolved until the first nodegroup is about to roll, so a dry run or a
// run without nodegroup work never touches the Kubernetes API.
func newNodegroupHealthGate(cmd *cli.Command, awsCfg aws.Config, clusterName string) *nodegroupHealthGate {
	quiet := cmd.Bool("quiet")
	return &nodegroupHealthGate{
		cluster: clusterName,
		region:  awsCfg.Region,
		skip:    cmd.Bool("skip-health-check"),
		yes:     cmd.Bool("yes"),
		force:   cmd.Bool("force"),
		quiet:   quiet,
		warn:    ui.Stderr,
		connect: func(ctx context.Context) (healthGateChecker, bool) {
			return gateConnect(ctx, cmd, awsCfg, clusterName)
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
		blockers := report.Names()
		switch {
		case err != nil && ctx.Err() != nil:
			return fmt.Errorf("checking PodDisruptionBudgets for nodegroup %s: %w", nodegroup, err)
		case err != nil:
			// Unchecked is not clear: refuse, as for a blocker, unless
			// --force. `nodegroup update` does the same (drainGate).
			blockers = []string{"the PodDisruptionBudgets could not be read (" + awserr.Summary(err) + ")"}
			// --force can roll past it, but the read is still missing.
			g.addFailures(diag.FromError(diag.KindCluster, g.cluster, "", fmt.Errorf("listing PodDisruptionBudgets before the roll of nodegroup %s: %w", nodegroup, err)))
		}
		if len(blockers) > 0 {
			if !g.force {
				return fmt.Errorf("%d drain blocker(s) would stop draining nodegroup %s: %s; let the workloads recover, relax the PDBs, or narrow PDB selectors so each pod matches one PDB, fix the access to the PDBs, or pass --force to evict anyway",
					len(blockers), nodegroup, strings.Join(blockers, "; "))
			}
			g.warnf("Warning: --force: rolling nodegroup %s despite these drain blockers: %s\n", nodegroup, strings.Join(blockers, "; "))
		}
		// Scope the PDB part of the full health check to this nodegroup.
		g.checker.SetTargetNodegroups([]string{nodegroup})
	}

	summary := g.checker.RunAllChecks(ctx, g.cluster)
	g.addFailures(summary.Failures...)
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
