// Package runner provides shared CLI command primitives so that every
// command's run* function doesn't re-implement context+awsconfig+credential
// setup, the "no cluster specified" fallback, and json/yaml encoding.
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/fatih/color"
	"github.com/urfave/cli/v2"
	"gopkg.in/yaml.v3"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/awsconfig"
	clustercmd "github.com/dantech2000/refresh/internal/commands/cluster"
	"github.com/dantech2000/refresh/internal/commands/factory"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/ui"
)

// SetupAWS opens a context with the command's timeout, loads the AWS config,
// and checks credentials. Returns the context, a cancel func to be deferred,
// and the loaded aws.Config.
func SetupAWS(c *cli.Context) (context.Context, context.CancelFunc, aws.Config, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.Duration("timeout"))
	cfg, err := awsconfig.Load(ctx, c)
	if err != nil {
		cancel()
		color.Red("Failed to load AWS config: %v", err)
		return nil, func() {}, aws.Config{}, err
	}
	if err := awsinternal.CheckAWSCredentials(ctx, cfg); err != nil {
		cancel()
		return nil, func() {}, aws.Config{}, err
	}
	return ctx, cancel, cfg, nil
}

// RequestedCluster returns the cluster name requested by the user: first
// positional arg if present, otherwise --cluster.
func RequestedCluster(c *cli.Context) string {
	if first := c.Args().First(); strings.TrimSpace(first) != "" {
		return first
	}
	return c.String("cluster")
}

// ResolveClusterOrList resolves the requested cluster name. If no cluster was
// requested, it prints "No cluster specified. Available clusters:" plus the
// cluster table and returns listed=true so the caller can short-circuit.
func ResolveClusterOrList(ctx context.Context, cfg aws.Config, c *cli.Context) (clusterName string, listed bool, err error) {
	requested := RequestedCluster(c)
	if strings.TrimSpace(requested) == "" {
		ui.Outln("No cluster specified. Available clusters:")
		ui.Outln()
		start := time.Now()
		svc := factory.NewClusterService(cfg, false, nil)
		summaries, lerr := svc.List(ctx, clustersvc.ListOptions{})
		if lerr != nil {
			return "", true, lerr
		}
		_ = clustercmd.OutputClustersTable(summaries, time.Since(start), false, false)
		return "", true, nil
	}
	name, err := awsinternal.ClusterName(ctx, cfg, requested)
	if err != nil {
		return "", false, err
	}
	return name, false, nil
}

// SecondPositional returns the value of flagName, or the second non-flag
// positional argument if the flag is not set.
func SecondPositional(c *cli.Context, flagName string) string {
	if v := strings.TrimSpace(c.String(flagName)); v != "" {
		return v
	}
	var nonFlags []string
	for _, tok := range c.Args().Slice() {
		if strings.HasPrefix(tok, "-") {
			continue
		}
		nonFlags = append(nonFlags, tok)
	}
	if len(nonFlags) >= 2 {
		return nonFlags[1]
	}
	return ""
}

// EncodeStdout writes payload to stdout as JSON or YAML based on format. For
// any other format value, returns ErrUnknownFormat so the caller can fall
// through to its table renderer.
func EncodeStdout(format string, payload any) (handled bool, err error) {
	switch strings.ToLower(format) {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return true, enc.Encode(payload)
	case "yaml":
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return true, enc.Encode(payload)
	default:
		return false, nil
	}
}

// WithSpinner runs fn between starting and stopping a spinner for category.
// On error, the spinner is stopped without a success message.
func WithSpinner(category, successMsg string, fn func() error) error {
	spinner := ui.NewFunSpinnerForCategory(category)
	if err := spinner.Start(); err != nil {
		return fmt.Errorf("failed to start spinner: %w", err)
	}
	defer spinner.Stop()
	if err := fn(); err != nil {
		return err
	}
	spinner.Success(successMsg)
	return nil
}
