package symedit

import (
	"errors"
	"strings"
	"testing"
)

const src = `// Package geo does geometry.
package geo

import "math"

// Meters is a distance.
type Meters float64 // SI unit

type (
	// Point is a position.
	Point struct{ X, Y Meters }

	Line struct{ A, B Point }
)

const Origin = 0

var (
	maxIter = 20
	a, b    = 1, 2
)

// Dist returns the distance between p and q.
func Dist(p, q Point) Meters {
	return Meters(math.Hypot(float64(q.X-p.X), float64(q.Y-p.Y)))
}

func (p *Point) Scale(f float64) { p.X *= Meters(f) }

func (l Line) Len() Meters { return Dist(l.A, l.B) }
`

func TestList(t *testing.T) {
	decls, err := List([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range decls {
		got = append(got, d.Kind+" "+d.Name)
	}
	want := "type Meters|type Point|type Line|const Origin|var maxIter|var a|var b|func Dist|method Point.Scale|method Line.Len"
	if strings.Join(got, "|") != want {
		t.Errorf("List =\n%s\nwant\n%s", strings.Join(got, "|"), want)
	}
	if decls[0].Line != 6 || decls[0].EndLine != 7 {
		t.Errorf("Meters spans %d-%d, want 6-7 (doc comment included)", decls[0].Line, decls[0].EndLine)
	}
	if !decls[1].Grouped || decls[0].Grouped {
		t.Errorf("Grouped flags wrong: %+v %+v", decls[0], decls[1])
	}
}

func TestNormalizeName(t *testing.T) {
	for in, want := range map[string]string{
		"(*Point).Scale": "Point.Scale",
		"(Point).Scale":  "Point.Scale",
		"*Point.Scale":   "Point.Scale",
		"List[T].Push":   "List.Push",
		" Dist ":         "Dist",
	} {
		if got := NormalizeName(in); got != want {
			t.Errorf("NormalizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShow(t *testing.T) {
	got, err := Show([]byte(src), "(*Point).Scale")
	if err != nil {
		t.Fatal(err)
	}
	if got != "func (p *Point) Scale(f float64) { p.X *= Meters(f) }" {
		t.Errorf("Show = %q", got)
	}
	got, err = Show([]byte(src), "Meters")
	if err != nil {
		t.Fatal(err)
	}
	if got != "// Meters is a distance.\ntype Meters float64 // SI unit" {
		t.Errorf("Show(Meters) = %q", got)
	}
	if _, err := Show([]byte(src), "Nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Show(Nope) error = %v, want ErrNotFound", err)
	}
}

func TestReplaceFunc(t *testing.T) {
	out, err := Replace([]byte(src), "Dist", []byte(`// Dist returns the Euclidean distance.
func Dist(p, q Point) Meters {
	dx, dy := q.X-p.X, q.Y-p.Y
	return Meters(math.Sqrt(float64(dx*dx + dy*dy)))
}`), false)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "// Dist returns the Euclidean distance.\nfunc Dist") || strings.Contains(s, "math.Hypot") {
		t.Errorf("replace did not swap the function:\n%s", s)
	}
	if strings.Count(s, "func Dist") != 1 || !strings.Contains(s, "func (l Line) Len()") {
		t.Errorf("surrounding code damaged:\n%s", s)
	}
}

func TestReplaceGroupedSpecKeepsGroup(t *testing.T) {
	out, err := Replace([]byte(src), "Line", []byte("// Line is a segment.\ntype Line struct {\n\tA, B Point\n\tName string\n}"), false)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "type (\n") || !strings.Contains(s, "\t// Line is a segment.\n\tLine struct {") || !strings.Contains(s, "Name string") {
		t.Errorf("grouped replace wrong:\n%s", s)
	}
	if _, err := Replace([]byte(src), "Line", []byte("var Line = 1"), false); err == nil {
		t.Error("replacing a grouped type with a var should fail")
	}
}

func TestReplaceRules(t *testing.T) {
	if _, err := Replace([]byte(src), "Dist", []byte("func Distance() {}"), false); err == nil || !strings.Contains(err.Error(), "-rename") {
		t.Errorf("renaming without -rename: err = %v", err)
	}
	if _, err := Replace([]byte(src), "Dist", []byte("func Distance() {}"), true); err != nil {
		t.Errorf("renaming with rename: err = %v", err)
	}
	if _, err := Replace([]byte(src), "Dist", []byte("func Dist() {}\nfunc Other() {}"), false); err == nil {
		t.Error("two declarations should be rejected")
	}
	if _, err := Replace([]byte(src), "Dist", []byte("import \"os\"\nfunc Dist() {}"), false); err == nil || !strings.Contains(err.Error(), "imports") {
		t.Errorf("imports in snippet: err = %v", err)
	}
	if _, err := Replace([]byte(src), "Dist", []byte("func Dist( {"), false); err == nil {
		t.Error("unparsable snippet should be rejected")
	}
}

func TestDelete(t *testing.T) {
	out, err := Delete([]byte(src), "Point.Scale")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "Scale") {
		t.Errorf("Scale still present:\n%s", out)
	}
	if strings.Contains(string(out), "\n\n\n") {
		t.Errorf("delete left a run of blank lines:\n%s", out)
	}
	out, err = Delete([]byte(src), "maxIter")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "maxIter") || !strings.Contains(string(out), "a, b") {
		t.Errorf("grouped delete wrong:\n%s", out)
	}
}

func TestAdd(t *testing.T) {
	out, err := Add([]byte(src), []byte("// Area is zero for a point.\nfunc (p Point) Area() Meters { return 0 }"), "Point.Scale")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	i, j, k := strings.Index(s, "Scale("), strings.Index(s, "Area()"), strings.Index(s, "Len()")
	if i < 0 || j < 0 || k < 0 || i >= j || j >= k {
		t.Errorf("Area not inserted between Scale and Len:\n%s", s)
	}
	out, err = Add([]byte(src), []byte("const Max = 10"), "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(out), "const Max = 10\n") {
		t.Errorf("append at end failed:\n%s", out)
	}
	if _, err := Add([]byte(src), []byte("func Dist() {}"), ""); err == nil {
		t.Error("adding an existing name should fail")
	}
	if _, err := Add([]byte(src), []byte("func X() {}"), "Point"); err == nil {
		t.Error("adding after a grouped spec should fail")
	}
}

func TestNames(t *testing.T) {
	got, err := Names([]byte("func A() {}\ntype B int\nfunc (B) C() {}"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "A,B,B.C" {
		t.Errorf("Names = %v", got)
	}
}
