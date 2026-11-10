package health

import (
	"github.com/dantech2000/refresh/internal/exitcode"
	"github.com/dantech2000/refresh/internal/fleet"
)

// FleetExit is the exit code of "refresh status" for the clusters that it
// shows: 3 when a cluster is out of standard support, else 2 when one needs
// attention, else 0. An incomplete read exits 4 and wins over all three;
// the caller decides it first, from the failures of the run, which also
// name the regions that did not answer.
func FleetExit(clusters []fleet.Findings) int {
	code := exitcode.OK
	for _, f := range clusters {
		switch {
		case f.OutOfStandardSupport():
			return exitcode.Blocked
		case f.NeedsAttention():
			code = exitcode.NeedsAttention
		}
	}
	return code
}
