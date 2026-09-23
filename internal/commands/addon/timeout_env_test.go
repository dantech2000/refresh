package addon

import (
	"slices"
	"testing"

	"github.com/urfave/cli/v3"
)

// timeoutFlag returns cmd's own --timeout flag, or nil.
func timeoutFlag(cmd *cli.Command) *cli.DurationFlag {
	for _, f := range cmd.Flags {
		if df, ok := f.(*cli.DurationFlag); ok && df.Name == "timeout" {
			return df
		}
	}
	return nil
}

// REFRESH_TIMEOUT sets short API/read timeouts. It must not cap a
// long-running addon update. list and describe declare no --timeout of their
// own: they use the global one, which reads REFRESH_TIMEOUT (see
// main_flags_test.go).
func TestRefreshTimeoutEnvScoping(t *testing.T) {
	root := Command()
	for _, name := range []string{"update", "update-all"} {
		df := timeoutFlag(findSub(root, name))
		if df == nil {
			t.Fatalf("addon %s: no --timeout flag", name)
		}
		if slices.Contains(df.Sources.EnvKeys(), "REFRESH_TIMEOUT") {
			t.Errorf("addon %s --timeout must not read REFRESH_TIMEOUT", name)
		}
	}
	for _, name := range []string{"list", "describe"} {
		if timeoutFlag(findSub(root, name)) != nil {
			t.Errorf("addon %s must not shadow the global --timeout", name)
		}
	}
}
