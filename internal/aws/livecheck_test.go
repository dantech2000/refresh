//go:build livecheck

package aws

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// TestLiveLatestAMIPaths resolves the latest AMI of every AMI type EKS
// defines, for every EKS version, through the SSM paths refresh builds.
func TestLiveLatestAMIPaths(t *testing.T) {
	ctx := context.Background()
	tr := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(tr.CloseIdleConnections) // keep-alive connections are not a leak
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion("us-east-1"), config.WithHTTPClient(&http.Client{Transport: tr}))
	if err != nil {
		t.Fatal(err)
	}
	vout, err := eks.NewFromConfig(cfg).DescribeClusterVersions(ctx, &eks.DescribeClusterVersionsInput{})
	if err != nil {
		t.Fatal(err)
	}
	var versions []string
	for _, cv := range vout.ClusterVersions {
		versions = append(versions, aws.ToString(cv.ClusterVersion))
	}
	sort.Strings(versions)
	t.Logf("EKS versions: %s", strings.Join(versions, " "))
	sc := ssm.NewFromConfig(cfg)
	for _, at := range types.AMITypes("").Values() {
		if _, known := lookupAMISSMSpec(at); !known {
			if at != types.AMITypesCustom {
				t.Errorf("%s: a new AMI type refresh has no SSM spec for", at)
			}
			continue
		}
		var found, missing []string
		for _, v := range versions {
			id, err := LatestAmiIDForType(ctx, sc, v, at)
			if err != nil || !strings.HasPrefix(id, "ami-") {
				missing = append(missing, v)
				continue
			}
			rel := LatestReleaseVersionForType(ctx, sc, v, at)
			found = append(found, v+"("+rel+")")
		}
		t.Logf("%-32s found: %s", at, strings.Join(found, " "))
		if len(missing) > 0 {
			t.Logf("%-32s MISSING: %s", "", strings.Join(missing, " "))
			// AL2 ends at 1.32 and Windows Server 2025 starts at 1.35; the
			// current families must cover every version.
			if s := string(at); strings.HasPrefix(s, "AL2023") || strings.HasPrefix(s, "BOTTLEROCKET") {
				t.Errorf("%s: no latest AMI for %s", at, strings.Join(missing, " "))
			}
		}
	}
}
