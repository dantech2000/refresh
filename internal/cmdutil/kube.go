package cmdutil

import (
	"os"
	"sync"

	"github.com/dantech2000/refresh/internal/kube"
)

// EnvInClusterName names the EKS cluster that a pod running refresh is in.
// The in-cluster config is used for that cluster only.
const EnvInClusterName = "REFRESH_IN_CLUSTER_NAME"

// NewKube returns the function for Factory.Kube: kube.Connect with the
// --kubeconfig and --kube-context of the run, once per target. A failed
// resolution is kept too: a run asks about one cluster, and the answer
// does not change within it.
func NewKube(f *Factory) func(target kube.Target) (*kube.Client, error) {
	type result struct {
		client *kube.Client
		err    error
	}
	var (
		mu      sync.Mutex
		clients = map[string]result{}
	)
	return func(target kube.Target) (*kube.Client, error) {
		mu.Lock()
		defer mu.Unlock()
		if r, ok := clients[target.Name]; ok {
			return r.client, r.err
		}
		client, err := kube.Connect(target, kube.Options{
			Kubeconfig:    f.Global.Kubeconfig,
			Context:       f.Global.KubeContext,
			InClusterName: os.Getenv(EnvInClusterName),
		})
		clients[target.Name] = result{client, err}
		return client, err
	}
}
