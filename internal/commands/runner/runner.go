// Package runner provides shared CLI command primitives so that every
// command's run* function doesn't re-implement context+awsconfig+credential
// setup, the "no cluster specified" fallback, and json/yaml encoding.
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/fatih/color"
	"github.com/mattn/go-isatty"
	"github.com/pterm/pterm"
	"github.com/urfave/cli/v3"
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
func setupAWS(ctx context.Context, cmd *cli.Command, defaultTimeout time.Duration, check credentialCheck) (context.Context, context.CancelFunc, aws.Config, error) {
	timeout := cmd.Duration("timeout")
	if timeout == 0 {
		timeout = defaultTimeout
	}
	// Derive from the action's context (cancelled on Ctrl+C / SIGTERM by main)
	// so signal handling propagates to in-flight AWS calls. ctx is nil only
	// for hand-constructed invocations in tests.
	if ctx == nil {
		ctx = context.Background()
	}
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}

	cfg, err := awsconfig.Load(ctx, cmd)
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
func SetupAWS(ctx context.Context, cmd *cli.Command) (context.Context, context.CancelFunc, aws.Config, error) {
	return setupAWS(ctx, cmd, 0, checkCredentialsLenient)
}

// SetupAWSWithTimeout is like SetupAWS but falls back to defaultTimeout
// when cmd.Duration("timeout") is zero.
func SetupAWSWithTimeout(ctx context.Context, cmd *cli.Command, defaultTimeout time.Duration) (context.Context, context.CancelFunc, aws.Config, error) {
	return setupAWS(ctx, cmd, defaultTimeout, checkCredentialsLenient)
}

// SetupAWSStrict is like SetupAWS but uses ValidateAWSCredentials and prints
// the credential help message on failure (used by destructive commands).
func SetupAWSStrict(ctx context.Context, cmd *cli.Command) (context.Context, context.CancelFunc, aws.Config, error) {
	return setupAWS(ctx, cmd, 0, checkCredentialsStrict)
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
func RequestedCluster(cmd *cli.Command) string {
	if v := flagValueIfSet(cmd, "cluster"); v != "" {
		return v
	}
	args := cmd.Args().Slice()
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		return args[0]
	}
	return strings.TrimSpace(cmd.String("cluster"))
}

