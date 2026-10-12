package parity

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

func TestNormaliserText(t *testing.T) {
	n := newNormaliser("http://127.0.0.1:54321", "/tmp/run-1")
	n.token("c0ffee00-aaaa-bbbb-cccc-000000000002")
	n.token("c0ffee00-aaaa-bbbb-cccc-000000000001")
	tests := []struct{ in, want string }{
		{"endpoint http://127.0.0.1:54321/clusters", "endpoint http://fake/clusters"},
		{"wrote /tmp/run-1/refresh/config.yaml", "wrote $DIR/refresh/config.yaml"},
		{"token c0ffee00-aaaa-bbbb-cccc-000000000001 then c0ffee00-aaaa-bbbb-cccc-000000000002", "token token-2 then token-1"},
		{`"startedAt": "2026-10-12T09:15:02.123456Z"`, `"startedAt": "<time>"`},
		{"time=2026-10-12T09:15:02.123+02:00 level=WARN", "time=<time> level=WARN"},
		{"upgraded about 2026-10-11; until about 2026-10-18", "upgraded about <time>; until about <time>"},
		{"[09:15:02] polling", "[<time>] polling"},
		{"took 1.234s, then 350ms, then 12.5µs", "took <duration>, then <duration>, then <duration>"},
		// Flag values and versions are not measurements.
		{"Timeout: 5m0s | Poll interval: 15s", "Timeout: 5m0s | Poll interval: 15s"},
		{"release 1.32.0-20260101 of v1.19.0-eksbuild.1", "release 1.32.0-20260101 of v1.19.0-eksbuild.1"},
		{"update 00000000-0000-4000-8000-000000000001", "update 00000000-0000-4000-8000-000000000001"},
	}
	for _, tt := range tests {
		if got := n.text(tt.in); got != tt.want {
			t.Errorf("text(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestNormaliserMutation(t *testing.T) {
	n := newNormaliser("", "")
	request := func(token string) awsxtest.Request {
		return awsxtest.Request{
			Operation: fleettest.EKSUpdateNodegroupVersion, Region: Region, Mutates: true,
			Input: map[string]any{"clusterName": "prod", "nodegroupName": "ng-a", "version": "1.33", "clientRequestToken": token},
		}
	}
	var got []Mutation
	for _, token := range []string{"aaa", "bbb", "aaa"} {
		got = append(got, n.mutation(request(token)))
	}
	want := func(token string) Mutation {
		return Mutation{
			Operation: fleettest.EKSUpdateNodegroupVersion, Region: Region, Target: "prod/ng-a",
			Payload: map[string]any{"version": "1.33", "clientRequestToken": token},
		}
	}
	if diff := cmp.Diff([]Mutation{want("token-1"), want("token-2"), want("token-1")}, got); diff != "" {
		t.Errorf("mutations (-want +got):\n%s", diff)
	}

	cluster := n.mutation(awsxtest.Request{Operation: fleettest.EKSUpdateClusterVersion, Input: map[string]any{"name": "prod", "version": "1.33"}})
	if cluster.Target != "prod" {
		t.Errorf("target of a cluster update = %q, want prod", cluster.Target)
	}
}

// TestEnvironmentIsClosed checks that the binary gets nothing from the
// machine: every variable is set by the runner, and the paths stay inside
// the run's directory.
func TestEnvironmentIsClosed(t *testing.T) {
	t.Setenv("AWS_PROFILE", "real")
	t.Setenv("REFRESH_CONTEXT", "real")
	env := environment("http://127.0.0.1:1", "/tmp/run", "/tmp/run/empty")
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		if name == "AWS_PROFILE" || name == "REFRESH_CONTEXT" || strings.Contains(value, "real") {
			t.Errorf("the environment carries %s from the machine", kv)
		}
		pathLike := strings.HasPrefix(value, "/") && name != "PATH"
		if pathLike && !strings.HasPrefix(value, "/tmp/run") {
			t.Errorf("%s points outside the run's directory", kv)
		}
	}
	for _, want := range []string{"AWS_ENDPOINT_URL=http://127.0.0.1:1", "KUBECONFIG=/tmp/run/empty", "NO_COLOR=1", "COLUMNS=120", "HOME=/tmp/run"} {
		if !strings.Contains("\n"+strings.Join(env, "\n")+"\n", "\n"+want+"\n") {
			t.Errorf("the environment lacks %s", want)
		}
	}
}

// TestScenariosAreComplete checks the table itself: every mutating legacy
// command has the cases the suite promises.
func TestScenariosAreComplete(t *testing.T) {
	commands := map[string][]string{
		"addon-update":     {"yes", "dry-run", "unattended", "failed", "resume", "lost-response", "throttled-read"},
		"nodegroup-scale":  {"yes", "dry-run", "unattended", "failed", "resume", "lost-response", "throttled-read"},
		"nodegroup-update": {"yes", "dry-run", "failed", "resume", "interrupt", "lost-response", "throttled-read"},
		"cluster-upgrade":  {"yes", "dry-run", "unattended", "blocked", "failed", "resume", "interrupt", "lost-response", "throttled-read"},
		"cluster-rollback": {"yes", "dry-run", "unattended", "blocked", "failed", "resume", "interrupt", "lost-response", "throttled-read"},
	}
	have := map[string]bool{}
	for _, sc := range scenarios() {
		have[sc.Name] = true
		if len(sc.Legacy) == 0 {
			t.Errorf("%s has no legacy command line", sc.Name)
		}
		if sc.Seed == nil {
			t.Errorf("%s has no seed", sc.Name)
		}
	}
	for command, cases := range commands {
		for _, c := range cases {
			if !have[command+"-"+c] {
				t.Errorf("no scenario %s-%s", command, c)
			}
		}
	}
}
