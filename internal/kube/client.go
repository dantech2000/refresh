package kube

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
	metricsv1beta1 "k8s.io/metrics/pkg/client/clientset/versioned/typed/metrics/v1beta1"
)

// Target is the EKS cluster that a client must point at.
type Target struct {
	Name, Region string
	// Endpoint is the URL of the API server, from DescribeCluster. "" when
	// the cluster has none.
	Endpoint string
}

// UpdateKubeconfigHint is the command that adds a kubeconfig context for
// the target.
func (t Target) UpdateKubeconfigHint() string {
	hint := "aws eks update-kubeconfig --name " + t.Name
	if t.Region != "" {
		hint += " --region " + t.Region
	}
	return hint
}

// Options say how to find the config of a target.
type Options struct {
	// Kubeconfig is --kubeconfig. "" means $KUBECONFIG, then ~/.kube/config.
	Kubeconfig string
	// Context is --kube-context. It is trusted as named: its server need
	// not match the endpoint.
	Context string
	// InClusterName is $REFRESH_IN_CLUSTER_NAME. The command reads the
	// environment; this package does not.
	InClusterName string
	// RequestTimeout bounds each request. Zero means 30 seconds.
	RequestTimeout time.Duration
	// InClusterConfig returns the config of the pod that refresh runs in.
	// Nil means rest.InClusterConfig. A test sets it.
	InClusterConfig func() (*rest.Config, error)
}

// Source says where a config came from.
type Source string

// The sources of a config.
const (
	// SourceKubeconfig: a kubeconfig context whose server matches the
	// endpoint of the target.
	SourceKubeconfig Source = "Kubeconfig"
	// SourceNamedContext: the context of --kube-context.
	SourceNamedContext Source = "NamedContext"
	// SourceInCluster: the service account of the pod.
	SourceInCluster Source = "InCluster"
)

// Selection says which config was chosen.
type Selection struct {
	Source Source
	// Path is the kubeconfig file, or the list of $KUBECONFIG.
	Path    string
	Context string
	Server  string
	// SwitchedFrom is the current context when another one was chosen,
	// because the current one points at another cluster.
	SwitchedFrom string
	// Unverified is set for a --kube-context whose server does not match
	// the endpoint of the target, such as a proxied API server.
	Unverified bool
}

// Notice is the one line that a command prints about the selection, or
// "" when there is nothing to say: the context is the current one, or the
// pod's own.
func (s Selection) Notice() string {
	switch {
	case s.Unverified:
		return fmt.Sprintf("using kubeconfig context %q (server %s) as requested; its server is not the endpoint of the cluster", s.Context, s.Server)
	case s.SwitchedFrom != "":
		return fmt.Sprintf("using kubeconfig context %q; the current context %q points at another cluster", s.Context, s.SwitchedFrom)
	}
	return ""
}

// Client is a Kubernetes client for one target.
type Client struct {
	Clientset kubernetes.Interface
	// Metrics is the node lister of metrics.k8s.io, on the same config.
	Metrics   metricsv1beta1.NodeMetricsInterface
	Selection Selection
}

// ErrNoConfig says that there is no kubeconfig and no in-cluster config:
// nothing to match the target against. A command treats it as it treats a
// MismatchError: a warning, and no Kubernetes.
var ErrNoConfig = errors.New("no kubeconfig found, and no in-cluster config")

// MismatchError says that no config points at the target. A command
// prints it as a warning, with Hint, and goes on without Kubernetes.
type MismatchError struct {
	Target Target
	// Context and Server are the current kubeconfig context and its
	// server, when there is a kubeconfig.
	Context string
	Server  string
	// InCluster is set when the pod's own config was the only candidate.
	InCluster bool
}

func (e *MismatchError) Error() string {
	switch {
	case e.InCluster:
		return fmt.Sprintf("the in-cluster config (server %s) is not known to be EKS cluster %s; set REFRESH_IN_CLUSTER_NAME=%s if this pod runs in it",
			e.Server, e.Target.Name, e.Target.Name)
	case e.Target.Endpoint == "":
		return fmt.Sprintf("no kubeconfig context can be matched to EKS cluster %s: the cluster has no API endpoint", e.Target.Name)
	case e.Context != "":
		return fmt.Sprintf("kubeconfig context %q (server %s) does not point at EKS cluster %s (%s), and no other context does",
			e.Context, e.Server, e.Target.Name, e.Target.Endpoint)
	}
	return fmt.Sprintf("no kubeconfig context points at EKS cluster %s (%s)", e.Target.Name, e.Target.Endpoint)
}

