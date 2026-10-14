// Command gosym reads and edits Go code one top-level declaration at a
// time, addressed by name instead of by line numbers.
//
//	gosym list    <file|dir>                         list declarations
//	gosym show    [-n] <file|dir> <name>             print one declaration
//	gosym replace [-f file] [-rename] <file|dir> <name>   replace it (source on stdin)
//	gosym add     [-f file] [-after name] <file>     add declarations (source on stdin)
//	gosym delete  <file|dir> <name>                  remove one declaration
//
// Names are Func, Type, Var, Const or Type.Method; (*T).M and T[K].M are
// accepted too. When given a directory, gosym finds the file that declares
// the name. After every edit the file is formatted and its imports are
// fixed (added or removed) the way goimports does, and the edit is refused
// if the result does not parse.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/imports"

	"github.com/tcw/gostrict/internal/symedit"
)

const usage = `usage:
  gosym list    [-json] <file|dir>
  gosym show    [-n] <file|dir> <name>
  gosym replace [-f file] [-rename] <file|dir> <name>   (new source on stdin unless -f)
  gosym add     [-f file] [-after name] <file>          (new source on stdin unless -f)
  gosym delete  <file|dir> <name>

Names: Func, Type, Var, Const, or Type.Method ((*T).M and T[K].M also work).
Flags go before the positional arguments.`

