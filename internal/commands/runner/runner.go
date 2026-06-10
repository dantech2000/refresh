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
	"github.com/dantech2000/refresh/internal/commands/clusterview"
	"github.com/dantech2000/refresh/internal/commands/factory"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/ui"
)

// credentialCheck is the credential validation strategy used by the setup
// helpers below.
type credentialCheck func(ctx context.Context, cfg aws.Config) error

// checkCredentialsLenient wraps awsinternal.CheckAWSCredentials.
func checkCredentialsLenient(ctx context.Context, cfg aws.Config) error {
	return awsinternal.CheckAWSCredentials(ctx, cfg)
}

// checkCredentialsStrict wraps awsinternal.ValidateAWSCredentials and prints
// the help message on failure.
func checkCredentialsStrict(ctx context.Context, cfg aws.Config) error {
	if err := awsinternal.ValidateAWSCredentials(ctx, cfg); err != nil {
		color.Red("%v", err)
		ui.Outln()
		awsinternal.PrintCredentialHelp()
		return fmt.Errorf("AWS credential validation failed")
	}
	return nil
}

// setupAWS is the shared body of SetupAWS/SetupAWSWithTimeout/SetupAWSStrict.
// On error the internal context is canceled and the returned cancel is nil.
func setupAWS(c *cli.Context, defaultTimeout time.Duration, check credentialCheck) (context.Context, context.CancelFunc, aws.Config, error) {
	timeout := c.Duration("timeout")
	if timeout == 0 {
		timeout = defaultTimeout
	}
	var (
		ctx    context.Context
		cancel context.CancelFunc
	)
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), timeout)
	} else {
		ctx, cancel = context.WithCancel(context.Background())
	}

	cfg, err := awsconfig.Load(ctx, c)
	if err != nil {
		cancel()
		color.Red("Failed to load AWS config: %v", err)
		return nil, nil, aws.Config{}, err
	}
	if err := check(ctx, cfg); err != nil {
		cancel()
		return nil, nil, aws.Config{}, err
	}
	return ctx, cancel, cfg, nil
}

// SetupAWS opens a context with the command's timeout, loads the AWS config,
// and checks credentials. On error, the returned cancel is nil and the
// internal context has already been cancelled.
func SetupAWS(c *cli.Context) (context.Context, context.CancelFunc, aws.Config, error) {
	return setupAWS(c, 0, checkCredentialsLenient)
}

// SetupAWSWithTimeout is like SetupAWS but falls back to defaultTimeout
// when c.Duration("timeout") is zero.
func SetupAWSWithTimeout(c *cli.Context, defaultTimeout time.Duration) (context.Context, context.CancelFunc, aws.Config, error) {
	return setupAWS(c, defaultTimeout, checkCredentialsLenient)
}

// SetupAWSStrict is like SetupAWS but uses ValidateAWSCredentials and prints
// the credential help message on failure (used by destructive commands).
func SetupAWSStrict(c *cli.Context) (context.Context, context.CancelFunc, aws.Config, error) {
	return setupAWS(c, 0, checkCredentialsStrict)
}

// ParseFilters parses repeated key=value --filter flag values into a map.
// Tokens without "=" are ignored.
func ParseFilters(filters []string) map[string]string {
	out := make(map[string]string)
	for _, f := range filters {
		if parts := strings.SplitN(f, "=", 2); len(parts) == 2 {
			out[parts[0]] = parts[1]
		}
	}
	return out
}

// RequestedCluster returns the cluster name requested by the user: --cluster
// when explicitly set (so positionals can fill later slots), otherwise the
// first positional arg.
func RequestedCluster(c *cli.Context) string {
	nonFlags := nonFlagArgs(c) // also applies trailing flags to the context
	if v := flagValueIfSet(c, "cluster"); v != "" {
		return v
	}
	if len(nonFlags) > 0 && strings.TrimSpace(nonFlags[0]) != "" {
		return nonFlags[0]
	}
	return strings.TrimSpace(c.String("cluster"))
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
		_ = clusterview.OutputClustersTable(summaries, time.Since(start), false, false)
		return "", true, nil
	}
	name, err := awsinternal.ClusterName(ctx, cfg, requested)
	if err != nil {
		return "", false, err
	}
	return name, false, nil
}

// PositionalAt returns the value of flagName, or the non-flag positional
// argument at index (0-indexed) if the flag is not set. Does NOT account for
// prior flags absorbing positional slots — use PositionalSlot for that.
func PositionalAt(c *cli.Context, flagName string, index int) string {
	nonFlags := nonFlagArgs(c) // also applies trailing flags to the context
	if v := flagValueIfSet(c, flagName); v != "" {
		return v
	}
	if index < len(nonFlags) {
		return nonFlags[index]
	}
	return flagDefault(c, flagName)
}

