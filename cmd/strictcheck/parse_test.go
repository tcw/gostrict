package main

import (
	"strings"
	"testing"
)

func TestParsePos(t *testing.T) {
	tests := []struct {
		in        string
		file      string
		line, col int
	}{
		{"/a/b.go:12:3", "/a/b.go", 12, 3},
		{"/a/b.go:12", "/a/b.go", 12, 0},
		{`C:\a\b.go:7:1`, `C:\a\b.go`, 7, 1},
		{"-", "", 0, 0},
		{"/a/b.go", "/a/b.go", 0, 0},
	}
	for _, tt := range tests {
		f, l, c := parsePos(tt.in)
		if f != tt.file || l != tt.line || c != tt.col {
			t.Errorf("parsePos(%q) = %q, %d, %d; want %q, %d, %d", tt.in, f, l, c, tt.file, tt.line, tt.col)
		}
	}
}

func TestParseGolangci(t *testing.T) {
	data := []byte(`{"Issues":[{"FromLinter":"errcheck","Text":"unchecked","Pos":{"Filename":"cmd/x/main.go","Line":4,"Column":2}}],"Report":{}}`)
	diags, err := parseGolangci(data, "/root")
	if err != nil {
		t.Fatal(err)
	}
	want := Diagnostic{Step: "lint", Rule: "errcheck", File: "cmd/x/main.go", Line: 4, Col: 2, Message: "unchecked"}
	if len(diags) != 1 || diags[0] != want {
		t.Fatalf("got %+v, want %+v", diags, want)
	}
}

func TestParseAnalysisJSON(t *testing.T) {
	data := []byte(`{"example.com/m/core":{"nilaway":[{"posn":"/root/core/a.go:3:9","message":"Potential nil panic"}]}}
{"example.com/m/other":{"nilaway":{"error":"boom"}}}`)
	diags, err := parseAnalysisJSON(data, "/root")
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) != 2 {
		t.Fatalf("got %d diagnostics, want 2: %+v", len(diags), diags)
	}
	got := map[string]Diagnostic{}
	for _, d := range diags {
		got[d.File] = d
	}
	if d := got["core/a.go"]; d.Line != 3 || d.Col != 9 || d.Rule != "nilaway" {
		t.Errorf("nil panic diagnostic = %+v", d)
	}
	if d := got[""]; !strings.Contains(d.Message, "boom") {
		t.Errorf("error diagnostic = %+v", d)
	}
}

func TestParseTestEvents(t *testing.T) {
	events := `{"Action":"run","Package":"m/p","Test":"TestA"}
{"Action":"output","Package":"m/p","Test":"TestA","Output":"=== RUN   TestA\n"}
{"Action":"output","Package":"m/p","Test":"TestA","Output":"    a_test.go:9: got 1, want 2\n"}
{"Action":"output","Package":"m/p","Test":"TestA","Output":"--- FAIL: TestA (0.00s)\n"}
{"Action":"fail","Package":"m/p","Test":"TestA"}
{"Action":"output","Package":"m/p","Test":"TestB/sub","Output":"    b_test.go:20: sub broke\n"}
{"Action":"fail","Package":"m/p","Test":"TestB/sub"}
{"Action":"fail","Package":"m/p","Test":"TestB"}
{"Action":"pass","Package":"m/p","Test":"TestC"}
{"Action":"fail","Package":"m/p"}
{"Action":"output","Package":"m/q","Output":"panic: init failed\n"}
{"Action":"fail","Package":"m/q"}
not json
`
	diags, err := parseTestEvents(strings.NewReader(events), map[string]string{"m/p": "/root/p"}, "/root")
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) != 3 {
		t.Fatalf("got %d diagnostics, want 3 (TestA, TestB/sub, package m/q): %+v", len(diags), diags)
	}
	if d := diags[0]; d.File != "p/a_test.go" || d.Line != 9 || d.Rule != "test-failure" || !strings.Contains(d.Message, "got 1, want 2") || strings.Contains(d.Message, "=== RUN") {
		t.Errorf("TestA diagnostic = %+v", d)
	}
	if d := diags[1]; !strings.HasPrefix(d.Message, "TestB/sub failed") || d.Line != 20 {
		t.Errorf("subtest diagnostic = %+v", d)
	}
	if d := diags[2]; d.Rule != "test-package" || !strings.Contains(d.Message, "init failed") {
		t.Errorf("package diagnostic = %+v", d)
	}
}

func TestCleanTestOutputDropsForeignFrames(t *testing.T) {
	in := []string{
		"panic: /root/core/c.go:30: precondition failed [recovered]",
		"\tpanic: /root/core/c.go:30: precondition failed",
		"goroutine 7 [running]:",
		"testing.tRunner.func1.2({0x1, 0x2})",
		"\t/usr/local/go/src/testing/testing.go:1734 +0x21c",
		"example.com/m/core.Distance(...)",
		"\t/root/core/c.go:30 +0xc5",
	}
	got := strings.Join(cleanTestOutput(in, "/root"), "\n")
	want := "panic: core/c.go:30: precondition failed\nexample.com/m/core.Distance(...)\n\tcore/c.go:30"
	if got != want {
		t.Errorf("cleanTestOutput =\n%s\nwant\n%s", got, want)
	}
}

func TestFinalize(t *testing.T) {
	r := &Report{
		Steps: []Step{{Name: "analyze", Status: statusPass}},
		Diagnostics: []Diagnostic{
			{Step: "analyze", Rule: "b", File: "z.go", Line: 1},
			{Step: "analyze", Rule: "a", File: "a.go", Line: 2},
			{Step: "analyze", Rule: "a", File: "a.go", Line: 2},
			{Step: "analyze", Rule: "a", File: "a.go", Line: 1},
		},
	}
	finalize(r, 2)
	if r.OK {
		t.Error("OK = true with findings")
	}
	if r.Counts["a"] != 2 || r.Counts["b"] != 1 {
		t.Errorf("Counts = %v, want a:2 b:1 (duplicates removed)", r.Counts)
	}
	if r.Truncated != 1 || len(r.Diagnostics) != 2 || r.Diagnostics[0].Line != 1 || r.Diagnostics[0].File != "a.go" {
		t.Errorf("after truncation: %+v (truncated %d)", r.Diagnostics, r.Truncated)
	}
	if r.Steps[0].Status != statusFail || r.Steps[0].Count != 3 {
		t.Errorf("step = %+v, want fail with 3 findings", r.Steps[0])
	}
}
