package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/mod/modfile"

	"github.com/tcw/gostrict/internal/directive"
)

// The review step compares the working tree with the merge base of the
// default branch and reports changes a person should look at because they
// can make a check pass without fixing anything: new suppressions, removed
// or skipped tests, tests that assert less, removed //strict: markers, build
// constraints that hide files, hand edits to generated files, lint
// configuration changes and new dependencies.

// errSkip marks a step that could not apply, such as review outside a git
// repository. It is reported as skipped, not as an error.
type errSkip struct{ reason string }

func (e errSkip) Error() string { return e.reason }

// fileDiff is what changed in one file between the base and the working tree.
type fileDiff struct {
	path    string // slash-separated, relative to the root; the new path, or the old one if deleted
	isNew   bool   // the file did not exist in the base
	deleted bool
	added   []diffLine
	removed []diffLine
}

type diffLine struct {
	line int // in the new file for added lines, the old file for removed ones
	text string
}

// changeSet is what changed between the merge base of the default branch
// and the working tree. The review and coverage steps share it.
type changeSet struct {
	base  string // merge base commit
	label string // the ref the merge base was found from
	diffs []*fileDiff
}

// changed returns the change set, computing it on first use.
func (r *runner) changed(ctx context.Context) (*changeSet, error) {
	if r.changes != nil {
		return r.changes, nil
	}
	if _, _, code, err := run(ctx, r.root, "git", "rev-parse", "--show-toplevel"); err != nil || code != 0 {
		return nil, errSkip{"not a git repository"}
	}
	base, label, err := r.mergeBase(ctx)
	if err != nil {
		return nil, err
	}
	diffs, err := r.diff(ctx, base)
	if err != nil {
		return nil, err
	}
	r.changes = &changeSet{base: base, label: label, diffs: diffs}
	return r.changes, nil
}

func (r *runner) review(ctx context.Context) ([]Diagnostic, string, error) {
	cs, err := r.changed(ctx)
	if err != nil {
		return nil, "", err
	}
	var diags []Diagnostic
	for _, fd := range cs.diffs {
		if isTestdata(fd.path) {
			continue
		}
		if path.Base(fd.path) == "go.mod" {
			d, err := r.reviewGoMod(ctx, cs, fd)
			if err != nil {
				return nil, "", err
			}
			diags = append(diags, d...)
			continue
		}
		diags = append(diags, r.reviewFile(fd)...)
	}
	tests, err := r.testChanges(ctx, cs)
	if err != nil {
		return nil, "", err
	}
	diags = append(diags, tests...)
	return diags, fmt.Sprintf("against %s, %d files changed", cs.label, len(cs.diffs)), nil
}

// mergeBase returns the commit to compare with and a label naming it.
func (r *runner) mergeBase(ctx context.Context) (string, string, error) {
	ref := r.base
	if ref == "" {
		ref = r.defaultBranch(ctx)
		if ref == "" {
			return "", "", errSkip{"no default branch found (no origin/HEAD, main or master); pass -base"}
		}
	}
	out, stderr, code, err := run(ctx, r.root, "git", "merge-base", "HEAD", ref)
	if err != nil {
		return "", "", err
	}
	if code != 0 {
		if r.base == "" {
			return "", "", errSkip{fmt.Sprintf("no merge base with %s: %s", ref, tail(stderr, 3))}
		}
		return "", "", fmt.Errorf("git merge-base HEAD %s: %s", ref, tail(stderr, 3))
	}
	return strings.TrimSpace(string(out)), ref, nil
}

// defaultBranch returns origin/HEAD's target, or the first of origin/main,
// origin/master, main and master that exists, or "".
func (r *runner) defaultBranch(ctx context.Context) string {
	if out, _, code, err := run(ctx, r.root, "git", "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil && code == 0 {
		return strings.TrimSpace(string(out))
	}
	for _, ref := range []string{"origin/main", "origin/master", "main", "master"} {
		if _, _, code, err := run(ctx, r.root, "git", "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err == nil && code == 0 {
			return ref
		}
	}
	return ""
}

