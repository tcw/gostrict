package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/nilaway"
	nilawayconfig "go.uber.org/nilaway/config"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/checker"
	"golang.org/x/tools/go/packages"
)

// runner holds what the steps share. The compile step fills in pkgs, dirs,
// modulePath and generated; later steps read them.
type runner struct {
	root      rootDir
	patterns  []string
	short     bool
	analyzers []*analysis.Analyzer

	pkgs       []*packages.Package
	dirs       map[string]string // import path -> directory
	modulePath string
	generated  map[string]bool // absolute file names with a "Code generated" header
}

// stepFunc runs one step. It returns the diagnostics found, a short detail
// line, and an error only when the step itself could not run.
type stepFunc func(ctx context.Context) ([]Diagnostic, string, error)

func timed(ctx context.Context, name string, f stepFunc) (Step, []Diagnostic) {
	start := time.Now()
	diags, detail, err := f(ctx)
	s := Step{Name: name, Status: statusPass, Detail: detail, Millis: time.Since(start).Milliseconds()}
	switch {
	case err != nil:
		s.Status = statusError
		s.Detail = err.Error()
	case len(diags) > 0:
		s.Status = statusFail
	}
	for i := range diags {
		diags[i].Step = name
	}
	return s, diags
}

// compile loads and type-checks the packages, tests included.
func (r *runner) compile(ctx context.Context) ([]Diagnostic, string, error) {
	cfg := &packages.Config{
		Mode:    packages.LoadAllSyntax | packages.NeedModule,
		Dir:     string(r.root),
		Tests:   true,
		Context: ctx,
	}
	pkgs, err := packages.Load(cfg, r.patterns...)
	if err != nil {
		return nil, "", fmt.Errorf("load packages: %w", err)
	}
	if len(pkgs) == 0 {
		return nil, "", errors.New("no packages matched " + strings.Join(r.patterns, " "))
	}
	r.pkgs = pkgs
	r.dirs = map[string]string{}
	r.generated = map[string]bool{}
	for _, p := range pkgs {
		if p.Module != nil && r.modulePath == "" {
			r.modulePath = p.Module.Path
		}
		if len(p.GoFiles) > 0 {
			r.dirs[p.PkgPath] = filepath.Dir(p.GoFiles[0])
		}
		for _, f := range p.Syntax {
			if ast.IsGenerated(f) {
				r.generated[p.Fset.Position(f.Pos()).Filename] = true
			}
		}
	}
	var diags []Diagnostic
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for _, e := range p.Errors {
			file, line, col := parsePos(e.Pos)
			diags = append(diags, Diagnostic{Rule: "compile", File: r.root.rel(file), Line: line, Col: col, Message: e.Msg})
		}
	})
	return diags, fmt.Sprintf("%d packages", countRoots(pkgs)), nil
}

func countRoots(pkgs []*packages.Package) int {
	seen := map[string]bool{}
	for _, p := range pkgs {
		seen[p.PkgPath] = true
	}
	return len(seen)
}

// analyze runs the analyzers over the loaded packages in process.
func (r *runner) analyze(_ context.Context) ([]Diagnostic, string, error) {
	diags, err := r.runAnalyzers(r.analyzers)
	return diags, fmt.Sprintf("%d analyzers", len(r.analyzers)), err
}

// runAnalyzers runs analyzers over the loaded packages in process and
// returns their findings outside generated files.
func (r *runner) runAnalyzers(analyzers []*analysis.Analyzer) ([]Diagnostic, error) {
	var roots []*packages.Package
	for _, p := range r.pkgs {
		if p.Name == "main" && strings.HasSuffix(p.ID, ".test") {
			continue // synthesized test main package
		}
		roots = append(roots, p)
	}
	graph, err := checker.Analyze(analyzers, roots, nil)
	if err != nil {
		return nil, fmt.Errorf("run analyzers: %w", err)
	}
	var diags []Diagnostic
	var failures []string
	for act := range graph.All() {
		if !act.IsRoot {
			continue
		}
		if act.Err != nil {
			failures = append(failures, act.String()+": "+act.Err.Error())
			continue
		}
		for _, d := range act.Diagnostics {
			pos := act.Package.Fset.Position(d.Pos)
			if r.generated[pos.Filename] {
				continue
			}
			diags = append(diags, Diagnostic{
				Rule: act.Analyzer.Name, File: r.root.rel(pos.Filename),
				Line: pos.Line, Col: pos.Column, Message: d.Message,
			})
		}
	}
	if len(failures) > 0 {
		return diags, fmt.Errorf("analyzer failures: %s", strings.Join(failures, "; "))
	}
	return diags, nil
}

