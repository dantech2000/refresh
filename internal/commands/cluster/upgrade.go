package cluster

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/fatih/color"
	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/commands/factory"
	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/services/upgrade"
	"github.com/dantech2000/refresh/internal/ui"
)

// upgradeDefaultTimeout bounds a full orchestrated upgrade. Control-plane
// hops run ~10m each and nodegroup rolls ~10-20m per group, so multi-hop
// upgrades legitimately run for hours.
const upgradeDefaultTimeout = 4 * time.Hour

func upgradeCommand() *cli.Command {
	return &cli.Command{
		Name:      "upgrade",
		Usage:     "Orchestrate a cluster upgrade: control plane → addons → nodegroups, with gates",
		ArgsUsage: "[cluster]",
		Description: `Plan and execute a full EKS cluster upgrade to a target Kubernetes version.

EKS upgrades one minor version at a time, so a multi-minor upgrade expands
into sequential hops. Each hop runs: readiness (cluster insights + kubelet
version skew) → control plane → addons (dependency order, versions compatible
with the hop target) → nodegroup rolls, with a health gate after every phase.

The plan is re-derived from live cluster state on every run, so rerunning the
same command after a failure (or Ctrl+C) resumes where it left off, and
rerunning after success is a no-op.

Examples:
   # Print the plan only (exits non-zero if anything blocks the upgrade)
   refresh cluster upgrade -c prod-east --to 1.33 --dry-run

   # Execute, confirming each mutating phase
   refresh cluster upgrade -c prod-east --to 1.33

   # Non-interactive (CI) run
   refresh cluster upgrade -c prod-east --to 1.33 --yes`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "cluster", Aliases: []string{"c"}, Usage: "EKS cluster name or pattern"},
			&cli.StringFlag{Name: "to", Usage: "Target Kubernetes version (e.g. 1.33)", Required: true},
			&cli.BoolFlag{Name: "dry-run", Aliases: []string{"d"}, Usage: "Print the full ordered plan without mutating anything"},
			&cli.BoolFlag{Name: "yes", Aliases: []string{"y"}, Usage: "Skip per-phase confirmation prompts"},
			&cli.BoolFlag{Name: "force", Usage: "Force nodegroup rolls when pods can't be drained due to PDBs"},
			&cli.StringSliceFlag{Name: "skip", Aliases: []string{"s"}, Usage: "Addon to skip (repeatable; for addons managed via Helm/GitOps)"},
			&cli.StringSliceFlag{Name: "skip-nodegroup", Usage: "Nodegroup name pattern to skip (repeatable)"},
			&cli.BoolFlag{Name: "quiet", Aliases: []string{"q"}, Usage: "Suppress progress output"},
			&cli.DurationFlag{Name: "timeout", Aliases: []string{"t"}, Usage: "Overall operation timeout", Value: upgradeDefaultTimeout, Sources: cli.EnvVars("REFRESH_TIMEOUT")},
			&cli.DurationFlag{Name: "poll-interval", Aliases: []string{"p"}, Usage: "How often to poll in-flight updates", Value: 15 * time.Second},
			&cli.StringFlag{Name: "format", Aliases: []string{"o"}, Usage: "Plan output format (table, json, yaml, plain)", Value: "table"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error { return runUpgrade(ctx, cmd) },
	}
}

