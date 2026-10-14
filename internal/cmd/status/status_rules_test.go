package status_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/cmd/cmdtest"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// Ctrl+C during "status" after a region answered exits 1 (REF-165). The
// rows that were read are printed, and the run is not a partial result
// with exit code 4.
func TestStatus_Interrupt(t *testing.T) {
	e := newFake(t)
	ctx, cancel := context.WithCancel(cmdutiltest.Context(t))
	defer cancel()
	e.Server.OnRequest = func(hctx context.Context, r awsxtest.Request) {
		if r.Operation == fleettest.EKSDescribeAddon {
			cancel()
			<-hctx.Done()
		}
	}
	// One request slot: the cluster is answered before any add-on is
	// asked for, as in TestStatus_Deadline.
	err := e.Run(ctx, "status", "-o", "json", "--max-concurrency", "1")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want the interrupt", err)
	}
	if got := cmdutil.ExitCode(err); got != cmdutil.ExitFailure {
		t.Errorf("exit code = %d, want %d", got, cmdutil.ExitFailure)
	}
	cmdutiltest.ValidateJSON(t, apidoc.KindFleetStatus, e.Out.Bytes())
	if !strings.Contains(e.Out.String(), `"incomplete": true`) {
		t.Errorf("the document has no incomplete row:\n%s", e.Out)
	}
}

// A "status" run in which no region answered is an error with exit code
// 1 and no document (REF-165). It is not an empty fleet with exit code 0.
func TestStatus_NoRegionAnswers(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		fault func(*fleettest.World)
		want  string
	}{
		{
			name:  "a denial in every region",
			args:  []string{"-r", east, "-r", eu},
			fault: func(w *fleettest.World) { w.SetFault(fleettest.EKSListClusters, fleettest.Fault{Deny: true}) },
			want:  "could not list clusters in any of 2 region(s); the first error, for us-east-1:",
		},
		{
			name:  "one region, denied",
			fault: func(w *fleettest.World) { w.SetFault(fleettest.EKSListClusters, fleettest.Fault{Deny: true}) },
			want:  "eks:ListClusters",
		},
		{
			name: "every region refuses the credentials",
			args: []string{"-r", east, "-r", eu},
			fault: func(w *fleettest.World) {
				w.SetScopedFault(fleettest.Scope{Service: "eks"}, fleettest.Fault{Unauthenticated: true})
			},
			want: "no region answered",
		},
	}
	for _, tt := range tests {
		for _, format := range []string{"table", "json"} {
			t.Run(tt.name+"/"+format, func(t *testing.T) {
				e := newFake(t)
				tt.fault(e.World)
				err := e.Run(cmdutiltest.Context(t), append(append([]string{"status"}, tt.args...), "-o", format)...)
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("error = %v, want %q", err, tt.want)
				}
				if got := cmdutil.ExitCode(err); got != cmdutil.ExitFailure {
					t.Errorf("exit code = %d, want %d", got, cmdutil.ExitFailure)
				}
				if e.Out.Len() != 0 {
					t.Errorf("stdout = %q, want no document", e.Out)
				}
			})
		}
	}
}

