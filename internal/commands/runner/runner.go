// Package runner provides shared CLI command primitives so that every
// command's run* function doesn't re-implement context+awsconfig+credential
// setup, the "no cluster specified" fallback, and json/yaml encoding.
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/fatih/color"
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

// checkCredentials is awsinternal.CheckAWSCredentials. It prints nothing: the
// returned error carries the setup help (only for a credential problem) and
// main prints it once, on stderr, so stdout stays clean for -o json/yaml.
func checkCredentials(ctx context.Context, cfg aws.Config) error {
	return awsinternal.CheckAWSCredentials(ctx, cfg)
}

// setupAWS is the shared body of the SetupAWS* helpers.
// On error the internal context is canceled and the returned cancel is nil.
// timeout <= 0 means no deadline (the context is then only signal-cancellable).
func setupAWS(ctx context.Context, cmd *cli.Command, timeout time.Duration, check credentialCheck) (context.Context, context.CancelFunc, aws.Config, error) {
	// Derive from the action's context (cancelled on Ctrl+C / SIGTERM by main)
	// so signal handling propagates to in-flight AWS calls. ctx is nil only
	// for hand-constructed invocations in tests.
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := apiContext(ctx, timeout)

	// Config loading and the credential check (STS, SSO, IMDS) always run
	// under --timeout, even when the returned context has a longer or no
	// deadline, so a stalled credential source can't hang the command.
	checkCtx, cancelCheck := checkContext(ctx, APITimeout(cmd), timeout)
	defer cancelCheck()

	cfg, err := awsconfig.Load(checkCtx, cmd)
	if err != nil {
		cancel()
		// Returned, not printed: main prints it once, on stderr.
		return nil, nil, aws.Config{}, fmt.Errorf("failed to load AWS config: %w", err)
	}
	if err := check(checkCtx, cfg); err != nil {
		cancel()
		return nil, nil, aws.Config{}, err
	}
	return ctx, cancel, cfg, nil
}

// apiContext derives the context for a command's AWS calls from the
// signal-cancellable action context. timeout <= 0 means no deadline.
//
// Prompts under the returned context (cluster or nodegroup pattern
// confirmation, phase confirmation) wait on the action context, not on the
// API deadline, and the deadline stops while they wait. So an unanswered
// prompt does not fail with a timeout, and the time the user takes to answer
// is not taken from the AWS call budget. Ctrl+C still cancels a prompt.
func apiContext(signalCtx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		ctx, cancel := context.WithCancel(signalCtx)
		return ui.WithPromptScope(ctx, signalCtx, nil), cancel
	}
	dl, cancel := withPausableTimeout(signalCtx, timeout)
	return ui.WithPromptScope(dl, signalCtx, dl.pause), cancel
}

// checkContext bounds the setup phase by apiTimeout (--timeout) when that is
// shorter than the returned context's timeout (or that has no deadline).
func checkContext(ctx context.Context, apiTimeout, timeout time.Duration) (context.Context, context.CancelFunc) {
	if apiTimeout > 0 && (timeout <= 0 || apiTimeout < timeout) {
		return context.WithTimeout(ctx, apiTimeout)
	}
	return ctx, func() {}
}

// SetupAWS opens a context with the command's timeout, loads the AWS config,
// and checks credentials. On error, the returned cancel is nil and the
// internal context has already been cancelled.
func SetupAWS(ctx context.Context, cmd *cli.Command) (context.Context, context.CancelFunc, aws.Config, error) {
	return setupAWS(ctx, cmd, APITimeout(cmd), checkCredentials)
}

// SetupAWSWithDeadline is like SetupAWS but uses the given timeout for the
// returned context instead of --timeout. A timeout <= 0 means no deadline:
// the context is cancelled only by Ctrl+C / SIGTERM. Use it when a command
// scopes its own deadlines (per cluster, per wait) and --timeout alone would
// cut a long-running operation short.
func SetupAWSWithDeadline(ctx context.Context, cmd *cli.Command, timeout time.Duration) (context.Context, context.CancelFunc, aws.Config, error) {
	return setupAWS(ctx, cmd, timeout, checkCredentials)
}

