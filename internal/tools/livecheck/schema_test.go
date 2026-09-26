//go:build livecheck

package livecheck

import (
	"bytes"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

// TestLiveOutputMatchesSchemas runs the built binary against the account and
// validates each document against its published schema.
func TestLiveOutputMatchesSchemas(t *testing.T) {
	bin, _ := filepath.Abs("../../../dist/refresh")
	root, _ := filepath.Abs("../../../docs/schema/v1")
	for _, args := range [][]string{
		{"status", "-o", "json"},
		{"status", "-o", "yaml"},
		{"status", "-A", "-o", "json"},
		{"status", "-r", "us-east-1", "-r", "af-south-1", "-o", "json"},
		{"cluster", "list", "-o", "json"},
		{"cluster", "list", "-A", "-o", "yaml"},
		{"nodegroup", "update", "--all-clusters", "--dry-run", "-o", "json"},
		{"nodegroup", "update", "--all-clusters", "--dry-run", "-r", "us-east-1", "-r", "us-west-2", "-o", "yaml"},
	} {
		var stdout, stderr bytes.Buffer
		cmd := exec.CommandContext(t.Context(), bin, args...)
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		code := 0
		if ee := (*exec.ExitError)(nil); errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		var doc any
		if args[len(args)-1] == "yaml" {
			var y any
			if yerr := yaml.Unmarshal(stdout.Bytes(), &y); yerr != nil {
				t.Errorf("%v: yaml: %v\n%s", args, yerr, stdout.String())
				continue
			}
			b, _ := json.Marshal(y)
			doc, _ = jsonschema.UnmarshalJSON(bytes.NewReader(b))
		} else {
			var jerr error
			if doc, jerr = jsonschema.UnmarshalJSON(bytes.NewReader(stdout.Bytes())); jerr != nil {
				t.Errorf("%v: exit %d, stdout is not one JSON document: %v\n%s\nstderr: %s", args, code, jerr, stdout.String(), stderr.String())
				continue
			}
		}
		kind, _ := doc.(map[string]any)["kind"].(string)
		c := jsonschema.NewCompiler()
		sch, err := c.Compile(filepath.Join(root, kind+".json"))
		if err != nil {
			t.Errorf("%v: schema for kind %q: %v", args, kind, err)
			continue
		}
		if err := sch.Validate(doc); err != nil {
			t.Errorf("%v: %s does not match its schema: %v", args, kind, err)
			continue
		}
		t.Logf("exit %d  %-70s %s valid · stderr: %q", code, strings.Join(args, " "), kind, firstLine(stderr.String()))
	}
}

func firstLine(s string) string {
	if i := bytes.IndexByte([]byte(s), '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
