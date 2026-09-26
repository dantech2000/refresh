package cluster

import (
	"context"
	"errors"
	"fmt"
	"strings"

	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/commands/clusterview"
	"github.com/dantech2000/refresh/internal/commands/factory"
	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/health"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/services/status"
	"github.com/dantech2000/refresh/internal/services/upgrade"
	"github.com/dantech2000/refresh/internal/ui"
)

func upgradeCheckCommand() *cli.Command {
	return &cli.Command{
		Name:      "upgrade-check",
		Usage:     "Report upgrade readiness: EKS Cluster Insights + version skew (read-only)",
		ArgsUsage: "[cluster]",
		Description: `Read-only upgrade-readiness report for an EKS cluster.

Surfaces AWS Cluster Insights (the same upgrade checks the console shows) plus a
local version-skew picture: control-plane version vs each managed nodegroup, and
installed addons vs the latest compatible version — with ordered, actionable
findings. Nothing is mutated; this is the pre-flight read before 'cluster upgrade'.

Insights are defined and computed by EKS (the same set the console shows), and
that catalog evolves — so list the live set for your cluster with --show-passing,
then drill into any with --id, which accepts the short ID shown in the table, the
full ID, or a case-insensitive name substring (e.g. --id "deprecated").

It works as a CI gate. The exit code follows the readiness verdict: the
insights in the report (--category and --status narrow them), the
nodegroup/addon skew, and the control-plane health check:
   0  ready: no finding
   2  needs attention: WARNING insights, a nodegroup behind the control
      plane but inside the kubelet skew limit, an addon behind latest, or a
      control-plane health warning
   3  blocked: an ERROR or UNKNOWN insight (as 'cluster upgrade' blocks on
      both), a nodegroup at the kubelet skew limit, or a failed
      control-plane health check
   4  incomplete: nothing blocks, but a nodegroup or addon could not be
      read (listed under "failures")
   1  error (AWS error, not found, interrupt)
Precedence: 3, then 4, then 2.
With --id, the exit code reflects that one insight's status. With -o json or
-o yaml, the document is printed first, then the exit code applies.
--exit-zero exits 0 on a completed check (report mode), also when incomplete.

Examples:
   refresh cluster upgrade-check -c prod-east
   refresh cluster upgrade-check -c prod-east --show-passing -o json
   refresh cluster upgrade-check -c prod-east --id "deprecated"   # detail view (by name)
   refresh cluster upgrade-check -c prod-east -o json --exit-zero  # report only`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "cluster", Aliases: []string{"c"}, Usage: "EKS cluster name or pattern"},
			&cli.StringFlag{Name: "category", Usage: "Insight category (UPGRADE_READINESS, MISCONFIGURATION)", Value: "UPGRADE_READINESS"},
			&cli.StringSliceFlag{Name: "status", Usage: "Filter by insight status (PASSING, WARNING, ERROR, UNKNOWN); PASSING needs no --show-passing"},
			&cli.BoolFlag{Name: "show-passing", Usage: "Include PASSING insights (hidden by default)"},
			&cli.StringFlag{Name: "id", Usage: "Show the detail view for one insight of --category — accepts its ID, a short ID prefix (as shown in the table), or a name substring"},
			&cli.StringFlag{Name: "format", Aliases: []string{"o"}, Usage: "Output format (table, json, yaml, plain)", Value: "table"},
			&cli.BoolFlag{Name: "exit-zero", Usage: "Exit 0 even when the check finds warnings (2), blockers (3), or unreadable items (4): report mode"},
		},
		Action: runUpgradeCheck,
	}
}

