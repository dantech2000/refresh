package runner

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/aws/awserr"
	appconfig "github.com/dantech2000/refresh/internal/config"
	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/render"
)

// RegionScopeHint tells the user how to narrow a region sweep.
const RegionScopeHint = "scope with -r or REFRESH_EKS_REGIONS"

// ReportSkippedRegions writes the one notice line of a default region sweep
// (no -r, no REFRESH_EKS_REGIONS) that skipped regions closed to these
// credentials, such as an SCP denial or a region that is not enabled:
//
//	Skipped 2 region(s) not accessible to these credentials: ap-east-1, me-south-1 (scope with -r or REFRESH_EKS_REGIONS)
//
// Skipped regions are not failures: they do not go in the document's
// "failures" and do not change the exit code. It writes nothing when skipped
// is empty.
func ReportSkippedRegions(w io.Writer, skipped []string) {
	if len(skipped) == 0 {
		return
	}
	th := render.Default(w)
	_, _ = fmt.Fprintln(w, th.Paint(th.Pal.Yellow, fmt.Sprintf("Skipped %d region(s) not accessible to these credentials: %s (%s)",
		len(skipped), strings.Join(skipped, ", "), RegionScopeHint)))
}

// NoRegionAnswered explains a region sweep in which no region answered.
// Command setup only resolves credentials and makes no STS call, so keys that
// resolve but are invalid or expired reach the sweep. skipped holds the
// regions the sweep skipped as closed, and errs the original error of every
// region that failed: all of them, not only the first (one region can fail
// for another reason before a later one names the credentials), and the
// errors themselves, not their diag.Failure summaries (a summary keeps one
// line of text and drops the error chain).
//
// It returns the credential error, with the setup help once, in two cases:
//   - a region failed with a credential error that no closed region returns
//     (ExpiredTokenException, a signing error, a credential source
//     failure). It returns that region's error, so errors.Is/As still reach
//     the cause. No STS call is needed.
//   - a region was skipped or failed as unavailable. Invalid keys get the
//     same codes as a region closed to the account
//     (UnrecognizedClientException, InvalidClientTokenId), so it asks STS
//     once.
//
// It returns nil otherwise (valid credentials, another failure, ctx done),
// and the caller reports its own error.
func NoRegionAnswered(ctx context.Context, cfg aws.Config, skipped []string, errs []error) error {
	lookalike := len(skipped) > 0
	var closed error // the first region that refused the credentials as closed
	for _, err := range errs {
		// Classify as the sweeps do, so this agrees with the failure list.
		switch diag.FromError(diag.KindRegion, "", diag.OpListClusters, err).Reason {
		case diag.ReasonCredentialError:
			// FormatAWSError adds the setup help, or returns an error that
			// already carries it unchanged.
			return fmt.Errorf("AWS credential validation failed: %w", awsinternal.FormatAWSError(err, "listing clusters"))
		case diag.ReasonRegionUnavailable:
			lookalike = true
			if closed == nil {
				closed = err
			}
		}
	}
	select {
	case <-ctx.Done():
		return nil // interrupted or out of time: the caller's error says so
	default:
	}
	if !lookalike {
		return nil
	}
	stsCfg := cfg.Copy()
	stsCfg.Region = appconfig.STSRegion(cfg.Region)
	err := awsinternal.CheckAWSCredentials(ctx, stsCfg)
	switch {
	case err != nil && awserr.IsCredentialError(err):
		return err
	case err == nil && closed != nil:
		// STS took the keys, so the region, not the keys, refused them. The
		// region's own error would print the credential setup help.
		return &RegionsClosedError{STSRegion: stsCfg.Region, Err: closed}
	}
	return nil
}

// RegionsClosedError reports a sweep in which every region that failed
// refused credentials that STS accepts: the regions are not enabled for the
// account, or a policy blocks them.
type RegionsClosedError struct {
	STSRegion string // where the credentials were checked
	Err       error  // the first region's error
}

func (e *RegionsClosedError) Error() string {
	return fmt.Sprintf("no region answered, but STS in %s accepts these credentials\n"+
		"  first error: %s\n"+
		"A region refuses valid credentials when the account has not enabled it (an opt-in region) or a policy such as an SCP blocks it.\n"+
		"Enable the region in the account settings, or choose other regions (%s).",
		e.STSRegion, strings.TrimSuffix(awserr.Summary(e.Err), "."), RegionScopeHint)
}

func (e *RegionsClosedError) Unwrap() error { return e.Err }

// TableListsFailures reports whether the output format is a human view
// (table, or cluster list's tree) that lists the run's failures itself, in
// an INCOMPLETE DATA section (render.FailureSection). For every other format
// (json, yaml, plain) the command writes them to stderr with ReportFailures,
// so a table run does not print each failure twice.
func TableListsFailures(format string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "table", "tree":
		return true
	default:
		return false
	}
}
