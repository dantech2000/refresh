package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/commands/documents"
	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
)

// schemaDir holds the committed schemas, relative to this package.
const schemaDir = "../../../docs/schema/v1"

// schemaCase is one command run whose -o json and -o yaml documents must
// match the schema of kind.
type schemaCase struct {
	name  string
	kind  apidoc.Kind
	world []*fakeaws.Cluster
	setup func(*fakeaws.Server)
	// args is the command line after "refresh", without -o.
	args []string
}

func fullCluster() *fakeaws.Cluster {
	return &fakeaws.Cluster{
		Name:       "prod",
		Version:    "1.31",
		Nodegroups: []*fakeaws.Nodegroup{{Name: "web", Version: "1.31"}, {Name: "busy", Version: "1.31", Status: "UPDATING"}},
		Addons:     []*fakeaws.Addon{{Name: "vpc-cni", Version: "v1.18.0", Available: []string{"v1.19.0", "v1.18.0"}}},
		Insights:   []*fakeaws.Insight{{ID: "insight-skew", Name: "Kubelet version skew", Status: "WARNING"}},
	}
}

// calmCluster is fullCluster with no nodegroup UPDATING, for the commands
// that refuse to start a change on a busy cluster.
func calmCluster() *fakeaws.Cluster {
	c := fullCluster()
	c.Nodegroups = c.Nodegroups[:1]
	return c
}

// schemaCases are the successful runs, one or more per kind. The failure
// runs come from the failure contract's cases.
var schemaCases = []schemaCase{
	{name: "status", kind: apidoc.KindFleetStatus, args: []string{"status", "-r", "us-east-1"}},
	{name: "cluster list", kind: apidoc.KindClusterList, args: []string{"cluster", "list", "-r", "us-east-1"}},
	{name: "cluster describe", kind: apidoc.KindClusterDescription, args: []string{"cluster", "describe", "prod"}},
	{name: "cluster describe --show-health", kind: apidoc.KindClusterDescription, args: []string{"cluster", "describe", "prod", "--show-health"}},
	{name: "cluster upgrade-check", kind: apidoc.KindUpgradeCheck, args: []string{"cluster", "upgrade-check", "prod", "--exit-zero"}},
	{name: "cluster upgrade-check --id", kind: apidoc.KindInsightDescription, args: []string{"cluster", "upgrade-check", "prod", "--id", "insight-skew", "--exit-zero"}},
	{name: "cluster upgrade --dry-run", kind: apidoc.KindUpgradePlan, args: []string{"cluster", "upgrade", "prod", "--to", "1.32", "--dry-run", "--skip-insights-check"}},
	{name: "cluster upgrade --yes", kind: apidoc.KindUpgradeRun, args: []string{"cluster", "upgrade", "prod", "--to", "1.32", "--yes", "--skip-insights-check", "--skip", "vpc-cni", "--skip-nodegroup", "busy", "--poll-interval", "5ms"}},
	{name: "nodegroup list", kind: apidoc.KindNodegroupList, args: []string{"nodegroup", "list", "prod"}},
	{name: "nodegroup describe", kind: apidoc.KindNodegroupDescription, args: []string{"nodegroup", "describe", "prod", "-n", "web"}},
	{name: "nodegroup update", kind: apidoc.KindNodegroupUpdate, args: []string{"nodegroup", "update", "prod", "--skip-health-check", "--yes", "--poll-interval", "5ms"}},
	{
		name: "nodegroup update stopped by the health gate", kind: apidoc.KindNodegroupUpdate,
		world: []*fakeaws.Cluster{prod(&fakeaws.Nodegroup{Name: "web", Version: "1.31", Status: "DEGRADED"})},
		args:  []string{"nodegroup", "update", "prod", "web", "--yes"},
	},
	{name: "nodegroup update --dry-run", kind: apidoc.KindNodegroupUpdatePlan, args: []string{"nodegroup", "update", "prod", "--dry-run"}},
	{name: "nodegroup update --all-clusters", kind: apidoc.KindFleetUpdate, args: []string{"nodegroup", "update", "--all-clusters", "-r", "us-east-1", "--skip-health-check", "--yes", "--poll-interval", "5ms"}},
	{name: "nodegroup update --all-clusters --dry-run", kind: apidoc.KindFleetUpdatePlan, args: []string{"nodegroup", "update", "--all-clusters", "-r", "us-east-1", "--dry-run"}},
	{name: "nodegroup update --health-only", kind: apidoc.KindHealthSummary, args: []string{"nodegroup", "update", "prod", "--health-only"}},
	{name: "addon list", kind: apidoc.KindAddonList, args: []string{"addon", "list", "prod", "--show-health"}},
	{name: "addon describe", kind: apidoc.KindAddonDescription, args: []string{"addon", "describe", "prod", "vpc-cni"}},
	{name: "addon update", kind: apidoc.KindAddonUpdate, world: []*fakeaws.Cluster{calmCluster()}, args: []string{"addon", "update", "prod", "vpc-cni", "--yes", "--wait"}},
	{name: "addon update --dry-run", kind: apidoc.KindAddonUpdate, args: []string{"addon", "update", "prod", "vpc-cni", "--dry-run"}},
	{name: "addon update --all", kind: apidoc.KindAddonUpdateAll, world: []*fakeaws.Cluster{calmCluster()}, args: []string{"addon", "update", "prod", "--all", "--yes"}},
}

