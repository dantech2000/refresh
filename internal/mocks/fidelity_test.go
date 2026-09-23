package mocks

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/smithy-go"
)

func TestWithCluster_UnknownNameIsTypedNotFound(t *testing.T) {
	m := NewEKSAPI().WithCluster("prod", "1.32").WithCluster("staging", "1.31").Build()
	ctx := context.Background()

	out, err := m.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String("staging")})
	if err != nil || aws.ToString(out.Cluster.Version) != "1.31" {
		t.Fatalf("DescribeCluster(staging) = %#v, %v", out, err)
	}

	var rnf *ekstypes.ResourceNotFoundException
	if _, err := m.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String("nope")}); !errors.As(err, &rnf) {
		t.Fatalf("DescribeCluster(nope) err = %v (%T), want *ResourceNotFoundException", err, err)
	}
	var apiErr smithy.APIError
	if _, err := m.ListNodegroups(ctx, &eks.ListNodegroupsInput{ClusterName: aws.String("nope")}); !errors.As(err, &apiErr) || apiErr.ErrorCode() != "ResourceNotFoundException" {
		t.Fatalf("ListNodegroups(nope) err = %v, want ResourceNotFoundException", err)
	}
	m2 := NewEKSAPI().WithCluster("prod", "1.32").WithAddon("vpc-cni", "v1", ekstypes.AddonStatusActive).Build()
	if _, err := m2.ListAddons(ctx, &eks.ListAddonsInput{ClusterName: aws.String("nope")}); !errors.As(err, &rnf) {
		t.Fatalf("ListAddons(nope) err = %v, want ResourceNotFoundException", err)
	}

	// Re-registering a name replaces it rather than duplicating it.
	m3 := NewEKSAPI().WithCluster("prod", "1.31").WithCluster("prod", "1.32").Build()
	list, err := m3.ListClusters(ctx, &eks.ListClustersInput{})
	if err != nil || len(list.Clusters) != 1 {
		t.Fatalf("ListClusters after re-register = %v, %v", list, err)
	}
	if c, _ := m3.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String("prod")}); aws.ToString(c.Cluster.Version) != "1.32" {
		t.Fatalf("re-registered version = %s, want 1.32", aws.ToString(c.Cluster.Version))
	}

	list, err = m.ListClusters(ctx, &eks.ListClustersInput{})
	if err != nil || strings.Join(list.Clusters, ",") != "prod,staging" {
		t.Fatalf("ListClusters = %v, %v; want registered clusters in order", list, err)
	}
}

func TestWithCluster_RealisticEndpoint(t *testing.T) {
	m := NewEKSAPI().
		WithCluster("prod", "1.32").
		WithCluster("eu", "1.32", ClusterRegion("eu-west-1")).
		WithCluster("custom", "1.32", ClusterEndpoint("https://example.test"), ClusterStatus(ekstypes.ClusterStatusUpdating)).
		Build()
	ctx := context.Background()
	endpoint := regexp.MustCompile(`^https://[0-9A-F]{32}\.gr7\.([a-z0-9-]+)\.eks\.amazonaws\.com$`)

	for name, region := range map[string]string{"prod": DefaultRegion, "eu": "eu-west-1"} {
		out, err := m.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String(name)})
		if err != nil {
			t.Fatal(err)
		}
		match := endpoint.FindStringSubmatch(aws.ToString(out.Cluster.Endpoint))
		if match == nil || match[1] != region {
			t.Fatalf("%s endpoint = %q, want EKS shape in %s", name, aws.ToString(out.Cluster.Endpoint), region)
		}
		if !strings.Contains(aws.ToString(out.Cluster.Arn), ":"+region+":") {
			t.Fatalf("%s arn = %q, want region %s", name, aws.ToString(out.Cluster.Arn), region)
		}
	}
	custom, _ := m.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String("custom")})
	if aws.ToString(custom.Cluster.Endpoint) != "https://example.test" || custom.Cluster.Status != ekstypes.ClusterStatusUpdating {
		t.Fatalf("custom cluster = %#v", custom.Cluster)
	}

	a, _ := m.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String("prod")})
	b, _ := m.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String("prod")})
	eu, _ := m.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String("eu")})
	if aws.ToString(a.Cluster.Endpoint) != aws.ToString(b.Cluster.Endpoint) || aws.ToString(a.Cluster.Endpoint) == aws.ToString(eu.Cluster.Endpoint) {
		t.Fatal("endpoints must be stable per cluster and distinct across clusters")
	}
	// Mutating a returned cluster must not leak into the next call.
	a.Cluster.Version = aws.String("9.99")
	if c, _ := m.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String("prod")}); aws.ToString(c.Cluster.Version) != "1.32" {
		t.Fatal("DescribeCluster returned shared state")
	}
}

