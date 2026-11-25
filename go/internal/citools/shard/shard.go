// Package shard is the Go port of ci/shard.py: pick, run and verify a deterministic slice of tests/test_*.py.
//
//	ci-shard list [--shard I --total N]     module file names, one per line (I is 1-based)
//	ci-shard run --shard I --total N --out F [--only L] [--quarantine Q] [--today D]   run that slice (tests in the quarantine list Q, default ci/quarantine.json, are left out), write its report (JSON) to F
//	ci-shard verify --total N --dir D [--only L]          check the shard reports in D cover every module once
//	ci-shard timings --dir D [--out F]      merge the reports' per-module seconds into a timing file
//
// Selection (greedy longest-first packing onto the lightest shard, median weight for unknown modules, the
// test-id split of test_guard_diff.py) and verification are pure Go and give the same shards as ci/shard.py. The tests
// are Python unittest modules, so running them (and listing a split module's test ids) goes through python3 with the
// same loader as ci/shard.py. Every subcommand takes --root (default: the git top level of the current directory);
// the timing file is <root>/ci/shard_timings.json.
package shard

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"factory/go/internal/citools/affected"
)

// SplitTestIDsModules are modules whose test ids are split across all shards when their timing alone exceeds the
// pack limit (T-1168). Keep in sync with ci/shard_timings.json and the tests/ tree.
var SplitTestIDsModules = map[string]bool{"test_guard_diff.py": true}

// Error is a bad shard argument or an inconsistent set of shard reports.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

func errf(format string, a ...any) error { return &Error{fmt.Sprintf(format, a...)} }

// AllModules lists tests_dir/test_*.py, sorted.
func AllModules(testsDir string) ([]string, error) {
	ents, err := os.ReadDir(testsDir)
	if err != nil {
		return nil, errf("cannot list %s: %v; run from a repository checkout or pass --root", testsDir, err)
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

// LoadTimings reads {module: seconds}; an absent file is an empty map.
func LoadTimings(path string) (map[string]float64, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]float64{}, nil
	}
	if err != nil {
		return nil, errf("unreadable timing file %s: %v; regenerate it (see docs/tests-and-ci.md)", path, err)
	}
	var data map[string]float64
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, errf("timing file %s must map module name to seconds (%v); regenerate it (see docs/tests-and-ci.md)", path, err)
	}
	return data, nil
}

