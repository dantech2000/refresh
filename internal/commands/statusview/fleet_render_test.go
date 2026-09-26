package statusview

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/render"
	statussvc "github.com/dantech2000/refresh/internal/services/status"
	"github.com/dantech2000/refresh/internal/ui"
	"github.com/dantech2000/refresh/internal/ui/plaintest"
)

func iptr(i int) *int { return &i }

func sampleFleet() []statussvc.ClusterStatus {
	return []statussvc.ClusterStatus{
		{ // current: standard support, nothing stale
			Name: "prod-east", Region: "us-east-1", Version: "1.32",
			Support:        statussvc.SupportPosture{Tier: statussvc.SupportStandard, DaysRemaining: iptr(200)},
			Compute:        statussvc.ComputeManaged,
			NodegroupCount: 3,
		},
		{ // warning: standard support but an addon behind
			Name: "staging", Region: "us-west-2", Version: "1.31",
			Support:        statussvc.SupportPosture{Tier: statussvc.SupportStandard, DaysRemaining: iptr(320)},
			Compute:        statussvc.ComputeManaged,
			NodegroupCount: 2,
			AddonsBehind:   statussvc.AddonsBehindSummary{Total: 4, Behind: 1, Names: []string{"kube-proxy"}},
		},
		{ // failure: unsupported EKS + stale AMIs
			Name: "data-eu", Region: "eu-central-1", Version: "1.29",
			Support:        statussvc.SupportPosture{Tier: statussvc.SupportUnsupported},
			Compute:        statussvc.ComputeManaged,
			NodegroupCount: 5,
			StaleAMI:       statussvc.StaleAMISummary{Total: 5, Behind: 5, OldestDays: iptr(47)},
			AddonsBehind:   statussvc.AddonsBehindSummary{Total: 4, Behind: 2, Names: []string{"vpc-cni", "coredns"}},
		},
	}
}

func TestFleetLines_HealthIssueHint(t *testing.T) {
	th := render.New(render.ColorNone, true)
	fleet := []statussvc.ClusterStatus{{
		Name: "prod-east", Region: "us-east-1", Version: "1.32",
		Support:      statussvc.SupportPosture{Tier: statussvc.SupportStandard, DaysRemaining: iptr(200)},
		Compute:      statussvc.ComputeManaged,
		HealthIssues: 2,
	}}
	joined := strings.Join(fleetLines(th, fleet, nil, 0), "\n")
	// A health-issue cluster is flagged "need attention" and the hint names the cause.
	mustContain(t, joined, "▲  prod-east")
	mustContain(t, joined, "has 2 control-plane health issue(s)")
	// The row itself shows the cause in the HEALTH column (health issues alone
	// make status exit 2).
	mustContain(t, joined, "HEALTH")
	mustContain(t, joined, "2 issue(s)")

	rows := plaintest.Check(t, fleetPlainOut(t, fleet), fleetPlainHeaders...)
	if got := rows[0][7]; got != "2 issue(s)" {
		t.Errorf("plain HEALTH cell = %q, want \"2 issue(s)\"", got)
	}
}

func TestFleetLines_Pretty(t *testing.T) {
	th := render.New(render.ColorNone, true) // deterministic: glyphs, no ANSI
	lines := fleetLines(th, sampleFleet(), nil, 0)
	joined := strings.Join(lines, "\n")

	// No color escapes leak under ColorNone (additive-color contract).
	if strings.Contains(joined, "\x1b") {
		t.Fatalf("ColorNone output contains ANSI escapes:\n%s", joined)
	}

	// Header + summary chips (chips have no column padding, so exact-match).
	if lines[0] != "FLEET  3 clusters · 3 regions" {
		t.Errorf("header = %q", lines[0])
	}
	if lines[2] != "● 1 current   ▲ 1 need attention   ✗ 1 unsupported" {
		t.Errorf("chips = %q", lines[2])
	}

	// Each cluster's row carries its status glyph (its own column, 2-space gap)
	// + the salient facts.
	mustContain(t, joined, "●  prod-east")
	mustContain(t, joined, "▲  staging")
	mustContain(t, joined, "✗  data-eu")
	mustContain(t, joined, "5/5 (47d)")           // stale AMI cell
	mustContain(t, joined, "2 (vpc-cni,coredns)") // addons-behind cell
	mustContain(t, joined, "standard (200d)")     // support cell

	// Footer aggregates and the next-step hint point at the worst cluster.
	mustContain(t, joined, "3 clusters · 5 stale nodegroups · 3 addons behind · 1 extended/unsupported")
	mustContain(t, joined, "refresh cluster upgrade-check -c data-eu -r eu-central-1")
}