func TestDescribeClusterVersions_OnlyConfiguredVersions(t *testing.T) {
	ctx := context.Background()
	m := NewEKSAPI().Build()

	out, err := m.DescribeClusterVersions(ctx, &eks.DescribeClusterVersionsInput{ClusterVersions: []string{"1.99"}})
	if err != nil || len(out.ClusterVersions) != 0 {
		t.Fatalf("1.99 = %#v, %v; want not offered", out, err)
	}
	out, _ = m.DescribeClusterVersions(ctx, &eks.DescribeClusterVersionsInput{ClusterVersions: []string{"1.33"}})
	if len(out.ClusterVersions) != 1 || aws.ToString(out.ClusterVersions[0].ClusterVersion) != "1.33" {
		t.Fatalf("1.33 = %#v, want offered", out)
	}
	all, _ := m.DescribeClusterVersions(ctx, &eks.DescribeClusterVersionsInput{})
	if len(all.ClusterVersions) != len(DefaultSupportedVersions) {
		t.Fatalf("all = %d versions, want %d", len(all.ClusterVersions), len(DefaultSupportedVersions))
	}
	first, last := all.ClusterVersions[0], all.ClusterVersions[len(all.ClusterVersions)-1]
	if first.Status != ekstypes.ClusterVersionStatusExtendedSupport || last.Status != ekstypes.ClusterVersionStatusStandardSupport || !last.DefaultVersion {
		t.Fatalf("support statuses: first=%s last=%s default=%v", first.Status, last.Status, last.DefaultVersion)
	}

	m = NewEKSAPI().WithSupportedVersions("1.99").Build()
	out, _ = m.DescribeClusterVersions(ctx, &eks.DescribeClusterVersionsInput{ClusterVersions: []string{"1.99", "1.33"}})
	if len(out.ClusterVersions) != 1 || aws.ToString(out.ClusterVersions[0].ClusterVersion) != "1.99" {
		t.Fatalf("custom catalogue = %#v, want only 1.99", out)
	}
}

