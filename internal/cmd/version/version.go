// Package version implements "refresh version".
package version

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/build"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/updatecheck"
)

// envNoUpdateCheck turns the update check off, as --no-update-check does.
const envNoUpdateCheck = "REFRESH_NO_UPDATE_CHECK"

type options struct {
	ios   *cmdutil.IOStreams
	build build.Info
	// latestTag returns the tag of the newest release.
	latestTag func(ctx context.Context) (string, error)

	output        cmdutil.Format
	noUpdateCheck bool
}

// NewCmdVersion returns the version command.
func NewCmdVersion(f *cmdutil.Factory) *cobra.Command {
	o := &options{ios: f.IOStreams, build: f.Build, latestTag: f.LatestRelease}
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print the version of refresh",
		Long: `Print the version, and the commit and build date of a release build.

On a terminal, the command also checks GitHub Releases for a newer version
and prints a one-line hint on stderr when there is one. The check runs at
most once a day, and a failed check prints nothing. It does not run when
stdout is not a terminal. To turn it off, use --no-update-check or set
REFRESH_NO_UPDATE_CHECK to any value other than 0, false, or no.

Exit codes:
  0  Done. A failed update check does not change the code.
  1  An error.`,
		Example: `  refresh version

  # The version as a document, for a script
  refresh version -o json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			o.complete()
			if err := o.validate(); err != nil {
				return err
			}
			return o.run(cmd.Context())
		},
	}
	cmdutil.AddOutputFlag(cmd, &o.output, cmdutil.FormatTable, cmdutil.FormatJSON, cmdutil.FormatYAML)
	cmd.Flags().BoolVar(&o.noUpdateCheck, "no-update-check", false,
		"Do not check for a newer release (env: "+envNoUpdateCheck+")")
	return cmd
}

// Format returns the lines that "refresh version" and "refresh --version"
// print.
func Format(info build.Info) string {
	var b strings.Builder
	fmt.Fprintf(&b, "refresh version: %s\n", info.Version)
	if info.Commit != "" {
		fmt.Fprintf(&b, "commit: %s\n", info.Commit)
	}
	if info.Date != "" {
		fmt.Fprintf(&b, "built: %s\n", info.Date)
	}
	return b.String()
}

func (o *options) complete() {
	if !o.noUpdateCheck {
		o.noUpdateCheck = envTruthy(os.Getenv(envNoUpdateCheck))
	}
}

// validate has nothing to reject: the output flag is checked as it is
// parsed.
func (o *options) validate() error { return nil }

func (o *options) run(ctx context.Context) error {
	if o.output.IsMachine() {
		doc := apidoc.Version{Version: o.build.Version, Commit: o.build.Commit, BuildDate: o.build.Date}
		if err := cmdutil.PrintDocument(o.ios, o.output, doc); err != nil {
			return err
		}
	} else if _, err := fmt.Fprint(o.ios.Out, Format(o.build)); err != nil {
		return err
	}
	o.printUpdateHint(ctx)
	return nil
}

// printUpdateHint writes one line to stderr when a newer release exists. It
// does nothing when the check is turned off or stdout is not a terminal, and
// it prints nothing when the check fails.
func (o *options) printUpdateHint(ctx context.Context) {
	if o.noUpdateCheck || !o.ios.OutTTY {
		return
	}
	latest, err := o.latestTag(ctx)
	if err != nil {
		return
	}
	if hint := updatecheck.UpgradeHint(o.build.Version, latest); hint != "" {
		// The hint is a courtesy. A failed write to stderr changes nothing.
		_, _ = fmt.Fprintln(o.ios.ErrOut, hint)
	}
}

// envTruthy reports whether v turns an opt-out on. Any value does, except
// the empty string, 0, false, and no.
func envTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "no":
		return false
	}
	return true
}
