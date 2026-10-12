package cmdutil

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/apidoc"
)

// Format is a value of the -o/--output flag.
type Format string

// The output formats. A command accepts the ones it gives to AddOutputFlag.
const (
	FormatTable Format = "table"
	FormatJSON  Format = "json"
	FormatYAML  Format = "yaml"
	FormatPlain Format = "plain"
)

// IsMachine reports whether f is JSON or YAML. Stdout then holds exactly one
// document, and everything for a person goes to stderr.
func (f Format) IsMachine() bool {
	return f == FormatJSON || f == FormatYAML
}

// AddOutputFlag adds -o/--output to cmd. The flag stores its value in p and
// rejects a value that is not in allowed. The default is the value of p, or
// the first allowed format when p is empty.
func AddOutputFlag(cmd *cobra.Command, p *Format, allowed ...Format) {
	if *p == "" {
		*p = allowed[0]
	}
	names := make([]string, len(allowed))
	for i, f := range allowed {
		names[i] = string(f)
	}
	cmd.Flags().VarP(&formatValue{format: p, allowed: allowed}, "output", "o",
		"Output format: "+strings.Join(names, ", "))
	// The flag was added on the line above, so this cannot fail.
	_ = cmd.RegisterFlagCompletionFunc("output", cobra.FixedCompletions(names, cobra.ShellCompDirectiveNoFileComp))
}

// formatValue is the pflag.Value of the output flag.
type formatValue struct {
	format  *Format
	allowed []Format
}

func (v *formatValue) String() string { return string(*v.format) }

func (v *formatValue) Type() string { return "format" }

func (v *formatValue) Set(s string) error {
	f := Format(strings.ToLower(s))
	if !slices.Contains(v.allowed, f) {
		names := make([]string, len(v.allowed))
		for i, a := range v.allowed {
			names[i] = string(a)
		}
		return fmt.Errorf("must be one of: %s", strings.Join(names, ", "))
	}
	*v.format = f
	return nil
}

// PrintDocument writes doc to the output stream as JSON or YAML.
func PrintDocument(io *IOStreams, format Format, doc apidoc.Document) error {
	switch format {
	case FormatJSON:
		return apidoc.EncodeJSON(io.Out, doc)
	case FormatYAML:
		return apidoc.EncodeYAML(io.Out, doc)
	default:
		return fmt.Errorf("cannot print the %s document as %q", doc.Kind(), format)
	}
}
