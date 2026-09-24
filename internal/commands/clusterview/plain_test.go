package clusterview

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/render"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/services/status"
	"github.com/dantech2000/refresh/internal/ui"
	"github.com/dantech2000/refresh/internal/ui/plaintest"
)

// plainOut renders t to a string.
func plainOut(t *ui.PlainTable) string {
	var buf bytes.Buffer
	t.Write(&buf)
	return buf.String()
}

// assertHumanHeaders fails unless one line of the human view carries every
// header, in order — the plain header must name the same columns.
func assertHumanHeaders(t *testing.T, human []string, headers ...string) {
	t.Helper()
	for _, l := range human {
		rest, ok := l, true
		for _, h := range headers {
			i := strings.Index(rest, h)
			if i < 0 {
				ok = false
				break
			}
			rest = rest[i+len(h):]
		}
		if ok {
			return
		}
	}
	t.Errorf("no human table header line carries %q:\n%s", headers, strings.Join(human, "\n"))
}

func TestClusterListPlain(t *testing.T) {
	th := render.New(render.ColorNone, true)
	summaries := sampleSummaries()
	summaries[0].Name = "prod\tweird\nname"
	for _, tc := range []struct {
		name                    string
		multiRegion, showHealth bool
		headers                 []string
	}{
		{"basic", false, false, []string{"CLUSTER", "STATUS", "VERSION", "NODES"}},
		{"health", false, true, []string{"CLUSTER", "STATUS", "VERSION", "HEALTH", "NODES"}},
		{"all-regions", true, true, []string{"CLUSTER", "REGION", "STATUS", "VERSION", "HEALTH", "NODES"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := plainOut(clusterListPlain(summaries, tc.multiRegion, tc.showHealth))
			rows := plaintest.Check(t, out, tc.headers...)
			if len(rows) != len(summaries) {
				t.Fatalf("got %d rows, want %d:\n%s", len(rows), len(summaries), out)
			}
			if rows[0][0] != "prod weird name" {
				t.Errorf("embedded tab/newline not escaped: %q", rows[0][0])
			}
			// Same vocabulary as the table: raw EKS status and the NODES count.
			if rows[0][len(rows[0])-1] != "3" {
				t.Errorf("NODES cell = %q, want 3 (desired count)", rows[0][len(rows[0])-1])
			}
			if !strings.Contains(out, "\tACTIVE\t") {
				t.Errorf("STATUS should be the raw EKS status:\n%s", out)
			}
			assertHumanHeaders(t, clusterListLines(th, summaries, tc.multiRegion, tc.showHealth), tc.headers...)
		})
	}
}

func TestOutputClustersTable_PlainEmptyIsHeaderOnly(t *testing.T) {
	ui.SetPlainOutput(true)
	defer ui.SetPlainOutput(false)
	out, err := captureStdout(t, func() error { return OutputClustersTable(nil, nil, time.Second, false, false) })
	if err != nil {
		t.Fatal(err)
	}
	if out != "CLUSTER\tSTATUS\tVERSION\tNODES\n" {
		t.Errorf("empty plain list should be the header only, got %q", out)
	}
}

func TestClusterDetailPlain(t *testing.T) {
	days := 120
	d := &clustersvc.ClusterDetails{
		Name: "prod", Status: "ACTIVE", Version: "1.30", PlatformVersion: "eks.1",
		Region:    "us-east-1",
		Endpoint:  "https://" + strings.Repeat("a", 150) + ".eks.amazonaws.com",
		CreatedAt: time.Now().Add(-72 * time.Hour),
		Support:   &status.SupportPosture{Tier: status.SupportStandard, DaysRemaining: &days},
		Networking: clustersvc.NetworkingInfo{
			VpcID: "vpc-1", SubnetIDs: []string{"subnet-1", "subnet-2"}, SecurityGroupIDs: []string{"sg-1"},
		},
		Security:   clustersvc.SecurityInfo{DeletionProtection: true},
		Nodegroups: &[]clustersvc.NodegroupSummary{{Name: "ng-a", Status: "ACTIVE", InstanceType: "m5.large", DesiredSize: 3}},
		Addons:     &[]clustersvc.AddonInfo{{Name: "vpc-cni", Version: "v1.18.3", Status: "ACTIVE", Health: "Healthy"}},
		HealthIssues: []clustersvc.HealthIssue{
			{Code: "AccessDenied", Message: "role\tmissing\npermission", ResourceIDs: []string{"arn:1"}},
		},
		Health: &health.HealthSummary{
			Decision: health.DecisionWarn, OverallScore: 80, Warnings: []string{"quota low"},
			Results: []health.HealthResult{{Name: "quota", Status: health.StatusWarn, Message: "quota low"}},
		},
	}
	rows := plaintest.Check(t, plainOut(clusterDetailPlain(d)), "FIELD", "VALUE")
	want := map[string]string{
		"name":                      "prod",
		"endpoint":                  d.Endpoint,
		"support":                   "standard (120d)",
		"subnets":                   "subnet-1,subnet-2",
		"deletion protection":       "enabled",
		"encryption":                "disabled",
		"nodegroup/ng-a":            "instance=m5.large nodes=3 status=ACTIVE",
		"addon/vpc-cni":             "version=v1.18.3 status=ACTIVE health=Healthy",
		"health issue/AccessDenied": "role missing permission [arn:1]",
		"health":                    "WARN (80/100): quota low",
		"health check/quota":        "WARN: quota low",
	}
	for f, v := range want {
		if got, ok := plaintest.Field(rows, f); !ok || got != v {
			t.Errorf("field %q = %q (found=%v), want %q", f, got, ok, v)
		}
	}
}

