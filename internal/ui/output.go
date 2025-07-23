package ui

import (
	"fmt"

	"github.com/fatih/color"

	"github.com/dantech2000/refresh/internal/types"
)

func PrintNodegroupsTree(clusterName string, nodegroups []types.NodegroupInfo) {
	// Print cluster name as root
	fmt.Printf("%s\n", color.CyanString(clusterName))

	for i, ng := range nodegroups {
		isLast := i == len(nodegroups)-1
		var prefix, itemPrefix string

		if isLast {
			prefix = "└── "
			itemPrefix = "    "
		} else {
			prefix = "├── "
			itemPrefix = "│   "
		}

		// Print nodegroup name
		fmt.Printf("%s%s\n", prefix, color.YellowString(ng.Name))

		// Print nodegroup details
		fmt.Printf("%s├── Status: %s\n", itemPrefix, ng.Status)
		fmt.Printf("%s├── Instance Type: %s\n", itemPrefix, color.BlueString(ng.InstanceType))
		fmt.Printf("%s├── Desired: %s\n", itemPrefix, color.GreenString(fmt.Sprintf("%d", ng.Desired)))

		if ng.CurrentAmi != "" {
			fmt.Printf("%s├── Current AMI: %s\n", itemPrefix, color.WhiteString(ng.CurrentAmi))
		} else {
			fmt.Printf("%s├── Current AMI: %s\n", itemPrefix, color.RedString("Unknown"))
		}

		fmt.Printf("%s└── AMI Status: %s\n", itemPrefix, ng.AmiStatus)

		// Add spacing between nodegroups except for the last one
		if !isLast {
			fmt.Println()
		}
	}
}
