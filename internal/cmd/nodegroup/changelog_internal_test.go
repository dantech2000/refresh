package nodegroup

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// releaseServer serves body with status for every request and counts the requests.
func releaseServer(t *testing.T, status int, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("Accept header = %q, want application/vnd.github+json", got)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &requests
}

const threeReleases = `[
	{"tag_name":"v20260601","body":"- kernel 5.10.99\n- CVE-2026-1\n"},
	{"tag_name":"v20260301","body":"- containerd 1.7.0\n"},
	{"tag_name":"v20260101","body":"- old kernel, before current\n"}
]`

func TestReleaseDate(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"1.31.0-20260601", "20260601", true},
		{"v20260601", "20260601", true},
		{"no-date-here", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := releaseDate(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("releaseDate(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestSummarizeReleaseBody(t *testing.T) {
	// The generated notes are pull request titles, then HTML tables whose
	// cells name kernels. The summary keeps the titles, not the cells.
	generated := "<!-- Release notes generated -->\n\n## What's Changed\n" +
		"* fix(boot-hook): poll managed interfaces by @mselim00 in https://github.com/awslabs/amazon-eks-ami/pull/2821\n" +
		"* feat: use igzip for decompression by @fletcherw in https://github.com/awslabs/amazon-eks-ami/pull/2828\n\n" +
		"**Full Changelog**: https://github.com/awslabs/amazon-eks-ami/compare/v1...v2\n\n---\n\n<h2>AMI Details</h2>\n<table>\n" +
		"  <tr>\n    <td>al2023-ami-minimal-2023.12.20260918.0-kernel-6.18-arm64</td>\n  </tr>\n</table>\n"
	var many strings.Builder
	for i := range 9 {
		fmt.Fprintf(&many, "- kernel patch %d\n", i)
	}
	cases := []struct {
		name, body string
		want       []string
	}{
		{
			name: "plain text notes",
			body: "## Changes\n- Bump kernel to 5.10.99\n- update containerd to 1.7.0\n- Fix CVE-2026-1234\n- unrelated doc tweak\n",
			want: []string{"Bump kernel to 5.10.99", "update containerd to 1.7.0", "Fix CVE-2026-1234"},
		},
		{
			name: "generated notes",
			body: generated,
			want: []string{"fix(boot-hook): poll managed interfaces (#2821)", "feat: use igzip for decompression (#2828)"},
		},
		{
			name: "duplicates",
			body: "- runc 1.2\n- runc 1.2\n",
			want: []string{"runc 1.2"},
		},
		{
			name: "cap of six",
			body: many.String(),
			want: []string{"kernel patch 0", "kernel patch 1", "kernel patch 2", "kernel patch 3", "kernel patch 4", "kernel patch 5"},
		},
		{name: "empty", body: "", want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, summarizeReleaseBody(tc.body)); diff != "" {
				t.Errorf("summarizeReleaseBody (-want +got):\n%s", diff)
			}
		})
	}
}

func TestChangelogLinesFetched(t *testing.T) {
	srv, _ := releaseServer(t, http.StatusOK, threeReleases)
	lines, warnings := changelogLines(context.Background(), srv.Client(), srv.URL, []changelogTarget{
		{Nodegroup: "ng-a", AMIType: "AL2023_x86_64_STANDARD", Release: "1.31.0-20260201"},
	})
	want := []string{
		"nodegroup ng-a: 2 release(s) after 1.31.0-20260201",
		"  v20260601",
		"    - kernel 5.10.99",
		"    - CVE-2026-1",
		"  v20260301",
		"    - containerd 1.7.0",
	}
	if diff := cmp.Diff(want, lines); diff != "" {
		t.Errorf("lines (-want +got):\n%s", diff)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %q, want none", warnings)
	}
}

func TestChangelogLinesOnNewestRelease(t *testing.T) {
	srv, _ := releaseServer(t, http.StatusOK, threeReleases)
	lines, warnings := changelogLines(context.Background(), srv.Client(), srv.URL, []changelogTarget{
		{Nodegroup: "ng-a", AMIType: "AL2_x86_64", Release: "1.31.0-20260601"},
	})
	want := []string{"nodegroup ng-a: on the newest published release (1.31.0-20260601)"}
	if diff := cmp.Diff(want, lines); diff != "" {
		t.Errorf("lines (-want +got):\n%s", diff)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %q, want none", warnings)
	}
}

