// Command modzip unpacks the module zip that the Go proxy would serve for the
// SDK at the checkout: the files that git tracks or would track (uncommitted
// changes included), less what the module zip leaves out (every folder with a
// go.mod of its own, vendor folders). scripts/test-module-zip runs the tests
// there, so a test that reads a file the zip leaves out fails in CI and not
// in a consumer's `go test all`.
//
//	go run . -repo ../.. -out DIR
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"golang.org/x/mod/module"
	"golang.org/x/mod/zip"
)

func main() {
	repo := flag.String("repo", ".", "the repository checkout")
	out := flag.String("out", "", "the empty folder to unpack the module into")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "modzip: -out is required")
		os.Exit(2)
	}
	if err := run(*repo, *out); err != nil {
		fmt.Fprintln(os.Stderr, "modzip:", err)
		os.Exit(1)
	}
}

func run(repo, out string) error {
	stage, err := os.MkdirTemp("", "modzip-stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := stageCheckout(repo, stage); err != nil {
		return err
	}
	f, err := os.CreateTemp("", "modzip-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	m := module.Version{Path: "github.com/coder/acp-go-sdk", Version: "v0.0.0-modzip"}
	if err := zip.CreateFromDir(f, m, stage); err != nil {
		return fmt.Errorf("create the module zip: %w", err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	return zip.Unzip(out, m, f.Name())
}

// stageCheckout copies the files that git tracks or would track into dir, so
// that ignored files (.gocache, build output) stay out of the zip.
func stageCheckout(repo, dir string) error {
	cmd := exec.Command("git", "-C", repo, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	cmd.Stderr = os.Stderr
	list, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("git ls-files: %w", err)
	}
	for _, name := range bytes.Split(bytes.TrimSuffix(list, []byte{0}), []byte{0}) {
		src := filepath.Join(repo, string(name))
		info, err := os.Lstat(src)
		if os.IsNotExist(err) {
			continue // deleted in the checkout, not yet in the index
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			continue // the module zip holds regular files only
		}
		if err := copyFile(src, filepath.Join(dir, string(name)), info.Mode()); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	w, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, in); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}
