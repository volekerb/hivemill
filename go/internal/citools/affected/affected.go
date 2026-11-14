// Package affected picks the test modules a git diff can affect: the Go port of ci/affected.py (T-0519).
//
// The selection rules (1 to 5, quick mode, exit codes) are the ones in the docstring of ci/affected.py, which stays the
// reference until the P5e switch; parity tests run both on the same fixture repos. Python source is read with a small
// lexer (pysrc.go) instead of ast. Running the selected tests still goes through the Python unittest loader (the tests
// are Python), started as `python3 -c` with the ci/affected.py of the same checkout, so quick mode's child process
// groups, the hard budget and the host-wide test slot (tests/_slot.py) behave exactly as before.
//
// Package tier (ci-tier) and risktier use ChangedDefs, Mentions, Select and Git from here.
package affected

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	DefaultSmoke = "test_db_layout.py test_inbox.py test_judge.py"
	Cap          = 0.40
	HubCalls     = 15
	Bin          = "bin/factory_cli.py"
	ShardTest    = "test_shard.py"
	QuickBudgetS = 120
	ExitBudget   = 4
	ExitLoad     = 5
)

var fullDefs = map[string]bool{"SCHEMA": true, "DEFAULT_CONFIG": true, "main": true, "db": true}

// TestTreeLints are the modules that scan every tests/*.py or every tracked file (see ci/affected.py).
var TestTreeLints = []string{"test_guard_impl_coverage.py", "test_git_env_hermetic.py", "test_repo_hygiene.py",
	"test_no_duplicate_defs.py", "test_skilleval_vendor.py", "test_skilleval_tasks.py", "test_scrub_clean.py"}

// Error is a git or parse failure; the message says what to do next.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// Git runs `git -C root -c core.fsmonitor=false args` and returns stdout.
func Git(root string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", root, "-c", "core.fsmonitor=false"}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		code := -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		return "", &Error{fmt.Sprintf("git %s failed (exit %d): %s; check that the base ref exists (git fetch origin)",
			strings.Join(args, " "), code, strings.TrimSpace(stderr.String()))}
	}
	return string(out), nil
}

// Exists reports whether rev:path exists (git cat-file -e).
func Exists(root, rev, p string) bool {
	return exec.Command("git", "-C", root, "cat-file", "-e", rev+":"+p).Run() == nil
}

// ChangedFile is a path of `git diff base...head` and whether it exists at head; renames show as delete + add.
type ChangedFile struct {
	Path   string
	Exists bool
}

func ChangedFiles(root, base, head string) ([]ChangedFile, error) {
	out, err := Git(root, "diff", "--name-status", "--no-renames", base+"..."+head)
	if err != nil {
		return nil, err
	}
	var res []ChangedFile
	for _, line := range pyLines(out) {
		st, p, _ := strings.Cut(line, "\t")
		res = append(res, ChangedFile{p, !strings.HasPrefix(st, "D")})
	}
	return res, nil
}

// pyLines is str.splitlines for git output: split on \n, drop the final empty piece.
func pyLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

