package health

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/fleet"
)

// kubeletSkewLimit is how many minor versions a nodegroup may trail the
// control plane. A nodegroup at the limit blocks the next upgrade: one
// more hop would put it past what Kubernetes supports.
const kubeletSkewLimit = 3

// CategoryUpgradeReadiness is the default category of the insights.
const CategoryUpgradeReadiness = string(ekstypes.CategoryUpgradeReadiness)

// ListInsights reads the insights of one category of a cluster, every
// status included. "" is UPGRADE_READINESS.
func ListInsights(ctx context.Context, c *awsx.Clients, cluster, category string) ([]apidoc.Insight, error) {
	if category == "" {
		category = CategoryUpgradeReadiness
	}
	raw, err := awsx.ListAll(ctx,
		func(ctx context.Context, token *string) (*eks.ListInsightsOutput, error) {
			return c.EKS.ListInsights(ctx, &eks.ListInsightsInput{
				ClusterName: aws.String(cluster),
				Filter:      &ekstypes.InsightsFilter{Categories: []ekstypes.Category{ekstypes.Category(category)}},
				NextToken:   token,
			})
		},
		func(out *eks.ListInsightsOutput) ([]ekstypes.InsightSummary, *string) {
			return out.Insights, out.NextToken
		},
	)
	if err != nil {
		return nil, err
	}
	out := make([]apidoc.Insight, 0, len(raw))
	for _, in := range raw {
		out = append(out, InsightDoc(in.Id, in.Name, in.Category, in.InsightStatus, in.KubernetesVersion, in.LastRefreshTime, in.Description))
	}
	return out, nil
}

// InsightDoc returns the document form of an insight from the fields that
// the list and the describe calls share.
func InsightDoc(id, name *string, category ekstypes.Category, status *ekstypes.InsightStatus, version *string, refreshed *time.Time, description *string) apidoc.Insight {
	doc := apidoc.Insight{
		ID: aws.ToString(id), Name: aws.ToString(name), Category: string(category),
		KubernetesVersion: aws.ToString(version), Description: aws.ToString(description),
		Status: string(ekstypes.InsightStatusValueUnknown),
	}
	if status != nil {
		doc.Status, doc.StatusReason = string(status.Status), aws.ToString(status.Reason)
	}
	if refreshed != nil {
		doc.LastRefreshTime = *refreshed
	}
	return doc
}

// InsightsInput is what the Insights check judges: the insights of one
// category, or the error of reading them.
type InsightsInput struct {
	Insights []apidoc.Insight
	Err      error
	// Category is the category that was read. "" is UPGRADE_READINESS.
	Category string
	// ForVersion, when set, keeps the insights about that Kubernetes
	// version only, and makes "no insight" a failure: the upgrade gate
	// refreshes the insights first, so none means that EKS has not
	// evaluated the version.
	ForVersion fleet.Version
}

// Insights checks the EKS cluster insights: an ERROR insight blocks, and
// so does an UNKNOWN one, which EKS could not evaluate. A WARNING insight
// warns. No insight at all in UPGRADE_READINESS warns too: EKS has not
// evaluated the cluster yet.
func Insights(in InsightsInput) Check {
	return Check{Name: "Insights", Blocking: true, FailClosed: true, Run: func(_ context.Context, run *Input) Result {
		return runInsights(in, run.Snapshot.Cluster.Name)
	}}
}

func runInsights(in InsightsInput, cluster string) Result {
	if in.Err != nil {
		f := fleet.FailureFromError(apidoc.FailureCluster, cluster, in.Err)
		return Result{Outcome: Unreadable, Message: "could not read the cluster insights: " + f.Error + failureAction(f), Failures: []apidoc.Failure{f}}
	}
	var errs, unknown, warnings []string
	total := 0
	for _, i := range in.Insights {
		if !in.ForVersion.IsZero() {
			if v, err := fleet.ParseVersion(i.KubernetesVersion); err != nil || v != in.ForVersion {
				continue
			}
		}
		total++
		switch i.Status {
		case string(ekstypes.InsightStatusValuePassing):
		case string(ekstypes.InsightStatusValueWarning):
			warnings = append(warnings, i.Name)
		case string(ekstypes.InsightStatusValueError):
			errs = append(errs, i.Name)
		default:
			unknown = append(unknown, i.Name)
		}
	}
	findings := slices.Sorted(slices.Values(slices.Concat(errs, unknown, warnings)))
	r := Result{Findings: findings}
	var parts []string
	if len(errs) > 0 {
		parts = append(parts, plural(len(errs), "ERROR insight")+" ("+strings.Join(errs, ", ")+")")
	}
	if len(unknown) > 0 {
		parts = append(parts, plural(len(unknown), "UNKNOWN insight")+" that EKS could not evaluate ("+strings.Join(unknown, ", ")+")")
	}
	category := in.Category
	if category == "" {
		category = CategoryUpgradeReadiness
	}
	switch {
	case len(parts) > 0:
		r.Outcome, r.Message = Fail, strings.Join(parts, "; ")
	case total == 0 && !in.ForVersion.IsZero():
		r.Outcome, r.Message = Fail, fmt.Sprintf("EKS has no insight for Kubernetes %s yet", in.ForVersion)
	case total == 0 && category == CategoryUpgradeReadiness:
		r.Outcome, r.Message = Warn, "EKS has not evaluated the upgrade insights of the cluster yet"
	case len(warnings) > 0:
		r.Outcome, r.Message = Warn, plural(len(warnings), "WARNING insight")+" ("+strings.Join(warnings, ", ")+")"
	default:
		r.Outcome, r.Message = Pass, fmt.Sprintf("%s passing", plural(total, "insight"))
	}
	return r
}

