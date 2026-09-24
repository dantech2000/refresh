package common

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// unretriedAWSCallAllowlist names the functions that may make an AWS call
// outside WithRetry, keyed "<path from the module root>:<function>". A method
// is keyed "Type.Method"; a call inside a function literal counts toward the
// top-level declaration that holds it. Every entry says why the call is safe
// without WithRetry.
var unretriedAWSCallAllowlist = map[string]string{
	// Status polls: pollUntil reads again on the next tick, and pollError
	// keeps polling through a transient error (keepPolling) and fails at
	// once on a permanent one. WithRetry would only add a second backoff.
	"internal/services/addons/version_analyzer.go:ServiceImpl.WaitUntilActive":    "poll loop that already polls through transient errors",
	"internal/services/addons/version_analyzer.go:ServiceImpl.waitForAddonUpdate": "poll loop that already polls through transient errors",
}

// TestAWSCallsAreRetried fails when non-test code calls an AWS SDK
// operation outside WithRetry.
//
// The rule: a call is an AWS call when the callee is a method named Op whose
// signature takes a context.Context and then a pointer to OpInput from an
// aws-sdk-go-v2/service package, such as
// DescribeNodegroup(ctx, *eks.DescribeNodegroupInput, ...). That covers a
// concrete SDK client and any local interface that wraps one. It skips
// refresh's own helpers, whose parameters or names differ (as with
// waitForUpdate(ctx, *eks.DescribeUpdateInput, ...)), the mock function
// fields (DescribeNodegroupFn), and SDK waiters, which retry on their own. Such a call
// must sit inside a function literal passed to common.WithRetry, or to the
// page function of ListAllPages (internal/aws or internal/aws/awserr), which
// retries each page. The only other exceptions are the entries of
// unretriedAWSCallAllowlist.
//
// The check is typed: it loads each refresh package's dependencies from the
// build cache with `go list -export` and type-checks the non-test files.
func TestAWSCallsAreRetried(t *testing.T) {
	if testing.Short() {
		t.Skip("type-checks every package; skipped with -short")
	}
	root := moduleRoot(t)
	pkgs := listExportPackages(t, root)
	fset := token.NewFileSet()
	imp := exportImporter(fset, pkgs)

	used := make(map[string]bool)
	var violations []string
	for _, p := range pkgs {
		if p.Module == nil || !p.Module.Main || len(p.GoFiles) == 0 {
			continue
		}
		files := make([]*ast.File, 0, len(p.GoFiles))
		for _, name := range p.GoFiles {
			f, err := parser.ParseFile(fset, filepath.Join(p.Dir, name), nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, f)
		}
		info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Uses: map[*ast.Ident]types.Object{}}
		conf := types.Config{Importer: imp}
		if _, err := conf.Check(p.ImportPath, fset, files, info); err != nil {
			t.Fatalf("type-checking %s: %v", p.ImportPath, err)
		}
		for _, f := range files {
			rel, err := filepath.Rel(root, fset.File(f.Pos()).Name())
			if err != nil {
				t.Fatal(err)
			}
			rel = filepath.ToSlash(rel)
			for _, decl := range f.Decls {
				// A function or method, or a package-level declaration
				// whose value holds a function literal (a test seam such
				// as dryrunDescribeCluster), keyed "var".
				name := "var"
				if fn, ok := decl.(*ast.FuncDecl); ok {
					name = funcDeclName(fn)
				}
				key := rel + ":" + name
				for _, call := range unretriedAWSCalls(info, decl) {
					if _, ok := unretriedAWSCallAllowlist[key]; ok {
						used[key] = true
						continue
					}
					violations = append(violations, fmt.Sprintf("%s: %s calls %s outside common.WithRetry",
						relPosition(root, fset.Position(call.Pos())), name, calleeName(call)))
				}
			}
		}
	}

	sort.Strings(violations)
	for _, v := range violations {
		t.Error(v)
	}
	if len(violations) > 0 {
		t.Log("Wrap each AWS call in common.WithRetry (or a ListAllPages page function); " +
			"if the call is safe without it, add the function to unretriedAWSCallAllowlist with the reason.")
	}
	for key := range unretriedAWSCallAllowlist {
		if !used[key] {
			t.Errorf("unretriedAWSCallAllowlist entry %q matches no unretried AWS call; remove it", key)
		}
	}
}

