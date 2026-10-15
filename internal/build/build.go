// Package build holds the version details of the running binary: the ones
// that the linker stamps into a release, or, for a binary built with
// go install or go build, the ones that Go records in its build info.
package build

import (
	"runtime/debug"
	"strings"
)

// Set at link time, for example:
//
//	go build -ldflags "-X github.com/dantech2000/refresh/internal/build.version=v1.2.3"
var (
	version   = "dev"
	commit    = ""
	buildDate = ""
)

// Info describes one build of refresh.
type Info struct {
	Version string // release version, the module version of go install, or "dev"
	Commit  string // short commit hash; empty when no build recorded one
	Date    string // build or commit time in RFC 3339; empty when none was recorded
}

// Get returns the details of the running binary. A release has them
// stamped at link time. Without the stamp, Go's build info supplies them:
// go install of a tag records the module version, and a build in a git
// checkout records the commit and its time.
func Get() Info {
	info := Info{Version: version, Commit: commit, Date: buildDate}
	if version != "dev" {
		return info
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		return fromBuildInfo(info, bi)
	}
	return info
}

// commitLen is the length of a short commit hash, as GoReleaser prints it.
const commitLen = 7

// fromBuildInfo fills what the linker did not stamp from Go's build info.
// The version drops its leading "v", as a release prints it.
func fromBuildInfo(info Info, bi *debug.BuildInfo) Info {
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		info.Version = strings.TrimPrefix(v, "v")
	}
	modified := false
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			if info.Commit == "" {
				info.Commit = s.Value[:min(commitLen, len(s.Value))]
			}
		case "vcs.time":
			if info.Date == "" {
				info.Date = s.Value
			}
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if modified && info.Commit != "" {
		info.Commit += "-dirty"
	}
	return info
}