func median(xs []float64) float64 {
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// Weights is {module: seconds}; modules without a timing get the median of the known ones (1.0 if none).
func Weights(modules []string, timings map[string]float64) map[string]float64 {
	var known []float64
	for _, m := range modules {
		if t, ok := timings[m]; ok {
			known = append(known, t)
		}
	}
	med := 1.0
	if len(known) > 0 {
		med = median(known)
	}
	out := map[string]float64{}
	for _, m := range modules {
		if t, ok := timings[m]; ok {
			out[m] = t
		} else {
			out[m] = med
		}
	}
	return out
}

// pySum is Python 3.12+ sum() over floats (Neumaier compensated summation), so the pack limit rounds the same way.
func pySum(xs []float64) float64 {
	sum, c := 0.0, 0.0
	for _, x := range xs {
		t := sum + x
		if math.Abs(sum) >= math.Abs(x) {
			c += (sum - t) + x
		} else {
			c += (x - t) + sum
		}
		sum = t
	}
	if c != 0 && !math.IsInf(c, 0) && !math.IsNaN(c) {
		sum += c
	}
	return sum
}

// SplitModules are the modules whose timing alone exceeds the pack limit and that are in SplitTestIDsModules.
func SplitModules(modules []string, timings map[string]float64, total int) map[string]bool {
	out := map[string]bool{}
	if total < 1 {
		return out
	}
	sorted := append([]string(nil), modules...)
	sort.Strings(sorted)
	w := Weights(sorted, timings)
	vals := make([]float64, len(sorted))
	for i, m := range sorted {
		vals[i] = w[m]
	}
	limit := pySum(vals) / float64(total) * 1.15
	for _, m := range modules {
		if SplitTestIDsModules[m] && w[m] > limit {
			out[m] = true
		}
	}
	return out
}

// Pack is greedy longest-processing-time packing: `total` sorted module lists.
func Pack(modules []string, total int, timings map[string]float64) ([][]string, error) {
	if total < 1 {
		return nil, errf("total %d out of range; use total >= 1", total)
	}
	sorted := append([]string(nil), modules...)
	sort.Strings(sorted)
	sm := SplitModules(sorted, timings, total)
	var rest []string
	for _, m := range sorted {
		if !sm[m] {
			rest = append(rest, m)
		}
	}
	w := Weights(rest, timings)
	order := append([]string(nil), rest...)
	sort.SliceStable(order, func(i, j int) bool {
		if w[order[i]] != w[order[j]] {
			return w[order[i]] > w[order[j]]
		}
		return order[i] < order[j]
	})
	bins := make([][]string, total)
	loads := make([]float64, total)
	for _, m := range order {
		best := 0
		for k := 1; k < total; k++ {
			if loads[k] < loads[best] {
				best = k
			}
		}
		bins[best] = append(bins[best], m)
		loads[best] += w[m]
	}
	var splitSorted []string
	for m := range sm {
		splitSorted = append(splitSorted, m)
	}
	sort.Strings(splitSorted)
	for i := range bins {
		bins[i] = append(bins[i], splitSorted...)
		sort.Strings(bins[i])
		if bins[i] == nil {
			bins[i] = []string{}
		}
	}
	return bins, nil
}

// Select is the modules of 1-based shard out of total.
func Select(modules []string, shard, total int, timings map[string]float64) ([]string, error) {
	if total < 1 || shard < 1 || shard > total {
		return nil, errf("shard %d out of range for total %d; use 1 <= shard <= total", shard, total)
	}
	bins, err := Pack(modules, total, timings)
	if err != nil {
		return nil, err
	}
	return bins[shard-1], nil
}

// ShardOfTestIDs is the ids for shard (1-based) of total: round-robin on sorted position (T-1039 / T-1168).
func ShardOfTestIDs(ids []string, shard, total int) []string {
	s := append([]string(nil), ids...)
	sort.Strings(s)
	out := []string{}
	for pos, id := range s {
		if pos%total == shard-1 {
			out = append(out, id)
		}
	}
	return out
}

// ReadOnly reads module names listed one per line; nil if path is empty.
func ReadOnly(path string) (map[string]bool, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errf("cannot read the --only file %s: %v; pass the affected selection file", path, err)
	}
	out := map[string]bool{}
	text := strings.ReplaceAll(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\r", "\n") // universal newlines
	for _, l := range strings.Split(text, "\n") {
		if t := affected.PyStrip(l); t != "" {
			out[t] = true
		}
	}
	return out, nil
}

// pyRepr formats a list of strings like Python's repr.
func pyRepr(xs []string) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(x) + "'"
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// pyFloat formats a float like Python's repr for the magnitudes timings have.
func pyFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.ContainsAny(s, ".") {
		s += ".0"
	}
	return s
}

// QuarantineMaxDays is the longest allowed quarantine (expires - added), as in ci/shard.py.
const QuarantineMaxDays = 14

// QuarantineError is an unreadable or malformed quarantine file; the CLI exits 2 (never run nothing, never skip all).
type QuarantineError struct{ Msg string }

func (e *QuarantineError) Error() string { return e.Msg }

// QEntry is one valid quarantine entry.
type QEntry struct{ ID, Owner, Added, Expires string }

var taskIDRe = regexp.MustCompile(`^T-[0-9]+$`)

func qdate(v string) (time.Time, bool) {
	if len(v) != 10 {
		return time.Time{}, false
	}
	t, err := time.Parse("2006-01-02", v)
	return t, err == nil
}