func main() {
	os.Exit(realMain(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

type cli struct {
	stdin  io.Reader
	stdout io.Writer
}

func realMain(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "help" || args[0] == "--help" {
		_, _ = fmt.Fprintln(stderr, usage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	c := cli{stdin: stdin, stdout: stdout}
	cmds := map[string]func([]string) error{
		"list": c.list, "show": c.show, "replace": c.replace, "add": c.add, "delete": c.delete,
	}
	cmd, ok := cmds[args[0]]
	if !ok {
		_, _ = fmt.Fprintf(stderr, "gosym: unknown command %q\n%s\n", args[0], usage)
		return 2
	}
	if err := cmd(args[1:]); err != nil {
		_, _ = fmt.Fprintln(stderr, "gosym:", err)
		var ue usageError
		if errors.As(err, &ue) {
			return 2
		}
		return 1
	}
	return 0
}

type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg + "\n" + usage }

func flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func parseArgs(fs *flag.FlagSet, args []string, n int) ([]string, error) {
	if err := fs.Parse(args); err != nil {
		return nil, usageError{fs.Name() + ": " + err.Error()}
	}
	if fs.NArg() != n {
		return nil, usageError{fmt.Sprintf("%s: want %d arguments, got %d", fs.Name(), n, fs.NArg())}
	}
	return fs.Args(), nil
}

func (c cli) list(args []string) error {
	fs := flags("list")
	asJSON := fs.Bool("json", false, "print JSON")
	pos, err := parseArgs(fs, args, 1)
	if err != nil {
		return err
	}
	files, err := goFiles(pos[0])
	if err != nil {
		return err
	}
	type entry struct {
		File string `json:"file"`
		symedit.Decl
	}
	var all []entry
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("read %s: %w", f, err)
		}
		decls, err := symedit.List(src)
		if err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
		for _, d := range decls {
			all = append(all, entry{File: f, Decl: d})
		}
	}
	if *asJSON {
		if all == nil {
			all = []entry{}
		}
		enc := json.NewEncoder(c.stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(all); err != nil {
			return fmt.Errorf("write: %w", err)
		}
		return nil
	}
	var b strings.Builder
	for _, e := range all {
		fmt.Fprintf(&b, "%-6s %-30s %s:%d-%d\n", e.Kind, e.Name, e.File, e.Line, e.EndLine)
	}
	return write(c.stdout, b.String())
}

func (c cli) show(args []string) error {
	fs := flags("show")
	numbers := fs.Bool("n", false, "prefix lines with their line numbers")
	pos, err := parseArgs(fs, args, 2)
	if err != nil {
		return err
	}
	file, src, err := resolve(target{path: pos[0], name: pos[1]})
	if err != nil {
		return err
	}
	text, err := symedit.Show(src, pos[1])
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	if !*numbers {
		return write(c.stdout, text+"\n")
	}
	d, err := symedit.Find(src, pos[1])
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "// %s:%d-%d\n", file, d.Line, d.EndLine)
	for i, l := range strings.Split(text, "\n") {
		fmt.Fprintf(&b, "%5d\t%s\n", d.Line+i, l)
	}
	return write(c.stdout, b.String())
}

func (c cli) replace(args []string) error {
	fs := flags("replace")
	from := fs.String("f", "", "read the new source from this file instead of stdin")
	rename := fs.Bool("rename", false, "allow the replacement to define a different name")
	pos, err := parseArgs(fs, args, 2)
	if err != nil {
		return err
	}
	snippet, err := c.snippet(*from)
	if err != nil {
		return err
	}
	file, src, err := resolve(target{path: pos[0], name: pos[1]})
	if err != nil {
		return err
	}
	if *rename {
		if err := checkNewNames(file, snippet, symedit.NormalizeName(pos[1])); err != nil {
			return err
		}
	}
	out, err := symedit.Replace(src, pos[1], snippet, *rename)
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	return c.save(file, out, "replaced", snippet)
}

func (c cli) add(args []string) error {
	fs := flags("add")
	from := fs.String("f", "", "read the new source from this file instead of stdin")
	after := fs.String("after", "", "insert after this declaration (default: end of file)")
	pos, err := parseArgs(fs, args, 1)
	if err != nil {
		return err
	}
	file := pos[0]
	if fi, err := os.Stat(file); err != nil || fi.IsDir() {
		return usageError{"add: give a .go file, not a directory"}
	}
	snippet, err := c.snippet(*from)
	if err != nil {
		return err
	}
	if err := checkNewNames(file, snippet, ""); err != nil {
		return err
	}
	src, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("read %s: %w", file, err)
	}
	out, err := symedit.Add(src, snippet, *after)
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	return c.save(file, out, "added", snippet)
}

func (c cli) delete(args []string) error {
	pos, err := parseArgs(flags("delete"), args, 2)
	if err != nil {
		return err
	}
	file, src, err := resolve(target{path: pos[0], name: pos[1]})
	if err != nil {
		return err
	}
	out, err := symedit.Delete(src, pos[1])
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	fixed, err := fixImports(file, out)
	if err != nil {
		return err
	}
	if err := writeFile(file, fixed); err != nil {
		return err
	}
	return write(c.stdout, fmt.Sprintf("deleted %s from %s\n", symedit.NormalizeName(pos[1]), file))
}

func (c cli) snippet(from string) ([]byte, error) {
	var b []byte
	var err error
	if from != "" {
		b, err = os.ReadFile(from)
	} else {
		b, err = io.ReadAll(c.stdin)
	}
	if err != nil {
		return nil, fmt.Errorf("read new source: %w", err)
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil, errors.New("new source is empty")
	}
	return b, nil
}

// save fixes imports, writes the file and reports where the new
// declarations ended up.
func (c cli) save(file string, out []byte, verb string, snippet []byte) error {
	fixed, err := fixImports(file, out)
	if err != nil {
		return err
	}
	if err := writeFile(file, fixed); err != nil {
		return err
	}
	names, err := symedit.Names(snippet)
	if err != nil {
		return fmt.Errorf("read names: %w", err)
	}
	var b strings.Builder
	for _, n := range names {
		if d, err := symedit.Find(fixed, n); err == nil {
			fmt.Fprintf(&b, "%s %s %s in %s (lines %d-%d)\n", verb, d.Kind, d.Name, file, d.Line, d.EndLine)
		}
	}
	return write(c.stdout, b.String())
}

// checkNewNames refuses names that another file of the same package
// already declares. except is a name being replaced.
func checkNewNames(file string, snippet []byte, except string) error {
	names, err := symedit.Names(snippet)
	if err != nil {
		return fmt.Errorf("new source: %w", err)
	}
	files, err := goFiles(filepath.Dir(file))
	if err != nil {
		return err
	}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("read %s: %w", f, err)
		}
		if pkgName(src) != pkgName(mustRead(file)) {
			continue // e.g. an external _test package
		}
		decls, err := symedit.List(src)
		if err != nil {
			continue
		}
		for _, d := range decls {
			for _, n := range names {
				if d.Name == n && n != except {
					return fmt.Errorf("%s is already declared in %s:%d", n, f, d.Line)
				}
			}
		}
	}
	return nil
}

