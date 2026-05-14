package workload

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/dantech2000/refresh/internal/services/workloads"
)

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	original := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = original })

	callErr := fn()
	_ = w.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String(), callErr
}

func findSub(cmd *cli.Command, name string) *cli.Command {
	for _, sc := range cmd.Subcommands {
		if sc.Name == name {
			return sc
		}
		for _, a := range sc.Aliases {
			if a == name {
				return sc
			}
		}
	}
	return nil
}

func sampleResult() workloads.PDBCoverageResult {
	return workloads.PDBCoverageResult{
		Deployments: []workloads.PDBCoverageRow{
			{Namespace: "apps", Deployment: "api", HasPDB: true, PDBs: []string{"api-pdb"}, Status: "PROTECTED"},
			{Namespace: "apps", Deployment: "worker", HasPDB: false, Status: "MISSING"},
		},
		Summary: workloads.PDBCoverageSummary{
			TotalDeployments: 2,
			WithPDB:          1,
			WithoutPDB:       1,
		},
	}
}

func TestCommandStructure(t *testing.T) {
	cmd := Command()
	if cmd.Name != "workload" {
		t.Fatalf("Command name = %q", cmd.Name)
	}
	if findSub(cmd, "pdbs") == nil {
		t.Fatal("workload pdbs subcommand missing")
	}
	if findSub(cmd, "pdb") == nil {
		t.Fatal("workload pdb alias missing")
	}
}

func TestOutputPDBCoverageJSON(t *testing.T) {
	out, err := captureStdout(t, func() error { return outputPDBCoverageJSON(sampleResult()) })
	if err != nil {
		t.Fatalf("JSON output error: %v", err)
	}
	for _, want := range []string{"worker", "api", "withoutPdb", "withPdb", "apps"} {
		if !strings.Contains(out, want) {
			t.Errorf("JSON output missing %q: %q", want, out)
		}
	}
}

func TestOutputPDBCoverageYAML(t *testing.T) {
	out, err := captureStdout(t, func() error { return outputPDBCoverageYAML(sampleResult()) })
	if err != nil {
		t.Fatalf("YAML output error: %v", err)
	}
	for _, want := range []string{"worker", "api", "withoutPdb", "withPdb", "apps"} {
		if !strings.Contains(out, want) {
			t.Errorf("YAML output missing %q: %q", want, out)
		}
	}
}

func TestOutputPDBCoverageTable(t *testing.T) {
	// Note: pterm table rows are rendered directly to the original os.Stdout file handle
	// (not the captured pipe), so we can only assert on ui.Outf header/footer content.
	out, err := captureStdout(t, func() error {
		return outputPDBCoverageTable(sampleResult(), time.Second)
	})
	if err != nil {
		t.Fatalf("table output error: %v", err)
	}
	for _, want := range []string{"PDB Coverage", "missing PDBs", "total deployments"} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q: %q", want, out)
		}
	}
	// Summary numbers should reflect the sample data (1 with PDB, 1 without).
	if !strings.Contains(out, "1 missing PDBs") {
		t.Errorf("summary count wrong: %q", out)
	}
}

func TestOutputPDBCoverageTable_Empty(t *testing.T) {
	// For empty results, the header is still printed via ui.Outf but the summary
	// line ("N missing PDBs out of M total deployments") must NOT appear because
	// TotalDeployments == 0.
	out, err := captureStdout(t, func() error {
		return outputPDBCoverageTable(workloads.PDBCoverageResult{}, time.Second)
	})
	if err != nil {
		t.Fatalf("empty table error: %v", err)
	}
	if !strings.Contains(out, "PDB Coverage") {
		t.Errorf("empty table: expected header 'PDB Coverage', got %q", out)
	}
	if strings.Contains(out, "missing PDBs") {
		t.Errorf("empty table: unexpected summary line in output: %q", out)
	}
}
