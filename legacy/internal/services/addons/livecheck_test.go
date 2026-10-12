//go:build livecheck

package addons

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/eks"
)

// TestLiveAddonVersions reads real add-on versions and checks that refresh
// orders them without ties.
func TestLiveAddonVersions(t *testing.T) {
	ctx := context.Background()
	tr := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(tr.CloseIdleConnections) // keep-alive connections are not a leak
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion("us-east-1"), config.WithHTTPClient(&http.Client{Transport: tr}))
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(eks.NewFromConfig(cfg), slog.New(slog.DiscardHandler))
	for _, name := range []string{"vpc-cni", "coredns", "kube-proxy", "aws-ebs-csi-driver", "eks-pod-identity-agent", "metrics-server"} {
		all, err := svc.GetAvailableVersions(ctx, name, "")
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		for i := 1; i < len(all); i++ {
			if c := CompareVersions(all[i-1].Version, all[i].Version); c < 0 || (c == 0 && all[i-1].Version != all[i].Version) {
				t.Errorf("%s: order %s before %s (compare %d)", name, all[i-1].Version, all[i].Version, c)
			}
		}
		var per []string
		for _, k := range []string{"1.31", "1.32", "1.33", "1.34", "1.35", "1.36"} {
			vs, err := svc.GetAvailableVersions(ctx, name, k)
			if err != nil {
				per = append(per, k+"=none")
				continue
			}
			per = append(per, k+"="+vs[0].Version)
		}
		t.Logf("%-24s %d versions, newest %s · per k8s: %s", name, len(all), all[0].Version, strings.Join(per, " "))
	}
}