func mustRead(file string) []byte {
	b, _ := os.ReadFile(file)
	return b
}

// pkgName returns the package clause name of src, or "".
func pkgName(src []byte) string {
	for _, l := range strings.Split(string(src), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(l), "package "); ok {
			return strings.TrimSpace(strings.SplitN(rest, "//", 2)[0])
		}
	}
	return ""
}

// goFiles returns path itself for a file, or the .go files in a directory.
func goFiles(path string) ([]string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat: %w", err)
	}
	if !fi.IsDir() {
		return []string{path}, nil
	}
	files, err := filepath.Glob(filepath.Join(path, "*.go"))
	if err != nil {
		return nil, fmt.Errorf("glob: %w", err)
	}
	sort.Strings(files)
	return files, nil
}

// target names a declaration and the file or directory to look for it in.
type target struct {
	path string // a .go file or a package directory
	name string // declaration name, e.g. Func or Type.Method
}

// resolve finds the file under t.path that declares t.name.
func resolve(t target) (string, []byte, error) {
	path, name := t.path, t.name
	files, err := goFiles(path)
	if err != nil {
		return "", nil, err
	}
	type hit struct {
		file string
		src  []byte
	}
	var hits []hit
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			return "", nil, fmt.Errorf("read %s: %w", f, err)
		}
		if _, err := symedit.Find(src, name); err == nil {
			hits = append(hits, hit{file: f, src: src})
		} else if !errors.Is(err, symedit.ErrNotFound) && len(files) == 1 {
			return "", nil, fmt.Errorf("%s: %w", f, err)
		}
	}
	switch len(hits) {
	case 0:
		return "", nil, fmt.Errorf("%s not declared in %s (%d files searched); try gosym list %s", symedit.NormalizeName(name), path, len(files), path)
	case 1:
		return hits[0].file, hits[0].src, nil
	}
	names := make([]string, len(hits))
	for i, h := range hits {
		names[i] = h.file
	}
	return "", nil, fmt.Errorf("%s is declared in several files (%s); pass the file instead of the directory", symedit.NormalizeName(name), strings.Join(names, ", "))
}

func fixImports(file string, src []byte) ([]byte, error) {
	out, err := imports.Process(file, src, &imports.Options{Comments: true, TabIndent: true, TabWidth: 8})
	if err != nil {
		return nil, fmt.Errorf("fix imports in %s: %w", file, err)
	}
	return out, nil
}

// writeFile replaces file atomically, keeping its permissions.
func writeFile(file string, data []byte) error {
	fi, err := os.Stat(file)
	if err != nil {
		return fmt.Errorf("stat %s: %w", file, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".gosym-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // best-effort cleanup; it is gone after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close() //nolint:errcheck,gosec // the write error is the one worth reporting
		return fmt.Errorf("write %s: %w", tmp.Name(), err)
	}
	if err := tmp.Chmod(fi.Mode().Perm()); err != nil {
		tmp.Close() //nolint:errcheck,gosec // the chmod error is the one worth reporting
		return fmt.Errorf("chmod %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), file); err != nil {
		return fmt.Errorf("replace %s: %w", file, err)
	}
	return nil
}

func write(w io.Writer, s string) error {
	if _, err := io.WriteString(w, s); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}
