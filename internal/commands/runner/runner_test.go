package runner

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/urfave/cli/v3"
	"gopkg.in/yaml.v3"
)

// ── setupAWS context propagation ──────────────────────────────────────────────

// newTimeoutCommand returns a parsed *cli.Command carrying a timeout flag,
// mirroring the root command's global --timeout.
func newTimeoutCommand(t *testing.T) *cli.Command {
	t.Helper()
	var captured *cli.Command
	cmd := &cli.Command{
		Name:  "test",
		Flags: []cli.Flag{&cli.DurationFlag{Name: "timeout", Value: time.Minute}},
		Action: func(_ context.Context, c *cli.Command) error {
			captured = c
			return nil
		},
	}
	if err := cmd.Run(context.Background(), []string{"test"}); err != nil {
		t.Fatal(err)
	}
	return captured
}

// Regression: setupAWS must derive its context from the action's context
// (cancelled on Ctrl+C / SIGTERM by main) rather than context.Background(),
// so signal cancellation propagates to in-flight AWS calls.
func TestSetupAWS_DerivesFromParentContext(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	cmd := newTimeoutCommand(t)

	got, cancel, _, err := setupAWS(parent, cmd, 0, func(context.Context, aws.Config) error {
		return nil
	})
	if err != nil {
		t.Fatalf("setupAWS() = %v", err)
	}
	defer cancel()

	select {
	case <-got.Done():
		t.Fatal("context cancelled prematurely")
	default:
	}
	cancelParent()
	select {
	case <-got.Done():
		// parent cancellation propagated — correct
	case <-time.After(time.Second):
		t.Fatal("parent cancellation did not propagate to setupAWS context")
	}
}

// setupAWS applies the timeout it is given (not --timeout), and timeout <= 0
// means no deadline: long-running callers (fleet update, scale --wait) rely on
// this to scope deadlines themselves.
func TestSetupAWS_TimeoutScoping(t *testing.T) {
	cmd := newTimeoutCommand(t) // --timeout defaults to 1m
	noop := func(context.Context, aws.Config) error { return nil }

	ctx, cancel, _, err := setupAWS(context.Background(), cmd, 0, noop)
	if err != nil {
		t.Fatalf("setupAWS() = %v", err)
	}
	if _, ok := ctx.Deadline(); ok {
		t.Error("timeout 0: want no deadline")
	}
	cancel()

	ctx, cancel, _, err = setupAWS(context.Background(), cmd, 3*time.Hour, noop)
	if err != nil {
		t.Fatalf("setupAWS() = %v", err)
	}
	dl, ok := ctx.Deadline()
	if !ok || time.Until(dl) < 2*time.Hour {
		t.Errorf("timeout 3h: deadline = %v (ok=%v), want ~3h out", dl, ok)
	}
	cancel()
}

// Even with no overall deadline (timeout 0), the credential check must run
// under --timeout so a stalled STS/SSO/IMDS call can't hang the command.
func TestSetupAWS_CredentialCheckBoundedByFlagTimeout(t *testing.T) {
	cmd := newTimeoutCommand(t) // --timeout defaults to 1m
	var checkDeadline time.Time
	var hasDeadline bool
	ctx, cancel, _, err := setupAWS(context.Background(), cmd, 0, func(c context.Context, _ aws.Config) error {
		checkDeadline, hasDeadline = c.Deadline()
		return nil
	})
	if err != nil {
		t.Fatalf("setupAWS() = %v", err)
	}
	defer cancel()
	if !hasDeadline || time.Until(checkDeadline) > time.Minute {
		t.Errorf("credential check deadline = %v (ok=%v), want <= 1m", checkDeadline, hasDeadline)
	}
	if _, ok := ctx.Deadline(); ok {
		t.Error("returned context: want no deadline")
	}
	if ctx.Err() != nil {
		t.Error("returned context must stay live after the check context is released")
	}
}

func TestCheckContext(t *testing.T) {
	cases := []struct {
		name         string
		api, overall time.Duration
		wantDeadline bool
	}{
		{"no overall deadline", time.Minute, 0, true},
		{"overall longer than api", time.Minute, time.Hour, true},
		{"overall shorter than api", time.Hour, time.Minute, false},
		{"no api timeout", 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := checkContext(context.Background(), tc.api, tc.overall)
			defer cancel()
			if _, ok := ctx.Deadline(); ok != tc.wantDeadline {
				t.Errorf("deadline = %v, want %v", ok, tc.wantDeadline)
			}
		})
	}
}

