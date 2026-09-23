package aws

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/dantech2000/refresh/internal/cliconfig"
	"github.com/dantech2000/refresh/internal/ui"
	"github.com/fatih/color"
	"k8s.io/client-go/tools/clientcmd"
)

// awsNamePattern is the regex for valid AWS EKS cluster names.
var awsNamePattern = regexp.MustCompile(`^[0-9A-Za-z][A-Za-z0-9-_]*$`)

// ErrNoClusterSpecified is returned when no cluster could be resolved from the
// --cluster flag, a positional argument, the active refresh context, or the
// current kubeconfig context.
var ErrNoClusterSpecified = errors.New("no cluster specified; pass --cluster or run `refresh use <context>`")

// ClusterNameOptions tunes how a cluster pattern is resolved to a name.
type ClusterNameOptions struct {
	// ReadOnly marks the caller as a read-only command. Read-only commands
	// fall back to the kubeconfig current cluster, and without a TTY a single
	// non-exact substring match is accepted (with a note on stderr). Mutating
	// commands get neither: a kubeconfig that happens to point at prod must
	// never pick the target of an upgrade.
	ReadOnly bool
	// NonInteractive treats the run as having no TTY even when stdin is one,
	// so resolution never prompts. Set it when a prompt would be wrong, such
	// as a mutating command run with --yes and -o json/yaml: a non-exact name
	// then fails with an error that names the candidate(s).
	NonInteractive bool
}

// resolveSpinner is the progress indicator shown while clusters are listed.
type resolveSpinner interface {
	Start() error
	Success(message string)
	Stop()
}

// newResolveSpinner and promptLine are vars so tests can check that the
// spinner is stopped before any prompt is shown (its redraw erases the line).
var (
	newResolveSpinner = func() resolveSpinner { return ui.NewFunSpinnerForCategory("general") }
	promptLine        = ui.ReadLine
)

// ListClustersAPI is the EKS subset needed to resolve cluster names.
type ListClustersAPI interface {
	ListClusters(ctx context.Context, in *eks.ListClustersInput, optFns ...func(*eks.Options)) (*eks.ListClustersOutput, error)
}

// stdinIsTerminal reports whether a prompt can be answered. It is a var so
// tests can simulate a TTY or an unattended run.
var stdinIsTerminal = func() bool {
	return ui.IsTerminal(os.Stdin)
}

// ClusterName resolves the EKS cluster name for a mutating command. The
// pattern comes from cliFlag, then the active refresh context; the kubeconfig
// current context is never used. A cluster taken from the context is
// announced on stderr. An exact name always wins; a single non-exact
// substring match needs interactive confirmation and fails without a TTY.
func ClusterName(ctx context.Context, awsCfg aws.Config, cliFlag string) (string, error) {
	return ClusterNameWithOptions(ctx, awsCfg, cliFlag, ClusterNameOptions{})
}

// ClusterNameWithOptions is ClusterName with caller-specific options.
func ClusterNameWithOptions(ctx context.Context, awsCfg aws.Config, cliFlag string, opts ClusterNameOptions) (string, error) {
	pattern, fromContext, err := resolveClusterPattern(cliFlag, opts.ReadOnly)
	if err != nil {
		return "", err
	}
	if fromContext != "" && !opts.ReadOnly {
		_, _ = ui.StderrColor(color.FgYellow).Fprintf(ui.Stderr, "Using cluster %s (from context %s)\n", pattern, fromContext)
	}
	return resolveClusterName(ctx, eks.NewFromConfig(awsCfg), pattern, opts)
}

// resolveClusterName lists the clusters visible through api and selects the
// one that pattern refers to.
func resolveClusterName(ctx context.Context, api ListClustersAPI, pattern string, opts ClusterNameOptions) (string, error) {
	// Get available clusters with spinner
	spinner := newResolveSpinner()
	if err := spinner.Start(); err != nil {
		return "", err
	}

	clusters, err := listClusterNames(ctx, api)
	if err != nil {
		spinner.Stop()
		// listClusterNames (ListAllPages) already formatted the error;
		// formatting it again would print the IAM help twice.
		return "", err
	}
	// Success stops the spinner. It must happen before confirmClusterSelection:
	// a running spinner redraws its line and would erase any prompt.
	spinner.Success("Cluster name resolved!")

	if len(clusters) == 0 {
		return "", fmt.Errorf("no EKS clusters found in current region")
	}

	// Find matching clusters (an exact name match short-circuits to itself)
	matches := MatchingClusters(clusters, pattern)

	// Handle matches with user confirmation
	selectedCluster, err := confirmClusterSelection(ctx, matches, pattern, opts)
	if err != nil {
		// Show available clusters for reference
		if len(matches) == 0 {
			_, _ = ui.StderrColor(color.FgYellow).Fprintln(ui.Stderr, "Available clusters:")
			for _, cluster := range clusters {
				_, _ = fmt.Fprintf(ui.Stderr, "  - %s\n", cluster)
			}
		}
		return "", err
	}

	// Inform user if a different cluster was selected. Stderr keeps
	// -o json/yaml stdout clean.
	if selectedCluster != pattern {
		_, _ = ui.StderrColor(color.FgGreen).Fprintf(ui.Stderr, "Using cluster: %s\n", selectedCluster)
	}

	return selectedCluster, nil
}