func TestFleetLines_ASCIIFallback(t *testing.T) {
	th := render.New(render.ColorNone, false) // non-UTF-8 terminal
	joined := strings.Join(fleetLines(th, sampleFleet(), nil, 0), "\n")
	// Glyphs degrade to ASCII tokens; meaning preserved without color or Unicode.
	mustContain(t, joined, "[X] data-eu")
	mustContain(t, joined, "[OK] 1 current")
	if strings.ContainsAny(joined, "●▲✗") {
		t.Errorf("ASCII fallback still contains Unicode glyphs:\n%s", joined)
	}
}

func TestFleetLines_AllHealthy(t *testing.T) {
	th := render.New(render.ColorNone, true)
	healthy := []statussvc.ClusterStatus{{
		Name: "ok", Region: "us-east-1", Version: "1.33",
		Support: statussvc.SupportPosture{Tier: statussvc.SupportStandard, DaysRemaining: iptr(300)},
		Compute: statussvc.ComputeManaged, NodegroupCount: 1,
	}}
	lines := fleetLines(th, healthy, nil, 0)
	joined := strings.Join(lines, "\n")
	// Chips show only the "current" count; no warn/fail chips.
	if lines[2] != "● 1 current" {
		t.Errorf("chips = %q, want %q", lines[2], "● 1 current")
	}
	// No next-step hint when everything is current.
	if strings.Contains(joined, "upgrade-check") {
		t.Errorf("healthy fleet should not emit a hint:\n%s", joined)
	}
}

// An incomplete row (failed DescribeCluster, or a cluster the sweep never
// reached) must not render as current: it gets the unknown glyph, its own chip,
// a footer count, and its failure in the INCOMPLETE DATA section.
func TestFleetLines_IncompleteRow(t *testing.T) {
	th := render.New(render.ColorNone, true)
	fleet := []statussvc.ClusterStatus{
		{
			Name: "ok", Region: "us-east-1", Version: "1.33",
			Support: statussvc.SupportPosture{Tier: statussvc.SupportStandard, DaysRemaining: iptr(300)},
			Compute: statussvc.ComputeManaged, NodegroupCount: 1,
		},
		{
			Name: "ghost", Region: "us-west-2",
			Support:    statussvc.SupportPosture{Tier: statussvc.SupportUnknown},
			Compute:    statussvc.ComputeNone,
			Incomplete: true,
		},
	}
	failures := []diag.Failure{{
		Kind: diag.KindCluster, Name: "ghost", Region: "us-west-2", Operation: diag.OpDescribeCluster,
		Reason: diag.ReasonAccessDenied, Error: "AccessDeniedException: denied",
	}}
	lines := fleetLines(th, fleet, failures, 0)
	joined := strings.Join(lines, "\n")
	if lines[2] != "● 1 current   ○ 1 incomplete" {
		t.Errorf("chips = %q", lines[2])
	}
	mustContain(t, joined, "○  ghost")
	mustContain(t, joined, "INCOMPLETE DATA")
	mustContain(t, joined, "○ cluster ghost (us-west-2): AccessDenied: AccessDeniedException: denied")
	mustContain(t, joined, "· 1 incomplete")
}

func TestOverall_IncompleteRowIsNotHealthy(t *testing.T) {
	c := statussvc.ClusterStatus{
		Name:       "ghost",
		Support:    statussvc.SupportPosture{Tier: statussvc.SupportUnknown},
		Incomplete: true,
	}
	if got := overall(c); got != render.Unknown {
		t.Errorf("overall = %v, want render.Unknown", got)
	}
}

func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("output missing %q in:\n%s", needle, haystack)
	}
}

