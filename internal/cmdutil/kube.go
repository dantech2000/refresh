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
// --kubeconfig and --kube-context of the run, once per target. The key is
// the whole target: a fleet or the UI may hold two clusters of one name in
// two regions. A failed resolution is not kept. It makes no request, and
// the UI asks again after the user adds a kubeconfig context.
func NewKube(f *Factory) func(target kube.Target) (*kube.Client, error) {
	var (
		mu      sync.Mutex
		clients = map[kube.Target]*kube.Client{}
	)
	return func(target kube.Target) (*kube.Client, error) {
		mu.Lock()
		defer mu.Unlock()
		if client, ok := clients[target]; ok {
			return client, nil
		}
		client, err := kube.Connect(target, kube.Options{
			Kubeconfig:    f.Global.Kubeconfig,
			Context:       f.Global.KubeContext,
			InClusterName: os.Getenv(EnvInClusterName),
		})
		if err != nil {
			return nil, err
		}
		clients[target] = client
		return client, nil
	}
}