// lint runs the golangci-lint built into strictcheck, by running this
// binary again with golangciEnv set.
func (r *runner) lint(ctx context.Context) ([]Diagnostic, string, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, "", fmt.Errorf("find strictcheck binary: %w", err)
	}
	cfg, source, cleanup, err := golangciConfigFile(r.root)
	defer cleanup()
	if err != nil {
		return nil, "", err
	}
	detail := "golangci-lint " + golangciVersion() + ", " + source
	args := append([]string{
		"run", "--config=" + cfg, "--output.json.path=stdout", "--show-stats=false",
		"--path-mode=abs", "--max-issues-per-linter=0", "--max-same-issues=0",
	}, r.patterns...)
	stdout, stderr, code, err := runEnv(ctx, r.root, []string{golangciEnv + "=1"}, self, args...)
	if err != nil {
		return nil, "", err
	}
	if code != 0 && code != 1 {
		return nil, "", fmt.Errorf("golangci-lint exited %d: %s", code, tail(stderr, 15))
	}
	if len(bytes.TrimSpace(stdout)) == 0 {
		return nil, detail, nil
	}
	diags, err := parseGolangci(stdout, r.root)
	return diags, detail, err
}

// test runs go test -json and reports failing tests.
func (r *runner) test(ctx context.Context) ([]Diagnostic, string, error) {
	args := []string{"test", "-json", "-count=1"}
	if r.short {
		args = append(args, "-short")
	}
	args = append(args, r.patterns...)
	stdout, stderr, code, err := run(ctx, r.root, "go", args...)
	if err != nil {
		return nil, "", err
	}
	diags, err := parseTestEvents(bytes.NewReader(stdout), r.dirs, r.root)
	if err != nil {
		return nil, "", err
	}
	if code != 0 && len(diags) == 0 {
		return nil, "", fmt.Errorf("go test exited %d without a reported failure: %s", code, tail(stderr, 15))
	}
	return diags, "", nil
}

// nilaway runs NilAway in process over the module's own packages.
func (r *runner) nilaway(_ context.Context) ([]Diagnostic, string, error) {
	flags := map[string]string{nilawayconfig.PrettyPrintFlag: "false"}
	if r.modulePath != "" {
		flags[nilawayconfig.IncludePkgsFlag] = r.modulePath
	}
	for name, value := range flags {
		if err := nilawayconfig.Analyzer.Flags.Set(name, value); err != nil {
			return nil, "", fmt.Errorf("configure nilaway: %w", err)
		}
	}
	diags, err := r.runAnalyzers([]*analysis.Analyzer{nilaway.Analyzer})
	return diags, "", err
}

// run executes a command in dir and returns its output and exit code. err
// is set only when the command could not be started or was cancelled.
func run(ctx context.Context, dir rootDir, name string, args ...string) ([]byte, []byte, int, error) {
	return runEnv(ctx, dir, nil, name, args...)
}

// runEnv is run with extra environment variables (KEY=value).
func runEnv(ctx context.Context, dir rootDir, env []string, name string, args ...string) ([]byte, []byte, int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = string(dir)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, nil, 0, fmt.Errorf("%s: %w", name, ctx.Err())
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return stdout.Bytes(), stderr.Bytes(), exitErr.ExitCode(), nil
	}
	if err != nil {
		return nil, nil, 0, fmt.Errorf("run %s: %w", name, err)
	}
	return stdout.Bytes(), stderr.Bytes(), 0, nil
}

// tail returns the last n non-empty lines of b, joined by " | ".
func tail(b []byte, n int) string {
	var lines []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}