func TestUpgradeCheckPlain(t *testing.T) {
	refreshed := time.Date(2026, 6, 14, 9, 30, 0, 0, time.UTC)
	report := &clustersvc.UpgradeReport{
		Cluster: "prod",
		Insights: []clustersvc.InsightSummary{
			{ID: "0123456789abcdef-full-id", Name: "Deprecated APIs", Category: "UPGRADE_READINESS", Status: "ERROR", KubernetesVersion: "1.31", LastRefreshTime: &refreshed},
			{ID: "fedcba", Name: "Kubelet skew", Category: "UPGRADE_READINESS", Status: "PASSING"},
		},
		Skew: clustersvc.SkewReport{ControlPlaneVersion: "1.30", Findings: []string{"ng-a is behind"}},
	}
	headers := []string{"ID", "NAME", "CATEGORY", "STATUS", "K8S", "LAST REFRESH"}
	rows := plaintest.Check(t, plainOut(upgradeCheckPlain(report)), headers...)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0][0] != "0123456789abcdef-full-id" {
		t.Errorf("plain must carry the full insight ID, got %q", rows[0][0])
	}
	if rows[1][4] != "-" || rows[1][5] != "-" {
		t.Errorf("empty cells should be '-', got %q", rows[1])
	}
	assertHumanHeaders(t, upgradeCheckLines(render.New(render.ColorNone, true), report), headers...)

	// The rest of the report goes to the info writer (stderr), not stdout.
	var info bytes.Buffer
	writeUpgradeCheckInfo(&info, report)
	for _, want := range []string{"NOT READY", "ng-a is behind"} {
		if !strings.Contains(info.String(), want) {
			t.Errorf("info missing %q:\n%s", want, info.String())
		}
	}
}

func TestInsightDetailPlain(t *testing.T) {
	last := time.Date(2026, 6, 14, 9, 30, 0, 0, time.UTC)
	d := &clustersvc.InsightDetail{
		InsightSummary: clustersvc.InsightSummary{
			ID: "id-1", Name: "Deprecated APIs", Category: "UPGRADE_READINESS", Status: "ERROR",
			Description: "multi\nline\n\tdescription",
		},
		Resources:      []string{"ns/a", "ns/b"},
		AdditionalInfo: map[string]string{"doc": "https://example.com"},
		Deprecations: []clustersvc.DeprecationDetail{{
			Usage: "policy/v1beta1 PodDisruptionBudget", ReplacedWith: "policy/v1", StopServingVersion: "1.25",
			ClientStats: []clustersvc.ClientStat{{UserAgent: "kube-state-metrics/v2", LastRequestTime: &last, NumberOfRequestsLast30Days: 412}},
		}},
	}
	rows := plaintest.Check(t, plainOut(insightDetailPlain(d)), "FIELD", "VALUE")
	for f, v := range map[string]string{
		"id":          "id-1",
		"description": "multi line description",
		"info/doc":    "https://example.com",
		"deprecated api/policy/v1beta1 PodDisruptionBudget":        "replacement=policy/v1 removed-in=1.25",
		"deprecated api client/policy/v1beta1 PodDisruptionBudget": "kube-state-metrics/v2: 412 req/30d, last seen 2026-06-14 09:30",
	} {
		if got, ok := plaintest.Field(rows, f); !ok || got != v {
			t.Errorf("field %q = %q (found=%v), want %q", f, got, ok, v)
		}
	}
}