// Hint says how to get a context for the target.
func (e *MismatchError) Hint() string {
	if e.InCluster {
		return ""
	}
	return "add one with: " + e.Target.UpdateKubeconfigHint() + ", or pass --kube-context"
}

// defaultRequestTimeout bounds one request when Options sets no timeout.
const defaultRequestTimeout = 30 * time.Second

// dialTimeout bounds the connection to an API server.
const dialTimeout = 10 * time.Second

// clientQPS and clientBurst pace the requests of one client.
const (
	clientQPS   = 50
	clientBurst = 100
)

// Connect returns a client for target. It makes no request. The config is,
// in order:
//
//  1. The context of Options.Context, trusted as named. It is an error
//     when the context is not in the kubeconfig, or when there is no
//     kubeconfig at all.
//  2. The kubeconfig context whose server matches the endpoint of the
//     target: the current context first, then the others by name.
//  3. With no kubeconfig file, the in-cluster config, when its host
//     matches the endpoint or Options.InClusterName is the target.
//
// Anything else is a *MismatchError.
func Connect(target Target, opts Options) (*Client, error) {
	path, source, err := locate(opts.Kubeconfig)
	if err != nil {
		return nil, err
	}
	var (
		cfg *rest.Config
		sel Selection
	)
	if path == "" {
		cfg, sel, err = inCluster(target, opts)
	} else {
		cfg, sel, err = fromKubeconfig(path, source, target, opts.Context)
	}
	if err != nil {
		return nil, err
	}
	return newClient(cfg, sel, opts)
}

// CheckContext returns an error when name, the value of --kube-context,
// is not a context of the kubeconfig, or when there is no kubeconfig. A
// command calls it before any AWS call, so that a typing error in the
// flag stops the run at once. An empty name needs no check.
func CheckContext(kubeconfig, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	path, source, err := locate(kubeconfig)
	if err != nil {
		return err
	}
	if path == "" {
		return fmt.Errorf("--kube-context %q given, but no kubeconfig found", name)
	}
	raw, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules(path, source), &clientcmd.ConfigOverrides{}).RawConfig()
	if err != nil {
		return fmt.Errorf("loading kubeconfig %s: %w", path, err)
	}
	if raw.Contexts[name] == nil {
		return fmt.Errorf("kubeconfig context %q not found in %s", name, path)
	}
	return nil
}

// locate returns the kubeconfig to load and where it was named:
// --kubeconfig, $KUBECONFIG (a list, used when one of its files exists),
// or ~/.kube/config. path is "" when no file exists, which falls through
// to the in-cluster config. An explicit path that does not exist is an
// error.
func locate(explicit string) (path, source string, err error) {
	path = strings.TrimSpace(explicit)
	switch {
	case path != "":
		source = "--kubeconfig"
	case os.Getenv(clientcmd.RecommendedConfigPathEnvVar) != "":
		path, source = os.Getenv(clientcmd.RecommendedConfigPathEnvVar), clientcmd.RecommendedConfigPathEnvVar
	default:
		if home, herr := os.UserHomeDir(); herr == nil {
			path, source = filepath.Join(home, clientcmd.RecommendedHomeDir, clientcmd.RecommendedFileName), "default"
		}
	}
	if path == "" {
		return "", source, nil
	}
	files := []string{path}
	if source == clientcmd.RecommendedConfigPathEnvVar {
		files = filepath.SplitList(path)
	}
	for _, f := range files {
		if st, statErr := os.Stat(f); f != "" && statErr == nil && !st.IsDir() {
			return path, source, nil
		}
	}
	if source == "--kubeconfig" {
		return path, source, fmt.Errorf("kubeconfig %q not found", path)
	}
	return "", source, nil
}

// loadingRules returns the client-go rules for a path from locate. A
// $KUBECONFIG list uses the default rules, which merge its files.
func loadingRules(path, source string) *clientcmd.ClientConfigLoadingRules {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	rules.WarnIfAllMissing = false
	if source != clientcmd.RecommendedConfigPathEnvVar {
		rules.ExplicitPath = path
	}
	return rules
}

