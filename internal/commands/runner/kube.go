package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/fatih/color"
	"github.com/urfave/cli/v3"
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
	// KubeContext is an optional --kube-context. A context named explicitly is
	// trusted even when its server doesn't match the cluster endpoint
	// (proxied or tunnelled API servers); a one-line note says so.
	KubeContext string
	// Verbose prints diagnostics for an unreachable or unconfigured cluster
	// API, on stderr like every notice here. A cluster mismatch is always
	// reported.
	Verbose bool
	// SkipNote says what degrades when no client is returned, e.g.
	// "Workload/PDB checks will be skipped."
	SkipNote string
}

// KubeContextFlag is the --kube-context flag shared by every command that
// takes --kubeconfig.
func KubeContextFlag() *cli.StringFlag {
	return &cli.StringFlag{
		Name:  "kube-context",
		Usage: "Kubeconfig context for Kubernetes checks; trusted even if its server doesn't match the cluster endpoint (proxied or tunnelled API servers). Default: a context whose server matches the endpoint",
	}
}

// Seams for tests.
var (
	// kubeWarnOut, when set, replaces stderr for the notices below.
	kubeWarnOut io.Writer
	probeKube             = health.ProbeConnection
)

// kubeWarn returns where kube notices go: kubeWarnOut when a test set it,
// else os.Stderr as it is at call time.
func kubeWarn() io.Writer {
	if kubeWarnOut != nil {
		return kubeWarnOut
	}
	return os.Stderr
}

// kubeNotices dedupes cluster-mismatch and context notices, which several
// resolutions for the same cluster in one run would otherwise repeat. One set
// lives on the context of each CLI run (WithKubeNotices), so nothing carries
// over between runs in the same process.
type kubeNotices struct{ seen sync.Map }

type kubeNoticesKey struct{}

// WithKubeNotices returns ctx carrying a fresh notice-dedupe set for
// ResolveClusterKubeClient. Call it once per CLI run, on the root context.
func WithKubeNotices(ctx context.Context) context.Context {
	return context.WithValue(ctx, kubeNoticesKey{}, &kubeNotices{})
}

// noticeOnce reports whether the notice named key should print: the first
// time for ctx's run, and always when ctx carries no dedupe set.
func noticeOnce(ctx context.Context, key string) bool {
	n, ok := ctx.Value(kubeNoticesKey{}).(*kubeNotices)
	if !ok {
		return true
	}
	_, seen := n.seen.LoadOrStore(key, struct{}{})
	return !seen
}

// ResolveClusterKubeClient returns a Kubernetes client for req.Cluster, with
// connectivity probed. It compares each kubeconfig context's API server with
// the cluster endpoint from DescribeCluster and tries the matching contexts
// (current context first) until one is reachable. When none matches, it
// returns nil and warns on stderr (naming both servers and the
// update-kubeconfig command), so kube-dependent checks are skipped rather than
// run against the wrong cluster. The returned selection builds sibling
// clients (metrics) against the same cluster via health.BuildMetricsClient.
func ResolveClusterKubeClient(ctx context.Context, req KubeRequest) (kubernetes.Interface, health.KubeSelection) {
	target, err := health.DescribeTarget(ctx, req.API, req.Cluster, req.Region)
	if err != nil {
		if req.Verbose {
			_, _ = color.New(color.FgYellow).Fprintf(kubeWarn(), "Kubernetes checks unavailable: cannot verify the kubeconfig targets %s: %v\n",
				req.Cluster, awsinternal.FormatAWSError(err, "describing cluster"))
			if req.SkipNote != "" {
				_, _ = color.New(color.FgYellow).Fprintf(kubeWarn(), "%s\n", req.SkipNote)
			}
		}
		return nil, health.KubeSelection{Target: target}
	}

	client, sel, err := health.ConnectKubeClientForCluster(ctx, req.Kubeconfig, req.KubeContext, target, probeKube)
	if err != nil {
		var mm *health.ClusterMismatchError
		switch {
		case errors.As(err, &mm):
			if noticeOnce(ctx, "mismatch|"+target.Name+"|"+mm.Server) {
				warn := color.New(color.FgYellow)
				_, _ = warn.Fprintf(kubeWarn(), "Warning: skipping Kubernetes checks: %v\n", mm)
				if !mm.InCluster {
					_, _ = warn.Fprintf(kubeWarn(), "  To add a context for %s, run: %s (or pass --kube-context)\n",
						target.Name, target.UpdateKubeconfigHint())
				}
				if req.SkipNote != "" {
					_, _ = warn.Fprintf(kubeWarn(), "  %s\n", req.SkipNote)
				}
			}
		case health.IsProbeError(err):
			if req.Verbose {
				_, _ = color.New(color.FgYellow).Fprintf(kubeWarn(), "Kubernetes API unreachable via %s: %v\n", sel.Diag, err)
				if req.SkipNote != "" {
					_, _ = color.New(color.FgYellow).Fprintf(kubeWarn(), "%s\n", req.SkipNote)
				}
			}
		default:
			if req.Verbose {
				_, _ = color.New(color.FgYellow).Fprintf(kubeWarn(), "Kubernetes checks unavailable: %v (%s)\n", err, sel.Diag)
				if req.SkipNote != "" {
					_, _ = color.New(color.FgYellow).Fprintf(kubeWarn(), "%s\n", req.SkipNote)
				}
			}
		}
		return nil, sel
	}

	diag := sel.Diag
	switch {
	case diag.Unverified:
		if noticeOnce(ctx, "unverified|"+target.Name+"|"+diag.Context) {
			_, _ = fmt.Fprintf(kubeWarn(), "Note: using kubeconfig context %q (server %s) as requested; not verified as EKS cluster %s (%s)\n",
				diag.Context, diag.Server, target.Name, target.Endpoint)
		}
	case diag.SwitchedFrom != "" && req.Verbose:
		if noticeOnce(ctx, "switch|"+target.Name+"|"+diag.Context) {
			_, _ = fmt.Fprintf(kubeWarn(), "Using kubeconfig context %q for %s (current context %q points at another cluster)\n",
				diag.Context, target.Name, diag.SwitchedFrom)
		}
	}
	return client, sel
}