// A release without highlights counts, and at most 10 releases show their highlights.
func TestChangelogLinesNotesCap(t *testing.T) {
	var b strings.Builder
	b.WriteString(`[{"tag_name":"v20260420","body":"- docs only\n"},{"tag_name":"no-date","body":"- kernel\n"}`)
	for i := range 12 {
		fmt.Fprintf(&b, `,{"tag_name":"v202603%02d","body":"- kernel %d\n"}`, i+1, i)
	}
	b.WriteString("]")
	srv, _ := releaseServer(t, http.StatusOK, b.String())
	lines, warnings := changelogLines(context.Background(), srv.Client(), srv.URL, []changelogTarget{
		{Nodegroup: "ng-a", AMIType: "AL2023_ARM_64_STANDARD", Release: "1.32.0-20250101"},
	})
	if len(warnings) != 0 {
		t.Fatalf("warnings = %q, want none", warnings)
	}
	if len(lines) != 21 || lines[0] != "nodegroup ng-a: 13 release(s) after 1.32.0-20250101" {
		t.Errorf("got %d lines, first %q; want 21 lines for 13 releases", len(lines), lines)
	}
}

func TestChangelogLinesOneFetchForSeveralTargets(t *testing.T) {
	srv, requests := releaseServer(t, http.StatusOK, threeReleases)
	lines, warnings := changelogLines(context.Background(), srv.Client(), srv.URL, []changelogTarget{
		{Nodegroup: "ng-a", AMIType: "AL2023_x86_64_STANDARD", Release: "1.31.0-20260201"},
		{Nodegroup: "ng-b", AMIType: "AL2_ARM_64", Release: "1.31.0-20260601"},
		{Nodegroup: "ng-c", AMIType: "AL2023_x86_64_NVIDIA", Release: "1.31.0-20260301"},
		{Nodegroup: "ng-d", AMIType: "CUSTOM", Release: ""},
	})
	if got := requests.Load(); got != 1 {
		t.Errorf("requests = %d, want 1", got)
	}
	want := []string{
		"nodegroup ng-a: 2 release(s) after 1.31.0-20260201",
		"  v20260601",
		"    - kernel 5.10.99",
		"    - CVE-2026-1",
		"  v20260301",
		"    - containerd 1.7.0",
		"nodegroup ng-b: on the newest published release (1.31.0-20260601)",
		"nodegroup ng-c: 1 release(s) after 1.31.0-20260301",
		"  v20260601",
		"    - kernel 5.10.99",
		"    - CVE-2026-1",
	}
	if diff := cmp.Diff(want, lines); diff != "" {
		t.Errorf("lines (-want +got):\n%s", diff)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %q, want none", warnings)
	}
}

