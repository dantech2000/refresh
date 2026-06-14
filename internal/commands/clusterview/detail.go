package clusterview

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fatih/color"

	"github.com/dantech2000/refresh/internal/render"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/ui"
)

// OutputClusterDetailsTable renders a single cluster's expanded details. The
// human path uses the render design system (sections, status tokens, a health
// card); `-o plain` keeps the uncolored key/value + tab-separated tables.
func OutputClusterDetailsTable(details *clustersvc.ClusterDetails, elapsed time.Duration) error {
	if ui.PlainOutput() {
		return outputClusterDetailsPlain(details, elapsed)
	}
	th := render.Default(os.Stdout)
	for _, line := range clusterDetailLines(th, details, elapsed) {
		fmt.Println(line)
	}
	return nil
}

// outputClusterDetailsPlain renders the uncolored key/value + tab-separated
// add-ons table for `-o plain`.
func outputClusterDetailsPlain(details *clustersvc.ClusterDetails, elapsed time.Duration) error {
	ui.Outf("Cluster Information: %s\n", color.CyanString(details.Name))
	ui.PrintElapsed(elapsed)

	tbl := ui.NewDynamicTable()
	tbl.Add("Status", formatStatus(details.Status)).
		Add("Version", details.Version).
		Add("Platform", details.PlatformVersion).
		Add("Endpoint", truncateEndpoint(details.Endpoint))

	if details.Health != nil {
		tbl.Add("Health", formatHealth(details.Health))
	}

	if len(details.Nodegroups) > 0 {
		// "nodes total" is desired capacity (always known); per-nodegroup
		// readiness is shown elsewhere only when measured. (REF-130)
		var totalNodes int32
		activeNGs := 0
		for _, ng := range details.Nodegroups {
			totalNodes += ng.DesiredSize
			if ng.Status == "ACTIVE" {
				activeNGs++
			}
		}
		tbl.Add("Nodegroups", fmt.Sprintf("%d active (%d nodes total)", activeNGs, totalNodes))
	}

	if details.Networking.VpcId != "" {
		vpc := details.Networking.VpcId
		if details.Networking.VpcCidr != "" {
			vpc += fmt.Sprintf(" (%s)", details.Networking.VpcCidr)
		}
		tbl.Add("VPC", vpc)
		if len(details.Networking.SubnetIds) > 0 {
			tbl.Add("Subnets", fmt.Sprintf("%d subnets", len(details.Networking.SubnetIds)))
		}
		if len(details.Networking.SecurityGroupIds) > 0 {
			tbl.Add("Security Groups", fmt.Sprintf("%d groups", len(details.Networking.SecurityGroupIds)))
		}
	}

	loggingStatus := "Disabled"
	if len(details.Security.LoggingEnabled) > 0 {
		loggingStatus = strings.Join(details.Security.LoggingEnabled, ", ") + " enabled"
	}
	tbl.Add("Logging", loggingStatus)

	encStatus := color.RedString("DISABLED")
	if details.Security.EncryptionEnabled {
		encStatus = color.GreenString("ENABLED") + " (at rest via KMS)"
	}
	tbl.Add("Encryption", encStatus)
	tbl.AddBool("Deletion Protection", details.Security.DeletionProtection)
	if details.CreatedAt.IsZero() {
		tbl.Add("Created", "unknown")
		tbl.Add("Age", "unknown")
	} else {
		tbl.Add("Created", details.CreatedAt.Format("2006-01-02 15:04:05 UTC"))
		tbl.Add("Age", formatAge(time.Since(details.CreatedAt)))
	}
	tbl.Render()

	if len(details.Addons) > 0 {
		ui.Outln("\nAdd-ons:")
		cols := []ui.Column{
			{Title: "NAME", Min: 4, Max: 24, Align: ui.AlignLeft},
			{Title: "VERSION", Min: 8, Align: ui.AlignLeft},
			{Title: "STATUS", Min: 10, Align: ui.AlignLeft},
			{Title: "HEALTH", Min: 8, Align: ui.AlignLeft},
		}
		addTbl := ui.NewPTable(cols, ui.CyanHeaders())
		for _, a := range details.Addons {
			h := a.Health
			if h == "" {
				h = "Unknown"
			}
			addTbl.AddRow(ui.TruncateANSI(a.Name, 24), a.Version, a.Status, formatAddonHealth(h))
		}
		addTbl.Render()
	}

	if len(details.HealthIssues) > 0 {
		ui.Outln("\nHealth Issues:")
		for _, iss := range details.HealthIssues {
			line := iss.Code
			if iss.Message != "" {
				line += ": " + iss.Message
			}
			if len(iss.ResourceIds) > 0 {
				line += " [" + strings.Join(iss.ResourceIds, ", ") + "]"
			}
			fmt.Printf("  - %s\n", line)
		}
	}
	return nil
}
