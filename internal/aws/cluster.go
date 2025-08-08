package aws

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/fatih/color"
	"k8s.io/client-go/tools/clientcmd"
)

func ClusterName(ctx context.Context, awsCfg aws.Config, cliFlag string) (string, error) {
	var pattern string

	// If CLI flag provided, use it as pattern
	if cliFlag != "" {
		pattern = cliFlag
	} else {
		// Try to get from kubeconfig
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			kubeconfig = os.ExpandEnv("$HOME/.kube/config")
		}
		configOverrides := &clientcmd.ConfigOverrides{}
		clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig},
			configOverrides,
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
		awsNameRe := "^[0-9A-Za-z][A-Za-z0-9-_]*$"
		if matched := regexp.MustCompile(awsNameRe).MatchString(clusterRef); matched {
			pattern = clusterRef
		} else {
			clusterEntry := rawConfig.Clusters[clusterRef]
			if clusterEntry != nil && clusterEntry.Server != "" {
				server := clusterEntry.Server
				parts := strings.Split(server, ".")
				if len(parts) > 0 {
					maybeName := strings.TrimPrefix(parts[0], "https://")
					if regexp.MustCompile(awsNameRe).MatchString(maybeName) {
						pattern = maybeName
					}
				}
			}
		}

		if pattern == "" {
			return "", fmt.Errorf("could not determine valid EKS cluster name; please use --cluster flag")
		}
	}

	// Get available clusters
	clusters, err := availableClusters(ctx, awsCfg)
	if err != nil {
		return "", FormatAWSError(err, "listing EKS clusters")
	}

	if len(clusters) == 0 {
		return "", fmt.Errorf("no EKS clusters found in current region")
	}

	// Find matching clusters
	matches := MatchingClusters(clusters, pattern)

	// If exact match exists, prefer it
	for _, match := range matches {
		if match == pattern {
			return match, nil
		}
	}

	// Handle matches
	selectedCluster, err := confirmClusterSelection(matches, pattern)
	if err != nil {
		// If no matches found, show available clusters for reference
		if len(matches) == 0 {
			color.Yellow("Available clusters:")
			for _, cluster := range clusters {
				fmt.Printf("  - %s\n", cluster)
			}
		}
		return "", err
	}

	// If we found a match but it's different from the pattern, inform the user
	if selectedCluster != pattern {
		color.Green("Using cluster: %s", selectedCluster)
	}

	return selectedCluster, nil
}

// availableClusters returns all EKS cluster names in the current region.
func availableClusters(ctx context.Context, awsCfg aws.Config) ([]string, error) {
	eksClient := eks.NewFromConfig(awsCfg)

	var clusters []string
	var nextToken *string
	for {
		out, err := eksClient.ListClusters(ctx, &eks.ListClustersInput{NextToken: nextToken})
		if err != nil {
			return nil, err // Caller handles formatting
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

	var matches []string
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
	if len(matches) == 1 {
		return matches[0], nil
	}

	if len(matches) == 0 {
		return "", fmt.Errorf("no clusters found matching pattern: %s", pattern)
	}

	// Multiple matches - show them and ask for confirmation
	color.Yellow("Multiple clusters match pattern '%s':", pattern)
	for i, cluster := range matches {
		fmt.Printf("  %d) %s\n", i+1, cluster)
	}

	color.Cyan("Select cluster number (1-%d) or press Enter to cancel: ", len(matches))
	var response string
	if _, err := fmt.Scanln(&response); err != nil {
		// If there's an error reading input, treat as cancellation
		return "", fmt.Errorf("operation cancelled: failed to read input")
	}

	if response == "" {
		return "", fmt.Errorf("operation cancelled by user")
	}

	// Try to parse as number
	var selected int
	if n, err := fmt.Sscanf(response, "%d", &selected); n == 1 && err == nil {
		if selected >= 1 && selected <= len(matches) {
			return matches[selected-1], nil
		}
	}

	return "", fmt.Errorf("invalid selection: %s", response)
}