// quarantineProblem is why the entry is invalid, or "" (same wording as ci/shard.py).
func quarantineProblem(e map[string]any) string {
	str := map[string]string{}
	for _, k := range []string{"id", "owner", "added", "expires", "reason", "evidence"} {
		v, ok := e[k]
		if !ok {
			return "missing " + k
		}
		sv, isStr := v.(string)
		if !isStr || strings.TrimSpace(sv) == "" {
			return "empty " + k
		}
		str[k] = sv
	}
	if !taskIDRe.MatchString(str["owner"]) {
		return fmt.Sprintf("owner %s is not a task id like T-0001", pyQuote(str["owner"]))
	}
	if !strings.HasPrefix(str["evidence"], "https://") {
		return "evidence must be an https:// URL"
	}
	d := map[string]time.Time{}
	for _, k := range []string{"added", "expires"} {
		t, ok := qdate(str[k])
		if !ok {
			return fmt.Sprintf("bad date %s %s (use YYYY-MM-DD)", k, pyQuote(str[k]))
		}
		d[k] = t
	}
	if !d["expires"].After(d["added"]) {
		return "expires must be after added"
	}
	if d["expires"].After(d["added"].AddDate(0, 0, QuarantineMaxDays)) {
		return fmt.Sprintf("expires is more than %d days after added", QuarantineMaxDays)
	}
	return ""
}

func pyQuote(s string) string {
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		return `"` + s + `"`
	}
	return "'" + strings.ReplaceAll(s, "'", `\'`) + "'"
}

// LoadQuarantine is the Go twin of load_quarantine in ci/shard.py: the active entries and the warnings.
func LoadQuarantine(path, today string) ([]QEntry, []string, error) {
	now, ok := qdate(today)
	if !ok {
		return nil, nil, &QuarantineError{fmt.Sprintf("bad --today %s; use YYYY-MM-DD", pyQuote(today))}
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, &QuarantineError{fmt.Sprintf("cannot read quarantine file %s: %v; fix it or restore it from main", path, err)}
	}
	var data struct {
		Entries *[]json.RawMessage `json:"entries"`
	}
	if err := json.Unmarshal(raw, &data); err != nil || data.Entries == nil {
		return nil, nil, &QuarantineError{fmt.Sprintf("quarantine file %s must be a JSON object {\"entries\": [...]}; fix it or restore it from main", path)}
	}
	var active []QEntry
	var warnings []string
	for _, r := range *data.Entries {
		var e map[string]any
		if json.Unmarshal(r, &e) != nil || e == nil {
			warnings = append(warnings, "quarantine: ignored invalid entry ?: entry is not an object")
			continue
		}
		id, _ := e["id"].(string)
		if strings.HasPrefix(id, "go:") {
			continue
		}
		if why := quarantineProblem(e); why != "" {
			if id == "" {
				id = "?"
			}
			warnings = append(warnings, fmt.Sprintf("quarantine: ignored invalid entry %s: %s", id, why))
			continue
		}
		q := QEntry{ID: id, Owner: e["owner"].(string), Added: e["added"].(string), Expires: e["expires"].(string)}
		active = append(active, q)
		if exp, _ := qdate(q.Expires); exp.Before(now) {
			warnings = append(warnings, fmt.Sprintf("quarantine: EXPIRED %s owner %s expired %s", q.ID, q.Owner, q.Expires))
		}
	}
	return active, warnings, nil
}

// QuarantineMatches: an entry id names a test, a class or a module and covers everything under it.
func QuarantineMatches(entryID, testID string) bool {
	return testID == entryID || strings.HasPrefix(testID, entryID+".")
}

// quarantineHits is the entries covering at least one of testIDs (by entry id) and those test ids.
func quarantineHits(entries []QEntry, testIDs []string) (map[string]QEntry, map[string]bool) {
	hit, dropped := map[string]QEntry{}, map[string]bool{}
	for _, t := range testIDs {
		for _, e := range entries {
			if QuarantineMatches(e.ID, t) {
				if _, ok := hit[e.ID]; !ok {
					hit[e.ID] = e
				}
				dropped[t] = true
			}
		}
	}
	return hit, dropped
}

// Report is the JSON a shard run writes.
type Report struct {
	Shard   int                 `json:"shard"`
	Total   int                 `json:"total"`
	Modules []string            `json:"modules"`
	Tests   int                 `json:"tests"`
	Seconds map[string]float64  `json:"seconds"`
	TestIDs map[string][]string `json:"test_ids,omitempty"`
	// Quarantined lists the quarantine entry ids that dropped at least one test from this shard.
	Quarantined []string `json:"quarantined,omitempty"`
}

