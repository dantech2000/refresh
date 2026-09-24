package clusterview

import (
	"fmt"
	"os"

	"github.com/dantech2000/refresh/internal/render"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/ui"
)

// OutputClusterDetailsTable renders a single cluster's expanded details. The
// human path uses the render design system (sections, status tokens, a health
// card, and the INCOMPLETE DATA section for details.Failures); `-o plain`
// writes a FIELD/VALUE TSV (see clusterDetailPlain), and the caller reports
// the failures on stderr.
func OutputClusterDetailsTable(details *clustersvc.ClusterDetails) error {
	if ui.PlainOutput() {
		clusterDetailPlain(details).Render()
		return nil
	}
	th := render.Default(os.Stdout)
	for _, line := range clusterDetailLines(th, details) {
		fmt.Println(line)
	}
	return nil
}
