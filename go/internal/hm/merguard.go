package hm

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// The hard limits of a T3 merge (owner decision, T-1988): a judge MERGE merges a T3 PR without the owner unless Check
// finds one of these in the diff or in the PR's state. They are mechanical and err towards the owner: a file that cannot
// be parsed, or an edit that cannot be told from a removal, is a limit.

// Limit is one reason a T3 PR goes to the owner.
type Limit struct {
	Rule string `json:"rule"` // a: deny/ask/hooks | b: guard tests | c: workflows | d: merge rights, guard | e: PR state | f: this gate
	File string `json:"file,omitempty"`
	Why  string `json:"why"`
}

func (l Limit) String() string {
	if l.File == "" {
		return l.Rule + ") " + l.Why
	}
	return l.Rule + ") " + l.File + ": " + l.Why
}

func limitList(limits []Limit) []string {
	out := make([]string, 0, len(limits))
	for _, l := range limits {
		out = append(out, l.String())
	}
	return out
}

// DiffFile is one changed file: its content on the merge base and on the head. A rename is a delete and an add.
type DiffFile struct {
	Path           string
	Old, New       string
	Added, Deleted bool
}

// Diff is what the PR changes.
type Diff []DiffFile

// PRStatus is the PR's state as Check reads it.
type PRStatus struct {
	CI        string   // green | red | pending
	Mergeable string   // MERGEABLE | CONFLICTING | UNKNOWN
	Required  []string // required check names
}

// Check returns the hard limits the PR hits; none means it may merge without the owner.
func Check(diff Diff, st PRStatus) []Limit {
	var out []Limit
	add := func(rule, file, format string, args ...any) {
		out = append(out, Limit{rule, file, fmt.Sprintf(format, args...)})
	}
	var oldJobs, newJobs []string
	for _, f := range diff {
		if isSettings(f.Path) {
			checkSettings(f, add)
		}
		checkGuardTests(f, add)
		checkGuardSource(f, add)
		checkGate(f, add)
		if strings.HasPrefix(f.Path, ".github/workflows/") {
			o, n := workflowJobs(f.Old), workflowJobs(f.New)
			for _, id := range sortedKeys(o) {
				if _, ok := n[id]; !ok {
					add("c", f.Path, "removes job %s", id)
				}
				oldJobs = append(oldJobs, or(o[id], id)) // the check a job reports: its name, else its id
			}
			for id, name := range n {
				newJobs = append(newJobs, or(name, id))
			}
			for _, k := range []struct {
				re   *regexp.Regexp
				what string
			}{{ifFalseRe, "if: false"}, {continueRe, "continue-on-error: true"}} {
				if len(k.re.FindAllString(f.New, -1)) > len(k.re.FindAllString(f.Old, -1)) {
					add("c", f.Path, "adds %s", k.what)
				}
			}
		}
		if strings.HasPrefix(f.Path, "roles/hooks/worker-guard") {
			if gone := removed(codeLines(f.Old), codeLines(f.New)); len(gone) > 0 {
				add("d", f.Path, "removes or edits %d guard line(s), first: %s", len(gone), truncate(gone[0], 80))
			}
		}
	}
	for _, c := range st.Required {
		if hasCheck(oldJobs, c) && !hasCheck(newJobs, c) {
			add("c", ".github/workflows", "removes the required check name %s", c)
		}
	}
	if st.CI != "green" {
		add("e", "", "CI is not all green (%s)", or(st.CI, "unknown"))
	}
	if st.Mergeable != "MERGEABLE" {
		add("e", "", "the PR is not mergeable (%s)", or(st.Mergeable, "unknown"))
	}
	return out
}

type addLimit func(rule, file, format string, args ...any)

func isSettings(p string) bool {
	base := path.Base(p)
	settings := strings.HasPrefix(base, "settings") && strings.HasSuffix(base, ".json")
	switch {
	case strings.HasPrefix(p, "roles/"):
		return settings || strings.HasSuffix(base, ".json.in")
	case strings.HasPrefix(p, "go/internal/hm/assets/"): // the embedded copies of the runner's templates
		return strings.HasSuffix(base, ".json.in")
	case strings.HasPrefix(p, "plugins/"):
		return settings
	}
	return strings.HasPrefix(p, "state/hivemill/settings")
}

// lists collects the entries of every array under the given keys, at any depth.
func lists(v any, keys map[string]bool, out map[string][]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if arr, ok := val.([]any); ok && keys[k] {
				for _, e := range arr {
					b, _ := json.Marshal(e)
					out[k] = append(out[k], string(b))
				}
			}
			lists(val, keys, out)
		}
	case []any:
		for _, val := range x {
			lists(val, keys, out)
		}
	}
}