// Regions returns the explicit scan list of a multi-region command: its own
// repeatable -r/--region values. Without them, and only when the command is
// not sweeping (allRegions false: no -A, no --tree), the global --region
// placed before the subcommand counts as the scan list, so
// `refresh --region X status` equals `refresh status --region X`. urfave/cli
// resolves "region" to the local slice flag even when only the global one was
// set, so reading cmd.StringSlice("region") alone would drop it.
//
// With a sweep, the global --region only sets the home region and so the
// partition (`refresh --region cn-north-1 cluster list -A` sweeps the China
// partition), and REFRESH_EKS_REGIONS still applies downstream. It returns
// nil when there is no explicit list.
//
// Every subcommand that declares its own --region slice must read it through
// this helper (status, cluster list, nodegroup update --all-clusters).
func Regions(cmd *cli.Command, allRegions bool) []string {
	var local []string
	for _, f := range cmd.Flags {
		if slices.Contains(f.Names(), "region") && f.IsSet() {
			local = awsconfig.SetFlagValues(cmd, "region")
			break
		}
	}
	if len(local) > 0 || allRegions {
		return local
	}
	return awsconfig.SetFlagValues(cmd, "region")
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
//
// Keep --cluster flags free of env Sources: urfave/cli reports an
// env-sourced flag as set, so the env var would beat an explicit positional
// and shift the positional into the next slot. `nodegroup update` reads
// EKS_CLUSTER_NAME itself.
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

// ResolveCluster resolves the cluster for a mutating command. Resolution
// order: --cluster flag, first positional, active `refresh use` context. The
// kubeconfig current context is never used, so a stray kubeconfig cannot pick
// the target of a mutation. A cluster from the context is announced on
// stderr. It never lists clusters: when nothing resolves it returns an error
// wrapping awsinternal.ErrNoClusterSpecified. A non-exact name needs
// interactive confirmation, except with -o json/yaml (see ResolveClusterName).
func ResolveCluster(ctx context.Context, cfg aws.Config, cmd *cli.Command) (string, error) {
	return ResolveClusterName(ctx, cfg, RequestedCluster(cmd), cmd.String("format"))
}

// ResolveClusterName is ResolveCluster for a caller that parsed the requested
// cluster itself. With a machine format (-o json/yaml) it never prompts, even
// on a TTY: a non-exact name fails with an error that names the candidate.
func ResolveClusterName(ctx context.Context, cfg aws.Config, requested, format string) (string, error) {
	return awsinternal.ClusterNameWithOptions(ctx, cfg, requested, mutatingClusterOptions(format))
}

// mutatingClusterOptions returns the resolution options for a mutating
// command run with the given -o format.
func mutatingClusterOptions(format string) awsinternal.ClusterNameOptions {
	return awsinternal.ClusterNameOptions{NonInteractive: IsMachineFormat(format)}
}

// ResolveClusterOrList resolves the cluster for a read-only command, using
// the ResolveCluster order plus a final fallback to the kubeconfig current
// context. When nothing resolves, it prints the
// available clusters to stderr as a hint (skipped for -o json/yaml) and
// returns listed=true with a non-nil error, so the command exits non-zero
// and stdout stays empty.
func ResolveClusterOrList(ctx context.Context, cfg aws.Config, cmd *cli.Command) (clusterName string, listed bool, err error) {
	// -o json/yaml never prompts: a partial name resolves as it would
	// without a terminal.
	opts := awsinternal.ClusterNameOptions{ReadOnly: true, NonInteractive: IsMachineFormat(cmd.String("format"))}
	name, err := awsinternal.ClusterNameWithOptions(ctx, cfg, RequestedCluster(cmd), opts)
	if err == nil {
		return name, false, nil
	}
	if !errors.Is(err, awsinternal.ErrNoClusterSpecified) {
		return "", false, err
	}
	switch strings.ToLower(cmd.String("format")) {
	case "json", "yaml":
		return "", true, err
	}
	svc := factory.NewClusterService(cfg, false, nil)
	summaries, lerr := svc.List(ctx, clustersvc.ListOptions{})
	if lerr == nil {
		_, _ = fmt.Fprintln(ui.Stderr, "No cluster specified. Available clusters:")
		_, _ = fmt.Fprintln(ui.Stderr)
		clusterview.WriteClustersHint(ui.Stderr, summaries)
		_, _ = fmt.Fprintln(ui.Stderr)
	}
	return "", true, err
}

// flagValueIfSet returns the trimmed value of flagName only when it was
// explicitly provided (on the command line, or by an env var source). Flags that merely carry a default
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
// positional". For each prior flag name that IS set on cmd, this
// helper subtracts 1 from the expected positional index, so flags and
// positionals can be mixed freely.
//
// Example: a command with slot order (cluster, addon, version) where the
// cluster has --cluster, the addon has --addon, and the version has --version:
//
//	cluster := PositionalSlot(cmd, "cluster")                       // slot 0
//	addon   := PositionalSlot(cmd, "addon", "cluster")              // slot 1
//	version := PositionalSlot(cmd, "version", "cluster", "addon")   // slot 2
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

// IsMachineFormat reports whether format is json or yaml: stdout then carries
// exactly one document and every human line goes to stderr or is dropped.
func IsMachineFormat(format string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "json", "yaml":
		return true
	}
	return false
}

