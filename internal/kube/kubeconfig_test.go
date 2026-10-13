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
contexts:
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
`

func TestCurrentCluster(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(kubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		context string
		want    string
		wantOK  bool
	}{
		{"", "prod-arn", true}, // the current context
		{"by-arn", "prod-arn", true},
		// The exec plugin names the cluster for AWS; the cluster entry is
		// only the name that the kubeconfig gives it.
		{"by-exec", "prod-exec", true},
		{"by-exec-equals", "prod-equals", true},
		{"by-authenticator", "prod-auth", true},
		{"by-eksctl", "prod-eksctl", true},
		{"by-name", "plain_name-1", true},
		{"unreadable", "", false},
		{"no-cluster", "", false},
		{"no-such-context", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.context, func(t *testing.T) {
			got, ok, err := kube.CurrentCluster(path, tt.context)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("CurrentCluster(%q) = %q, %v, want %q, %v", tt.context, got, ok, tt.want, tt.wantOK)
			}
		})
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
	got, ok, err := kube.CurrentCluster("", "")
	if err != nil || !ok || got != "prod-arn" {
		t.Errorf("CurrentCluster = %q, %v, %v, want prod-arn", got, ok, err)
	}

	t.Setenv("KUBECONFIG", filepath.Join(dir, "missing"))
	got, ok, err = kube.CurrentCluster("", "")
	if err != nil || ok || got != "" {
		t.Errorf("CurrentCluster with no kubeconfig = %q, %v, %v, want nothing and no error", got, ok, err)
	}
}

func TestCurrentCluster_BrokenFileIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte("clusters: [not, a, kubeconfig"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := kube.CurrentCluster(path, ""); err == nil || ok {
		t.Errorf("CurrentCluster of a broken file: ok = %v, error = %v, want an error", ok, err)
	}
}
