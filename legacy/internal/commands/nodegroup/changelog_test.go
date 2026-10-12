package nodegroup

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
)

func TestReleaseDate(t *testing.T) {
	cases := map[string]string{
		"1.31.0-20260601": "20260601",
		"v20260601":       "20260601",
		"no-date-here":    "",
	}
	for in, want := range cases {
		got, ok := releaseDate(in)
		if want == "" {
			if ok {
				t.Errorf("releaseDate(%q) = %q, want no match", in, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("releaseDate(%q) = %q (ok=%v), want %q", in, got, ok, want)
		}
	}
}

func TestSummarizeReleaseBody(t *testing.T) {
	body := "## Changes\n- Bump kernel to 5.10.99\n- update containerd to 1.7.0\n- Fix CVE-2026-1234\n- unrelated doc tweak\n"
	got := summarizeReleaseBody(body)
	if len(got) != 3 {
		t.Fatalf("got %d highlights, want 3: %v", len(got), got)
	}
}

func TestBuildAMIChangelog_Fetched(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
			{"tag_name":"v20260601","body":"- kernel 5.10.99\n- CVE-2026-1\n"},
			{"tag_name":"v20260301","body":"- containerd 1.7.0\n"},
			{"tag_name":"v20260101","body":"- old, before current\n"}
		]`))
	}))
	defer srv.Close()

	// Point the fetcher at the test server.
	oldURL := eksAMIReleasesURL
	eksAMIReleasesURL = srv.URL
	defer func() { eksAMIReleasesURL = oldURL }()

	cl := buildAMIChangelog(context.Background(), srv.Client(), ekstypes.AMITypesAl2023X8664Standard, "1.31.0-20260201", "1.31.0-20260601")
	if cl.Degraded {
		t.Fatalf("unexpected degraded: %s", cl.Reason)
	}
	// Releases after 20260201 up to 20260601: v20260301 and v20260601 → 2.
	if cl.Behind != 2 {
		t.Errorf("Behind = %d, want 2", cl.Behind)
	}
	if len(cl.Notes) != 2 {
		t.Errorf("Notes = %d, want 2", len(cl.Notes))
	}
}

func TestBuildAMIChangelog_DegradesOnUnparseable(t *testing.T) {
	cl := buildAMIChangelog(context.Background(), nil, ekstypes.AMITypesAl2023X8664Standard, "custom-x", "also-custom")
	if !cl.Degraded {
		t.Error("expected degraded when release dates can't be parsed")
	}
}

// Bottlerocket and Windows versions are not amazon-eks-ami date stamps: they
// must never reach the date parser or the amazon-eks-ami releases API, even
// when a Bottlerocket commit hash is all digits and would parse as a date.
func TestBuildAMIChangelog_NonAmazonLinuxFamilies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("amazon-eks-ami releases API called for a non-Amazon Linux AMI")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	oldURL := eksAMIReleasesURL
	eksAMIReleasesURL = srv.URL
	defer func() { eksAMIReleasesURL = oldURL }()

	cases := []struct {
		amiType         ekstypes.AMITypes
		current, target string
		url             string
	}{
		{ekstypes.AMITypesBottlerocketX8664, "1.20.3-5d9ac849", "1.21.0-0a1b2c3d", "https://github.com/bottlerocket-os/bottlerocket/releases"},
		// All-digit commit hashes: 8 digits each, which the date parser would accept.
		{ekstypes.AMITypesBottlerocketArm64Nvidia, "1.20.3-20240101", "1.21.0-20260601", "https://github.com/bottlerocket-os/bottlerocket/releases"},
		{ekstypes.AMITypesWindowsCore2022X8664, "1.31-2026.01.14", "1.31-2026.09.14", "https://docs.aws.amazon.com/eks/latest/userguide/eks-ami-versions-windows.html"},
	}
	for _, tc := range cases {
		cl := buildAMIChangelog(context.Background(), srv.Client(), tc.amiType, tc.current, tc.target)
		if cl.NotesURL != tc.url || cl.Behind != 0 || len(cl.Notes) != 0 || cl.Degraded {
			t.Errorf("%s: changelog = %+v, want only NotesURL %s", tc.amiType, cl, tc.url)
		}
		out := captureStdout(t, func() { printChangelog(cl, false) })
		if !strings.Contains(out, "see release notes: "+tc.url) || strings.Contains(out, "unavailable") {
			t.Errorf("%s: printed %q", tc.amiType, out)
		}
	}
}

// Release bodies grew to ~26 KB each, so the list outgrew the old 1 MiB read
// cap and the notes failed with "unexpected end of JSON input" (seen against
// the real API). A multi-MiB list parses; one past the cap says so.
func TestBuildAMIChangelog_LargeReleaseList(t *testing.T) {
	big := strings.Repeat("- a long line of release notes\\n", 900) // ~27 KB per body
	list := func(n int) string {
		var b strings.Builder
		b.WriteString("[")
		for i := range n {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `{"tag_name":"v202605%02d","body":"%s"}`, i%28+1, big)
		}
		b.WriteString("]")
		return b.String()
	}
	for _, tc := range []struct {
		n        int
		degraded string
	}{{n: 60}, {n: 200, degraded: "larger than 4 MiB"}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(list(tc.n)))
		}))
		oldURL := eksAMIReleasesURL
		eksAMIReleasesURL = srv.URL
		cl := buildAMIChangelog(context.Background(), srv.Client(), ekstypes.AMITypesAl2023X8664Standard, "1.34.10-20260501", "1.34.11-20260528")
		eksAMIReleasesURL = oldURL
		srv.Close()
		if tc.degraded == "" && (cl.Degraded || cl.Behind == 0) {
			t.Errorf("%d releases: degraded %q, behind %d", tc.n, cl.Reason, cl.Behind)
		}
		if tc.degraded != "" && (!cl.Degraded || !strings.Contains(cl.Reason, tc.degraded)) {
			t.Errorf("%d releases: reason = %q, want %q", tc.n, cl.Reason, tc.degraded)
		}
	}
}

// The real notes (2026): GitHub-generated PR titles, then HTML tables whose
// cells mention kernels. The summary keeps the PR titles, not the cells.
func TestSummarizeReleaseBody_GeneratedNotes(t *testing.T) {
	body := "<!-- Release notes generated -->\n\n## What's Changed\n" +
		"* fix(boot-hook): poll managed interfaces by @mselim00 in https://github.com/awslabs/amazon-eks-ami/pull/2821\n" +
		"* feat: use igzip for decompression by @fletcherw in https://github.com/awslabs/amazon-eks-ami/pull/2828\n\n" +
		"**Full Changelog**: https://github.com/awslabs/amazon-eks-ami/compare/v1...v2\n\n---\n\n<h2>AMI Details</h2>\n<table>\n" +
		"  <tr>\n    <td>al2023-ami-minimal-2023.12.20260918.0-kernel-6.18-arm64</td>\n  </tr>\n</table>\n"
	got := summarizeReleaseBody(body)
	want := []string{"fix(boot-hook): poll managed interfaces (#2821)", "feat: use igzip for decompression (#2828)"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %q, want %q", got, want)
	}
}
