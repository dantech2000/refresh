package clusterview

import (
	"fmt"
	"os"
	"time"

	"github.com/dantech2000/refresh/internal/render"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/ui"
)

// OutputClusterDetailsTable renders a single cluster's expanded details. The
// human path uses the render design system (sections, status tokens, a health
// card); `-o plain` writes a FIELD/VALUE TSV (see clusterDetailPlain).
func OutputClusterDetailsTable(details *clustersvc.ClusterDetails, elapsed time.Duration) error {
	if ui.PlainOutput() {
		clusterDetailPlain(details).Render()
		return nil
	}
	th := render.Default(os.Stdout)
	for _, line := range clusterDetailLines(th, details, elapsed) {
		fmt.Println(line)
	}
	return nil
}