// fromKubeconfig chooses the context of a kubeconfig for target.
func fromKubeconfig(path, source string, target Target, named string) (*rest.Config, Selection, error) {
	rules := loadingRules(path, source)
	raw, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).RawConfig()
	if err != nil {
		return nil, Selection{}, fmt.Errorf("loading kubeconfig %s: %w", path, err)
	}
	build := func(name string) (*rest.Config, error) {
		cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: name}).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("loading kubeconfig %s (context %q): %w", path, name, err)
		}
		return cfg, nil
	}
	if named = strings.TrimSpace(named); named != "" {
		if raw.Contexts[named] == nil {
			return nil, Selection{}, fmt.Errorf("kubeconfig context %q not found in %s", named, path)
		}
		cfg, err := build(named)
		if err != nil {
			return nil, Selection{}, err
		}
		server := contextServer(raw, named)
		return cfg, Selection{
			Source: SourceNamedContext, Path: path, Context: named, Server: server,
			Unverified: !SameEndpoint(server, target.Endpoint),
		}, nil
	}
	current := raw.CurrentContext
	if target.Endpoint == "" {
		return nil, Selection{}, &MismatchError{Target: target, Context: current}
	}
	for _, name := range matchingContexts(raw, target.Endpoint) {
		cfg, err := build(name)
		if err != nil {
			// A context whose config does not build is not the one to
			// use; the next match may be.
			continue
		}
		sel := Selection{Source: SourceKubeconfig, Path: path, Context: name, Server: contextServer(raw, name)}
		if name != current {
			sel.SwitchedFrom = current
		}
		return cfg, sel, nil
	}
	return nil, Selection{}, &MismatchError{Target: target, Context: current, Server: contextServer(raw, current)}
}

// inCluster chooses the pod's own config for target.
func inCluster(target Target, opts Options) (*rest.Config, Selection, error) {
	if strings.TrimSpace(opts.Context) != "" {
		return nil, Selection{}, fmt.Errorf("--kube-context %q given, but no kubeconfig found", opts.Context)
	}
	load := opts.InClusterConfig
	if load == nil {
		load = rest.InClusterConfig
	}
	cfg, err := load()
	if err != nil {
		return nil, Selection{}, fmt.Errorf("%w: %w", ErrNoConfig, err)
	}
	sel := Selection{Source: SourceInCluster, Server: cfg.Host}
	if SameEndpoint(cfg.Host, target.Endpoint) || (target.Name != "" && strings.TrimSpace(opts.InClusterName) == target.Name) {
		return cfg, sel, nil
	}
	return nil, sel, &MismatchError{Target: target, Server: cfg.Host, InCluster: true}
}

// newClient builds the clientset and the metrics client of a config, with
// the timeouts and the exec settings of this package.
func newClient(cfg *rest.Config, sel Selection, opts Options) (*Client, error) {
	cfg.Timeout = opts.RequestTimeout
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultRequestTimeout
	}
	dialer := &net.Dialer{Timeout: dialTimeout}
	cfg.Dial = dialer.DialContext
	// A command makes a short burst of lists, at most a few dozen at
	// once, and nothing after. client-go's default of 5 requests a second
	// with a burst of 10 would hold the later lists of a gate for seconds;
	// the API server's own fairness bounds a burst it does not like.
	cfg.QPS, cfg.Burst = clientQPS, clientBurst
	if cfg.ExecProvider != nil {
		// A credential plugin never reads the terminal: a prompt of the
		// command cannot race it for stdin.
		cfg.ExecProvider.StdinUnavailable = true
		cfg.ExecProvider.InteractiveMode = clientcmdapi.NeverExecInteractiveMode
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("building the Kubernetes client: %w", err)
	}
	metrics, err := metricsclient.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("building the metrics client: %w", err)
	}
	return &Client{Clientset: clientset, Metrics: metrics.MetricsV1beta1().NodeMetricses(), Selection: sel}, nil
}

// contextServer returns the server of a kubeconfig context, or "".
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

// matchingContexts lists the contexts whose server matches endpoint: the
// current context first when it matches, then the rest by name.
func matchingContexts(raw clientcmdapi.Config, endpoint string) []string {
	var out []string
	if raw.CurrentContext != "" && SameEndpoint(contextServer(raw, raw.CurrentContext), endpoint) {
		out = append(out, raw.CurrentContext)
	}
	for _, name := range slices.Sorted(mapKeys(raw.Contexts)) {
		if name != raw.CurrentContext && SameEndpoint(contextServer(raw, name), endpoint) {
			out = append(out, name)
		}
	}
	return out
}

func mapKeys[V any](m map[string]V) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// NormalizeEndpoint reduces an API server URL to a comparable "host:port":
// the host in lower case without a trailing dot, and the port, 443 when
// the URL names none. A bare host is accepted. It returns "" for a URL
// with no host.
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
		port = "443"
		if strings.EqualFold(u.Scheme, "http") {
			port = "80"
		}
	}
	return net.JoinHostPort(host, port)
}

// SameEndpoint reports whether a kubeconfig server and an EKS endpoint
// name the same API server.
func SameEndpoint(server, endpoint string) bool {
	a, b := NormalizeEndpoint(server), NormalizeEndpoint(endpoint)
	return a != "" && a == b
}
