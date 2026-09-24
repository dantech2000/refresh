package nodegroup

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/services/addons"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
	"github.com/dantech2000/refresh/internal/services/status"
	"github.com/dantech2000/refresh/internal/services/upgrade"
)

const modulePath = "github.com/dantech2000/refresh/"

// outputRootTypes are the types passed (directly, or as slice/map elements)
// to runner.EncodeStdout. Every struct reachable from them is an output type.
var outputRootTypes = []reflect.Type{
	reflect.TypeFor[clustersvc.ClusterList](),
	reflect.TypeFor[clustersvc.ClusterSummary](),
	reflect.TypeFor[clustersvc.ClusterDetails](),
	reflect.TypeFor[clustersvc.UpgradeReport](),
	reflect.TypeFor[clustersvc.InsightDetail](),
	reflect.TypeFor[upgrade.Plan](),
	reflect.TypeFor[upgrade.Report](),
	reflect.TypeFor[nodegroupsvc.NodegroupList](),
	reflect.TypeFor[nodegroupsvc.NodegroupSummary](),
	reflect.TypeFor[nodegroupsvc.NodegroupDetails](),
	reflect.TypeFor[health.HealthSummary](),
	reflect.TypeFor[addons.AddonList](),
	reflect.TypeFor[addons.AddonSummary](),
	reflect.TypeFor[addons.AddonDetails](),
	reflect.TypeFor[addons.AddonUpdateResult](),
	reflect.TypeFor[status.FleetStatus](),
	reflect.TypeFor[updateOutcomes](),
	reflect.TypeFor[updateDocument](),
	reflect.TypeFor[dryRunPlan](),
	reflect.TypeFor[clusterUpdateResult](),
	reflect.TypeFor[fleetDryRunResult](),
	reflect.TypeFor[regionDiscoveryError](),
}

var jsonMarshalerType = reflect.TypeFor[json.Marshaler]()

// TestOutputTypesHaveMatchingYAMLTags enforces the CLAUDE.md convention: every
// exported field of an output struct carries both a json and a yaml tag, and
// the two are identical, so a direct yaml.Marshal matches -o json.
func TestOutputTypesHaveMatchingYAMLTags(t *testing.T) {
	seen := map[reflect.Type]bool{}
	var problems []string
	var walk func(reflect.Type)
	walk = func(typ reflect.Type) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array || typ.Kind() == reflect.Map {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || seen[typ] || !strings.HasPrefix(typ.PkgPath(), modulePath) {
			return
		}
		seen[typ] = true
		if typ.Implements(jsonMarshalerType) || reflect.PointerTo(typ).Implements(jsonMarshalerType) {
			return
		}
		for i := range typ.NumField() {
			f := typ.Field(i)
			if !f.IsExported() {
				continue
			}
			name := typ.String() + "." + f.Name
			jsonTag, hasJSON := f.Tag.Lookup("json")
			yamlTag, hasYAML := f.Tag.Lookup("yaml")
			switch {
			case f.Anonymous && !hasJSON:
				// encoding/json inlines an untagged embedded struct;
				// yaml.v3 only does with ",inline".
				if yamlTag != ",inline" {
					problems = append(problems, name+`: untagged embedded struct needs yaml:",inline"`)
				}
			case !hasJSON:
				problems = append(problems, name+": missing json tag")
			case !hasYAML:
				problems = append(problems, fmt.Sprintf("%s: missing yaml tag (want yaml:%q)", name, jsonTag))
			case jsonTag != yamlTag:
				problems = append(problems, fmt.Sprintf("%s: json:%q != yaml:%q", name, jsonTag, yamlTag))
			}
			if jsonTag != "-" {
				walk(f.Type)
			}
		}
	}
	for _, root := range outputRootTypes {
		walk(root)
	}
	if len(seen) < len(outputRootTypes) {
		t.Fatalf("walked only %d types; roots must be module structs", len(seen))
	}
	for _, p := range problems {
		t.Error(p)
	}
}

var updateGolden = flag.Bool("update-golden", false, "rewrite testdata golden files")

// TestEncodeStdoutGolden pins the -o json and -o yaml bytes of a fully
// populated NodegroupDetails and ClusterDetails, so tag changes can't alter
// the machine output.
func TestEncodeStdoutGolden(t *testing.T) {
	payloads := map[string]any{
		"nodegroup_details": fillValue(reflect.TypeFor[nodegroupsvc.NodegroupDetails](), 0).Interface(),
		"cluster_details":   fillValue(reflect.TypeFor[clustersvc.ClusterDetails](), 0).Interface(),
	}
	for name, payload := range payloads {
		for _, format := range []string{"json", "yaml"} {
			t.Run(name+"."+format, func(t *testing.T) {
				got := encodeToString(t, format, payload)
				path := filepath.Join("testdata", name+"."+format+".golden")
				if *updateGolden {
					if err := os.MkdirAll("testdata", 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("reading golden (run with -update-golden to create): %v", err)
				}
				if got != string(want) {
					t.Errorf("%s output changed.\n--- got ---\n%s\n--- want ---\n%s", format, got, want)
				}
			})
		}
	}
}

func encodeToString(t *testing.T, format string, payload any) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	handled, encErr := runner.EncodeStdout(format, payload)
	os.Stdout = orig
	_ = w.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	if !handled || encErr != nil {
		t.Fatalf("EncodeStdout(%s): handled=%v err=%v", format, handled, encErr)
	}
	return buf.String()
}

// fillValue returns a deterministic, fully populated value of typ: every
// field set, every slice and map with one element, every pointer non-nil.
func fillValue(typ reflect.Type, depth int) reflect.Value {
	v := reflect.New(typ).Elem()
	if depth > 6 {
		return v
	}
	switch typ.Kind() {
	case reflect.String:
		v.SetString("s-" + strings.ToLower(typ.Name()))
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(int64(depth + 1))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(uint64(depth + 1))
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1.5)
	case reflect.Pointer:
		v.Set(fillValue(typ.Elem(), depth+1).Addr())
	case reflect.Slice:
		s := reflect.MakeSlice(typ, 1, 1)
		s.Index(0).Set(fillValue(typ.Elem(), depth+1))
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(typ)
		m.SetMapIndex(fillValue(typ.Key(), depth+1), fillValue(typ.Elem(), depth+1))
		v.Set(m)
	case reflect.Struct:
		if typ == reflect.TypeFor[time.Time]() {
			v.Set(reflect.ValueOf(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)))
			return v
		}
		for i := range typ.NumField() {
			if typ.Field(i).IsExported() {
				fv := fillValue(typ.Field(i).Type, depth+1)
				if typ.Field(i).Type.Kind() == reflect.String {
					fv.SetString("s-" + strings.ToLower(typ.Field(i).Name))
				}
				v.Field(i).Set(fv)
			}
		}
	}
	return v
}
