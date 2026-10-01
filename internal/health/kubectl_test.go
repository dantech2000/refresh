package health

import (
	"os"
	"path/filepath"
	"testing"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func TestKubectlCluster(t *testing.T) {
	const arn = "arn:aws:eks:eu-west-1:111122223333:cluster/prod-api"
	raw := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			arn:                             {Server: "https://ABC.gr7.eu-west-1.eks.amazonaws.com"},
			"dev.us-west-2.eksctl.io":       {Server: "https://DEF.yl4.us-west-2.eks.amazonaws.com"},
			"plain":                         {Server: "https://GHI.sk1.ap-southeast-2.eks.amazonaws.com"},
			"cn":                            {Server: "https://JKL.sk1.cn-north-1.eks.amazonaws.com.cn"},
			"kind-local":                    {Server: "https://127.0.0.1:6443"},
			"arn:aws-cn:eks:cn-north-1:1:x": {Server: "https://x"},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"exec": {Exec: &clientcmdapi.ExecConfig{Command: "aws", Args: []string{"eks", "get-token", "--cluster-name", "batch", "--region=us-east-2"}}},
			"iam":  {Exec: &clientcmdapi.ExecConfig{Command: "aws-iam-authenticator", Args: []string{"token", "-i", "legacy"}}},
			"none": {},
		},
		Contexts: map[string]*clientcmdapi.Context{
			arn:      {Cluster: arn},
			"short":  {Cluster: arn},
			"eksctl": {Cluster: "dev.us-west-2.eksctl.io"},
			"exec":   {Cluster: "plain", AuthInfo: "exec"},
			"iam":    {Cluster: "plain", AuthInfo: "iam"},
			"cn":     {Cluster: "cn", AuthInfo: "iam"},
			"kind":   {Cluster: "kind-local", AuthInfo: "none"},
			"noname": {Cluster: "plain", AuthInfo: "none"},
			"badarn": {Cluster: "arn:aws-cn:eks:cn-north-1:1:x"},
		},
	}
	for _, tc := range []struct {
		ctx  string
		want KubectlCluster
		ok   bool
	}{
		{arn, KubectlCluster{Context: arn, Name: "prod-api", Region: "eu-west-1", Account: "111122223333"}, true},
		{"short", KubectlCluster{Context: "short", Name: "prod-api", Region: "eu-west-1", Account: "111122223333"}, true},
		{"eksctl", KubectlCluster{Context: "eksctl", Name: "dev", Region: "us-west-2"}, true},
		// The exec plugin's region wins over the server host's.
		{"exec", KubectlCluster{Context: "exec", Name: "batch", Region: "us-east-2"}, true},
		{"iam", KubectlCluster{Context: "iam", Name: "legacy", Region: "ap-southeast-2"}, true},
		{"cn", KubectlCluster{Context: "cn", Name: "legacy", Region: "cn-north-1"}, true},
		{"kind", KubectlCluster{Context: "kind"}, false},
		{"noname", KubectlCluster{Context: "noname", Region: "ap-southeast-2"}, false},
		{"badarn", KubectlCluster{Context: "badarn"}, false},
		{"missing", KubectlCluster{}, false},
	} {
		got, ok := kubectlCluster(raw, tc.ctx)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: got %+v, %v; want %+v, %v", tc.ctx, got, ok, tc.want, tc.ok)
		}
	}
}

func TestCurrentKubectlCluster(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	const cfg = `apiVersion: v1
kind: Config
current-context: a
clusters:
- name: arn:aws:eks:us-east-1:111122223333:cluster/one
  cluster: {server: "https://A.gr7.us-east-1.eks.amazonaws.com"}
- name: two.eu-west-1.eksctl.io
  cluster: {server: "https://B.gr7.eu-west-1.eks.amazonaws.com"}
contexts:
- name: a
  context: {cluster: "arn:aws:eks:us-east-1:111122223333:cluster/one"}
- name: b
  context: {cluster: two.eu-west-1.eksctl.io}
`
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := CurrentKubectlCluster(path, ""); !ok || got.Name != "one" || got.Region != "us-east-1" || got.Context != "a" {
		t.Errorf("current context: got %+v, %v", got, ok)
	}
	if got, ok := CurrentKubectlCluster(path, "b"); !ok || got.Name != "two" || got.Region != "eu-west-1" {
		t.Errorf("--kube-context b: got %+v, %v", got, ok)
	}
	if _, ok := CurrentKubectlCluster(filepath.Join(dir, "missing"), ""); ok {
		t.Error("a missing kubeconfig gave a cluster")
	}
}
