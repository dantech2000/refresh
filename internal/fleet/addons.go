package fleet

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/fanout"
)

// addonsResult is the add-on group of a snapshot.
type addonsResult struct {
	addons   []Addon
	state    State
	failures []apidoc.Failure
}

// fetchAddons reads the add-ons of a cluster: the ones in only, or every
// add-on that EKS lists when only is empty. The describe calls run
// together; the request limit of the clients bounds them.
func fetchAddons(ctx context.Context, c *awsx.Clients, cluster string, only []string) addonsResult {
	names := only
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

	type described struct {
		ran     bool
		addon   Addon
		failure *apidoc.Failure
	}
	results := fanout.ForEach(ctx, names, max(len(names), 1), func(ctx context.Context, name string) described {
		out, err := c.EKS.DescribeAddon(ctx, &eks.DescribeAddonInput{ClusterName: aws.String(cluster), AddonName: aws.String(name)})
		if err == nil && out.Addon == nil {
			err = fmt.Errorf("describing add-on %s: %w", name, errEmptyAnswer)
		}
		if err != nil {
			failure := inRegion(c, FailureFromError(apidoc.FailureAddon, name, err))
			failure.Cluster = cluster
			return described{ran: true, addon: Addon{Name: name, State: stateOf(err)}, failure: &failure}
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
		return described{ran: true, addon: addon}
	})

	res := addonsResult{addons: make([]Addon, len(names)), state: Collected}
	for i, r := range results {
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
	slices.SortFunc(res.addons, func(a, b Addon) int { return cmp.Compare(a.Name, b.Name) })
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
