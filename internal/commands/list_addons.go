package commands

import (
    "context"
    "encoding/json"
    "fmt"
    "os"
    "strings"
    "time"

    "github.com/aws/aws-sdk-go-v2/aws"
    "github.com/aws/aws-sdk-go-v2/config"
    "github.com/aws/aws-sdk-go-v2/service/eks"
    ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
    "github.com/fatih/color"
    "github.com/urfave/cli/v2"
    "github.com/yarlson/pin"
    "gopkg.in/yaml.v3"

    awsinternal "github.com/dantech2000/refresh/internal/aws"
    appconfig "github.com/dantech2000/refresh/internal/config"
)

type addonRow struct {
    Name    string `json:"name"`
    Version string `json:"version"`
    Status  string `json:"status"`
    Health  string `json:"health"`
}

// ListAddonsCommand lists EKS add-ons for a cluster
func ListAddonsCommand() *cli.Command {
    return &cli.Command{
        Name:  "list-addons",
        Usage: "List EKS add-ons in a cluster",
        Flags: []cli.Flag{
            &cli.DurationFlag{Name: "timeout", Aliases: []string{"t"}, Usage: "Operation timeout", Value: appconfig.DefaultTimeout, EnvVars: []string{"REFRESH_TIMEOUT"}},
            &cli.StringFlag{Name: "cluster", Aliases: []string{"c"}, Usage: "EKS cluster name or pattern"},
            &cli.BoolFlag{Name: "show-health", Aliases: []string{"H"}, Usage: "Include health mapping in table output"},
            &cli.StringFlag{Name: "format", Aliases: []string{"o"}, Usage: "Output format (table, json, yaml)", Value: "table"},
        },
        Action: func(c *cli.Context) error { return runListAddons(c) },
    }
}

func runListAddons(c *cli.Context) error {
    ctx, cancel := context.WithTimeout(context.Background(), c.Duration("timeout"))
    defer cancel()

    cfg, err := config.LoadDefaultConfig(ctx)
    if err != nil {
        color.Red("Failed to load AWS config: %v", err)
        return err
    }
    if err := awsinternal.ValidateAWSCredentials(ctx, cfg); err != nil {
        color.Red("%v", err)
        fmt.Println()
        awsinternal.PrintCredentialHelp()
        return fmt.Errorf("AWS credential validation failed")
    }
    clusterName, err := awsinternal.ClusterName(ctx, cfg, c.String("cluster"))
    if err != nil {
        return err
    }
    eksClient := eks.NewFromConfig(cfg)

    spinner := pin.New("Gathering add-on information...",
        pin.WithSpinnerColor(pin.ColorCyan),
        pin.WithTextColor(pin.ColorYellow),
    )
    cancelSpin := spinner.Start(ctx)
    defer cancelSpin()

    start := time.Now()
    rows, err := fetchAddons(ctx, eksClient, clusterName, c.Bool("show-health"))
    spinner.Stop("Add-on information gathered!")
    if err != nil {
        return err
    }

    switch strings.ToLower(c.String("format")) {
    case "json":
        enc := json.NewEncoder(os.Stdout)
        enc.SetIndent("", "  ")
        return enc.Encode(map[string]any{"cluster": clusterName, "addons": rows, "count": len(rows)})
    case "yaml":
        enc := yaml.NewEncoder(os.Stdout)
        enc.SetIndent(2)
        defer func() { _ = enc.Close() }()
        return enc.Encode(map[string]any{"cluster": clusterName, "addons": rows, "count": len(rows)})
    default:
        return outputAddonsTable(clusterName, rows, time.Since(start))
    }
}

