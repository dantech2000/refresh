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
    "github.com/fatih/color"
    "github.com/urfave/cli/v2"
    "gopkg.in/yaml.v3"

    awsinternal "github.com/dantech2000/refresh/internal/aws"
    appconfig "github.com/dantech2000/refresh/internal/config"
)

// DescribeAddonCommand describes a single EKS add-on with configuration
func DescribeAddonCommand() *cli.Command {
    return &cli.Command{
        Name:  "describe-addon",
        Usage: "Describe a specific EKS add-on",
        Flags: []cli.Flag{
            &cli.DurationFlag{Name: "timeout", Aliases: []string{"t"}, Usage: "Operation timeout", Value: appconfig.DefaultTimeout, EnvVars: []string{"REFRESH_TIMEOUT"}},
            &cli.StringFlag{Name: "cluster", Aliases: []string{"c"}, Usage: "EKS cluster name or pattern"},
            &cli.StringFlag{Name: "addon", Aliases: []string{"a"}, Usage: "Add-on name (e.g., vpc-cni)", Required: true},
            &cli.StringFlag{Name: "format", Aliases: []string{"o"}, Usage: "Output format (table, json, yaml)", Value: "table"},
        },
        Action: func(c *cli.Context) error { return runDescribeAddon(c) },
    }
}

type addonDetails struct {
    Name       string                 `json:"name"`
    Version    string                 `json:"version"`
    Status     string                 `json:"status"`
    Health     string                 `json:"health"`
    ARN        string                 `json:"arn"`
    CreatedAt  *time.Time             `json:"createdAt"`
    ModifiedAt *time.Time             `json:"modifiedAt"`
    Config     map[string]interface{} `json:"configuration"`
}

func runDescribeAddon(c *cli.Context) error {
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
    addonName := c.String("addon")
    eksClient := eks.NewFromConfig(cfg)

    d, err := eksClient.DescribeAddon(ctx, &eks.DescribeAddonInput{ClusterName: aws.String(clusterName), AddonName: aws.String(addonName)})
    if err != nil || d.Addon == nil {
        return awsinternal.FormatAWSError(err, "describing add-on")
    }

    health := mapAddonHealth(d.Addon.Status)
    details := addonDetails{
        Name:       aws.ToString(d.Addon.AddonName),
        Version:    aws.ToString(d.Addon.AddonVersion),
        Status:     string(d.Addon.Status),
        Health:     health,
        ARN:        aws.ToString(d.Addon.AddonArn),
        CreatedAt:  d.Addon.CreatedAt,
        ModifiedAt: d.Addon.ModifiedAt,
        Config:     map[string]interface{}{},
    }

    if d.Addon.ConfigurationValues != nil && *d.Addon.ConfigurationValues != "" {
        // Try to decode JSON or YAML-like string into a generic map for json/yaml outputs
        var cfgMap map[string]interface{}
        raw := *d.Addon.ConfigurationValues
        if err := yaml.Unmarshal([]byte(raw), &cfgMap); err == nil {
            details.Config = cfgMap
        } else {
            details.Config = map[string]interface{}{"raw": raw}
        }
    }

    switch strings.ToLower(c.String("format")) {
    case "json":
        enc := json.NewEncoder(os.Stdout)
        enc.SetIndent("", "  ")
        return enc.Encode(details)
    case "yaml":
        enc := yaml.NewEncoder(os.Stdout)
        enc.SetIndent(2)
        defer func() { _ = enc.Close() }()
        return enc.Encode(details)
    default:
        return outputAddonDetailsTable(clusterName, details)
    }
}

func outputAddonDetailsTable(cluster string, d addonDetails) error {
    fmt.Printf("Add-on Details: %s (%s)\n", color.CyanString(d.Name), color.WhiteString(cluster))
    fmt.Printf("Version: %s\n", d.Version)
    fmt.Printf("Status: %s\n", d.Status)
    if d.Health != "" { fmt.Printf("Health: %s\n", d.Health) }
    if d.ARN != "" { fmt.Printf("ARN: %s\n", d.ARN) }
    if d.CreatedAt != nil { fmt.Printf("Created: %s\n", d.CreatedAt.Format(time.RFC3339)) }
    if d.ModifiedAt != nil { fmt.Printf("Modified: %s\n", d.ModifiedAt.Format(time.RFC3339)) }
    if len(d.Config) > 0 {
        fmt.Println("\nConfiguration:")
        // Render keys sorted for stable output
        // Simple dump for now
        y, _ := yaml.Marshal(d.Config)
        fmt.Println(string(y))
    }
    return nil
}