// AllModules lists tests_dir/test_*.py names, sorted.
func AllModules(testsDir string) ([]string, error) {
	ents, err := os.ReadDir(testsDir)
	if err != nil {
		return nil, &Error{fmt.Sprintf("cannot list %s: %v; run from a repository checkout", testsDir, err)}
	}
	var out []string
	for _, e := range ents {
		n := e.Name()
		if strings.HasPrefix(n, "test_") && strings.HasSuffix(n, ".py") {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, nil
}

// SmokeReader returns the value of config key affected_smoke from the factory DB; ok=false when there is none. Tests
// replace it. The default asks python3's sqlite3 module (read-only), like ci/affected.py; the Go CI tools may not
// link a SQLite driver.
var SmokeReader = readSmokeConfig

func readSmokeConfig() (string, bool) {
	home := os.Getenv("FACTORY_HOME")
	if home == "" {
		home = "~/factory"
	}
	if strings.HasPrefix(home, "~") {
		if h, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(h, home[1:])
		}
	}
	db := filepath.Join(home, "db", "factory.db")
	if _, err := os.Stat(db); err != nil {
		return "", false
	}
	const snippet = `import sqlite3, sys
try:
    con = sqlite3.connect("file:%s?mode=ro" % sys.argv[1], uri=True, timeout=2)
    try:
        row = con.execute("SELECT value FROM config WHERE key='affected_smoke'").fetchone()
    finally:
        con.close()
except sqlite3.Error:
    sys.exit(3)
if row and str(row[0]).strip():
    sys.stdout.write(str(row[0]))
else:
    sys.exit(3)
`
	out, err := exec.Command("python3", "-I", "-c", snippet, db).Output()
	if err != nil {
		return "", false // no table, no key, no python3: the default (as ci/affected.py on sqlite errors)
	}
	return string(out), true
}

// SmokeSet is config affected_smoke (else the default); unknown modules are dropped.
func SmokeSet(modules []string) map[string]bool {
	val := DefaultSmoke
	if v, ok := SmokeReader(); ok && strings.TrimSpace(v) != "" {
		val = v
	}
	known := map[string]bool{}
	for _, m := range modules {
		known[m] = true
	}
	out := map[string]bool{}
	for _, m := range strings.Fields(val) {
		if known[m] {
			out[m] = true
		}
	}
	return out
}

func readModule(testsDir, m string) (string, error) {
	b, err := os.ReadFile(filepath.Join(testsDir, m))
	if err != nil {
		return "", &Error{fmt.Sprintf("cannot read %s/%s: %v", testsDir, m, err)}
	}
	return string(b), nil
}

// Mentions returns the modules whose source contains needle (as a whole word if word).
func Mentions(testsDir string, modules []string, needle string, word bool) (map[string]bool, error) {
	hit := map[string]bool{}
	for _, m := range modules {
		text, err := readModule(testsDir, m)
		if err != nil {
			return nil, err
		}
		if word {
			if containsWord(text, needle) {
				hit[m] = true
			}
		} else if strings.Contains(text, needle) {
			hit[m] = true
		}
	}
	return hit, nil
}

// containsWord is a search for needle not preceded or followed by [A-Za-z0-9_].
func containsWord(text, needle string) bool {
	return findWord(text, needle, 0, len(text), "") >= 0
}

// findWord finds needle in text[from:to] with word boundaries checked against the whole text; extraBefore lists
// further bytes that may not precede the match. It returns the match start or -1.
func findWord(text, needle string, from, to int, extraBefore string) int {
	if needle == "" {
		return -1
	}
	i := from
	for i <= to-len(needle) {
		k := strings.Index(text[i:to], needle)
		if k < 0 {
			return -1
		}
		s := i + k
		e := s + len(needle)
		okB := s == 0 || (!isWordByte(text[s-1]) && !strings.ContainsRune(extraBefore, rune(text[s-1])))
		okA := e >= len(text) || !isWordByte(text[e])
		if okB && okA {
			return s
		}
		i = s + 1
	}
	return -1
}

// ImportsModule reports which modules import the python file p as a package member: `from web.views import home`
// (also in a name list or parentheses) or `import web.views.home` (ci/affected.py imports_module).
func ImportsModule(testsDir string, modules []string, p string) (map[string]bool, error) {
	hit := map[string]bool{}
	if !strings.HasSuffix(p, ".py") {
		return hit, nil
	}
	parts := strings.Split(strings.TrimSuffix(p, ".py"), "/")
	if parts[len(parts)-1] == "__init__" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) < 2 {
		return hit, nil
	}
	pkg, name := strings.Join(parts[:len(parts)-1], "."), parts[len(parts)-1]
	fromRe := regexp.MustCompile(`(?m)^[ \t]*from[ \t]+` + regexp.QuoteMeta(pkg) + `[ \t]+import[ \t]+`)
	impRe := regexp.MustCompile(`(?m)^[ \t]*import[ \t]+`)
	for _, m := range modules {
		text, err := readModule(testsDir, m)
		if err != nil {
			return nil, err
		}
		if importsFrom(text, fromRe, name) || importsDotted(text, impRe, pkg+"."+name) {
			hit[m] = true
		}
	}
	return hit, nil
}

