// Package gendocs implements the hidden "refresh gen-docs" command, which
// writes the command reference: the Markdown pages of the docs site, or the
// man pages of a release archive.
package gendocs

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"

	"github.com/dantech2000/refresh/internal/cmdutil"
)

// The values of --format.
const (
	formatMarkdown = "markdown"
	formatMan      = "man"
)

type options struct {
	io      *cmdutil.IOStreams
	version string

	out    string
	format string
}

// NewCmdGenDocs returns the gen-docs command.
func NewCmdGenDocs(f *cmdutil.Factory) *cobra.Command {
	o := &options{io: f.IOStreams, version: f.Build.Version}
	cmd := &cobra.Command{
		Use:    "gen-docs --out <directory>",
		Hidden: true,
		Short:  "Generate the command reference",
		Long: `Write one page for each command to a directory, as Markdown or as man
pages. The pages come from the command tree of this binary, so they match
its commands and flags.

Two runs of the same binary write the same Markdown. A man page holds its
date, which is the time of the run, or SOURCE_DATE_EPOCH when that is set.`,
		Example: `  refresh gen-docs --out docs/reference

  # Man pages for a release archive
  refresh gen-docs --format man --out dist/man/man1`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.validate(); err != nil {
				return err
			}
			return o.run(cmd.Root())
		},
	}
	cmd.Flags().StringVar(&o.out, "out", "", "Directory for the generated pages (required)")
	cmd.Flags().StringVar(&o.format, "format", formatMarkdown, "Page format: markdown, man")
	// The flags were added above, so these cannot fail.
	_ = cmd.MarkFlagRequired("out")
	_ = cmd.RegisterFlagCompletionFunc("format",
		cobra.FixedCompletions([]cobra.Completion{formatMarkdown, formatMan}, cobra.ShellCompDirectiveNoFileComp))
	return cmd
}

func (o *options) validate() error {
	if o.format != formatMarkdown && o.format != formatMan {
		return fmt.Errorf("invalid --format %q: must be one of: %s, %s", o.format, formatMarkdown, formatMan)
	}
	return nil
}

func (o *options) run(root *cobra.Command) error {
	if err := os.MkdirAll(o.out, 0o755); err != nil { //nolint:gosec // G301: the pages are public documentation
		return fmt.Errorf("creating %s: %w", o.out, err)
	}
	// Cobra ends each page with a line that holds the date of the run.
	// Without it the Markdown pages change only when a command changes.
	disableAutoGenTag(root)

	generate := func(dir string) error { return doc.GenMarkdownTree(root, dir) }
	what := "the command reference"
	if o.format == formatMan {
		header := &doc.GenManHeader{
			Title:   "REFRESH",
			Section: "1",
			Source:  "refresh " + o.version,
			Manual:  "refresh manual",
		}
		generate = func(dir string) error { return doc.GenManTree(root, header, dir) }
		what = "the man pages"
	}
	// Cobra writes with os.Create, which follows a symbolic link and leaves
	// part of the pages when it fails. GenerateFiles prevents both.
	if err := cmdutil.GenerateFiles(o.out, generate); err != nil {
		return fmt.Errorf("writing %s to %s: %w", what, o.out, err)
	}
	_, err := fmt.Fprintf(o.io.Out, "Generated %s in %s\n", what, o.out)
	return err
}

func disableAutoGenTag(cmd *cobra.Command) {
	cmd.DisableAutoGenTag = true
	for _, c := range cmd.Commands() {
		disableAutoGenTag(c)
	}
}
