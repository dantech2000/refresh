package upgrade

import (
	"context"
	"fmt"
	"time"

	"github.com/dantech2000/refresh/internal/services/addons"
)

// addonWaitTimeout bounds how long a single addon update may take before the
// phase is considered failed.
const addonWaitTimeout = 20 * time.Minute

// UpgradeAddons updates every installed addon (minus the skip list) to the
// latest version compatible with targetVersion, serially in dependency order
// (vpc-cni → coredns/kube-proxy → the rest), waiting for each to go ACTIVE.
//
// It runs after the control-plane step of a hop, so targetVersion is also the
// cluster's (new) current version; versions are still chosen explicitly
// against targetVersion rather than "latest for whatever the cluster runs"
// so the intent survives mid-phase retries. The addon service's built-in
// pre/post health checks act as the phase gate: the first failure halts the
// phase (and therefore the hop) with the failing addon named.
func (s *Service) UpgradeAddons(ctx context.Context, clusterName, targetVersion string, skip []string, progress ProgressFunc) error {
	progress = ensureProgress(progress)
	svc := s.addonsService()

	addonList, err := svc.List(ctx, clusterName, addons.ListOptions{})
	if err != nil {
		return err
	}
	addonList = addons.SortByDependency(addonList)

	for _, a := range addonList {
		if matchesAny(a.Name, skip) {
			progress("addon %s: skipped (managed out-of-band)", a.Name)
			continue
		}

		versions, err := svc.GetAvailableVersions(ctx, a.Name, targetVersion)
		if err != nil {
			return fmt.Errorf("addon %s: no version compatible with %s: %w", a.Name, targetVersion, err)
		}
		chosen := versions[0].Version

		if addons.CompareVersions(a.Version, chosen) >= 0 {
			progress("addon %s already at %s (latest compatible with %s), skipping", a.Name, a.Version, targetVersion)
			continue
		}

		progress("addon %s: %s → %s", a.Name, a.Version, chosen)
		result, err := svc.Update(ctx, clusterName, a.Name, addons.UpdateOptions{
			Version:      chosen,
			HealthCheck:  true,
			Wait:         true,
			WaitTimeout:  addonWaitTimeout,
			PollInterval: s.PollInterval,
		})
		if err != nil {
			return fmt.Errorf("addon %s update to %s failed: %w", a.Name, chosen, err)
		}
		if result.HealthIssues != "" {
			return fmt.Errorf("addon %s updated to %s but failed its health gate: %s", a.Name, chosen, result.HealthIssues)
		}
		progress("addon %s is ACTIVE at %s", a.Name, chosen)
	}
	return nil
}
