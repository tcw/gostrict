// Command strictcheck runs every check on a Go module and prints one JSON
// report: compile errors, analyzer findings, golangci-lint issues, failing
// tests and NilAway findings, all in the same shape. golangci-lint, NilAway
// and the golangci-lint configuration are built in; nothing else needs to be
// installed.
//
//	strictcheck [flags] [packages]      (default ./...)
//
// The exit code is 0 when everything passes, 1 when there are findings or a
// step failed, and 2 for usage errors.
//
// Steps, in order:
//
//	compile  load and type-check all packages, tests included
//	analyze  gostrict analyzers (argswap, units, purity, contracts), plus
//	         the go vet suite when the lint step is not running it
//	lint     golangci-lint v2 with the module's .golangci.yml, or the
//	         built-in configuration when there is none
//	test     go test -json
//	nilaway  go.uber.org/nilaway on the module's own packages
//
// When compile fails, the later steps are skipped: fix those errors first.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/appends"
	"golang.org/x/tools/go/analysis/passes/assign"
	"golang.org/x/tools/go/analysis/passes/atomic"
	"golang.org/x/tools/go/analysis/passes/bools"
	"golang.org/x/tools/go/analysis/passes/composite"
	"golang.org/x/tools/go/analysis/passes/copylock"
	"golang.org/x/tools/go/analysis/passes/defers"
	"golang.org/x/tools/go/analysis/passes/directive"
	"golang.org/x/tools/go/analysis/passes/errorsas"
	"golang.org/x/tools/go/analysis/passes/httpresponse"
	"golang.org/x/tools/go/analysis/passes/ifaceassert"
	"golang.org/x/tools/go/analysis/passes/loopclosure"
	"golang.org/x/tools/go/analysis/passes/lostcancel"
	"golang.org/x/tools/go/analysis/passes/nilfunc"
	"golang.org/x/tools/go/analysis/passes/nilness"
	"golang.org/x/tools/go/analysis/passes/printf"
	"golang.org/x/tools/go/analysis/passes/shift"
	"golang.org/x/tools/go/analysis/passes/sigchanyzer"
	"golang.org/x/tools/go/analysis/passes/slog"
	"golang.org/x/tools/go/analysis/passes/stdmethods"
	"golang.org/x/tools/go/analysis/passes/stdversion"
	"golang.org/x/tools/go/analysis/passes/stringintconv"
	"golang.org/x/tools/go/analysis/passes/structtag"
	"golang.org/x/tools/go/analysis/passes/testinggoroutine"
	"golang.org/x/tools/go/analysis/passes/tests"
	"golang.org/x/tools/go/analysis/passes/timeformat"
	"golang.org/x/tools/go/analysis/passes/unmarshal"
	"golang.org/x/tools/go/analysis/passes/unreachable"
	"golang.org/x/tools/go/analysis/passes/unsafeptr"
	"golang.org/x/tools/go/analysis/passes/unusedresult"
	"golang.org/x/tools/go/analysis/passes/unusedwrite"
	"golang.org/x/tools/go/analysis/passes/waitgroup"

	"github.com/tcw/gostrict/analyzer/argswap"
	"github.com/tcw/gostrict/analyzer/contracts"
	"github.com/tcw/gostrict/analyzer/purity"
	"github.com/tcw/gostrict/analyzer/units"
)

func strictAnalyzers() []*analysis.Analyzer {
	return []*analysis.Analyzer{argswap.Analyzer, units.Analyzer, purity.Analyzer, contracts.Analyzer}
}

// vetAnalyzers is the go vet suite plus nilness and unusedwrite.
func vetAnalyzers() []*analysis.Analyzer {
	return []*analysis.Analyzer{
		appends.Analyzer, assign.Analyzer, atomic.Analyzer, bools.Analyzer,
		composite.Analyzer, copylock.Analyzer, defers.Analyzer, directive.Analyzer,
		errorsas.Analyzer, httpresponse.Analyzer, ifaceassert.Analyzer,
		loopclosure.Analyzer, lostcancel.Analyzer, nilfunc.Analyzer, nilness.Analyzer,
		printf.Analyzer, shift.Analyzer, sigchanyzer.Analyzer, slog.Analyzer,
		stdmethods.Analyzer, stdversion.Analyzer, stringintconv.Analyzer,
		structtag.Analyzer, testinggoroutine.Analyzer, tests.Analyzer,
		timeformat.Analyzer, unmarshal.Analyzer, unreachable.Analyzer,
		unsafeptr.Analyzer, unusedresult.Analyzer, unusedwrite.Analyzer,
		waitgroup.Analyzer,
	}
}

// allSteps lists the steps in the order they run.
func allSteps() []string {
	return []string{"compile", "analyze", "lint", "test", "nilaway"}
}

type options struct {
	dir         string
	printConfig bool
	fmt         bool
	steps       []string
	format      string
	max         int
	short       bool
	vet         string
	timeout     time.Duration
	patterns    []string
}

func main() {
	if os.Getenv(golangciEnv) == "1" {
		os.Exit(golangciMain())
	}
	os.Exit(realMain(os.Args[1:], os.Stdout, os.Stderr))
}