func runUpgradeCheck(ctx context.Context, cmd *cli.Command) error {
	if err := runner.ValidateFormat(cmd.String("format"), runner.FormatsStandard); err != nil {
		return err
	}
	ctx, cancel, awsCfg, err := runner.SetupAWS(ctx, cmd)
	if err != nil {
		return err
	}
	defer cancel()

	clusterName, listed, err := runner.ResolveClusterOrList(ctx, awsCfg, cmd)
	if err != nil || listed {
		return err
	}

	service := factory.NewClusterService(awsCfg, false, nil)

	// Detail view for a single insight. The --id value may be a full insight ID,
	// a short ID prefix (as shown in the insights table), or a case-insensitive
	// name substring — so the user never has to copy a raw UUID.
	if q := cmd.String("id"); q != "" {
		var detail *clustersvc.InsightDetail
		if werr := runner.WithSpinner("cluster", "Insight details loaded", func() error {
			id, rerr := service.ResolveInsightID(ctx, clusterName, cmd.String("category"), q)
			if rerr != nil {
				return rerr
			}
			detail, rerr = service.DescribeInsight(ctx, clusterName, id)
			return rerr
		}); werr != nil {
			return werr
		}
		if handled, encErr := runner.EncodeStdout(cmd.String("format"), detail); handled {
			if encErr != nil {
				return encErr
			}
			return gateExit(cmd, insightDetailExit(detail))
		}
		if err := clusterview.OutputInsightDetail(detail); err != nil {
			return err
		}
		return gateExit(cmd, insightDetailExit(detail))
	}

	opts := clustersvc.UpgradeCheckOptions{
		Category:    cmd.String("category"),
		Statuses:    cmd.StringSlice("status"),
		ShowPassing: cmd.Bool("show-passing"),
	}

	var report *clustersvc.UpgradeReport
	if werr := runner.WithSpinner("cluster", "Upgrade readiness computed", func() error {
		var rerr error
		report, rerr = service.UpgradeCheck(ctx, clusterName, opts)
		return rerr
	}); werr != nil {
		return werr
	}

	// Support posture for the control-plane version, via the same resolver
	// behind `refresh status` (REF-145).
	if report != nil && report.Skew.ControlPlaneVersion != "" {
		posture := status.NewSupportResolver(factory.NewEKSClient(awsCfg)).Resolve(ctx, report.Skew.ControlPlaneVersion)
		posture = status.ApplySupportType(posture, ekstypes.SupportType(report.SupportType))
		report.Support = &posture
	}

	// Rollback availability, best effort: the key is left out when the
	// update history can't be read (eks:ListUpdates).
	if report != nil && report.Skew.ControlPlaneVersion != "" {
		rb := upgrade.NewService(factory.NewEKSClient(awsCfg), factory.NewDefaultLogger(nil))
		if a, rerr := rb.RollbackAvailability(ctx, clusterName, report.Skew.ControlPlaneVersion); rerr == nil {
			report.Rollback = a
		}
	}

	// Control-plane health gate from the free AWS/EKS CloudWatch metrics — etcd
	// usage vs the 8 GiB read-only limit + API-server error rate (REF-140).
	if report != nil {
		checker := health.NewCheckerForConfig(awsCfg, nil, nil)
		cp := checker.CheckControlPlaneMetrics(ctx, clusterName)
		report.ControlPlane = &cp
	}

	format := cmd.String("format")
	if handled, encErr := runner.EncodeStdout(format, report); handled {
		if encErr != nil {
			return encErr
		}
	} else if err := clusterview.OutputUpgradeCheck(report, opts.Category); err != nil {
		return err
	}
	if !runner.TableListsFailures(format) {
		runner.ReportFailures(ui.Stderr, report.Failures)
	}
	return finishCheck(ctx, cmd, upgradeCheckExit(report))
}

// finishCheck returns the report's exit: exit 1 after an interrupt (the
// report may be cut short, and --exit-zero does not hide that), else the
// gate verdict.
func finishCheck(ctx context.Context, cmd *cli.Command, verdict error) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return errors.New("upgrade check interrupted; the report may be incomplete")
	}
	return gateExit(cmd, verdict)
}

// gateExit drops a gate verdict (exit 2, 3, or 4) when --exit-zero is set.
func gateExit(cmd *cli.Command, verdict error) error {
	if cmd.Bool("exit-zero") {
		return nil
	}
	return verdict
}

// upgradeCheckExit maps the report's readiness to the CI-gate exit code:
// 3 when something blocks the upgrade, 4 when some skew data could not be
// read (report.Failures), 2 for warnings only, else nil. See
// clustersvc.UpgradeReport.Readiness for what counts as which.
func upgradeCheckExit(report *clustersvc.UpgradeReport) error {
	level, reasons := report.Readiness()
	switch level {
	case clustersvc.ReadinessBlocked:
		return cli.Exit(fmt.Sprintf("upgrade blocked: %s (pass --exit-zero to report only)", strings.Join(reasons, ", ")), runner.ExitBlocked)
	case clustersvc.ReadinessIncomplete:
		return runner.IncompleteExit(report.Failures)
	case clustersvc.ReadinessReview:
		return cli.Exit(fmt.Sprintf("upgrade needs attention: %s (pass --exit-zero to report only)", strings.Join(reasons, ", ")), runner.ExitNeedsAttention)
	default:
		return nil
	}
}

// insightDetailExit maps one insight (the --id view) to the gate exit code:
// 3 for ERROR or UNKNOWN, 2 for WARNING, else nil.
func insightDetailExit(detail *clustersvc.InsightDetail) error {
	if detail == nil {
		return nil
	}
	switch detail.Status {
	case clustersvc.InsightStatusPassing:
		return nil
	case clustersvc.InsightStatusWarning:
		return cli.Exit(fmt.Sprintf("insight %q is WARNING (pass --exit-zero to report only)", detail.Name), runner.ExitNeedsAttention)
	default:
		return cli.Exit(fmt.Sprintf("insight %q is %s, which blocks the upgrade (pass --exit-zero to report only)", detail.Name, detail.Status), runner.ExitBlocked)
	}
}