func TestWithInsight_KeyedByCluster(t *testing.T) {
	ctx := context.Background()
	m := NewEKSAPI().
		WithCluster("prod", "1.31").
		WithCluster("dev", "1.31").
		WithInsight("prod", "Deprecated APIs", ekstypes.InsightStatusValueError, "1.32").
		WithInsight("prod", "Kubelet skew", ekstypes.InsightStatusValuePassing, "1.33").
		Build()

	names := func(cluster string, filter *ekstypes.InsightsFilter) []string {
		t.Helper()
		out, err := m.ListInsights(ctx, &eks.ListInsightsInput{ClusterName: aws.String(cluster), Filter: filter})
		if err != nil {
			t.Fatalf("ListInsights(%s): %v", cluster, err)
		}
		var got []string
		for _, i := range out.Insights {
			if i.Id == nil || i.Category != ekstypes.CategoryUpgradeReadiness {
				t.Fatalf("insight %#v missing id or category", i)
			}
			got = append(got, aws.ToString(i.Name))
		}
		return got
	}
	if got := names("prod", nil); len(got) != 2 {
		t.Fatalf("prod insights = %v, want 2", got)
	}
	if got := names("dev", nil); len(got) != 0 {
		t.Fatalf("dev insights = %v, want none (insights are per cluster)", got)
	}
	if got := names("prod", &ekstypes.InsightsFilter{KubernetesVersions: []string{"1.32"}}); len(got) != 1 || got[0] != "Deprecated APIs" {
		t.Fatalf("prod@1.32 = %v", got)
	}
	if got := names("prod", &ekstypes.InsightsFilter{Statuses: []ekstypes.InsightStatusValue{ekstypes.InsightStatusValuePassing}}); len(got) != 1 || got[0] != "Kubelet skew" {
		t.Fatalf("prod PASSING = %v", got)
	}
	if got := names("prod", &ekstypes.InsightsFilter{Categories: []ekstypes.Category{ekstypes.CategoryMisconfiguration}}); len(got) != 0 {
		t.Fatalf("prod MISCONFIGURATION = %v, want none", got)
	}
	if _, err := m.ListInsights(ctx, &eks.ListInsightsInput{ClusterName: aws.String("ghost")}); !errors.As(err, new(*ekstypes.ResourceNotFoundException)) {
		t.Fatalf("ghost err = %v, want ResourceNotFoundException", err)
	}
}

func TestWithUpdateStatuses_ScriptedPerID(t *testing.T) {
	ctx := context.Background()
	m := NewEKSAPI().
		WithUpdateStatuses("u-a", ekstypes.UpdateStatusInProgress, ekstypes.UpdateStatusInProgress, ekstypes.UpdateStatusSuccessful).
		WithUpdateStatuses("u-b", ekstypes.UpdateStatusFailed).
		Build()

	status := func(id string) ekstypes.UpdateStatus {
		t.Helper()
		out, err := m.DescribeUpdate(ctx, &eks.DescribeUpdateInput{UpdateId: aws.String(id)})
		if err != nil {
			t.Fatalf("DescribeUpdate(%s): %v", id, err)
		}
		if aws.ToString(out.Update.Id) != id {
			t.Fatalf("id = %s, want %s", aws.ToString(out.Update.Id), id)
		}
		return out.Update.Status
	}
	want := []ekstypes.UpdateStatus{
		ekstypes.UpdateStatusInProgress, ekstypes.UpdateStatusInProgress,
		ekstypes.UpdateStatusSuccessful, ekstypes.UpdateStatusSuccessful, // last repeats
	}
	for i, w := range want {
		if got := status("u-a"); got != w {
			t.Fatalf("u-a poll %d = %s, want %s", i, got, w)
		}
		if got := status("u-b"); got != ekstypes.UpdateStatusFailed {
			t.Fatalf("u-b poll %d = %s, want Failed (independent of u-a)", i, got)
		}
	}
	if _, err := m.DescribeUpdate(ctx, &eks.DescribeUpdateInput{UpdateId: aws.String("u-zzz")}); !errors.As(err, new(*ekstypes.ResourceNotFoundException)) {
		t.Fatalf("unknown update err = %v, want ResourceNotFoundException", err)
	}

	defer func() {
		if recover() == nil {
			t.Fatal("WithUpdateStatuses with no statuses should panic")
		}
	}()
	NewEKSAPI().WithUpdateStatuses("u-empty")
}