// resolveClusterPattern determines the cluster pattern from CLI flag, then
// the active refresh context, then (only when allowKubeconfig) the kubeconfig
// current context. fromContext names the refresh context when the pattern
// came from it. When nothing yields a name, the error wraps
// ErrNoClusterSpecified.
func resolveClusterPattern(cliFlag string, allowKubeconfig bool) (pattern, fromContext string, err error) {
	if cliFlag = strings.TrimSpace(cliFlag); cliFlag != "" {
		return cliFlag, "", nil
	}
	ctxName, name, err := activeContextCluster()
	if err != nil {
		return "", "", err
	}
	if name != "" {
		return name, ctxName, nil
	}
	if !allowKubeconfig {
		return "", "", fmt.Errorf("%w (mutating commands do not use the kubeconfig current context)", ErrNoClusterSpecified)
	}
	name, err = extractClusterFromKubeconfig()
	if err != nil {
		return "", "", fmt.Errorf("%w (kubeconfig: %w)", ErrNoClusterSpecified, err)
	}
	return name, "", nil
}

// activeContextCluster returns the active refresh context's name and cluster.
// An unreadable context file counts as no context; an unknown REFRESH_CONTEXT
// is an error.
func activeContextCluster() (ctxName, cluster string, err error) {
	f, lerr := cliconfig.Load()
	if lerr != nil {
		return "", "", nil //nolint:nilerr // an unreadable context file counts as no context, as before
	}
	name, ctx, ok, err := f.Active()
	if err != nil || !ok {
		return "", "", err
	}
	return name, strings.TrimSpace(ctx.Cluster), nil
}

// extractClusterFromKubeconfig extracts the cluster name from the current kubeconfig context.
func extractClusterFromKubeconfig() (string, error) {
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		kubeconfig = os.ExpandEnv("$HOME/.kube/config")
	}

	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig},
		&clientcmd.ConfigOverrides{},
	)

	rawConfig, err := clientConfig.RawConfig()
	if err != nil {
		return "", fmt.Errorf("loading kubeconfig: %w", err)
	}

	currentContext := rawConfig.Contexts[rawConfig.CurrentContext]
	if currentContext == nil {
		return "", fmt.Errorf("no current context in kubeconfig")
	}

	clusterRef := currentContext.Cluster
	if clusterRef == "" {
		return "", fmt.Errorf("could not determine EKS cluster name from kubeconfig context")
	}

	// Check if it's already a valid AWS name
	if awsNamePattern.MatchString(clusterRef) {
		return clusterRef, nil
	}

	// `aws eks update-kubeconfig` names the cluster entry by its ARN.
	if name := clusterNameFromARN(clusterRef); name != "" {
		return name, nil
	}

	// Try to extract from cluster server URL
	clusterEntry := rawConfig.Clusters[clusterRef]
	if clusterEntry != nil && clusterEntry.Server != "" {
		if pattern := extractNameFromServer(clusterEntry.Server); pattern != "" {
			return pattern, nil
		}
	}

	return "", fmt.Errorf("could not determine valid EKS cluster name; please use --cluster flag")
}

// clusterNameFromARN returns the cluster name from an EKS cluster ARN
// (arn:aws:eks:<region>:<account>:cluster/<name>), or "" if ref is not one.
func clusterNameFromARN(ref string) string {
	if !strings.HasPrefix(ref, "arn:") {
		return ""
	}
	_, name, ok := strings.Cut(ref, ":cluster/")
	if !ok || !awsNamePattern.MatchString(name) {
		return ""
	}
	return name
}

// extractNameFromServer attempts to extract the cluster name from a server URL.
func extractNameFromServer(server string) string {
	parts := strings.Split(server, ".")
	if len(parts) == 0 {
		return ""
	}

	maybeName := strings.TrimPrefix(parts[0], "https://")
	if awsNamePattern.MatchString(maybeName) {
		return maybeName
	}

	return ""
}

