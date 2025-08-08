package health

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// CheckCriticalWorkloads validates that critical system workloads are running
func (hc *HealthChecker) CheckCriticalWorkloads(ctx context.Context) HealthResult {
	result := HealthResult{
		Name:       "Critical Workloads",
		IsBlocking: true, // Critical workloads are blocking
		Details:    []string{},
	}

	if hc.k8sClient == nil {
		result.Status = StatusWarn
		result.Score = 70
		result.Message = "Kubernetes client not available, skipping workload check"
		result.Details = append(result.Details, "Install kubectl and configure cluster access to enable this check")
		return result
	}

	// Check critical namespaces
	criticalNamespaces := []string{
		"kube-system",
		"kube-public",
		"kube-node-lease",
	}

	totalPods := 0
	runningPods := 0
	var problemPods []string

	for _, namespace := range criticalNamespaces {
		pods, err := hc.k8sClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			result.Details = append(result.Details, fmt.Sprintf("Failed to list pods in %s: %v", namespace, err))
			continue
		}

		namespacePods := 0
		namespaceRunning := 0

		for _, pod := range pods.Items {
			// Skip completed pods (jobs, etc.)
			if pod.Status.Phase == "Succeeded" {
				continue
			}

			totalPods++
			namespacePods++

			switch pod.Status.Phase {
			case "Running":
				// Check if all containers are ready
				allReady := true
				for _, containerStatus := range pod.Status.ContainerStatuses {
					if !containerStatus.Ready {
						allReady = false
						break
					}
				}
				if allReady {
					runningPods++
					namespaceRunning++
				} else {
					problemPods = append(problemPods, fmt.Sprintf("%s/%s (containers not ready)", namespace, pod.Name))
				}
			case "Pending":
				problemPods = append(problemPods, fmt.Sprintf("%s/%s (Pending)", namespace, pod.Name))
			case "Failed":
				problemPods = append(problemPods, fmt.Sprintf("%s/%s (Failed)", namespace, pod.Name))
			case "Unknown":
				problemPods = append(problemPods, fmt.Sprintf("%s/%s (Unknown)", namespace, pod.Name))
			default:
				problemPods = append(problemPods, fmt.Sprintf("%s/%s (%s)", namespace, pod.Name, pod.Status.Phase))
			}
		}

		result.Details = append(result.Details, fmt.Sprintf("%s: %d/%d pods running", namespace, namespaceRunning, namespacePods))
	}

	// Calculate score and status
	if totalPods == 0 {
		result.Status = StatusWarn
		result.Score = 70
		result.Message = "No critical pods found"
		return result
	}

	scorePercentage := (runningPods * 100) / totalPods
	result.Score = scorePercentage

	if len(problemPods) == 0 {
		result.Status = StatusPass
		result.Message = fmt.Sprintf("%d/%d critical pods running", runningPods, totalPods)
	} else if scorePercentage >= 90 {
		result.Status = StatusWarn
		result.Message = fmt.Sprintf("%d/%d critical pods running, %d issues", runningPods, totalPods, len(problemPods))
		result.Details = append(result.Details, fmt.Sprintf("Problem pods: %v", problemPods))
	} else {
		result.Status = StatusFail
		result.Message = fmt.Sprintf("%d/%d critical pods running, %d issues", runningPods, totalPods, len(problemPods))
		result.Details = append(result.Details, fmt.Sprintf("Problem pods: %v", problemPods))
	}

	return result
}

// GetKubernetesClient creates a Kubernetes client from kubeconfig
func GetKubernetesClient() (kubernetes.Interface, error) {
	// Prefer kubeconfig if present
	kubeconfigPath := os.Getenv("KUBECONFIG")
	if kubeconfigPath == "" {
		homeDir, err := os.UserHomeDir()
		if err == nil {
			kubeconfigPath = filepath.Join(homeDir, ".kube", "config")
		}
	}

	if kubeconfigPath != "" {
		if st, err := os.Stat(kubeconfigPath); err == nil && !st.IsDir() {
			cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
			if err == nil {
				if clientset, cerr := kubernetes.NewForConfig(cfg); cerr == nil {
					return clientset, nil
				}
			}
		}
	}

	// Fall back to in-cluster configuration
	if icCfg, err := rest.InClusterConfig(); err == nil {
		if clientset, cerr := kubernetes.NewForConfig(icCfg); cerr == nil {
			return clientset, nil
		}
	}

	return nil, fmt.Errorf("unable to create kubernetes client: no kubeconfig found and in-cluster config not available")
}
