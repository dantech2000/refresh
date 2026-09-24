package addons

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/aws/awserr"
	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/services/common"
)

// ErrNoVersionsFound is returned (wrapped) by GetAvailableVersions when the
// API call succeeded but EKS lists no version of the addon, or none
// compatible with the requested Kubernetes version. Callers use errors.Is to
// tell "genuinely incompatible" apart from an API failure.
var ErrNoVersionsFound = errors.New("no versions found")

// GetAvailableVersions returns available versions for an addon, newest first.
// Pass k8sVersion to restrict results to versions compatible with that
// Kubernetes version. All pages are fetched and the result is sorted by
// version (the API does not document an ordering guarantee).
func (s *ServiceImpl) GetAvailableVersions(ctx context.Context, addonName string, k8sVersion string) ([]AddonVersionInfo, error) {
	newInput := func(token *string) *eks.DescribeAddonVersionsInput {
		input := &eks.DescribeAddonVersionsInput{
			AddonName: aws.String(addonName),
			NextToken: token,
		}
		if k8sVersion != "" {
			input.KubernetesVersion = aws.String(k8sVersion)
		}
		return input
	}

	addonInfos, err := awsinternal.ListAllPages(ctx, fmt.Sprintf("describing versions for addon %s", addonName),
		func(rc context.Context, token *string) (*eks.DescribeAddonVersionsOutput, error) {
			return s.eksClient.DescribeAddonVersions(rc, newInput(token))
		},
		func(out *eks.DescribeAddonVersionsOutput) ([]ekstypes.AddonInfo, *string) {
			return out.Addons, out.NextToken
		},
	)
	if err != nil {
		return nil, fmt.Errorf("describing addon versions: %w", err)
	}

	var versions []AddonVersionInfo
	for _, info := range addonInfos {
		for _, v := range info.AddonVersions {
			var compatibilities []string
			for _, c := range v.Compatibilities {
				if c.ClusterVersion != nil {
					compatibilities = append(compatibilities, *c.ClusterVersion)
				}
			}
			versions = append(versions, AddonVersionInfo{
				Version:           aws.ToString(v.AddonVersion),
				Compatibilities:   compatibilities,
				Architecture:      append([]string{}, v.Architecture...),
				RequiresIAMPolicy: v.RequiresIamPermissions,
			})
		}
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("%w for addon %s", ErrNoVersionsFound, addonName)
	}

	// Newest first: callers treat versions[0] as "latest".
	sort.SliceStable(versions, func(i, j int) bool {
		return compareAddonVersions(versions[i].Version, versions[j].Version) > 0
	})

	return versions, nil
}

// CompareVersions compares EKS addon version strings such as
// "v1.18.1-eksbuild.3", returning >0 when a is newer than b. Exported for the
// upgrade orchestrator's already-satisfied checks.
func CompareVersions(a, b string) int {
	return compareAddonVersions(a, b)
}

// compareAddonVersions compares EKS addon version strings such as
// "v1.18.1-eksbuild.3", returning >0 when a is newer than b. Numeric segments
// are compared numerically; non-numeric segments lexically.
func compareAddonVersions(a, b string) int {
	segs := func(v string) []string {
		v = strings.TrimPrefix(strings.TrimSpace(v), "v")
		return strings.FieldsFunc(v, func(r rune) bool { return r == '.' || r == '-' || r == '+' })
	}
	as, bs := segs(a), segs(b)
	for i := 0; i < len(as) && i < len(bs); i++ {
		an, bn := isDigits(as[i]), isDigits(bs[i])
		switch {
		case an && bn:
			if c := compareDigits(as[i], bs[i]); c != 0 {
				return c
			}
		case an:
			return 1 // numeric beats non-numeric ("1" > "eksbuild")
		case bn:
			return -1
		default:
			if c := strings.Compare(as[i], bs[i]); c != 0 {
				return c
			}
		}
	}
	return len(as) - len(bs)
}

