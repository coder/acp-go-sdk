package acp

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// repoFiles returns the files that match pattern, a filepath.Glob pattern
// relative to the repository root. Use it for any file a test reads from a
// folder that holds a go.mod of its own (testdata/bench, cmd/generate and the
// verify skills under .agents/skills): the module zip that the Go proxy serves
// leaves such folders out, so `go test github.com/coder/acp-go-sdk/...` in a
// consumer's module cache has none of those files. In the module zip,
// repoFiles skips the test. In a checkout, it fails the test when nothing
// matches, so a lost fixture fails loudly. make test-module-zip runs the tests
// in a real module zip.
func repoFiles(tb testing.TB, pattern string) []string {
	tb.Helper()
	matches, err := filepath.Glob(pattern)
	if err != nil {
		tb.Fatalf("glob %s: %v", pattern, err)
	}
	if len(matches) > 0 {
		sort.Strings(matches)
		return matches
	}
	if inModuleZip(tb) {
		tb.Skipf("no %s: this is the module zip, which leaves out every folder with a go.mod of its own", pattern)
	}
	tb.Fatalf("no files match %s", pattern)
	return nil
}

// inModuleZip reports whether the tests run in the module zip: a checkout
// holds go.mod files below the root (cmd/generate, testdata/bench, ...), and
// the module zip holds none.
func inModuleZip(tb testing.TB) bool {
	tb.Helper()
	nested := errors.New("nested go.mod")
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() || path == "." {
			return nil
		}
		switch d.Name() {
		case ".git", ".gocache":
			return filepath.SkipDir
		}
		if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
			return nested
		}
		return nil
	})
	if errors.Is(err, nested) {
		return false
	}
	if err != nil {
		tb.Fatalf("look for a nested go.mod: %v", err)
	}
	return true
}
