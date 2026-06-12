package nodegroup

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/fatih/color"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
)

const changelogHTTPLimit = 4 * time.Second

// eksAMIReleasesURL is a var (not const) so tests can point it at a stub server.
var eksAMIReleasesURL = "https://api.github.com/repos/awslabs/amazon-eks-ami/releases?per_page=100"

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
	Current  string        `json:"current" yaml:"current"`
	Target   string        `json:"target" yaml:"target"`
	Behind   int           `json:"releasesBehind" yaml:"releasesBehind"`
	Notes    []releaseNote `json:"notes,omitempty" yaml:"notes,omitempty"`
	Degraded bool          `json:"degraded,omitempty" yaml:"degraded,omitempty"`
	Reason   string        `json:"reason,omitempty" yaml:"reason,omitempty"`
}

// releaseDate returns the trailing 8-digit date stamp from a release version/tag.
func releaseDate(release string) (string, bool) {
	m := dateInRelease.FindAllString(release, -1)
	if len(m) == 0 {
		return "", false
	}
	return m[len(m)-1], true
}

// summarizeReleaseBody pulls the lines that matter for a node patch (kernel,
// container runtime, CVE fixes) out of a release body.
func summarizeReleaseBody(body string) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, line := range strings.Split(body, "\n") {
		l := strings.TrimLeft(strings.TrimSpace(line), "-*# ")
		if l == "" {
			continue
		}
		low := strings.ToLower(l)
		if strings.Contains(low, "kernel") || strings.Contains(low, "containerd") ||
			strings.Contains(low, "runc") || strings.Contains(low, "cve-") {
			if _, dup := seen[l]; dup {
				continue
			}
			seen[l] = struct{}{}
			out = append(out, l)
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
func buildAMIChangelog(ctx context.Context, httpClient *http.Client, current, target string) amiChangelog {
	cl := amiChangelog{Current: current, Target: target}
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
	var releases []ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, err
	}
	return releases, nil
}

// printChangelogsForNodegroups resolves and prints the AMI changelog for each
// selected nodegroup (used in dry-run). Custom-AMI nodegroups are skipped. full
// prints all notes; otherwise the first few with a "+N more" hint.
func printChangelogsForNodegroups(ctx context.Context, awsCfg aws.Config, eksClient *eks.Client, clusterName string, nodegroups []string, full bool) {
	clusterOut, err := eksClient.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String(clusterName)})
	if err != nil || clusterOut.Cluster == nil || clusterOut.Cluster.Version == nil {
		return
	}
	k8sVersion := *clusterOut.Cluster.Version
	ssmClient := ssm.NewFromConfig(awsCfg)

	for _, ng := range nodegroups {
		desc, err := eksClient.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{
			ClusterName:   aws.String(clusterName),
			NodegroupName: aws.String(ng),
		})
		if err != nil || desc.Nodegroup == nil {
			continue
		}
		if desc.Nodegroup.AmiType == ekstypes.AMITypesCustom {
			continue
		}
		current := aws.ToString(desc.Nodegroup.ReleaseVersion)
		target := awsinternal.LatestReleaseVersionForType(ctx, ssmClient, k8sVersion, desc.Nodegroup.AmiType)
		if current == "" || target == "" || current == target {
			continue
		}
		fmt.Printf("  nodegroup %s:\n", ng)
		printChangelog(buildAMIChangelog(ctx, nil, current, target), full)
	}
}

func printChangelog(cl amiChangelog, full bool) {
	delta := fmt.Sprintf("%s → %s", orDash(cl.Current), orDash(cl.Target))
	if cl.Behind > 0 {
		delta += fmt.Sprintf(" (%d release(s) behind)", cl.Behind)
	}
	color.Cyan("    AMI changelog: %s", delta)
	if cl.Degraded {
		color.Yellow("      release notes unavailable (%s)", cl.Reason)
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