// isDigits reports whether s is a non-empty run of ASCII digits.
func isDigits(s string) bool {
	return s != "" && strings.TrimLeft(s, "0123456789") == ""
}

// compareDigits compares two ASCII digit strings by numeric value, with no
// size limit: a segment too large for an int still orders as a number.
func compareDigits(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		return len(a) - len(b)
	}
	return strings.Compare(a, b)
}

// WaitUntilActive blocks until the addon reaches ACTIVE — attaching to an
// in-flight update started by a previous run — or the update fails, bounded by
// timeout. It is the exported entry point the upgrade orchestrator uses to
// resume an addon that is still CREATING/UPDATING. It has no update ID to
// follow, so it watches the addon status; Update follows its own update ID.
func (s *ServiceImpl) WaitUntilActive(ctx context.Context, clusterName, addonName string, timeout, pollInterval time.Duration) error {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if pollInterval <= 0 {
		pollInterval = addonUpdatePollInterval
	}
	op := fmt.Sprintf("checking the status of addon %s", addonName)
	var lastErr error
	return pollUntil(ctx, pollInterval, func() (bool, error) {
		desc, err := s.eksClient.DescribeAddon(ctx, &eks.DescribeAddonInput{
			ClusterName: aws.String(clusterName),
			AddonName:   aws.String(addonName),
		})
		if err != nil {
			lastErr = err
			return false, diag.WithOperation(diag.OpDescribeAddon, pollError(ctx, err, op))
		}
		if desc == nil || desc.Addon == nil {
			return false, nil
		}
		switch desc.Addon.Status {
		case ekstypes.AddonStatusActive:
			return true, nil
		case ekstypes.AddonStatusDegraded, ekstypes.AddonStatusCreateFailed,
			ekstypes.AddonStatusUpdateFailed, ekstypes.AddonStatusDeleteFailed:
			return false, fmt.Errorf("addon update failed: status %s", desc.Addon.Status)
		}
		return false, nil
	}, func(err error) error {
		return waitTimeoutError(err, fmt.Sprintf("waiting for addon %s to become ACTIVE", addonName), lastErr)
	})
}

// waitForAddonUpdate follows the EKS update updateID until it is Successful,
// Failed, or Cancelled, then confirms the addon reports targetVersion.
// Transient poll errors (throttling, 5xx, network) are polled through; a
// permanent API error (AccessDenied, validation) fails at once. pollInterval
// falls back to addonUpdatePollInterval when zero.
func (s *ServiceImpl) waitForAddonUpdate(ctx context.Context, clusterName, addonName, updateID, targetVersion string, pollInterval time.Duration) error {
	if updateID == "" {
		return fmt.Errorf("waiting for addon %s update: EKS returned no update ID", addonName)
	}
	if pollInterval <= 0 {
		pollInterval = addonUpdatePollInterval
	}
	op := fmt.Sprintf("checking update %s of addon %s", updateID, addonName)
	var lastErr error
	var lastStatus ekstypes.UpdateStatus
	err := pollUntil(ctx, pollInterval, func() (bool, error) {
		out, err := s.eksClient.DescribeUpdate(ctx, &eks.DescribeUpdateInput{
			Name:      aws.String(clusterName),
			UpdateId:  aws.String(updateID),
			AddonName: aws.String(addonName),
		})
		if err != nil {
			lastErr = err
			return false, diag.WithOperation(diag.OpDescribeUpdate, pollError(ctx, err, op))
		}
		if out == nil || out.Update == nil {
			return false, nil
		}
		lastStatus = out.Update.Status
		switch out.Update.Status {
		case ekstypes.UpdateStatusSuccessful:
			return true, nil
		case ekstypes.UpdateStatusFailed, ekstypes.UpdateStatusCancelled:
			return false, &updateEndedError{addon: addonName, updateID: updateID, status: out.Update.Status, details: updateErrorDetails(out.Update.Errors)}
		}
		return false, nil
	}, func(err error) error {
		what := fmt.Sprintf("waiting for addon %s update %s", addonName, updateID)
		if lastStatus != "" {
			what += fmt.Sprintf(" (last status %s)", lastStatus)
		}
		return waitTimeoutError(err, what, lastErr)
	})
	if err != nil {
		return err
	}

	// EKS says the update succeeded; make sure the addon agrees.
	desc, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeAddonOutput, error) {
		return s.eksClient.DescribeAddon(rc, &eks.DescribeAddonInput{
			ClusterName: aws.String(clusterName),
			AddonName:   aws.String(addonName),
		})
	})
	if err != nil {
		return diag.WithOperation(diag.OpDescribeAddon, awsinternal.FormatAWSError(err, fmt.Sprintf("confirming the version of addon %s", addonName)))
	}
	if desc == nil || desc.Addon == nil {
		return fmt.Errorf("confirming the version of addon %s: empty DescribeAddon response", addonName)
	}
	if got := aws.ToString(desc.Addon.AddonVersion); CompareVersions(got, targetVersion) != 0 {
		return fmt.Errorf("addon %s update %s reported Successful, but the addon is at %s, not %s", addonName, updateID, got, targetVersion)
	}
	return nil
}

