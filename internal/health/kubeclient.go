package health

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
)

// kubeProbeTimeout bounds the connectivity probe so an unreachable cluster
// fails fast with a diagnostic instead of stalling the whole health check.
const kubeProbeTimeout = 5 * time.Second

// KubeDiag describes how the Kubernetes client was (or would be) resolved, so
// callers can emit an actionable message when the API can't be reached.
type KubeDiag struct {
	Source  string // "--kubeconfig", "KUBECONFIG", "default", "in-cluster", "none"
	Path    string
	Context string
	// Server is the API server of the context that was used (or, on a
	// cluster mismatch, of the current context). Set only for target-checked
	// resolution.
	Server string
	// SwitchedFrom is the kubeconfig current-context when a different context
	// was selected because it points at the target cluster.
	SwitchedFrom string
}

// String renders the resolution attempt for diagnostics.
func (d KubeDiag) String() string {
	switch {
	case d.Source == "in-cluster":
		return "in-cluster service account"
	case d.Path != "":
		ctx := d.Context
		if ctx == "" {
			ctx = "default context"
		}
		return fmt.Sprintf("kubeconfig %s (context %q, via %s)", d.Path, ctx, d.Source)
	default:
		return "no kubeconfig found"
	}
}

// resolveRESTConfig resolves a *rest.Config and a diagnostic, preferring an
// explicit kubeconfig path, then $KUBECONFIG, then ~/.kube/config, then
// in-cluster config. An explicit --kubeconfig path that doesn't exist is a hard
// error. Shared by the kube and metrics client builders so both clients
// resolve identically.
//
// When target is non-nil, the config must point at that EKS cluster: the
// current context is used if its server matches the cluster endpoint,
// otherwise another context whose server matches is selected, otherwise a
// *ClusterMismatchError is returned and no config is produced.
func resolveRESTConfig(kubeconfigPath string, target *TargetCluster) (*rest.Config, KubeDiag, error) {
	source := ""
	path := strings.TrimSpace(kubeconfigPath)
	switch {
	case path != "":
		source = "--kubeconfig"
	case os.Getenv("KUBECONFIG") != "":
		path, source = os.Getenv("KUBECONFIG"), "KUBECONFIG"
	default:
		if home, err := os.UserHomeDir(); err == nil {
			path, source = filepath.Join(home, ".kube", "config"), "default"
		}
	}

	if path != "" {
		st, statErr := os.Stat(path)
		switch {
		case statErr == nil && !st.IsDir():
			diag := KubeDiag{Source: source, Path: path}
			if target != nil {
				cfg, err := restConfigForTarget(path, &diag, *target)
				if err != nil {
					return nil, diag, err
				}
				return cfg, diag, nil
			}
			if raw, lerr := clientcmd.LoadFromFile(path); lerr == nil {
				diag.Context = raw.CurrentContext
			}
			cfg, cerr := clientcmd.BuildConfigFromFlags("", path)
			if cerr != nil {
				return nil, diag, fmt.Errorf("loading kubeconfig %s: %w", path, cerr)
			}
			return cfg, diag, nil
		case source == "--kubeconfig":
			// An explicitly requested file that isn't there is a user error,
			// not a reason to silently fall back.
			return nil, KubeDiag{Source: source, Path: path}, fmt.Errorf("kubeconfig %q not found", path)
		}
		// A missing default/$KUBECONFIG file falls through to in-cluster.
	}

	if icCfg, err := rest.InClusterConfig(); err == nil {
		diag := KubeDiag{Source: "in-cluster", Server: icCfg.Host}
		// In-cluster config points at the kubernetes service IP, so it can only
		// be verified when that host happens to equal the EKS endpoint.
		if target != nil && !SameClusterEndpoint(icCfg.Host, target.Endpoint) {
			return nil, diag, &ClusterMismatchError{Target: *target, Server: icCfg.Host}
		}
		return icCfg, diag, nil
	}
	return nil, KubeDiag{Source: "none"}, fmt.Errorf("no kubeconfig found and in-cluster config not available")
}

// BuildKubeClient builds a Kubernetes client, preferring an explicit kubeconfig
// path, then $KUBECONFIG, then ~/.kube/config, then in-cluster config. It
// returns a KubeDiag describing what was tried (for diagnostics) alongside the
// client. An explicit --kubeconfig path that doesn't exist is a hard error.
//
// BuildKubeClient does not check which cluster the client points at. Code that
// acts on a specific EKS cluster must use [BuildKubeClientForCluster].
func BuildKubeClient(kubeconfigPath string) (kubernetes.Interface, KubeDiag, error) {
	return buildKubeClient(kubeconfigPath, nil)
}

// BuildKubeClientForCluster builds a Kubernetes client that is verified to
// point at target (see [SameClusterEndpoint]). If the kubeconfig current
// context points elsewhere, a context whose server matches the target endpoint
// is selected instead; if none exists, a *ClusterMismatchError is returned and
// no client is built.
func BuildKubeClientForCluster(kubeconfigPath string, target TargetCluster) (kubernetes.Interface, KubeDiag, error) {
	return buildKubeClient(kubeconfigPath, &target)
}

func buildKubeClient(kubeconfigPath string, target *TargetCluster) (kubernetes.Interface, KubeDiag, error) {
	cfg, diag, err := resolveRESTConfig(kubeconfigPath, target)
	if err != nil {
		return nil, diag, err
	}
	client, nerr := kubernetes.NewForConfig(cfg)
	if nerr != nil {
		return nil, diag, fmt.Errorf("building kubernetes client: %w", nerr)
	}
	return client, diag, nil
}

// BuildMetricsClient builds a metrics-server (metrics.k8s.io) node-metrics
// lister from the same kubeconfig resolution as BuildKubeClient. A config error
// is returned; metrics-server simply not being installed is NOT an error here —
// that surfaces at List time, so the utilization check can skip gracefully.
func BuildMetricsClient(kubeconfigPath string) (NodeMetricsLister, error) {
	return buildMetricsClient(kubeconfigPath, nil)
}

// BuildMetricsClientForCluster is [BuildMetricsClient] with the same target
// check and context selection as [BuildKubeClientForCluster].
func BuildMetricsClientForCluster(kubeconfigPath string, target TargetCluster) (NodeMetricsLister, error) {
	return buildMetricsClient(kubeconfigPath, &target)
}

func buildMetricsClient(kubeconfigPath string, target *TargetCluster) (NodeMetricsLister, error) {
	cfg, _, err := resolveRESTConfig(kubeconfigPath, target)
	if err != nil {
		return nil, err
	}
	cs, err := metricsclient.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("building metrics client: %w", err)
	}
	return cs.MetricsV1beta1().NodeMetricses(), nil
}

// ProbeConnection verifies the Kubernetes API is actually reachable (and basic
// list RBAC is present) with a bounded timeout, so callers can report an
// unreachable cluster up-front rather than silently degrading every check.
func ProbeConnection(ctx context.Context, client kubernetes.Interface) error {
	if client == nil {
		return fmt.Errorf("kubernetes client not initialized")
	}
	pctx, cancel := context.WithTimeout(ctx, kubeProbeTimeout)
	defer cancel()
	if _, err := client.CoreV1().Namespaces().List(pctx, metav1.ListOptions{Limit: 1}); err != nil {
		return err
	}
	return nil
}
