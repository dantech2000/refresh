package cmdutil_test

import (
	"go/build"
	"strings"
	"testing"
)

// cmdutil holds the parts of a command line that know nothing about the
// fleet: the streams, the factory, the exit codes, the output flag, the
// prompts. A command that only prints its version must not link the code
// that reads clusters. What the fleet commands share is in
// internal/cmd/fleetcmd.
func TestCmdutilImportsNoFleetAndNoAWSService(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range pkg.Imports {
		if strings.HasSuffix(imp, "/internal/fleet") || strings.Contains(imp, "/internal/fleet/") {
			t.Errorf("cmdutil imports %s; the fleet belongs to the commands, in internal/cmd/fleetcmd", imp)
		}
		if strings.Contains(imp, "aws-sdk-go-v2/service/") {
			t.Errorf("cmdutil imports %s; an AWS service is reached through internal/awsx", imp)
		}
	}
}