func TestErrorHelpersAreTypedAPIErrors(t *testing.T) {
	cases := []struct {
		err   error
		code  string
		fault smithy.ErrorFault
	}{
		{AccessDenied(), "AccessDeniedException", smithy.FaultClient},
		{Throttling(), "ThrottlingException", smithy.FaultClient},
		{NotFound(), "ResourceNotFoundException", smithy.FaultClient},
		{APIError("InternalFailure", "boom"), "InternalFailure", smithy.FaultServer},
		{APIError("ServiceUnavailableException", "busy"), "ServiceUnavailableException", smithy.FaultServer},
		{APIError("InvalidParameterException", "bad"), "InvalidParameterException", smithy.FaultClient},
	}
	for _, tc := range cases {
		var ae smithy.APIError
		if !errors.As(fmt.Errorf("wrapped: %w", tc.err), &ae) {
			t.Fatalf("%v is not a smithy.APIError", tc.err)
		}
		if ae.ErrorCode() != tc.code || ae.ErrorFault() != tc.fault {
			t.Fatalf("%v: code=%s fault=%v, want %s %v", tc.err, ae.ErrorCode(), ae.ErrorFault(), tc.code, tc.fault)
		}
	}
	if !errors.As(NotFound(), new(*ekstypes.ResourceNotFoundException)) {
		t.Fatal("NotFound must be the EKS-modelled exception type")
	}
}

