package main

import (
	"bytes"
	"context"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// format reports Go files in the loaded packages that gofmt would change,
// including files left out of this build by build constraints. Generated
// files are skipped: their formatting is the generator's.
func (r *runner) format(_ context.Context) ([]Diagnostic, string, error) {
	files := r.sourceFiles()
	var diags []Diagnostic
	for _, name := range files {
		src, err := os.ReadFile(name)
		if err != nil {
			return nil, "", fmt.Errorf("read source: %w", err)
		}
		if isGeneratedSource(src) {
			continue
		}
		rel := r.root.rel(name)
		out, err := format.Source(src)
		if err != nil {
			diags = append(diags, Diagnostic{Rule: "gofmt", File: rel, Message: "gofmt cannot parse the file: " + err.Error()})
			continue
		}
		if !bytes.Equal(src, out) {
			diags = append(diags, Diagnostic{
				Rule: "gofmt", File: rel, Line: firstDiffLine(src, out),
				Message: "file is not gofmt-formatted; run gofmt -w " + rel,
			})
		}
	}
	return diags, fmt.Sprintf("%d files", len(files)), nil
}

// sourceFiles returns the .go files of the loaded packages under the root,
// sorted and without duplicates.
func (r *runner) sourceFiles() []string {
	seen := map[string]bool{}
	var files []string
	for _, p := range r.pkgs {
		for _, f := range slices.Concat(p.GoFiles, p.IgnoredFiles) {
			if !seen[f] && strings.HasSuffix(f, ".go") && r.root.contains(f) {
				seen[f] = true
				files = append(files, f)
			}
		}
	}
	slices.Sort(files)
	return files
}

// isGeneratedSource reports whether src has a "Code generated ... DO NOT
// EDIT." line before the package clause. Unlike ast.IsGenerated it needs no
// parse, so it also works for files left out of the build.
func isGeneratedSource(src []byte) bool {
	for _, l := range strings.Split(string(src), "\n") {
		if strings.HasPrefix(l, "package ") {
			return false
		}
		if strings.HasPrefix(l, "// Code generated ") && strings.HasSuffix(strings.TrimSpace(l), " DO NOT EDIT.") {
			return true
		}
	}
	return false
}

// firstDiffLine returns the 1-based line where a and b first differ.
func firstDiffLine(a, b []byte) int {
	al, bl := strings.Split(string(a), "\n"), strings.Split(string(b), "\n")
	for i := range min(len(al), len(bl)) {
		if al[i] != bl[i] {
			return i + 1
		}
	}
	return min(len(al), len(bl))
}

// tidy reports the changes go mod tidy would make to go.mod and go.sum.
func (r *runner) tidy(ctx context.Context) ([]Diagnostic, string, error) {
	if r.goMod == "" {
		return nil, "", errSkip{"not in a module"}
	}
	modDir := rootDir(filepath.Dir(r.goMod))
	stdout, stderr, code, err := run(ctx, modDir, "go", "mod", "tidy", "-diff")
	if err != nil {
		return nil, "", err
	}
	if code == 0 {
		return nil, "", nil
	}
	if len(bytes.TrimSpace(stdout)) == 0 {
		return nil, "", fmt.Errorf("go mod tidy -diff exited %d: %s", code, tail(stderr, 5))
	}
	return parseTidyDiff(stdout, modDir, r.root), "", nil
}

// parseTidyDiff turns go mod tidy -diff output, a unified diff per file
// from current/<name> to tidy/<name>, into one finding per file.
func parseTidyDiff(out []byte, modDir, root rootDir) []Diagnostic {
	var diags []Diagnostic
	var cur *Diagnostic
	var lines []string
	flush := func() {
		if cur == nil {
			return
		}
		if len(lines) > maxOutputLines {
			lines = append(lines[:maxOutputLines:maxOutputLines], fmt.Sprintf("... (%d more lines)", len(lines)-maxOutputLines))
		}
		cur.Message += ":\n" + strings.Join(lines, "\n")
		diags = append(diags, *cur)
		cur, lines = nil, nil
	}
	for _, l := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(l, "diff "):
			flush()
		case strings.HasPrefix(l, "--- "):
			name := strings.TrimPrefix(strings.TrimPrefix(l, "--- "), "current/")
			file := root.rel(filepath.Join(string(modDir), filepath.FromSlash(name)))
			cur = &Diagnostic{Rule: "go-mod-tidy", File: file, Message: file + " is not tidy; run go mod tidy, which makes these changes"}
		case strings.HasPrefix(l, "+++ "), strings.HasPrefix(l, "@@"):
		case cur != nil && (strings.HasPrefix(l, "+") || strings.HasPrefix(l, "-")):
			lines = append(lines, l)
		}
	}
	flush()
	return diags
}