// pyHelper runs a python3 snippet that writes JSON to the file named by its first argument after the root.
func pyHelper(root, snippet string, arg any, out any) error {
	tmp, err := os.CreateTemp("", "ci-shard-*.json")
	if err != nil {
		return errf("cannot create a temp file: %v", err)
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	enc, _ := json.Marshal(arg)
	cmd := exec.Command("python3", "-c", snippet, root, tmp.Name(), string(enc))
	cmd.Dir = root
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	runErr := cmd.Run()
	raw, readErr := os.ReadFile(tmp.Name())
	if readErr == nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return errf("python3 helper wrote unreadable JSON: %v", err)
		}
		return nil
	}
	if runErr != nil {
		return errf("python3 test loader failed: %v; install python3 and run from a repository checkout", runErr)
	}
	return errf("python3 test loader wrote no result")
}

const collectSnippet = `import json, sys, unittest
root, outp, arg = sys.argv[1], sys.argv[2], json.loads(sys.argv[3])
tests = arg["tests"]
sys.path.insert(0, root)
sys.path.insert(0, tests)
res = {}
for m in arg["modules"]:
    suite = unittest.TestLoader().discover(tests, pattern=m, top_level_dir=tests)
    ids = []
    def walk(st):
        for t in st:
            if isinstance(t, unittest.TestSuite):
                walk(t)
            else:
                ids.append(t.id())
    walk(suite)
    res[m] = sorted(ids)
json.dump(res, open(outp, "w"))
`

const runSnippet = `import json, os, sys, time, unittest
root, outp, arg = sys.argv[1], sys.argv[2], json.loads(sys.argv[3])
tests = arg["tests"]
os.chdir(root)
sys.path.insert(0, root)
sys.path.insert(0, tests)
loader = unittest.TestLoader()
runner = unittest.TextTestRunner(verbosity=2)
count, ok, seconds = 0, True, {}
for m in arg["modules"]:
    t0 = time.monotonic()
    ids = arg["ids"].get(m)
    drop = set(arg["drop"].get(m) or [])
    if ids is not None:
        result = runner.run(unittest.TestSuite(loader.loadTestsFromName(i) for i in ids))
    else:
        suite = loader.discover(tests, pattern=m, top_level_dir=tests)
        if drop:
            found = []
            def walk(st):
                for t in st:
                    if isinstance(t, unittest.TestSuite):
                        walk(t)
                    else:
                        found.append(t)
            walk(suite)
            suite = unittest.TestSuite(t for t in found if t.id() not in drop)
        result = runner.run(suite)
    seconds[m] = round(time.monotonic() - t0, 1)
    count += result.testsRun
    ok = ok and result.wasSuccessful()
json.dump({"tests": count, "ok": ok, "seconds": seconds}, open(outp, "w"))
`

// CollectTestIDs is the sorted unittest ids of the given module file names (python3 discovery).
func CollectTestIDs(root, testsDir string, modules []string) (map[string][]string, error) {
	out := map[string][]string{}
	if len(modules) == 0 {
		return out, nil
	}
	err := pyHelper(root, collectSnippet, map[string]any{"tests": testsDir, "modules": modules}, &out)
	return out, err
}

// Run runs the shard's modules (only those in only, if given), writes the report to out and returns the exit code.
func Run(root string, shard, total int, out string, only map[string]bool, stderr *os.File) (int, error) {
	return RunQuarantine(root, shard, total, out, only, "", "", os.Stdout, stderr)
}