// setupAWS must tolerate a nil context (hand-constructed invocations in tests).
func TestSetupAWS_NilContextFallsBack(t *testing.T) {
	cmd := newTimeoutCommand(t)

	// A nil context is the case under test. Pass it through a typed variable,
	// not a literal nil, so staticcheck SA1012 has nothing to flag.
	var nilCtx context.Context
	_, cancel, _, err := setupAWS(nilCtx, cmd, 0, func(context.Context, aws.Config) error { return nil })
	if err != nil {
		t.Fatalf("setupAWS() = %v", err)
	}
	cancel()
}

// newTestCommand builds a parsed *cli.Command with the given string flags
// registered and args parsed. flags is name→value; "" value means the flag is
// registered but not explicitly set. Set flags are passed as --name=value
// tokens before the positionals (v3 parses flags anywhere, so placement is
// irrelevant).
func newTestCommand(t *testing.T, args []string, flags map[string]string) *cli.Command {
	t.Helper()
	var captured *cli.Command
	cmd := &cli.Command{
		Name: "test",
		Action: func(_ context.Context, c *cli.Command) error {
			captured = c
			return nil
		},
	}
	argv := []string{"test"}
	for name, value := range flags {
		cmd.Flags = append(cmd.Flags, &cli.StringFlag{Name: name})
		if value != "" {
			argv = append(argv, "--"+name+"="+value)
		}
	}
	argv = append(argv, args...)
	if err := cmd.Run(context.Background(), argv); err != nil {
		t.Fatal(err)
	}
	return captured
}

// ── PositionalSlot ────────────────────────────────────────────────────────────

// Regression for the addon update bug: `--addon=foo my-cluster v1.2.3` must
// resolve version to "v1.2.3", not "" (which would silently default to
// "latest"). The version slot is third positional after (cluster, addon).
// With --addon set, the positional index shifts from 2 down to 1.
func TestPositionalSlot_FlagShiftsLaterPositionals(t *testing.T) {
	cmd := newTestCommand(t,
		[]string{"my-cluster", "v1.2.3"},
		map[string]string{"cluster": "", "addon": "foo", "version": ""},
	)
	if got := PositionalSlot(cmd, "version", "cluster", "addon"); got != "v1.2.3" {
		t.Errorf("version = %q, want v1.2.3 (positional shifted because --addon set)", got)
	}
}

func TestPositionalSlot_AllPositional(t *testing.T) {
	cmd := newTestCommand(t,
		[]string{"my-cluster", "vpc-cni", "v1.2.3"},
		map[string]string{"cluster": "", "addon": "", "version": ""},
	)
	if got := PositionalSlot(cmd, "version", "cluster", "addon"); got != "v1.2.3" {
		t.Errorf("version = %q, want v1.2.3", got)
	}
	if got := PositionalSlot(cmd, "addon", "cluster"); got != "vpc-cni" {
		t.Errorf("addon = %q, want vpc-cni", got)
	}
	if got := PositionalSlot(cmd, "cluster"); got != "my-cluster" {
		t.Errorf("cluster = %q, want my-cluster", got)
	}
}

func TestPositionalSlot_FlagWinsWhenSet(t *testing.T) {
	cmd := newTestCommand(t,
		[]string{"my-cluster", "vpc-cni"},
		map[string]string{"cluster": "", "addon": "", "version": "v9.9.9"},
	)
	if got := PositionalSlot(cmd, "version", "cluster", "addon"); got != "v9.9.9" {
		t.Errorf("version = %q, want v9.9.9 (from flag)", got)
	}
}

func TestPositionalSlot_AllFlagsLeavesNoPositional(t *testing.T) {
	cmd := newTestCommand(t,
		nil,
		map[string]string{"cluster": "my-cluster", "addon": "vpc-cni", "version": "v1.2.3"},
	)
	if got := PositionalSlot(cmd, "version", "cluster", "addon"); got != "v1.2.3" {
		t.Errorf("version = %q, want v1.2.3", got)
	}
}

func TestPositionalSlot_MissingSlotReturnsEmpty(t *testing.T) {
	cmd := newTestCommand(t,
		[]string{"my-cluster"},
		map[string]string{"cluster": "", "addon": "", "version": ""},
	)
	if got := PositionalSlot(cmd, "version", "cluster", "addon"); got != "" {
		t.Errorf("version = %q, want empty (no positional, no flag)", got)
	}
}

