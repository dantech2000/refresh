// Package fleetcmd holds what the commands that read the fleet share: which
// regions a sweep reads, what a sweep that got no answer reports, and the
// error for a cluster that does not exist.
package fleetcmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/fleet/fleetdata"
)

// EnvRegions names the regions of a sweep, separated by commas. It applies
// when the command line names none.
const EnvRegions = "REFRESH_EKS_REGIONS"

// regionScopeHint tells the user how to narrow a sweep.
const regionScopeHint = "scope with -r or " + EnvRegions

// AddAllRegionsFlag adds -A/--all-regions to cmd.
func AddAllRegionsFlag(cmd *cobra.Command, p *bool) {
	cmd.Flags().BoolVarP(p, "all-regions", "A", false,
		"Read every region: the list in "+EnvRegions+", or the regions that every account has enabled")
}

// ValidateSweepFlags rejects -A together with -r: one names every region
// and the other names some. It also checks the region names in
// REFRESH_EKS_REGIONS.
func ValidateSweepFlags(global *cmdutil.GlobalOptions, allRegions bool) error {
	if allRegions && len(regionNames(global.Regions)) > 0 {
		return errors.New("-A/--all-regions and -r/--region cannot be used together: name the regions, or sweep all of them")
	}
	return CheckEnvRegions()
}

// CheckEnvRegions checks the shape of each region name in
// REFRESH_EKS_REGIONS, before any call. The root command checks the names
// of -r.
func CheckEnvRegions() error {
	for _, r := range regionNames(strings.Split(os.Getenv(EnvRegions), ",")) {
		if err := awsx.CheckRegion(r); err != nil {
			return fmt.Errorf("invalid %s: %w", EnvRegions, err)
		}
	}
	return nil
}

// SweepRegions returns the regions that a sweep command reads:
//
//  1. The regions of -r, when it is given.
//  2. With -A, or when no home region is configured: the regions in
//     REFRESH_EKS_REGIONS, or else the built-in list for the partition of
//     home, as a default sweep.
//  3. The home region.
//
// home is the region of the AWS configuration, or "".
func SweepRegions(global *cmdutil.GlobalOptions, home string, allRegions bool) fleet.Regions {
	if named := regionNames(global.Regions); len(named) > 0 {
		return fleet.Regions{Names: named}
	}
	if !allRegions && home != "" {
		return fleet.Regions{Names: []string{home}}
	}
	if env := regionNames(strings.Split(os.Getenv(EnvRegions), ",")); len(env) > 0 {
		return fleet.Regions{Names: env}
	}
	return fleet.Regions{Names: fleetdata.Regions(home), Default: true}
}

// regionNames returns the names in raw that are not empty, without spaces
// around them and without repeats, in their order.
func regionNames(raw []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, r := range raw {
		if r = strings.TrimSpace(r); r != "" && !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	return out
}

// ReportSkippedRegions writes the one notice of a default sweep that
// skipped regions closed to the credentials. They are not failures.
func ReportSkippedRegions(ios *cmdutil.IOStreams, skipped []string) {
	if len(skipped) == 0 {
		return
	}
	// Nothing can report a failed write to stderr.
	_, _ = fmt.Fprintln(ios.ErrOut, ios.ErrTheme().Caution(fmt.Sprintf(
		"Skipped %d region(s) not accessible to these credentials: %s (%s)",
		len(skipped), strings.Join(skipped, ", "), regionScopeHint)))
}

// RegionsClosedError reports a sweep in which every region refused
// credentials that STS accepts: the regions are not enabled for the
// account, or a policy blocks them.
type RegionsClosedError struct {
	// STSRegion is where the credentials were checked.
	STSRegion string
	// Err is the error of the first region.
	Err error
}

func (e *RegionsClosedError) Error() string {
	return fmt.Sprintf("no region answered, but STS in %s accepts these credentials\n"+
		"AWS: %s\n"+
		"A region refuses valid credentials when the account has not enabled it (an opt-in region) or a policy such as an SCP blocks it.\n"+
		"Enable the region in the account settings, or choose other regions (%s).",
		e.STSRegion, strings.TrimSuffix(awsx.Summary(e.Err), "."), regionScopeHint)
}

func (e *RegionsClosedError) Unwrap() error { return e.Err }

// SweepError returns the error of a sweep in which no region answered, and
// nil for a sweep with an answer. An empty result must not read as "no
// clusters": expired credentials or an outage fail every region at once.
//
// Loading the configuration makes no STS call, so keys that resolve and
// that AWS does not accept reach the sweep. A region that the account has
// not enabled rejects valid keys with the same codes as bad keys. So when
// a region was skipped or refused the credentials, SweepError asks STS
// once, in a region that every account has enabled, and reports the
// credentials or the regions.
func SweepError(ctx context.Context, p *awsx.Provider, res *fleet.SweepResult) error {
	if len(res.Answered) > 0 {
		return nil
	}
	lookalike := len(res.Skipped) > 0
	var closed error // the first region that refused the credentials as closed
	for i, err := range res.Errs {
		switch res.Failures[i].Reason {
		case apidoc.ReasonCredentialError:
			// FormatAWSError writes the headline and the help for a
			// credential error. Another headline before it would say
			// the same thing twice.
			return awsx.FormatAWSError(err, "listing clusters")
		case apidoc.ReasonRegionUnavailable:
			lookalike = true
			if closed == nil {
				closed = err
			}
		case apidoc.ReasonAccessDenied:
			// A wrong secret key gets an unreadable 403 from EKS, which
			// reads as a denial. STS tells which it is.
			lookalike = true
		}
	}
	if lookalike && ctx.Err() == nil {
		region := fleetdata.AlwaysOnRegion(p.Region())
		_, err := p.Clients(region).STS.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
		switch {
		case err != nil && awsx.IsCredentialError(err):
			return awsx.FormatAWSError(err, "checking the credentials")
		case err == nil && closed != nil:
			return &RegionsClosedError{STSRegion: region, Err: closed}
		}
	}
	regions := len(res.Regions.Names)
	if len(res.Errs) == 0 {
		return fmt.Errorf("could not list clusters in any of %d region(s): none is accessible to these credentials; %s",
			regions, regionScopeHint)
	}
	first := awsx.FormatAWSError(res.Errs[0], "listing clusters in "+res.Failures[0].Name)
	if regions == 1 {
		return first
	}
	return fmt.Errorf("could not list clusters in any of %d region(s); the first error, for %s: %w",
		regions, res.Failures[0].Name, first)
}

// ClusterError returns the error of a command whose cluster could not be
// read. err is the error of fleet.Fetcher.Fetch. A cluster that does not
// exist gets the names of the clusters that are close to it, which costs
// one list call; any other error gets the AWS help text.
func ClusterError(ctx context.Context, fetcher *fleet.Fetcher, c *awsx.Clients, cluster string, err error) error {
	if !awsx.IsNotFound(err) {
		return awsx.FormatAWSError(err, "describing cluster "+cluster)
	}
	// Without the list the error still says what is wrong.
	names, _ := fetcher.Clusters(ctx, c)
	return cmdutil.NotFoundError("cluster", cluster, c.Region, names, "refresh cluster list -r "+c.Region)
}
