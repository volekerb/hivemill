// Package risktier is the Go port of ci/risk_tier.py (G-022 #1): the risk tier T0..T3 of a PR diff, read from the
// rules in ci/tier_rules.json.
//
//	T3  any sensitive path or sensitive top-level def (rules sensitive_*), or more than large_lines changed lines
//	T0  docs only (rules docs_*)
//	T1  tests only, or at most small_lines changed lines
//	T2  everything else
//
// ci/risk_tier.py has no command line (bin/factory imports it); the ci-risk-tier command is new and prints the tier on
// the first line and one "reason: ..." line per T3 reason. It fails closed: a sensitive file that cannot be read is
// T3 ("<unparsable>"), a git or rules error is exit 2. Python top-level defs come from package affected (the same
// reader as ci-affected).
package risktier

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"factory/go/internal/citools/affected"
)

// Tiers in ascending order.
var Tiers = []string{"T0", "T1", "T2", "T3"}

// Rules is ci/tier_rules.json.
type Rules struct {
	DocsGlobs            []string            `json:"docs_globs"`
	DocsExcludedPrefixes []string            `json:"docs_excluded_prefixes"`
	TestsGlobs           []string            `json:"tests_globs"`
	SensitivePaths       []string            `json:"sensitive_paths"`
	SensitiveDefs        map[string][]string `json:"sensitive_defs"`
	SmallLines           int                 `json:"small_lines"`
	LargeLines           int                 `json:"large_lines"`
}

// Error is a rules or git failure; the message says what to do next.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

var requiredKeys = []string{"docs_globs", "docs_excluded_prefixes", "tests_globs", "sensitive_paths", "sensitive_defs", "small_lines", "large_lines"}

// LoadRules reads and validates a rules file.
func LoadRules(path string) (Rules, error) {
	fail := func(why error) (Rules, error) {
		return Rules{}, &Error{fmt.Sprintf("cannot load tier rules %s: %v; restore ci/tier_rules.json on the main checkout", path, why)}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fail(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return fail(err)
	}
	for _, k := range requiredKeys {
		if _, ok := keys[k]; !ok {
			return fail(fmt.Errorf("missing key %q", k))
		}
	}
	var r Rules
	if err := json.Unmarshal(raw, &r); err != nil {
		return fail(err)
	}
	return r, nil
}

// MaxTier is the higher of two tiers; an empty one counts as lowest.
func MaxTier(a, b string) string {
	idx := func(t string) int {
		for i, x := range Tiers {
			if x == t {
				return i
			}
		}
		return -1
	}
	if idx(b) > idx(a) {
		return b
	}
	return a
}

func match(path, pat string) bool {
	if strings.HasSuffix(pat, "/**") {
		return strings.HasPrefix(path, pat[:len(pat)-2])
	}
	if strings.HasSuffix(pat, "/*.md") && strings.Count(path, "/") != strings.Count(pat, "/") {
		return false // only `<dir>/*.md` is one segment; other globs keep fnmatch's `*` crossing `/`
	}
	return affected.FnMatch(path, pat)
}

func anyMatch(path string, pats []string) bool {
	for _, p := range pats {
		if match(path, p) {
			return true
		}
	}
	return false
}

// SensitiveDefs returns the sorted "path:def" for each changed top-level def matching rules.SensitiveDefs. diff maps
// path to {base source, head source}; an unreadable source counts as a hit on "<unparsable>".
func SensitiveDefs(diff map[string][2]string, rules Rules) ([]string, error) {
	hits := map[string]bool{}
	for path, pats := range rules.SensitiveDefs {
		srcs, ok := diff[path]
		if !ok {
			continue
		}
		names, err := affected.ChangedDefs(srcs[0], srcs[1])
		if err != nil {
			var pe *affected.ParseError
			if errors.As(err, &pe) {
				hits[path+":<unparsable>"] = true
				continue
			}
			return nil, err
		}
		for n := range names {
			if anyMatch(n, pats) {
				hits[path+":"+n] = true
			}
		}
	}
	out := make([]string, 0, len(hits))
	for h := range hits {
		out = append(out, h)
	}
	sort.Strings(out)
	return out, nil
}

// Reasons says why the diff is T3: empty when it is not.
func Reasons(diff map[string][2]string, paths []string, lines int, rules Rules) ([]string, error) {
	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)
	var out []string
	for _, p := range sorted {
		if anyMatch(p, rules.SensitivePaths) {
			out = append(out, "sensitive path "+p)
		}
	}
	defs, err := SensitiveDefs(diff, rules)
	if err != nil {
		return nil, err
	}
	for _, d := range defs {
		out = append(out, "sensitive def "+d)
	}
	if lines > rules.LargeLines {
		out = append(out, fmt.Sprintf("%d changed lines (> %d)", lines, rules.LargeLines))
	}
	return out, nil
}

