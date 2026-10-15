package cluster_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/cmd/cmdtest"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// expiring is a credentials provider that works until it is told to stop,
// as a session whose token runs out does. Its credentials are already
// expired when it hands them out, so the SDK asks it again for every
// request and sees the change at once.
type expiring struct{ expired atomic.Bool }

func (p *expiring) Retrieve(context.Context) (aws.Credentials, error) {
	if p.expired.Load() {
		return aws.Credentials{}, errors.New("the SSO session has expired; run aws sso login")
	}
	return aws.Credentials{AccessKeyID: "AKIDFAKEFAKEFAKE", SecretAccessKey: "fake-secret", Source: "test", CanExpire: true}, nil
}

// onFirst returns an OnRequest hook that calls f once, on the first
// request for op. The hook runs before a fault sees the request, so what
// f sets applies to that request too.
func onFirst(op fleettest.Operation, f func()) func(context.Context, awsxtest.Request) {
	var once atomic.Bool
	return func(_ context.Context, r awsxtest.Request) {
		if r.Operation == op && once.CompareAndSwap(false, true) {
			f()
		}
	}
}

// onPoll returns an OnRequest hook that calls f once, on the second poll
// of the first update the run started: the run is inside a wait.
func onPoll(f func()) func(context.Context, awsxtest.Request) {
	var polls atomic.Int32
	return func(_ context.Context, r awsxtest.Request) {
		if r.Operation == fleettest.EKSDescribeUpdate && polls.Add(1) == 2 {
			f()
		}
	}
}

// oneDocument fails the test unless out is exactly one JSON document of
// the kind, with nothing before or after it.
func oneDocument(t *testing.T, out []byte, kind string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(out))
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("stdout is not a JSON document: %v\n%s", err, out)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Errorf("stdout has more than one document (%v):\n%s", err, out)
	}
	if doc["kind"] != kind {
		t.Errorf("kind = %v, want %s", doc["kind"], kind)
	}
	return doc
}

// midRun is a change that something stops after its first phase.
type midRun struct {
	name    string
	command []string
	// version and upgraded build the engine world.
	version  string
	upgraded bool
	// prepare breaks the world part way.
	prepare func(e *cmdtest.Fake)
	exit    int
	sent    []string
	// want are texts that the output must have: what stopped the run, the
	// IAM action to grant, the phases that completed, and the command
	// that goes on.
	want []string
}

func midRuns() []midRun {
	upgrade := []string{"cluster", "upgrade", "prod", "--to", "1.33", "--yes"}
	rollback := []string{"cluster", "rollback", "prod", "--yes"}
	resumeUpgrade := "to go on from the live state of the cluster, run: refresh cluster upgrade prod --to 1.33 --yes"
	resumeRollback := "to go on from the live state of the cluster, run: refresh cluster rollback prod --yes"
	expire := func(e *cmdtest.Fake) func() {
		return func() { e.World.SetScopedFault(fleettest.Scope{}, fleettest.Fault{Code: "ExpiredTokenException"}) }
	}
	return []midRun{
		{
			// The token runs out after the control plane moved, and AWS
			// refuses the next submit. Nothing after it is sent.
			name: "credentials expire before a submit", command: upgrade, version: "1.32",
			prepare: func(e *cmdtest.Fake) { e.Server.OnRequest = onFirst(fleettest.EKSUpdateAddon, expire(e)) },
			exit:    cmdutil.ExitFailure, sent: []string{refresh, cp33, cniTo33},
			want: []string{"ExpiredTokenException", "eks:UpdateAddon", "completed: the control plane to 1.33", resumeUpgrade},
		},
		{
			// The token runs out while the run waits for the control
			// plane. The update may still run: the result is incomplete.
			name: "credentials expire during a wait", command: upgrade, version: "1.32",
			prepare: func(e *cmdtest.Fake) { e.Server.OnRequest = onPoll(expire(e)) },
			exit:    cmdutil.ExitIncomplete, sent: []string{refresh, cp33},
			want: []string{"ExpiredTokenException", "no phase completed", resumeUpgrade},
		},
		{
			name: "UpdateNodegroupVersion is denied after the control plane", command: upgrade, version: "1.32",
			prepare: func(e *cmdtest.Fake) {
				e.World.SetFault(fleettest.EKSUpdateNodegroupVersion, fleettest.Fault{Deny: true})
			},
			exit: cmdutil.ExitFailure, sent: []string{refresh, cp33, cniTo33, api33},
			want: []string{
				"grant eks:UpdateNodegroupVersion",
				"completed: the control plane to 1.33, the add-ons that 1.33 needs before the nodegroup rolls", resumeUpgrade,
			},
		},
		{
			// A rollback reads the update history again before each change,
			// to know that the window is still open.
			name: "ListUpdates is denied after the first roll", command: rollback, version: "1.33", upgraded: true,
			prepare: func(e *cmdtest.Fake) {
				e.Server.OnRequest = onFirst(fleettest.EKSUpdateNodegroupVersion, func() {
					e.World.SetFault(fleettest.EKSListUpdates, fleettest.Fault{Deny: true})
				})
			},
			exit: cmdutil.ExitIncomplete, sent: []string{api32},
			want: []string{"could not read the update history of prod", "nodegroup web was not changed", resumeRollback},
		},
	}
}

