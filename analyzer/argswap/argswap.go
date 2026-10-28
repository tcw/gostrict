// Package argswap reports functions whose adjacent parameters share a
// predeclared type, because swapped arguments at a call site then compile
// without any error.
package argswap

import (
	"go/ast"
	"go/types"
	"reflect"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"

	"github.com/tcw/gostrict/internal/directive"
)

const doc = `report adjacent parameters that share a predeclared type

Go has no named arguments, so a call like Move(x, y, dist) compiles even when
x and y are swapped. This analyzer reports every run of two or more adjacent
parameters with the same predeclared type (int, float64, string, bool, ...).

Fix it by giving the parameters distinct defined types (type Meters float64,
type Degrees float64), or by taking a struct so call sites name each field.
Where order truly does not matter (Max(a, b int)), suppress it in the doc
comment with a reason:

	//strict:allow argswap commutative`

// Analyzer is the argswap analyzer.
//
//nolint:gochecknoglobals // analysis.Analyzer values are package-level by convention; drivers compare them by pointer.
var Analyzer = newAnalyzer()

type config struct {
	ignore string // comma-separated method names to skip
}

func newAnalyzer() *analysis.Analyzer {
	cfg := &config{ignore: "Less,Swap"}
	a := &analysis.Analyzer{
		Name:       "argswap",
		Doc:        doc,
		Requires:   []*analysis.Analyzer{inspect.Analyzer},
		ResultType: reflect.TypeFor[*directive.Used](),
	}
	a.Flags.StringVar(&cfg.ignore, "ignore", cfg.ignore, "comma-separated method names to skip (interface methods whose signature you do not control)")
	a.Run = func(pass *analysis.Pass) (any, error) { return run(pass, cfg) }
	return a
}

type param struct {
	name string
	typ  types.Type
	pos  ast.Node
}

func run(pass *analysis.Pass, cfg *config) (any, error) {
	ignored := map[string]bool{}
	for _, n := range strings.Split(cfg.ignore, ",") {
		if n = strings.TrimSpace(n); n != "" {
			ignored[n] = true
		}
	}
	used := directive.NewUsed()
	insp, ok := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	if !ok {
		return used, nil
	}
	insp.Preorder([]ast.Node{(*ast.FuncDecl)(nil)}, func(n ast.Node) {
		fd, ok := n.(*ast.FuncDecl)
		if !ok || isTestFile(pass, fd) {
			return
		}
		if fd.Recv != nil && ignored[fd.Name.Name] {
			return
		}
		params := flatten(pass, fd.Type.Params)
		for _, r := range runs(params) {
			if used.Suppressed(pass, "argswap", fd.Doc) {
				return
			}
			names := make([]string, len(r))
			for i, p := range r {
				names[i] = p.name
			}
			pass.ReportRangef(r[0].pos,
				"parameters %s of %s share type %s, so swapped arguments compile silently; give them distinct defined types (e.g. type Meters float64) or take a struct, or add //strict:allow argswap <reason>",
				strings.Join(names, ", "), fd.Name.Name, types.TypeString(r[0].typ, types.RelativeTo(pass.Pkg)))
		}
	})
	return used, nil
}

func isTestFile(pass *analysis.Pass, n ast.Node) bool {
	return strings.HasSuffix(pass.Fset.Position(n.Pos()).Filename, "_test.go")
}

// flatten expands a field list into one entry per parameter.
func flatten(pass *analysis.Pass, fl *ast.FieldList) []param {
	if fl == nil {
		return nil
	}
	var out []param
	for _, f := range fl.List {
		t := pass.TypesInfo.TypeOf(f.Type)
		if len(f.Names) == 0 {
			out = append(out, param{name: "_", typ: t, pos: f})
			continue
		}
		for _, id := range f.Names {
			out = append(out, param{name: id.Name, typ: t, pos: id})
		}
	}
	return out
}

// runs returns every maximal run of two or more adjacent parameters that
// share an identical swappable type.
func runs(ps []param) [][]param {
	var out [][]param
	for i := 0; i < len(ps); {
		j := i + 1
		for j < len(ps) && swappable(ps[i].typ) && types.Identical(ps[i].typ, ps[j].typ) {
			j++
		}
		if j-i >= 2 {
			out = append(out, ps[i:j])
		}
		i = j
	}
	return out
}

// swappable reports whether t is a predeclared boolean, numeric or string
// type. Defined types such as Meters are the remedy, so they never count.
func swappable(t types.Type) bool {
	if t == nil {
		return false
	}
	b, ok := types.Unalias(t).(*types.Basic)
	return ok && b.Info()&(types.IsBoolean|types.IsNumeric|types.IsString) != 0
}
