// Package documents lists every document refresh prints with -o json|yaml,
// for the JSON Schema generator and the tests that check the documents
// against their schemas.
package documents

import (
	"cmp"
	"slices"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/commands/addon"
	"github.com/dantech2000/refresh/internal/commands/cluster"
	"github.com/dantech2000/refresh/internal/commands/nodegroup"
	"github.com/dantech2000/refresh/internal/commands/statuscmd"
)

// All returns a zero value of every document type, in the order of
// apidoc.Kinds.
func All() []apidoc.Document {
	all := slices.Concat(statuscmd.Documents(), cluster.Documents(), nodegroup.Documents(), addon.Documents())
	kinds := apidoc.Kinds()
	slices.SortStableFunc(all, func(a, b apidoc.Document) int {
		return cmp.Compare(slices.Index(kinds, a.DocumentKind()), slices.Index(kinds, b.DocumentKind()))
	})
	return all
}
