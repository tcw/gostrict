// Package contracts requires exported functions in //strict:contracts
// packages to state their preconditions with contract.Require.
package contracts

import (
	"go/ast"
	"go/constant"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/types/typeutil"

	"github.com/tcw/gostrict/internal/directive"
)

const doc = `require stated preconditions in //strict:contracts packages

In a package whose file header contains the line

	//strict:contracts

every exported function or method that takes parameters must begin with a
call to contract.Require, stating what callers must guarantee:

	func Slope(a, b Point) Percent {
		contract.Require(a != b, "a and b must be distinct points")
		...
	}

It also reports contract calls that state nothing: a condition that is the
constant true, or a message that is empty or not a constant string.
Suppress a single function with //strict:allow contracts <reason>.`

// Analyzer is the contracts analyzer.
//
//nolint:gochecknoglobals // analysis.Analyzer values are package-level by convention; drivers compare them by pointer.
var Analyzer = newAnalyzer()

type config struct {
	pkg string // import path of the contract package
}

func newAnalyzer() *analysis.Analyzer {
	cfg := &config{pkg: "github.com/tcw/gostrict/contract"}
	a := &analysis.Analyzer{Name: "contracts", Doc: doc}
	a.Flags.StringVar(&cfg.pkg, "pkg", cfg.pkg, "import path of the contract package")
	a.Run = func(pass *analysis.Pass) (any, error) { return run(pass, cfg) }
	return a
}

func run(pass *analysis.Pass, cfg *config) (any, error) {
	if !directive.Package(pass.Files, "contracts") {
		return nil, nil
	}
	for _, f := range pass.Files {
		if strings.HasSuffix(pass.Fset.Position(f.Pos()).Filename, "_test.go") {
			continue
		}
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil {
				checkFunc(pass, cfg, fd)
			}
		}
	}
	return nil, nil
}

func checkFunc(pass *analysis.Pass, cfg *config, fd *ast.FuncDecl) {
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if name := contractCall(pass, cfg, call); name != "" {
				checkCall(pass, call, name)
			}
		}
		return true
	})
	if !needsPrecondition(fd) || startsWithRequire(pass, cfg, fd) {
		return
	}
	if directive.Suppressed(pass, "contracts", fd.Doc) {
		return
	}
	pass.ReportRangef(fd.Name,
		"exported %s takes parameters but states no precondition; begin the body with contract.Require(cond, \"what callers must guarantee\"), or add //strict:allow contracts <reason>",
		fd.Name.Name)
}

func needsPrecondition(fd *ast.FuncDecl) bool {
	if !fd.Name.IsExported() || fd.Type.Params.NumFields() == 0 {
		return false
	}
	return fd.Recv == nil || exportedRecv(fd.Recv)
}

func startsWithRequire(pass *analysis.Pass, cfg *config, fd *ast.FuncDecl) bool {
	if len(fd.Body.List) == 0 {
		return false
	}
	es, ok := fd.Body.List[0].(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := es.X.(*ast.CallExpr)
	return ok && contractCall(pass, cfg, call) == "Require"
}

// contractCall returns the function name if call invokes a function of the
// contract package, or "".
func contractCall(pass *analysis.Pass, cfg *config, call *ast.CallExpr) string {
	fn, ok := typeutil.Callee(pass.TypesInfo, call).(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != cfg.pkg {
		return ""
	}
	switch fn.Name() {
	case "Require", "Ensure", "Invariant":
		return fn.Name()
	}
	return ""
}

func checkCall(pass *analysis.Pass, call *ast.CallExpr, name string) {
	if len(call.Args) != 2 {
		return
	}
	if tv, ok := pass.TypesInfo.Types[call.Args[0]]; ok && tv.Value != nil && tv.Value.Kind() == constant.Bool && constant.BoolVal(tv.Value) {
		pass.ReportRangef(call.Args[0], "contract.%s with a condition that is always true states nothing; write the real condition", name)
	}
	tv, ok := pass.TypesInfo.Types[call.Args[1]]
	if !ok || tv.Value == nil || tv.Value.Kind() != constant.String {
		pass.ReportRangef(call.Args[1], "contract.%s message must be a constant string, so the contract reads as documentation", name)
		return
	}
	if strings.TrimSpace(constant.StringVal(tv.Value)) == "" {
		pass.ReportRangef(call.Args[1], "contract.%s message is empty; say what must hold", name)
	}
}

// exportedRecv reports whether a method receiver's base type is exported.
func exportedRecv(fl *ast.FieldList) bool {
	if len(fl.List) == 0 {
		return false
	}
	e := fl.List[0].Type
	for {
		switch x := e.(type) {
		case *ast.StarExpr:
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.IndexListExpr:
			e = x.X
		case *ast.Ident:
			return x.IsExported()
		default:
			return false
		}
	}
}
