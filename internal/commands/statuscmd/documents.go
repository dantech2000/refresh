package statuscmd

import (
	"github.com/dantech2000/refresh/internal/apidoc"
	statussvc "github.com/dantech2000/refresh/internal/services/status"
)

// Documents returns a zero value of each document `refresh status` prints
// with -o json|yaml, for the JSON Schema generator.
func Documents() []apidoc.Document {
	return []apidoc.Document{statussvc.FleetStatus{}}
}