// emptyCluster has no nodegroups, add-ons, tags, or VPC details, so every
// list a command reads from it is empty.
func emptyCluster() *fakeaws.Cluster { return &fakeaws.Cluster{Name: "prod", Version: "1.31"} }

// emptyCases read nothing, or empty lists. The schemas reject null, so these
// runs check that an empty list prints [] and that data a command did not
// collect is left out.
var emptyCases = []schemaCase{
	{name: "status with no clusters", kind: apidoc.KindFleetStatus, world: []*fakeaws.Cluster{}, args: []string{"status", "-r", "us-east-1"}},
	{name: "cluster list with no clusters", kind: apidoc.KindClusterList, world: []*fakeaws.Cluster{}, args: []string{"cluster", "list", "-r", "us-east-1"}},
	{name: "fleet update with no clusters", kind: apidoc.KindFleetUpdate, world: []*fakeaws.Cluster{}, args: []string{"nodegroup", "update", "--all-clusters", "-r", "us-east-1", "--skip-health-check", "--yes"}},
	{name: "fleet dry run with no clusters", kind: apidoc.KindFleetUpdatePlan, world: []*fakeaws.Cluster{}, args: []string{"nodegroup", "update", "--all-clusters", "-r", "us-east-1", "--dry-run"}},
	{name: "cluster describe --detailed of an empty cluster", kind: apidoc.KindClusterDescription, world: []*fakeaws.Cluster{emptyCluster()}, args: []string{"cluster", "describe", "prod", "--detailed"}},
	{name: "cluster describe --no-addons", kind: apidoc.KindClusterDescription, world: []*fakeaws.Cluster{emptyCluster()}, args: []string{"cluster", "describe", "prod", "--no-addons", "--no-health"}},
	{name: "cluster upgrade-check of an empty cluster", kind: apidoc.KindUpgradeCheck, world: []*fakeaws.Cluster{emptyCluster()}, args: []string{"cluster", "upgrade-check", "prod", "--exit-zero"}},
	{name: "cluster upgrade --dry-run to the current version", kind: apidoc.KindUpgradePlan, world: []*fakeaws.Cluster{emptyCluster()}, args: []string{"cluster", "upgrade", "prod", "--to", "1.31", "--dry-run"}},
	{name: "nodegroup describe --show-instances", kind: apidoc.KindNodegroupDescription, args: []string{"nodegroup", "describe", "prod", "-n", "web", "--show-instances"}},
	{name: "nodegroup list of an empty cluster", kind: apidoc.KindNodegroupList, world: []*fakeaws.Cluster{emptyCluster()}, args: []string{"nodegroup", "list", "prod"}},
	{name: "addon list of an empty cluster", kind: apidoc.KindAddonList, world: []*fakeaws.Cluster{emptyCluster()}, args: []string{"addon", "list", "prod"}},
	{name: "addon update --all of an empty cluster", kind: apidoc.KindAddonUpdateAll, world: []*fakeaws.Cluster{emptyCluster()}, args: []string{"addon", "update", "prod", "--all", "--yes"}},
}

// failureKinds maps each failure contract case (by the command it runs) to
// the kind of its document.
func failureKind(args []string) (apidoc.Kind, bool) {
	line := strings.Join(args, " ")
	switch {
	case strings.HasPrefix(line, "nodegroup update --all-clusters") && slices.Contains(args, "--dry-run"):
		return apidoc.KindFleetUpdatePlan, true
	case strings.HasPrefix(line, "nodegroup update --all-clusters"):
		return apidoc.KindFleetUpdate, true
	case strings.HasPrefix(line, "nodegroup update") && slices.Contains(args, "--dry-run"):
		return apidoc.KindNodegroupUpdatePlan, true
	case strings.HasPrefix(line, "nodegroup update"):
		return apidoc.KindNodegroupUpdate, true
	case strings.HasPrefix(line, "addon update") && slices.Contains(args, "--all"):
		return apidoc.KindAddonUpdateAll, true
	case strings.HasPrefix(line, "addon update"):
		return apidoc.KindAddonUpdate, true
	case strings.HasPrefix(line, "cluster upgrade") && slices.Contains(args, "--dry-run"):
		return apidoc.KindUpgradePlan, true
	case strings.HasPrefix(line, "cluster upgrade"):
		return apidoc.KindUpgradeRun, true
	}
	return "", false
}

