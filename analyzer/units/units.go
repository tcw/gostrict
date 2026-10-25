// Package units reports raw float64 (and optionally int) values in the
// exported API of packages marked //strict:units.
package units

import (
	"go/ast"
	"go/types"
	"reflect"
	"strings"

	"golang.org/x/tools/go/analysis"

	"github.com/tcw/gostrict/internal/directive"
)

const doc = `require unit types in the exported API of //strict:units packages

In a package whose file header contains the line

	//strict:units

every exported function, method and struct field must use defined types
(type Meters float64, type Degrees float64) instead of raw float32/float64,
including inside slices, arrays, maps and pointers. The compiler then rejects
code that passes degrees where meters are expected.

With -ints, raw integer types are reported as well, except byte slices and
byte arrays ([]byte, [32]byte), which hold data rather than a quantity.
Suppress a single declaration with //strict:allow units <reason> in its doc
comment.`

// Analyzer is the units analyzer.
//
//nolint:gochecknoglobals // analysis.Analyzer values are package-level by convention; drivers compare them by pointer.
var Analyzer = newAnalyzer()

type config struct {
	ints bool
}

func newAnalyzer() *analysis.Analyzer {
	cfg := &config{}
	a := &analysis.Analyzer{Name: "units", Doc: doc, ResultType: reflect.TypeFor[*directive.Used]()}
	a.Flags.BoolVar(&cfg.ints, "ints", false, "also report raw integer types")
	a.Run = func(pass *analysis.Pass) (any, error) { return run(pass, cfg) }
	return a
}

func run(pass *analysis.Pass, cfg *config) (any, error) {
	used := directive.NewUsed()
	if !directive.Package(pass.Files, "units") {
		return used, nil
	}
	for _, f := range pass.Files {
		if strings.HasSuffix(pass.Fset.Position(f.Pos()).Filename, "_test.go") {
			continue
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				checkFunc(pass, cfg, used, d)
			case *ast.GenDecl:
				checkTypes(pass, cfg, used, d)
			}
		}
	}
	return used, nil
}

func checkFunc(pass *analysis.Pass, cfg *config, used *directive.Used, fd *ast.FuncDecl) {
	if !fd.Name.IsExported() || (fd.Recv != nil && !exportedRecv(fd.Recv)) {
		return
	}
	check := func(fl *ast.FieldList, what string) {
		if fl == nil {
			return
		}
		for _, field := range fl.List {
			t := pass.TypesInfo.TypeOf(field.Type)
			if raw := rawUnit(t, cfg.ints); raw != "" {
				if used.Suppressed(pass, "units", fd.Doc) {
					return
				}
				pass.ReportRangef(field.Type,
					"%s of exported %s uses raw %s; declare a unit type (e.g. type Meters float64) so the compiler catches unit mix-ups, or add //strict:allow units <reason>",
					what, fd.Name.Name, raw)
			}
		}
	}
	check(fd.Type.Params, "parameter")
	check(fd.Type.Results, "result")
}

func checkTypes(pass *analysis.Pass, cfg *config, used *directive.Used, gd *ast.GenDecl) {
	for _, spec := range gd.Specs {
		ts, ok := spec.(*ast.TypeSpec)
		if !ok || !ts.Name.IsExported() {
			continue
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			continue
		}
		for _, field := range st.Fields.List {
			if !anyExported(field.Names) {
				continue
			}
			raw := rawUnit(pass.TypesInfo.TypeOf(field.Type), cfg.ints)
			if raw == "" || used.Suppressed(pass, "units", gd.Doc, ts.Doc, field.Doc, field.Comment) {
				continue
			}
			pass.ReportRangef(field.Type,
				"field %s.%s uses raw %s; declare a unit type (e.g. type Meters float64) so the compiler catches unit mix-ups, or add //strict:allow units <reason>",
				ts.Name.Name, field.Names[0].Name, raw)
		}
	}
}

func anyExported(ids []*ast.Ident) bool {
	for _, id := range ids {
		if id.IsExported() {
			return true
		}
	}
	return false
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

// rawUnit returns the name of the raw numeric type found in t (looking
// through slices, arrays, pointers and maps), or "" if there is none.
// Byte slices and arrays are data, not quantities, so they never count.
func rawUnit(t types.Type, ints bool) string {
	switch t := types.Unalias(t).(type) {
	case *types.Basic:
		info := t.Info()
		if info&types.IsFloat != 0 || (ints && info&types.IsInteger != 0) {
			return t.Name()
		}
	case *types.Slice:
		if isByte(t.Elem()) {
			return ""
		}
		return rawUnit(t.Elem(), ints)
	case *types.Array:
		if isByte(t.Elem()) {
			return ""
		}
		return rawUnit(t.Elem(), ints)
	case *types.Pointer:
		return rawUnit(t.Elem(), ints)
	case *types.Map:
		if r := rawUnit(t.Key(), ints); r != "" {
			return r
		}
		return rawUnit(t.Elem(), ints)
	}
	return ""
}

// isByte reports whether t is byte (uint8).
func isByte(t types.Type) bool {
	b, ok := types.Unalias(t).(*types.Basic)
	return ok && b.Kind() == types.Uint8
}
