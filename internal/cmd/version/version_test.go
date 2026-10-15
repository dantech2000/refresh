package version

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/build"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
)

func TestFormat(t *testing.T) {
	tests := []struct {
		name string
		info build.Info
		want string
	}{
		{
			name: "release build",
			info: build.Info{Version: "v1.2.3", Commit: "abc1234", Date: "2026-01-02T03:04:05Z"},
			want: "refresh version: v1.2.3\ncommit: abc1234\nbuilt: 2026-01-02T03:04:05Z\n",
		},
		{
			name: "local build",
			info: build.Info{Version: "dev"},
			want: "refresh version: dev\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if diff := cmp.Diff(tt.want, Format(tt.info)); diff != "" {
				t.Errorf("Format() (-want +got):\n%s", diff)
			}
		})
	}
}

func TestUpdateHint(t *testing.T) {
	const hint = "A newer version (v1.3.0) is available (you have v1.2.3). Upgrade: brew upgrade refresh\n"
	tests := []struct {
		name      string
		outTTY    bool
		flag      bool   // --no-update-check
		env       string // REFRESH_NO_UPDATE_CHECK
		version   string
		output    cmdutil.Format
		latest    string
		latestErr error
		wantCalls int
		wantErr   string // stderr
	}{
		{name: "newer release on a terminal", outTTY: true, latest: "v1.3.0", wantCalls: 1, wantErr: hint},
		{name: "same release", outTTY: true, latest: "v1.2.3", wantCalls: 1},
		{name: "older release", outTTY: true, latest: "v1.0.0", wantCalls: 1},
		{name: "stdout is not a terminal", latest: "v1.3.0"},
		{name: "flag turns the check off", outTTY: true, flag: true, latest: "v1.3.0"},
		{name: "env turns the check off", outTTY: true, env: "1", latest: "v1.3.0"},
		{name: "env accepts any word", outTTY: true, env: "yes", latest: "v1.3.0"},
		{name: "env false keeps the check", outTTY: true, env: "false", latest: "v1.3.0", wantCalls: 1, wantErr: hint},
		{name: "env 0 keeps the check", outTTY: true, env: "0", latest: "v1.3.0", wantCalls: 1, wantErr: hint},
		{name: "env no keeps the check", outTTY: true, env: "No", latest: "v1.3.0", wantCalls: 1, wantErr: hint},
		{name: "failed check is silent", outTTY: true, latestErr: errors.New("offline"), wantCalls: 1},
		{name: "local build gets no hint", outTTY: true, version: "dev", latest: "v1.3.0", wantCalls: 1},
		{
			name:   "hint goes to stderr for a document",
			outTTY: true, output: cmdutil.FormatJSON, latest: "v1.3.0", wantCalls: 1, wantErr: hint,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(envNoUpdateCheck, tt.env)
			ios, _, out, errOut := cmdutiltest.NewIOStreams()
			ios.OutTTY = tt.outTTY
			calls := 0
			o := &options{
				ios:   ios,
				build: build.Info{Version: "v1.2.3"},
				latestTag: func(context.Context) (string, error) {
					calls++
					return tt.latest, tt.latestErr
				},
				output:        cmdutil.FormatTable,
				noUpdateCheck: tt.flag,
			}
			if tt.version != "" {
				o.build.Version = tt.version
			}
			if tt.output != "" {
				o.output = tt.output
			}

			o.complete()
			if err := o.run(context.Background()); err != nil {
				t.Fatalf("run() error = %v", err)
			}

			if calls != tt.wantCalls {
				t.Errorf("update checks = %d, want %d", calls, tt.wantCalls)
			}
			if diff := cmp.Diff(tt.wantErr, errOut.String()); diff != "" {
				t.Errorf("stderr (-want +got):\n%s", diff)
			}
			if out.Len() == 0 {
				t.Error("stdout is empty, want the version")
			}
		})
	}
}