// listClusterNames returns all EKS cluster names visible through api.
func listClusterNames(ctx context.Context, api ListClustersAPI) ([]string, error) {
	return ListAllPages(ctx, "listing clusters",
		func(rc context.Context, token *string) (*eks.ListClustersOutput, error) {
			return api.ListClusters(rc, &eks.ListClustersInput{NextToken: token})
		},
		func(out *eks.ListClustersOutput) ([]string, *string) { return out.Clusters, out.NextToken },
	)
}

// MatchingClusters returns cluster names that contain the given pattern.
// If a cluster is named exactly pattern, only that cluster is returned, so
// "prod" never also selects "prod-legacy". If pattern is empty, returns all
// clusters.
func MatchingClusters(clusters []string, pattern string) []string {
	return matchPreferExact(clusters, pattern)
}

// matchPreferExact returns the names equal to pattern when there is one,
// otherwise every name containing pattern. An empty pattern returns names.
func matchPreferExact(names []string, pattern string) []string {
	if pattern == "" {
		return names
	}
	for _, n := range names {
		if n == pattern {
			return []string{n}
		}
	}

	matches := make([]string, 0, len(names))
	for _, n := range names {
		if strings.Contains(n, pattern) {
			matches = append(matches, n)
		}
	}

	return matches
}

// confirmClusterSelection picks the cluster from matches. An exact match is
// returned as-is. A single non-exact (substring) match is confirmed on a TTY;
// without one it is accepted only for read-only callers. Multiple matches
// prompt for a choice on a TTY and fail without one. opts.NonInteractive
// counts as "no TTY".
func confirmClusterSelection(ctx context.Context, matches []string, pattern string, opts ClusterNameOptions) (string, error) {
	interactive := !opts.NonInteractive && stdinIsTerminal()
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no clusters found matching pattern: %s", pattern)
	case 1:
		match := matches[0]
		if match == pattern {
			return match, nil
		}
		if interactive {
			return promptForSingleClusterMatch(ctx, match, pattern)
		}
		if opts.ReadOnly {
			_, _ = ui.StderrColor(color.FgYellow).Fprintf(ui.Stderr, "No cluster named %q; using the only partial match %q\n", pattern, match)
			return match, nil
		}
		return "", fmt.Errorf("no cluster named %q (partial match: %s); pass the exact name with --cluster (no interactive terminal for confirmation)", pattern, match)
	default:
		if !interactive {
			return "", fmt.Errorf("pattern %q matched %d clusters (%s); pass the exact name with --cluster (no interactive terminal for selection)", pattern, len(matches), strings.Join(matches, ", "))
		}
		return promptForClusterSelection(ctx, matches, pattern)
	}
}

// promptForSingleClusterMatch asks the user to confirm a non-exact match.
func promptForSingleClusterMatch(ctx context.Context, match, pattern string) (string, error) {
	_, _ = ui.StderrColor(color.FgYellow).Fprintf(ui.Stderr, "No cluster named %q. Use %q? [y/N]: ", pattern, match)
	response, err := promptLine(ctx)
	if err != nil {
		return "", ui.PromptError(err)
	}
	switch strings.ToLower(response) {
	case "y", "yes":
		return match, nil
	default:
		return "", fmt.Errorf("operation cancelled by user")
	}
}

// promptForClusterSelection displays matching clusters and prompts for selection.
func promptForClusterSelection(ctx context.Context, matches []string, pattern string) (string, error) {
	_, _ = ui.StderrColor(color.FgYellow).Fprintf(ui.Stderr, "Multiple clusters match pattern '%s':\n", pattern)
	for i, cluster := range matches {
		_, _ = fmt.Fprintf(ui.Stderr, "  %d) %s\n", i+1, cluster)
	}

	_, _ = ui.StderrColor(color.FgCyan).Fprintf(ui.Stderr, "Select cluster number (1-%d) or press Enter to cancel: ", len(matches))

	response, err := promptLine(ctx)
	if err != nil {
		return "", ui.PromptError(err)
	}

	if response == "" {
		return "", fmt.Errorf("operation cancelled by user")
	}

	var selected int
	if n, err := fmt.Sscanf(response, "%d", &selected); n == 1 && err == nil {
		if selected >= 1 && selected <= len(matches) {
			return matches[selected-1], nil
		}
	}

	return "", fmt.Errorf("invalid selection: %s", response)
}
