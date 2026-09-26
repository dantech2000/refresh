package nodegroup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/commands/factory"
	"github.com/dantech2000/refresh/internal/common"
	"github.com/dantech2000/refresh/internal/render"
)

const changelogHTTPLimit = 4 * time.Second

// changelogBodyLimit caps the release list read (50 releases are ~1.3 MB).
const changelogBodyLimit = 4 << 20

// eksAMIReleasesURL is a var (not const) so tests can point it at a stub server.
// 50 releases is about a year of weekly AMIs. Release bodies run ~26 KB
// each, so 100 of them (2.6 MB) overran the old 1 MiB read cap.
var eksAMIReleasesURL = "https://api.github.com/repos/awslabs/amazon-eks-ami/releases?per_page=50"

// dateInRelease matches the 8-digit date stamp in an EKS AMI release version or
// tag (e.g. "1.31.0-20260601" → "20260601", "v20260601" → "20260601").
var dateInRelease = regexp.MustCompile(`\d{8}`)

// releaseNote is a summarized amazon-eks-ami GitHub release.
type releaseNote struct {
	Tag        string   `json:"tag" yaml:"tag"`
	Highlights []string `json:"highlights,omitempty" yaml:"highlights,omitempty"`
}

// amiChangelog is the current→target AMI release delta plus best-effort notes.
type amiChangelog struct {
	Current string        `json:"current" yaml:"current"`
	Target  string        `json:"target" yaml:"target"`
	Behind  int           `json:"releasesBehind" yaml:"releasesBehind"`
	Notes   []releaseNote `json:"notes,omitempty" yaml:"notes,omitempty"`
	// NotesURL points at the family's own release notes when they are not
	// amazon-eks-ami's (Bottlerocket, Windows); Notes and Behind stay empty.
	NotesURL string `json:"notesUrl,omitempty" yaml:"notesUrl,omitempty"`
	Degraded bool   `json:"degraded,omitempty" yaml:"degraded,omitempty"`
	Reason   string `json:"reason,omitempty" yaml:"reason,omitempty"`
}

// releaseDate returns the trailing 8-digit date stamp from a release version/tag.
func releaseDate(release string) (string, bool) {
	m := dateInRelease.FindAllString(release, -1)
	if len(m) == 0 {
		return "", false
	}
	return m[len(m)-1], true
}

// prAuthorTail is the "by @user in <PR url>" tail GitHub's generated
// release notes put on each change.
var prAuthorTail = regexp.MustCompile(`\s+by @\S+ in https://github\.com/\S+/pull/(\d+)\s*$`)

