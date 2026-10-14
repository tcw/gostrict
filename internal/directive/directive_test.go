package directive

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func parse(t *testing.T, src string) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "x.go", src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestPackageMarker(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want bool
	}{
		{"header", "// Package p.\n//\n//strict:pure\npackage p\n", true},
		{"spaced", "//  strict:pure\npackage p\n", false},
		{"after clause", "package p\n\n//strict:pure\nfunc F() {}\n", false},
		{"other marker", "//strict:units\npackage p\n", false},
		{"build tag first", "//go:build linux\n\n//strict:pure\npackage p\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Package([]*ast.File{parse(t, tt.src)}, "pure"); got != tt.want {
				t.Errorf("Package = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseAllow(t *testing.T) {
	tests := []struct {
		text   string
		ok     bool
		rules  []string
		reason string
	}{
		{"//strict:allow argswap order does not matter", true, []string{"argswap"}, "order does not matter"},
		{"//strict:allow argswap,units shared reason", true, []string{"argswap", "units"}, "shared reason"},
		{"//strict:allow argswap", true, []string{"argswap"}, ""},
		{"//strict:allowed argswap x", false, nil, ""},
		{"// strict:allow argswap x", false, nil, ""},
		{"//strict:pure", false, nil, ""},
	}
	for _, tt := range tests {
		s, ok := parseAllow(&ast.Comment{Text: tt.text})
		if ok != tt.ok {
			t.Errorf("%q: ok = %v, want %v", tt.text, ok, tt.ok)
			continue
		}
		if !ok {
			continue
		}
		if len(s.Rules) != len(tt.rules) || s.Reason != tt.reason {
			t.Errorf("%q: got rules %v reason %q, want %v %q", tt.text, s.Rules, s.Reason, tt.rules, tt.reason)
			continue
		}
		for i := range s.Rules {
			if s.Rules[i] != tt.rules[i] {
				t.Errorf("%q: rule %d = %q, want %q", tt.text, i, s.Rules[i], tt.rules[i])
			}
		}
	}
}
