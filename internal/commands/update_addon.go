package commands

import (
    "context"
    "fmt"
    "strings"

    "github.com/aws/aws-sdk-go-v2/aws"
    "github.com/aws/aws-sdk-go-v2/config"
    "github.com/aws/aws-sdk-go-v2/service/eks"
    "github.com/fatih/color"
    "github.com/urfave/cli/v2"

    awsinternal "github.com/dantech2000/refresh/internal/aws"
    appconfig "github.com/dantech2000/refresh/internal/config"
)

// UpdateAddonCommand updates an EKS add-on to a target version (or latest)
func UpdateAddonCommand() *cli.Command {
    return &cli.Command{
        Name:  "update-addon",
        Usage: "Update an EKS add-on (optionally to latest)",
        Flags: []cli.Flag{
            &cli.DurationFlag{Name: "timeout", Aliases: []string{"t"}, Usage: "Operation timeout", Value: appconfig.DefaultTimeout, EnvVars: []string{"REFRESH_TIMEOUT"}},
            &cli.StringFlag{Name: "cluster", Aliases: []string{"c"}, Usage: "EKS cluster name or pattern"},
            &cli.StringFlag{Name: "addon", Aliases: []string{"a"}, Usage: "Add-on name (e.g., vpc-cni)", Required: true},
            &cli.StringFlag{Name: "version", Usage: "Target version or 'latest'", Value: "latest"},
            &cli.BoolFlag{Name: "health-check", Aliases: []string{"H"}, Usage: "Placeholder for future pre/post health validation"},
            &cli.BoolFlag{Name: "dry-run", Aliases: []string{"d"}, Usage: "Preview without applying changes"},
        },
        Action: func(c *cli.Context) error { return runUpdateAddon(c) },
    }
}

func runUpdateAddon(c *cli.Context) error {
    ctx, cancel := context.WithTimeout(context.Background(), c.Duration("timeout"))
    defer cancel()

    cfg, err := config.LoadDefaultConfig(ctx)
    if err != nil { color.Red("Failed to load AWS config: %v", err); return err }
    if err := awsinternal.ValidateAWSCredentials(ctx, cfg); err != nil {
        color.Red("%v", err); fmt.Println(); awsinternal.PrintCredentialHelp(); return fmt.Errorf("AWS credential validation failed")
    }
    clusterName, err := awsinternal.ClusterName(ctx, cfg, c.String("cluster"))
    if err != nil { return err }
    addonName := c.String("addon")
    version := c.String("version")
    dryRun := c.Bool("dry-run")

    eksClient := eks.NewFromConfig(cfg)

    // Resolve latest version if requested
    targetVersion := version
    if strings.EqualFold(version, "latest") {
        avail, err := eksClient.DescribeAddonVersions(ctx, &eks.DescribeAddonVersionsInput{AddonName: aws.String(addonName)})
        if err != nil || len(avail.Addons) == 0 || len(avail.Addons[0].AddonVersions) == 0 {
            return awsinternal.FormatAWSError(err, "resolving latest add-on version")
        }
        // Choose the first as latest for now; SDK returns sorted versions
        targetVersion = aws.ToString(avail.Addons[0].AddonVersions[0].AddonVersion)
    }

    if dryRun {
        color.Cyan("DRY RUN: Would update add-on %s to version %s on cluster %s", addonName, targetVersion, clusterName)
        return nil
    }

    out, err := eksClient.UpdateAddon(ctx, &eks.UpdateAddonInput{
        ClusterName: aws.String(clusterName),
        AddonName:   aws.String(addonName),
        AddonVersion: aws.String(targetVersion),
    })
    if err != nil {
        return awsinternal.FormatAWSError(err, "updating add-on")
    }

    color.Green("Update started for add-on %s (ID: %s)", addonName, aws.ToString(out.Update.Id))
    color.White("Use AWS Console or 'refresh describe-addon -c %s -a %s' to check status.", clusterName, addonName)
    return nil
}


