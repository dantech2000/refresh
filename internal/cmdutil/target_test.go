package cmdutil_test

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/cmdutil"
)

func TestSuggest(t *testing.T) {
	names := []string{"prod", "prod-eu", "staging", "dev", "payments-prod", "data"}
	tests := []struct {
		name string
		want []string
	}{
		{"prd", []string{"prod", "prod-eu"}},
		{"PROD", []string{"payments-prod", "prod", "prod-eu"}},
		{"stagin", []string{"staging"}},
		{"dve", []string{"dev"}},
		{"prod-eu-1", []string{"prod", "prod-eu"}},
		{"zzzzzzzz", []string{}},
		{"", nil},
	}
	for _, tt := range tests {
		if diff := cmp.Diff(tt.want, cmdutil.Suggest(tt.name, names)); diff != "" {
			t.Errorf("Suggest(%q) (-want +got):\n%s", tt.name, diff)
		}
	}
	many := []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7"}
	if got := cmdutil.Suggest("a", many); len(got) != 5 {
		t.Errorf("Suggest returned %d names, want at most 5", len(got))
	}
}

func TestNotFoundError(t *testing.T) {
	err := cmdutil.NotFoundError("cluster", "prd", "us-east-1", []string{"prod", "dev"}, "refresh cluster list")
	want := "cluster \"prd\" not found in us-east-1\nDid you mean:\n  prod\nList the clusters with: refresh cluster list"
	if diff := cmp.Diff(want, err.Error()); diff != "" {
		t.Errorf("error (-want +got):\n%s", diff)
	}
}

// A command that changes a cluster never takes its cluster from the
// kubeconfig, so the help of its -c flag does not offer that, and the
// hint for a run with no cluster names only the ways that work.
func TestChangeClusterFlagHelp(t *testing.T) {
	read, change := &cobra.Command{Use: "list"}, &cobra.Command{Use: "update"}
	var name string
	cmdutil.AddClusterFlag(read, &name)
	cmdutil.AddChangeClusterFlag(change, &name)
	if usage := read.Flags().Lookup("cluster").Usage; !strings.Contains(usage, "then the current kubeconfig context") {
		t.Errorf("the help of a read command = %q, want the kubeconfig as the last default", usage)
	}
	usage := change.Flags().Lookup("cluster").Usage
	if !strings.Contains(usage, "never the kubeconfig") || strings.Contains(usage, "then the current kubeconfig context") {
		t.Errorf("the help of a changing command = %q, want it to say that the kubeconfig is never used", usage)
	}
}
