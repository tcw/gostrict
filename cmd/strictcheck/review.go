package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
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

	"github.com/tcw/gostrict/internal/directive"
)

// The review step compares the working tree with the merge base of the
// default branch and reports changes a person should look at because they
// can make a check pass without fixing anything: new suppressions, removed
// or skipped tests, removed //strict: markers, hand edits to generated
// files, and lint configuration changes.

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

func (r *runner) review(ctx context.Context) ([]Diagnostic, string, error) {
	if _, _, code, err := run(ctx, r.root, "git", "rev-parse", "--show-toplevel"); err != nil || code != 0 {
		return nil, "", errSkip{"not a git repository"}
	}
	base, label, err := r.mergeBase(ctx)
	if err != nil {
		return nil, "", err
	}
	diffs, err := r.diff(ctx, base)
	if err != nil {
		return nil, "", err
	}
	var diags []Diagnostic
	for _, fd := range diffs {
		if isTestdata(fd.path) {
			continue
		}
		diags = append(diags, r.reviewFile(fd)...)
	}
	removed, err := r.removedTests(ctx, base, diffs)
	if err != nil {
		return nil, "", err
	}
	diags = append(diags, removed...)
	return diags, fmt.Sprintf("against %s, %d files changed", label, len(diffs)), nil
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
		if p == "" || (!strings.HasSuffix(p, ".go") && !lintConfigRE.MatchString(p)) {
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
		return diags // suppressions in generated code come from the generator
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
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			hides := slices.ContainsFunc(suppressionPrefixes(), func(p string) bool { return strings.HasPrefix(c.Text, p) })
			if hides && isNew(c.Pos(), c.Text) {
				add("suppression", fset.Position(c.Pos()).Line, "adds a suppression; check that the finding it hides is wrong for this code and that the reason says why: %s", c.Text)
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

// removedTests reports Test, Benchmark, Fuzz and Example functions that a
// changed test file had in the base and that no longer exist anywhere in
// the same directory, so moving a test between files is not reported.
func (r *runner) removedTests(ctx context.Context, base string, diffs []*fileDiff) ([]Diagnostic, error) {
	type key struct{ dir, name string }
	before := map[key]string{} // test -> file it was in
	dirs := map[string]bool{}
	for _, fd := range diffs {
		if fd.isNew || !strings.HasSuffix(fd.path, "_test.go") || isTestdata(fd.path) {
			continue
		}
		out, stderr, code, err := run(ctx, r.root, "git", "show", base+":./"+fd.path)
		if err != nil {
			return nil, err
		}
		if code != 0 {
			return nil, fmt.Errorf("git show %s:%s: %s", base, fd.path, tail(stderr, 3))
		}
		dir := path.Dir(fd.path)
		dirs[dir] = true
		for _, name := range testFuncs(out) {
			before[key{dir, name}] = fd.path
		}
	}
	after := map[key]bool{}
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
			for _, name := range testFuncs(data) {
				after[key{dir, name}] = true
			}
		}
	}
	var diags []Diagnostic
	for k, file := range before {
		if !after[k] {
			diags = append(diags, Diagnostic{Rule: "test-removed", File: file, Message: fmt.Sprintf("removes %s; check that what it tested is still tested", k.name)})
		}
	}
	return diags, nil
}

// testFuncs returns the names of the test, benchmark, fuzz and example
// functions in a Go source file, or nil if it does not parse.
func testFuncs(src []byte) []string {
	f, err := parser.ParseFile(token.NewFileSet(), "x_test.go", src, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	var names []string
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv != nil {
			continue
		}
		for _, prefix := range []string{"Test", "Benchmark", "Fuzz", "Example"} {
			if strings.HasPrefix(fd.Name.Name, prefix) {
				names = append(names, fd.Name.Name)
				break
			}
		}
	}
	return names
}
