// Package gendocs implements the hidden "refresh gen-docs" command, which
// writes the command reference of the docs site.
package gendocs

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"

	"github.com/dantech2000/refresh/internal/cmdutil"
)

type options struct {
	io  *cmdutil.IOStreams
	out string
}

// NewCmdGenDocs returns the gen-docs command.
func NewCmdGenDocs(f *cmdutil.Factory) *cobra.Command {
	o := &options{io: f.IOStreams}
	cmd := &cobra.Command{
		Use:    "gen-docs --out <directory>",
		Hidden: true,
		Short:  "Generate the Markdown command reference",
		Long: `Write one Markdown page for each command to a directory. The pages come
from the command tree of this binary, so they match its commands and flags.
Two runs of the same binary write the same bytes.`,
		Example: `  refresh gen-docs --out docs/reference`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return o.run(cmd.Root())
		},
	}
	cmd.Flags().StringVar(&o.out, "out", "", "Directory for the generated pages (required)")
	// The flag was added above, so this cannot fail.
	_ = cmd.MarkFlagRequired("out")
	return cmd
}

func (o *options) run(root *cobra.Command) error {
	if err := os.MkdirAll(o.out, 0o755); err != nil { //nolint:gosec // G301: the pages are public documentation
		return fmt.Errorf("creating %s: %w", o.out, err)
	}
	// Cobra ends each page with a line that holds the date of the run.
	// Without it the pages change only when a command changes.
	disableAutoGenTag(root)
	if err := doc.GenMarkdownTree(root, o.out); err != nil {
		return fmt.Errorf("writing the reference to %s: %w", o.out, err)
	}
	_, err := fmt.Fprintf(o.io.Out, "Generated the command reference in %s\n", o.out)
	return err
}

func disableAutoGenTag(cmd *cobra.Command) {
	cmd.DisableAutoGenTag = true
	for _, c := range cmd.Commands() {
		disableAutoGenTag(c)
	}
}