func TestFleetLines_NodegroupsBehindControlPlane(t *testing.T) {
	th := render.New(render.ColorNone, true)
	fleet := []statussvc.ClusterStatus{{
		Name: "prod-east", Region: "us-east-1", Version: "1.32",
		Support:                      statussvc.SupportPosture{Tier: statussvc.SupportStandard, DaysRemaining: iptr(200)},
		Compute:                      statussvc.ComputeManaged,
		NodegroupCount:               2,
		NodegroupsBehindControlPlane: 1,
	}}
	joined := strings.Join(fleetLines(th, fleet, nil, 0), "\n")
	mustContain(t, joined, "▲  prod-east")
	mustContain(t, joined, "1 nodegroup behind control plane")
	mustContain(t, joined, "has 1 nodegroup(s) behind the control plane")
	// The STALE AMI cell gives the row's reason, not a bare "0".
	mustContain(t, joined, "▲ 0 · 1 behind CP")

	// -o plain carries the same text in the same column.
	if got := staleAMICell(fleet[0]); !strings.Contains(got, "0 · 1 behind CP") {
		t.Errorf("plain STALE AMI cell = %q, want it to name the nodegroup behind CP", got)
	}
	fleet[0].StaleAMI = statussvc.StaleAMISummary{Total: 2, Behind: 1, OldestDays: iptr(30)}
	if got := staleAMICell(fleet[0]); !strings.Contains(got, "1/2 (30d) · 1 behind CP") {
		t.Errorf("plain STALE AMI cell = %q", got)
	}
	if got := stalePretty(th, fleet[0]); !strings.Contains(got, "1/2 (30d) · 1 behind CP") {
		t.Errorf("pretty STALE AMI cell = %q", got)
	}
}

// The -o plain table keeps its column count (see fleetPlainHeaders) when a nodegroup lags
// the control plane, so awk/cut scripts don't break.
func TestOutputFleetPlain_BehindCPKeepsColumnCount(t *testing.T) {
	fleet := []statussvc.ClusterStatus{{
		Name: "prod-east", Region: "us-east-1", Version: "1.32",
		Support:                      statussvc.SupportPosture{Tier: statussvc.SupportStandard},
		Compute:                      statussvc.ComputeManaged,
		NodegroupCount:               2,
		NodegroupsBehindControlPlane: 2,
	}}
	rows := plaintest.Check(t, fleetPlainOut(t, fleet), fleetPlainHeaders...)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if !strings.Contains(rows[0][5], "0 · 2 behind CP") {
		t.Errorf("STALE AMI cell = %q, want \"0 · 2 behind CP\"", rows[0][5])
	}
}

var fleetPlainHeaders = []string{"CLUSTER", "REGION", "VERSION", "SUPPORT", "COMPUTE", "STALE AMI", "ADDONS", "HEALTH"}

// fleetPlainOut runs OutputFleetTable under -o plain and returns stdout.
func fleetPlainOut(t *testing.T, fleet []statussvc.ClusterStatus) string {
	t.Helper()
	ui.SetPlainOutput(true)
	defer ui.SetPlainOutput(false)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	perr := OutputFleetTable(fleet, nil, time.Second)
	os.Stdout = orig
	_ = w.Close()
	out, _ := io.ReadAll(r)
	if perr != nil {
		t.Fatalf("OutputFleetTable: %v", perr)
	}
	return string(out)
}

// -o plain is pure TSV: no footer, no glyphs, the human table's column names
// and vocabulary, and nothing truncated.
func TestOutputFleetPlain_Contract(t *testing.T) {
	fleet := sampleFleet()
	fleet = append(fleet,
		statussvc.ClusterStatus{
			Name: "auto", Region: "us-east-1", Version: "1.31",
			Support:      statussvc.SupportPosture{Tier: statussvc.SupportExtended, DaysRemaining: iptr(40), ExtraCostUSDPerHour: 0.5},
			Compute:      statussvc.ComputeAutoMode,
			AddonsBehind: statussvc.AddonsBehindSummary{Total: 5, Behind: 3, Names: []string{"a", "b", "c"}},
		},
		statussvc.ClusterStatus{Name: "karp", Region: "us-east-1", Version: "1.32", Compute: statussvc.ComputeKarpenter},
		statussvc.ClusterStatus{Name: "bare", Region: "us-east-1", Version: "1.32", Incomplete: true},
	)
	out := fleetPlainOut(t, fleet)
	rows := plaintest.Check(t, out, fleetPlainHeaders...)
	if len(rows) != len(fleet) {
		t.Fatalf("got %d rows, want %d (no footer):\n%s", len(rows), len(fleet), out)
	}
	for _, glyph := range []string{"⚠", "✖", "🤖", "✔", "▲", "●"} {
		if strings.Contains(out, glyph) {
			t.Errorf("plain output contains glyph %q:\n%s", glyph, out)
		}
	}
	byName := map[string][]string{}
	for _, r := range rows {
		byName[r[0]] = r
	}
	for name, want := range map[string][]string{
		"data-eu": {"unsupported", "5 nodegroups", "5/5 (47d)", "2 (vpc-cni,coredns)"},
		"auto":    {"extended (40d) +$0.50/hr", "Auto Mode", "n/a", "3 (a,b,c)"},
		"karp":    {"unknown", "Karpenter", "n/a", "0"},
		"bare":    {"unknown", "none", "n/a", "0"},
	} {
		if got := byName[name][3:7]; strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("%s SUPPORT..ADDONS = %q, want %q", name, got, want)
		}
	}

	// Every header is a human table column, in the same order. Failures go
	// to stderr, not into a column.
	human := fleetLines(render.New(render.ColorNone, true), fleet, nil, 0)
	var headerLine string
	for _, l := range human {
		if strings.Contains(l, "CLUSTER") && strings.Contains(l, "STALE AMI") {
			headerLine = l
		}
	}
	rest := headerLine
	for _, h := range fleetPlainHeaders {
		i := strings.Index(rest, h)
		if i < 0 {
			t.Fatalf("human header %q lacks %q (in order)", headerLine, h)
		}
		rest = rest[i+len(h):]
	}
}

