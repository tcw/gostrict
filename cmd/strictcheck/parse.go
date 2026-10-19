package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// parsePos splits "file:line:col" or "file:line" into its parts. It parses
// from the right, so Windows drive letters survive.
func parsePos(s string) (string, int, int) {
	s = strings.TrimSpace(s)
	file, line, col := s, 0, 0
	nums := []int{}
	for range 2 {
		i := strings.LastIndexByte(file, ':')
		if i < 0 {
			break
		}
		n, err := strconv.Atoi(file[i+1:])
		if err != nil {
			break
		}
		nums = append(nums, n)
		file = file[:i]
	}
	switch len(nums) {
	case 1:
		line = nums[0]
	case 2:
		line, col = nums[1], nums[0]
	}
	if file == "-" {
		file = ""
	}
	return file, line, col
}

// testKey identifies a test (or, with an empty test, a package) in go test output.
type testKey struct{ pkg, test string }

// testEvent is one line of go test -json output.
type testEvent struct {
	Action  string
	Package string
	Test    string
	Output  string
}

var (
	// "    foo_test.go:12: message", as printed by t.Errorf and friends.
	testLogPos = regexp.MustCompile(`^\s+([^\s:]+\.go):(\d+): `)
	// any absolute path to a .go file with a line number, e.g. in panics.
	absGoPos = regexp.MustCompile(`(/[^\s:]+\.go):(\d+)`)
)

const maxOutputLines = 25

// parseTestEvents turns go test -json output into one diagnostic per failed
// test (leaf subtests only) and one per package that failed without a
// failing test. dirs maps import paths to package directories.
func parseTestEvents(r io.Reader, dirs map[string]string, root rootDir) ([]Diagnostic, error) {
	output := map[testKey][]string{}
	var failed []testKey
	failedPkg := map[string]bool{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var ev testEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		k := testKey{ev.Package, ev.Test}
		switch ev.Action {
		case "output":
			output[k] = append(output[k], strings.TrimRight(ev.Output, "\n"))
		case "fail":
			if ev.Test != "" {
				failed = append(failed, k)
				failedPkg[ev.Package] = true
			} else if ev.Package != "" {
				failed = append(failed, k)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read go test output: %w", err)
	}
	var diags []Diagnostic
	for _, k := range failed {
		if k.test == "" && failedPkg[k.pkg] {
			continue // the failing tests already explain it
		}
		if k.test != "" && hasFailedChild(k, failed) {
			continue
		}
		file, ln := locate(output[k], dirs[k.pkg], root)
		lines := cleanTestOutput(output[k], root)
		d := Diagnostic{Step: "test", File: root.rel(file), Line: ln}
		if k.test != "" {
			d.Rule = "test-failure"
			d.Message = fmt.Sprintf("%s failed", k.test)
		} else {
			d.Rule = "test-package"
			d.Message = fmt.Sprintf("package %s failed", k.pkg)
		}
		if len(lines) > 0 {
			d.Message += ":\n" + strings.Join(lines, "\n")
		}
		diags = append(diags, d)
	}
	return diags, nil
}

func hasFailedChild(k testKey, failed []testKey) bool {
	for _, f := range failed {
		if f.pkg == k.pkg && strings.HasPrefix(f.test, k.test+"/") {
			return true
		}
	}
	return false
}

// cleanTestOutput drops go test's own framing lines and stack frames
// outside root, makes paths under root relative, and caps the length.
func cleanTestOutput(lines []string, root rootDir) []string {
	prefix := root.prefix()
	inRoot := func(l string) bool { return prefix != "" && strings.Contains(l, prefix) }
	var out []string
	seen := map[string]bool{}
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		t := strings.TrimSpace(l)
		switch {
		case t == "", t == "FAIL", t == "PASS",
			strings.HasPrefix(t, "=== "), strings.HasPrefix(t, "--- FAIL"), strings.HasPrefix(t, "--- PASS"),
			strings.HasPrefix(t, "FAIL\t"), strings.HasPrefix(t, "ok  \t"), strings.HasPrefix(t, "exit status"),
			strings.HasPrefix(t, "coverage:"), strings.HasPrefix(t, "goroutine "):
			continue
		}
		// A stack frame is a function line followed by a tab-indented file line.
		if i+1 < len(lines) && isFrameFile(lines[i+1]) && !isFrameFile(l) {
			if !inRoot(lines[i+1]) {
				i++ // drop frames outside the module
				continue
			}
		}
		if isFrameFile(l) {
			if !inRoot(l) {
				continue
			}
			l = frameOffset.ReplaceAllString(l, "")
		}
		l = strings.TrimSuffix(l, " [recovered]")
		if prefix != "" {
			l = strings.ReplaceAll(l, prefix, "")
		}
		if t = strings.TrimSpace(l); strings.HasPrefix(t, "panic: ") {
			if seen[t] {
				continue
			}
			seen[t] = true
		}
		out = append(out, l)
	}
	if len(out) > maxOutputLines {
		out = append(out[:maxOutputLines:maxOutputLines], fmt.Sprintf("... (%d more lines)", len(out)-maxOutputLines))
	}
	return out
}

// frameOffset matches the " +0x1d" suffix of a stack frame file line.
var frameOffset = regexp.MustCompile(` \+0x[0-9a-f]+$`)

// isFrameFile reports whether l is the file:line half of a stack frame.
func isFrameFile(l string) bool {
	return strings.HasPrefix(l, "\t") && absGoPos.MatchString(l)
}

// locate finds the most useful source position in test output, preferring
// a t.Errorf-style log line, then a frame in a _test.go file under root,
// then any position under root.
func locate(lines []string, pkgDir string, root rootDir) (string, int) {
	for _, l := range lines {
		if m := testLogPos.FindStringSubmatch(l); m != nil && pkgDir != "" {
			n, _ := strconv.Atoi(m[2])
			return filepath.Join(pkgDir, m[1]), n
		}
	}
	var fallback string
	fallbackLine := 0
	for _, l := range lines {
		for _, m := range absGoPos.FindAllStringSubmatch(l, -1) {
			if !root.contains(m[1]) {
				continue
			}
			n, _ := strconv.Atoi(m[2])
			if strings.HasSuffix(m[1], "_test.go") {
				return m[1], n
			}
			if fallback == "" {
				fallback, fallbackLine = m[1], n
			}
		}
	}
	return fallback, fallbackLine
}
