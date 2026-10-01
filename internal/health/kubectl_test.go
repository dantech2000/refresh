package health

import (
	"os"
	"path/filepath"
	"testing"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func TestKubectlCluster(t *testing.T) {
	const arn = "arn:aws:eks:eu-west-1:111122223333:cluster/prod-api"
	const ieServer = "https://ABC.gr7.eu-west-1.eks.amazonaws.com"
	exec := func(cmd string, env map[string]string, args ...string) *clientcmdapi.AuthInfo {
		e := &clientcmdapi.ExecConfig{Command: cmd, Args: args}
		for _, k := range []string{"AWS_DEFAULT_REGION", "AWS_REGION"} { // AWS_DEFAULT_REGION listed first
			if v, ok := env[k]; ok {
				e.Env = append(e.Env, clientcmdapi.ExecEnvVar{Name: k, Value: v})
			}
		}
		return &clientcmdapi.AuthInfo{Exec: e}
	}
	raw := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			arn:                             {Server: ieServer},
			"dev.us-west-2.eksctl.io":       {Server: "https://DEF.yl4.us-west-2.eks.amazonaws.com"},
			"syd":                           {Server: "https://GHI.sk1.ap-southeast-2.eks.amazonaws.com"},
			"cn":                            {Server: "https://JKL.sk1.cn-north-1.eks.amazonaws.com.cn"},
			"v6":                            {Server: "https://MNO.gr7.eu-south-1.api.aws"},
			"v6cn":                          {Server: "https://PQR.gr7.cn-northwest-1.api.amazonwebservices.com.cn"},
			"proxied":                       {Server: "https://k8s.internal.example:443"},
			"kind-local":                    {Server: "https://127.0.0.1:6443"},
			"arn:aws-cn:eks:cn-north-1:1:x": {Server: "https://x"},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			// get-token signs in us-east-1; the server says ap-southeast-2.
			"gettoken":   exec("aws", nil, "eks", "get-token", "--cluster-name", "batch", "--region=us-east-1"),
			"gettokenv6": exec("/usr/local/bin/aws", nil, "--region", "us-east-1", "eks", "get-token", "--cluster-name", "v6"),
			"outposts":   exec("aws", nil, "eks", "get-token", "--cluster-id", "0123abcd", "--region", "us-west-2"),
			"iam":        exec("aws-iam-authenticator", nil, "token", "-i", "legacy"),
			"iamenv":     exec("aws-iam-authenticator", map[string]string{"AWS_DEFAULT_REGION": "us-east-1"}, "token", "-i", "selfmanaged"),
			"envorder":   exec("aws", map[string]string{"AWS_DEFAULT_REGION": "us-east-1", "AWS_REGION": "eu-west-1"}, "eks", "get-token", "--cluster-name", "app"),
			"none":       {},
		},
		Contexts: map[string]*clientcmdapi.Context{
			arn:          {Cluster: arn},
			"short":      {Cluster: arn},
			"eksctl":     {Cluster: "dev.us-west-2.eksctl.io"},
			"gettoken":   {Cluster: "syd", AuthInfo: "gettoken"},
			"v6":         {Cluster: "v6", AuthInfo: "gettokenv6"},
			"v6cn":       {Cluster: "v6cn", AuthInfo: "iam"},
			"outposts":   {Cluster: "proxied", AuthInfo: "outposts"},
			"iam":        {Cluster: "syd", AuthInfo: "iam"},
			"cn":         {Cluster: "cn", AuthInfo: "iam"},
			"selfmanage": {Cluster: "proxied", AuthInfo: "iamenv"},
			"envorder":   {Cluster: "proxied", AuthInfo: "envorder"},
			"kind":       {Cluster: "kind-local", AuthInfo: "none"},
			"noname":     {Cluster: "syd", AuthInfo: "none"},
			"badarn":     {Cluster: "arn:aws-cn:eks:cn-north-1:1:x"},
		},
	}
	const syd = "https://GHI.sk1.ap-southeast-2.eks.amazonaws.com"
	for _, tc := range []struct {
		ctx  string
		want KubectlCluster
		ok   bool
	}{
		{arn, KubectlCluster{Context: arn, Name: "prod-api", Region: "eu-west-1", Account: "111122223333", Server: ieServer}, true},
		{"short", KubectlCluster{Context: "short", Name: "prod-api", Region: "eu-west-1", Account: "111122223333", Server: ieServer}, true},
		{"eksctl", KubectlCluster{Context: "eksctl", Name: "dev", Region: "us-west-2", Server: "https://DEF.yl4.us-west-2.eks.amazonaws.com"}, true},
		// The server's region wins over the region get-token signs with.
		{"gettoken", KubectlCluster{Context: "gettoken", Name: "batch", Region: "ap-southeast-2", Server: syd}, true},
		{"v6", KubectlCluster{Context: "v6", Name: "v6", Region: "eu-south-1", Server: "https://MNO.gr7.eu-south-1.api.aws"}, true},
		{"v6cn", KubectlCluster{Context: "v6cn", Name: "legacy", Region: "cn-northwest-1", Server: "https://PQR.gr7.cn-northwest-1.api.amazonwebservices.com.cn"}, true},
		// An Outposts cluster ID is not a cluster name.
		{"outposts", KubectlCluster{Context: "outposts", Region: "us-west-2", Server: "https://k8s.internal.example:443"}, false},
		{"iam", KubectlCluster{Context: "iam", Name: "legacy", Region: "ap-southeast-2", Server: syd}, true},
		{"cn", KubectlCluster{Context: "cn", Name: "legacy", Region: "cn-north-1", Server: "https://JKL.sk1.cn-north-1.eks.amazonaws.com.cn"}, true},
		// aws-iam-authenticator also serves self-managed clusters: without an
		// EKS server it names no EKS cluster.
		{"selfmanage", KubectlCluster{Context: "selfmanage", Server: "https://k8s.internal.example:443"}, false},
		// AWS_REGION wins over AWS_DEFAULT_REGION, wherever it is listed.
		{"envorder", KubectlCluster{Context: "envorder", Name: "app", Region: "eu-west-1", Server: "https://k8s.internal.example:443"}, true},
		{"kind", KubectlCluster{Context: "kind", Server: "https://127.0.0.1:6443"}, false},
		{"noname", KubectlCluster{Context: "noname", Region: "ap-southeast-2", Server: syd}, false},
		{"badarn", KubectlCluster{Context: "badarn", Server: "https://x"}, false},
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
