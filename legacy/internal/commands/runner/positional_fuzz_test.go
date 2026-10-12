package runner

import (
	"context"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"
)

// fuzzSlotValue turns fuzz input into a slot value that parses the same as
// a flag value and as a positional: trimmed, non-empty, no leading "-".
func fuzzSlotValue(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "-") {
		s = "x" + s
	}
	return s
}

// FuzzPositionalSlot fills the (cluster, addon, version) slots of a command
// like `addon update`, each either with its flag or positionally, with the
// flags interleaved anywhere among the positionals. PositionalSlot must
// return every slot's value whatever the mix and the order, and extra
// trailing positionals must not change the result.
func FuzzPositionalSlot(f *testing.F) {
	f.Add("prod", "vpc-cni", "v1.18.1-eksbuild.3", uint8(0), uint8(0), "")
	f.Add("prod", "vpc-cni", "v1.2.3", uint8(2), uint8(1), "extra")
	f.Add("prod", "coredns", "latest", uint8(7), uint8(5), "")
	f.Add(" -c ", "=", "a=b", uint8(5), uint8(3), "--")
	f.Add("x", "y", "z", uint8(4), uint8(255), "tail")
	f.Fuzz(func(t *testing.T, cluster, addon, version string, flagMask, order uint8, extra string) {
		names := []string{"cluster", "addon", "version"}
		values := []string{fuzzSlotValue(cluster), fuzzSlotValue(addon), fuzzSlotValue(version)}

		var positionals, flagTokens []string
		for i, name := range names {
			if flagMask&(1<<i) != 0 {
				flagTokens = append(flagTokens, "--"+name+"="+values[i])
			} else {
				positionals = append(positionals, values[i])
			}
		}
		if extra != "" && !strings.ContainsAny(extra, "\n") {
			positionals = append(positionals, fuzzSlotValue(extra))
		}
		// Insert each flag token at a position chosen by order.
		argv := append([]string(nil), positionals...)
		for i, tok := range flagTokens {
			at := int(order>>(2*i)) % (len(argv) + 1)
			argv = append(argv[:at], append([]string{tok}, argv[at:]...)...)
		}

		// A leaf under a root, as in the real tree. HideHelpCommand keeps
		// urfave/cli from reading a first positional "h" or "help" as its
		// help command, which happens before PositionalSlot runs.
		var got []string
		root := &cli.Command{
			Name:            "refresh",
			HideHelpCommand: true,
			Commands: []*cli.Command{{
				Name: "update",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "cluster"},
					&cli.StringFlag{Name: "addon"},
					&cli.StringFlag{Name: "version"},
				},
				Action: func(_ context.Context, c *cli.Command) error {
					got = []string{
						PositionalSlot(c, "cluster"),
						PositionalSlot(c, "addon", "cluster"),
						PositionalSlot(c, "version", "cluster", "addon"),
					}
					return nil
				},
			}},
		}
		if err := root.Run(context.Background(), append([]string{"refresh", "update"}, argv...)); err != nil || got == nil {
			t.Fatalf("argv %q: err = %v, action ran = %v", argv, err, got != nil)
		}
		for i := range names {
			if got[i] != values[i] {
				t.Fatalf("argv %q: %s = %q, want %q", argv, names[i], got[i], values[i])
			}
		}
	})
}
