package upgrade

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/services/addons"
)

// addonFailure is the failure of an addon update that returned err: the
// result's own failure when it has one, else one built from err.
func addonFailure(clusterName, addon, updateID string, result *addons.AddonUpdateResult, err error) *diag.Failure {
	if result != nil && result.Failure != nil {
		return result.Failure
	}
	return addons.UpdateFailure(diag.KindAddon, clusterName, addon, updateID, err)
}

// addonWaitTimeout bounds how long a single addon update may take before the
// phase is considered failed.
const addonWaitTimeout = 20 * time.Minute

// UpgradeAddons updates every installed addon (minus the skip list) to the
// latest version compatible with targetVersion, serially in dependency order
// (vpc-cni → coredns/kube-proxy → the rest), waiting for each to go ACTIVE.
// When only is not empty, the phase updates just the addons it names: the
// engine runs one phase for the addons the new control plane cannot run
// (before the nodegroup rolls) and one for the rest (after them).
//
// It runs after the control-plane step of a hop, so targetVersion is also the
// cluster's (new) current version; versions are still chosen explicitly
// against targetVersion rather than "latest for whatever the cluster runs"
// so the intent survives mid-phase retries. The addon service's built-in
// pre/post health checks act as the phase gate: the first failure halts the
// phase (and therefore the hop) with the failing addon named.
func (s *Service) UpgradeAddons(ctx context.Context, clusterName, targetVersion string, skip, only []string, progress ProgressFunc) error {
	return s.updateAddonsFor(ctx, clusterName, targetVersion, skip, only,
		func(current string, versions []addons.AddonVersionInfo) (bool, string) {
			if addons.CompareVersions(current, versions[0].Version) >= 0 {
				return true, fmt.Sprintf("already at %s (latest compatible with %s)", current, targetVersion)
			}
			return false, ""
		}, progress)
}

// updateAddonsFor moves each installed addon (minus skip, and limited to
// only when it is not empty) to the newest version compatible with
// targetVersion, unless keep reports that its current version satisfies the
// phase, with the reason to print. An upgrade keeps an addon at or above
// the newest version; a rollback keeps one that targetVersion lists.
func (s *Service) updateAddonsFor(ctx context.Context, clusterName, targetVersion string, skip, only []string, keep func(current string, versions []addons.AddonVersionInfo) (bool, string), progress ProgressFunc) error {
	progress = ensureProgress(progress)
	svc := s.addonsService()

	addonList, err := svc.List(ctx, clusterName, addons.ListOptions{})
	if err != nil {
		return onItem(diag.KindCluster, clusterName, diag.OpListAddons, err)
	}
	addonList = addons.SortByDependency(addonList)

	for _, a := range addonList {
		if len(only) > 0 && !slices.Contains(only, a.Name) {
			continue
		}
		if isSkippedAddon(a.Name, skip) {
			progress("addon %s: skipped (managed out-of-band)", a.Name)
			continue
		}

		onAddon := func(op string, err error) error { return onItem(diag.KindAddon, a.Name, op, err) }
		versions, err := svc.GetAvailableVersions(ctx, a.Name, targetVersion)
		if err != nil {
			if errors.Is(err, addons.ErrNoVersionsFound) {
				return onAddon(diag.OpDescribeAddonVersions, fmt.Errorf("addon %s: no version compatible with %s: %w", a.Name, targetVersion, err))
			}
			return onAddon(diag.OpDescribeAddonVersions, fmt.Errorf("addon %s: looking up versions compatible with %s: %w", a.Name, targetVersion, err))
		}
		chosen := versions[0].Version

		// Resume support: a re-run after Ctrl+C may find an addon still
		// CREATING/UPDATING from the previous run. The control-plane and
		// nodegroup phases attach to such in-flight updates; the addon phase
		// must too, or svc.Update's pre-update health gate hard-fails on the
		// UPDATING status. Wait for it to settle, then re-read the installed
		// version and let the normal skip/converge logic below decide.
		current, status, err := svc.AddonStatus(ctx, clusterName, a.Name)
		if err != nil {
			return onAddon(diag.OpDescribeAddon, fmt.Errorf("addon %s: reading status: %w", a.Name, err))
		}
		if status == ekstypes.AddonStatusCreating || status == ekstypes.AddonStatusUpdating {
			progress("addon %s is %s (in-flight update from a previous run); attaching and waiting for it to settle", a.Name, status)
			if err := svc.WaitUntilActive(ctx, clusterName, a.Name, addonWaitTimeout, s.PollInterval); err != nil {
				return onAddon(diag.OpDescribeAddon, fmt.Errorf("addon %s: waiting for in-flight update to finish: %w", a.Name, err))
			}
			if current, _, err = svc.AddonStatus(ctx, clusterName, a.Name); err != nil {
				return onAddon(diag.OpDescribeAddon, fmt.Errorf("addon %s: reading status after attach: %w", a.Name, err))
			}
		}

		if ok, why := keep(current, versions); ok {
			progress("addon %s %s, skipping", a.Name, why)
			continue
		}

		progress("addon %s: %s → %s", a.Name, current, chosen)
		result, err := svc.Update(ctx, clusterName, a.Name, addons.UpdateOptions{
			Version: chosen,
			// Validate the pinned version against the phase's target, not
			// the live control plane: a rollback downgrades addons before
			// the control plane moves back.
			KubernetesVersion: targetVersion,
			HealthCheck:       true,
			Wait:              true,
			WaitTimeout:       addonWaitTimeout,
			PollInterval:      s.PollInterval,
		})
		if err != nil {
			updateID := ""
			if result != nil {
				updateID = result.UpdateID
			}
			return &itemError{
				failure: addonFailure(clusterName, a.Name, updateID, result, err),
				err:     fmt.Errorf("addon %s update to %s failed: %w", a.Name, chosen, err),
			}
		}
		if result.HealthIssues != "" {
			return &gateError{err: fmt.Errorf("addon %s updated to %s but failed its health gate: %s", a.Name, chosen, result.HealthIssues)}
		}
		if result.Failure != nil {
			// The update landed, but its health could not be read: the gate
			// can't pass, and the read is the failure.
			return &gateError{
				err:      fmt.Errorf("addon %s updated to %s but its health gate could not read it: %s", a.Name, chosen, result.Failure.Error),
				failures: []diag.Failure{*result.Failure},
			}
		}
		progress("addon %s is ACTIVE at %s", a.Name, chosen)
	}
	return nil
}