func TestPageSize_SplitsEveryListCall(t *testing.T) {
	ctx := context.Background()
	m := NewEKSAPI().
		WithPageSize(2).
		WithCluster("c1", "1.32").WithCluster("c2", "1.32").WithCluster("c3", "1.32").
		WithNodegroup("ng1", "1.32", ekstypes.AMITypesAl2023X8664Standard).
		WithNodegroup("ng2", "1.32", ekstypes.AMITypesAl2023X8664Standard).
		WithNodegroup("ng3", "1.32", ekstypes.AMITypesAl2023X8664Standard).
		WithNodegroup("ng4", "1.32", ekstypes.AMITypesAl2023X8664Standard).
		WithAddon("vpc-cni", "v1", ekstypes.AddonStatusActive).
		WithAddon("coredns", "v1", ekstypes.AddonStatusActive).
		WithAddon("kube-proxy", "v1", ekstypes.AddonStatusActive).
		WithAddonVersions("vpc-cni", []string{"v5", "v4", "v3", "v2", "v1"}, "1.32").
		WithInsight("c1", "i1", ekstypes.InsightStatusValuePassing, "1.33").
		WithInsight("c1", "i2", ekstypes.InsightStatusValuePassing, "1.33").
		WithInsight("c1", "i3", ekstypes.InsightStatusValuePassing, "1.33").
		Build()

	type page struct {
		items []string
		next  *string
	}
	drain := func(name string, call func(token *string) (page, error)) []string {
		t.Helper()
		var all []string
		var token *string
		pages := 0
		for {
			p, err := call(token)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if len(p.items) > 2 {
				t.Fatalf("%s page %d has %d items, want <= 2", name, pages, len(p.items))
			}
			pages++
			all = append(all, p.items...)
			if p.next == nil {
				break
			}
			token = p.next
		}
		if pages < 2 {
			t.Fatalf("%s: %d page(s), want paging", name, pages)
		}
		return all
	}

	clusters := drain("ListClusters", func(tok *string) (page, error) {
		out, err := m.ListClusters(ctx, &eks.ListClustersInput{NextToken: tok})
		if err != nil {
			return page{}, err
		}
		return page{out.Clusters, out.NextToken}, nil
	})
	nodegroups := drain("ListNodegroups", func(tok *string) (page, error) {
		out, err := m.ListNodegroups(ctx, &eks.ListNodegroupsInput{ClusterName: aws.String("c1"), NextToken: tok})
		if err != nil {
			return page{}, err
		}
		return page{out.Nodegroups, out.NextToken}, nil
	})
	addons := drain("ListAddons", func(tok *string) (page, error) {
		out, err := m.ListAddons(ctx, &eks.ListAddonsInput{ClusterName: aws.String("c1"), NextToken: tok})
		if err != nil {
			return page{}, err
		}
		return page{out.Addons, out.NextToken}, nil
	})
	insights := drain("ListInsights", func(tok *string) (page, error) {
		out, err := m.ListInsights(ctx, &eks.ListInsightsInput{ClusterName: aws.String("c1"), NextToken: tok})
		if err != nil {
			return page{}, err
		}
		var names []string
		for _, i := range out.Insights {
			names = append(names, aws.ToString(i.Name))
		}
		return page{names, out.NextToken}, nil
	})
	versions := drain("DescribeAddonVersions", func(tok *string) (page, error) {
		out, err := m.DescribeAddonVersions(ctx, &eks.DescribeAddonVersionsInput{AddonName: aws.String("vpc-cni"), NextToken: tok})
		if err != nil {
			return page{}, err
		}
		var vs []string
		for _, a := range out.Addons {
			if aws.ToString(a.AddonName) != "vpc-cni" {
				t.Fatalf("page carries addon %q", aws.ToString(a.AddonName))
			}
			for _, v := range a.AddonVersions {
				vs = append(vs, aws.ToString(v.AddonVersion))
			}
		}
		return page{vs, out.NextToken}, nil
	})

	for name, tc := range map[string]struct{ got, want []string }{
		"clusters":   {clusters, []string{"c1", "c2", "c3"}},
		"nodegroups": {nodegroups, []string{"ng1", "ng2", "ng3", "ng4"}},
		"addons":     {addons, []string{"vpc-cni", "coredns", "kube-proxy"}},
		"insights":   {insights, []string{"i1", "i2", "i3"}},
		"versions":   {versions, []string{"v5", "v4", "v3", "v2", "v1"}},
	} {
		if strings.Join(tc.got, ",") != strings.Join(tc.want, ",") {
			t.Fatalf("%s = %v, want %v", name, tc.got, tc.want)
		}
	}

	// An unknown or out-of-range token is rejected, as EKS rejects it.
	var ipe *ekstypes.InvalidParameterException
	for _, tok := range []string{"garbage", pageTokenPrefix + "99", pageTokenPrefix + "-1"} {
		if _, err := m.ListNodegroups(ctx, &eks.ListNodegroupsInput{ClusterName: aws.String("c1"), NextToken: aws.String(tok)}); !errors.As(err, &ipe) {
			t.Fatalf("token %q err = %v, want InvalidParameterException", tok, err)
		}
		if _, err := m.ListClusters(ctx, &eks.ListClustersInput{NextToken: aws.String(tok)}); !errors.As(err, &ipe) {
			t.Fatalf("ListClusters token %q err = %v, want InvalidParameterException", tok, err)
		}
		if _, err := m.ListAddons(ctx, &eks.ListAddonsInput{ClusterName: aws.String("c1"), NextToken: aws.String(tok)}); !errors.As(err, &ipe) {
			t.Fatalf("ListAddons token %q err = %v, want InvalidParameterException", tok, err)
		}
		if _, err := m.ListInsights(ctx, &eks.ListInsightsInput{ClusterName: aws.String("c1"), NextToken: aws.String(tok)}); !errors.As(err, &ipe) {
			t.Fatalf("ListInsights token %q err = %v, want InvalidParameterException", tok, err)
		}
		if _, err := m.DescribeAddonVersions(ctx, &eks.DescribeAddonVersionsInput{AddonName: aws.String("vpc-cni"), NextToken: aws.String(tok)}); !errors.As(err, &ipe) {
			t.Fatalf("DescribeAddonVersions token %q err = %v, want InvalidParameterException", tok, err)
		}
	}
}

