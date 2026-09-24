package runner

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/smithy-go"
	"github.com/fatih/color"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/aws/awserr"
	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/ui"
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
	_, _ = fmt.Fprintln(w, ui.ColorFor(w, color.FgYellow).Sprintf("Skipped %d region(s) not accessible to these credentials: %s (%s)",
		len(skipped), strings.Join(skipped, ", "), RegionScopeHint))
}

// NoRegionAnswered explains a region sweep in which no region answered.
// Command setup only resolves credentials and makes no STS call, so keys that
// resolve but are invalid or expired reach the sweep. skipped holds the
// regions the sweep skipped as closed, and failed the failure of every region
// that failed (all of them, not only the first: one region can fail for
// another reason before a later one names the credentials).
//
// It returns the credential error, with the setup help once, in two cases:
//   - a region failed as CredentialError: a credential error that no closed
//     region returns (ExpiredTokenException, a signature error, a credential
//     source failure). No STS call is needed.
//   - a region was skipped or failed as unavailable. Invalid keys get the
//     same codes as a region closed to the account
//     (UnrecognizedClientException, InvalidClientTokenId), so it asks STS
//     once.
//
// It returns nil otherwise (valid credentials, another failure, ctx done),
// and the caller reports its own error.
func NoRegionAnswered(ctx context.Context, cfg aws.Config, skipped []string, failed []diag.Failure) error {
	for _, f := range failed {
		if f.Reason == diag.ReasonCredentialError {
			return fmt.Errorf("AWS credential validation failed: %w", awserr.CredentialSetupError(failureError(f)))
		}
	}
	lookalike := len(skipped) > 0 || slices.ContainsFunc(failed, func(f diag.Failure) bool {
		return f.Reason == diag.ReasonRegionUnavailable
	})
	select {
	case <-ctx.Done():
		return nil // interrupted or out of time: the caller's error says so
	default:
	}
	if !lookalike {
		return nil
	}
	if err := awsinternal.CheckAWSCredentials(ctx, cfg); err != nil && awserr.IsCredentialError(err) {
		return err
	}
	return nil
}

// failureError rebuilds a region failure as an error: an API error with the
// failure's AWS error code when it has one, so errors.As and ErrorCode keep
// working on the result.
func failureError(f diag.Failure) error {
	if f.AWSErrorCode == "" {
		return fmt.Errorf("region %s: %s", f.Name, f.Error)
	}
	msg := strings.TrimPrefix(f.Error, f.AWSErrorCode+": ")
	return fmt.Errorf("region %s: %w", f.Name, &smithy.GenericAPIError{Code: f.AWSErrorCode, Message: msg})
}

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
