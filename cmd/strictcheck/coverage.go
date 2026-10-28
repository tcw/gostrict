package main

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path"
	"path/filepath"
	"strings"

	"golang.org/x/tools/cover"
)

// coverage reports functions that changed since the base and that no test
// runs at all, using the profile the test step recorded with -coverpkg, so
// a function counts as tested when any package's tests reach it. A function
// with some statements run is not reported: line-level gaps are mostly
// error paths and would bury the functions with no test at all.
func (r *runner) coverage(ctx context.Context) ([]Diagnostic, string, error) {
	if !r.profiled {
		return nil, "", errSkip{"needs the test step, which did not record coverage"}
	}
	cs, err := r.changed(ctx)
	if err != nil {
		return nil, "", err
	}
	blocks := map[string][]cover.ProfileBlock{} // by absolute file name
	for _, prof := range r.profiles {
		importPath, base := path.Split(prof.FileName)
		if dir, ok := r.dirs[strings.TrimSuffix(importPath, "/")]; ok {
			blocks[filepath.Join(dir, base)] = prof.Blocks
		}
	}
	var diags []Diagnostic
	changed := 0
	for _, fd := range cs.diffs {
		if fd.deleted || !strings.HasSuffix(fd.path, ".go") || strings.HasSuffix(fd.path, "_test.go") || isTestdata(fd.path) {
			continue
		}
		file := r.abs(fd.path)
		fileBlocks, ok := blocks[file]
		if !ok {
			continue // not in a package the tests covered, or not built here
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution|parser.ParseComments)
		if err != nil || ast.IsGenerated(f) {
			continue
		}
		added := map[int]bool{}
		for _, l := range fd.added {
			added[l.line] = true
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil || (fn.Recv == nil && fn.Name.Name == "main" && f.Name.Name == "main") {
				continue
			}
			lines := lineRange{fset.Position(fn.Pos()).Line, fset.Position(fn.End()).Line}
			if !lines.touches(added) {
				continue
			}
			changed++
			if hit, stmts := lines.executed(fileBlocks); stmts > 0 && !hit {
				diags = append(diags, Diagnostic{
					Rule: "untested", File: fd.path, Line: lines.start,
					Message: fmt.Sprintf("%s was added or changed, but no test runs it; add a test that does", funcName(fn)),
				})
			}
		}
	}
	return diags, fmt.Sprintf("against %s, %d changed functions", cs.label, changed), nil
}

// lineRange is the lines start through end of a file, inclusive.
type lineRange struct{ start, end int }

// touches reports whether any line in the range was added.
func (lr lineRange) touches(added map[int]bool) bool {
	for l := lr.start; l <= lr.end; l++ {
		if added[l] {
			return true
		}
	}
	return false
}

// executed reports whether any block within the range ran, and how many
// statements those blocks hold.
func (lr lineRange) executed(blocks []cover.ProfileBlock) (bool, int) {
	hit, stmts := false, 0
	for _, b := range blocks {
		if b.StartLine >= lr.start && b.EndLine <= lr.end {
			stmts += b.NumStmt
			hit = hit || b.Count > 0
		}
	}
	return hit, stmts
}

// funcName returns F, T.M or (*T).M.
func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	recv := fn.Recv.List[0].Type
	if star, ok := recv.(*ast.StarExpr); ok {
		return "(*" + types.ExprString(stripIndex(star.X)) + ")." + fn.Name.Name
	}
	return types.ExprString(stripIndex(recv)) + "." + fn.Name.Name
}

// stripIndex removes type arguments from a receiver type: T[K] becomes T.
func stripIndex(x ast.Expr) ast.Expr {
	switch x := x.(type) {
	case *ast.IndexExpr:
		return x.X
	case *ast.IndexListExpr:
		return x.X
	}
	return x
}
