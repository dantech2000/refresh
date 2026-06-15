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
// error. Shared by BuildKubeClient and BuildMetricsClient so both clients
// resolve identically.
func resolveRESTConfig(kubeconfigPath string) (*rest.Config, KubeDiag, error) {
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
		return icCfg, KubeDiag{Source: "in-cluster"}, nil
	}
	return nil, KubeDiag{Source: "none"}, fmt.Errorf("no kubeconfig found and in-cluster config not available")
}

// BuildKubeClient builds a Kubernetes client, preferring an explicit kubeconfig
// path, then $KUBECONFIG, then ~/.kube/config, then in-cluster config. It
// returns a KubeDiag describing what was tried (for diagnostics) alongside the
// client. An explicit --kubeconfig path that doesn't exist is a hard error.
func BuildKubeClient(kubeconfigPath string) (kubernetes.Interface, KubeDiag, error) {
	cfg, diag, err := resolveRESTConfig(kubeconfigPath)
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
	cfg, _, err := resolveRESTConfig(kubeconfigPath)
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
