package cmdutil_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
)

func TestRequireYes(t *testing.T) {
	tests := []struct {
		name   string
		tty    bool
		format cmdutil.Format
		yes    bool
		dryRun bool
		want   string // a part of the error, or "" for none
	}{
		{name: "a terminal and a table", tty: true, format: cmdutil.FormatTable},
		{name: "no terminal", format: cmdutil.FormatTable, want: "no terminal to confirm the change; pass --yes"},
		{name: "a machine format on a terminal", tty: true, format: cmdutil.FormatJSON, want: "-o json prints one document and cannot prompt; pass --yes"},
		{name: "yes without a terminal", format: cmdutil.FormatYAML, yes: true},
		{name: "a dry run without a terminal", format: cmdutil.FormatJSON, dryRun: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ios, in, _, _ := cmdutiltest.NewIOStreams()
			ios.InTTY = tt.tty
			in.WriteString("y\n")
			err := cmdutil.RequireYes(ios, tt.format, tt.yes, tt.dryRun)
			switch {
			case tt.want == "" && err != nil:
				t.Errorf("error = %v, want none", err)
			case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
				t.Errorf("error = %v, want %q", err, tt.want)
			}
			if in.Len() != 2 {
				t.Error("RequireYes read from stdin")
			}
		})
	}
}

func TestConfirm(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"y\n", true}, {"Y\n", true}, {"yes\n", true}, {"YES\n", true},
		{"n\n", false}, {"\n", false}, {"", false}, {"maybe\n", false},
	}
	for _, tt := range tests {
		t.Run(strings.TrimSpace(tt.input), func(t *testing.T) {
			ios, in, _, errOut := cmdutiltest.NewIOStreams()
			in.WriteString(tt.input)
			got, err := cmdutil.Confirm(context.Background(), ios, "proceed despite 1 warning (Nodes: 5/6 ready)?")
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("Confirm(%q) = %v, want %v", tt.input, got, tt.want)
			}
			if errOut.String() != "proceed despite 1 warning (Nodes: 5/6 ready)? [y/N]: " {
				t.Errorf("stderr = %q", errOut.String())
			}
		})
	}
	t.Run("cancelled", func(t *testing.T) {
		ios, _, _, _ := cmdutiltest.NewIOStreams()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := cmdutil.Confirm(ctx, ios, "proceed?"); !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want the cancellation", err)
		}
	})
}
