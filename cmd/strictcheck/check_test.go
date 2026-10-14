package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func runCheck(t *testing.T, dir string, args ...string) (*Report, int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := realMain(append([]string{"-C", dir, "-steps", "compile,analyze", "-vet", "off"}, args...), &out, &errOut)
	var r Report
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("decode report: %v\nstdout: %s\nstderr: %s", err, out.String(), errOut.String())
	}
	return &r, code
}

func TestCompileErrorsBlockLaterSteps(t *testing.T) {
	r, code := runCheck(t, "testdata/broken")
	if code != 1 || r.OK {
		t.Fatalf("code = %d, ok = %v; want 1, false", code, r.OK)
	}
	if len(r.Diagnostics) != 1 || r.Diagnostics[0].Rule != "compile" || r.Diagnostics[0].File != "broken.go" || r.Diagnostics[0].Line != 4 {
		t.Fatalf("diagnostics = %+v, want one compile error at broken.go:4", r.Diagnostics)
	}
	if r.Steps[1].Name != "analyze" || r.Steps[1].Status != statusSkipped {
		t.Errorf("analyze step = %+v, want skipped", r.Steps[1])
	}
}

func TestAnalyzeFindingsSkipGeneratedFiles(t *testing.T) {
	r, code := runCheck(t, "testdata/findings")
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if len(r.Diagnostics) != 1 {
		t.Fatalf("diagnostics = %+v, want only the argswap finding in findings.go", r.Diagnostics)
	}
	d := r.Diagnostics[0]
	if d.Rule != "argswap" || d.File != "findings.go" || d.Line != 4 || !strings.Contains(d.Message, "dx, dy") {
		t.Errorf("diagnostic = %+v", d)
	}
}

func TestUsageErrors(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := realMain([]string{"-steps", "compile,nope"}, &out, &errOut); code != 2 {
		t.Errorf("unknown step: code = %d, want 2", code)
	}
	if code := realMain([]string{"-format", "xml"}, &out, &errOut); code != 2 {
		t.Errorf("bad format: code = %d, want 2", code)
	}
}

func TestAnalyzerFlagsArePassedThrough(t *testing.T) {
	r, _ := runCheck(t, "testdata/findings", "-argswap.ignore", "Move")
	// Move is a function, not a method, so the ignore list does not apply.
	if len(r.Diagnostics) != 1 {
		t.Fatalf("diagnostics = %+v", r.Diagnostics)
	}
}
