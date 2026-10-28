package main

import (
	"fmt"
	"go/build/constraint"
	"regexp"
	"slices"
)

// platform is one build configuration an ordinary go build can have: no
// -tags, any GOOS and GOARCH, cgo on or off.
type platform struct {
	goos, goarch string
	cgo          bool
}

func (p platform) String() string {
	s := p.goos + "/" + p.goarch
	if !p.cgo {
		s += " without cgo"
	}
	return s
}

// platforms lists the configurations to try, common ones first so that the
// example a finding names is a familiar one. GOOS and GOARCH are crossed
// freely; a pair Go does not support only makes a constraint look
// satisfiable where it is not, which errs toward fewer findings.
func platforms() []platform {
	goos := []string{
		"linux", "darwin", "windows", "freebsd", "android", "ios", "js", "wasip1",
		"aix", "dragonfly", "hurd", "illumos", "netbsd", "openbsd", "plan9", "solaris", "zos",
	}
	goarch := []string{
		"amd64", "arm64", "386", "arm", "wasm", "loong64", "mips", "mipsle",
		"mips64", "mips64le", "ppc64", "ppc64le", "riscv64", "s390x",
	}
	var ps []platform
	for _, cgo := range []bool{true, false} {
		for _, sys := range goos {
			for _, arch := range goarch {
				ps = append(ps, platform{sys, arch, cgo})
			}
		}
	}
	return ps
}

var goVersionTag = regexp.MustCompile(`^go1\.[0-9]+$`)

// builds reports whether a file with constraint x is built on p without
// extra -tags. A nil x is no constraint.
func builds(x constraint.Expr, p platform) bool {
	if x == nil {
		return true
	}
	unix := []string{
		"aix", "android", "darwin", "dragonfly", "freebsd", "hurd", "illumos",
		"ios", "linux", "netbsd", "openbsd", "solaris",
	}
	return x.Eval(func(tag string) bool {
		switch tag {
		case p.goos, p.goarch, "gc":
			return true
		case "unix":
			return slices.Contains(unix, p.goos)
		case "cgo":
			return p.cgo
		case "linux":
			return p.goos == "android"
		case "darwin":
			return p.goos == "ios"
		case "solaris":
			return p.goos == "illumos"
		}
		return goVersionTag.MatchString(tag)
	})
}

// buildExcluded describes how a new //go:build line keeps a file out of
// builds, or returns "" if it does not. old is the constraint the file had
// in the base (nil for none); a file that is new may carry any platform
// constraint, but not one that no ordinary build satisfies.
func buildExcluded(line string, old constraint.Expr, isNew bool) string {
	x, err := constraint.Parse(line)
	if err != nil {
		return "" // the compile step reports it
	}
	ps := platforms()
	if !slices.ContainsFunc(ps, func(p platform) bool { return builds(x, p) }) {
		return fmt.Sprintf("adds %s, which no build without -tags satisfies, so compile, analyze and test never see this file; check that it is meant to be built only with those tags", line)
	}
	if isNew {
		return ""
	}
	for _, p := range ps {
		if builds(old, p) && !builds(x, p) {
			return fmt.Sprintf("narrows the build constraint to %s, so the file is no longer built for %s, among others; check that the code it holds is not needed there", line, p)
		}
	}
	return ""
}
