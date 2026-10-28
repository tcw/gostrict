// Package purity enforces that packages marked //strict:pure contain only
// deterministic, side-effect-free code: no I/O, no clock, no randomness, no
// goroutines and no mutable global state.
package purity

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"reflect"
	"strconv"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/types/typeutil"

	"github.com/tcw/gostrict/internal/directive"
)

const doc = `enforce side-effect-free code in //strict:pure packages

In a package whose file header contains the line

	//strict:pure

this analyzer reports:

  - imports of standard packages that do I/O, read ambient state or add
    concurrency (os, net, net/http, log, context, sync, math/rand, ...);
  - imports of non-standard packages that are not themselves marked
    //strict:pure (unless listed in -allow);
  - calls that read the clock or write to stdout (time.Now, fmt.Println, ...);
  - go statements;
  - package-level variables, except error sentinels and blank (_) assertions.

This approximates an effect system at package granularity: the pure core
holds the domain rules, and a thin shell package around it does the I/O.
Test files are not checked. Suppress a single finding with
//strict:allow purity <reason> in the doc comment of the declaration.`

// Analyzer is the purity analyzer.
//
//nolint:gochecknoglobals // analysis.Analyzer values are package-level by convention; drivers compare them by pointer.
var Analyzer = newAnalyzer()

type config struct {
	allow string // comma-separated import path prefixes treated as pure
}

func newAnalyzer() *analysis.Analyzer {
	cfg := &config{allow: "github.com/tcw/gostrict/contract"}
	a := &analysis.Analyzer{
		Name:       "purity",
		Doc:        doc,
		FactTypes:  []analysis.Fact{new(isPure)},
		ResultType: reflect.TypeFor[*directive.Used](),
	}
	a.Flags.StringVar(&cfg.allow, "allow", cfg.allow, "comma-separated import path prefixes to treat as pure (e.g. third-party geometry libraries)")
	a.Run = func(pass *analysis.Pass) (any, error) { return run(pass, cfg) }
	return a
}

// isPure is the package fact exported by every //strict:pure package, so
// that importing packages can verify their dependencies.
type isPure struct{}

func (*isPure) AFact()         {}
func (*isPure) String() string { return "pure" }

type forbidden struct {
	path   string
	prefix bool // also forbid sub-packages
	why    string
}

// forbiddenStd lists standard library packages a pure package may not import.
func forbiddenStd() []forbidden {
	return []forbidden{
		{"os", true, "file system, environment and process I/O"},
		{"net", false, "network I/O"},
		{"net/http", true, "network I/O"},
		{"net/rpc", true, "network I/O"},
		{"net/smtp", true, "network I/O"},
		{"net/textproto", true, "network I/O"},
		{"io/ioutil", true, "file system I/O"},
		{"io/fs", true, "file system I/O"},
		{"syscall", true, "system calls"},
		{"log", true, "logging is output"},
		{"database/sql", true, "database I/O"},
		{"math/rand", true, "nondeterminism"},
		{"crypto/rand", true, "nondeterminism"},
		{"context", true, "cancellation and deadlines belong to the shell"},
		{"sync", true, "shared mutable state and concurrency"},
		{"runtime", true, "runtime introspection"},
		{"unsafe", true, "unchecked memory access"},
		{"plugin", true, "dynamic loading"},
		{"expvar", true, "global mutable state"},
	}
}

// forbiddenCalls maps package path and function name to the reason it is
// impure.
func forbiddenCalls() map[string]map[string]string {
	clock := "reads the clock; take the time as a parameter"
	timer := "depends on wall-clock time; schedule work in the shell"
	stdout := "writes to stdout; return values (or take an io.Writer) instead"
	stdin := "reads stdin; take the input as a parameter"
	return map[string]map[string]string{
		"time": {
			"Now": clock, "Since": clock, "Until": clock,
			"Sleep": timer, "After": timer, "AfterFunc": timer, "Tick": timer,
			"NewTimer": timer, "NewTicker": timer,
		},
		"fmt": {
			"Print": stdout, "Printf": stdout, "Println": stdout,
			"Scan": stdin, "Scanf": stdin, "Scanln": stdin,
		},
	}
}

type checker struct {
	pass  *analysis.Pass
	used  *directive.Used
	allow []string
	calls map[string]map[string]string
}

