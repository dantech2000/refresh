package nodegroup

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/fatih/color"
	"github.com/urfave/cli/v3"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/commands/factory"
	"github.com/dantech2000/refresh/internal/commands/runner"
	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
)

func runList(ctx context.Context, cmd *cli.Command) error {
	if err := runner.ValidateFormat(cmd.String("format"), runner.FormatsStandard); err != nil {
		return err
	}
	// Each --watch iteration performs the full setup+fetch+render cycle so a
	// fresh service (and cache) is used every time.
	return runner.Watch(ctx, cmd, func() error { return listNodegroupsOnce(ctx, cmd) })
}

func listNodegroupsOnce(ctx context.Context, cmd *cli.Command) error {
	ctx, cancel, awsCfg, err := runner.SetupAWS(ctx, cmd)
	if err != nil {
		return err
	}
	defer cancel()

	clusterName, listed, err := runner.ResolveClusterOrList(ctx, awsCfg, cmd)
	if err != nil || listed {
		return err
	}

	logger := factory.NewDefaultLogger(nil)
	// --check-readiness measures real Kubernetes Ready node counts (per
	// nodegroup) instead of leaving the NODES column at desired-only. When the
	// cluster API is unreachable, resolveHealthKubeClient returns nil with a
	// diagnostic and readiness stays honestly unknown. (REF-130)
	var svc *nodegroupsvc.ServiceImpl
	if cmd.Bool("check-readiness") {
		humanOutput := strings.EqualFold(cmd.String("format"), "table")
		k8sClient, _ := resolveHealthKubeClient(ctx, eks.NewFromConfig(awsCfg), awsCfg.Region, clusterName, cmd.String("kubeconfig"), cmd.String("kube-context"), humanOutput)
		svc = factory.NewNodegroupServiceWithHealth(awsCfg, k8sClient, logger)
	} else {
		svc = factory.NewNodegroupService(awsCfg, false, logger)
	}

	filters := runner.ParseFilters(cmd.StringSlice("filter"))
	opts := nodegroupsvc.ListOptions{
		Filters: filters,
	}

	var res nodegroupsvc.ListResult
	start := time.Now()
	if err := runner.WithSpinner("nodegroup", "Nodegroup information gathered!", func() error {
		var lerr error
		res, lerr = svc.ListDetailed(ctx, clusterName, opts)
		return lerr
	}); err != nil {
		return err
	}

	// Sort before encoding so --sort/--desc apply to every output format, not
	// just table/plain — matching cluster list and keeping JSON/YAML scriptable. (REF-49)
	items := sortNodegroupSummaries(res.Summaries, cmd.String("sort"), cmd.Bool("desc"))

	if err := writeNodegroupList(cmd.String("format"), clusterName, items, res.Failures, time.Since(start)); err != nil {
		return err
	}
	return reportListProblems(warnOut, clusterName, res)
}

// warnOut receives list/describe warnings; a variable so tests can capture it.
var warnOut io.Writer = os.Stderr

// writeNodegroupList prints what was gathered in the requested format. When
// some nodegroups could not be described, JSON/YAML carry them under
// "failures" so "count" is never mistaken for the full nodegroup count.
func writeNodegroupList(format, clusterName string, items []nodegroupsvc.NodegroupSummary, failures []string, elapsed time.Duration) error {
	payload := map[string]any{"cluster": clusterName, "nodegroups": items, "count": len(items)}
	if len(failures) > 0 {
		payload["failures"] = failures
	}
	if handled, err := runner.EncodeStdout(format, payload); handled {
		return err
	}
	return outputNodegroupsTable(clusterName, items, elapsed)
}

// reportListProblems warns on w about incomplete list data. A failed
// latest-AMI lookup gets one warning (the rows already say "unknown (lookup
// failed)"). Nodegroups that could not be described are named one per line,
// and the returned error makes the command exit non-zero.
func reportListProblems(w io.Writer, clusterName string, res nodegroupsvc.ListResult) error {
	if res.AMILookupErr != nil {
		warnAMILookup(w, len(res.AMILookupFailures), res.AMILookupErr)
	}
	if len(res.Failures) == 0 {
		return nil
	}
	for _, f := range res.Failures {
		_, _ = fmt.Fprintln(w, color.YellowString("warning: nodegroup %s", f))
	}
	return fmt.Errorf("listing nodegroups for cluster %s: %d nodegroup(s) could not be described; the list is incomplete", clusterName, len(res.Failures))
}

// warnAMILookup prints a single warning for failed latest-AMI lookups,
// formatted so a missing permission names ssm:GetParameter.
func warnAMILookup(w io.Writer, n int, err error) {
	_, _ = fmt.Fprintln(w, color.YellowString("warning: could not look up the latest recommended AMI for %d nodegroup(s); their AMI status shows %q",
		n, amiLookupFailedText))
	_, _ = fmt.Fprintln(w, color.YellowString("%v", awsinternal.FormatAWSError(err, "reading the latest recommended EKS AMI from SSM")))
}
