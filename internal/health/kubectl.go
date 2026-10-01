package health

import (
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// KubectlCluster is the EKS cluster a kubeconfig context points at, as far
// as the kubeconfig tells: Name and Region when they could be read, Account
// only from a cluster ARN, and Server, the context's API server.
type KubectlCluster struct {
	Context string
	Name    string
	Region  string
	Account string
	Server  string
}

// clusterARN matches an EKS cluster ARN in any partition.
var clusterARN = regexp.MustCompile(`^arn:aws[a-z-]*:eks:([a-z0-9-]+):(\d{12}):cluster/([A-Za-z0-9][A-Za-z0-9_-]*)$`)

// eksctlCluster matches the cluster entry eksctl writes: <name>.<region>.eksctl.io.
var eksctlCluster = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9_-]*)\.([a-z]{2}(?:-[a-z]+)+-\d)\.eksctl\.io$`)

// eksServer matches an EKS API server host and captures its region: the
// IPv4 endpoint (<id>.<suffix>.<region>.eks.amazonaws.com[.cn]) and the
// dual-stack one of IPv6 clusters (….<region>.api.aws, or
// ….<region>.api.amazonwebservices.com.cn in China).
var eksServer = regexp.MustCompile(`\.([a-z]{2}(?:-[a-z]+)+-\d)\.(?:eks\.amazonaws\.com(?:\.cn)?|api\.aws|api\.amazonwebservices\.com\.cn)$`)

// CurrentKubectlCluster reads the EKS cluster of the kubeconfig context
// kubectl uses: kubeContext when set, else the current context. The
// kubeconfig is found as for every Kubernetes call (--kubeconfig, then
// $KUBECONFIG, then ~/.kube/config). ok is false when there is no
// kubeconfig or context, or the context is not an EKS cluster refresh can
// place in a region.
//
// The name and region come from, in order: a cluster ARN (the context or
// cluster name `aws eks update-kubeconfig` writes), and an eksctl cluster
// name. Otherwise the region comes from the API server host, and the name
// from the `aws eks get-token` arguments (or any exec plugin's, when the
// server is an EKS endpoint); the plugin's region, which signs the token
// and need not be the cluster's, is the last resort.
func CurrentKubectlCluster(kubeconfigPath, kubeContext string) (KubectlCluster, bool) {
	path, source, err := locateKubeconfig(kubeconfigPath)
	if err != nil || path == "" {
		return KubectlCluster{}, false
	}
	raw, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(kubeconfigRules(path, source), &clientcmd.ConfigOverrides{}).RawConfig()
	if err != nil {
		return KubectlCluster{}, false
	}
	name := strings.TrimSpace(kubeContext)
	if name == "" {
		name = raw.CurrentContext
	}
	return kubectlCluster(raw, name)
}

// kubectlCluster reads the EKS cluster of context name in raw.
func kubectlCluster(raw clientcmdapi.Config, name string) (KubectlCluster, bool) {
	c := raw.Contexts[name]
	if c == nil {
		return KubectlCluster{}, false
	}
	out := KubectlCluster{Context: name}
	if cl := raw.Clusters[c.Cluster]; cl != nil {
		out.Server = cl.Server
	}
	for _, ref := range []string{c.Cluster, name} {
		if m := clusterARN.FindStringSubmatch(ref); m != nil {
			out.Region, out.Account, out.Name = m[1], m[2], m[3]
			return out, true
		}
	}
	if m := eksctlCluster.FindStringSubmatch(c.Cluster); m != nil {
		out.Name, out.Region = m[1], m[2]
		return out, true
	}
	out.Region = serverRegion(out.Server)
	if u := raw.AuthInfos[c.AuthInfo]; u != nil && u.Exec != nil && (out.Region != "" || isGetToken(u.Exec)) {
		out.Name = argValue(u.Exec.Args, "--cluster-name", "-i", "--cluster-id")
		if isGetToken(u.Exec) && argValue(u.Exec.Args, "--cluster-id") != "" {
			out.Name = "" // an Outposts cluster ID, not its name
		}
		if out.Region == "" {
			out.Region = execRegion(u.Exec)
		}
	}
	return out, out.Name != "" && out.Region != ""
}

// serverRegion is the region of an EKS API server URL, or "".
func serverRegion(server string) string {
	u, err := url.Parse(server)
	if err != nil {
		return ""
	}
	if m := eksServer.FindStringSubmatch(u.Hostname()); m != nil {
		return m[1]
	}
	return ""
}

// IsEKSServer reports whether server is an EKS API server URL.
func IsEKSServer(server string) bool { return serverRegion(server) != "" }

// isGetToken reports an `aws eks get-token` exec plugin.
func isGetToken(e *clientcmdapi.ExecConfig) bool {
	cmd := strings.TrimSuffix(filepath.Base(e.Command), ".exe")
	return (cmd == "aws" || cmd == "aws2") && slices.Contains(e.Args, "eks") && slices.Contains(e.Args, "get-token")
}

// execRegion is the region an exec plugin signs with: --region, else
// AWS_REGION, else AWS_DEFAULT_REGION, as the AWS CLI reads them.
func execRegion(e *clientcmdapi.ExecConfig) string {
	if r := argValue(e.Args, "--region"); r != "" {
		return r
	}
	for _, name := range []string{"AWS_REGION", "AWS_DEFAULT_REGION"} {
		for _, v := range e.Env {
			if v.Name == name && strings.TrimSpace(v.Value) != "" {
				return strings.TrimSpace(v.Value)
			}
		}
	}
	return ""
}

// argValue is the value of the first of flags in args, as `--flag value` or
// `--flag=value`, or "".
func argValue(args []string, flags ...string) string {
	for i, a := range args {
		for _, f := range flags {
			if v, ok := strings.CutPrefix(a, f+"="); ok {
				return strings.TrimSpace(v)
			}
			if a == f && i+1 < len(args) {
				return strings.TrimSpace(args[i+1])
			}
		}
	}
	return ""
}
