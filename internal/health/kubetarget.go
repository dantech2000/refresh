package health

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/dantech2000/refresh/internal/services/common"
)

// TargetCluster identifies the EKS cluster a Kubernetes client must point at.
// Endpoint is the cluster's API server URL from DescribeCluster; it is what a
// kubeconfig context's server is compared against.
type TargetCluster struct {
	Name     string
	Region   string
	ARN      string
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

// DescribeTarget looks up the target cluster's API endpoint and ARN. The raw
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
		t.ARN = aws.ToString(out.Cluster.Arn)
		t.Endpoint = aws.ToString(out.Cluster.Endpoint)
	}
	return t, nil
}

// ClusterMismatchError reports that no usable kubeconfig context points at the
// target cluster. Callers must not use a Kubernetes client in this case.
type ClusterMismatchError struct {
	Target  TargetCluster
	Context string // kubeconfig context that was considered ("" for in-cluster)
	Server  string // that context's API server
}

func (e *ClusterMismatchError) Error() string {
	target := e.Target.Name
	if e.Target.Endpoint != "" {
		target = fmt.Sprintf("%s (%s)", e.Target.Name, e.Target.Endpoint)
	}
	var src string
	switch {
	case e.Context != "":
		src = fmt.Sprintf("kubeconfig context %q (server %s)", e.Context, e.Server)
	case e.Server != "":
		src = fmt.Sprintf("in-cluster config (server %s)", e.Server)
	default:
		src = "the resolved Kubernetes config"
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

// selectContextForEndpoint picks the kubeconfig context whose cluster server
// matches endpoint: the current context when it matches, otherwise the first
// matching context by name. ok is false when none match; the current context
// and its server are always returned for diagnostics.
func selectContextForEndpoint(raw clientcmdapi.Config, endpoint string) (ctxName, currentServer string, ok bool) {
	serverOf := func(name string) string {
		c := raw.Contexts[name]
		if c == nil {
			return ""
		}
		if cl := raw.Clusters[c.Cluster]; cl != nil {
			return cl.Server
		}
		return ""
	}
	currentServer = serverOf(raw.CurrentContext)
	if raw.CurrentContext != "" && SameClusterEndpoint(currentServer, endpoint) {
		return raw.CurrentContext, currentServer, true
	}
	names := make([]string, 0, len(raw.Contexts))
	for name := range raw.Contexts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if SameClusterEndpoint(serverOf(name), endpoint) {
			return name, currentServer, true
		}
	}
	return "", currentServer, false
}

// restConfigForTarget builds a rest.Config from the kubeconfig at path using
// the context that points at target. It returns a *ClusterMismatchError when no
// context does.
func restConfigForTarget(path string, diag *KubeDiag, target TargetCluster) (*rest.Config, error) {
	rules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: path}
	raw, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).RawConfig()
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig %s: %w", path, err)
	}
	diag.Context = raw.CurrentContext
	if target.Endpoint == "" {
		return nil, &ClusterMismatchError{Target: target, Context: raw.CurrentContext}
	}
	name, currentServer, ok := selectContextForEndpoint(raw, target.Endpoint)
	diag.Server = currentServer
	if !ok {
		return nil, &ClusterMismatchError{Target: target, Context: raw.CurrentContext, Server: currentServer}
	}
	if name != raw.CurrentContext {
		diag.SwitchedFrom = raw.CurrentContext
	}
	diag.Context = name
	diag.Server = target.Endpoint
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules,
		&clientcmd.ConfigOverrides{CurrentContext: name}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig %s (context %q): %w", path, name, err)
	}
	return cfg, nil
}
