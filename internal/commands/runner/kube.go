package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/fatih/color"
	"k8s.io/client-go/kubernetes"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/health"
)

// KubeRequest describes the EKS cluster a Kubernetes client is needed for.
type KubeRequest struct {
	API        health.ClusterDescriber // used to look up the cluster endpoint
	Cluster    string
	Region     string
	Kubeconfig string // optional --kubeconfig path
	// Verbose prints diagnostics for an unreachable or unconfigured cluster
	// API. A cluster mismatch is always reported on stderr.
	Verbose bool
	// SkipNote says what degrades when no client is returned, e.g.
	// "Workload/PDB checks will be skipped."
	SkipNote string
}

// Seams for tests.
var (
	kubeWarnOut io.Writer = os.Stderr
	probeKube             = health.ProbeConnection
)

// kubeNotices dedupes cluster-mismatch and context-switch notices, which
// several resolutions for the same cluster in one run would otherwise repeat.
var kubeNotices sync.Map

func noticeOnce(key string) bool {
	_, seen := kubeNotices.LoadOrStore(key, struct{}{})
	return !seen
}

// ResolveClusterKubeClient returns a Kubernetes client verified to point at
// req.Cluster, with connectivity probed. It compares the kubeconfig context's
// API server with the cluster endpoint from DescribeCluster; when the current
// context points at another cluster, a context that matches is used instead.
// When none matches, it returns nil and warns on stderr (naming both servers
// and the update-kubeconfig command), so kube-dependent checks are skipped
// rather than run against the wrong cluster. The returned target is the
// resolved cluster, for building sibling clients (e.g. metrics) the same way.
func ResolveClusterKubeClient(ctx context.Context, req KubeRequest) (kubernetes.Interface, health.TargetCluster) {
	target, err := health.DescribeTarget(ctx, req.API, req.Cluster, req.Region)
	if err != nil {
		if req.Verbose {
			color.Yellow("Kubernetes checks unavailable: cannot verify the kubeconfig targets %s: %v",
				req.Cluster, awsinternal.FormatAWSError(err, "describing cluster"))
			if req.SkipNote != "" {
				color.Yellow("%s", req.SkipNote)
			}
		}
		return nil, target
	}

	client, diag, err := health.BuildKubeClientForCluster(req.Kubeconfig, target)
	if err != nil {
		var mm *health.ClusterMismatchError
		if errors.As(err, &mm) {
			if noticeOnce("mismatch|" + target.Name + "|" + mm.Server) {
				warn := color.New(color.FgYellow)
				_, _ = warn.Fprintf(kubeWarnOut, "Warning: skipping Kubernetes checks: %v\n", mm)
				_, _ = warn.Fprintf(kubeWarnOut, "  To add a context for %s, run: %s\n", target.Name, target.UpdateKubeconfigHint())
				if req.SkipNote != "" {
					_, _ = warn.Fprintf(kubeWarnOut, "  %s\n", req.SkipNote)
				}
			}
			return nil, target
		}
		if req.Verbose {
			color.Yellow("Kubernetes checks unavailable: %v (%s)", err, diag)
			if req.SkipNote != "" {
				color.Yellow("%s", req.SkipNote)
			}
		}
		return nil, target
	}
	if diag.Unverified && noticeOnce("unverified|"+target.Name) {
		_, _ = color.New(color.FgYellow).Fprintf(kubeWarnOut,
			"Warning: using in-cluster Kubernetes config (server %s); could not verify it is EKS cluster %s (%s)\n",
			diag.Server, target.Name, target.Endpoint)
	}
	if diag.SwitchedFrom != "" && req.Verbose && noticeOnce("switch|"+target.Name+"|"+diag.Context) {
		_, _ = fmt.Fprintf(kubeWarnOut, "Using kubeconfig context %q for %s (current context %q points at another cluster)\n",
			diag.Context, target.Name, diag.SwitchedFrom)
	}
	if probeErr := probeKube(ctx, client); probeErr != nil {
		if req.Verbose {
			color.Yellow("Kubernetes API unreachable via %s: %v", diag, probeErr)
			if req.SkipNote != "" {
				color.Yellow("%s", req.SkipNote)
			}
		}
		return nil, target
	}
	return client, target
}
