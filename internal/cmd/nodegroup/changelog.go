package nodegroup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// eksAMIReleasesURL is the GitHub API list of the amazon-eks-ami releases.
// 50 releases are about a year of weekly AMIs.
const eksAMIReleasesURL = "https://api.github.com/repos/awslabs/amazon-eks-ami/releases?per_page=50"

// The release notes pages of the AMI families that amazon-eks-ami does not publish.
const (
	bottlerocketReleasesURL = "https://github.com/bottlerocket-os/bottlerocket/releases"
	windowsAMIReleasesURL   = "https://docs.aws.amazon.com/eks/latest/userguide/eks-ami-versions-windows.html"
)

const (
	// changelogHTTPLimit is the longest that the release list request can take.
	changelogHTTPLimit = 4 * time.Second
	// changelogBodyLimit is the largest release list that is read. 50 releases are about 1.3 MB.
	changelogBodyLimit = 4 << 20
	// changelogMaxHighlights is the most highlights that one release shows.
	changelogMaxHighlights = 6
	// changelogMaxNotes is the most releases that one nodegroup shows with their highlights.
	changelogMaxNotes = 10
)

// dateInRelease matches the 8-digit date in an EKS AMI release version or tag,
// such as 20260601 in 1.31.0-20260601 and in v20260601.
var dateInRelease = regexp.MustCompile(`\d{8}`)

// prAuthorTail matches the "by @user in <pull request URL>" tail that GitHub's
// generated release notes put on each change.
var prAuthorTail = regexp.MustCompile(`\s+by @\S+ in https://github\.com/\S+/pull/(\d+)\s*$`)

// changelogTarget is one nodegroup that a dry run would roll.
type changelogTarget struct {
	Nodegroup string
	AMIType   string // the EKS AMI type, such as AL2023_x86_64_STANDARD, BOTTLEROCKET_x86_64, WINDOWS_CORE_2022_x86_64, CUSTOM
	Release   string // the AMI release version the nodegroup runs now, such as 1.32.0-20250101
}

// amiFamily is the publisher of the release notes of an AMI type.
type amiFamily int

const (
	familyUnknown amiFamily = iota
	familyCustom
	familyAmazonLinux
	familyBottlerocket
	familyWindows
)

// familyOf returns the family of an EKS AMI type.
func familyOf(amiType string) amiFamily {
	switch {
	case amiType == "CUSTOM":
		return familyCustom
	case strings.HasPrefix(amiType, "AL2_"), strings.HasPrefix(amiType, "AL2023_"):
		return familyAmazonLinux
	case strings.HasPrefix(amiType, "BOTTLEROCKET_"):
		return familyBottlerocket
	case strings.HasPrefix(amiType, "WINDOWS_"):
		return familyWindows
	default:
		return familyUnknown
	}
}

// amiRelease is one amazon-eks-ami release: its tag, its date, and its summary.
type amiRelease struct {
	tag        string
	date       string
	highlights []string
}

// changelogLines returns the lines of the AMI release notes for the targets, for stderr,
// and one warning for each thing that could not be shown. It never returns an error:
// a failed fetch is a warning. client is the HTTP client; nil means that no client is
// available, which is a warning and no request. url is the releases API URL; "" means eksAMIReleasesURL.
//
// A roll goes to the newest AMI release of the Kubernetes version of the nodegroup, so
// the notes are the releases that are dated after the release the nodegroup runs now.
// Only the Amazon Linux families have dated versions that amazon-eks-ami publishes.
// Bottlerocket and Windows get a pointer to their own release notes.
func changelogLines(ctx context.Context, client *http.Client, url string, targets []changelogTarget) (lines, warnings []string) {
	// dates holds the release date of each Amazon Linux target that has one.
	dates := make(map[int]string, len(targets))
	var dated []string
	for i, t := range targets {
		if familyOf(t.AMIType) != familyAmazonLinux {
			continue
		}
		if d, ok := releaseDate(t.Release); ok {
			dates[i] = d
			dated = append(dated, t.Nodegroup)
		}
	}

	var releases []amiRelease
	fetched := false
	if len(dated) > 0 {
		var err error
		releases, err = fetchEKSAMIReleases(ctx, client, url)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("release notes unavailable for %s: %v", nodegroupNames(dated), err))
		}
		fetched = err == nil
	}

	for i, t := range targets {
		switch familyOf(t.AMIType) {
		case familyCustom:
		case familyBottlerocket:
			lines = append(lines, fmt.Sprintf("nodegroup %s: see release notes: %s", t.Nodegroup, bottlerocketReleasesURL))
		case familyWindows:
			lines = append(lines, fmt.Sprintf("nodegroup %s: see release notes: %s", t.Nodegroup, windowsAMIReleasesURL))
		case familyAmazonLinux:
			date, ok := dates[i]
			if !ok {
				warnings = append(warnings, fmt.Sprintf("release notes unavailable for nodegroup %s: release version %q has no date", t.Nodegroup, t.Release))
				continue
			}
			if fetched {
				lines = append(lines, releaseLines(t, date, releases)...)
			}
		default:
			warnings = append(warnings, fmt.Sprintf("release notes unavailable for nodegroup %s: no release notes source for AMI type %q", t.Nodegroup, t.AMIType))
		}
	}
	return lines, warnings
}