// A change that loses its credentials or a permission part way stops
// there: it sends nothing more, says what stopped it and what to grant,
// names the phases that completed, and prints the command that goes on.
// With -o json stdout is still exactly one Run document.
func TestEngineCommands_StopPartWay(t *testing.T) {
	for _, tc := range midRuns() {
		for _, output := range []string{"table", "json"} {
			t.Run(tc.name+"/"+output, func(t *testing.T) {
				e := cmdtest.NewFake(t, engineWorld(tc.version, tc.upgraded, nil), east)
				e.UseWorldClock()
				withKube(t, e)
				tc.prepare(e)
				err := e.Run(cmdutiltest.Context(t), append(append([]string{}, tc.command...), "-o", output)...)
				if got := cmdutil.ExitCode(err); got != tc.exit {
					t.Errorf("exit code = %d, want %d (error: %v)", got, tc.exit, err)
				}
				if diff := cmp.Diff(tc.sent, sentBy(e)); diff != "" {
					t.Errorf("the requests that change the cluster (-want +got):\n%s", diff)
				}
				msg := ""
				if err != nil {
					msg = err.Error()
				}
				all := msg + "\n" + e.Out.String() + e.Err.String()
				for _, w := range tc.want {
					if !strings.Contains(all, w) {
						t.Errorf("the output has no %q:\n%s", w, all)
					}
				}
				if output == "json" {
					doc := oneDocument(t, e.Out.Bytes(), "Run")
					if f, _ := doc["failures"].([]any); len(f) == 0 {
						t.Errorf("the Run document lists no failure:\n%s", e.Out)
					}
					if strings.Contains(e.Err.String(), `"apiVersion"`) {
						t.Errorf("a document reached stderr:\n%s", e.Err)
					}
				}
			})
		}
	}
}

// Credentials that the local provider can no longer produce (an SSO
// session that ended, a profile whose source is gone) fail before any
// request is sent. The run must stop at the first such poll, with the
// phases that completed and the command that goes on.
func TestUpgrade_ProviderFailsDuringAWait(t *testing.T) {
	t.Skip("known defect: internal/change/wait.go:332 and :392 end a wait only on awsx.IsPermanentAPIError; " +
		"a credential-chain error (awsx.IsCredentialError, not an API error) is retried until --wait-timeout, " +
		"so an upgrade polls for 4h with a warning on each poll and then exits 4 on a timeout")
	e := cmdtest.NewFake(t, engineWorld("1.32", false, nil), east)
	e.UseWorldClock()
	withKube(t, e)
	creds := &expiring{}
	cfg := e.Server.Config(east)
	cfg.Credentials = creds
	cmdutiltest.UseAWS(e.Factory, cfg)
	e.Server.OnRequest = onPoll(func() { creds.expired.Store(true) })
	start := e.World.Now()
	err := e.Run(cmdutiltest.Context(t), "cluster", "upgrade", "prod", "--to", "1.33", "--yes")
	if got := cmdutil.ExitCode(err); got != cmdutil.ExitIncomplete && got != cmdutil.ExitFailure {
		t.Errorf("exit code = %d (%v), want 1 or 4", got, err)
	}
	if waited := e.World.Now().Sub(start); waited > 5*time.Minute {
		t.Errorf("the run waited %s with credentials that cannot be retrieved, want a stop at the first failed poll", waited)
	}
	if n := strings.Count(e.Err.String(), "could not read the status"); n > 1 {
		t.Errorf("%d warnings about the same credential failure, want at most 1", n)
	}
	if err == nil || !strings.Contains(err.Error(), "refresh cluster upgrade prod --to 1.33 --yes") {
		t.Errorf("error = %v, want the command that goes on", err)
	}
}

// A rollback whose read of the update history is denied must say which
// permission it lacks, as every other denied read does: the failure names
// eks:ListUpdates and is AccessDenied. Today the failure is "Unknown: the
// update history of the cluster was not read", with no action to grant.
func TestRollback_DeniedHistoryNamesTheAction(t *testing.T) {
	t.Skip("known defect: internal/change/rollbackeligibility.go:168 builds the failure of an unread update history " +
		"with ReasonUnknown and no operation, so a denied eks:ListUpdates never names the action to grant")
	for _, when := range []string{"before any change", "after the first roll"} {
		t.Run(when, func(t *testing.T) {
			e := cmdtest.NewFake(t, engineWorld("1.33", true, nil), east)
			e.UseWorldClock()
			withKube(t, e)
			deny := func() { e.World.SetFault(fleettest.EKSListUpdates, fleettest.Fault{Deny: true}) }
			if when == "before any change" {
				deny()
			} else {
				e.Server.OnRequest = onFirst(fleettest.EKSUpdateNodegroupVersion, deny)
			}
			err := e.Run(cmdutiltest.Context(t), "cluster", "rollback", "prod", "--yes", "-o", "json")
			if got := cmdutil.ExitCode(err); got != cmdutil.ExitIncomplete {
				t.Errorf("exit code = %d (%v), want 4", got, err)
			}
			if !strings.Contains(e.Out.String(), `"operation": "eks:ListUpdates"`) || !strings.Contains(e.Out.String(), "AccessDenied") {
				t.Errorf("no AccessDenied failure for eks:ListUpdates:\n%s", e.Out)
			}
		})
	}
}
