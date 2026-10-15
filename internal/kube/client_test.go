package kube_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/dantech2000/refresh/internal/kube"
)

// The endpoint of the target, and the server of the contexts that point
// elsewhere. Connect makes no request, so no server listens on them.
const (
	endpoint = "https://ABC123.gr7.us-east-1.eks.amazonaws.com"
	other    = "https://OTHER.gr7.us-west-2.eks.amazonaws.com"
)

var target = kube.Target{Name: "prod", Region: "us-east-1", Endpoint: endpoint}

// writeKubeconfig writes a kubeconfig with the contexts in servers, keyed
// by context name, and the current context. The user of every context is
// an exec plugin.
func writeKubeconfig(t *testing.T, name, current string, servers map[string]string) string {
	t.Helper()
	cfg := clientcmdapi.NewConfig()
	for ctx, server := range servers {
		cfg.Clusters[ctx] = &clientcmdapi.Cluster{Server: server}
		cfg.AuthInfos[ctx] = &clientcmdapi.AuthInfo{Exec: &clientcmdapi.ExecConfig{
			Command: "aws", Args: []string{"eks", "get-token", "--cluster-name", ctx}, APIVersion: "client.authentication.k8s.io/v1beta1",
		}}
		cfg.Contexts[ctx] = &clientcmdapi.Context{Cluster: ctx, AuthInfo: ctx}
	}
	cfg.CurrentContext = current
	path := filepath.Join(t.TempDir(), name)
	if err := clientcmd.WriteToFile(*cfg, path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestConnect(t *testing.T) {
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "none"))
	tests := []struct {
		name    string
		current string
		servers map[string]string
		opts    kube.Options
		want    kube.Selection
		notice  string
	}{
		{
			name: "the current context matches", current: "prod", servers: map[string]string{"prod": endpoint, "staging": other},
			want: kube.Selection{Source: kube.SourceKubeconfig, Context: "prod", Server: endpoint},
		},
		{
			name: "another context matches", current: "staging", servers: map[string]string{"prod": endpoint, "staging": other},
			want:   kube.Selection{Source: kube.SourceKubeconfig, Context: "prod", Server: endpoint, SwitchedFrom: "staging"},
			notice: `using kubeconfig context "prod"; the current context "staging" points at another cluster`,
		},
		{
			name: "the match is by host and port", current: "prod", servers: map[string]string{"prod": strings.ToLower(endpoint) + ":443/"},
			want: kube.Selection{Source: kube.SourceKubeconfig, Context: "prod", Server: strings.ToLower(endpoint) + ":443/"},
		},
		{
			name: "a named context is trusted", current: "prod", servers: map[string]string{"prod": endpoint, "tunnel": "https://localhost:8443"},
			opts:   kube.Options{Context: "tunnel"},
			want:   kube.Selection{Source: kube.SourceNamedContext, Context: "tunnel", Server: "https://localhost:8443", Unverified: true},
			notice: `using kubeconfig context "tunnel" (server https://localhost:8443) as requested; its server is not the endpoint of the cluster`,
		},
		{
			name: "a named context that matches", current: "staging", servers: map[string]string{"prod": endpoint, "staging": other},
			opts: kube.Options{Context: "prod"},
			want: kube.Selection{Source: kube.SourceNamedContext, Context: "prod", Server: endpoint},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := tt.opts
			opts.Kubeconfig = writeKubeconfig(t, "config", tt.current, tt.servers)
			client, err := kube.Connect(target, opts)
			if err != nil {
				t.Fatal(err)
			}
			tt.want.Path = opts.Kubeconfig
			if diff := cmp.Diff(tt.want, client.Selection); diff != "" {
				t.Errorf("selection (-want +got):\n%s", diff)
			}
			if got := client.Selection.Notice(); got != tt.notice {
				t.Errorf("Notice() = %q, want %q", got, tt.notice)
			}
			if client.Clientset == nil || client.Metrics == nil {
				t.Error("a client is nil")
			}
		})
	}
}

