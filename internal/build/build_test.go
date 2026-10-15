package build

import (
	"runtime/debug"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestFromBuildInfo(t *testing.T) {
	unstamped := Info{Version: "dev"}
	tests := []struct {
		name string
		info Info
		bi   debug.BuildInfo
		want Info
	}{
		{
			name: "go install of a tag",
			info: unstamped,
			bi:   debug.BuildInfo{Main: debug.Module{Version: "v0.17.0"}},
			want: Info{Version: "0.17.0"},
		},
		{
			name: "go install of a commit after a tag",
			info: unstamped,
			bi:   debug.BuildInfo{Main: debug.Module{Version: "v0.17.1-0.20261015061207-d4b37253c0de"}},
			want: Info{Version: "0.17.1-0.20261015061207-d4b37253c0de"},
		},
		{
			name: "go build in a clean checkout",
			info: unstamped,
			bi: debug.BuildInfo{
				Main: debug.Module{Version: "(devel)"},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "d4b37253c0de8c4a2b0f3e5e8e1c6f0a9b8d7c6e"},
					{Key: "vcs.time", Value: "2026-10-15T06:12:07Z"},
					{Key: "vcs.modified", Value: "false"},
				},
			},
			want: Info{Version: "dev", Commit: "d4b3725", Date: "2026-10-15T06:12:07Z"},
		},
		{
			name: "go build with local changes",
			info: unstamped,
			bi: debug.BuildInfo{
				Main:     debug.Module{Version: "v0.17.1-0.20261015061207-d4b37253c0de+dirty"},
				Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "d4b3725"}, {Key: "vcs.modified", Value: "true"}},
			},
			want: Info{Version: "0.17.1-0.20261015061207-d4b37253c0de+dirty", Commit: "d4b3725-dirty"},
		},
		{
			name: "no module version and no VCS",
			info: unstamped,
			bi:   debug.BuildInfo{Main: debug.Module{Version: "(devel)"}},
			want: unstamped,
		},
		{
			name: "a stamped commit and date are kept",
			info: Info{Version: "dev", Commit: "abc1234", Date: "2026-01-02T03:04:05Z"},
			bi: debug.BuildInfo{
				Main:     debug.Module{Version: "v0.17.0"},
				Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "ffffffffff"}, {Key: "vcs.time", Value: "2020-01-01T00:00:00Z"}},
			},
			want: Info{Version: "0.17.0", Commit: "abc1234", Date: "2026-01-02T03:04:05Z"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if diff := cmp.Diff(tt.want, fromBuildInfo(tt.info, &tt.bi)); diff != "" {
				t.Errorf("fromBuildInfo (-want +got):\n%s", diff)
			}
		})
	}
}
