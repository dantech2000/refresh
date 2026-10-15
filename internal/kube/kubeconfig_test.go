package kube_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dantech2000/refresh/internal/kube"
)

const kubeconfig = `apiVersion: v1
kind: Config
current-context: by-arn
clusters:
- name: arn:aws:eks:us-east-1:123456789012:cluster/prod-arn
  cluster: {server: https://ABC.gr7.us-east-1.eks.amazonaws.com}
- name: kube-name
  cluster: {server: https://DEF.gr7.us-east-1.eks.amazonaws.com}
- name: prod-eksctl.us-west-2.eksctl.io
  cluster: {server: https://GHI.gr7.us-west-2.eks.amazonaws.com}
- name: plain_name-1
  cluster: {server: https://example.invalid}
- name: not/a/name
  cluster: {server: https://example.invalid}
- name: sydney
  cluster: {server: https://JKL.sk1.ap-southeast-2.eks.amazonaws.com}
- name: china
  cluster: {server: https://MNO.sk1.cn-north-1.eks.amazonaws.com.cn}
- name: dual-stack
  cluster: {server: https://PQR.gr7.eu-south-1.api.aws}
- name: kind-local
  cluster: {server: https://127.0.0.1:6443}
contexts:
- name: arn:aws:eks:eu-west-1:123456789012:cluster/prod-context-arn
  context: {cluster: kube-name, user: none}
- name: by-exec-other-region
  context: {cluster: sydney, user: aws-cli}
- name: by-exec-no-eks-server
  context: {cluster: plain_name-1, user: aws-cli}
- name: by-exec-env
  context: {cluster: plain_name-1, user: aws-cli-env}
- name: by-exec-no-region
  context: {cluster: plain_name-1, user: aws-cli-equals}
- name: china
  context: {cluster: china, user: authenticator}
- name: dual-stack
  context: {cluster: dual-stack, user: aws-cli-equals}
- name: outposts
  context: {cluster: kube-name, user: outposts}
- name: self-managed
  context: {cluster: plain_name-1, user: authenticator}
- name: kind
  context: {cluster: kind-local, user: none}
- name: eks-server-no-name
  context: {cluster: sydney, user: none}
- name: bad-arn
  context: {cluster: "arn:aws:eks:us-east-1:1:cluster/short-account", user: none}
- name: by-arn
  context: {cluster: "arn:aws:eks:us-east-1:123456789012:cluster/prod-arn", user: none}
- name: by-exec
  context: {cluster: kube-name, user: aws-cli}
- name: by-exec-equals
  context: {cluster: kube-name, user: aws-cli-equals}
- name: by-authenticator
  context: {cluster: kube-name, user: authenticator}
- name: by-eksctl
  context: {cluster: prod-eksctl.us-west-2.eksctl.io, user: none}
- name: by-name
  context: {cluster: plain_name-1, user: none}
- name: unreadable
  context: {cluster: not/a/name, user: none}
- name: no-cluster
  context: {user: none}
users:
- name: none
  user: {}
- name: aws-cli
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      command: aws
      args: [--region, us-east-1, eks, get-token, --cluster-name, prod-exec, --output, json]
- name: aws-cli-equals
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      command: aws
      args: [eks, get-token, --cluster-name=prod-equals]
- name: authenticator
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      command: aws-iam-authenticator
      args: [token, -i, prod-auth]
- name: aws-cli-env
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      command: /usr/local/bin/aws
      args: [eks, get-token, --cluster-name, prod-env]
      env:
      - {name: AWS_DEFAULT_REGION, value: us-east-2}
      - {name: AWS_REGION, value: eu-central-1}
- name: outposts
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      command: aws
      args: [eks, get-token, --cluster-id, 0123abcd, --region, us-west-2]
`

