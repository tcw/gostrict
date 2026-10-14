// Package symedit finds and edits top-level Go declarations by name, so a
// change can target "function Plan" instead of a range of lines.
//
// All functions are pure: they take source bytes and return new source
// bytes. Callers handle files, imports and formatting.
package symedit

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/scanner"
	"go/token"
	"strings"
)

// Decl is one named top-level declaration.
type Decl struct {
	Kind    string `json:"kind"` // func, method, type, var or const
	Name    string `json:"name"` // Name, or Recv.Method for methods
	Line    int    `json:"line"`
	EndLine int    `json:"end_line"`
	Grouped bool   `json:"grouped,omitempty"` // declared inside a parenthesized type/var/const block

	start, end int    // byte range, doc comment included
	tok        string // type, var or const for GenDecl entries
}

// ErrNotFound is returned when no declaration has the requested name.
var ErrNotFound = errors.New("declaration not found")

// NormalizeName turns "(*T).M", "T[K].M" and "*T.M" into "T.M".
func NormalizeName(name string) string {
	name = strings.TrimSpace(name)
	recv, method, ok := strings.Cut(name, ").")
	if ok {
		name = strings.TrimLeft(strings.TrimPrefix(recv, "("), "*") + "." + method
	}
	name = strings.TrimPrefix(name, "*")
	if i := strings.IndexByte(name, '['); i >= 0 {
		if j := strings.IndexByte(name, ']'); j > i {
			name = name[:i] + name[j+1:]
		}
	}
	return name
}

func parse(src []byte) (*token.FileSet, *ast.File, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, nil, fmt.Errorf("parse: %w", err)
	}
	return fset, f, nil
}

// List returns every named top-level declaration in src, in source order.
func List(src []byte) ([]Decl, error) {
	fset, f, err := parse(src)
	if err != nil {
		return nil, err
	}
	return collect(fset, f), nil
}

func collect(fset *token.FileSet, f *ast.File) []Decl {
	off := func(p token.Pos) int { return fset.Position(p).Offset }
	line := func(p token.Pos) int { return fset.Position(p).Line }
	var out []Decl
	add := func(d Decl, start, end token.Pos) {
		d.start, d.end = off(start), off(end)
		d.Line, d.EndLine = line(start), line(end-1)
		out = append(out, d)
	}
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			kind, name := "func", d.Name.Name
			if d.Recv != nil && len(d.Recv.List) > 0 {
				kind, name = "method", recvName(d.Recv.List[0].Type)+"."+name
			}
			add(Decl{Kind: kind, Name: name}, docStart(d.Doc, d.Pos()), d.End())
		case *ast.GenDecl:
			if d.Tok == token.IMPORT {
				continue
			}
			tok := d.Tok.String()
			grouped := d.Lparen.IsValid()
			for _, spec := range d.Specs {
				names, specDoc, comment := specInfo(spec)
				start, end := docStart(d.Doc, d.Pos()), d.End()
				if grouped {
					start, end = docStart(specDoc, spec.Pos()), spec.End()
				}
				if comment != nil && comment.End() > end {
					end = comment.End()
				}
				for _, n := range names {
					if n == "_" {
						continue
					}
					add(Decl{Kind: tok, Name: n, Grouped: grouped, tok: tok}, start, end)
				}
			}
		}
	}
	return out
}

func docStart(doc *ast.CommentGroup, pos token.Pos) token.Pos {
	if doc != nil {
		return doc.Pos()
	}
	return pos
}

func specInfo(spec ast.Spec) ([]string, *ast.CommentGroup, *ast.CommentGroup) {
	switch s := spec.(type) {
	case *ast.TypeSpec:
		return []string{s.Name.Name}, s.Doc, s.Comment
	case *ast.ValueSpec:
		names := make([]string, len(s.Names))
		for i, n := range s.Names {
			names[i] = n.Name
		}
		return names, s.Doc, s.Comment
	}
	return nil, nil, nil
}

