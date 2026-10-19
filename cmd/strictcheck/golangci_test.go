package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain lets the test binary act as golangci-lint, as the strictcheck
// binary does, because the lint step runs the current executable.
func TestMain(m *testing.M) {
	if os.Getenv(golangciEnv) == "1" {
		os.Exit(golangciMain())
	}
	os.Exit(m.Run())
}

func TestLintUsesBuiltInConfig(t *testing.T) {
	r, code := runCheck(t, "testdata/lint", "-steps", "lint")
	if code != 1 {
		t.Fatalf("code = %d, want 1; steps = %+v", code, r.Steps)
	}
	if len(r.Diagnostics) != 1 {
		t.Fatalf("diagnostics = %+v, want one errcheck finding", r.Diagnostics)
	}
	if d := r.Diagnostics[0]; d.Step != "lint" || d.Rule != "errcheck" || d.File != "lint.go" || d.Line != 7 {
		t.Errorf("diagnostic = %+v, want errcheck at lint.go:7", d)
	}
	if detail := r.Steps[len(r.Steps)-1].Detail; !strings.Contains(detail, "built-in config") {
		t.Errorf("lint detail = %q, want it to name the built-in config", detail)
	}
}

func TestLintPrefersModuleConfig(t *testing.T) {
	r, code := runCheck(t, "testdata/lintcfg", "-steps", "lint")
	if code != 0 || len(r.Diagnostics) != 0 {
		t.Fatalf("code = %d, diagnostics = %+v; want the module's config, which leaves errcheck off", code, r.Diagnostics)
	}
	if detail := r.Steps[len(r.Steps)-1].Detail; !strings.Contains(detail, ".golangci.yml") {
		t.Errorf("lint detail = %q, want it to name .golangci.yml", detail)
	}
}

func TestNilAwayRunsInProcess(t *testing.T) {
	r, code := runCheck(t, "testdata/nilpanic", "-steps", "nilaway")
	if code != 1 || len(r.Diagnostics) == 0 {
		t.Fatalf("code = %d, diagnostics = %+v; want a nilaway finding", code, r.Diagnostics)
	}
	d := r.Diagnostics[0]
	if d.Step != "nilaway" || d.Rule != "nilaway" || d.File != "nilpanic.go" || d.Line != 5 {
		t.Errorf("diagnostic = %+v, want nilaway at nilpanic.go:5", d)
	}
	if strings.Contains(d.Message, "\x1b[") {
		t.Errorf("message has terminal colors: %q", d.Message)
	}
}

func TestPrintConfig(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := realMain([]string{"-print-config"}, &out, &errOut); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, errOut.String())
	}
	if !bytes.Equal(out.Bytes(), golangciConfig) || !strings.Contains(out.String(), `version: "2"`) {
		t.Errorf("printed config does not match the built-in one:\n%s", out.String())
	}
}

func TestFindGolangciConfig(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := findGolangciConfig(sub); err != nil || got != "" {
		t.Fatalf("no config: got %q, %v", got, err)
	}
	want := filepath.Join(root, "a", ".golangci.yaml")
	if err := os.WriteFile(want, []byte("version: \"2\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := findGolangciConfig(sub); err != nil || got != want {
		t.Errorf("config in parent: got %q, %v; want %q", got, err, want)
	}
}

func TestFormat(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod":  "module example.com/ugly\n\ngo 1.24\n",
		"ugly.go": "package ugly\n\nimport (\n\t\"strings\"\n\t\"fmt\"\n)\n\nfunc   Z( ) string {  return fmt.Sprint(strings.ToUpper(\"z\")) }\n",
	}
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var out, errOut bytes.Buffer
	if code := realMain([]string{"-C", dir, "-fmt"}, &out, &errOut); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, errOut.String())
	}
	got, err := os.ReadFile(filepath.Join(dir, "ugly.go"))
	if err != nil {
		t.Fatal(err)
	}
	want := "package ugly\n\nimport (\n\t\"fmt\"\n\t\"strings\"\n)\n\nfunc Z() string { return fmt.Sprint(strings.ToUpper(\"z\")) }\n"
	if string(got) != want {
		t.Errorf("formatted file =\n%s\nwant\n%s", got, want)
	}
}
