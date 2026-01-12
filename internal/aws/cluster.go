package aws

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/dantech2000/refresh/internal/ui"
	"github.com/fatih/color"
	"k8s.io/client-go/tools/clientcmd"
)

// ClusterResolver handles cluster name resolution and matching.
type ClusterResolver struct {
	awsCfg aws.Config
}

// NewClusterResolver creates a new cluster resolver.
func NewClusterResolver(awsCfg aws.Config) *ClusterResolver {
	return &ClusterResolver{awsCfg: awsCfg}
}

// awsNamePattern is the regex for valid AWS EKS cluster names.
var awsNamePattern = regexp.MustCompile(`^[0-9A-Za-z][A-Za-z0-9-_]*$`)

// ClusterName resolves the EKS cluster name from CLI flag or kubeconfig.
// It supports partial name matching and prompts for confirmation when multiple matches exist.
func ClusterName(ctx context.Context, awsCfg aws.Config, cliFlag string) (string, error) {
	pattern, err := resolveClusterPattern(cliFlag)
	if err != nil {
		return "", err
	}

	// Get available clusters with spinner
	spinner := ui.NewFunSpinnerForCategory("general")
	if err := spinner.Start(); err != nil {
		return "", err
	}
	defer spinner.Stop()

	clusters, err := availableClusters(ctx, awsCfg)
	spinner.Success("Cluster name resolved!")
	if err != nil {
		return "", FormatAWSError(err, "listing EKS clusters")
	}

	if len(clusters) == 0 {
		return "", fmt.Errorf("no EKS clusters found in current region")
	}

	// Find matching clusters
	matches := MatchingClusters(clusters, pattern)

	// Prefer exact match
	for _, match := range matches {
		if match == pattern {
			return match, nil
		}
	}

	// Handle matches with user confirmation
	selectedCluster, err := confirmClusterSelection(matches, pattern)
	if err != nil {
		// Show available clusters for reference
		if len(matches) == 0 {
			color.Yellow("Available clusters:")
			for _, cluster := range clusters {
				fmt.Printf("  - %s\n", cluster)
			}
		}
		return "", err
	}

	// Inform user if a different cluster was selected
	if selectedCluster != pattern {
		color.Green("Using cluster: %s", selectedCluster)
	}

	return selectedCluster, nil
}

// resolveClusterPattern determines the cluster pattern from CLI flag or kubeconfig.
func resolveClusterPattern(cliFlag string) (string, error) {
	if cliFlag != "" {
		return cliFlag, nil
	}

	return extractClusterFromKubeconfig()
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

	// Try to extract from cluster server URL
	clusterEntry := rawConfig.Clusters[clusterRef]
	if clusterEntry != nil && clusterEntry.Server != "" {
		if pattern := extractNameFromServer(clusterEntry.Server); pattern != "" {
			return pattern, nil
		}
	}

	return "", fmt.Errorf("could not determine valid EKS cluster name; please use --cluster flag")
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

// availableClusters returns all EKS cluster names in the current region.
func availableClusters(ctx context.Context, awsCfg aws.Config) ([]string, error) {
	eksClient := eks.NewFromConfig(awsCfg)

	var clusters []string
	var nextToken *string

	for {
		out, err := eksClient.ListClusters(ctx, &eks.ListClustersInput{NextToken: nextToken})
		if err != nil {
			return nil, err
		}

		clusters = append(clusters, out.Clusters...)

		if out.NextToken == nil {
			break
		}
		nextToken = out.NextToken
	}

	return clusters, nil
}

// MatchingClusters returns cluster names that contain the given pattern.
// If pattern is empty, returns all clusters.
func MatchingClusters(clusters []string, pattern string) []string {
	if pattern == "" {
		return clusters
	}

	matches := make([]string, 0, len(clusters))
	for _, cluster := range clusters {
		if strings.Contains(cluster, pattern) {
			matches = append(matches, cluster)
		}
	}

	return matches
}

// confirmClusterSelection prompts user to confirm when multiple clusters match.
// Returns the selected cluster or error if user cancels.
func confirmClusterSelection(matches []string, pattern string) (string, error) {
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no clusters found matching pattern: %s", pattern)
	case 1:
		return matches[0], nil
	default:
		return promptForClusterSelection(matches, pattern)
	}
}

// promptForClusterSelection displays matching clusters and prompts for selection.
func promptForClusterSelection(matches []string, pattern string) (string, error) {
	color.Yellow("Multiple clusters match pattern '%s':", pattern)
	for i, cluster := range matches {
		fmt.Printf("  %d) %s\n", i+1, cluster)
	}

	color.Cyan("Select cluster number (1-%d) or press Enter to cancel: ", len(matches))

	var response string
	if _, err := fmt.Scanln(&response); err != nil {
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