// Output-format presets for ValidateFormat. Commands pass the set they can
// actually render so an unknown value fails loudly instead of silently
// falling through to the table renderer.
var (
	// FormatsStandard is the common set for list/describe/encode commands.
	FormatsStandard = []string{"table", "json", "yaml", "plain"}
	// FormatsWithTree adds the cluster-list-only hierarchical tree renderer.
	FormatsWithTree = []string{"table", "json", "yaml", "plain", "tree"}
	// FormatsDocument is for commands that emit a human report or one
	// JSON/YAML document, with no plain TSV table (e.g. nodegroup update's
	// run summary).
	FormatsDocument = []string{"table", "json", "yaml"}
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
	return ui.IsTerminal(os.Stdout)
}

// Watch reruns fn every --watch-interval until interrupted when --watch is
// set; otherwise it runs fn once. On an interactive terminal the screen is
// cleared between iterations (top-style); when output is piped, iterations
// append instead. fn should perform the full fetch+render cycle so every
// iteration shows fresh data.
//
// --watch with -o json/yaml is rejected: stdout for those formats carries
// exactly one document, and a watch would print one per interval. Scripts
// that poll should run the command in their own loop.
func Watch(ctx context.Context, cmd *cli.Command, fn func() error) error {
	if !cmd.Bool("watch") {
		return fn()
	}
	if IsMachineFormat(cmd.String("format")) {
		return fmt.Errorf("--watch cannot be combined with -o %s: stdout carries one document per run; poll by running the command in a loop instead", strings.ToLower(cmd.String("format")))
	}

	interval := cmd.Duration("watch-interval")
	if interval <= 0 {
		interval = 10 * time.Second
	}

	// Also stop on parent context cancellation (a deadline expiry or signal),
	// not only on a bare SIGINT/SIGTERM, so the loop never runs past ctx.
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	interactive := watchIsTerminal()
	for {
		if interactive {
			fmt.Print("\033[H\033[2J") // clear screen, cursor home
		}
		// A partial result (exit 4) was printed and warned about on stderr;
		// the next poll may be complete, so keep watching.
		if err := fn(); err != nil && !IsIncomplete(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
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
