package aws

import (
	"bufio"
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
	"github.com/mattn/go-isatty"
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
	// ReadOnly marks the caller as a read-only command. Without a TTY, a
	// single non-exact substring match is then accepted (with a note on
	// stderr) instead of failing, since nothing is mutated.
	ReadOnly bool
}

// ListClustersAPI is the EKS subset needed to resolve cluster names.
type ListClustersAPI interface {
	ListClusters(ctx context.Context, in *eks.ListClustersInput, optFns ...func(*eks.Options)) (*eks.ListClustersOutput, error)
}

// stdinIsTerminal reports whether a prompt can be answered. It is a var so
// tests can simulate a TTY or an unattended run.
var stdinIsTerminal = func() bool {
	return isatty.IsTerminal(os.Stdin.Fd()) || isatty.IsCygwinTerminal(os.Stdin.Fd())
}

// ClusterName resolves the EKS cluster name for a mutating command. The
// pattern comes from cliFlag, then the active refresh context, then the
// current kubeconfig context. An exact name always wins; a single non-exact
// substring match needs interactive confirmation and fails without a TTY.
func ClusterName(ctx context.Context, awsCfg aws.Config, cliFlag string) (string, error) {
	return ClusterNameWithOptions(ctx, awsCfg, cliFlag, ClusterNameOptions{})
}

// ClusterNameWithOptions is ClusterName with caller-specific options.
func ClusterNameWithOptions(ctx context.Context, awsCfg aws.Config, cliFlag string, opts ClusterNameOptions) (string, error) {
	pattern, err := resolveClusterPattern(cliFlag)
	if err != nil {
		return "", err
	}
	return resolveClusterName(ctx, eks.NewFromConfig(awsCfg), pattern, opts)
}

// resolveClusterName lists the clusters visible through api and selects the
// one that pattern refers to.
func resolveClusterName(ctx context.Context, api ListClustersAPI, pattern string, opts ClusterNameOptions) (string, error) {
	// Get available clusters with spinner
	spinner := ui.NewFunSpinnerForCategory("general")
	if err := spinner.Start(); err != nil {
		return "", err
	}
	defer spinner.Stop()

	clusters, err := listClusterNames(ctx, api)
	if err != nil {
		return "", FormatAWSError(err, "listing EKS clusters")
	}
	spinner.Success("Cluster name resolved!")

	if len(clusters) == 0 {
		return "", fmt.Errorf("no EKS clusters found in current region")
	}

	// Find matching clusters (an exact name match short-circuits to itself)
	matches := MatchingClusters(clusters, pattern)

	// Handle matches with user confirmation
	selectedCluster, err := confirmClusterSelection(matches, pattern, opts)
	if err != nil {
		// Show available clusters for reference
		if len(matches) == 0 {
			_, _ = color.New(color.FgYellow).Fprintln(os.Stderr, "Available clusters:")
			for _, cluster := range clusters {
				_, _ = fmt.Fprintf(os.Stderr, "  - %s\n", cluster)
			}
		}
		return "", err
	}

	// Inform user if a different cluster was selected. Stderr keeps
	// -o json/yaml stdout clean.
	if selectedCluster != pattern {
		_, _ = color.New(color.FgGreen).Fprintf(os.Stderr, "Using cluster: %s\n", selectedCluster)
	}

	return selectedCluster, nil
}

// resolveClusterPattern determines the cluster pattern from CLI flag,
// active refresh context, or kubeconfig (in that order). When none yields a
// name, the error wraps ErrNoClusterSpecified.
func resolveClusterPattern(cliFlag string) (string, error) {
	if cliFlag = strings.TrimSpace(cliFlag); cliFlag != "" {
		return cliFlag, nil
	}
	if name := activeContextCluster(); name != "" {
		return name, nil
	}
	name, err := extractClusterFromKubeconfig()
	if err != nil {
		return "", fmt.Errorf("%w (kubeconfig: %v)", ErrNoClusterSpecified, err)
	}
	return name, nil
}

func activeContextCluster() string {
	f, err := cliconfig.Load()
	if err != nil {
		return ""
	}
	if _, ctx, ok := f.Active(); ok {
		return strings.TrimSpace(ctx.Cluster)
	}
	return ""
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
		return "", fmt.Errorf("failed to load kubeconfig: %v", err)
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
// prompt for a choice on a TTY and fail without one.
func confirmClusterSelection(matches []string, pattern string, opts ClusterNameOptions) (string, error) {
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no clusters found matching pattern: %s", pattern)
	case 1:
		match := matches[0]
		if match == pattern {
			return match, nil
		}
		if stdinIsTerminal() {
			return promptForSingleClusterMatch(match, pattern)
		}
		if opts.ReadOnly {
			_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "No cluster named %q; using the only partial match %q\n", pattern, match)
			return match, nil
		}
		return "", fmt.Errorf("no cluster named %q (partial match: %s); pass the exact name with --cluster (no interactive terminal for confirmation)", pattern, match)
	default:
		if !stdinIsTerminal() {
			return "", fmt.Errorf("pattern %q matched %d clusters (%s); pass the exact name with --cluster (no interactive terminal for selection)", pattern, len(matches), strings.Join(matches, ", "))
		}
		return promptForClusterSelection(matches, pattern)
	}
}

// promptForSingleClusterMatch asks the user to confirm a non-exact match.
func promptForSingleClusterMatch(match, pattern string) (string, error) {
	_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "No cluster named %q. Use %q? [y/N]: ", pattern, match)
	response, err := readPromptLine()
	if err != nil {
		return "", fmt.Errorf("operation cancelled: failed to read input")
	}
	switch strings.ToLower(response) {
	case "y", "yes":
		return match, nil
	default:
		return "", fmt.Errorf("operation cancelled by user")
	}
}

// promptForClusterSelection displays matching clusters and prompts for selection.
func promptForClusterSelection(matches []string, pattern string) (string, error) {
	_, _ = color.New(color.FgYellow).Fprintf(os.Stderr, "Multiple clusters match pattern '%s':\n", pattern)
	for i, cluster := range matches {
		_, _ = fmt.Fprintf(os.Stderr, "  %d) %s\n", i+1, cluster)
	}

	_, _ = color.New(color.FgCyan).Fprintf(os.Stderr, "Select cluster number (1-%d) or press Enter to cancel: ", len(matches))

	response, err := readPromptLine()
	if err != nil {
		return "", fmt.Errorf("operation cancelled: failed to read input")
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

// readPromptLine reads one line from stdin. Unlike fmt.Scanln, a bare Enter
// returns an empty string instead of an error, so prompts can honor their
// advertised "press Enter to cancel/decline" behavior.
func readPromptLine() (string, error) {
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