// diff returns the changes from base to the working tree under the root,
// untracked files included.
func (r *runner) diff(ctx context.Context, base string) ([]*fileDiff, error) {
	out, stderr, code, err := run(ctx, r.root, "git", "diff", "--relative", "--no-renames", "--no-color", "--no-ext-diff", "-U0", base, "--")
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("git diff: %s", tail(stderr, 3))
	}
	diffs, err := parseDiff(out)
	if err != nil {
		return nil, err
	}
	out, stderr, code, err = run(ctx, r.root, "git", "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("git ls-files: %s", tail(stderr, 3))
	}
	for _, p := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if p == "" || (!strings.HasSuffix(p, ".go") && !lintConfigRE.MatchString(p) && path.Base(p) != "go.mod") {
			continue
		}
		fd := &fileDiff{path: p, isNew: true}
		data, err := os.ReadFile(filepath.Join(string(r.root), filepath.FromSlash(p)))
		if err != nil {
			return nil, fmt.Errorf("read untracked file: %w", err)
		}
		for i, l := range strings.Split(string(data), "\n") {
			fd.added = append(fd.added, diffLine{i + 1, l})
		}
		diffs = append(diffs, fd)
	}
	return diffs, nil
}

var hunkRE = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// parseDiff parses git diff output made with -U0 and --no-renames.
func parseDiff(out []byte) ([]*fileDiff, error) {
	var diffs []*fileDiff
	var cur *fileDiff
	oldLine, newLine := 0, 0
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		l := sc.Text()
		switch {
		case strings.HasPrefix(l, "diff --git "):
			cur = &fileDiff{}
			diffs = append(diffs, cur)
		case cur == nil:
			continue
		case strings.HasPrefix(l, "--- "):
			if l == "--- /dev/null" {
				cur.isNew = true
			} else {
				cur.path = strings.TrimPrefix(l, "--- a/")
			}
		case strings.HasPrefix(l, "+++ "):
			if l == "+++ /dev/null" {
				cur.deleted = true
			} else {
				cur.path = strings.TrimPrefix(l, "+++ b/")
			}
		case strings.HasPrefix(l, "@@"):
			m := hunkRE.FindStringSubmatch(l)
			if m == nil {
				return nil, fmt.Errorf("parse diff hunk %q", l)
			}
			oldLine, _ = strconv.Atoi(m[1])
			newLine, _ = strconv.Atoi(m[2])
		case strings.HasPrefix(l, "+"):
			cur.added = append(cur.added, diffLine{newLine, l[1:]})
			newLine++
		case strings.HasPrefix(l, "-"):
			cur.removed = append(cur.removed, diffLine{oldLine, l[1:]})
			oldLine++
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read diff: %w", err)
	}
	// A file whose path git quotes (unusual characters) has no a/ or b/
	// prefix to strip; drop entries without a path rather than guess.
	return slices.DeleteFunc(diffs, func(d *fileDiff) bool { return d.path == "" }), nil
}

func isTestdata(p string) bool {
	return slices.Contains(strings.Split(p, "/"), "testdata")
}

var lintConfigRE = regexp.MustCompile(`(^|/)\.golangci\.(ya?ml|toml|json)$`)

// suppressionPrefixes are the comments that hide findings: gostrict's,
// golangci-lint's (NilAway's included) and staticcheck's.
func suppressionPrefixes() []string {
	return []string{"//nolint", "//strict:allow", "//lint:ignore", "//lint:file-ignore"}
}

func packageMarkers() []string {
	return []string{"pure", "units", "contracts"}
}