// awsCallGuardFixture shows what TestAWSCallsAreRetried flags: the calls in
// bare and concrete, and nothing else.
const awsCallGuardFixture = `package fixture

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/eks"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/common"
)

type api interface {
	DescribeCluster(ctx context.Context, in *eks.DescribeClusterInput, optFns ...func(*eks.Options)) (*eks.DescribeClusterOutput, error)
	ListClusters(ctx context.Context, in *eks.ListClustersInput, optFns ...func(*eks.Options)) (*eks.ListClustersOutput, error)
}

type svc struct{ api api }

func (s svc) waitForUpdate(ctx context.Context, in *eks.DescribeUpdateInput) {}

func bare(ctx context.Context, c api) { _, _ = c.DescribeCluster(ctx, nil) }

func concrete(ctx context.Context, c *eks.Client) { _, _ = c.ListClusters(ctx, &eks.ListClustersInput{}) }

func retried(ctx context.Context, c api) {
	_, _ = common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeClusterOutput, error) {
		return c.DescribeCluster(rc, nil)
	})
}

func paged(ctx context.Context, c api) {
	_, _ = awsinternal.ListAllPages(ctx, "listing clusters",
		func(rc context.Context, token *string) (*eks.ListClustersOutput, error) {
			return c.ListClusters(rc, &eks.ListClustersInput{NextToken: token})
		},
		func(out *eks.ListClustersOutput) ([]string, *string) { return out.Clusters, out.NextToken })
}

func helper(ctx context.Context, s svc) { s.waitForUpdate(ctx, &eks.DescribeUpdateInput{}) }
`

// TestAWSCallGuardFixture checks the guard's rule on awsCallGuardFixture.
func TestAWSCallGuardFixture(t *testing.T) {
	if testing.Short() {
		t.Skip("needs go list -export; skipped with -short")
	}
	fset := token.NewFileSet()
	imp := exportImporter(fset, listExportPackages(t, moduleRoot(t)))
	f, err := parser.ParseFile(fset, "fixture.go", awsCallGuardFixture, 0)
	if err != nil {
		t.Fatal(err)
	}
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Uses: map[*ast.Ident]types.Object{}}
	if _, err := (&types.Config{Importer: imp}).Check("fixture", fset, []*ast.File{f}, info); err != nil {
		t.Fatal(err)
	}
	var flagged []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		for _, call := range unretriedAWSCalls(info, fn) {
			flagged = append(flagged, fn.Name.Name+":"+calleeName(call))
		}
	}
	if got, want := strings.Join(flagged, ","), "bare:DescribeCluster,concrete:ListClusters"; got != want {
		t.Errorf("flagged %q, want %q", got, want)
	}
}

// exportImporter imports packages from the export data go list reported.
func exportImporter(fset *token.FileSet, pkgs []listedPackage) types.Importer {
	exports := make(map[string]string, len(pkgs))
	for _, p := range pkgs {
		if p.Export != "" {
			exports[p.ImportPath] = p.Export
		}
	}
	return importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		f, ok := exports[path]
		if !ok {
			return nil, fmt.Errorf("no export data for %s", path)
		}
		return os.Open(f)
	})
}

// unretriedAWSCalls returns the AWS calls in node that no WithRetry or
// ListAllPages function literal encloses.
func unretriedAWSCalls(info *types.Info, node ast.Node) []*ast.CallExpr {
	var out []*ast.CallExpr
	var stack []ast.Node
	ast.Inspect(node, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		if call, ok := n.(*ast.CallExpr); ok && isAWSCall(info, call) && !insideRetry(info, stack) {
			out = append(out, call)
		}
		stack = append(stack, n)
		return true
	})
	return out
}