func TestCurrentCluster(t *testing.T) {
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(kubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		context    string
		want       string
		wantRegion string
		wantOK     bool
	}{
		{"", "prod-arn", "us-east-1", true}, // the current context
		{"by-arn", "prod-arn", "us-east-1", true},
		{"arn:aws:eks:eu-west-1:123456789012:cluster/prod-context-arn", "prod-context-arn", "eu-west-1", true},
		// The exec plugin names the cluster for AWS; the cluster entry is
		// only the name that the kubeconfig gives it.
		{"by-exec", "prod-exec", "us-east-1", true},
		{"by-exec-equals", "prod-equals", "us-east-1", true},
		{"by-authenticator", "prod-auth", "us-east-1", true},
		{"by-eksctl", "prod-eksctl", "us-west-2", true},
		// The region of the API server wins over the region that
		// get-token signs with.
		{"by-exec-other-region", "prod-exec", "ap-southeast-2", true},
		// Without an EKS API server, the region is the one of get-token:
		// --region, then AWS_REGION, then AWS_DEFAULT_REGION.
		{"by-exec-no-eks-server", "prod-exec", "us-east-1", true},
		{"by-exec-env", "prod-env", "eu-central-1", true},
		{"by-exec-no-region", "", "", false},
		{"china", "prod-auth", "cn-north-1", true},
		{"dual-stack", "prod-equals", "eu-south-1", true},
		// The ID of a cluster on Outposts is not its name.
		{"outposts", "", "", false},
		// aws-iam-authenticator also serves self-managed clusters: with no
		// EKS API server it names no EKS cluster.
		{"self-managed", "", "", false},
		// A context that is not EKS names no cluster, whatever its
		// cluster entry is called.
		{"kind", "", "", false},
		{"by-name", "", "", false},
		{"eks-server-no-name", "", "", false},
		{"bad-arn", "", "", false},
		{"unreadable", "", "", false},
		{"no-cluster", "", "", false},
		{"no-such-context", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.context, func(t *testing.T) {
			got, region, ok, err := kube.CurrentCluster(path, tt.context)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want || region != tt.wantRegion || ok != tt.wantOK {
				t.Errorf("CurrentCluster(%q) = %q, %q, %v, want %q, %q, %v", tt.context, got, region, ok, tt.want, tt.wantRegion, tt.wantOK)
			}
		})
	}
}

// The plugin inherits the environment of refresh: an inherited AWS_REGION
// wins over the plugin's own AWS_DEFAULT_REGION, and the plugin's own
// AWS_REGION wins over the inherited one.
func TestCurrentCluster_ExecRegionInheritsTheEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(kubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_REGION", "ap-south-1")
	t.Setenv("AWS_DEFAULT_REGION", "sa-east-1")
	for context, want := range map[string]string{
		"by-exec-env":       "eu-central-1", // the plugin's own AWS_REGION
		"by-exec-no-region": "ap-south-1",   // the inherited AWS_REGION
	} {
		if _, region, ok, err := kube.CurrentCluster(path, context); err != nil || !ok || region != want {
			t.Errorf("CurrentCluster(%q) region = %q, %v, %v, want %q", context, region, ok, err, want)
		}
	}
	t.Setenv("AWS_REGION", "")
	if _, region, ok, err := kube.CurrentCluster(path, "by-exec-no-region"); err != nil || !ok || region != "sa-east-1" {
		t.Errorf("with AWS_DEFAULT_REGION alone: region = %q, %v, %v, want sa-east-1", region, ok, err)
	}
}

func TestCurrentCluster_FromTheEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte(kubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	// $KUBECONFIG is a list. A file of the list that does not exist is
	// skipped.
	t.Setenv("KUBECONFIG", filepath.Join(dir, "missing")+string(os.PathListSeparator)+path)
	got, _, ok, err := kube.CurrentCluster("", "")
	if err != nil || !ok || got != "prod-arn" {
		t.Errorf("CurrentCluster = %q, %v, %v, want prod-arn", got, ok, err)
	}

	t.Setenv("KUBECONFIG", filepath.Join(dir, "missing"))
	got, _, ok, err = kube.CurrentCluster("", "")
	if err != nil || ok || got != "" {
		t.Errorf("CurrentCluster with no kubeconfig = %q, %v, %v, want nothing and no error", got, ok, err)
	}
}

func TestCurrentCluster_BrokenFileIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte("clusters: [not, a, kubeconfig"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, err := kube.CurrentCluster(path, ""); err == nil || ok {
		t.Errorf("CurrentCluster of a broken file: ok = %v, error = %v, want an error", ok, err)
	}
}