func (r *runner) reviewFile(fd *fileDiff) []Diagnostic {
	var diags []Diagnostic
	add := func(rule string, line int, format string, args ...any) {
		diags = append(diags, Diagnostic{Rule: rule, File: fd.path, Line: line, Message: fmt.Sprintf(format, args...)})
	}
	if lintConfigRE.MatchString(fd.path) {
		verb := "changes"
		if fd.isNew {
			verb = "adds"
		}
		add("lint-config", 0, "%s a golangci-lint configuration; check that no linter or setting was turned off", verb)
		return diags
	}
	if !strings.HasSuffix(fd.path, ".go") {
		return diags
	}
	for _, l := range fd.removed {
		name, ok := strings.CutPrefix(strings.TrimSpace(l.text), "//strict:")
		if ok && slices.Contains(packageMarkers(), name) && !r.markersIn(path.Dir(fd.path))[name] {
			add("marker-removed", 0, "removes //strict:%s, so the package is no longer checked for it", name)
		}
	}
	if fd.deleted {
		return diags
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, r.abs(fd.path), nil, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return diags // the compile step reports it
	}
	if !fd.isNew && ast.IsGenerated(f) {
		add("generated-edit", 0, "changes a generated file (Code generated ... DO NOT EDIT); unless the generator produced this change, the next run of the generator discards it")
		return diags // suppressions and build constraints in generated code come from the generator
	}
	// isNew reports whether text is on an added line and was not simply
	// carried over from a removed line, as when the code before a //nolint
	// changes.
	added := map[int]bool{}
	for _, l := range fd.added {
		added[l.line] = true
	}
	isNew := func(pos token.Pos, text string) bool {
		line := fset.Position(pos).Line
		return added[line] && !slices.ContainsFunc(fd.removed, func(l diffLine) bool { return strings.Contains(l.text, text) })
	}
	// The //go:build line this change replaces, if any. A file whose line
	// is unchanged has no new one to check.
	oldLine := ""
	for _, l := range fd.removed {
		if t := strings.TrimSpace(l.text); constraint.IsGoBuild(t) {
			oldLine = t
		}
	}
	var oldBuild constraint.Expr // nil: no constraint
	if oldLine != "" {
		oldBuild, _ = constraint.Parse(oldLine)
	}
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			line := fset.Position(c.Pos()).Line
			if c.Pos() < f.Package && constraint.IsGoBuild(c.Text) && added[line] && c.Text != oldLine {
				if msg := buildExcluded(c.Text, oldBuild, fd.isNew); msg != "" {
					add("build-excluded", line, "%s", msg)
				}
			}
			hides := slices.ContainsFunc(suppressionPrefixes(), func(p string) bool { return strings.HasPrefix(c.Text, p) })
			if hides && isNew(c.Pos(), c.Text) {
				add("suppression", line, "adds a suppression; check that the finding it hides is wrong for this code and that the reason says why: %s", c.Text)
			}
		}
	}
	if !strings.HasSuffix(fd.path, "_test.go") {
		return diags
	}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !slices.Contains([]string{"Skip", "Skipf", "SkipNow"}, sel.Sel.Name) {
			return true
		}
		if fn := types.ExprString(call.Fun); isNew(call.Pos(), fn+"(") {
			add("test-skip", fset.Position(call.Pos()).Line, "adds a call to %s; a skipped test checks nothing", fn)
		}
		return true
	})
	return diags
}

func (r *runner) abs(p string) string {
	return filepath.Join(string(r.root), filepath.FromSlash(p))
}

// markersIn returns the package markers that the Go files in dir carry.
func (r *runner) markersIn(dir string) map[string]bool {
	matches, err := filepath.Glob(filepath.Join(r.abs(dir), "*.go"))
	if err != nil {
		return nil
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, m := range matches {
		if f, err := parser.ParseFile(fset, m, nil, parser.PackageClauseOnly|parser.ParseComments); err == nil {
			files = append(files, f)
		}
	}
	found := map[string]bool{}
	for _, name := range packageMarkers() {
		found[name] = directive.Package(files, name)
	}
	return found
}

// reviewGoMod reports requirements and replacements that a go.mod has and
// its base version did not. Indirect requirements are left out: they follow
// from the direct ones.
func (r *runner) reviewGoMod(ctx context.Context, cs *changeSet, fd *fileDiff) ([]Diagnostic, error) {
	if fd.deleted {
		return nil, nil
	}
	data, err := os.ReadFile(r.abs(fd.path))
	if err != nil {
		return nil, fmt.Errorf("read go.mod: %w", err)
	}
	cur := parseGoMod(fd.path, data)
	if cur == nil {
		return nil, nil // the compile step reports a broken go.mod
	}
	required, replaced := map[string]bool{}, map[string]bool{}
	if !fd.isNew {
		src, err := r.baseFile(ctx, cs, fd.path)
		if err != nil {
			return nil, err
		}
		if old := parseGoMod(fd.path, src); old != nil {
			for _, req := range old.Require {
				required[req.Mod.Path] = true
			}
			for _, rep := range old.Replace {
				replaced[replaceString(rep)] = true
			}
		}
	}
	var diags []Diagnostic
	for _, req := range cur.Require {
		if req.Indirect || required[req.Mod.Path] {
			continue
		}
		diags = append(diags, Diagnostic{
			Rule: "dependency-added", File: fd.path, Line: req.Syntax.Start.Line,
			Message: fmt.Sprintf("adds a dependency on %s %s; check that it is the module intended (models invent plausible module paths, and others register them) and that the standard library or an existing dependency cannot do the job", req.Mod.Path, req.Mod.Version),
		})
	}
	for _, rep := range cur.Replace {
		if replaced[replaceString(rep)] {
			continue
		}
		diags = append(diags, Diagnostic{
			Rule: "dependency-added", File: fd.path, Line: rep.Syntax.Start.Line,
			Message: fmt.Sprintf("adds replace %s; builds now use other code than the required version names", replaceString(rep)),
		})
	}
	return diags, nil
}

// parseGoMod parses a go.mod file, or returns nil if it does not parse.
func parseGoMod(name string, data []byte) *modfile.File {
	f, err := modfile.Parse(name, data, nil)
	if err != nil {
		return nil
	}
	return f
}

func replaceString(rep *modfile.Replace) string {
	return rep.Old.String() + " => " + rep.New.String()
}

// baseFile returns the content of p, relative to the root, at the merge base.
func (r *runner) baseFile(ctx context.Context, cs *changeSet, p string) ([]byte, error) {
	out, stderr, code, err := run(ctx, r.root, "git", "show", cs.base+":./"+p)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("git show %s:%s: %s", cs.base, p, tail(stderr, 3))
	}
	return out, nil
}

