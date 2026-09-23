package health

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/dantech2000/refresh/internal/services/common"
)

// InClusterNameEnv names the env var a pod sets to declare which EKS cluster it
// runs in. In-cluster config points at the kubernetes service IP, which can't
// be matched to an EKS endpoint, so it is only trusted when this equals the
// target cluster name.
const InClusterNameEnv = "REFRESH_IN_CLUSTER_NAME"

// TargetCluster identifies the EKS cluster a Kubernetes client must point at.
// Endpoint is the cluster's API server URL from DescribeCluster; it is what a
// kubeconfig context's server is compared against.
type TargetCluster struct {
	Name     string
	Region   string
	Endpoint string
}

// UpdateKubeconfigHint is the command that adds (and selects) a kubeconfig
// context for the target cluster.
func (t TargetCluster) UpdateKubeconfigHint() string {
	hint := "aws eks update-kubeconfig --name " + t.Name
	if t.Region != "" {
		hint += " --region " + t.Region
	}
	return hint
}

// ClusterDescriber is the slice of the EKS API needed to look up a cluster's
// endpoint.
type ClusterDescriber interface {
	DescribeCluster(ctx context.Context, params *eks.DescribeClusterInput, optFns ...func(*eks.Options)) (*eks.DescribeClusterOutput, error)
}

// DescribeTarget looks up the target cluster's API endpoint. The raw
// AWS error is returned so the caller can format it (the health package can't
// import internal/aws without a cycle).
func DescribeTarget(ctx context.Context, api ClusterDescriber, name, region string) (TargetCluster, error) {
	out, err := common.WithRetry(ctx, common.DefaultRetryConfig,
		func(rc context.Context) (*eks.DescribeClusterOutput, error) {
			return api.DescribeCluster(rc, &eks.DescribeClusterInput{Name: aws.String(name)})
		})
	if err != nil {
		return TargetCluster{}, err
	}
	t := TargetCluster{Name: name, Region: region}
	if out != nil && out.Cluster != nil {
		t.Endpoint = aws.ToString(out.Cluster.Endpoint)
	}
	return t, nil
}

// ClusterMismatchError reports that no usable Kubernetes config points at the
// target cluster. Callers must not use a Kubernetes client in this case.
type ClusterMismatchError struct {
	Target    TargetCluster
	Context   string // kubeconfig context that was considered
	Server    string // that context's (or the in-cluster) API server
	InCluster bool
}

func (e *ClusterMismatchError) Error() string {
	target := e.Target.Name
	if e.Target.Endpoint != "" {
		target = fmt.Sprintf("%s (%s)", e.Target.Name, e.Target.Endpoint)
	}
	if e.InCluster {
		return fmt.Sprintf("in-cluster config (server %s) cannot be verified as EKS cluster %s; set %s=%s if this pod runs in that cluster",
			e.Server, target, InClusterNameEnv, e.Target.Name)
	}
	src := "the resolved Kubernetes config"
	if e.Context != "" {
		src = fmt.Sprintf("kubeconfig context %q (server %s)", e.Context, e.Server)
	}
	if e.Target.Endpoint == "" {
		return fmt.Sprintf("cannot verify that %s points at EKS cluster %s: the cluster has no API endpoint", src, target)
	}
	return fmt.Sprintf("%s does not point at EKS cluster %s, and no other kubeconfig context does", src, target)
}

// NormalizeEndpoint reduces an API server URL to a comparable "host:port"
// form: the host is lowercased, a trailing dot, path and trailing slash are
// dropped, and the port defaults to 443. A bare host (no scheme) is accepted.
// It returns "" when the input has no host.
func NormalizeEndpoint(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return ""
	}
	port := u.Port()
	if port == "" {
		if strings.EqualFold(u.Scheme, "http") {
			port = "80"
		} else {
			port = "443"
		}
	}
	return net.JoinHostPort(host, port)
}

// SameClusterEndpoint reports whether a kube API server URL and an EKS cluster
// endpoint refer to the same API server.
func SameClusterEndpoint(server, endpoint string) bool {
	a, b := NormalizeEndpoint(server), NormalizeEndpoint(endpoint)
	return a != "" && a == b
}

// contextServer returns the API server of a kubeconfig context.
func contextServer(raw clientcmdapi.Config, name string) string {
	c := raw.Contexts[name]
	if c == nil {
		return ""
	}
	if cl := raw.Clusters[c.Cluster]; cl != nil {
		return cl.Server
	}
	return ""
}

// matchingContexts lists every kubeconfig context whose server matches
// endpoint: the current context first when it matches, then the rest by name.
func matchingContexts(raw clientcmdapi.Config, endpoint string) []string {
	var out []string
	if raw.CurrentContext != "" && SameClusterEndpoint(contextServer(raw, raw.CurrentContext), endpoint) {
		out = append(out, raw.CurrentContext)
	}
	names := make([]string, 0, len(raw.Contexts))
	for name := range raw.Contexts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if name != raw.CurrentContext && SameClusterEndpoint(contextServer(raw, name), endpoint) {
			out = append(out, name)
		}
	}
	return out
}

// kubeCandidate is one config to try for the target cluster.
type kubeCandidate struct {
	cfg  *rest.Config
	diag KubeDiag
}

