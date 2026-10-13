package fleetcmd

import (
	"errors"
	"fmt"

	"k8s.io/client-go/kubernetes"
	metricsv1beta1 "k8s.io/metrics/pkg/client/clientset/versioned/typed/metrics/v1beta1"

	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/kube"
)

// KubeAccess is the Kubernetes side of a command: the client for the
// cluster, or why there is none.
type KubeAccess struct {
	Client  kubernetes.Interface
	Metrics metricsv1beta1.NodeMetricsInterface
	// NoKube says why Client is nil, in the words of a message.
	NoKube string
	// Warning is what to print on stderr when there is no client, with the
	// hint on a second line, or "". Notice is the one line about a client
	// that was chosen in a way worth a word, or "".
	Warning, Notice string
}

// ConnectKube resolves the Kubernetes client of the cluster of snap with
// connect, which is Factory.Kube. It makes no request. A cluster that
// cannot be matched to a kubeconfig context gives no client and a warning.
func ConnectKube(connect func(kube.Target) (*kube.Client, error), snap *fleet.Snapshot) KubeAccess {
	target := kube.Target{Name: snap.Cluster.Name, Region: snap.Cluster.Region, Endpoint: snap.Cluster.Endpoint}
	client, err := connect(target)
	if err != nil {
		access := KubeAccess{NoKube: err.Error(), Warning: "no Kubernetes access to " + target.Name + ": " + err.Error()}
		var mm *kube.MismatchError
		if errors.As(err, &mm) && mm.Hint() != "" {
			access.Warning += "\n  " + mm.Hint()
		}
		return access
	}
	return KubeAccess{Client: client.Clientset, Metrics: client.Metrics, Notice: client.Selection.Notice()}
}

// ReportKube prints the warning or the notice of the access on stderr.
func ReportKube(ios *cmdutil.IOStreams, access KubeAccess) {
	th := ios.ErrTheme()
	// Nothing can report a failed write to stderr.
	switch {
	case access.Warning != "":
		_, _ = fmt.Fprintln(ios.ErrOut, th.Caution("warning: "+access.Warning))
	case access.Notice != "":
		_, _ = fmt.Fprintln(ios.ErrOut, th.Dim(access.Notice))
	}
}