// Bottlerocket and Windows versions are not amazon-eks-ami dates. They never
// reach the date parser or the releases API, even when a Bottlerocket commit
// hash is all digits and would parse as a date. Custom and unknown AMI types
// make no request either.
func TestChangelogLinesOtherFamilies(t *testing.T) {
	const bottlerocket = "https://github.com/bottlerocket-os/bottlerocket/releases"
	const windows = "https://docs.aws.amazon.com/eks/latest/userguide/eks-ami-versions-windows.html"
	cases := []struct {
		name     string
		target   changelogTarget
		lines    []string
		warnings []string
	}{
		{
			name:   "bottlerocket",
			target: changelogTarget{Nodegroup: "ng", AMIType: "BOTTLEROCKET_x86_64", Release: "1.20.3-5d9ac849"},
			lines:  []string{"nodegroup ng: see release notes: " + bottlerocket},
		},
		{
			name:   "bottlerocket with an all-digit commit hash",
			target: changelogTarget{Nodegroup: "ng", AMIType: "BOTTLEROCKET_ARM_64_NVIDIA", Release: "1.20.3-20240101"},
			lines:  []string{"nodegroup ng: see release notes: " + bottlerocket},
		},
		{
			name:   "windows",
			target: changelogTarget{Nodegroup: "ng", AMIType: "WINDOWS_CORE_2022_x86_64", Release: "1.31-2026.01.14"},
			lines:  []string{"nodegroup ng: see release notes: " + windows},
		},
		{
			name:   "custom",
			target: changelogTarget{Nodegroup: "ng", AMIType: "CUSTOM", Release: "1.31.0-20260201"},
		},
		{
			name:     "unknown",
			target:   changelogTarget{Nodegroup: "ng", AMIType: "FUTURE_OS_x86_64", Release: "1.31.0-20260201"},
			warnings: []string{`release notes unavailable for nodegroup ng: no release notes source for AMI type "FUTURE_OS_x86_64"`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, requests := releaseServer(t, http.StatusInternalServerError, "")
			lines, warnings := changelogLines(context.Background(), srv.Client(), srv.URL, []changelogTarget{tc.target})
			if diff := cmp.Diff(tc.lines, lines); diff != "" {
				t.Errorf("lines (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.warnings, warnings); diff != "" {
				t.Errorf("warnings (-want +got):\n%s", diff)
			}
			if got := requests.Load(); got != 0 {
				t.Errorf("requests = %d, want 0", got)
			}
		})
	}
}

func TestChangelogLinesUnparseableRelease(t *testing.T) {
	srv, requests := releaseServer(t, http.StatusOK, threeReleases)
	lines, warnings := changelogLines(context.Background(), srv.Client(), srv.URL, []changelogTarget{
		{Nodegroup: "ng-a", AMIType: "AL2023_x86_64_STANDARD", Release: "custom-x"},
	})
	if len(lines) != 0 {
		t.Errorf("lines = %q, want none", lines)
	}
	want := []string{`release notes unavailable for nodegroup ng-a: release version "custom-x" has no date`}
	if diff := cmp.Diff(want, warnings); diff != "" {
		t.Errorf("warnings (-want +got):\n%s", diff)
	}
	if got := requests.Load(); got != 0 {
		t.Errorf("requests = %d, want 0", got)
	}
}

func TestChangelogLinesNilClient(t *testing.T) {
	srv, requests := releaseServer(t, http.StatusOK, threeReleases)
	lines, warnings := changelogLines(context.Background(), nil, srv.URL, []changelogTarget{
		{Nodegroup: "ng-a", AMIType: "AL2023_x86_64_STANDARD", Release: "1.31.0-20260201"},
	})
	if len(lines) != 0 {
		t.Errorf("lines = %q, want none", lines)
	}
	want := []string{"release notes unavailable for nodegroup ng-a: no HTTP client is available"}
	if diff := cmp.Diff(want, warnings); diff != "" {
		t.Errorf("warnings (-want +got):\n%s", diff)
	}
	if got := requests.Load(); got != 0 {
		t.Errorf("requests = %d, want 0", got)
	}
}

// A failed fetch is one warning for all the nodegroups that needed it. The
// other targets keep their lines.
func TestChangelogLinesFetchFailures(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		reason string
	}{
		{"not found", http.StatusNotFound, "{}", "the amazon-eks-ami releases API returned 404 Not Found"},
		{"rate limited", http.StatusForbidden, `{"message":"rate limit"}`, "the amazon-eks-ami releases API returned 403 Forbidden"},
		{"invalid JSON", http.StatusOK, `[{"tag_name":`, "parse the amazon-eks-ami release list: "},
		{"not a list", http.StatusOK, `{"message":"x"}`, "parse the amazon-eks-ami release list: "},
		{"oversized body", http.StatusOK, strings.Repeat(" ", changelogBodyLimit+1), "the amazon-eks-ami release list is larger than 4 MiB"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, requests := releaseServer(t, tc.status, tc.body)
			lines, warnings := changelogLines(context.Background(), srv.Client(), srv.URL, []changelogTarget{
				{Nodegroup: "ng-a", AMIType: "AL2023_x86_64_STANDARD", Release: "1.31.0-20260201"},
				{Nodegroup: "ng-b", AMIType: "BOTTLEROCKET_x86_64", Release: "1.20.3-5d9ac849"},
				{Nodegroup: "ng-c", AMIType: "AL2_x86_64", Release: "1.31.0-20260301"},
			})
			wantLines := []string{"nodegroup ng-b: see release notes: https://github.com/bottlerocket-os/bottlerocket/releases"}
			if diff := cmp.Diff(wantLines, lines); diff != "" {
				t.Errorf("lines (-want +got):\n%s", diff)
			}
			prefix := "release notes unavailable for nodegroups ng-a, ng-c: " + tc.reason
			if len(warnings) != 1 || !strings.HasPrefix(warnings[0], prefix) {
				t.Errorf("warnings = %q, want one that starts with %q", warnings, prefix)
			}
			if got := requests.Load(); got != 1 {
				t.Errorf("requests = %d, want 1", got)
			}
		})
	}
}

