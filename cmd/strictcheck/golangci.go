package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/golangci/golangci-lint/v2/pkg/commands"
	"github.com/golangci/golangci-lint/v2/pkg/exitcodes"
)

// golangciConfig is the default golangci-lint configuration, used when the
// module has no configuration of its own.
//
//go:embed golangci.yml
var golangciConfig []byte

// golangciEnv, set to "1", makes the strictcheck binary act as golangci-lint.
// The lint step runs strictcheck itself this way, so golangci-lint does not
// have to be installed and its global state stays out of this process.
const golangciEnv = "STRICTCHECK_AS_GOLANGCI_LINT"

const golangciModule = "github.com/golangci/golangci-lint/v2"

// golangciMain runs the built-in golangci-lint with os.Args, like its own
// main function does, and returns the exit code.
func golangciMain() int {
	info := commands.BuildInfo{Version: golangciVersion(), Commit: "built into strictcheck", Date: "(unknown)"}
	if bi, ok := debug.ReadBuildInfo(); ok {
		info.GoVersion = bi.GoVersion
	}
	if err := commands.Execute(info); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "golangci-lint: %v\n", err)
		return exitcodes.Failure
	}
	return exitcodes.Success
}

// golangciVersion returns the version of the linked golangci-lint module.
func golangciVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range bi.Deps {
			if dep.Path == golangciModule {
				return strings.TrimPrefix(dep.Version, "v")
			}
		}
	}
	return "unknown"
}

// golangciConfigNames are the file names golangci-lint reads its
// configuration from.
func golangciConfigNames() []string {
	return []string{".golangci.yml", ".golangci.yaml", ".golangci.toml", ".golangci.json"}
}

// findGolangciConfig looks for a golangci-lint configuration in dir and its
// parents, as golangci-lint does, and returns its path or "".
func findGolangciConfig(dir string) (string, error) {
	for {
		for _, name := range golangciConfigNames() {
			p := filepath.Join(dir, name)
			_, err := os.Stat(p)
			if err == nil {
				return p, nil
			}
			if !errors.Is(err, fs.ErrNotExist) {
				return "", fmt.Errorf("look for golangci-lint config: %w", err)
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}

// golangciConfigFile returns the golangci-lint configuration to use: the
// module's own if it has one, otherwise the built-in one written to a
// temporary file. The caller must call cleanup when done with the file.
func golangciConfigFile(root rootDir) (path, source string, cleanup func(), err error) {
	cleanup = func() {}
	own, err := findGolangciConfig(string(root))
	if err != nil || own != "" {
		return own, root.rel(own), cleanup, err
	}
	f, err := os.CreateTemp("", "strictcheck-*.golangci.yml")
	if err != nil {
		return "", "", cleanup, fmt.Errorf("write built-in golangci-lint config: %w", err)
	}
	cleanup = func() { _ = os.Remove(f.Name()) }
	_, err = f.Write(golangciConfig)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		cleanup()
		return "", "", func() {}, fmt.Errorf("write built-in golangci-lint config: %w", err)
	}
	return f.Name(), "built-in config", cleanup, nil
}

// format runs the built-in golangci-lint fmt on the packages, with the same
// configuration the lint step uses, and copies its output.
func format(ctx context.Context, opts options, stdout, stderr io.Writer) error {
	root := moduleRoot(opts)
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find strictcheck binary: %w", err)
	}
	cfg, _, cleanup, err := golangciConfigFile(root)
	defer cleanup()
	if err != nil {
		return err
	}
	args := append([]string{"fmt", "--config=" + cfg}, opts.patterns...)
	out, errOut, code, err := runEnv(ctx, root, []string{golangciEnv + "=1"}, self, args...)
	if err != nil {
		return err
	}
	_, _ = stdout.Write(out)
	_, _ = stderr.Write(errOut)
	if code != 0 {
		return fmt.Errorf("golangci-lint fmt exited %d", code)
	}
	return nil
}