// readFailureCases are read commands with a fault, so that the schemas are
// also checked against documents that have failures and incomplete rows.
var readFailureCases = []schemaCase{
	{
		name: "status with a region that can't be listed", kind: apidoc.KindFleetStatus,
		setup: func(s *fakeaws.Server) {
			s.FailRegions(func(r string) string {
				if r == "eu-west-1" {
					return "AccessDeniedException"
				}
				return ""
			})
		},
		args: []string{"status", "-r", "us-east-1", "-r", "eu-west-1"},
	},
	{
		name: "cluster list with a nodegroup list that can't be read", kind: apidoc.KindClusterList,
		world: []*fakeaws.Cluster{{Name: "prod", Version: "1.31", ListNodegroupsError: "AccessDeniedException"}},
		args:  []string{"cluster", "list", "-r", "us-east-1"},
	},
	{
		name: "cluster describe with a nodegroup that can't be read", kind: apidoc.KindClusterDescription,
		world: []*fakeaws.Cluster{prod(&fakeaws.Nodegroup{Name: "web", Version: "1.31", DescribeNodegroupError: "AccessDeniedException"})},
		args:  []string{"cluster", "describe", "prod"},
	},
	{
		name: "nodegroup list with a nodegroup that can't be read", kind: apidoc.KindNodegroupList,
		world: []*fakeaws.Cluster{prod(&fakeaws.Nodegroup{Name: "web", Version: "1.31", DescribeNodegroupError: "AccessDeniedException"})},
		args:  []string{"nodegroup", "list", "prod"},
	},
	{
		name: "addon list with an add-on that can't be read", kind: apidoc.KindAddonList,
		world: []*fakeaws.Cluster{withAddons(&fakeaws.Addon{Name: "vpc-cni", Version: "v1.18.0", DescribeAddonError: "AccessDeniedException"})},
		args:  []string{"addon", "list", "prod"},
	},
	{
		name: "upgrade-check with an add-on that can't be read", kind: apidoc.KindUpgradeCheck,
		world: []*fakeaws.Cluster{withAddons(&fakeaws.Addon{Name: "vpc-cni", Version: "v1.18.0", DescribeAddonError: "AccessDeniedException"})},
		args:  []string{"cluster", "upgrade-check", "prod", "--exit-zero"},
	},
}

// TestDocumentsMatchSchemas runs each command with -o json and -o yaml and
// checks the document against the committed schema of its kind, with
// unknown properties rejected, so a field the schema does not describe
// fails too. The YAML document must have the same structure as the JSON
// one. Every kind must be covered.
func TestDocumentsMatchSchemas(t *testing.T) {
	schemas := compileSchemas(t)
	all := slices.Concat(schemaCases, emptyCases, readFailureCases)
	for _, tc := range cases {
		if kind, ok := failureKind(tc.args); ok && slices.Contains(tc.formats, "json") {
			all = append(all, schemaCase{name: "failure: " + tc.name, kind: kind, world: tc.world, setup: tc.setup, args: tc.args})
		}
	}

	covered := map[apidoc.Kind]bool{}
	for _, tc := range all {
		t.Run(tc.name, func(t *testing.T) {
			jsonDoc := runDocument(t, tc, "json")
			yamlDoc := runDocument(t, tc, "yaml")

			m, ok := jsonDoc.(map[string]any)
			if !ok {
				t.Fatalf("the document is not an object: %v", jsonDoc)
			}
			if m["apiVersion"] != apidoc.APIVersion || m["kind"] != string(tc.kind) {
				t.Fatalf("apiVersion, kind = %v, %v; want %s, %s", m["apiVersion"], m["kind"], apidoc.APIVersion, tc.kind)
			}
			sch := schemas[tc.kind]
			if err := sch.Validate(jsonDoc); err != nil {
				t.Errorf("the -o json document does not match %s.json:\n%v", tc.kind, err)
			}
			if err := sch.Validate(yamlDoc); err != nil {
				t.Errorf("the -o yaml document does not match %s.json:\n%v", tc.kind, err)
			}
			if got, want := maskVolatile(yamlDoc), maskVolatile(jsonDoc); !reflect.DeepEqual(got, want) {
				t.Errorf("the -o yaml document differs from -o json:\n yaml=%v\n json=%v", got, want)
			}
			covered[tc.kind] = true
		})
	}
	for _, k := range apidoc.Kinds() {
		if !covered[k] {
			t.Errorf("no command run checks the %s document against its schema; add a schemaCase", k)
		}
	}
}

