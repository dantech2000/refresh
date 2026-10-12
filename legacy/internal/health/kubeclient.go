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
	// Unverified is set when the user named the context explicitly
	// (--kube-context) and its server does not match the target endpoint,
	// e.g. a proxied or tunnelled API server. The context is trusted as asked.
	Unverified bool
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

// inClusterConfig is a seam for tests.
var inClusterConfig = rest.InClusterConfig

// locateKubeconfig picks the kubeconfig file: an explicit path, then
// $KUBECONFIG, then ~/.kube/config. A $KUBECONFIG value may list several files
// (colon-separated on Unix); path is then that whole list, and it is used when
// at least one file in it exists. path is "" when no file exists and the
// caller should fall back to in-cluster config. An explicit path that doesn't
// exist is a hard error.
func locateKubeconfig(kubeconfigPath string) (path, source string, err error) {
	path = strings.TrimSpace(kubeconfigPath)
	switch {
	case path != "":
		source = "--kubeconfig"
	case os.Getenv("KUBECONFIG") != "":
		path, source = os.Getenv("KUBECONFIG"), "KUBECONFIG"
	default:
		if home, herr := os.UserHomeDir(); herr == nil {
			path, source = filepath.Join(home, ".kube", "config"), "default"
		}
	}
	if path == "" {
		return "", source, nil
	}
	files := []string{path}
	if source == "KUBECONFIG" {
		files = filepath.SplitList(path)
	}
	for _, f := range files {
		if st, statErr := os.Stat(f); f != "" && statErr == nil && !st.IsDir() {
			return path, source, nil
		}
	}
	if source == "--kubeconfig" {
		// An explicitly requested file that isn't there is a user error, not a
		// reason to silently fall back.
		return path, source, fmt.Errorf("kubeconfig %q not found", path)
	}
	// A missing default/$KUBECONFIG file falls through to in-cluster.
	return "", source, nil
}

// kubeconfigRules returns the client-go loading rules for a path from
// [locateKubeconfig]. A $KUBECONFIG list uses client-go's default precedence
// rules, which merge the files (the first file to set a value wins). Any other
// source is a single explicit file.
func kubeconfigRules(path, source string) *clientcmd.ClientConfigLoadingRules {
	if source == "KUBECONFIG" {
		return clientcmd.NewDefaultClientConfigLoadingRules()
	}
	return &clientcmd.ClientConfigLoadingRules{ExplicitPath: path}
}

// BuildMetricsClient builds a metrics-server (metrics.k8s.io) node-metrics
// lister for a selection already made by [ConnectKubeClientForCluster], so
// both clients talk to the same cluster. A config error is returned;
// metrics-server simply not being installed is NOT an error here — that
// surfaces at List time, so the utilization check can skip gracefully.
func BuildMetricsClient(sel KubeSelection) (NodeMetricsLister, error) {
	if sel.config == nil {
		return nil, fmt.Errorf("no verified Kubernetes config")
	}
	cs, err := metricsclient.NewForConfig(sel.config)
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
