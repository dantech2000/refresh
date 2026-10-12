// Package build holds the version details that the linker stamps into the
// binary.
package build

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
	Version string // release tag, or "dev" for a local build
	Commit  string // short commit hash; empty for a local build
	Date    string // build time in RFC 3339; empty for a local build
}

// Get returns the details of the running binary.
func Get() Info {
	return Info{Version: version, Commit: commit, Date: buildDate}
}
