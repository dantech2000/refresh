package cluster

import (
	"slices"
	"testing"

	"github.com/urfave/cli/v3"
)

// REFRESH_TIMEOUT sets short API/read timeouts. It must not cap the
// multi-hour cluster upgrade.
func TestUpgradeTimeoutIgnoresRefreshTimeoutEnv(t *testing.T) {
	for _, f := range upgradeCommand().Flags {
		df, ok := f.(*cli.DurationFlag)
		if !ok || df.Name != "timeout" {
			continue
		}
		if slices.Contains(df.Sources.EnvKeys(), "REFRESH_TIMEOUT") {
			t.Fatal("cluster upgrade --timeout must not read REFRESH_TIMEOUT")
		}
		return
	}
	t.Fatal("cluster upgrade: no --timeout flag")
}
