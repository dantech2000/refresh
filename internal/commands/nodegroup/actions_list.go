package nodegroup

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/fatih/color"
	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/commands/factory"
	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/diag"
	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
	"github.com/dantech2000/refresh/internal/ui"
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
		k8sClient, _ := resolveHealthKubeClient(ctx, factory.NewEKSClient(awsCfg), awsCfg.Region, clusterName, cmd.String("kubeconfig"), cmd.String("kube-context"), humanOutput)
		svc = factory.NewNodegroupServiceWithHealth(awsCfg, k8sClient, logger)
	} else {
		svc = factory.NewNodegroupService(awsCfg, false, logger)
	}

	filters := runner.ParseFilters(cmd.StringSlice("filter"))
	opts := nodegroupsvc.ListOptions{
		Filters: filters,
	}

	var res nodegroupsvc.ListResult
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

	format := cmd.String("format")
	failures := diag.List(slices.Clone(res.Failures))
	diag.Sort(failures)
	if err := writeNodegroupList(format, clusterName, items, failures); err != nil {
		return err
	}
	warnAMILookup(warnOut, items)
	if !runner.TableListsFailures(format) {
		runner.ReportFailures(warnOut, failures)
	}
	return runner.UnlessInterrupted(ctx, runner.IncompleteExit(failures))
}

// warnOut receives list/describe warnings; a variable so tests can capture it.
var warnOut io.Writer = ui.Stderr

// writeNodegroupList prints what was gathered in the requested format. The
// nodegroups that could not be described are in the document's "failures"
// (and the table's INCOMPLETE DATA section), so "count" is never mistaken
// for the full nodegroup count.
func writeNodegroupList(format, clusterName string, items []nodegroupsvc.NodegroupSummary, failures diag.List) error {
	doc := nodegroupsvc.NodegroupList{Cluster: clusterName, Nodegroups: apidoc.List(items), Count: len(items), Failures: failures}
	if handled, err := runner.EncodeStdout(format, doc); handled {
		return err
	}
	return outputNodegroupsTable(clusterName, items, failures)
}

// warnAMILookup writes one advisory line for the nodegroups whose latest
// recommended AMI could not be looked up. Their rows already say "unknown
// (lookup failed)" and carry amiLookupFailure; the lookup is advisory, so it
// does not change the exit code. The line names the reason and the IAM
// action (ssm:GetParameter) of the first failure.
func warnAMILookup(w io.Writer, items []nodegroupsvc.NodegroupSummary) {
	var first *diag.Failure
	n := 0
	for _, ng := range items {
		if ng.AMILookupFailure != nil {
			if first == nil {
				first = ng.AMILookupFailure
			}
			n++
		}
	}
	if first == nil {
		return
	}
	_, _ = fmt.Fprintln(w, ui.ColorFor(w, color.FgYellow).Sprintf("warning: could not look up the latest recommended AMI for %d nodegroup(s), so their AMI status shows %q (%s: %s: %s)",
		n, amiLookupFailedText, first.Operation, first.Reason, first.Error))
}