// An Auto Mode cluster can also run managed nodegroups. Their stale AMIs
// count in the footer and make status exit 2, so the row shows them instead
// of "n/a", and COMPUTE names the nodegroups.
func TestFleetLines_AutoModeWithNodegroupsShowsStaleAMI(t *testing.T) {
	th := render.New(render.ColorNone, true)
	fleet := []statussvc.ClusterStatus{{
		Name: "auto", Region: "us-west-2", Version: "1.33",
		Support:        statussvc.SupportPosture{Tier: statussvc.SupportStandard, DaysRemaining: iptr(100)},
		Compute:        statussvc.ComputeAutoMode,
		NodegroupCount: 2,
		StaleAMI:       statussvc.StaleAMISummary{Total: 2, Behind: 1, OldestDays: iptr(30)},
	}}
	joined := strings.Join(fleetLines(th, fleet, nil, 0), "\n")
	mustContain(t, joined, "Auto Mode + 2 nodegroups")
	mustContain(t, joined, "▲ 1/2 (30d)")
	mustContain(t, joined, "1 stale nodegroup")

	rows := plaintest.Check(t, fleetPlainOut(t, fleet), fleetPlainHeaders...)
	if got := rows[0][4:6]; got[0] != "Auto Mode + 2 nodegroups" || got[1] != "1/2 (30d)" {
		t.Errorf("plain COMPUTE, STALE AMI = %q, want [Auto Mode + 2 nodegroups 1/2 (30d)]", got)
	}

	// One nodegroup reads in the singular.
	fleet[0].NodegroupCount = 1
	if got := computeCell(fleet[0]); got != "Auto Mode + 1 nodegroup" {
		t.Errorf("Auto Mode with one nodegroup: COMPUTE = %q, want Auto Mode + 1 nodegroup", got)
	}

	// Without nodegroups, Auto Mode has no AMIs of its own to report.
	fleet[0].NodegroupCount, fleet[0].StaleAMI = 0, statussvc.StaleAMISummary{}
	if got := staleAMICell(fleet[0]); got != "n/a" {
		t.Errorf("Auto Mode without nodegroups: STALE AMI = %q, want n/a", got)
	}
	if got := computeCell(fleet[0]); got != "Auto Mode" {
		t.Errorf("Auto Mode without nodegroups: COMPUTE = %q, want Auto Mode", got)
	}
}

// The next-step hint names the cluster's region, so it works after
// "status -A" for a cluster outside the default region.
func TestFleetLines_HintNamesRegion(t *testing.T) {
	th := render.New(render.ColorNone, true)
	fleet := []statussvc.ClusterStatus{{
		Name: "far", Region: "ap-southeast-2", Version: "1.33",
		Support:      statussvc.SupportPosture{Tier: statussvc.SupportStandard, DaysRemaining: iptr(100)},
		Compute:      statussvc.ComputeManaged,
		AddonsBehind: statussvc.AddonsBehindSummary{Total: 1, Behind: 1, Names: []string{"coredns"}},
	}}
	joined := strings.Join(fleetLines(th, fleet, nil, 0), "\n")
	mustContain(t, joined, "refresh cluster upgrade-check -c far -r ap-southeast-2")
}
