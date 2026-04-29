package commands

import (
	"strings"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/services/workloads"
)

func samplePDBCoverageResult() workloads.PDBCoverageResult {
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

func TestWorkloadCommandStructure(t *testing.T) {
	cmd := WorkloadCommand()
	if cmd.Name != "workload" {
		t.Fatalf("WorkloadCommand name = %q", cmd.Name)
	}
	if findSub(cmd, "pdbs") == nil {
		t.Fatal("workload pdbs subcommand missing")
	}
	if findSub(cmd, "pdb") == nil {
		t.Fatal("workload pdb alias missing")
	}
}

func TestOutputPDBCoverageFormats(t *testing.T) {
	result := samplePDBCoverageResult()

	for _, fn := range []func(workloads.PDBCoverageResult) error{
		outputPDBCoverageJSON,
		outputPDBCoverageYAML,
	} {
		out, err := captureCommandStdout(t, func() error { return fn(result) })
		if err != nil {
			t.Fatalf("structured output: %v", err)
		}
		if !strings.Contains(out, "worker") || !strings.Contains(out, "withoutPdb") {
			t.Fatalf("structured output = %q", out)
		}
	}

	out, err := captureCommandStdout(t, func() error {
		return outputPDBCoverageTable(result, time.Second)
	})
	if err != nil {
		t.Fatalf("table output: %v", err)
	}
	if !strings.Contains(out, "PDB Coverage") || !strings.Contains(out, "missing PDBs") {
		t.Fatalf("table output = %q", out)
	}

	_, err = captureCommandStdout(t, func() error {
		return outputPDBCoverageTable(workloads.PDBCoverageResult{}, time.Second)
	})
	if err != nil {
		t.Fatalf("empty table output: %v", err)
	}
}
