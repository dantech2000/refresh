package clusterview

import (
	"fmt"
	"os"
	"strings"

	"github.com/dantech2000/refresh/internal/render"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/ui"
)

const insightTimeLayout = "2006-01-02 15:04"

// OutputUpgradeCheck renders the upgrade-check report. The human path uses the
// render design system (a readiness verdict + tokenized insights + skew +
// failures). `-o plain` writes the insights as TSV on stdout and the rest of
// the report (verdict, support, control plane, version skew) as text on
// stderr; the caller reports the failures on stderr.
func OutputUpgradeCheck(report *clustersvc.UpgradeReport) error {
	if ui.PlainOutput() {
		writeUpgradeCheckInfo(ui.Stderr, report)
		upgradeCheckPlain(report).Render()
		return nil
	}
	th := render.Default(os.Stdout)
	for _, line := range upgradeCheckLines(th, report) {
		fmt.Println(line)
	}
	return nil
}

// OutputInsightDetail renders a single insight (the DescribeInsight detail
// view). The human path uses the render design system (header + sections);
// `-o plain` writes a FIELD/VALUE TSV (see insightDetailPlain).
func OutputInsightDetail(detail *clustersvc.InsightDetail) error {
	if !ui.PlainOutput() {
		th := render.Default(os.Stdout)
		for _, line := range insightDetailLines(th, detail) {
			fmt.Println(line)
		}
		return nil
	}

	insightDetailPlain(detail).Render()
	return nil
}

func valueOrDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// oneLine collapses Markdown body text to a single line for terminal display.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
