package fleetcmd

import (
	"context"
	"io"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/fleet"
)

// completionTimeout bounds the AWS call of a shell completion. A shell
// that waits for names must not hang on a slow endpoint.
const completionTimeout = 3 * time.Second

// A completion function has one way to fail: it offers no names. The shell
// prints what a completion writes, so nothing here writes an error, asks a
// question, or reads the kubeconfig for a cluster that the user did not
// name another way.

// lister returns the names to offer, from one list call.
type lister func(ctx context.Context, fetcher *fleet.Fetcher, c *awsx.Clients) ([]string, error)

// complete returns the completion function for the names that list
// returns. It offers nothing once the command line has most arguments.
func complete(f *cmdutil.Factory, most int, list lister) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
		if most >= 0 && len(args) >= most {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), completionTimeout)
		defer cancel()
		// A warning of the AWS setup must not reach the terminal in the
		// middle of a command line.
		quiet := *f.IOStreams
		quiet.ErrOut = io.Discard
		silent := *f
		silent.IOStreams = &quiet
		clients, err := cmdutil.ClusterClients(ctx, &silent)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		names, err := list(ctx, &fleet.Fetcher{}, clients)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		slices.Sort(names)
		return names, cobra.ShellCompDirectiveNoFileComp
	}
}

// CompleteClusters returns the completion function for a cluster name: the
// clusters of the region of the run. As the completion of an argument it
// offers names for the first argument only.
func CompleteClusters(f *cmdutil.Factory) cobra.CompletionFunc {
	return complete(f, 1, func(ctx context.Context, fetcher *fleet.Fetcher, c *awsx.Clients) ([]string, error) {
		return fetcher.Clusters(ctx, c)
	})
}

// RegisterClusterFlagCompletion completes the value of -c/--cluster with
// the clusters of the region of the run.
func RegisterClusterFlagCompletion(cmd *cobra.Command, f *cmdutil.Factory) {
	list := complete(f, -1, func(ctx context.Context, fetcher *fleet.Fetcher, c *awsx.Clients) ([]string, error) {
		return fetcher.Clusters(ctx, c)
	})
	// The flag exists: the command added it before this call.
	_ = cmd.RegisterFlagCompletionFunc("cluster", list)
}

// CompleteNodegroups returns the completion function for the nodegroup
// argument of a command: the nodegroups of the cluster that the flag, the
// environment, or the active context names.
func CompleteNodegroups(f *cmdutil.Factory, clusterFlag *string) cobra.CompletionFunc {
	return complete(f, 1, func(ctx context.Context, fetcher *fleet.Fetcher, c *awsx.Clients) ([]string, error) {
		cluster, err := completionCluster(f, *clusterFlag)
		if err != nil {
			return nil, err
		}
		return fetcher.NodegroupNames(ctx, c, cluster)
	})
}

// CompleteAddons returns the completion function for the add-on argument
// of a command.
func CompleteAddons(f *cmdutil.Factory, clusterFlag *string) cobra.CompletionFunc {
	return complete(f, 1, func(ctx context.Context, fetcher *fleet.Fetcher, c *awsx.Clients) ([]string, error) {
		cluster, err := completionCluster(f, *clusterFlag)
		if err != nil {
			return nil, err
		}
		return fetcher.AddonNames(ctx, c, cluster)
	})
}

// completionCluster returns the cluster for a completion. It resolves as a
// command that changes something does: without the kubeconfig. A
// completion that guessed the cluster from the kubeconfig would offer the
// nodegroups of a cluster that the user did not choose.
func completionCluster(f *cmdutil.Factory, explicit string) (string, error) {
	target, err := cmdutil.ResolveCluster(f, explicit, false)
	return target.Cluster, err
}