// RunQuarantine is Run with an explicit quarantine file ("" = <root>/ci/quarantine.json) and today ("" = the real
// date); quarantine lines go to stdout.
func RunQuarantine(root string, shard, total int, out string, only map[string]bool, qfile, today string, stdout io.Writer, stderr *os.File) (int, error) {
	if qfile == "" {
		qfile = filepath.Join(root, "ci", "quarantine.json")
	}
	if today == "" {
		today = time.Now().Format("2006-01-02")
	}
	entries, warnings, err := LoadQuarantine(qfile, today)
	if err != nil {
		return 2, err
	}
	for _, w := range warnings {
		fmt.Fprintln(stdout, w)
	}
	testsDir := filepath.Join(root, "tests")
	all, err := AllModules(testsDir)
	if err != nil {
		return 1, err
	}
	timings, err := LoadTimings(filepath.Join(root, "ci", "shard_timings.json"))
	if err != nil {
		return 1, err
	}
	mods, err := Select(all, shard, total, timings)
	if err != nil {
		return 1, err
	}
	if only != nil {
		var keep []string
		for _, m := range mods {
			if only[m] {
				keep = append(keep, m)
			}
		}
		mods = keep
	}
	split := SplitModules(all, timings, total)
	var splitHere []string
	for _, m := range mods {
		if split[m] {
			splitHere = append(splitHere, m)
		}
	}
	idSubsets := map[string][]string{}
	// modEntries: the entries whose module part is one of this shard's modules; only those need the id listing.
	modEntries := map[string][]QEntry{}
	for _, m := range mods {
		for _, e := range entries {
			if strings.Split(e.ID, ".")[0] == strings.TrimSuffix(m, ".py") {
				modEntries[m] = append(modEntries[m], e)
			}
		}
	}
	var collectMods []string
	for _, m := range mods {
		if split[m] || len(modEntries[m]) > 0 {
			collectMods = append(collectMods, m)
		}
	}
	quarantined := map[string]QEntry{}
	drop := map[string][]string{}
	if len(collectMods) > 0 {
		ids, err := CollectTestIDs(root, testsDir, collectMods)
		if err != nil {
			return 1, err
		}
		for _, m := range collectMods {
			if split[m] {
				// a split module runs only this shard's id slice: quarantine looks at that slice, as ci/shard.py does
				sub := ShardOfTestIDs(ids[m], shard, total)
				hit, dropped := quarantineHits(modEntries[m], sub)
				for k, v := range hit {
					quarantined[k] = v
				}
				var keep []string
				for _, id := range sub {
					if !dropped[id] {
						keep = append(keep, id)
					}
				}
				if keep == nil {
					keep = []string{}
				}
				idSubsets[m] = keep
			} else {
				hit, dropped := quarantineHits(modEntries[m], ids[m])
				for k, v := range hit {
					quarantined[k] = v
				}
				for id := range dropped {
					drop[m] = append(drop[m], id)
				}
			}
		}
	}
	var res struct {
		Tests   int                `json:"tests"`
		OK      bool               `json:"ok"`
		Seconds map[string]float64 `json:"seconds"`
	}
	if len(mods) > 0 {
		if err := pyHelper(root, runSnippet, map[string]any{"tests": testsDir, "modules": mods, "ids": idSubsets, "drop": drop}, &res); err != nil {
			return 1, err
		}
	} else {
		res.OK, res.Seconds = true, map[string]float64{}
	}
	if mods == nil {
		mods = []string{}
	}
	rep := Report{Shard: shard, Total: total, Modules: mods, Tests: res.Tests, Seconds: res.Seconds}
	qids := make([]string, 0, len(quarantined))
	for id := range quarantined {
		qids = append(qids, id)
	}
	sort.Strings(qids)
	for _, id := range qids {
		fmt.Fprintf(stdout, "quarantine: excluded %s (owner %s, expires %s)\n", id, quarantined[id].Owner, quarantined[id].Expires)
	}
	rep.Quarantined = qids
	if len(idSubsets) > 0 {
		rep.TestIDs = idSubsets
	}
	b, err := json.Marshal(rep)
	if err != nil {
		return 1, err
	}
	if err := os.WriteFile(out, b, 0o644); err != nil {
		return 1, errf("cannot write the report %s: %v", out, err)
	}
	if res.Tests == 0 && len(mods) > 0 && len(qids) == 0 {
		fmt.Fprintf(stderr, "shard %d ran 0 tests from %d modules\n", shard, len(mods))
		return 1, nil
	}
	if res.OK {
		return 0, nil
	}
	return 1, nil
}

