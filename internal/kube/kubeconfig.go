// Package kube reads what refresh needs from a kubeconfig.
package kube

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// clusterARN matches an EKS cluster ARN in any partition, and captures the
// region and the name.
var clusterARN = regexp.MustCompile(`^arn:aws[a-z-]*:eks:([a-z0-9-]+):\d{12}:cluster/([A-Za-z0-9][A-Za-z0-9_-]*)$`)

// eksctlCluster matches the cluster entry that eksctl writes,
// <name>.<region>.eksctl.io.
var eksctlCluster = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9_-]*)\.([a-z]{2}(?:-[a-z]+)+-\d)\.eksctl\.io$`)

// eksServer matches the host of an EKS API server and captures its region:
// <id>.<suffix>.<region>.eks.amazonaws.com, with .cn in China, and the
// dual-stack host of an IPv6 cluster, which ends in <region>.api.aws or in
// <region>.api.amazonwebservices.com.cn.
var eksServer = regexp.MustCompile(`\.([a-z]{2}(?:-[a-z]+)+-\d)\.(?:eks\.amazonaws\.com(?:\.cn)?|api\.aws|api\.amazonwebservices\.com\.cn)$`)

// clusterName matches the names that EKS accepts for a cluster.
var clusterName = regexp.MustCompile(`^[0-9A-Za-z][A-Za-z0-9_-]*$`)

// CurrentCluster returns the name and the region of the EKS cluster that a
// kubeconfig context points at. kubeconfig is the path of the file; ""
// means the files that kubectl reads, $KUBECONFIG or ~/.kube/config.
// contextName is the context; "" means the current context.
//
// ok is false when there is nothing to read: no kubeconfig, no such
// context, or a context that does not name an EKS cluster and its region
// in a form that this function knows. A context of kind, of a self-managed
// cluster, or of an EKS cluster on Outposts names none. That is not an
// error: the kubeconfig is the last place a read-only command looks for
// its cluster. An error is a kubeconfig that exists and cannot be parsed.
//
// The name and the region come from, in this order:
//
//  1. An EKS cluster ARN, as "aws eks update-kubeconfig" writes it: the
//     cluster entry, or the name of the context.
//  2. The cluster entry "<name>.<region>.eksctl.io", as eksctl writes it.
//  3. The exec credential plugin of the context: the name is its
//     --cluster-name or -i argument. The region is the one in the host of
//     the API server. The region that the plugin signs with need not be
//     the region of the cluster, so it counts only for "aws eks get-token"
//     and only when the host has none: --region, then AWS_REGION, then
//     AWS_DEFAULT_REGION, each from the plugin's own environment before
//     the environment of this process. Another plugin, such as
//     aws-iam-authenticator, also serves clusters that are not EKS, so it
//     names a cluster only with an EKS API server.
func CurrentCluster(kubeconfig, contextName string) (name, region string, ok bool, err error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	rules.ExplicitPath = kubeconfig
	// The loader warns on stderr about a missing file of $KUBECONFIG. A
	// missing kubeconfig is not worth a line here.
	rules.WarnIfAllMissing = false
	config, err := rules.Load()
	if err != nil {
		return "", "", false, err
	}
	if contextName == "" {
		contextName = config.CurrentContext
	}
	context := config.Contexts[contextName]
	if context == nil || context.Cluster == "" {
		return "", "", false, nil
	}
	for _, ref := range []string{context.Cluster, contextName} {
		if m := clusterARN.FindStringSubmatch(ref); m != nil {
			return m[2], m[1], true, nil
		}
	}
	if m := eksctlCluster.FindStringSubmatch(context.Cluster); m != nil {
		return m[1], m[2], true, nil
	}
	user := config.AuthInfos[context.AuthInfo]
	if user == nil || user.Exec == nil {
		return "", "", false, nil
	}
	if cluster := config.Clusters[context.Cluster]; cluster != nil {
		region = serverRegion(cluster.Server)
	}
	getToken := isGetToken(user.Exec)
	switch {
	case region == "" && !getToken:
		return "", "", false, nil
	case getToken && argValue(user.Exec.Args, "--cluster-id") != "":
		// The ID of a cluster on Outposts is not its name.
		return "", "", false, nil
	case region == "":
		region = execRegion(user.Exec)
	}
	name = argValue(user.Exec.Args, "--cluster-name", "-i")
	if !clusterName.MatchString(name) || region == "" {
		return "", "", false, nil
	}
	return name, region, true, nil
}

// serverRegion returns the region in the URL of an EKS API server, or "".
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

// isGetToken reports whether an exec plugin is "aws eks get-token".
func isGetToken(e *clientcmdapi.ExecConfig) bool {
	cmd := strings.TrimSuffix(filepath.Base(e.Command), ".exe")
	return (cmd == "aws" || cmd == "aws2") && slices.Contains(e.Args, "eks") && slices.Contains(e.Args, "get-token")
}

// execRegion returns the region that an exec plugin signs with: --region,
// then AWS_REGION, then AWS_DEFAULT_REGION, as the AWS CLI reads them. The
// plugin inherits the environment of this process, and its own entries
// replace single variables.
func execRegion(e *clientcmdapi.ExecConfig) string {
	if r := argValue(e.Args, "--region"); r != "" {
		return r
	}
	for _, name := range []string{"AWS_REGION", "AWS_DEFAULT_REGION"} {
		v := os.Getenv(name)
		for _, ev := range e.Env {
			if ev.Name == name {
				v = ev.Value
			}
		}
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// argValue returns the value of the first of flags in args, written as
// "--flag value" or as "--flag=value", or "".
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
