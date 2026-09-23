package addon

import (
	"slices"
	"testing"

	"github.com/urfave/cli/v3"
)

func timeoutEnvKeys(t *testing.T, cmd *cli.Command) []string {
	t.Helper()
	for _, f := range cmd.Flags {
		if df, ok := f.(*cli.DurationFlag); ok && df.Name == "timeout" {
			return df.Sources.EnvKeys()
		}
	}
	t.Fatalf("%s: no --timeout flag", cmd.Name)
	return nil
}

// REFRESH_TIMEOUT sets short API/read timeouts. It must not cap a
// long-running addon update, but still applies to list/describe.
func TestRefreshTimeoutEnvScoping(t *testing.T) {
	root := Command()
	for _, name := range []string{"update", "update-all"} {
		if keys := timeoutEnvKeys(t, findSub(root, name)); slices.Contains(keys, "REFRESH_TIMEOUT") {
			t.Errorf("addon %s --timeout must not read REFRESH_TIMEOUT", name)
		}
	}
	for _, name := range []string{"list", "describe"} {
		if keys := timeoutEnvKeys(t, findSub(root, name)); !slices.Contains(keys, "REFRESH_TIMEOUT") {
			t.Errorf("addon %s --timeout should still read REFRESH_TIMEOUT", name)
		}
	}
}