// v3 parses flags after positionals natively — the old trailing-flag replay
// machinery is gone, so verify the framework keeps this working end to end.
func TestPositionalSlot_TrailingFlagParsedNatively(t *testing.T) {
	cmd := newTestCommand(t,
		[]string{"my-cluster", "--version=v2.0.0"},
		map[string]string{"cluster": "", "addon": "", "version": ""},
	)
	if got := PositionalSlot(cmd, "cluster"); got != "my-cluster" {
		t.Errorf("cluster = %q, want my-cluster", got)
	}
	if got := PositionalSlot(cmd, "version", "cluster", "addon"); got != "v2.0.0" {
		t.Errorf("version = %q, want v2.0.0 (trailing flag parsed by v3)", got)
	}
}

// ── ParseFilters ──────────────────────────────────────────────────────────────

func TestParseFilters(t *testing.T) {
	got := ParseFilters([]string{"name=prod", "env=staging", "malformed", "k=v=extra"})
	if len(got) != 3 {
		t.Fatalf("ParseFilters returned %d entries, want 3: %v", len(got), got)
	}
	if got["name"] != "prod" || got["env"] != "staging" {
		t.Errorf("ParseFilters = %v", got)
	}
	// SplitN(2) keeps everything after the first "=" as the value.
	if got["k"] != "v=extra" {
		t.Errorf(`got["k"] = %q, want "v=extra"`, got["k"])
	}
}

func TestParseFilters_Empty(t *testing.T) {
	if got := ParseFilters(nil); len(got) != 0 {
		t.Errorf("ParseFilters(nil) = %v, want empty map", got)
	}
}

// ── ValidateFormat (REF-48) ─────────────────────────────────────────────────

func TestValidateFormat(t *testing.T) {
	for _, tc := range []struct {
		name    string
		format  string
		allowed []string
		wantErr bool
	}{
		{"empty is valid (caller defaults to table)", "", FormatsStandard, false},
		{"table valid", "table", FormatsStandard, false},
		{"json valid", "json", FormatsStandard, false},
		{"case-insensitive", "JSON", FormatsStandard, false},
		{"surrounding space tolerated", "  yaml ", FormatsStandard, false},
		{"tree only valid with tree set", "tree", FormatsWithTree, false},
		{"tree rejected in standard set", "tree", FormatsStandard, true},
		{"typo rejected", "jsom", FormatsStandard, true},
		{"xml rejected", "xml", FormatsStandard, true},
		{"yaml rejected for table/json-only", "yaml", FormatsTableJSON, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateFormat(tc.format, tc.allowed)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateFormat(%q) error = %v, wantErr %v", tc.format, err, tc.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "valid:") {
				t.Errorf("error %q should list valid formats", err)
			}
		})
	}
}

// ── EncodeStdout YAML key parity (REF-59) ───────────────────────────────────

// captureStdout redirects os.Stdout for the duration of fn and returns what was
// written.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()
	_ = w.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}

// EncodeStdout's YAML output must use the same camelCase keys as its JSON output
// (driven by the `json` tags), not yaml.v3's lowercased Go field names.
func TestEncodeStdout_YAMLKeysMatchJSON(t *testing.T) {
	type item struct {
		InstanceType string `json:"instanceType"`
		DesiredSize  int    `json:"desiredSize"`
		ReadyNodes   int    `json:"readyNodes"`
	}
	payload := map[string]any{"nodegroups": []item{{"m5.large", 3, 2}}, "count": 1}

	yamlOut := captureStdout(t, func() {
		if handled, err := EncodeStdout("yaml", payload); !handled || err != nil {
			t.Fatalf("EncodeStdout(yaml) handled=%v err=%v", handled, err)
		}
	})

	for _, key := range []string{"instanceType:", "desiredSize:", "readyNodes:"} {
		if !strings.Contains(yamlOut, key) {
			t.Errorf("YAML output missing camelCase key %q\n%s", key, yamlOut)
		}
	}
	if strings.Contains(yamlOut, "instancetype:") {
		t.Errorf("YAML output still has lowercased key 'instancetype'\n%s", yamlOut)
	}

	// And the YAML must round-trip to the same structure as the JSON encoding.
	var fromYAML, fromJSON any
	if err := yaml.Unmarshal([]byte(yamlOut), &fromYAML); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	jsonBytes, _ := json.Marshal(payload)
	if err := json.Unmarshal(jsonBytes, &fromJSON); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	gotJSON, _ := json.Marshal(fromYAML)
	wantJSON, _ := json.Marshal(fromJSON)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("YAML structure diverges from JSON:\n got=%s\nwant=%s", gotJSON, wantJSON)
	}
}
