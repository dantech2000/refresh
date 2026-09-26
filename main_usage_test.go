package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// A usage error printed the command's whole help page on stdout, so
// `refresh status -o json --nope` broke the one-document contract. It is now
// one error, and stdout stays empty.
func TestUsageErrorKeepsStdoutEmpty(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"refresh", "status", "-o", "json", "--nope"}, "run 'refresh status --help' for usage"},
		{[]string{"refresh", "cluster", "describe", "x", "--bogus"}, "run 'refresh cluster describe --help' for usage"},
		{[]string{"refresh", "nodegroup", "update", "--wait-timeout", "banana"}, `invalid value "banana"`},
	} {
		var out, errOut bytes.Buffer
		err := run(context.Background(), tc.args, &out, &errOut)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want %q", tc.args, err, tc.want)
		}
		if out.Len() != 0 {
			t.Errorf("%v: stdout has %d bytes:\n%s", tc.args, out.Len(), out.String())
		}
		if strings.Contains(errOut.String(), "USAGE:") {
			t.Errorf("%v: the help page was printed:\n%s", tc.args, errOut.String())
		}
	}
}
