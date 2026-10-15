//go:build livecheck

package fleetdata_test

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/fleet/fleetdata"
)

// maxParameters is the most names that one GetParameters call takes.
const maxParameters = 10

// TestLiveAMIParameters reads the SSM parameter of the latest AMI of every
// AMI type that EKS defines, for every Kubernetes version that EKS
// supports, through the paths of AMIParameter. It fails for an AMI type
// that the table does not have, and for a current family (AL2023 and
// Bottlerocket) with no AMI for a version. AL2 ends at 1.32 and Windows
// Server 2025 starts at 1.35, so their gaps are logged only.
//
// It runs in us-east-1 with read-only calls that cost nothing:
//
//   - eks:DescribeClusterVersions, paged
//   - ssm:GetParameters, ten paths a call
func TestLiveAMIParameters(t *testing.T) {
	cfg := awsxtest.LiveConfig(t, "us-east-1")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	versions := liveVersions(ctx, t, eks.NewFromConfig(cfg), false)
	t.Logf("EKS versions: %s", strings.Join(versions, " "))

	known := fleetdata.AMITypes()
	paths := map[string]string{} // path → "type version"
	var names []string
	for _, at := range ekstypes.AMITypes("").Values() {
		if at == ekstypes.AMITypesCustom {
			continue
		}
		if !slices.Contains(known, string(at)) {
			t.Errorf("%s: a new AMI type that the table of AMIParameter does not have", at)
			continue
		}
		for _, v := range versions {
			path, ok := fleetdata.AMIParameter(v, string(at))
			if !ok {
				t.Errorf("%s %s: AMIParameter has no path", at, v)
				continue
			}
			paths[path] = string(at) + " " + v
			names = append(names, path)
		}
	}
	found := map[string]string{}
	client := ssm.NewFromConfig(cfg)
	for batch := range slices.Chunk(names, maxParameters) {
		out, err := client.GetParameters(ctx, &ssm.GetParametersInput{Names: batch})
		if err != nil {
			t.Fatalf("ssm:GetParameters: %v", err)
		}
		for _, p := range out.Parameters {
			found[aws.ToString(p.Name)] = aws.ToString(p.Value)
		}
	}
	missing := map[string][]string{}
	for _, path := range names {
		at, v, _ := strings.Cut(paths[path], " ")
		if id := found[path]; !strings.HasPrefix(id, "ami-") {
			missing[at] = append(missing[at], v)
		}
	}
	for _, at := range slices.Sorted(maps.Keys(missing)) {
		gap := strings.Join(missing[at], " ")
		if strings.HasPrefix(at, "AL2023") || strings.HasPrefix(at, "BOTTLEROCKET") {
			t.Errorf("%s: no latest AMI for %s", at, gap)
			continue
		}
		t.Logf("%-32s no AMI for %s", at, gap)
	}
	t.Logf("%d paths, %d with an AMI (table as of %s)", len(names), len(names)-countAll(missing), fleetdata.AMIParametersAsOf)
}

// countAll counts the values of every key of m.
func countAll(m map[string][]string) int {
	n := 0
	for _, v := range m {
		n += len(v)
	}
	return n
}

// liveVersions returns the Kubernetes versions that EKS reports, oldest
// first. all includes the versions past the end of extended support.
func liveVersions(ctx context.Context, t *testing.T, client *eks.Client, all bool) []string {
	t.Helper()
	var versions []string
	pages := eks.NewDescribeClusterVersionsPaginator(client, &eks.DescribeClusterVersionsInput{IncludeAll: aws.Bool(all)})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			t.Fatalf("eks:DescribeClusterVersions: %v", err)
		}
		for _, cv := range page.ClusterVersions {
			versions = append(versions, aws.ToString(cv.ClusterVersion))
		}
	}
	if len(versions) == 0 {
		t.Fatal("eks:DescribeClusterVersions reported no version")
	}
	slices.SortFunc(versions, func(a, b string) int {
		va, _ := fleet.ParseVersion(a)
		vb, _ := fleet.ParseVersion(b)
		return va.Compare(vb)
	})
	return versions
}
