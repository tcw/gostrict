// Package directives reports //strict: comments that do nothing: misspelled
// markers and rule names, markers below the package clause, and
// //strict:allow directives that suppress no finding.
package directives

import (
	"go/ast"
	"slices"
	"strings"

	"golang.org/x/tools/go/analysis"

	"github.com/tcw/gostrict/analyzer/argswap"
	"github.com/tcw/gostrict/analyzer/contracts"
	"github.com/tcw/gostrict/analyzer/purity"
	"github.com/tcw/gostrict/analyzer/units"
	"github.com/tcw/gostrict/internal/directive"
)

const doc = `report //strict: comments that have no effect

A misspelled marker such as //strict:puer turns a check off without a word,
so this analyzer reports every //strict: comment that does nothing:

  - unknown directives (//strict:puer) and unknown rules in //strict:allow
  - package markers below the package clause, or with text after them
  - // strict:pure, which the space after // makes an ordinary comment
  - //strict:allow directives that suppress no finding, because the code
    changed, the package is not marked, or the directive is not in the doc
    comment of the declaration with the finding`

// Analyzer is the directives analyzer.
//
//nolint:gochecknoglobals // analysis.Analyzer values are package-level by convention; drivers compare them by pointer.
var Analyzer = &analysis.Analyzer{
	Name:     "directives",
	Doc:      doc,
	Requires: []*analysis.Analyzer{argswap.Analyzer, units.Analyzer, purity.Analyzer, contracts.Analyzer},
	Run:      run,
}

const prefix = "//strict:"

// rule is a name //strict:allow accepts.
type rule struct {
	analyzer *analysis.Analyzer
	marker   string // package marker the rule needs, or "" if it always runs
}

func rules() map[string]rule {
	return map[string]rule{
		"argswap":   {argswap.Analyzer, ""},
		"units":     {units.Analyzer, "units"},
		"purity":    {purity.Analyzer, "pure"},
		"contracts": {contracts.Analyzer, "contracts"},
	}
}

func markers() []string {
	return []string{"contracts", "pure", "units"}
}

func run(pass *analysis.Pass) (any, error) {
	known := rules()
	for _, f := range pass.Files {
		for _, cg := range f.Comments {
			header := cg.End() < f.Package
			for _, c := range cg.List {
				checkComment(pass, f, known, c, header)
			}
		}
	}
	return nil, nil //nolint:nilnil // the analyzer has no ResultType, so there is no result to return
}

func checkComment(pass *analysis.Pass, f *ast.File, known map[string]rule, c *ast.Comment, header bool) {
	rest, ok := strings.CutPrefix(c.Text, prefix)
	if !ok {
		checkSpaced(pass, c)
		return
	}
	name, args, _ := strings.Cut(rest, " ")
	name, _, _ = strings.Cut(name, "\t")
	switch {
	case name == "allow":
		checkAllow(pass, f, known, c)
	case slices.Contains(markers(), name):
		if !header {
			pass.ReportRangef(c, "//strict:%s has no effect here; package markers must be in a comment above the package clause", name)
		} else if strings.TrimSpace(c.Text) != prefix+name {
			pass.ReportRangef(c, "//strict:%s has text after it (%q), so it does not mark the package; put the marker on a line of its own", name, strings.TrimSpace(args))
		}
	default:
		pass.ReportRangef(c, "unknown directive //strict:%s, so it has no effect; known directives are //strict:allow and the package markers %s", name, list(markers()))
	}
}

// checkSpaced reports // strict:<name>, which looks like a directive but is
// an ordinary comment.
func checkSpaced(pass *analysis.Pass, c *ast.Comment) {
	rest, ok := strings.CutPrefix(c.Text, "//")
	if !ok || rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
		return
	}
	rest, ok = strings.CutPrefix(strings.TrimLeft(rest, " \t"), "strict:")
	if !ok {
		return
	}
	name, _, _ := strings.Cut(strings.TrimSpace(rest), " ")
	if name == "allow" || slices.Contains(markers(), name) {
		pass.ReportRangef(c, "// strict:%s is an ordinary comment because of the space after //; write //strict:%s", name, name)
	}
}

func checkAllow(pass *analysis.Pass, f *ast.File, known map[string]rule, c *ast.Comment) {
	s, ok := directive.ParseAllow(c)
	if !ok {
		return
	}
	if len(s.Rules) == 0 {
		pass.ReportRangef(c, "//strict:allow names no rule; write //strict:allow <rule> <reason>, where rule is one of %s", list(names(known)))
		return
	}
	for _, name := range s.Rules {
		r, ok := known[name]
		if !ok {
			pass.ReportRangef(c, "//strict:allow names unknown rule %q, so it suppresses nothing; rules are %s", name, list(names(known)))
			continue
		}
		used, ok := pass.ResultOf[r.analyzer].(*directive.Used)
		if !ok || used.Has(c.Pos()) {
			continue
		}
		if r.marker != "" && !directive.Package(pass.Files, r.marker) {
			pass.ReportRangef(c, "//strict:allow %s suppresses nothing: the package is not marked //strict:%s; remove the directive", name, r.marker)
			continue
		}
		if isTest(pass, f) {
			pass.ReportRangef(c, "//strict:allow %s suppresses nothing: %s does not check test files; remove the directive", name, name)
			continue
		}
		pass.ReportRangef(c, "//strict:allow %s suppresses nothing: there is no %s finding here; remove it, or move it to the doc comment of the declaration with the finding", name, name)
	}
}

func isTest(pass *analysis.Pass, f *ast.File) bool {
	return strings.HasSuffix(pass.Fset.Position(f.Pos()).Filename, "_test.go")
}

func names(known map[string]rule) []string {
	out := make([]string, 0, len(known))
	for n := range known {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

func list(names []string) string {
	return strings.Join(names, ", ")
}