// VersionSkew checks each nodegroup's Kubernetes version against the
// control plane, and each add-on against its latest compatible version. A
// nodegroup at the kubelet skew limit blocks the next upgrade. It reads
// the snapshot and makes no call.
func VersionSkew() Check {
	return Check{Name: "VersionSkew", Blocking: true, FailClosed: true, Run: runVersionSkew}
}

func runVersionSkew(_ context.Context, in *Input) Result {
	snap := in.Snapshot
	if snap.Have.Cluster != fleet.Collected || snap.Cluster.Version.IsZero() {
		return Result{Outcome: Unreadable, Message: "the version of the control plane is not known"}
	}
	var atLimit, behind, addonsBehind, unread []string
	for _, n := range snap.Nodegroups {
		if n.State != fleet.Collected {
			unread = append(unread, "nodegroup "+n.Name)
			continue
		}
		switch minors := minorsBehind(snap.Cluster.Version, n.Version); {
		case minors >= kubeletSkewLimit:
			atLimit = append(atLimit, fmt.Sprintf("%s (%s, %d behind)", n.Name, n.Version, minors))
		case minors > 0:
			behind = append(behind, fmt.Sprintf("%s (%s)", n.Name, n.Version))
		}
	}
	for _, a := range snap.Addons {
		if a.State != fleet.Collected || a.LatestState != fleet.Collected {
			unread = append(unread, "add-on "+a.Name)
			continue
		}
		if b, _ := a.Behind(); b {
			addonsBehind = append(addonsBehind, fmt.Sprintf("%s (%s, latest %s)", a.Name, a.Version, a.Latest))
		}
	}
	r := Result{}
	for _, list := range [][]string{atLimit, behind, addonsBehind} {
		for _, item := range list {
			name, _, _ := strings.Cut(item, " (")
			r.Findings = append(r.Findings, name)
		}
	}
	slices.Sort(r.Findings)
	var parts []string
	if len(behind) > 0 {
		parts = append(parts, plural(len(behind), "nodegroup")+" behind the control plane ("+strings.Join(behind, ", ")+")")
	}
	if len(addonsBehind) > 0 {
		parts = append(parts, plural(len(addonsBehind), "add-on")+" behind latest ("+strings.Join(addonsBehind, ", ")+")")
	}
	switch {
	case len(atLimit) > 0:
		r.Outcome = Fail
		r.Message = plural(len(atLimit), "nodegroup") + " at the kubelet skew limit (" + strings.Join(atLimit, ", ") + "); upgrade the nodes before the control plane"
		if len(parts) > 0 {
			r.Message += "; " + strings.Join(parts, "; ")
		}
	case len(unread) > 0:
		r.Outcome, r.Message = Unreadable, "not read: "+strings.Join(unread, ", ")
	case len(parts) > 0:
		r.Outcome, r.Message = Warn, strings.Join(parts, "; ")
	default:
		r.Outcome, r.Message = Pass, fmt.Sprintf("every nodegroup on %s and every add-on at its latest", snap.Cluster.Version)
	}
	if len(unread) > 0 && r.Outcome == Fail {
		r.Message += "; not read: " + strings.Join(unread, ", ")
	}
	return r
}

// minorsBehind returns how many minor versions v trails cp, or 0 when it
// does not trail it or is not known.
func minorsBehind(cp, v fleet.Version) int {
	if v.IsZero() || v.Major != cp.Major {
		return 0
	}
	return max(cp.Minor-v.Minor, 0)
}