// An Auto Mode cluster that also has managed nodegroups shows their stale
// AMIs and counts the nodegroups. It does not show n/a, which is for a
// cluster with no managed nodegroup.
func TestStatus_AutoModeWithNodegroups(t *testing.T) {
	world := fleettest.New()
	world.Edit(func(s *fleettest.State) {
		seed(s)
		s.Clusters = append(s.Clusters,
			&fleettest.Cluster{
				Name: "mixed", Region: east, Version: "1.33", AutoMode: true,
				Nodegroups: []*fleettest.Nodegroup{
					{Name: "web", Version: "1.33", AutoScalingGroups: []string{"asg-web"}},
					{Name: "api", Version: "1.33", AutoScalingGroups: []string{"asg-api"}},
				},
			},
			&fleettest.Cluster{Name: "auto", Region: east, Version: "1.34", AutoMode: true},
		)
		s.AutoScalingGroups = []fleettest.AutoScalingGroup{{Name: "asg-web", InstanceIDs: []string{"i-web"}}, {Name: "asg-api", InstanceIDs: []string{"i-api"}}}
		s.Instances = []fleettest.Instance{{ID: "i-web", ImageID: "ami-stale-1"}, {ID: "i-api", ImageID: "ami-latest133"}}
	})
	e := cmdtest.NewFake(t, world, east)
	err := e.Run(cmdutiltest.Context(t), "status", "-o", "plain")
	if got := cmdutil.ExitCode(err); got != cmdutil.ExitNeedsAttention {
		t.Errorf("exit code = %d (%v), want %d: a nodegroup of mixed has a stale AMI", got, err, cmdutil.ExitNeedsAttention)
	}
	rows := map[string][]string{}
	var header []string
	for i, line := range strings.Split(strings.TrimSpace(e.Out.String()), "\n") {
		cells := strings.Split(line, "\t")
		if i == 0 {
			header = cells
			continue
		}
		rows[cells[0]] = cells
	}
	cell := func(cluster, column string) string {
		for i, h := range header {
			if h == column && i < len(rows[cluster]) {
				return rows[cluster][i]
			}
		}
		t.Fatalf("no column %s for %s in:\n%s", column, cluster, e.Out)
		return ""
	}
	if got := cell("mixed", "COMPUTE"); got != "Auto Mode, 2 nodegroups" {
		t.Errorf("COMPUTE of mixed = %q, want the nodegroups beside Auto Mode", got)
	}
	if got := cell("mixed", "STALE AMI"); got == "n/a" || !strings.HasPrefix(got, "1") {
		t.Errorf("STALE AMI of mixed = %q, want the one stale nodegroup", got)
	}
	if got := cell("auto", "COMPUTE"); got != "Auto Mode" {
		t.Errorf("COMPUTE of auto = %q, want Auto Mode", got)
	}
	if got := cell("auto", "STALE AMI"); got != "n/a" {
		t.Errorf("STALE AMI of auto = %q, want n/a", got)
	}
}

// Progress never reaches stdout of a machine format: on a terminal,
// "status -o json" and "-o yaml" print one document and nothing else.
func TestStatus_NoProgressInAMachineFormat(t *testing.T) {
	for _, format := range []string{"json", "yaml", "plain"} {
		t.Run(format, func(t *testing.T) {
			e := newFake(t)
			e.Factory.IOStreams.OutTTY, e.Factory.IOStreams.ErrTTY = true, true
			e.Factory.IOStreams.TermSize = func() (int, int) { return 200, 40 }
			_ = e.Run(cmdutiltest.Context(t), append(twoRegions, "-o", format)...)
			out := e.Out.String()
			if strings.Contains(out, "\x1b[") || strings.Contains(out, "\r") {
				t.Errorf("stdout has cursor codes:\n%q", out)
			}
			switch format {
			case "json":
				cmdutiltest.ValidateJSON(t, apidoc.KindFleetStatus, e.Out.Bytes())
				dec := json.NewDecoder(strings.NewReader(out))
				var doc map[string]any
				if err := dec.Decode(&doc); err != nil || dec.More() {
					t.Errorf("stdout is not one JSON document (%v):\n%s", err, out)
				}
			case "yaml":
				cmdutiltest.ValidateYAML(t, apidoc.KindFleetStatus, e.Out.Bytes())
			case "plain":
				if !strings.HasPrefix(out, "NAME\t") && !strings.HasPrefix(out, "CLUSTER\t") {
					t.Errorf("stdout does not start with the header row:\n%s", out)
				}
			}
		})
	}
}

// A region name in REFRESH_EKS_REGIONS that cannot be a region is refused
// before any call.
func TestStatus_MalformedEnvRegion(t *testing.T) {
	e := newFake(t)
	t.Setenv("REFRESH_EKS_REGIONS", "us-east-1,bogus-1")
	err := e.Run(cmdutiltest.Context(t), "status", "-A")
	want := `invalid REFRESH_EKS_REGIONS: "bogus-1" is not a region name`
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("error = %v, want one that starts with %q", err, want)
	}
	if got := cmdutil.ExitCode(err); got != cmdutil.ExitFailure {
		t.Errorf("exit code = %d, want %d", got, cmdutil.ExitFailure)
	}
	if ops := e.Operations(); len(ops) != 0 {
		t.Errorf("requests = %v, want none", ops)
	}
}
