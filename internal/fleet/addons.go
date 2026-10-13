package fleet

import (
	"context"
	"fmt"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
)

// addonsResult is the add-on group of a snapshot.
type addonsResult struct {
	addons   []Addon
	state    State
	failures []apidoc.Failure
}

// addonKey is the key of the memo for the newest version of an add-on.
type addonKey struct {
	region, addon string
	version       Version
}

// fetchAddons reads the add-ons of a cluster: the ones in need.OnlyAddons,
// or every add-on that EKS lists. The describe calls run together, and
// with need.LatestAddons the lookups of the newest versions run next to
// them. clusterVersion returns the Kubernetes version of the cluster once
// the cluster is described, or the zero value when it could not be.
func (f *Fetcher) fetchAddons(ctx context.Context, c *awsx.Clients, cluster string, need Need, clusterVersion func() Version) addonsResult {
	names := need.OnlyAddons
	if len(names) == 0 {
		var err error
		if names, err = addonNames(ctx, c, cluster); err != nil {
			// The list is a fact about the cluster, so the failure names
			// the cluster.
			return addonsResult{
				state:    stateOf(err),
				failures: []apidoc.Failure{inRegion(c, FailureFromError(apidoc.FailureCluster, cluster, err))},
			}
		}
	}

	// In name order from here on.
	names = slices.Sorted(slices.Values(names))

	// One task for each add-on, and with need.LatestAddons one more for
	// its newest version. They run in one fan-out, so the lookups of the
	// newest versions run next to the describe calls.
	type task struct {
		index  int
		newest bool // the lookup of the newest version, not the describe
	}
	type result struct {
		ran     bool
		addon   Addon           // of a describe
		failure *apidoc.Failure // of a describe
		version string          // of a lookup
		err     error           // of a lookup
		skipped bool            // of a lookup: the cluster has no version
	}
	tasks := make([]task, 0, 2*len(names))
	for i := range names {
		tasks = append(tasks, task{index: i})
	}
	if need.LatestAddons {
		for i := range names {
			tasks = append(tasks, task{index: i, newest: true})
		}
	}
	results := each(ctx, f.tickets(c), tasks, func(ctx context.Context, t task) result {
		name := names[t.index]
		if t.newest {
			// The cluster is described in the first wave, so this wait is
			// over when the task starts, or soon after.
			version := clusterVersion()
			if version.IsZero() {
				return result{ran: true, skipped: true}
			}
			latest, err := f.latestAddon(ctx, c, name, version)
			return result{ran: true, version: latest, err: err}
		}
		out, err := c.EKS.DescribeAddon(ctx, &eks.DescribeAddonInput{ClusterName: aws.String(cluster), AddonName: aws.String(name)})
		if err == nil && out.Addon == nil {
			err = fmt.Errorf("describing add-on %s: %w", name, errEmptyAnswer)
		}
		if err != nil {
			failure := inRegion(c, FailureFromError(apidoc.FailureAddon, name, err))
			failure.Cluster = cluster
			return result{ran: true, addon: Addon{Name: name, State: stateOf(err)}, failure: &failure}
		}
		in := out.Addon
		addon := Addon{
			Name:                  name,
			State:                 Collected,
			Version:               aws.ToString(in.AddonVersion),
			Status:                string(in.Status),
			ARN:                   aws.ToString(in.AddonArn),
			ServiceAccountRoleARN: aws.ToString(in.ServiceAccountRoleArn),
			Configuration:         aws.ToString(in.ConfigurationValues),
			CreatedAt:             aws.ToTime(in.CreatedAt),
			ModifiedAt:            aws.ToTime(in.ModifiedAt),
		}
		if in.Health != nil {
			for _, issue := range in.Health.Issues {
				addon.Issues = append(addon.Issues, Issue{Code: string(issue.Code), Message: aws.ToString(issue.Message), ResourceIDs: issue.ResourceIds})
			}
		}
		return result{ran: true, addon: addon}
	})

	res := addonsResult{addons: make([]Addon, len(names)), state: Collected}
	for i, r := range results[:len(names)] {
		if !r.ran {
			// The context ended before this add-on started.
			failure := inRegion(c, notAttempted(ctx, apidoc.FailureAddon, names[i]))
			failure.Cluster = cluster
			r.addon, r.failure = Addon{Name: names[i], State: NotAttempted}, &failure
		}
		res.addons[i] = r.addon
		if r.failure != nil {
			res.failures = append(res.failures, *r.failure)
			res.state = Partial
		}
	}
	for i, l := range results[len(names):] {
		a := &res.addons[i]
		if a.State != Collected || l.skipped {
			continue
		}
		err := chunkError(ctx, l.ran, l.err)
		if err != nil {
			a.LatestState = stateOf(err)
			failure := inRegion(c, FailureFromError(apidoc.FailureAddon, a.Name, err))
			failure.Cluster = cluster
			res.failures = append(res.failures, failure)
			res.state = Partial
			continue
		}
		a.Latest, a.LatestState = l.version, Collected
	}
	return res
}

// AddonNames returns the names of the add-ons of a cluster, in the order
// that EKS lists them.
func (f *Fetcher) AddonNames(ctx context.Context, c *awsx.Clients, cluster string) ([]string, error) {
	return addonNames(ctx, c, cluster)
}

func addonNames(ctx context.Context, c *awsx.Clients, cluster string) ([]string, error) {
	return awsx.ListAll(ctx,
		func(ctx context.Context, token *string) (*eks.ListAddonsOutput, error) {
			return c.EKS.ListAddons(ctx, &eks.ListAddonsInput{ClusterName: aws.String(cluster), NextToken: token})
		},
		func(out *eks.ListAddonsOutput) ([]string, *string) { return out.Addons, out.NextToken },
	)
}

// latestAddon returns the newest version of an add-on that is compatible
// with a Kubernetes version, or "" when EKS publishes none. Every cluster
// of the run in the same region and on the same version shares one call
// for an add-on. EKS documents no order for the versions, so the newest is
// found by comparison.
func (f *Fetcher) latestAddon(ctx context.Context, c *awsx.Clients, addon string, version Version) (string, error) {
	key := addonKey{region: c.Region, addon: addon, version: version}
	return f.latestAddons.Get(ctx, key, func(ctx context.Context) (string, error) {
		infos, err := awsx.ListAll(ctx,
			func(ctx context.Context, token *string) (*eks.DescribeAddonVersionsOutput, error) {
				return c.EKS.DescribeAddonVersions(ctx, &eks.DescribeAddonVersionsInput{
					AddonName:         aws.String(addon),
					KubernetesVersion: aws.String(version.String()),
					NextToken:         token,
				})
			},
			func(out *eks.DescribeAddonVersionsOutput) ([]ekstypes.AddonInfo, *string) {
				return out.Addons, out.NextToken
			},
		)
		if err != nil {
			return "", err
		}
		latest := ""
		for _, info := range infos {
			for _, v := range info.AddonVersions {
				if candidate := aws.ToString(v.AddonVersion); latest == "" || CompareAddonVersions(candidate, latest) > 0 {
					latest = candidate
				}
			}
		}
		return latest, nil
	})
}