func run(pass *analysis.Pass, cfg *config) (any, error) {
	used := directive.NewUsed()
	if !directive.Package(pass.Files, "pure") {
		return used, nil
	}
	pass.ExportPackageFact(new(isPure))
	c := &checker{pass: pass, used: used, calls: forbiddenCalls()}
	for _, p := range strings.Split(cfg.allow, ",") {
		if p = strings.TrimSpace(p); p != "" {
			c.allow = append(c.allow, p)
		}
	}
	for _, f := range pass.Files {
		if strings.HasSuffix(pass.Fset.Position(f.Pos()).Filename, "_test.go") {
			continue
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.GenDecl:
				c.genDecl(d)
			case *ast.FuncDecl:
				c.funcDecl(d)
			}
		}
	}
	return used, nil
}

func (c *checker) genDecl(gd *ast.GenDecl) {
	for _, s := range gd.Specs {
		if is, ok := s.(*ast.ImportSpec); ok {
			c.importSpec(gd, is)
		}
		if vs, ok := s.(*ast.ValueSpec); ok && gd.Tok == token.VAR {
			c.globalVar(gd, vs)
		}
	}
}

func (c *checker) importSpec(gd *ast.GenDecl, is *ast.ImportSpec) {
	path, err := strconv.Unquote(is.Path.Value)
	if err != nil || c.allowed(path) {
		return
	}
	var msg string
	if isStd(path) {
		for _, f := range forbiddenStd() {
			if path == f.path || (f.prefix && strings.HasPrefix(path, f.path+"/")) {
				msg = fmt.Sprintf("imports %q (%s); move this to the shell package and pass the data in as values", path, f.why)
				break
			}
		}
	} else if !c.importedIsPure(path) {
		msg = fmt.Sprintf("imports %q, which is not marked //strict:pure; mark it pure if it has no side effects, list it in -purity.allow, or move the dependency to the shell", path)
	}
	if msg == "" || c.used.Suppressed(c.pass, "purity", gd.Doc, is.Doc, is.Comment) {
		return
	}
	c.pass.ReportRangef(is, "package %s is //strict:pure but %s", c.pass.Pkg.Name(), msg)
}

func (c *checker) importedIsPure(path string) bool {
	for _, p := range c.pass.Pkg.Imports() {
		if p.Path() == path {
			return c.pass.ImportPackageFact(p, new(isPure))
		}
	}
	return false
}

func (c *checker) allowed(path string) bool {
	for _, a := range c.allow {
		if path == a || strings.HasPrefix(path, a+"/") {
			return true
		}
	}
	return false
}

// isStd reports whether path looks like a standard library package: its
// first element contains no dot.
func isStd(path string) bool {
	first, _, _ := strings.Cut(path, "/")
	return !strings.Contains(first, ".")
}

func (c *checker) globalVar(gd *ast.GenDecl, vs *ast.ValueSpec) {
	for _, id := range vs.Names {
		if id.Name == "_" {
			continue
		}
		obj := c.pass.TypesInfo.Defs[id]
		if obj == nil || isErrorType(obj.Type()) {
			continue
		}
		if c.used.Suppressed(c.pass, "purity", gd.Doc, vs.Doc, vs.Comment) {
			return
		}
		c.pass.ReportRangef(id, "package %s is //strict:pure but declares package-level var %s, which is mutable global state; use a const, a func returning the value, or pass it in", c.pass.Pkg.Name(), id.Name)
	}
}

func isErrorType(t types.Type) bool {
	errType, ok := types.Universe.Lookup("error").Type().Underlying().(*types.Interface)
	return ok && types.Implements(t, errType)
}

func (c *checker) funcDecl(fd *ast.FuncDecl) {
	if fd.Body == nil {
		return
	}
	suppressed := false
	report := func(n ast.Node, format string, args ...any) {
		if suppressed {
			return
		}
		if c.used.Suppressed(c.pass, "purity", fd.Doc) {
			suppressed = true
			return
		}
		c.pass.ReportRangef(n, "package %s is //strict:pure but %s %s", c.pass.Pkg.Name(), fd.Name.Name, fmt.Sprintf(format, args...))
	}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.GoStmt:
			report(n, "starts a goroutine; concurrency belongs in the shell, keep the core sequential")
		case *ast.CallExpr:
			c.call(n, report)
		}
		return true
	})
}

func (c *checker) call(call *ast.CallExpr, report func(ast.Node, string, ...any)) {
	switch obj := typeutil.Callee(c.pass.TypesInfo, call).(type) {
	case *types.Builtin:
		if obj.Name() == "print" || obj.Name() == "println" {
			report(call, "calls %s, which writes to stderr; return values instead", obj.Name())
		}
	case *types.Func:
		if obj.Pkg() == nil {
			return
		}
		if why, ok := c.calls[obj.Pkg().Path()][obj.Name()]; ok {
			report(call, "calls %s.%s, which %s", obj.Pkg().Name(), obj.Name(), why)
		}
	}
}