func runUpgrade(ctx context.Context, cmd *cli.Command) error {
	// Strict credential validation: this command mutates the control plane.
	ctx, cancel, awsCfg, err := runner.SetupAWSStrict(ctx, cmd)
	if err != nil {
		return err
	}
	defer cancel()

	clusterName, listed, err := runner.ResolveClusterOrList(ctx, awsCfg, cmd)
	if err != nil || listed {
		return err
	}

	svc := upgrade.NewService(eks.NewFromConfig(awsCfg), factory.NewDefaultLogger(nil))
	if pi := cmd.Duration("poll-interval"); pi > 0 {
		svc.PollInterval = pi
	}

	planOpts := upgrade.PlanOptions{
		SkipAddons:     cmd.StringSlice("skip"),
		SkipNodegroups: cmd.StringSlice("skip-nodegroup"),
	}

	var plan *upgrade.Plan
	err = runner.WithSpinner("cluster", "Upgrade plan computed!", func() error {
		var perr error
		plan, perr = svc.BuildPlan(ctx, clusterName, cmd.String("to"), planOpts)
		return perr
	})
	if err != nil {
		return err
	}

	format := cmd.String("format")
	if handled, eerr := runner.EncodeStdout(format, plan); handled || eerr != nil {
		if eerr != nil {
			return eerr
		}
		if plan.Blocked() {
			return cli.Exit("", 1)
		}
		if cmd.Bool("dry-run") {
			return nil
		}
	} else {
		renderPlan(plan)
	}

	// A plan with blockers prints and exits non-zero without mutating.
	if plan.Blocked() {
		return cli.Exit(color.RedString("Upgrade blocked — resolve the blockers above and re-run."), 1)
	}
	if cmd.Bool("dry-run") {
		return nil
	}
	if plan.PendingSteps() == 0 {
		ui.Outln()
		ui.Outf("Nothing to do: %s already satisfies %s.\n", clusterName, plan.TargetVersion)
		return nil
	}

	progress := func(format string, args ...any) {
		if !cmd.Bool("quiet") {
			ui.Outf("  "+format+"\n", args...)
		}
	}

	report, err := svc.Execute(ctx, plan, upgrade.ExecuteOptions{
		Yes:            cmd.Bool("yes"),
		Confirm:        promptPhase,
		Progress:       progress,
		SkipAddons:     cmd.StringSlice("skip"),
		SkipNodegroups: cmd.StringSlice("skip-nodegroup"),
		Force:          cmd.Bool("force"),
	})

	renderReport(report)
	if err != nil {
		resume := fmt.Sprintf("refresh cluster upgrade -c %s --to %s", clusterName, plan.TargetVersion)
		ui.Outln()
		ui.Outf("Resume with: %s\n", color.CyanString(resume))
		return err
	}

	ui.Outln()
	ui.Outf("%s\n", color.GreenString("Upgrade complete: %s is at %s.", clusterName, plan.TargetVersion))
	return nil
}

// promptPhase asks for confirmation before a mutating phase. Bare Enter or a
// read error declines (safe default).
func promptPhase(label string) bool {
	fmt.Printf("\nProceed with %s? (y/N): ", label)
	reader := bufio.NewReader(os.Stdin)
	answer, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes"
}

// renderPlan prints the human-readable plan.
func renderPlan(plan *upgrade.Plan) {
	ui.Outln()
	path := plan.CurrentVersion
	for _, hop := range plan.Hops {
		path += " → " + hop.To
	}
	ui.Outf("Upgrade plan: %s %s (EKS upgrades are sequential minors)\n", color.New(color.Bold).Sprint(plan.ClusterName), path)

	for _, w := range plan.Warnings {
		ui.Outf("  %s %s\n", color.YellowString("▸ warning:"), w)
	}

	for _, hop := range plan.Hops {
		ui.Outln()
		ui.Outf("%s\n", color.New(color.Bold).Sprintf("Hop %s → %s", hop.From, hop.To))
		for i, step := range hop.Steps {
			marker, note := stepMarkerAndNote(step)
			line := fmt.Sprintf("  %2d. %s %s", i+1, marker, step.Description)
			if note != "" {
				line += " — " + note
			}
			ui.Outf("%s\n", line)
		}
	}
}

// stepMarkerAndNote maps a step's status to its display marker and note.
func stepMarkerAndNote(step upgrade.Step) (string, string) {
	switch step.Status {
	case upgrade.StatusCompleted:
		return color.GreenString("[done]   "), step.Reason
	case upgrade.StatusBlocked:
		return color.RedString("[BLOCKED]"), step.Reason
	case upgrade.StatusManual:
		return color.YellowString("[manual] "), step.Reason
	default:
		return color.CyanString("[pending]"), step.Reason
	}
}

// renderReport prints the completed / failed-at / remaining summary.
func renderReport(report *upgrade.Report) {
	if report == nil {
		return
	}
	ui.Outln()
	for _, c := range report.Completed {
		ui.Outf("%s %s\n", color.GreenString("completed:"), c)
	}
	if report.FailedAt != "" {
		ui.Outf("%s %s\n", color.RedString("failed at:"), report.FailedAt)
	}
	for _, r := range report.Remaining {
		ui.Outf("%s %s\n", color.YellowString("remaining:"), r)
	}
}