// isAWSCall reports whether call calls a method named Op that takes
// (context.Context, *<aws-sdk-go-v2/service/...>.OpInput, ...).
func isAWSCall(info *types.Info, call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	sig, ok := info.Types[call.Fun].Type.(*types.Signature)
	if !ok || sig.Params().Len() < 2 {
		return false
	}
	if named, ok := sig.Params().At(0).Type().(*types.Named); !ok ||
		named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != "context" || named.Obj().Name() != "Context" {
		return false
	}
	ptr, ok := sig.Params().At(1).Type().(*types.Pointer)
	if !ok {
		return false
	}
	named, ok := ptr.Elem().(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return false
	}
	return strings.HasPrefix(named.Obj().Pkg().Path(), "github.com/aws/aws-sdk-go-v2/service/") &&
		named.Obj().Name() == sel.Sel.Name+"Input"
}

// insideRetry reports whether a function literal in stack (the ancestors of
// a call, outermost first) is an argument of a WithRetry or ListAllPages
// call.
func insideRetry(info *types.Info, stack []ast.Node) bool {
	for i := len(stack) - 1; i > 0; i-- {
		lit, ok := stack[i].(*ast.FuncLit)
		if !ok {
			continue
		}
		parent, ok := stack[i-1].(*ast.CallExpr)
		if !ok || !isRetryFunc(info, parent) {
			continue
		}
		for _, arg := range parent.Args {
			if arg == lit {
				return true
			}
		}
	}
	return false
}

// isRetryFunc reports whether call calls common.WithRetry or a
// ListAllPages (internal/aws or internal/aws/awserr).
func isRetryFunc(info *types.Info, call *ast.CallExpr) bool {
	fun := call.Fun
	switch f := fun.(type) {
	case *ast.IndexExpr:
		fun = f.X
	case *ast.IndexListExpr:
		fun = f.X
	}
	var id *ast.Ident
	switch f := fun.(type) {
	case *ast.Ident:
		id = f
	case *ast.SelectorExpr:
		id = f.Sel
	default:
		return false
	}
	obj, ok := info.Uses[id].(*types.Func)
	if !ok || obj.Pkg() == nil {
		return false
	}
	const module = "github.com/dantech2000/refresh/internal/"
	switch obj.Pkg().Path() + "." + obj.Name() {
	case module + "common.WithRetry", module + "aws.ListAllPages", module + "aws/awserr.ListAllPages":
		return true
	}
	return false
}

// funcDeclName is "Name" for a function and "Type.Name" for a method.
func funcDeclName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	recv := fn.Recv.List[0].Type
	for {
		switch r := recv.(type) {
		case *ast.StarExpr:
			recv = r.X
			continue
		case *ast.IndexExpr:
			recv = r.X
			continue
		case *ast.IndexListExpr:
			recv = r.X
			continue
		case *ast.Ident:
			return r.Name + "." + fn.Name.Name
		}
		return fn.Name.Name
	}
}

// calleeName is the called operation, for the message. isAWSCall has
// checked that call.Fun is a selector.
func calleeName(call *ast.CallExpr) string {
	return call.Fun.(*ast.SelectorExpr).Sel.Name
}

func relPosition(root string, pos token.Position) string {
	if rel, err := filepath.Rel(root, pos.Filename); err == nil {
		pos.Filename = filepath.ToSlash(rel)
	}
	return pos.String()
}

// moduleRoot walks up from the package directory to the go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

type listedPackage struct {
	ImportPath string
	Dir        string
	GoFiles    []string
	Export     string
	Module     *struct{ Main bool }
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// listExportPackages runs `go list -export -deps` for the module. It prefers
// the go command under $GOROOT, which `go test` sets to the toolchain that
// built this test, so the export data matches the importer.
func listExportPackages(t *testing.T, root string) []listedPackage {
	t.Helper()
	goBin := "go"
	if goroot := os.Getenv("GOROOT"); goroot != "" {
		if bin := filepath.Join(goroot, "bin", "go"); fileExists(bin) {
			goBin = bin
		}
	}
	cmd := exec.CommandContext(t.Context(), goBin, "list", "-export", "-deps", "-json=ImportPath,Dir,GoFiles,Export,Module", "./...")
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, stderr.String())
	}
	var pkgs []listedPackage
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p listedPackage
		if err := dec.Decode(&p); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatal(err)
		}
		pkgs = append(pkgs, p)
	}
	return pkgs
}