// Tier classifies. diff: {path: {base src, head src}} for the files named in rules.SensitiveDefs; paths: every changed
// path (renames as delete + add); lines: added + deleted lines.
func Tier(diff map[string][2]string, paths []string, lines int, rules Rules) (string, error) {
	why, err := Reasons(diff, paths, lines, rules)
	if err != nil {
		return "", err
	}
	if len(why) > 0 {
		return "T3", nil
	}
	docs := func(p string) bool {
		if !anyMatch(p, rules.DocsGlobs) {
			return false
		}
		for _, x := range rules.DocsExcludedPrefixes {
			if strings.HasPrefix(p, x) {
				return false
			}
		}
		return true
	}
	allOf := func(f func(string) bool) bool {
		for _, p := range paths {
			if !f(p) {
				return false
			}
		}
		return len(paths) > 0
	}
	if allOf(docs) {
		return "T0", nil
	}
	if allOf(func(p string) bool { return anyMatch(p, rules.TestsGlobs) }) || lines <= rules.SmallLines {
		return "T1", nil
	}
	return "T2", nil
}

func git(root string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", &Error{fmt.Sprintf("git %s failed: %s", strings.Join(args, " "), strings.TrimSpace(stderr.String()))}
	}
	return string(out), nil
}

func show(root, rev, path string) string {
	out, err := exec.Command("git", "-C", root, "show", rev+":"+path).Output()
	if err != nil {
		return "" // the file does not exist there (added or deleted by the diff)
	}
	return string(out)
}

// ClassifyRange returns (tier, reasons) for `git diff base...head` in the repo at root.
func ClassifyRange(root, base, head string, rules Rules) (string, []string, error) {
	for _, ref := range []string{base, head} {
		if ref == "" || strings.HasPrefix(ref, "-") {
			return "", nil, &Error{fmt.Sprintf("bad ref %q: a ref must not start with '-'", ref)}
		}
	}
	out, err := git(root, "diff", "--numstat", "--no-renames", "-z", base+"..."+head)
	if err != nil {
		return "", nil, err
	}
	var paths []string
	lines := 0
	for _, ln := range strings.Split(out, "\x00") {
		if ln == "" {
			continue
		}
		parts := strings.SplitN(ln, "\t", 3)
		if len(parts) < 3 {
			return "", nil, &Error{fmt.Sprintf("malformed numstat entry %q", ln)}
		}
		paths = append(paths, parts[2])
		if parts[0] != "-" {
			a, err1 := strconv.Atoi(parts[0])
			d, err2 := strconv.Atoi(parts[1])
			if err1 != nil || err2 != nil {
				return "", nil, &Error{fmt.Sprintf("malformed numstat entry %q", ln)}
			}
			lines += a + d
		} else {
			lines += rules.LargeLines + 1 // binary file: size unknown, treat as large
		}
	}
	diff := map[string][2]string{}
	mergeBase := ""
	inPaths := map[string]bool{}
	for _, p := range paths {
		inPaths[p] = true
	}
	var defPaths []string
	for p := range rules.SensitiveDefs {
		defPaths = append(defPaths, p)
	}
	sort.Strings(defPaths)
	for _, p := range defPaths {
		if !inPaths[p] {
			continue
		}
		if mergeBase == "" {
			mb, err := git(root, "merge-base", base, head)
			if err != nil {
				return "", nil, err
			}
			mergeBase = strings.TrimSpace(mb)
		}
		diff[p] = [2]string{show(root, mergeBase, p), show(root, head, p)}
	}
	t, err := Tier(diff, paths, lines, rules)
	if err != nil {
		return "", nil, err
	}
	why, err := Reasons(diff, paths, lines, rules)
	return t, why, err
}
