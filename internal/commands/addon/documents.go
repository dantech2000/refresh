package addon

import (
	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/services/addons"
)

// Documents returns a zero value of each document the addon commands print
// with -o json|yaml, for the JSON Schema generator.
func Documents() []apidoc.Document {
	return []apidoc.Document{
		addons.AddonList{},
		addons.AddonDetails{},
		addonUpdateDocument{},
		addonUpdateAllDocument{},
	}
}