// nodegroupNames returns "nodegroup a" for one name and "nodegroups a, b" for more.
func nodegroupNames(names []string) string {
	if len(names) == 1 {
		return "nodegroup " + names[0]
	}
	return "nodegroups " + strings.Join(names, ", ")
}

// releaseLines returns the lines for one Amazon Linux nodegroup: the count of the
// releases dated after date, then the tag and the highlights of each release that has highlights.
func releaseLines(t changelogTarget, date string, releases []amiRelease) []string {
	var notes []string
	after, shown := 0, 0
	for _, r := range releases {
		if r.date <= date {
			continue
		}
		after++
		if len(r.highlights) == 0 || shown == changelogMaxNotes {
			continue
		}
		shown++
		notes = append(notes, "  "+r.tag)
		for _, h := range r.highlights {
			notes = append(notes, "    - "+h)
		}
	}
	if after == 0 {
		return []string{fmt.Sprintf("nodegroup %s: on the newest published release (%s)", t.Nodegroup, t.Release)}
	}
	head := fmt.Sprintf("nodegroup %s: %d release(s) after %s", t.Nodegroup, after, t.Release)
	return append([]string{head}, notes...)
}

// releaseDate returns the last 8-digit date in a release version or tag.
func releaseDate(release string) (string, bool) {
	m := dateInRelease.FindAllString(release, -1)
	if len(m) == 0 {
		return "", false
	}
	return m[len(m)-1], true
}

// summarizeReleaseBody returns the lines of a release body that matter for a node patch.
// The amazon-eks-ami notes are generated by GitHub: a list of pull request titles, then
// HTML tables of source AMIs. The summary keeps the titles, such as "fix(x): ... (#2821)".
// From older plain-text notes it keeps the lines about the kernel, the container runtime,
// or a CVE. It skips HTML.
func summarizeReleaseBody(body string) []string {
	var out []string
	seen := map[string]struct{}{}
	add := func(l string) {
		if _, dup := seen[l]; !dup {
			seen[l] = struct{}{}
			out = append(out, l)
		}
	}
	for line := range strings.SplitSeq(body, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "<") || strings.HasPrefix(t, "**Full Changelog") {
			continue
		}
		if m := prAuthorTail.FindStringSubmatchIndex(t); m != nil {
			title := strings.TrimLeft(t[:m[0]], "-* ")
			add(title + " (#" + t[m[2]:m[3]] + ")")
			continue
		}
		l := strings.TrimLeft(t, "-*# ")
		low := strings.ToLower(l)
		if strings.Contains(low, "kernel") || strings.Contains(low, "containerd") ||
			strings.Contains(low, "runc") || strings.Contains(low, "cve-") {
			add(l)
		}
	}
	if len(out) > changelogMaxHighlights {
		out = out[:changelogMaxHighlights]
	}
	return out
}

// fetchEKSAMIReleases reads the release list once and summarises each dated release.
// The request stops after changelogHTTPLimit.
func fetchEKSAMIReleases(ctx context.Context, client *http.Client, url string) ([]amiRelease, error) {
	if client == nil {
		return nil, errors.New("no HTTP client is available")
	}
	if url == "" {
		url = eksAMIReleasesURL
	}
	ctx, cancel := context.WithTimeout(ctx, changelogHTTPLimit)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build the release list request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get the amazon-eks-ami release list: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the amazon-eks-ami releases API returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, changelogBodyLimit+1))
	if err != nil {
		return nil, fmt.Errorf("read the amazon-eks-ami release list: %w", err)
	}
	if len(body) > changelogBodyLimit {
		return nil, fmt.Errorf("the amazon-eks-ami release list is larger than %d MiB", changelogBodyLimit>>20)
	}
	var raw []struct {
		TagName string `json:"tag_name"`
		Body    string `json:"body"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse the amazon-eks-ami release list: %w", err)
	}
	releases := make([]amiRelease, 0, len(raw))
	for _, r := range raw {
		date, ok := releaseDate(r.TagName)
		if !ok {
			continue
		}
		releases = append(releases, amiRelease{tag: r.TagName, date: date, highlights: summarizeReleaseBody(r.Body)})
	}
	return releases, nil
}