func fetchAddons(ctx context.Context, eksClient *eks.Client, clusterName string, withHealth bool) ([]addonRow, error) {
    var addonNames []string
    var nextToken *string
    for {
        out, err := eksClient.ListAddons(ctx, &eks.ListAddonsInput{ClusterName: aws.String(clusterName), NextToken: nextToken})
        if err != nil {
            return nil, awsinternal.FormatAWSError(err, "listing add-ons")
        }
        addonNames = append(addonNames, out.Addons...)
        if out.NextToken == nil || (out.NextToken != nil && aws.ToString(out.NextToken) == "") {
            break
        }
        nextToken = out.NextToken
    }
    rows := make([]addonRow, 0, len(addonNames))
    for _, name := range addonNames {
        d, err := eksClient.DescribeAddon(ctx, &eks.DescribeAddonInput{ClusterName: aws.String(clusterName), AddonName: aws.String(name)})
        if err != nil || d.Addon == nil {
            rows = append(rows, addonRow{Name: name, Version: "", Status: "UNKNOWN", Health: "Unknown"})
            continue
        }
        health := ""
        if withHealth {
            health = mapAddonHealth(d.Addon.Status)
        }
        rows = append(rows, addonRow{Name: aws.ToString(d.Addon.AddonName), Version: aws.ToString(d.Addon.AddonVersion), Status: string(d.Addon.Status), Health: health})
    }
    return rows, nil
}

func mapAddonHealth(s ekstypes.AddonStatus) string {
    switch s {
    case ekstypes.AddonStatusActive:
        return color.GreenString("PASS")
    case ekstypes.AddonStatusDegraded:
        return color.RedString("FAIL")
    case ekstypes.AddonStatusCreateFailed, ekstypes.AddonStatusDeleteFailed:
        return color.RedString("FAIL")
    case ekstypes.AddonStatusCreating, ekstypes.AddonStatusDeleting, ekstypes.AddonStatusUpdating:
        return color.CyanString("[IN PROGRESS]")
    default:
        return color.WhiteString("UNKNOWN")
    }
}

func outputAddonsTable(cluster string, rows []addonRow, elapsed time.Duration) error {
    fmt.Printf("Add-ons for cluster: %s\n", color.CyanString(cluster))
    fmt.Printf("Retrieved in %s\n\n", color.GreenString("%.1fs", elapsed.Seconds()))

    if len(rows) == 0 {
        color.Yellow("No add-ons found")
        return nil
    }

    // Determine widths
    nameW, verW, statusW, healthW := len("NAME"), len("VERSION"), len("STATUS"), len("HEALTH")
    for _, r := range rows {
        if l := len(r.Name); l > nameW { nameW = l }
        if l := len(r.Version); l > verW { verW = l }
        if l := len(r.Status); l > statusW { statusW = l }
        if l := len(stripAnsiCodes(r.Health)); l > healthW { healthW = l }
    }
    if nameW > 24 { nameW = 24 }
    if verW < 8 { verW = 8 }
    if statusW < 10 { statusW = 10 }
    if healthW < 8 { healthW = 8 }

    draw := func(l, m, r string) {
        fmt.Print(l)
        fmt.Print(strings.Repeat("─", nameW+2))
        fmt.Print(m)
        fmt.Print(strings.Repeat("─", verW+2))
        fmt.Print(m)
        fmt.Print(strings.Repeat("─", statusW+2))
        fmt.Print(m)
        fmt.Print(strings.Repeat("─", healthW+2))
        fmt.Println(r)
    }

    draw("┌", "┬", "┐")
    hName := padColoredString(color.CyanString("NAME"), nameW)
    hVer := padColoredString(color.CyanString("VERSION"), verW)
    hStatus := padColoredString(color.CyanString("STATUS"), statusW)
    hHealth := padColoredString(color.CyanString("HEALTH"), healthW)
    fmt.Printf("│ %s │ %s │ %s │ %s │\n", hName, hVer, hStatus, hHealth)
    draw("├", "┼", "┤")
    for _, r := range rows {
        fmt.Printf("│ %-*s │ %-*s │ %-*s │ %-*s │\n",
            nameW, truncateString(r.Name, nameW),
            verW, r.Version,
            statusW, r.Status,
            healthW, r.Health,
        )
    }
    draw("└", "┴", "┘")
    return nil
}