func recvName(e ast.Expr) string {
	for {
		switch x := e.(type) {
		case *ast.StarExpr:
			e = x.X
		case *ast.ParenExpr:
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.IndexListExpr:
			e = x.X
		case *ast.Ident:
			return x.Name
		default:
			return "?"
		}
	}
}

// Find returns the declaration called name.
func Find(src []byte, name string) (Decl, error) {
	decls, err := List(src)
	if err != nil {
		return Decl{}, err
	}
	name = NormalizeName(name)
	for _, d := range decls {
		if d.Name == name {
			return d, nil
		}
	}
	return Decl{}, fmt.Errorf("%w: %s", ErrNotFound, name)
}

// Show returns the source of the declaration called name, doc comment
// included.
func Show(src []byte, name string) (string, error) {
	d, err := Find(src, name)
	if err != nil {
		return "", err
	}
	return string(src[d.start:d.end]), nil
}

// snippetHeader is prepended to snippets so they parse as a file.
const snippetHeader = "package p\n\n"

// snippetError shifts parse error positions back to snippet line numbers.
func snippetError(err error) error {
	var list scanner.ErrorList
	if !errors.As(err, &list) {
		return err
	}
	headerLines := strings.Count(snippetHeader, "\n")
	msgs := make([]string, 0, len(list))
	for _, e := range list {
		msgs = append(msgs, fmt.Sprintf("line %d:%d: %s", e.Pos.Line-headerLines, e.Pos.Column, e.Msg))
		if len(msgs) == 5 {
			break
		}
	}
	return errors.New(strings.Join(msgs, "; "))
}

// parseSnippet parses one or more declarations given without a package
// clause. Import declarations are rejected: imports are fixed up afterwards.
func parseSnippet(snippet []byte) ([]byte, *token.FileSet, *ast.File, error) {
	src := append([]byte(snippetHeader), snippet...)
	fset, f, err := parse(src)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("replacement does not parse (give declarations only, no package clause): %w", snippetError(err))
	}
	for _, d := range f.Decls {
		if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.IMPORT {
			return nil, nil, nil, errors.New("replacement must not contain imports; they are added and removed automatically")
		}
	}
	if len(f.Decls) == 0 {
		return nil, nil, nil, errors.New("replacement contains no declarations")
	}
	return src, fset, f, nil
}

// Replace swaps the declaration called name for snippet, which must
// declare exactly one top-level declaration that defines name (or any name,
// if rename is true). A declaration inside a grouped type/var/const block is
// replaced in place, keeping the group.
func Replace(src []byte, name string, snippet []byte, rename bool) ([]byte, error) {
	target, err := Find(src, name)
	if err != nil {
		return nil, err
	}
	ssrc, sfset, sf, err := parseSnippet(snippet)
	if err != nil {
		return nil, err
	}
	if len(sf.Decls) != 1 {
		return nil, fmt.Errorf("replacement must contain exactly one declaration, found %d (use add for new declarations)", len(sf.Decls))
	}
	newDecls := collect(sfset, sf)
	if !rename && !defines(newDecls, target.Name) {
		return nil, fmt.Errorf("replacement does not define %s (pass -rename to allow a new name)", target.Name)
	}
	var text []byte
	if target.Grouped {
		text, err = specText(ssrc, sfset, sf, target.tok)
		if err != nil {
			return nil, err
		}
	} else {
		text = declText(ssrc, sfset, sf.Decls[0])
	}
	return splice(src, target.start, target.end, text)
}

// declText returns the source of a whole declaration, doc comment and
// trailing line comment included.
func declText(src []byte, fset *token.FileSet, d ast.Decl) []byte {
	off := func(p token.Pos) int { return fset.Position(p).Offset }
	start, end := d.Pos(), d.End()
	switch d := d.(type) {
	case *ast.FuncDecl:
		start = docStart(d.Doc, start)
	case *ast.GenDecl:
		start = docStart(d.Doc, start)
		if !d.Lparen.IsValid() && len(d.Specs) == 1 {
			if _, _, c := specInfo(d.Specs[0]); c != nil && c.End() > end {
				end = c.End()
			}
		}
	}
	return src[off(start):off(end)]
}