// pollUntil calls check at once and then every interval until it reports
// done or returns an error. When ctx ends first, it returns onTimeout(ctx.Err()).
func pollUntil(ctx context.Context, interval time.Duration, check func() (bool, error), onTimeout func(error) error) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		done, err := check()
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return onTimeout(ctx.Err())
		case <-ticker.C:
		}
	}
}

// pollError classifies a failed status poll. It returns nil to keep polling
// on a transient error (throttling, 5xx, network) or when ctx has ended (the
// poll loop then reports the timeout), and a formatted error for a permanent
// API error such as AccessDenied, which no amount of polling will fix.
func pollError(ctx context.Context, err error, op string) error {
	if keepPolling(ctx, err) {
		return nil
	}
	return awsinternal.FormatAWSError(err, op)
}

// keepPolling reports whether a failed status poll is worth repeating.
func keepPolling(ctx context.Context, err error) bool {
	return ctx.Err() != nil || common.IsRetryable(err) || awserr.IsNetworkError(err)
}

// waitTimeoutError reports a wait that ran out of time, with the last poll
// error when there was one: a timeout after repeated throttling reads
// differently from one where the update was only slow.
func waitTimeoutError(ctxErr error, what string, lastErr error) error {
	if lastErr != nil {
		return fmt.Errorf("%s: %w (last poll error: %s)", what, ctxErr, awserr.Summary(lastErr))
	}
	return fmt.Errorf("%s: %w", what, ctxErr)
}

// updateErrorDetails renders an EKS update's error details as
// ": CODE: msg [ids]; ...", or "" when there are none.
func updateErrorDetails(details []ekstypes.ErrorDetail) string {
	if len(details) == 0 {
		return ""
	}
	parts := make([]string, 0, len(details))
	for _, d := range details {
		p := string(d.ErrorCode)
		if msg := aws.ToString(d.ErrorMessage); msg != "" {
			if p != "" {
				p += ": "
			}
			p += msg
		}
		if len(d.ResourceIds) > 0 {
			p += " [" + strings.Join(d.ResourceIds, ", ") + "]"
		}
		parts = append(parts, p)
	}
	return ": " + strings.Join(parts, "; ")
}

func mapAddonHealth(status ekstypes.AddonStatus) Health {
	switch status {
	case ekstypes.AddonStatusActive:
		return HealthPass
	case ekstypes.AddonStatusDegraded, ekstypes.AddonStatusCreateFailed,
		ekstypes.AddonStatusUpdateFailed, ekstypes.AddonStatusDeleteFailed:
		return HealthFail
	case ekstypes.AddonStatusCreating, ekstypes.AddonStatusDeleting, ekstypes.AddonStatusUpdating:
		return HealthInProgress
	default:
		return HealthUnknown
	}
}
