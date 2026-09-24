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
// the failures on stderr. showSecurity adds the security fields
// (--show-security or --detailed).
func OutputClusterDetailsTable(details *clustersvc.ClusterDetails, showSecurity bool) error {
	if ui.PlainOutput() {
		clusterDetailPlain(details, showSecurity).Render()
		return nil
	}
	th := render.Default(os.Stdout)
	for _, line := range clusterDetailLines(th, details, showSecurity) {
		fmt.Println(line)
	}
	return nil
}