func defines(decls []Decl, name string) bool {
	for _, d := range decls {
		if d.Name == name {
			return true
		}
	}
	return false
}

// specText extracts the single spec of a snippet like "type X struct{}"
// (with its doc comment) so it can be placed inside an existing group.
func specText(ssrc []byte, fset *token.FileSet, f *ast.File, tok string) ([]byte, error) {
	gd, ok := f.Decls[0].(*ast.GenDecl)
	if !ok || gd.Tok.String() != tok || len(gd.Specs) != 1 {
		return nil, fmt.Errorf("target is inside a grouped %s block, so the replacement must be a single %s declaration", tok, tok)
	}
	spec := gd.Specs[0]
	_, specDoc, comment := specInfo(spec)
	off := func(p token.Pos) int { return fset.Position(p).Offset }
	end := off(spec.End())
	if comment != nil {
		end = max(end, off(comment.End()))
	}
	var buf bytes.Buffer
	switch {
	case specDoc != nil:
		buf.Write(ssrc[off(specDoc.Pos()):end])
	case gd.Doc != nil:
		buf.Write(ssrc[off(gd.Doc.Pos()):off(gd.Doc.End())])
		buf.WriteByte('\n')
		buf.Write(ssrc[off(spec.Pos()):end])
	default:
		buf.Write(ssrc[off(spec.Pos()):end])
	}
	return buf.Bytes(), nil
}

// Delete removes the declaration called name. For a name that shares a
// spec with others (var a, b = 1, 2) the whole spec is removed.
func Delete(src []byte, name string) ([]byte, error) {
	d, err := Find(src, name)
	if err != nil {
		return nil, err
	}
	return splice(src, d.start, d.end, nil)
}

// Add inserts snippet (one or more declarations) after the declaration
// called after, or at the end of the file when after is empty. It refuses
// names that already exist in src.
func Add(src, snippet []byte, after string) ([]byte, error) {
	ssrc, sfset, sf, err := parseSnippet(snippet)
	if err != nil {
		return nil, err
	}
	existing, err := List(src)
	if err != nil {
		return nil, err
	}
	for _, nd := range collect(sfset, sf) {
		if defines(existing, nd.Name) {
			return nil, fmt.Errorf("%s is already declared in this file (use replace)", nd.Name)
		}
	}
	text := bytes.TrimSpace(ssrc[len(snippetHeader):])
	at := len(src)
	if after != "" {
		d, err := Find(src, after)
		if err != nil {
			return nil, err
		}
		if d.Grouped {
			return nil, fmt.Errorf("%s is inside a grouped block; add after a top-level declaration instead", d.Name)
		}
		at = d.end
	}
	ins := append(append([]byte("\n\n"), text...), '\n')
	return splice(src, at, at, ins)
}

// splice replaces src[start:end] with text and gofmt-formats the result,
// which also verifies that it still parses.
//
//strict:allow argswap start and end are a half-open byte range in conventional order
func splice(src []byte, start, end int, text []byte) ([]byte, error) {
	out := make([]byte, 0, len(src)-(end-start)+len(text))
	out = append(out, src[:start]...)
	out = append(out, text...)
	out = append(out, src[end:]...)
	formatted, err := format.Source(out)
	if err != nil {
		return nil, fmt.Errorf("result does not parse: %w", err)
	}
	return formatted, nil
}

// Names returns the declaration names defined by snippet, for duplicate
// checks across a package.
func Names(snippet []byte) ([]string, error) {
	_, fset, f, err := parseSnippet(snippet)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, d := range collect(fset, f) {
		names = append(names, d.Name)
	}
	return names, nil
}