// summarizeReleaseBody pulls the lines that matter for a node patch out of a
// release body. amazon-eks-ami's notes are GitHub-generated: a "What's
// Changed" list of PR titles, then HTML tables of source AMIs. It keeps the
// PR titles ("fix(x): … (#2821)") and, from older plain-text notes, lines
// about the kernel, the container runtime, or a CVE. HTML is skipped.
func summarizeReleaseBody(body string) []string {
	var out []string
	seen := map[string]struct{}{}
	add := func(l string) {
		if _, dup := seen[l]; !dup {
			seen[l] = struct{}{}
			out = append(out, l)
		}
	}
	for _, line := range strings.Split(body, "\n") {
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
	if len(out) > 6 {
		out = out[:6]
	}
	return out
}

type ghRelease struct {
	TagName string `json:"tag_name"`
	Body    string `json:"body"`
}

// buildAMIChangelog computes the release delta and, best-effort, summarized
// notes for the amazon-eks-ami releases strictly after current up to target.
// Any failure degrades to just the version delta — it never blocks an update.
//
// Only the Amazon Linux families publish through amazon-eks-ami with
// date-stamped versions. Other families (Bottlerocket's 1.20.3-5d9ac849,
// whose commit hash can be all digits) never reach the date parser: they get
// a pointer to their own release notes instead.
func buildAMIChangelog(ctx context.Context, httpClient *http.Client, amiType ekstypes.AMITypes, current, target string) amiChangelog {
	cl := amiChangelog{Current: current, Target: target}
	if url, eksAMI := awsinternal.AMIReleaseNotes(amiType); !eksAMI {
		cl.NotesURL = url
		if url == "" {
			cl.Degraded = true
			cl.Reason = "no release notes source for AMI type " + string(amiType)
		}
		return cl
	}
	curDate, okC := releaseDate(current)
	tgtDate, okT := releaseDate(target)
	if !okC || !okT {
		cl.Degraded = true
		cl.Reason = "could not parse release dates"
		return cl
	}
	if curDate >= tgtDate {
		return cl // current is at or ahead of target — nothing to show
	}

	releases, err := fetchEKSAMIReleases(ctx, httpClient)
	if err != nil {
		cl.Degraded = true
		cl.Reason = err.Error()
		return cl
	}
	for _, r := range releases {
		d, ok := releaseDate(r.TagName)
		if !ok || d <= curDate || d > tgtDate {
			continue
		}
		cl.Behind++
		if highlights := summarizeReleaseBody(r.Body); len(highlights) > 0 && len(cl.Notes) < 10 {
			cl.Notes = append(cl.Notes, releaseNote{Tag: r.TagName, Highlights: highlights})
		}
	}
	return cl
}

func fetchEKSAMIReleases(ctx context.Context, httpClient *http.Client) ([]ghRelease, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: changelogHTTPLimit}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, eksAMIReleasesURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("amazon-eks-ami releases API returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, changelogBodyLimit+1))
	if err != nil {
		return nil, err
	}
	if len(body) > changelogBodyLimit {
		return nil, fmt.Errorf("the amazon-eks-ami release list is larger than %d MiB", changelogBodyLimit>>20)
	}
	var releases []ghRelease
	if err := json.Unmarshal(body, &releases); err != nil {
		return nil, err
	}
	return releases, nil
}

// printChangelogsForNodegroups resolves and prints the AMI changelog for each
// selected nodegroup (used in dry-run). Custom-AMI nodegroups are skipped. full
// prints all notes; otherwise the first few with a "+N more" hint.
func printChangelogsForNodegroups(ctx context.Context, awsCfg aws.Config, eksClient *eks.Client, clusterName string, nodegroups []string, full bool) {
	clusterOut, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeClusterOutput, error) {
		return eksClient.DescribeCluster(rc, &eks.DescribeClusterInput{Name: aws.String(clusterName)})
	})
	if err != nil || clusterOut == nil || clusterOut.Cluster == nil || clusterOut.Cluster.Version == nil {
		return
	}
	k8sVersion := *clusterOut.Cluster.Version
	ssmClient := factory.NewSSMClient(awsCfg)

	for _, ng := range nodegroups {
		desc, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeNodegroupOutput, error) {
			return eksClient.DescribeNodegroup(rc, &eks.DescribeNodegroupInput{
				ClusterName:   aws.String(clusterName),
				NodegroupName: aws.String(ng),
			})
		})
		if err != nil || desc == nil || desc.Nodegroup == nil {
			continue
		}
		if desc.Nodegroup.AmiType == ekstypes.AMITypesCustom {
			continue
		}
		current := aws.ToString(desc.Nodegroup.ReleaseVersion)
		// Target the nodegroup's own minor: the update keeps it there.
		ngVersion := awsinternal.NodegroupK8sVersion(desc.Nodegroup, k8sVersion)
		target := awsinternal.LatestReleaseVersionForType(ctx, ssmClient, ngVersion, desc.Nodegroup.AmiType)
		if current == "" || target == "" || current == target {
			continue
		}
		fmt.Printf("  nodegroup %s:\n", ng)
		printChangelog(buildAMIChangelog(ctx, nil, desc.Nodegroup.AmiType, current, target), full)
	}
}

func printChangelog(cl amiChangelog, full bool) {
	delta := fmt.Sprintf("%s → %s", orDash(cl.Current), orDash(cl.Target))
	if cl.Behind > 0 {
		delta += fmt.Sprintf(" (%d release(s) behind)", cl.Behind)
	}
	th := render.Default(os.Stdout)
	fmt.Println("    " + th.Bold(th.Pal.Sky, "AMI changelog:") + " " + delta)
	if cl.NotesURL != "" {
		fmt.Printf("      see release notes: %s\n", cl.NotesURL)
		return
	}
	if cl.Degraded {
		fmt.Println("      " + th.Line(render.Warn, "release notes unavailable (%s)", cl.Reason))
		return
	}
	shown := cl.Notes
	if !full && len(shown) > 3 {
		shown = shown[:3]
	}
	for _, n := range shown {
		fmt.Printf("      %s\n", n.Tag)
		for _, h := range n.Highlights {
			fmt.Printf("        - %s\n", h)
		}
	}
	if !full && len(cl.Notes) > len(shown) {
		fmt.Printf("      … +%d more release(s) (use --changelog for full notes)\n", len(cl.Notes)-len(shown))
	}
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
