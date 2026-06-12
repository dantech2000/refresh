package nodegroup

import (
	"context"
	"time"

	"github.com/urfave/cli/v3"

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
	return runner.Watch(cmd, func() error { return listNodegroupsOnce(ctx, cmd) })
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
	svc := factory.NewNodegroupService(awsCfg, false, logger)

	filters := runner.ParseFilters(cmd.StringSlice("filter"))
	opts := nodegroupsvc.ListOptions{
		Filters: filters,
	}

	var items []nodegroupsvc.NodegroupSummary
	start := time.Now()
	if err := runner.WithSpinner("nodegroup", "Nodegroup information gathered!", func() error {
		var lerr error
		items, lerr = svc.List(ctx, clusterName, opts)
		return lerr
	}); err != nil {
		return err
	}

	// Sort before encoding so --sort/--desc apply to every output format, not
	// just table/plain — matching cluster list and keeping JSON/YAML scriptable. (REF-49)
	items = sortNodegroupSummaries(items, cmd.String("sort"), cmd.Bool("desc"))

	payload := map[string]any{"cluster": clusterName, "nodegroups": items, "count": len(items)}
	if handled, err := runner.EncodeStdout(cmd.String("format"), payload); handled {
		return err
	}
	return outputNodegroupsTable(clusterName, items, time.Since(start))
}
