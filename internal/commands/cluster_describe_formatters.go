package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fatih/color"
	"gopkg.in/yaml.v3"

	"github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/ui"
)

func outputClusterDetailsJSON(details *cluster.ClusterDetails) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(details)
}

func outputClusterDetailsYAML(details *cluster.ClusterDetails) error {
	enc := yaml.NewEncoder(os.Stdout)
	enc.SetIndent(2)
	defer func() { _ = enc.Close() }()
	return enc.Encode(details)
}

func outputClusterDetailsTable(details *cluster.ClusterDetails, elapsed time.Duration) error {
	ui.Outf("Cluster Information: %s\n", color.CyanString(details.Name))
	ui.Outf("Retrieved in %s\n\n", color.GreenString("%.1fs", elapsed.Seconds()))

	tbl := ui.NewDynamicTable()
	tbl.Add("Status", formatStatus(details.Status)).
		Add("Version", details.Version).
		Add("Platform", details.PlatformVersion).
		Add("Endpoint", truncateEndpoint(details.Endpoint))

	if details.Health != nil {
		tbl.Add("Health", formatHealth(details.Health))
	}

	if len(details.Nodegroups) > 0 {
		var totalNodes int32
		activeNGs := 0
		for _, ng := range details.Nodegroups {
			totalNodes += ng.ReadyNodes
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
	tbl.Add("Created", details.CreatedAt.Format("2006-01-02 15:04:05 UTC"))
	tbl.Add("Age", formatAge(time.Since(details.CreatedAt)))
	tbl.Render()

	if len(details.Addons) > 0 {
		ui.Outln("\nAdd-ons:")
		cols := []ui.Column{
			{Title: "NAME", Min: 4, Max: 24, Align: ui.AlignLeft},
			{Title: "VERSION", Min: 8, Align: ui.AlignLeft},
			{Title: "STATUS", Min: 10, Align: ui.AlignLeft},
			{Title: "HEALTH", Min: 8, Align: ui.AlignLeft},
		}
		addTbl := ui.NewPTable(cols, ui.WithPTableHeaderColor(func(s string) string { return color.CyanString(s) }))
		for _, a := range details.Addons {
			h := a.Health
			if h == "" {
				h = "Unknown"
			}
			addTbl.AddRow(truncateString(a.Name, 24), a.Version, a.Status, formatAddonHealth(h))
		}
		addTbl.Render()
	}
	return nil
}
