package hm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// Touched-package proofs (`hivemill proofs touched`): the unit tests of every JS package a branch changed, for a
// project whose merge gate (gate_cmd) is its only CI.

// TouchedPackages maps changed paths (relative to root) to the nearest directory at or above each that holds a
// package.json: relative to root ("." is root), unique and sorted. A path with no package above it maps to nothing.
func TouchedPackages(root string, changed []string) []string {
	var out []string
	for _, c := range changed {
		p := filepath.Clean(filepath.FromSlash(c))
		if !filepath.IsLocal(p) {
			continue
		}
		for dir := p; ; dir = filepath.Dir(dir) { // p itself first: a changed submodule is a directory
			if st, err := os.Stat(filepath.Join(root, dir, "package.json")); err == nil && st.Mode().IsRegular() {
				out = append(out, filepath.ToSlash(dir))
				break
			}
			if dir == "." {
				break
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

type packageJSON struct {
	Scripts         map[string]string `json:"scripts"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

func readPackageJSON(pkgDir string) (p packageJSON, err error) {
	b, err := os.ReadFile(filepath.Join(pkgDir, "package.json"))
	if err != nil {
		return p, err
	}
	err = json.Unmarshal(b, &p)
	return p, err
}

// PackageTestArgv is the command that runs the tests of the package in pkgDir: its test script, else vitest when it
// depends on it; false when it has neither (or its package.json does not read).
func PackageTestArgv(pkgDir string) ([]string, bool) {
	p, err := readPackageJSON(pkgDir)
	switch {
	case err != nil:
		return nil, false
	case p.Scripts["test"] != "":
		return []string{"npm", "run", "test", "--prefix", pkgDir}, true
	case p.Dependencies["vitest"] != "" || p.DevDependencies["vitest"] != "":
		return []string{"npx", "vitest", "run", "--root", pkgDir}, true
	}
	return nil, false
}

// TouchedRun runs the tests of the packages changed on Base...HEAD of Root.
type TouchedRun struct {
	Root, Base string
	Exec       func(dir string, argv []string) (int, error) // runs argv in dir and returns its exit code
	Out        io.Writer
}

// Run returns 0 when no package failed, 1 when one failed, 2 on a usage or git error.
func (t TouchedRun) Run() int {
	if t.Base == "" || strings.HasPrefix(t.Base, "-") {
		fmt.Fprintf(t.Out, "proofs touched: bad base %q\n", t.Base)
		return 2
	}
	root, err := filepath.Abs(t.Root)
	if err != nil {
		fmt.Fprintln(t.Out, "proofs touched:", err)
		return 2
	}
	// --relative: paths relative to root also when it is below the repository top. -z: no quoting of odd names.
	// --no-renames: a file moved between packages touches both.
	diff, err := exec.Command("git", "-C", root, "diff", "--name-only", "--relative", "--no-renames", "-z", t.Base+"...HEAD", "--").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			err = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		fmt.Fprintln(t.Out, "proofs touched: git diff:", err)
		return 2
	}
	pkgs := TouchedPackages(root, strings.FieldsFunc(string(diff), func(r rune) bool { return r == 0 }))
	if len(pkgs) == 0 {
		fmt.Fprintln(t.Out, "no package changed")
		return 0
	}
	code := 0
	for _, pkg := range pkgs {
		dir := filepath.Join(root, filepath.FromSlash(pkg))
		argv, ok := PackageTestArgv(dir)
		if !ok {
			if _, err := readPackageJSON(dir); err != nil { // a broken package.json must not pass as "no tests"
				fmt.Fprintf(t.Out, "FAIL %s: package.json: %v\n", pkg, err)
				code = 1
				continue
			}
			fmt.Fprintf(t.Out, "SKIP %s: no tests\n", pkg)
			continue
		}
		switch rc, err := t.Exec(dir, argv); {
		case err != nil:
			fmt.Fprintf(t.Out, "FAIL %s: %v\n", strings.Join(argv, " "), err)
			code = 1
		case rc != 0:
			fmt.Fprintf(t.Out, "FAIL %s\n", strings.Join(argv, " "))
			code = 1
		default:
			fmt.Fprintf(t.Out, "PASS %s\n", strings.Join(argv, " "))
		}
	}
	return code
}
