package cmdutil

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/contexts"
	"github.com/dantech2000/refresh/internal/kube"
)

// EnvCluster names the cluster for every command of one shell. A flag or an
// argument beats it, and it beats the active context.
const EnvCluster = "REFRESH_CLUSTER"

// TargetSource says where the cluster of a command came from.
type TargetSource int

// The sources of a cluster, in the order that ResolveCluster tries them.
const (
	// TargetExplicit: the argument, or -c/--cluster.
	TargetExplicit TargetSource = iota + 1
	// TargetEnv: REFRESH_CLUSTER.
	TargetEnv
	// TargetContext: the active refresh context.
	TargetContext
	// TargetKubeconfig: the current kubeconfig context.
	TargetKubeconfig
)

// Target is the cluster that a command acts on.
type Target struct {
	Cluster string
	Source  TargetSource
}

// AddClusterFlag adds -c/--cluster to cmd.
func AddClusterFlag(cmd *cobra.Command, p *string) {
	cmd.Flags().StringVarP(p, "cluster", "c", "",
		"EKS cluster name (env: "+EnvCluster+"; default: the active context, then the current kubeconfig context)")
}

// AddChangeClusterFlag adds -c/--cluster to a command that changes a
// cluster. Its help does not name the kubeconfig: a change never takes its
// cluster from there.
func AddChangeClusterFlag(cmd *cobra.Command, p *string) {
	cmd.Flags().StringVarP(p, "cluster", "c", "",
		"EKS cluster name (env: "+EnvCluster+"; default: the active context; never the kubeconfig)")
}

// ResolveCluster returns the cluster that a command acts on. It makes no
// AWS call, and the name is taken as it is: a name is exact. The order is:
//
//  1. explicit: the argument, or -c/--cluster.
//  2. REFRESH_CLUSTER.
//  3. The active refresh context.
//  4. For a read-only command: the cluster of the kubeconfig context, which
//     --kubeconfig and --kube-context choose. Only a context that names an
//     EKS cluster and its region counts (see kube.CurrentCluster). The
//     command then reads that region: ResolveCluster stores it in
//     f.Global.Regions when -r/--region named none, so a command must
//     resolve its cluster before it calls f.AWS.
//
// A command that changes something passes readOnly false, with or without
// --dry-run: a kubeconfig that happens to point at production must never
// choose the target of a change.
func ResolveCluster(f *Factory, explicit string, readOnly bool) (Target, error) {
	if name := strings.TrimSpace(explicit); name != "" {
		return Target{Cluster: name, Source: TargetExplicit}, nil
	}
	if name := strings.TrimSpace(os.Getenv(EnvCluster)); name != "" {
		return Target{Cluster: name, Source: TargetEnv}, nil
	}
	store, err := f.Contexts()
	if err != nil {
		return Target{}, err
	}
	file, err := store.Load()
	if err != nil {
		return Target{}, err
	}
	_, active, ok, err := file.Active(os.Getenv(contexts.EnvContext))
	if err != nil {
		return Target{}, err
	}
	if ok && active.Cluster != "" {
		return Target{Cluster: active.Cluster, Source: TargetContext}, nil
	}
	if readOnly {
		name, region, ok, err := kube.CurrentCluster(f.Global.Kubeconfig, f.Global.KubeContext)
		if err != nil {
			return Target{}, fmt.Errorf("reading the kubeconfig for the cluster: %w", err)
		}
		if ok {
			// The kubeconfig also says where the cluster is. That region
			// is the region of the run unless -r/--region names one: the
			// region of the AWS configuration may be another one.
			if len(f.Global.Regions) == 0 {
				f.Global.Regions = []string{region}
			}
			return Target{Cluster: name, Source: TargetKubeconfig}, nil
		}
	}
	return Target{}, errors.New("no cluster specified\n" +
		"Set one with:\n" +
		"  -c/--cluster, or [cluster]      for this command, whichever it has\n" +
		"  " + EnvCluster + "                 for this shell\n" +
		"  refresh context use <name>      until you change it")
}

// NotFoundError returns the error for a named thing that does not exist:
//
//	cluster "prd" not found in us-east-1
//	Did you mean:
//	  prod
//	  prod-eu
//	List the clusters with: refresh cluster list -r us-east-1
//
// what is the thing with its name and where is where it was looked for.
// candidates are the names that do exist; the close ones are suggested.
// listWith is the command that lists them all.
func NotFoundError(what, name, where string, candidates []string, listWith string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %q not found in %s", what, name, where)
	if near := Suggest(name, candidates); len(near) > 0 {
		b.WriteString("\nDid you mean:")
		for _, c := range near {
			b.WriteString("\n  " + c)
		}
	}
	if listWith != "" {
		fmt.Fprintf(&b, "\nList the %ss with: %s", what, listWith)
	}
	return errors.New(b.String())
}

// maxSuggestions is the most names that Suggest returns.
const maxSuggestions = 5

// Suggest returns up to five of candidates that are close to name: first
// those that contain it or that it contains, then those within a small
// edit distance of it, as a whole or at their start, nearest first. Both
// tests ignore case.
func Suggest(name string, candidates []string) []string {
	type scored struct {
		name     string
		distance int
	}
	lower := strings.ToLower(name)
	if lower == "" {
		return nil
	}
	// A third of the length, and at least 2: "prd" finds "prod".
	limit := max(2, len(lower)/3)
	var near []scored
	for _, c := range candidates {
		lc := strings.ToLower(c)
		// A typing error at the start of a longer name counts too: the
		// start of the candidate is compared, one step behind a whole
		// name at the same distance.
		start := lc[:min(len(lc), len(lower)+1)]
		switch d := min(editDistance(lower, lc), editDistance(lower, start)+1); {
		case strings.Contains(lc, lower) || strings.Contains(lower, lc):
			near = append(near, scored{c, 0})
		case d <= limit:
			near = append(near, scored{c, d})
		}
	}
	slices.SortFunc(near, func(a, b scored) int {
		return cmp.Or(cmp.Compare(a.distance, b.distance), cmp.Compare(a.name, b.name))
	})
	out := make([]string, 0, maxSuggestions)
	for _, c := range near {
		if len(out) == maxSuggestions {
			break
		}
		out = append(out, c.name)
	}
	return out
}

// editDistance returns the number of single-character edits that turn a
// into b.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// ClusterClients returns the clients for a command that reads one cluster,
// in the region of the run. It fails when the run has more than one region
// or none.
func ClusterClients(ctx context.Context, f *Factory) (*awsx.Clients, error) {
	if len(f.Global.Regions) > 1 {
		return nil, fmt.Errorf("this command reads one cluster in one region, and -r/--region names %d", len(f.Global.Regions))
	}
	provider, err := f.AWS(ctx)
	if err != nil {
		return nil, err
	}
	if provider.Region() == "" {
		return nil, errors.New("no AWS region: pass -r/--region, set AWS_REGION, or use a context with a region")
	}
	return provider.Clients(""), nil
}