type rawReport struct {
	Shard   *int                `json:"shard"`
	Total   *int                `json:"total"`
	Modules []string            `json:"modules"`
	Tests   *int                `json:"tests"`
	Seconds map[string]float64  `json:"seconds"`
	TestIDs map[string][]string `json:"test_ids"`
	// Quarantined entry ids; the verify rule treats the tests they cover as run on purpose (T-1742).
	Quarantined []string `json:"quarantined"`
	name        string
}

func readReports(dir string) (map[int]rawReport, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, errf("cannot list the reports dir %s: %v", dir, err)
	}
	reports := map[int]rawReport{}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		var r rawReport
		if err == nil {
			err = json.Unmarshal(raw, &r)
		}
		if err == nil && (r.Shard == nil || r.Total == nil || r.Tests == nil) {
			err = fmt.Errorf("missing shard, total or tests")
		}
		if err != nil {
			return nil, errf("unreadable shard report %s: %v", e.Name(), err)
		}
		r.name = e.Name()
		reports[*r.Shard] = r
	}
	return reports, nil
}

// Verify checks that the reports cover every module (of only, if given) exactly once and returns the summary.
func Verify(root, dir string, total int, only map[string]bool) (string, int, error) {
	testsDir := filepath.Join(root, "tests")
	reports, err := readReports(dir)
	if err != nil {
		return "", 0, err
	}
	for _, i := range sortedKeys(reports) {
		if r := reports[i]; *r.Total != total {
			return "", 0, errf("%s was made for %d shards, expected %d", r.name, *r.Total, total)
		}
	}
	var missing []string
	for i := 1; i <= total; i++ {
		if _, ok := reports[i]; !ok {
			missing = append(missing, strconv.Itoa(i))
		}
	}
	if len(missing) > 0 {
		return "", 0, errf("missing shard reports: %s", strings.Join(missing, ", "))
	}
	seen := map[string][]int{}
	for _, i := range sortedKeys(reports) {
		for _, m := range reports[i].Modules {
			seen[m] = append(seen[m], i)
		}
	}
	all, err := AllModules(testsDir)
	if err != nil {
		return "", 0, err
	}
	expected := all
	if only != nil {
		expected = nil
		for _, m := range all {
			if only[m] {
				expected = append(expected, m)
			}
		}
	}
	timings, err := LoadTimings(filepath.Join(root, "ci", "shard_timings.json"))
	if err != nil {
		return "", 0, err
	}
	split := SplitModules(expected, timings, total)
	expSet := map[string]bool{}
	for _, m := range expected {
		expSet[m] = true
	}
	dup, absent, extra := []string{}, []string{}, []string{}
	for m, s := range seen {
		if !split[m] && len(s) > 1 {
			dup = append(dup, m)
		}
		if !expSet[m] {
			extra = append(extra, m)
		}
	}
	for _, m := range expected {
		if _, ok := seen[m]; !ok && !split[m] {
			absent = append(absent, m)
		}
	}
	sort.Strings(dup)
	sort.Strings(absent)
	sort.Strings(extra)
	if len(dup)+len(absent)+len(extra) > 0 {
		return "", 0, errf("module coverage wrong: ran twice=%s never ran=%s unknown=%s", pyRepr(dup), pyRepr(absent), pyRepr(extra))
	}
	var splitSorted []string
	for m := range split {
		splitSorted = append(splitSorted, m)
	}
	sort.Strings(splitSorted)
	for _, m := range splitSorted {
		if _, ok := seen[m]; !ok {
			return "", 0, errf("module coverage wrong: ran twice=[] never ran=%s unknown=[]", pyRepr([]string{m}))
		}
		idsMap, err := CollectTestIDs(root, testsDir, []string{m})
		if err != nil {
			return "", 0, err
		}
		full := idsMap[m]
		byShard := map[string][]int{}
		for _, i := range sortedKeys(reports) {
			r := reports[i]
			if !contains(r.Modules, m) {
				continue
			}
			ids, hasIDs := r.TestIDs[m]
			if !hasIDs || (len(ids) == 0 && len(r.Quarantined) == 0) {
				return "", 0, errf("split module %s on shard %d: missing test_ids in report", m, i)
			}
			for _, id := range ids {
				byShard[id] = append(byShard[id], i)
			}
		}
		var dupIDs, ran []string
		for id, s := range byShard {
			ran = append(ran, id)
			if len(s) > 1 {
				dupIDs = append(dupIDs, id)
			}
		}
		// Quarantined tests were dropped from the run on purpose: the report's "quarantined" entry ids cover them.
		var qEntries []QEntry
		for _, i := range sortedKeys(reports) {
			if contains(reports[i].Modules, m) {
				for _, q := range reports[i].Quarantined {
					qEntries = append(qEntries, QEntry{ID: q})
				}
			}
		}
		_, droppedFull := quarantineHits(qEntries, full)
		for id := range droppedFull {
			if _, in := byShard[id]; !in {
				ran = append(ran, id)
			}
		}
		sort.Strings(dupIDs)
		sort.Strings(ran)
		if len(dupIDs) > 0 || !equal(ran, full) {
			var never []string
			ranSet := map[string]bool{}
			for _, id := range ran {
				ranSet[id] = true
			}
			for _, id := range full {
				if !ranSet[id] {
					never = append(never, id)
				}
			}
			sort.Strings(never)
			return "", 0, errf("split module %s: id coverage wrong: ran twice=%s never ran=%s", m, pyRepr(dupIDs), pyRepr(never))
		}
		for _, i := range sortedKeys(reports) {
			r := reports[i]
			if !contains(r.Modules, m) {
				continue
			}
			got := r.TestIDs[m]
			var rqs []QEntry
			for _, q := range r.Quarantined {
				rqs = append(rqs, QEntry{ID: q})
			}
			_, rdrop := quarantineHits(rqs, full)
			var exp []string
			for _, id := range ShardOfTestIDs(full, i, total) {
				if !rdrop[id] {
					exp = append(exp, id)
				}
			}
			if !equal(got, exp) {
				return "", 0, errf("split module %s shard %d: expected %d ids, got %d (or wrong ids)", m, i, len(exp), len(got))
			}
		}
	}
	tests := 0
	for _, r := range reports {
		tests += *r.Tests
	}
	lines := []string{fmt.Sprintf("%d modules, %d tests across %d shards", len(expected), tests, total)}
	for _, i := range sortedKeys(reports) {
		r := reports[i]
		timing := "no timing"
		if r.Seconds != nil {
			var vals []float64
			for _, k := range sortedFloatKeys(r.Seconds) {
				vals = append(vals, r.Seconds[k])
			}
			timing = fmt.Sprintf("%.0f s", pySum(vals))
		}
		lines = append(lines, fmt.Sprintf("- shard %d: %d modules, %d tests, %s", i, len(r.Modules), *r.Tests, timing))
	}
	return strings.Join(lines, "\n"), tests, nil
}

// MergeTimings is {module: seconds} summed over the shard reports in dir.
func MergeTimings(dir string) (map[string]float64, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, errf("cannot list the reports dir %s: %v", dir, err)
	}
	out := map[string]float64{}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		var r struct {
			Seconds map[string]float64 `json:"seconds"`
		}
		if err == nil {
			err = json.Unmarshal(raw, &r)
		}
		if err == nil && r.Seconds == nil {
			err = fmt.Errorf("no seconds key")
		}
		if err != nil {
			return nil, errf("report %s has no per-module seconds: %v", e.Name(), err)
		}
		for m, s := range r.Seconds {
			out[m] += s
		}
	}
	return out, nil
}

// FormatTimings renders a timing file like json.dump(data, f, indent=1) plus a newline.
func FormatTimings(data map[string]float64) string {
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return "{}\n"
	}
	var b strings.Builder
	b.WriteString("{\n")
	for i, k := range keys {
		kb, _ := json.Marshal(k)
		fmt.Fprintf(&b, " %s: %s", kb, pyFloat(data[k]))
		if i < len(keys)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("}\n")
	return b.String()
}

func sortedKeys(m map[int]rawReport) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

func sortedFloatKeys(m map[string]float64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
