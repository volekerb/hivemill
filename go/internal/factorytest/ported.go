package factorytest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Every go/internal/factorytest/ported*.txt lists Python test modules that have a Go port, one line each:
//
//	<python module> <python case count> -> <go package>:<TestName prefix> <go case count>
//
// for example `tests/test_identity.py 22 -> internal/factorytest:TestIdentity 22`. The Python count is measured
// with `python3 -m unittest -v <module>` when the line is added and stored here, so the check needs no Python. The
// Go package is relative to go/. TestPortedCounts counts the top-level tests whose name starts with the prefix
// (`go test -list`) and fails when that number is below the Python count or below the Go count the line claims.
// Blank lines and lines starting with # are ignored. Parallel port tasks each own one ported_<area>.txt.

var portedLine = regexp.MustCompile(`^(\S+)\s+(\d+)\s+->\s+(\S+):(Test\w*)\s+(\d+)$`)

// PortedEntry is one parsed line.
type PortedEntry struct {
	Module      string
	PyCount     int
	Pkg, Prefix string
	GoClaimed   int
	File        string
	Line        int
}

// ParsePorted parses the text of one ported*.txt; file names it in messages. Malformed lines are returned as
// problems, never skipped.
func ParsePorted(file, text string) (entries []PortedEntry, problems []string) {
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		m := portedLine.FindStringSubmatch(line)
		if m == nil {
			problems = append(problems, fmt.Sprintf("%s:%d: %q is not `<python module> <n> -> <go package>:<TestPrefix> <m>`; fix the line", file, i+1, line))
			continue
		}
		py, _ := strconv.Atoi(m[2])
		gc, _ := strconv.Atoi(m[5])
		if py < 1 {
			problems = append(problems, fmt.Sprintf("%s:%d: python case count %d for %s must be at least 1; measure it with `python3 -m unittest -v`", file, i+1, py, m[1]))
			continue
		}
		entries = append(entries, PortedEntry{Module: m[1], PyCount: py, Pkg: m[3], Prefix: m[4], GoClaimed: gc, File: file, Line: i + 1})
	}
	return entries, problems
}

// ListFunc returns the names of the top-level Go tests of pkg (relative to go/) whose name starts with prefix.
type ListFunc func(pkg, prefix string) ([]string, error)

// CheckPorted returns one problem per entry whose Go port has fewer cases than the Python module (or than the line
// claims), and per entry that matches no test at all. An empty entries list is itself a problem: a check over
// nothing passes vacuously.
func CheckPorted(entries []PortedEntry, list ListFunc) []string {
	if len(entries) == 0 {
		return []string{"no ported*.txt line found: the case-count check would pass vacuously; add the line for the module you ported"}
	}
	var problems []string
	for _, e := range entries {
		names, err := list(e.Pkg, e.Prefix)
		where := fmt.Sprintf("%s:%d", e.File, e.Line)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: cannot list %s tests of %s: %v", where, e.Prefix, e.Pkg, err))
			continue
		}
		got := len(names)
		switch {
		case got == 0:
			problems = append(problems, fmt.Sprintf("%s: no test named %s* in %s; the port of %s is missing", where, e.Prefix, e.Pkg, e.Module))
		case got < e.PyCount:
			problems = append(problems, fmt.Sprintf("%s: %s has %d Python cases but %s:%s* has %d Go tests; port the missing %d", where, e.Module, e.PyCount, e.Pkg, e.Prefix, got, e.PyCount-got))
		case got < e.GoClaimed:
			problems = append(problems, fmt.Sprintf("%s: the line claims %d Go tests for %s* but %d exist; correct the line", where, e.GoClaimed, e.Prefix, got))
		}
	}
	return problems
}

// ReadPorted reads every ported*.txt in dir (sorted by name) and returns the parsed entries and the problems.
func ReadPorted(dir string) ([]PortedEntry, []string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "ported*.txt"))
	if err != nil {
		return nil, nil, err
	}
	sort.Strings(files)
	var all []PortedEntry
	var problems []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, nil, err
		}
		es, ps := ParsePorted(filepath.Base(f), string(b))
		all = append(all, es...)
		problems = append(problems, ps...)
	}
	return all, problems, nil
}

// GoTestList is the real ListFunc: `go test -list '^<prefix>' ./<pkg>` run in the go/ module.
func GoTestList(pkg, prefix string) ([]string, error) {
	mod, err := ModuleRoot()
	if err != nil {
		return nil, err
	}
	goBin, err := goTool()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(goBin, "test", "-list", "^"+regexp.QuoteMeta(prefix), "./"+strings.TrimPrefix(pkg, "./"))
	cmd.Dir = mod
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOFLAGS=-mod=readonly", "GOPROXY=off")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("go test -list failed: %v\n%s", err, out)
	}
	var names []string
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(l, prefix) && !strings.ContainsAny(l, " \t") {
			names = append(names, l)
		}
	}
	return names, nil
}