func settingsLists(content string) (map[string][]string, error) {
	out := map[string][]string{}
	if strings.TrimSpace(content) == "" {
		return out, nil
	}
	var v any
	if err := json.Unmarshal([]byte(content), &v); err != nil {
		return nil, err
	}
	lists(v, map[string]bool{"deny": true, "ask": true, "allow": true}, out)
	hookEntries(v, out)
	defaultModes(v, out)
	return out, nil
}

// hookEntries collects every hook under the top-level hooks key, each with its event and matcher.
func hookEntries(v any, out map[string][]string) {
	top, _ := v.(map[string]any)
	hooks, ok := top["hooks"]
	if !ok {
		return
	}
	add := func(x ...any) {
		b, _ := json.Marshal(x)
		out["hooks"] = append(out["hooks"], string(b))
	}
	events, ok := hooks.(map[string]any)
	if !ok {
		add(hooks)
		return
	}
	for event, val := range events {
		groups, ok := val.([]any)
		if !ok {
			add(event, val)
			continue
		}
		for _, g := range groups {
			m, _ := g.(map[string]any)
			inner, ok := m["hooks"].([]any)
			if !ok {
				add(event, g)
				continue
			}
			for _, h := range inner {
				add(event, m["matcher"], h)
			}
		}
	}
}

// defaultModes collects every defaultMode value, at any depth.
func defaultModes(v any, out map[string][]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if k == "defaultMode" {
				b, _ := json.Marshal(val)
				out[k] = append(out[k], string(b))
			}
			defaultModes(val, out)
		}
	case []any:
		for _, val := range x {
			defaultModes(val, out)
		}
	}
}

var ghMergeRe = regexp.MustCompile(`\bgh\b.*\bpr\b.*\bmerge\b.*--(admin|auto)\b`)

// allowsForcedMerge: the allow entry covers `gh pr merge --admin` or `--auto`: it spells it out, it is all of Bash, or its
// text before the first wildcard is compatible with `gh pr merge`.
func allowsForcedMerge(entry string) bool {
	var e string
	if json.Unmarshal([]byte(entry), &e) != nil {
		return false
	}
	if e == "Bash" || ghMergeRe.MatchString(e) {
		return true
	}
	cmd, ok := strings.CutPrefix(e, "Bash(")
	if !ok {
		return false
	}
	lit, _, wild := strings.Cut(strings.ReplaceAll(strings.TrimSuffix(cmd, ")"), ":*", "*"), "*")
	const merge = "gh pr merge"
	return wild && (strings.HasPrefix(merge, lit) || strings.HasPrefix(lit, merge+" "))
}

// checkSettings: limit a (a deny, ask or hook entry is gone or changed, or defaultMode changed) and limit d (a new allow
// of gh pr merge --admin/--auto).
func checkSettings(f DiffFile, add addLimit) {
	o, oerr := settingsLists(f.Old)
	n, nerr := settingsLists(f.New)
	if oerr != nil || nerr != nil {
		add("a", f.Path, "not readable as JSON, so its deny, ask and hooks entries cannot be compared")
		return
	}
	if len(removed(o["defaultMode"], n["defaultMode"]))+len(removed(n["defaultMode"], o["defaultMode"])) > 0 {
		add("a", f.Path, "changes defaultMode")
	}
	for _, k := range []string{"deny", "ask", "hooks"} {
		for _, e := range removed(o[k], n[k]) {
			add("a", f.Path, "removes or edits %s entry %s", k, truncate(e, 100))
		}
	}
	for _, e := range removed(n["allow"], o["allow"]) {
		if allowsForcedMerge(e) {
			add("d", f.Path, "adds an allow that covers gh pr merge --admin or --auto: %s", truncate(e, 100))
		}
	}
}

// removed returns the entries of old that new has fewer of.
func removed(old, new []string) []string {
	have := map[string]int{}
	for _, x := range new {
		have[x]++
	}
	var out []string
	for _, x := range old {
		if have[x] > 0 {
			have[x]--
			continue
		}
		out = append(out, x)
	}
	return out
}

// codeLines are the non-blank, non-comment lines with runs of space collapsed (a realigned line is not a removal).
func codeLines(s string) []string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.Join(strings.Fields(ln), " ")
		if ln != "" && !strings.HasPrefix(ln, "#") && !strings.HasPrefix(ln, "//") {
			out = append(out, ln)
		}
	}
	return out
}

var (
	testFuncRe = regexp.MustCompile(`(?m)^\s*(?:func (?:\([^)]*\) )?((?:Test|Fuzz)\w*)|(?:async )?def (test\w*))`)
	// a line with a string literal, or a bare table row: what a test case looks like in Go and Python
	caseRe = regexp.MustCompile("[\"'`]|^[{(\\[].*,$")
)

