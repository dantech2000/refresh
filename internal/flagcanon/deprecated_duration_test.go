package flagcanon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/urfave/cli/v3"
)

// A deprecated alias and its replacement set together is an error: before,
// the alias silently won over an explicit --wait-timeout.
func TestDeprecatedDuration_RejectsBothFlags(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"alias alone", []string{"--timeout", "5m"}, ""},
		{"replacement alone", []string{"--wait-timeout", "30m"}, ""},
		{"both", []string{"--timeout", "5m", "--wait-timeout", "30m"}, "--timeout on 'update' is a deprecated alias of --wait-timeout; pass only --wait-timeout"},
		{"both, replacement first", []string{"--wait-timeout", "30m", "-t", "5m"}, "pass only --wait-timeout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ran := false
			root := &cli.Command{
				Name: "refresh",
				Commands: []*cli.Command{{
					Name: "update",
					Flags: []cli.Flag{
						&cli.DurationFlag{Name: "wait-timeout", Value: time.Minute},
						DeprecatedDuration("timeout", "wait-timeout", "t"),
					},
					Action: func(context.Context, *cli.Command) error { ran = true; return nil },
				}},
			}
			err := root.Run(context.Background(), append([]string{"refresh", "update"}, tc.args...))
			if tc.wantErr == "" {
				if err != nil || !ran {
					t.Fatalf("err = %v, ran = %v; want success", err, ran)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if ran {
				t.Error("the action ran despite the conflicting flags")
			}
		})
	}
}