func realMain(args []string, stdout, stderr io.Writer) int {
	opts, err := parseFlags(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "strictcheck:", err)
		return 2
	}
	if opts.printConfig {
		if _, err := stdout.Write(golangciConfig); err != nil {
			_, _ = fmt.Fprintln(stderr, "strictcheck:", err)
			return 2
		}
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()
	if opts.fmt {
		if err := format(ctx, opts, stdout, stderr); err != nil {
			_, _ = fmt.Fprintln(stderr, "strictcheck:", err)
			return 1
		}
		return 0
	}
	rep := check(ctx, opts)
	finalize(rep, opts.max)
	if opts.format == "text" {
		err = writeText(stdout, rep)
	} else {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		err = enc.Encode(rep)
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "strictcheck:", err)
		return 2
	}
	if !rep.OK {
		return 1
	}
	return 0
}

func parseFlags(args []string, stderr io.Writer) (options, error) {
	fs := flag.NewFlagSet("strictcheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var opts options
	var steps string
	fs.StringVar(&opts.dir, "C", ".", "run in this directory (the module root)")
	fs.StringVar(&steps, "steps", strings.Join(allSteps(), ","), "comma-separated steps to run")
	fs.StringVar(&opts.format, "format", "json", "output format: json or text")
	fs.IntVar(&opts.max, "max", 200, "maximum findings to print (0 = all); counts still cover everything")
	fs.BoolVar(&opts.short, "short", false, "pass -short to go test")
	fs.StringVar(&opts.vet, "vet", "auto", "run the go vet suite in the analyze step: auto (only when the lint step does not run), on, off")
	fs.BoolVar(&opts.printConfig, "print-config", false, "print the built-in golangci-lint config and exit (save it as .golangci.yml to customize)")
	fs.BoolVar(&opts.fmt, "fmt", false, "format the packages with the formatters in the golangci-lint config (gofumpt, goimports) and exit")
	fs.DurationVar(&opts.timeout, "timeout", 10*time.Minute, "overall time limit")
	// Expose analyzer flags as -<analyzer>.<flag>, like go vet does.
	for _, a := range strictAnalyzers() {
		a.Flags.VisitAll(func(f *flag.Flag) {
			fs.Var(f.Value, a.Name+"."+f.Name, f.Usage)
		})
	}
	fs.Usage = func() {
		_, _ = fmt.Fprintln(fs.Output(), "usage: strictcheck [flags] [packages]\n\nflags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return opts, fmt.Errorf("parse flags: %w", err)
	}
	for _, s := range strings.Split(steps, ",") {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		if !slices.Contains(allSteps(), s) {
			return opts, fmt.Errorf("unknown step %q (have %s)", s, strings.Join(allSteps(), ", "))
		}
		opts.steps = append(opts.steps, s)
	}
	if opts.format != "json" && opts.format != "text" {
		return opts, fmt.Errorf("unknown format %q", opts.format)
	}
	if opts.vet != "auto" && opts.vet != "on" && opts.vet != "off" {
		return opts, fmt.Errorf("-vet must be auto, on or off, not %q", opts.vet)
	}
	opts.patterns = fs.Args()
	if len(opts.patterns) == 0 {
		opts.patterns = []string{"./..."}
	}
	return opts, nil
}

// moduleRoot returns the absolute form of the -C directory.
func moduleRoot(opts options) rootDir {
	abs, err := filepath.Abs(opts.dir)
	if err != nil {
		abs = opts.dir
	}
	return rootDir(abs)
}

// check runs the selected steps in order and collects their results.
func check(ctx context.Context, opts options) *Report {
	root := moduleRoot(opts)
	r := &runner{root: root, patterns: opts.patterns, short: opts.short, analyzers: strictAnalyzers()}
	want := func(s string) bool { return slices.Contains(opts.steps, s) }
	if opts.vet == "on" || (opts.vet == "auto" && !want("lint")) {
		r.analyzers = append(r.analyzers, vetAnalyzers()...)
	}

	rep := &Report{Root: string(root)}
	add := func(s Step, d []Diagnostic) {
		rep.Steps = append(rep.Steps, s)
		rep.Diagnostics = append(rep.Diagnostics, d...)
	}
	steps := map[string]stepFunc{
		"compile": r.compile, "analyze": r.analyze, "lint": r.lint,
		"test": r.test, "nilaway": r.nilaway,
	}
	// compile runs whenever a later step needs the loaded packages.
	needLoad := want("compile") || want("analyze") || want("test") || want("nilaway")
	blocked := ""
	for _, name := range allSteps() {
		if name == "compile" && !needLoad || name != "compile" && !want(name) {
			continue
		}
		if blocked != "" {
			add(Step{Name: name, Status: statusSkipped, Detail: blocked}, nil)
			continue
		}
		s, d := timed(ctx, name, steps[name])
		add(s, d)
		if name == "compile" && s.Status != statusPass {
			blocked = "compile step did not pass; fix it first"
		}
	}
	return rep
}
