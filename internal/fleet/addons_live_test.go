//go:build livecheck

package fleet_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"

	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet"
)

// TestLiveAddonVersions reads the real catalogues of the common add-ons,
// for every Kubernetes version that EKS supports, through
// Fetcher.AddonVersions. Every version must be one that
// ValidAddonVersion accepts: a change refuses any other. And
// CompareAddonVersions must order them strictly: two different versions
// that compare equal would make the newest one a guess.
//
// It runs in us-east-1 with read-only calls that cost nothing:
//
//   - eks:DescribeClusterVersions, paged
//   - eks:DescribeAddonVersions for each add-on and version, paged
func TestLiveAddonVersions(t *testing.T) {
	cfg := awsxtest.LiveConfig(t, "us-east-1")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	clients := awsx.NewProvider(cfg).Clients("us-east-1")
	var kubes []fleet.Version
	pages := eks.NewDescribeClusterVersionsPaginator(clients.EKS, &eks.DescribeClusterVersionsInput{})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			t.Fatalf("eks:DescribeClusterVersions: %v", err)
		}
		for _, cv := range page.ClusterVersions {
			v, err := fleet.ParseVersion(aws.ToString(cv.ClusterVersion))
			if err != nil {
				t.Errorf("EKS reports the version %q: %v", aws.ToString(cv.ClusterVersion), err)
				continue
			}
			kubes = append(kubes, v)
		}
	}
	var f fleet.Fetcher
	for _, name := range []string{"vpc-cni", "coredns", "kube-proxy", "aws-ebs-csi-driver", "eks-pod-identity-agent", "metrics-server"} {
		var newest []string
		for _, k := range kubes {
			versions, err := f.AddonVersions(ctx, clients, name, k)
			if err != nil {
				t.Errorf("%s for %s: %v", name, k, err)
				continue
			}
			for i, v := range versions {
				if !fleet.ValidAddonVersion(v) {
					t.Errorf("%s for %s: %q is not a version that refresh can compare", name, k, v)
				}
				if i > 0 && fleet.CompareAddonVersions(versions[i-1], v) <= 0 {
					t.Errorf("%s for %s: %s is not newer than %s", name, k, versions[i-1], v)
				}
			}
			if len(versions) == 0 {
				newest = append(newest, k.String()+"=none")
				continue
			}
			newest = append(newest, k.String()+"="+versions[0])
		}
		t.Logf("%-24s newest by Kubernetes version: %s", name, strings.Join(newest, " "))
	}
}