// flagValueIfSet returns the trimmed value of flagName only when it was
// explicitly provided (flag or env var). Flags that merely carry a default
// value return "" so a positional argument can still fill the slot.
func flagValueIfSet(c *cli.Context, flagName string) string {
	if flagName == "" || !c.IsSet(flagName) {
		return ""
	}
	return strings.TrimSpace(c.String(flagName))
}

// flagDefault returns the flag's default value (empty for most flags). Used
// as the last resort after explicit flags and positionals.
func flagDefault(c *cli.Context, flagName string) string {
	if flagName == "" {
		return ""
	}
	return strings.TrimSpace(c.String(flagName))
}

// PositionalSlot returns the value of flagName, or — when flagName is unset —
// the positional argument that fills its slot, accounting for prior slots
// that may have been satisfied by flags.
//
// priorFlags lists, in order, the flag name for each prior positional slot.
// An entry may be "" to mean "this prior slot has no flag and is always
// positional". For each prior flag name that IS set on the context, this
// helper subtracts 1 from the expected positional index, so flags and
// positionals can be mixed freely.
//
// Example: a command with slot order (cluster, addon, version) where the
// cluster has --cluster, the addon has --addon, and the version has --version:
//
//	cluster := PositionalSlot(c, "cluster")                       // slot 0
//	addon   := PositionalSlot(c, "addon", "cluster")              // slot 1
//	version := PositionalSlot(c, "version", "cluster", "addon")   // slot 2
//
// Invocation `--addon=foo my-cluster v1.2.3` yields cluster="my-cluster",
// addon="foo" (from flag), version="v1.2.3" — the version's positional index
// is shifted from 2 down to 1 because --addon consumed a slot.
func PositionalSlot(c *cli.Context, flagName string, priorFlags ...string) string {
	nonFlags := nonFlagArgs(c) // also applies trailing flags to the context
	if v := flagValueIfSet(c, flagName); v != "" {
		return v
	}
	consumedByFlags := 0
	for _, f := range priorFlags {
		if flagValueIfSet(c, f) != "" {
			consumedByFlags++
		}
	}
	idx := len(priorFlags) - consumedByFlags
	if idx < len(nonFlags) {
		return nonFlags[idx]
	}
	return flagDefault(c, flagName)
}

// ApplyTrailingFlags re-parses flag tokens that appear after the first
// positional argument (urfave/cli v2 stops flag parsing there and would
// otherwise silently ignore them) and applies recognized flags to the
// context. Commands that read flags before any positional helper runs can
// call this explicitly; the positional helpers invoke it implicitly.
func ApplyTrailingFlags(c *cli.Context) {
	_ = nonFlagArgs(c)
}

// nonFlagArgs returns the true positional arguments from c.Args(). Flag
// tokens that appear after the first positional are applied to the context
// via c.Set (so `refresh nodegroup update-ami my-cluster --force` works), and
// value-taking flags consume their value token so it is never mistaken for a
// positional (`describe my-cluster --timeframe 1h` must not read "1h" as a
// nodegroup name).
func nonFlagArgs(c *cli.Context) []string {
	type flagInfo struct {
		canonical  string
		takesValue bool
	}
	flagsByName := make(map[string]flagInfo)
	if c.Command != nil {
		for _, f := range c.Command.Flags {
			names := f.Names()
			if len(names) == 0 {
				continue
			}
			_, isBool := f.(*cli.BoolFlag)
			info := flagInfo{canonical: names[0], takesValue: !isBool}
			for _, n := range names {
				flagsByName[n] = info
			}
		}
	}

	args := c.Args().Slice()
	out := make([]string, 0, len(args))
	terminated := false // saw "--": everything after is positional
	for i := 0; i < len(args); i++ {
		tok := args[i]
		if terminated || !strings.HasPrefix(tok, "-") || tok == "-" {
			out = append(out, tok)
			continue
		}
		if tok == "--" {
			terminated = true
			continue
		}

		name := strings.TrimLeft(tok, "-")
		value := ""
		hasInline := false
		if eq := strings.Index(name, "="); eq >= 0 {
			value = name[eq+1:]
			name = name[:eq]
			hasInline = true
		}

		info, known := flagsByName[name]
		if !known {
			// Unknown flag-like token: drop it without guessing whether the
			// next token is its value.
			continue
		}
		switch {
		case !info.takesValue:
			if !hasInline {
				value = "true"
			}
			_ = c.Set(info.canonical, value)
		case hasInline:
			_ = c.Set(info.canonical, value)
		case i+1 < len(args):
			_ = c.Set(info.canonical, args[i+1])
			i++ // consume the value token
		}
	}
	return out
}

// EncodeStdout writes payload to stdout as JSON or YAML based on format. For
// any other format value it returns handled=false so the caller can fall
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