// Release bodies are about 26 KB each, so a list of several MiB must parse.
func TestChangelogLinesLargeReleaseList(t *testing.T) {
	big := strings.Repeat(`- a long line of release notes\n`, 900)
	var b strings.Builder
	b.WriteString("[")
	for i := range 60 {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"tag_name":"v202605%02d","body":"%s"}`, i%28+1, big)
	}
	b.WriteString("]")
	if b.Len() <= 1<<20 || b.Len() > changelogBodyLimit {
		t.Fatalf("list is %d bytes, want between 1 MiB and the limit", b.Len())
	}
	srv, _ := releaseServer(t, http.StatusOK, b.String())
	lines, warnings := changelogLines(context.Background(), srv.Client(), srv.URL, []changelogTarget{
		{Nodegroup: "ng-a", AMIType: "AL2023_x86_64_STANDARD", Release: "1.34.10-20260501"},
	})
	if len(warnings) != 0 {
		t.Fatalf("warnings = %q, want none", warnings)
	}
	want := []string{"nodegroup ng-a: 57 release(s) after 1.34.10-20260501"}
	if diff := cmp.Diff(want, lines); diff != "" {
		t.Errorf("lines (-want +got):\n%s", diff)
	}
}

func TestChangelogLinesCancelledContext(t *testing.T) {
	srv, requests := releaseServer(t, http.StatusOK, threeReleases)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	lines, warnings := changelogLines(ctx, srv.Client(), srv.URL, []changelogTarget{
		{Nodegroup: "ng-a", AMIType: "AL2023_x86_64_STANDARD", Release: "1.31.0-20260201"},
	})
	if len(lines) != 0 {
		t.Errorf("lines = %q, want none", lines)
	}
	if len(warnings) != 1 || !strings.HasPrefix(warnings[0], "release notes unavailable for nodegroup ng-a: ") ||
		!strings.Contains(warnings[0], context.Canceled.Error()) {
		t.Errorf("warnings = %q, want one that names the cancelled context", warnings)
	}
	if got := requests.Load(); got != 0 {
		t.Errorf("requests = %d, want 0", got)
	}
}

func TestChangelogLinesNoTargets(t *testing.T) {
	lines, warnings := changelogLines(context.Background(), nil, "", nil)
	if len(lines) != 0 || len(warnings) != 0 {
		t.Errorf("lines = %q, warnings = %q; want none", lines, warnings)
	}
}

// FuzzReleaseDate checks releaseDate on any string: an accepted date is an
// 8-digit substring of the input. It also checks well-formed amazon-eks-ami
// release versions and tags, where releaseDate must return the date and so
// order the releases by date.
func FuzzReleaseDate(f *testing.F) {
	f.Add("1.31.0-20260601", uint16(31), uint16(0), uint32(20260601), uint32(20260615))
	f.Add("v20260601", uint16(33), uint16(4), uint32(20251231), uint32(20260101))
	f.Add("1.31.0-2026060", uint16(9), uint16(65535), uint32(0), uint32(99999999))
	f.Add("", uint16(0), uint16(0), uint32(19700101), uint32(19700101))
	f.Add("1.30.1-202606011234", uint16(30), uint16(1), uint32(12345678), uint32(12345679))
	f.Fuzz(func(t *testing.T, raw string, minor, patch uint16, d1, d2 uint32) {
		if got, ok := releaseDate(raw); ok {
			if len(got) != 8 || strings.TrimLeft(got, "0123456789") != "" || !strings.Contains(raw, got) {
				t.Fatalf("releaseDate(%q) = %q; want an 8-digit substring of the input", raw, got)
			}
		} else if got != "" {
			t.Fatalf("releaseDate(%q) = %q, false; want empty when not ok", raw, got)
		}

		d1, d2 = d1%100000000, d2%100000000
		stamp1, stamp2 := fmt.Sprintf("%08d", d1), fmt.Sprintf("%08d", d2)
		for _, s := range []string{
			fmt.Sprintf("1.%d.%d-%s", minor, patch, stamp1),
			"v" + stamp1,
		} {
			if got, ok := releaseDate(s); !ok || got != stamp1 {
				t.Fatalf("releaseDate(%q) = %q, %v; want %q", s, got, ok, stamp1)
			}
		}
		r1, _ := releaseDate(fmt.Sprintf("1.%d.%d-%s", minor, patch, stamp1))
		r2, _ := releaseDate("v" + stamp2)
		if (r1 < r2) != (d1 < d2) || (r1 == r2) != (d1 == d2) {
			t.Fatalf("releaseDate order %q vs %q disagrees with dates %d vs %d", r1, r2, d1, d2)
		}
	})
}