func TestConnect_Errors(t *testing.T) {
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "none"))
	path := writeKubeconfig(t, "config", "staging", map[string]string{"staging": other})
	tests := []struct {
		name     string
		target   kube.Target
		opts     kube.Options
		mismatch bool
		noConfig bool
		want     string
	}{
		{
			name: "no context matches", target: target, opts: kube.Options{Kubeconfig: path}, mismatch: true,
			want: `kubeconfig context "staging" (server ` + other + `) does not point at EKS cluster prod (` + endpoint + `), and no other context does`,
		},
		{
			name: "the cluster has no endpoint", target: kube.Target{Name: "prod", Region: "us-east-1"}, opts: kube.Options{Kubeconfig: path}, mismatch: true,
			want: "no kubeconfig context can be matched to EKS cluster prod: the cluster has no API endpoint",
		},
		{
			name: "a named context that does not exist", target: target, opts: kube.Options{Kubeconfig: path, Context: "nope"},
			want: `kubeconfig context "nope" not found in ` + path,
		},
		{
			name: "an explicit path that does not exist", target: target, opts: kube.Options{Kubeconfig: filepath.Join(t.TempDir(), "missing")},
			want: "not found",
		},
		{
			name: "a named context with no kubeconfig", target: target, opts: kube.Options{Context: "prod", InClusterConfig: func() (*rest.Config, error) { return &rest.Config{Host: endpoint}, nil }},
			want: `--kube-context "prod" given, but no kubeconfig found`,
		},
		{
			name: "no kubeconfig and no in-cluster config", target: target,
			opts: kube.Options{InClusterConfig: func() (*rest.Config, error) { return nil, errors.New("not in a pod") }},
			want: "no kubeconfig found, and no in-cluster config: not in a pod", noConfig: true,
		},
		{
			name: "in-cluster config for another cluster", target: target, mismatch: true,
			opts: kube.Options{InClusterConfig: func() (*rest.Config, error) { return &rest.Config{Host: "https://10.100.0.1:443"}, nil }},
			want: "the in-cluster config (server https://10.100.0.1:443) is not known to be EKS cluster prod; set REFRESH_IN_CLUSTER_NAME=prod if this pod runs in it",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := kube.Connect(tt.target, tt.opts)
			if err == nil {
				t.Fatal("no error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to contain %q", err, tt.want)
			}
			var mm *kube.MismatchError
			if got := errors.As(err, &mm); got != tt.mismatch {
				t.Errorf("is a MismatchError = %v, want %v", got, tt.mismatch)
			}
			if got := errors.Is(err, kube.ErrNoConfig); got != tt.noConfig {
				t.Errorf("is ErrNoConfig = %v, want %v", got, tt.noConfig)
			}
			if tt.mismatch && !mm.InCluster && !strings.Contains(mm.Hint(), "aws eks update-kubeconfig --name prod --region us-east-1") {
				t.Errorf("Hint() = %q", mm.Hint())
			}
		})
	}
}

func TestConnect_InCluster(t *testing.T) {
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "none"))
	inCluster := func() (*rest.Config, error) { return &rest.Config{Host: "https://10.100.0.1:443"}, nil }
	// The host of the pod is the service IP, which no endpoint matches;
	// the name of the cluster in the environment is what vouches for it.
	client, err := kube.Connect(target, kube.Options{InClusterConfig: inCluster, InClusterName: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	want := kube.Selection{Source: kube.SourceInCluster, Server: "https://10.100.0.1:443"}
	if diff := cmp.Diff(want, client.Selection); diff != "" {
		t.Errorf("selection (-want +got):\n%s", diff)
	}
	matching := func() (*rest.Config, error) { return &rest.Config{Host: endpoint}, nil }
	if _, err := kube.Connect(target, kube.Options{InClusterConfig: matching}); err != nil {
		t.Errorf("an in-cluster host that matches the endpoint: %v", err)
	}
}

// $KUBECONFIG is a list; a file of the list that does not exist is
// skipped, and the files are merged.
func TestConnect_KubeconfigList(t *testing.T) {
	first := writeKubeconfig(t, "first", "staging", map[string]string{"staging": other})
	second := writeKubeconfig(t, "second", "", map[string]string{"prod": endpoint})
	t.Setenv("KUBECONFIG", strings.Join([]string{filepath.Join(t.TempDir(), "missing"), first, second}, string(os.PathListSeparator)))
	client, err := kube.Connect(target, kube.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if client.Selection.Context != "prod" || client.Selection.SwitchedFrom != "staging" {
		t.Errorf("selection = %+v, want the prod context of the second file", client.Selection)
	}
}

func TestNormalizeEndpoint(t *testing.T) {
	tests := []struct{ in, want string }{
		{"https://ABC.gr7.us-east-1.eks.amazonaws.com", "abc.gr7.us-east-1.eks.amazonaws.com:443"},
		{"https://abc.gr7.us-east-1.eks.amazonaws.com.:443/", "abc.gr7.us-east-1.eks.amazonaws.com:443"},
		{"abc.gr7.us-east-1.eks.amazonaws.com", "abc.gr7.us-east-1.eks.amazonaws.com:443"},
		{"http://localhost:8080", "localhost:8080"},
		{"http://localhost", "localhost:80"},
		{"https://[::1]:6443", "[::1]:6443"},
		{"", ""},
		{"https://", ""},
	}
	for _, tt := range tests {
		if got := kube.NormalizeEndpoint(tt.in); got != tt.want {
			t.Errorf("NormalizeEndpoint(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	if !kube.SameEndpoint("https://ABC.gr7.us-east-1.eks.amazonaws.com/", "abc.gr7.us-east-1.eks.amazonaws.com:443") {
		t.Error("SameEndpoint should ignore case, the scheme, and the path")
	}
	if kube.SameEndpoint("", "") {
		t.Error("two empty endpoints are not the same server")
	}
}

func TestCheckContext(t *testing.T) {
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "none"))
	path := writeKubeconfig(t, "config", "prod", map[string]string{"prod": endpoint})
	if err := kube.CheckContext(path, ""); err != nil {
		t.Errorf("no context: %v", err)
	}
	if err := kube.CheckContext(path, "prod"); err != nil {
		t.Errorf("a context of the file: %v", err)
	}
	if err := kube.CheckContext(path, "pord"); err == nil || !strings.Contains(err.Error(), `context "pord" not found in `+path) {
		t.Errorf("a missing context: %v", err)
	}
	if err := kube.CheckContext("", "prod"); err == nil || !strings.Contains(err.Error(), "no kubeconfig found") {
		t.Errorf("no kubeconfig: %v", err)
	}
	if err := kube.CheckContext(filepath.Join(t.TempDir(), "missing"), "prod"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("a missing file: %v", err)
	}
}