func importsFrom(text string, re *regexp.Regexp, name string) bool {
	for _, loc := range re.FindAllStringIndex(text, -1) {
		s := loc[1]
		// alternative 1: a parenthesised list, up to the first ")" (may span lines)
		if s < len(text) && text[s] == '(' {
			e := strings.IndexByte(text[s:], ')')
			end := len(text)
			if e >= 0 {
				end = s + e
			}
			if findWord(text, name, s+1, end, "") >= 0 {
				return true
			}
		}
		// alternative 2: the rest of the line up to a comment
		end := s
		for end < len(text) && text[end] != '#' && text[end] != '\n' {
			end++
		}
		if findWord(text, name, s, end, "") >= 0 {
			return true
		}
	}
	return false
}

func importsDotted(text string, re *regexp.Regexp, dotted string) bool {
	for _, loc := range re.FindAllStringIndex(text, -1) {
		s := loc[1]
		end := s
		for end < len(text) && text[end] != '#' && text[end] != '\n' {
			end++
		}
		if findWord(text, dotted, s, end, ".") >= 0 {
			return true
		}
	}
	return false
}

// full is a FULL selection; the message is the triggering rule.
type full struct{ msg string }

// Selection is the result of Select: Modules (sorted) unless Full, with Note the triggering rule or the joined notes.
type Selection struct {
	Modules []string
	Full    bool
	Note    string
}

func showFile(root, rev, p string) (string, error) {
	if !Exists(root, rev, p) {
		return "", nil
	}
	return Git(root, "show", rev+":"+p)
}