// ResolveClusterOrList resolves the requested cluster name. If no cluster was
// requested, it prints "No cluster specified. Available clusters:" plus the
// cluster table and returns listed=true so the caller can short-circuit.
func ResolveClusterOrList(ctx context.Context, cfg aws.Config, cmd *cli.Command) (clusterName string, listed bool, err error) {
	requested := RequestedCluster(cmd)
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

// PositionalAt returns the value of flagName, or the positional argument at
// index (0-indexed) if the flag is not set. Does NOT account for prior flags
// absorbing positional slots — use PositionalSlot for that.
func PositionalAt(cmd *cli.Command, flagName string, index int) string {
	if v := flagValueIfSet(cmd, flagName); v != "" {
		return v
	}
	args := cmd.Args().Slice()
	if index < len(args) {
		return args[index]
	}
	return flagDefault(cmd, flagName)
}

// flagValueIfSet returns the trimmed value of flagName only when it was
// explicitly provided (flag or env var). Flags that merely carry a default
// value return "" so a positional argument can still fill the slot.
func flagValueIfSet(cmd *cli.Command, flagName string) string {
	if flagName == "" || !cmd.IsSet(flagName) {
		return ""
	}
	return strings.TrimSpace(cmd.String(flagName))
}

// flagDefault returns the flag's default value (empty for most flags). Used
// as the last resort after explicit flags and positionals.
func flagDefault(cmd *cli.Command, flagName string) string {
	if flagName == "" {
		return ""
	}
	return strings.TrimSpace(cmd.String(flagName))
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
func PositionalSlot(cmd *cli.Command, flagName string, priorFlags ...string) string {
	if v := flagValueIfSet(cmd, flagName); v != "" {
		return v
	}
	consumedByFlags := 0
	for _, f := range priorFlags {
		if flagValueIfSet(cmd, f) != "" {
			consumedByFlags++
		}
	}
	args := cmd.Args().Slice()
	idx := len(priorFlags) - consumedByFlags
	if idx < len(args) {
		return args[idx]
	}
	return flagDefault(cmd, flagName)
}

// EncodeStdout writes payload to stdout as JSON or YAML based on format.
//
// "plain" is special-cased: it switches the UI layer into uncolored,
// tab-separated table rendering and returns handled=false, so the caller's
// table renderer produces grep/awk-friendly output.
//
// For any other format value it returns handled=false so the caller can fall
// through to its table renderer.
func EncodeStdout(format string, payload any) (handled bool, err error) {
	switch strings.ToLower(format) {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return true, enc.Encode(payload)
	case "yaml":
		// yaml.v3 ignores `json` tags and lowercases Go field names, so a struct
		// tagged only for JSON would serialize to YAML with keys that diverge
		// from the documented camelCase (e.g. instancetype vs instanceType).
		// Round-trip through JSON so the `json` tags drive both encoders and the
		// -o yaml keys always match -o json. (REF-59)
		data, err := json.Marshal(payload)
		if err != nil {
			return true, err
		}
		var generic any
		if err := json.Unmarshal(data, &generic); err != nil {
			return true, err
		}
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return true, enc.Encode(generic)
	case "plain":
		ui.SetPlainOutput(true)
		color.NoColor = true
		pterm.DisableColor()
		return false, nil
	default:
		return false, nil
	}
}

// Output-format presets for ValidateFormat. Commands pass the set they can
// actually render so an unknown value fails loudly instead of silently
// falling through to the table renderer.
var (
	// FormatsStandard is the common set for list/describe/encode commands.
	FormatsStandard = []string{"table", "json", "yaml", "plain"}
	// FormatsWithTree adds the cluster-list-only hierarchical tree renderer.
	FormatsWithTree = []string{"table", "json", "yaml", "plain", "tree"}
	// FormatsTableJSON is for commands that only emit a table or a JSON summary
	// (e.g. nodegroup update's run summary).
	FormatsTableJSON = []string{"table", "json"}
)

// ValidateFormat returns an error when format is not one of allowed. Matching
// is case-insensitive and an empty value is treated as valid (callers default
// it to "table"). Without this, runner.EncodeStdout returns handled=false for
// an unrecognized format and every caller falls through to its table renderer,
// so a typo like `-o jsom` silently prints a human table and exits 0. (REF-48)
func ValidateFormat(format string, allowed []string) error {
	f := strings.ToLower(strings.TrimSpace(format))
	if f == "" {
		return nil
	}
	for _, a := range allowed {
		if f == a {
			return nil
		}
	}
	return fmt.Errorf("invalid output format %q (valid: %s)", format, strings.Join(allowed, ", "))
}

// watchIsTerminal reports whether stdout is an interactive terminal.
// Overridable in tests.
var watchIsTerminal = func() bool {
	return isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd())
}

// Watch reruns fn every --watch-interval until interrupted when --watch is
// set; otherwise it runs fn once. On an interactive terminal the screen is
// cleared between iterations (top-style); when output is piped, iterations
// append instead. fn should perform the full fetch+render cycle so every
// iteration shows fresh data.
func Watch(cmd *cli.Command, fn func() error) error {
	if !cmd.Bool("watch") {
		return fn()
	}

	interval := cmd.Duration("watch-interval")
	if interval <= 0 {
		interval = 10 * time.Second
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	interactive := watchIsTerminal()
	for {
		if interactive {
			fmt.Print("\033[H\033[2J") // clear screen, cursor home
		}
		if err := fn(); err != nil {
			return err
		}
		select {
		case <-time.After(interval):
		case <-sigChan:
			return nil
		}
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
