package main

import (
	"cmp"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
)

// Diagnostic is one finding, from any step, in a single shape.
type Diagnostic struct {
	Step    string `json:"step"`
	Rule    string `json:"rule"`
	File    string `json:"file,omitempty"` // relative to the report root when inside it
	Line    int    `json:"line,omitempty"`
	Col     int    `json:"col,omitempty"`
	Message string `json:"message"`
}

// Step status values.
const (
	statusPass    = "pass"
	statusFail    = "fail"
	statusSkipped = "skipped"
	statusError   = "error" // the step itself could not run
	statusWarn    = "warn"  // an advisory step found something; ok is unaffected
)

// Step records the outcome of one stage of the check.
type Step struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
	Count  int    `json:"count"`
	Millis int64  `json:"ms"`
	// Advisory steps report findings for a person to look at; they set
	// status warn instead of fail and do not make the report fail.
	Advisory bool `json:"advisory,omitempty"`
}

// Report is the single JSON document strictcheck prints.
type Report struct {
	OK          bool           `json:"ok"`
	Root        string         `json:"root"`
	Steps       []Step         `json:"steps"`
	Counts      map[string]int `json:"counts"`              // findings per rule, before truncation
	Truncated   int            `json:"truncated,omitempty"` // findings left out because of -max
	Diagnostics []Diagnostic   `json:"diagnostics"`
}

// rootDir is the absolute module root that report paths are relative to.
// It is its own type so it cannot be swapped with a file path argument.
type rootDir string

// prefix returns the root followed by a path separator, or "" for no root.
func (root rootDir) prefix() string {
	if root == "" {
		return ""
	}
	return string(root) + string(filepath.Separator)
}

// contains reports whether file lies inside root.
func (root rootDir) contains(file string) bool {
	return root != "" && strings.HasPrefix(file, root.prefix())
}

// rel makes file relative to root when it lies inside root.
func (root rootDir) rel(file string) string {
	if file == "" || !filepath.IsAbs(file) {
		return filepath.ToSlash(file)
	}
	rel, err := filepath.Rel(string(root), file)
	if err != nil || strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(file)
	}
	return filepath.ToSlash(rel)
}

// finalize sorts and deduplicates diagnostics, fills in counts, applies
// the max limit, and sets OK.
func finalize(r *Report, maxDiags int) {
	slices.SortFunc(r.Diagnostics, func(a, b Diagnostic) int {
		return cmp.Or(
			cmp.Compare(a.File, b.File),
			cmp.Compare(a.Line, b.Line),
			cmp.Compare(a.Col, b.Col),
			cmp.Compare(a.Rule, b.Rule),
			cmp.Compare(a.Message, b.Message),
		)
	})
	r.Diagnostics = slices.CompactFunc(r.Diagnostics, func(a, b Diagnostic) bool {
		return a.File == b.File && a.Line == b.Line && a.Col == b.Col && a.Rule == b.Rule && a.Message == b.Message
	})
	r.Counts = map[string]int{}
	perStep := map[string]int{}
	for _, d := range r.Diagnostics {
		r.Counts[d.Rule]++
		perStep[d.Step]++
	}
	for i := range r.Steps {
		s := &r.Steps[i]
		s.Count = perStep[s.Name]
		if s.Count > 0 && s.Status == statusPass {
			s.Status = statusFail
			if s.Advisory {
				s.Status = statusWarn
			}
		}
	}
	if maxDiags > 0 && len(r.Diagnostics) > maxDiags {
		r.Truncated = len(r.Diagnostics) - maxDiags
		r.Diagnostics = r.Diagnostics[:maxDiags]
	}
	if r.Diagnostics == nil {
		r.Diagnostics = []Diagnostic{}
	}
	r.OK = true
	for _, s := range r.Steps {
		if s.Status == statusFail || s.Status == statusError {
			r.OK = false
		}
	}
}

// writeText prints the report for a human reader.
func writeText(w io.Writer, r *Report) error {
	var b strings.Builder
	for _, d := range r.Diagnostics {
		loc := d.File
		if d.Line > 0 {
			loc = fmt.Sprintf("%s:%d", loc, d.Line)
			if d.Col > 0 {
				loc = fmt.Sprintf("%s:%d", loc, d.Col)
			}
		}
		if loc == "" {
			loc = "-"
		}
		fmt.Fprintf(&b, "%s: [%s/%s] %s\n", loc, d.Step, d.Rule, d.Message)
	}
	if r.Truncated > 0 {
		fmt.Fprintf(&b, "... %d more findings not shown (raise -max)\n", r.Truncated)
	}
	for _, s := range r.Steps {
		fmt.Fprintf(&b, "%-8s %-7s %4d findings %6dms", s.Name, s.Status, s.Count, s.Millis)
		if s.Detail != "" {
			fmt.Fprintf(&b, "  (%s)", s.Detail)
		}
		b.WriteByte('\n')
	}
	if r.OK {
		b.WriteString("OK\n")
	} else {
		b.WriteString("FAIL\n")
	}
	_, err := io.WriteString(w, b.String())
	if err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	return nil
}