func selectBinFactory(root, base, head, testsDir string, modules []string) (map[string]bool, *full, error) {
	baseSrc, err := showFile(root, base, Bin)
	if err != nil {
		return nil, nil, err
	}
	headSrc, err := showFile(root, head, Bin)
	if err != nil {
		return nil, nil, err
	}
	names, err := ChangedDefs(baseSrc, headSrc)
	var calls map[string]int
	if err == nil && headSrc != "" {
		calls, err = CallSites(headSrc)
	}
	if err != nil {
		var pe *ParseError
		if errors.As(err, &pe) {
			return nil, &full{fmt.Sprintf("rule 3: %s does not parse (%s)", Bin, pe.Msg)}, nil
		}
		return nil, nil, err
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	sel := map[string]bool{}
	for _, n := range sorted {
		if n == "<stmt>" || fullDefs[n] {
			what := n
			if n == "<stmt>" {
				what = "a top-level statement"
			}
			return nil, &full{fmt.Sprintf("rule 3: %s changed (%s)", what, Bin)}, nil
		}
		if calls[n] >= HubCalls {
			return nil, &full{fmt.Sprintf("rule 3: %s has %d call sites (>= %d)", n, calls[n], HubCalls)}, nil
		}
		h, err := Mentions(testsDir, modules, n, true)
		if err != nil {
			return nil, nil, err
		}
		union(sel, h)
		if strings.HasPrefix(n, "cmd_") && len(n) > 4 {
			h, err := Mentions(testsDir, modules, n[4:], true)
			if err != nil {
				return nil, nil, err
			}
			union(sel, h)
		}
	}
	return sel, nil, nil
}

func union(dst, src map[string]bool) {
	for k := range src {
		dst[k] = true
	}
}

// Select is ci/affected.py select: the modules the diff base...head can affect, or FULL with the rule that triggered it.
func Select(root, base, head, testsDir string) (Selection, error) {
	if testsDir == "" {
		testsDir = filepath.Join(root, "tests")
	}
	modules, err := AllModules(testsDir)
	if err != nil {
		return Selection{}, err
	}
	sel, notes, fl, err := selectRules(root, base, head, testsDir, modules)
	if err != nil {
		return Selection{}, err
	}
	if fl != nil {
		return Selection{Full: true, Note: fl.msg}, nil
	}
	union(sel, SmokeSet(modules))
	if float64(len(sel)) > Cap*float64(len(modules)) {
		return Selection{Full: true, Note: fmt.Sprintf("rule 5: %d of %d modules selected (> %d%%)", len(sel), len(modules), int(Cap*100))}, nil
	}
	out := make([]string, 0, len(sel))
	for m := range sel {
		out = append(out, m)
	}
	sort.Strings(out)
	return Selection{Modules: out, Note: strings.Join(notes, "; ")}, nil
}

var (
	testModuleRe = regexp.MustCompile(`^tests/(test_[^/]+\.py)$`)
	ciPyRe       = regexp.MustCompile(`^ci/([^/]+)\.py$`)
)

func selectRules(root, base, head, testsDir string, modules []string) (map[string]bool, []string, *full, error) {
	files, err := ChangedFiles(root, base, head)
	if err != nil {
		return nil, nil, nil, err
	}
	have := map[string]bool{}
	for _, m := range modules {
		have[m] = true
	}
	sel := map[string]bool{}
	var notes []string
	for _, f := range files {
		if strings.HasPrefix(f.Path, "tests/") { // rule 1c
			for _, m := range TestTreeLints {
				if have[m] {
					sel[m] = true
				}
			}
			break
		}
	}
	add := func(h map[string]bool, err error) error {
		if err != nil {
			return err
		}
		union(sel, h)
		return nil
	}
	for _, f := range files {
		p := f.Path
		if p == "tests/harness.py" || p == "tests/_slot.py" || strings.HasPrefix(p, "tests/stubs/") {
			return nil, nil, &full{fmt.Sprintf("rule 1: %s changed", p)}, nil
		}
		if m := testModuleRe.FindStringSubmatch(p); m != nil {
			if f.Exists && have[m[1]] {
				sel[m[1]] = true
			} else if !f.Exists && have[ShardTest] {
				sel[ShardTest] = true // rule 1b
			}
			continue
		}
		if p == "ci/shard_timings.json" && have[ShardTest] {
			sel[ShardTest] = true // rule 1b; falls through to the generic rule
		}
		if c := ciPyRe.FindStringSubmatch(p); c != nil {
			x := c[1]
			if err := add(Mentions(testsDir, modules, "ci/"+x, false)); err != nil {
				return nil, nil, nil, err
			}
			if err := add(Mentions(testsDir, modules, x+".py", false)); err != nil {
				return nil, nil, nil, err
			}
			if have["test_"+x+".py"] {
				sel["test_"+x+".py"] = true
			}
			continue
		}
		if p == Bin {
			h, fl, err := selectBinFactory(root, base, head, testsDir, modules)
			if err != nil || fl != nil {
				return nil, nil, fl, err
			}
			union(sel, h)
			continue
		}
		if strings.HasPrefix(p, "roles/") {
			if err := add(Mentions(testsDir, modules, "roles/", false)); err != nil {
				return nil, nil, nil, err
			}
			if err := add(Mentions(testsDir, modules, path.Base(p), false)); err != nil {
				return nil, nil, nil, err
			}
			continue
		}
		if strings.HasPrefix(p, "docs/") || strings.HasSuffix(p, ".md") {
			notes = append(notes, "docs only: "+p)
			continue
		}
		hit := map[string]bool{} // any other file: modules that mention its path, file name or dotted module path
		stem := strings.TrimSuffix(p, ".py")
		for _, needle := range []string{p, path.Base(p), strings.ReplaceAll(stem, "/", ".")} {
			h, err := Mentions(testsDir, modules, needle, false)
			if err != nil {
				return nil, nil, nil, err
			}
			union(hit, h)
		}
		h, err := ImportsModule(testsDir, modules, p)
		if err != nil {
			return nil, nil, nil, err
		}
		union(hit, h)
		if len(hit) == 0 {
			return nil, nil, &full{fmt.Sprintf("no test module mentions %s; run the full suite", p)}, nil
		}
		union(sel, hit)
	}
	return sel, notes, nil, nil
}
