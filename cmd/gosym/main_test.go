package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const geo = `package geo

import "fmt"

// Meters is a distance.
type Meters float64

// Describe formats a distance.
func Describe(m Meters) string {
	return fmt.Sprintf("%.1f m", float64(m))
}
`

const geoTest = `package geo

import "testing"

func TestDescribe(t *testing.T) {
	if Describe(1) != "1.0 m" {
		t.Fatal("bad")
	}
}
`

func setup(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, src := range map[string]string{"geo.go": geo, "geo_test.go": geoTest} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func gosym(t *testing.T, stdin string, args ...string) (string, string, int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := realMain(args, strings.NewReader(stdin), &out, &errOut)
	return out.String(), errOut.String(), code
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestListDirJSON(t *testing.T) {
	dir := setup(t)
	out, errOut, code := gosym(t, "", "list", "-json", dir)
	if code != 0 {
		t.Fatalf("code %d: %s", code, errOut)
	}
	var entries []struct {
		File string `json:"file"`
		Kind string `json:"kind"`
		Name string `json:"name"`
		Line int    `json:"line"`
	}
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || entries[0].Name != "Meters" || entries[1].Name != "Describe" || entries[2].Name != "TestDescribe" {
		t.Errorf("entries = %+v", entries)
	}
}

func TestShowFindsFileInDir(t *testing.T) {
	dir := setup(t)
	out, errOut, code := gosym(t, "", "show", dir, "Describe")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errOut)
	}
	if !strings.HasPrefix(out, "// Describe formats a distance.\nfunc Describe") {
		t.Errorf("show = %q", out)
	}
}

func TestReplaceFixesImports(t *testing.T) {
	dir := setup(t)
	repl := "// Describe formats a distance.\nfunc Describe(m Meters) string {\n\treturn strconv.FormatFloat(float64(m), 'f', 1, 64) + \" m\"\n}\n"
	out, errOut, code := gosym(t, repl, "replace", dir, "Describe")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errOut)
	}
	if !strings.Contains(out, "replaced func Describe in") {
		t.Errorf("stdout = %q", out)
	}
	got := read(t, filepath.Join(dir, "geo.go"))
	if !strings.Contains(got, `import "strconv"`) || strings.Contains(got, `"fmt"`) {
		t.Errorf("imports not fixed:\n%s", got)
	}
}

func TestRenameRefusesExistingName(t *testing.T) {
	dir := setup(t)
	_, errOut, code := gosym(t, "func TestDescribe() {}", "replace", "-rename", dir, "Describe")
	if code == 0 || !strings.Contains(errOut, "already declared") {
		t.Errorf("code %d, stderr %q; want refusal", code, errOut)
	}
}

func TestAddAndDelete(t *testing.T) {
	dir := setup(t)
	file := filepath.Join(dir, "geo.go")
	if _, errOut, code := gosym(t, "// Kilometers is 1000 meters.\nconst Kilometers Meters = 1000\n", "add", "-after", "Meters", file); code != 0 {
		t.Fatalf("add: %s", errOut)
	}
	got := read(t, file)
	if !strings.Contains(got, "type Meters float64\n\n// Kilometers is 1000 meters.\nconst Kilometers Meters = 1000\n") {
		t.Errorf("add result:\n%s", got)
	}
	if _, errOut, code := gosym(t, "func Describe() {}", "add", file); code == 0 {
		t.Errorf("adding a duplicate should fail; stderr %q", errOut)
	}
	if _, errOut, code := gosym(t, "", "delete", dir, "Describe"); code != 0 {
		t.Fatalf("delete: %s", errOut)
	}
	got = read(t, file)
	if strings.Contains(got, "Describe") || strings.Contains(got, `"fmt"`) {
		t.Errorf("delete left Describe or its import:\n%s", got)
	}
}

func TestErrorsAndUsage(t *testing.T) {
	dir := setup(t)
	if _, _, code := gosym(t, ""); code != 2 {
		t.Errorf("no args: code %d, want 2", code)
	}
	if _, _, code := gosym(t, "", "frobnicate"); code != 2 {
		t.Errorf("unknown command: code %d, want 2", code)
	}
	if _, _, code := gosym(t, "", "show", dir); code != 2 {
		t.Errorf("missing name: code %d, want 2", code)
	}
	if _, errOut, code := gosym(t, "", "show", dir, "Nope"); code != 1 || !strings.Contains(errOut, "not declared") {
		t.Errorf("unknown name: code %d, stderr %q", code, errOut)
	}
	if _, errOut, code := gosym(t, "", "replace", dir, "Describe"); code != 1 || !strings.Contains(errOut, "empty") {
		t.Errorf("empty stdin: code %d, stderr %q", code, errOut)
	}
	before := read(t, filepath.Join(dir, "geo.go"))
	if _, _, code := gosym(t, "func Describe( {", "replace", dir, "Describe"); code != 1 {
		t.Errorf("bad source: code %d, want 1", code)
	}
	if read(t, filepath.Join(dir, "geo.go")) != before {
		t.Error("a failed replace changed the file")
	}
}