func isGuardTest(p string) bool {
	base := path.Base(p)
	switch {
	case strings.HasPrefix(p, "go/internal/guard/"):
		return strings.HasSuffix(p, "_test.go") || strings.Contains(p, "/testdata/")
	case strings.HasPrefix(p, "go/internal/factorytest/guard"), strings.HasPrefix(p, "go/cmd/worker-guard/"):
		return strings.HasSuffix(p, "_test.go") || strings.Contains(p, "/testdata/")
	case strings.HasPrefix(p, "roles/hooks/"):
		return strings.Contains(base, "test")
	case strings.HasPrefix(p, "tests/"):
		return strings.Contains(base, "guard") && strings.HasSuffix(base, ".py")
	}
	return false
}

var skipRe = regexp.MustCompile(`\.Skip(f|Now)?\(`)

// checkGuardSkips: limit b (a guard test file gains a skip or a new build constraint), a new file included.
func checkGuardSkips(f DiffFile, add addLimit) {
	if !strings.HasSuffix(f.Path, ".go") {
		return
	}
	skips := func(s string) int { return len(skipRe.FindAllString(strings.Join(codeLines(s), "\n"), -1)) }
	if skips(f.New) > skips(f.Old) {
		add("b", f.Path, "adds a test skip")
	}
	builds := func(s string) []string {
		var out []string
		for _, ln := range strings.Split(s, "\n") {
			if ln = strings.Join(strings.Fields(ln), " "); strings.HasPrefix(ln, "//go:build") {
				out = append(out, ln)
			}
		}
		return out
	}
	if added := removed(builds(f.New), builds(f.Old)); len(added) > 0 {
		add("b", f.Path, "adds a build constraint: %s", truncate(added[0], 80))
	}
}

// checkGuardSource: limit d for the Go guard, the default guard: any change that is not a test or test data.
func checkGuardSource(f DiffFile, add addLimit) {
	if (strings.HasPrefix(f.Path, "go/internal/guard/") || strings.HasPrefix(f.Path, "go/cmd/worker-guard/")) && !isGuardTest(f.Path) {
		add("d", f.Path, "changes the Go guard")
	}
}

const configGo = "go/internal/hm/config.go"

var t3DefaultRe = regexp.MustCompile(`t3_auto_merge"[^,\n]*`)

// checkGate: limit f (the PR changes this gate, the tier classification or the t3_auto_merge default).
func checkGate(f DiffFile, add addLimit) {
	switch f.Path {
	case "go/internal/hm/merguard.go":
		add("f", f.Path, "changes the merge guard")
	case "go/internal/hm/tierrules.go", "go/internal/hm/assets/tier_rules.json":
		add("f", f.Path, "changes the tier classification")
	case configGo:
		value := func(s string) []string { return t3DefaultRe.FindAllString(strings.Join(codeLines(s), "\n"), -1) }
		o, n := value(f.Old), value(f.New)
		if len(removed(o, n))+len(removed(n, o)) > 0 {
			add("f", f.Path, "changes the t3_auto_merge default")
		}
	}
}

// checkGuardTests: limit b (a guard test file, test function or case is gone, or a skip is added).
func checkGuardTests(f DiffFile, add addLimit) {
	if f.Path == "ci/guard_corpus.json" {
		checkCorpus(f, add)
		return
	}
	if !isGuardTest(f.Path) {
		return
	}
	checkGuardSkips(f, add)
	if f.Added {
		return
	}
	if f.Deleted {
		add("b", f.Path, "deletes a guard test file")
		return
	}
	names := func(s string) []string {
		var out []string
		for _, m := range testFuncRe.FindAllStringSubmatch(s, -1) {
			out = append(out, m[1]+m[2])
		}
		return out
	}
	for _, n := range removed(names(f.Old), names(f.New)) {
		add("b", f.Path, "removes test %s", n)
	}
	cases := func(s string) []string {
		var out []string
		for _, ln := range codeLines(s) {
			if strings.Contains(f.Path, "/testdata/") || caseRe.MatchString(ln) {
				out = append(out, ln)
			}
		}
		return out
	}
	if gone := removed(cases(f.Old), cases(f.New)); len(gone) > 0 {
		add("b", f.Path, "removes or edits %d test case line(s), first: %s", len(gone), truncate(gone[0], 80))
	}
}

