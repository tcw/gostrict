// Package directive parses the //strict: comments that opt packages into
// checks and suppress individual findings.
//
// Two forms exist:
//
//	//strict:pure                  package marker, placed above the package clause
//	//strict:allow argswap reason  suppression, placed in a declaration's doc comment
//
// Suppressions must give a reason, so every exception is explained in the code.
//
//strict:units
package directive

import (
	"go/ast"
	"go/token"
	"slices"
	"strings"

	"golang.org/x/tools/go/analysis"
)

const prefix = "//strict:"

// Package reports whether any file of the package carries the marker
// //strict:<name> in a comment that ends before its package clause.
func Package(files []*ast.File, name string) bool {
	want := prefix + name
	for _, f := range files {
		for _, cg := range f.Comments {
			if cg.End() >= f.Package {
				break
			}
			for _, c := range cg.List {
				if strings.TrimSpace(c.Text) == want {
					return true
				}
			}
		}
	}
	return false
}

// Suppression is a parsed //strict:allow comment.
type Suppression struct {
	Pos    token.Pos
	Rules  []string
	Reason string
}

// ParseAllow parses one comment line. It returns ok=false when the line is
// not a //strict:allow directive.
func ParseAllow(c *ast.Comment) (Suppression, bool) {
	rest, ok := strings.CutPrefix(c.Text, prefix+"allow")
	if !ok {
		return Suppression{}, false
	}
	if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
		return Suppression{}, false // e.g. //strict:allowed
	}
	fields := strings.Fields(rest)
	s := Suppression{Pos: c.Pos()}
	if len(fields) > 0 {
		s.Rules = strings.Split(fields[0], ",")
		s.Reason = strings.Join(fields[1:], " ")
	}
	return s, true
}

// Used records the //strict:allow directives that suppressed a finding.
// Each analyzer that honors suppressions returns one as its result, so the
// directives analyzer can report the directives that suppress nothing.
type Used struct {
	pos map[token.Pos]bool
}

// NewUsed returns an empty Used.
func NewUsed() *Used {
	return &Used{pos: map[token.Pos]bool{}}
}

// Has reports whether the directive at pos suppressed a finding.
func (u *Used) Has(pos token.Pos) bool {
	return u.pos[pos]
}

// Suppressed reports whether any of the comment groups holds a
// //strict:allow directive for rule, and records the directive as used.
// Call it only once a finding exists. A matching directive without a reason
// still suppresses the finding, but is itself reported, so the exception
// cannot stay unexplained.
func (u *Used) Suppressed(pass *analysis.Pass, rule string, groups ...*ast.CommentGroup) bool {
	for _, cg := range groups {
		if cg == nil {
			continue
		}
		for _, c := range cg.List {
			s, ok := ParseAllow(c)
			if !ok || !slices.Contains(s.Rules, rule) {
				continue
			}
			if !u.pos[s.Pos] && strings.TrimSpace(s.Reason) == "" {
				pass.Reportf(s.Pos, "%s: //strict:allow %s needs a reason after the rule name, e.g. //strict:allow %s order does not matter here", rule, rule, rule)
			}
			u.pos[s.Pos] = true
			return true
		}
	}
	return false
}