// runDocument runs tc with -o format and returns its one document, in the
// JSON data model (YAML is converted through JSON).
func runDocument(t *testing.T, tc schemaCase, format string) any {
	t.Helper()
	world := tc.world
	if world == nil {
		world = []*fakeaws.Cluster{fullCluster()}
	}
	srv := fakeaws.New(t, cloneWorld(world)...)
	if tc.setup != nil {
		tc.setup(srv)
	}
	args := append(append([]string{"refresh"}, tc.args...), "-o", format)
	stdout, stderr, err := fakeaws.Run(t, app(), args...)
	if strings.TrimSpace(stdout) == "" {
		t.Fatalf("%s: no document (exit %d, err %v)\nstderr:\n%s", format, runner.ExitCodeOf(err), err, stderr)
	}
	if format == "json" {
		var doc any
		if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
			t.Fatalf("json: %v", err)
		}
		return doc
	}
	var doc any
	if err := yaml.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("yaml: %v", err)
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("yaml document is not JSON-compatible: %v", err)
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// timestamp matches the RFC 3339 times in a document, which differ between
// two runs.
var timestamp = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$`)

// maskVolatile replaces the values that differ between two runs of the
// same command: timestamps and the error text of a failure (it can carry a
// request ID).
func maskVolatile(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = maskVolatile(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = maskVolatile(val)
		}
		return out
	case string:
		if timestamp.MatchString(x) {
			return "<time>"
		}
	}
	return v
}

// compileSchemas compiles every committed schema in strict mode: an object
// that lists its properties rejects any other property. The published
// schemas allow unknown properties (a later v1 release may add fields);
// the test does not, so a document field the schema lacks fails here.
func compileSchemas(t *testing.T) map[apidoc.Kind]*jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	for _, doc := range documents.All() {
		kind := doc.DocumentKind()
		f, err := os.Open(filepath.Join(schemaDir, string(kind)+".json"))
		if err != nil {
			t.Fatalf("%v (run task docs:gen)", err)
		}
		s, err := jsonschema.UnmarshalJSON(f)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if err := c.AddResource(apidoc.SchemaBaseURL+string(kind)+".json", closeObjects(s)); err != nil {
			t.Fatal(err)
		}
	}
	out := map[apidoc.Kind]*jsonschema.Schema{}
	for _, k := range apidoc.Kinds() {
		sch, err := c.Compile(apidoc.SchemaBaseURL + string(k) + ".json")
		if err != nil {
			t.Fatalf("compiling %s: %v", k, err)
		}
		out[k] = sch
	}
	return out
}

// closeObjects sets additionalProperties to false on every schema object
// that has properties and no additionalProperties.
func closeObjects(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			x[k] = closeObjects(val)
		}
		if _, ok := x["properties"]; ok {
			if _, set := x["additionalProperties"]; !set {
				x["additionalProperties"] = false
			}
		}
	case []any:
		for i, val := range x {
			x[i] = closeObjects(val)
		}
	}
	return v
}

// TestSchemaFilesMatchKinds checks that every kind has exactly one document
// type and one committed schema file, and that no schema file is left over.
// Whether a file is up to date is `task docs:check`.
func TestSchemaFilesMatchKinds(t *testing.T) {
	want := map[string]bool{}
	seen := map[apidoc.Kind]bool{}
	for _, doc := range documents.All() {
		k := doc.DocumentKind()
		if seen[k] {
			t.Errorf("two document types have kind %s", k)
		}
		seen[k] = true
		want[string(k)+".json"] = true
	}
	for _, k := range apidoc.Kinds() {
		if !seen[k] {
			t.Errorf("no document type has kind %s; add it to its command package's Documents()", k)
		}
	}
	files, err := filepath.Glob(filepath.Join(schemaDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range files {
		got = append(got, filepath.Base(f))
	}
	for name := range want {
		if !slices.Contains(got, name) {
			t.Errorf("docs/schema/v1/%s is missing (run task docs:gen)", name)
		}
	}
	for _, name := range got {
		if !want[name] {
			t.Errorf("docs/schema/v1/%s has no document kind (run task docs:gen)", name)
		}
	}
}