func checkCorpus(f DiffFile, add addLimit) {
	read := func(s string) (map[string][]string, error) {
		out := map[string][]string{}
		var v map[string]any
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			return nil, err
		}
		for _, k := range []string{"must_block", "must_allow"} {
			arr, _ := v[k].([]any)
			for _, e := range arr {
				b, _ := json.Marshal(e) // map keys come out sorted, so a reformatted entry is the same entry
				out[k] = append(out[k], string(b))
			}
		}
		return out, nil
	}
	if f.Added {
		return
	}
	if f.Deleted {
		add("b", f.Path, "deletes the guard corpus")
		return
	}
	o, oerr := read(f.Old)
	n, nerr := read(f.New)
	if oerr != nil || nerr != nil {
		add("b", f.Path, "not readable as JSON, so its cases cannot be compared")
		return
	}
	for _, k := range []string{"must_block", "must_allow"} {
		if gone := removed(o[k], n[k]); len(gone) > 0 {
			add("b", f.Path, "removes or edits %d %s case(s), first: %s", len(gone), k, truncate(gone[0], 80))
		}
	}
}

var (
	ifFalseRe  = regexp.MustCompile(`(?m)(?:^\s*(?:-\s+)?|[{,]\s*)["']?if["']?\s*:\s*["']?(?:\$\{\{\s*)?false\b`)
	continueRe = regexp.MustCompile(`(?m)(?:^\s*(?:-\s+)?|[{,]\s*)["']?continue-on-error["']?\s*:\s*["']?(?:true\b|\$\{\{)`)
	jobKeyRe   = regexp.MustCompile(`^( +)["']?([\w-]+)["']?\s*:`)
	nameRe     = regexp.MustCompile(`^( +)name\s*:\s*(.+?)\s*$`)
)

// workflowJobs maps each job id under `jobs:` to its name ("" without one). It reads the block layout by indentation;
// the module has no YAML parser.
func workflowJobs(content string) map[string]string {
	jobs := map[string]string{}
	in, indent, prop, id := false, "", "", ""
	for _, ln := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(ln)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(ln, " ") {
			in = strings.HasPrefix(ln, "jobs:")
			indent, prop, id = "", "", ""
			continue
		}
		if !in {
			continue
		}
		depth := ln[:len(ln)-len(strings.TrimLeft(ln, " "))]
		if indent == "" {
			indent = depth
		}
		if depth == indent {
			prop = ""
			if m := jobKeyRe.FindStringSubmatch(ln); m != nil {
				id = m[2]
				jobs[id] = ""
			}
			continue
		}
		if prop == "" {
			prop = depth // the indentation of the job's own keys; a step's name sits deeper
		}
		if m := nameRe.FindStringSubmatch(ln); m != nil && id != "" && depth == prop {
			jobs[id] = strings.Trim(m[2], `"'`)
		}
	}
	return jobs
}

// hasCheck: some job's check name reports the check (a matrix job reports "name (…)" or "prefix ${{ … }}").
func hasCheck(jobs []string, check string) bool {
	for _, j := range jobs {
		if j == check || strings.HasPrefix(j, check+" (") || strings.HasPrefix(check, j+" (") {
			return true
		}
		if i := strings.Index(j, "${{"); i > 0 && strings.HasPrefix(check, j[:i]) {
			return true
		}
	}
	return false
}

// readDiff reads what head changes since its merge base with origin/base, with both sides of every changed file.
func (r *Runner) readDiff(wt, base, head string) (Diff, error) {
	git := func(args ...string) (string, error) {
		rc, out, errs := r.Tools.Git(wt, args...)
		if rc != 0 {
			return "", fmt.Errorf("git %s: exit %d: %s", args[0], rc, firstLine(errs))
		}
		return out, nil
	}
	mb, err := git("merge-base", "origin/"+base, head)
	if err != nil {
		return nil, err
	}
	names, err := git("diff", "--name-status", "--no-renames", "-z", mb, head)
	if err != nil {
		return nil, err
	}
	var d Diff
	parts := strings.Split(strings.TrimRight(names, "\x00"), "\x00")
	for i := 0; i+1 < len(parts); i += 2 {
		f := DiffFile{Path: parts[i+1], Added: parts[i] == "A", Deleted: parts[i] == "D"}
		if !limited(f.Path) {
			d = append(d, f)
			continue
		}
		if !f.Added {
			if f.Old, err = git("show", mb+":"+f.Path); err != nil {
				return nil, err
			}
		}
		if !f.Deleted {
			if f.New, err = git("show", head+":"+f.Path); err != nil {
				return nil, err
			}
		}
		d = append(d, f)
	}
	sort.Slice(d, func(i, j int) bool { return d[i].Path < d[j].Path })
	return d, nil
}

// limited: Check reads the content of this path, so readDiff fetches it.
func limited(p string) bool {
	return isSettings(p) || isGuardTest(p) || p == configGo || p == "ci/guard_corpus.json" || strings.HasPrefix(p, ".github/workflows/") ||
		strings.HasPrefix(p, "roles/hooks/worker-guard")
}
