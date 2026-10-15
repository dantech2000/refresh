package cmdutil

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"
)

// AddSortFlags adds --sort and --desc to cmd. --sort stores its value in
// key and rejects a value that is not in keys. The default is the value of
// key, or the first of keys when key is empty.
func AddSortFlags(cmd *cobra.Command, key *string, desc *bool, keys ...string) {
	if *key == "" {
		*key = keys[0]
	}
	cmd.Flags().Var(&sortValue{key: key, keys: keys}, "sort", "Sort by: "+strings.Join(keys, ", "))
	cmd.Flags().BoolVar(desc, "desc", false, "Sort in descending order")
	// The flag was added two lines above, so this cannot fail.
	_ = cmd.RegisterFlagCompletionFunc("sort", cobra.FixedCompletions(keys, cobra.ShellCompDirectiveNoFileComp))
}

// sortValue is the pflag.Value of the sort flag.
type sortValue struct {
	key  *string
	keys []string
}

func (v *sortValue) String() string { return *v.key }

func (v *sortValue) Type() string { return "key" }

func (v *sortValue) Set(s string) error {
	i := slices.IndexFunc(v.keys, func(k string) bool { return strings.EqualFold(k, s) })
	if i < 0 {
		return fmt.Errorf("must be one of: %s", strings.Join(v.keys, ", "))
	}
	*v.key = v.keys[i]
	return nil
}