// testChanges compares the functions of changed test files with their base
// versions. It reports Test, Benchmark, Fuzz and Example functions that no
// longer exist anywhere in the same directory, so moving a test between
// files is not reported, and functions that now make fewer assertion calls.
func (r *runner) testChanges(ctx context.Context, cs *changeSet) ([]Diagnostic, error) {
	type key struct{ dir, name string }
	type place struct {
		file string
		fn   testFunc
	}
	before := map[key]place{}
	dirs := map[string]bool{}
	for _, fd := range cs.diffs {
		if fd.isNew || !strings.HasSuffix(fd.path, "_test.go") || isTestdata(fd.path) {
			continue
		}
		src, err := r.baseFile(ctx, cs, fd.path)
		if err != nil {
			return nil, err
		}
		dir := path.Dir(fd.path)
		dirs[dir] = true
		for _, fn := range testFuncs(src) {
			before[key{dir, fn.name}] = place{fd.path, fn}
		}
	}
	after := map[key]place{}
	for dir := range dirs {
		matches, err := filepath.Glob(filepath.Join(r.abs(dir), "*_test.go"))
		if err != nil {
			return nil, fmt.Errorf("list test files: %w", err)
		}
		for _, m := range matches {
			data, err := os.ReadFile(m)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("read test file: %w", err)
			}
			for _, fn := range testFuncs(data) {
				after[key{dir, fn.name}] = place{path.Join(dir, filepath.Base(m)), fn}
			}
		}
	}
	var diags []Diagnostic
	for k, b := range before {
		a, ok := after[k]
		switch {
		case !ok && isTestName(k.name):
			diags = append(diags, Diagnostic{Rule: "test-removed", File: b.file, Message: fmt.Sprintf("removes %s; check that what it tested is still tested", k.name)})
		case ok && a.fn.asserts < b.fn.asserts:
			diags = append(diags, Diagnostic{
				Rule: "assertion-removed", File: a.file, Line: a.fn.line,
				Message: fmt.Sprintf("%s has fewer assertion calls (t.Error, t.Fatal, assert.*, require.*, ...) than before: %d, down from %d; check that nothing it checked has gone unchecked", k.name, a.fn.asserts, b.fn.asserts),
			})
		}
	}
	return diags, nil
}

// testFunc is a top-level function in a test file.
type testFunc struct {
	name    string
	line    int
	asserts int // calls that can fail the test, counted by countAsserts
}

func isTestName(name string) bool {
	return slices.ContainsFunc([]string{"Test", "Benchmark", "Fuzz", "Example"}, func(p string) bool { return strings.HasPrefix(name, p) })
}

// testFuncs returns the top-level functions, not methods, of a Go test
// file, helpers included, or nil if it does not parse.
func testFuncs(src []byte) []testFunc {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x_test.go", src, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	var fns []testFunc
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv != nil || fd.Body == nil {
			continue
		}
		fns = append(fns, testFunc{name: fd.Name.Name, line: fset.Position(fd.Pos()).Line, asserts: countAsserts(fd.Body)})
	}
	return fns
}

// countAsserts counts the calls in body that can fail a test: Error,
// Errorf, Fatal, Fatalf, Fail and FailNow on any receiver, and any call
// through a package named assert or require (testify). It works on syntax
// alone, so err.Error() is told apart from t.Error(msg) by its arguments.
func countAsserts(body *ast.BlockStmt) int {
	n := 0
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && (id.Name == "assert" || id.Name == "require") {
			n++
			return true
		}
		switch sel.Sel.Name {
		case "Errorf", "Fatalf", "Fail", "FailNow":
			n++
		case "Error", "Fatal":
			if len(call.Args) > 0 {
				n++
			}
		}
		return true
	})
	return n
}