func TestPageSize_PassThroughAndEdgeCases(t *testing.T) {
	ctx := context.Background()
	m := NewEKSAPI().WithPageSize(1).Build()

	// A Fn that pages by hand keeps its own token, for every list call.
	custom := aws.String("custom")
	m.ListClustersFn = func(context.Context, *eks.ListClustersInput, ...func(*eks.Options)) (*eks.ListClustersOutput, error) {
		return &eks.ListClustersOutput{Clusters: []string{"a", "b"}, NextToken: custom}, nil
	}
	m.ListAddonsFn = func(context.Context, *eks.ListAddonsInput, ...func(*eks.Options)) (*eks.ListAddonsOutput, error) {
		return &eks.ListAddonsOutput{Addons: []string{"a", "b"}, NextToken: custom}, nil
	}
	m.ListNodegroupsFn = func(context.Context, *eks.ListNodegroupsInput, ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
		return &eks.ListNodegroupsOutput{Nodegroups: []string{"a", "b"}, NextToken: custom}, nil
	}
	m.ListInsightsFn = func(context.Context, *eks.ListInsightsInput, ...func(*eks.Options)) (*eks.ListInsightsOutput, error) {
		return &eks.ListInsightsOutput{Insights: make([]ekstypes.InsightSummary, 2), NextToken: custom}, nil
	}
	m.DescribeAddonVersionsFn = func(context.Context, *eks.DescribeAddonVersionsInput, ...func(*eks.Options)) (*eks.DescribeAddonVersionsOutput, error) {
		return &eks.DescribeAddonVersionsOutput{Addons: make([]ekstypes.AddonInfo, 2), NextToken: custom}, nil
	}
	c, _ := m.ListClusters(ctx, &eks.ListClustersInput{})
	a, _ := m.ListAddons(ctx, &eks.ListAddonsInput{})
	n, _ := m.ListNodegroups(ctx, &eks.ListNodegroupsInput{})
	i, _ := m.ListInsights(ctx, &eks.ListInsightsInput{})
	v, _ := m.DescribeAddonVersions(ctx, &eks.DescribeAddonVersionsInput{})
	if len(c.Clusters) != 2 || len(a.Addons) != 2 || len(n.Nodegroups) != 2 || len(i.Insights) != 2 || len(v.Addons) != 2 ||
		c.NextToken != custom || a.NextToken != custom || n.NextToken != custom || i.NextToken != custom || v.NextToken != custom {
		t.Fatal("hand-paged results must pass through unchanged")
	}

	// Errors pass through.
	m.ListAddonsFn = func(context.Context, *eks.ListAddonsInput, ...func(*eks.Options)) (*eks.ListAddonsOutput, error) {
		return nil, Throttling()
	}
	if _, err := m.ListAddons(ctx, &eks.ListAddonsInput{}); err == nil {
		t.Fatal("ListAddons error swallowed")
	}

	// An addon listed with no versions still appears on its page.
	m.DescribeAddonVersionsFn = func(context.Context, *eks.DescribeAddonVersionsInput, ...func(*eks.Options)) (*eks.DescribeAddonVersionsOutput, error) {
		return &eks.DescribeAddonVersionsOutput{Addons: []ekstypes.AddonInfo{
			{AddonName: aws.String("empty")},
			{AddonName: aws.String("full"), AddonVersions: []ekstypes.AddonVersionInfo{{AddonVersion: aws.String("v1")}}},
		}}, nil
	}
	p1, err := m.DescribeAddonVersions(ctx, &eks.DescribeAddonVersionsInput{})
	if err != nil || len(p1.Addons) != 1 || aws.ToString(p1.Addons[0].AddonName) != "empty" || p1.NextToken == nil {
		t.Fatalf("page 1 = %#v, %v", p1, err)
	}
	p2, err := m.DescribeAddonVersions(ctx, &eks.DescribeAddonVersionsInput{NextToken: p1.NextToken})
	if err != nil || len(p2.Addons) != 1 || aws.ToString(p2.Addons[0].AddonName) != "full" || p2.NextToken != nil {
		t.Fatalf("page 2 = %#v, %v", p2, err)
	}

	// An empty result is one page with no token.
	empty := NewEKSAPI().WithPageSize(3).Build()
	ng, err := empty.ListNodegroups(ctx, &eks.ListNodegroupsInput{})
	if err != nil || len(ng.Nodegroups) != 0 || ng.NextToken != nil {
		t.Fatalf("empty ListNodegroups = %#v, %v", ng, err)
	}
}
