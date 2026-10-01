package health

import (
	"net/url"
	"regexp"
	"strings"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// KubectlCluster is the EKS cluster a kubeconfig context points at, as far
// as the kubeconfig tells: Name and Region when they could be read, Account
// only from a cluster ARN.
type KubectlCluster struct {
	Context string
	Name    string
	Region  string
	Account string
}

// clusterARN matches an EKS cluster ARN in any partition.
var clusterARN = regexp.MustCompile(`^arn:aws[a-z-]*:eks:([a-z0-9-]+):(\d{12}):cluster/([A-Za-z0-9][A-Za-z0-9_-]*)$`)

// eksctlCluster matches the cluster entry eksctl writes: <name>.<region>.eksctl.io.
var eksctlCluster = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9_-]*)\.([a-z]{2}(?:-[a-z]+)+-\d)\.eksctl\.io$`)

// eksServer matches an EKS API server host: <id>.<suffix>.<region>.eks.amazonaws.com[.cn].
var eksServer = regexp.MustCompile(`\.([a-z]{2}(?:-[a-z]+)+-\d)\.eks\.amazonaws\.com(?:\.cn)?$`)

// CurrentKubectlCluster reads the EKS cluster of the kubeconfig context
// kubectl uses: kubeContext when set, else the current context. The
// kubeconfig is found as for every Kubernetes call (--kubeconfig, then
// $KUBECONFIG, then ~/.kube/config). ok is false when there is no
// kubeconfig or context, or the context is not an EKS cluster refresh can
// place in a region.
//
// The name and region come from, in order: a cluster ARN (the context or
// cluster name `aws eks update-kubeconfig` writes), an eksctl cluster name,
// and the exec plugin's --cluster-name and --region; the region also from
// the API server host.
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
	if u := raw.AuthInfos[c.AuthInfo]; u != nil && u.Exec != nil {
		out.Name = argValue(u.Exec.Args, "--cluster-name", "--cluster-id", "-i")
		out.Region = argValue(u.Exec.Args, "--region")
		for _, e := range u.Exec.Env {
			if out.Region == "" && (e.Name == "AWS_REGION" || e.Name == "AWS_DEFAULT_REGION") {
				out.Region = strings.TrimSpace(e.Value)
			}
		}
	}
	if out.Region == "" {
		if cl := raw.Clusters[c.Cluster]; cl != nil {
			if u, err := url.Parse(cl.Server); err == nil {
				if m := eksServer.FindStringSubmatch(u.Hostname()); m != nil {
					out.Region = m[1]
				}
			}
		}
	}
	return out, out.Name != "" && out.Region != ""
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
