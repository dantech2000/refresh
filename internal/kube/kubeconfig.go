// Package kube reads what refresh needs from a kubeconfig.
package kube

import (
	"regexp"
	"strings"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// clusterName matches the names that EKS accepts for a cluster.
var clusterName = regexp.MustCompile(`^[0-9A-Za-z][A-Za-z0-9_-]*$`)

// CurrentCluster returns the name of the EKS cluster that a kubeconfig
// context points at. kubeconfig is the path of the file; "" means the files
// that kubectl reads, $KUBECONFIG or ~/.kube/config. contextName is the
// context; "" means the current context.
//
// ok is false when there is nothing to read: no kubeconfig, no such
// context, or a context that does not name an EKS cluster in a form that
// this function knows. That is not an error: the kubeconfig is the last
// place a read-only command looks for its cluster. An error is a kubeconfig
// that exists and cannot be parsed.
//
// The name comes from, in this order:
//
//  1. The cluster entry when it is an EKS cluster ARN, as
//     "aws eks update-kubeconfig" writes it.
//  2. The --cluster-name argument of the context's exec credential plugin.
//  3. The cluster entry when it is "<name>.<region>.eksctl.io", as eksctl
//     writes it, or a plain cluster name.
func CurrentCluster(kubeconfig, contextName string) (name string, ok bool, err error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	rules.ExplicitPath = kubeconfig
	// The loader warns on stderr about a missing file of $KUBECONFIG. A
	// missing kubeconfig is not worth a line here.
	rules.WarnIfAllMissing = false
	config, err := rules.Load()
	if err != nil {
		return "", false, err
	}
	if contextName == "" {
		contextName = config.CurrentContext
	}
	context := config.Contexts[contextName]
	if context == nil || context.Cluster == "" {
		return "", false, nil
	}
	if name := fromARN(context.Cluster); name != "" {
		return name, true, nil
	}
	if name := fromExec(config.AuthInfos[context.AuthInfo]); name != "" {
		return name, true, nil
	}
	ref := context.Cluster
	if first, rest, cut := strings.Cut(ref, "."); cut && strings.HasSuffix(rest, ".eksctl.io") {
		ref = first
	}
	if clusterName.MatchString(ref) {
		return ref, true, nil
	}
	return "", false, nil
}

// fromARN returns the cluster name of an EKS cluster ARN,
// arn:aws:eks:<region>:<account>:cluster/<name>, or "".
func fromARN(ref string) string {
	if !strings.HasPrefix(ref, "arn:") {
		return ""
	}
	_, name, ok := strings.Cut(ref, ":cluster/")
	if !ok || !clusterName.MatchString(name) {
		return ""
	}
	return name
}

// fromExec returns the value of --cluster-name in the arguments of the exec
// credential plugin of a user, or "". Both "aws eks get-token" and
// aws-iam-authenticator take the cluster that way, the second one also as
// -i.
func fromExec(user *clientcmdapi.AuthInfo) string {
	if user == nil || user.Exec == nil {
		return ""
	}
	args := user.Exec.Args
	for i, arg := range args {
		if value, ok := strings.CutPrefix(arg, "--cluster-name="); ok && clusterName.MatchString(value) {
			return value
		}
		if (arg == "--cluster-name" || arg == "-i") && i+1 < len(args) && clusterName.MatchString(args[i+1]) {
			return args[i+1]
		}
	}
	return ""
}