// targetCandidates lists the configs that may be used for target, in the
// order to try them. kubeContext, when set, is the only candidate and is
// trusted even if its server doesn't match (proxied/tunnelled API servers).
// It returns a *ClusterMismatchError when nothing can be verified.
func targetCandidates(kubeconfigPath, kubeContext string, target TargetCluster) ([]kubeCandidate, KubeDiag, error) {
	path, source, err := locateKubeconfig(kubeconfigPath)
	if err != nil {
		return nil, KubeDiag{Source: source, Path: path}, err
	}
	kubeContext = strings.TrimSpace(kubeContext)

	if path == "" {
		if kubeContext != "" {
			return nil, KubeDiag{Source: "none"}, fmt.Errorf("--kube-context %q given but no kubeconfig found", kubeContext)
		}
		icCfg, icErr := inClusterConfig()
		if icErr != nil {
			return nil, KubeDiag{Source: "none"}, fmt.Errorf("no kubeconfig found and in-cluster config not available")
		}
		diag := KubeDiag{Source: "in-cluster", Server: icCfg.Host}
		if SameClusterEndpoint(icCfg.Host, target.Endpoint) ||
			(target.Name != "" && strings.TrimSpace(os.Getenv(InClusterNameEnv)) == target.Name) {
			return []kubeCandidate{{cfg: icCfg, diag: diag}}, diag, nil
		}
		return nil, diag, &ClusterMismatchError{Target: target, Server: icCfg.Host, InCluster: true}
	}

	rules := kubeconfigRules(path, source)
	raw, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).RawConfig()
	base := KubeDiag{Source: source, Path: path}
	if err != nil {
		return nil, base, fmt.Errorf("loading kubeconfig %s: %w", path, err)
	}
	base.Context = raw.CurrentContext
	base.Server = contextServer(raw, raw.CurrentContext)

	build := func(name string) (*rest.Config, error) {
		cfg, berr := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules,
			&clientcmd.ConfigOverrides{CurrentContext: name}).ClientConfig()
		if berr != nil {
			return nil, fmt.Errorf("loading kubeconfig %s (context %q): %w", path, name, berr)
		}
		return cfg, nil
	}

	if kubeContext != "" {
		if raw.Contexts[kubeContext] == nil {
			return nil, base, fmt.Errorf("kubeconfig context %q not found in %s", kubeContext, path)
		}
		diag := base
		diag.Context = kubeContext
		diag.Server = contextServer(raw, kubeContext)
		diag.Unverified = !SameClusterEndpoint(diag.Server, target.Endpoint)
		cfg, berr := build(kubeContext)
		if berr != nil {
			return nil, diag, berr
		}
		return []kubeCandidate{{cfg: cfg, diag: diag}}, diag, nil
	}

	if target.Endpoint == "" {
		return nil, base, &ClusterMismatchError{Target: target, Context: raw.CurrentContext}
	}
	names := matchingContexts(raw, target.Endpoint)
	if len(names) == 0 {
		return nil, base, &ClusterMismatchError{Target: target, Context: raw.CurrentContext, Server: base.Server}
	}
	var cands []kubeCandidate
	var lastErr error
	for _, name := range names {
		cfg, berr := build(name)
		if berr != nil {
			lastErr = berr
			continue
		}
		diag := base
		diag.Context = name
		diag.Server = contextServer(raw, name)
		if name != raw.CurrentContext {
			diag.SwitchedFrom = raw.CurrentContext
		}
		cands = append(cands, kubeCandidate{cfg: cfg, diag: diag})
	}
	if len(cands) == 0 {
		return nil, base, lastErr
	}
	return cands, cands[0].diag, nil
}

// KubeSelection is the verified config chosen by [ConnectKubeClientForCluster].
// Pass it to [BuildMetricsClient] so sibling clients use the same cluster.
type KubeSelection struct {
	Target TargetCluster
	Diag   KubeDiag
	config *rest.Config
}

// ProbeFunc checks that a client can reach its API server.
type ProbeFunc func(context.Context, kubernetes.Interface) error

// ConnectKubeClientForCluster returns a Kubernetes client that points at
// target. With kubeContext set, that context is used as the user asked (the
// selection's Diag.Unverified reports a server mismatch). Otherwise the
// kubeconfig contexts whose server matches the target endpoint are tried in
// order (current context first) until one passes probe; a nil probe accepts
// the first. In-cluster config is used only when its host matches the
// endpoint or $REFRESH_IN_CLUSTER_NAME names the target cluster. When nothing
// can be verified a *ClusterMismatchError is returned and no client is built.
func ConnectKubeClientForCluster(ctx context.Context, kubeconfigPath, kubeContext string, target TargetCluster, probe ProbeFunc) (kubernetes.Interface, KubeSelection, error) {
	cands, diag, err := targetCandidates(kubeconfigPath, kubeContext, target)
	if err != nil {
		return nil, KubeSelection{Target: target, Diag: diag}, err
	}
	var lastErr error
	for _, c := range cands {
		client, nerr := kubernetes.NewForConfig(c.cfg)
		if nerr != nil {
			lastErr, diag = fmt.Errorf("building kubernetes client: %w", nerr), c.diag
			continue
		}
		if probe != nil {
			if perr := probe(ctx, client); perr != nil {
				lastErr, diag = &ProbeError{Err: perr}, c.diag
				continue
			}
		}
		return client, KubeSelection{Target: target, Diag: c.diag, config: c.cfg}, nil
	}
	return nil, KubeSelection{Target: target, Diag: diag}, lastErr
}

// ProbeError reports that a verified config's API server was unreachable.
type ProbeError struct{ Err error }

func (e *ProbeError) Error() string { return e.Err.Error() }
func (e *ProbeError) Unwrap() error { return e.Err }

// IsProbeError reports whether err came from the connectivity probe.
func IsProbeError(err error) bool {
	var pe *ProbeError
	return errors.As(err, &pe)
}
