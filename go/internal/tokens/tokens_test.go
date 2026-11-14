package tokens

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// toGeneric marshals v and decodes it again, so Go results and the Python golden compare as plain JSON values.
func toGeneric(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func fixtureIndex(t *testing.T) *Snapshot {
	t.Helper()
	projects, workers := copyFixtures(t)
	ix := NewTokenIndex(projects, nil, workers, "")
	ix.Refresh()
	return ix.Snapshot()
}

// TestTallyGolden compares every number with the output of the Python functions on the same fixtures; the golden
// file was written by the generator command in the PR body (web.tokens + web.prices, TZ=UTC).
func TestTallyGolden(t *testing.T) {
	setup(t)
	var golden map[string]any
	if err := json.Unmarshal([]byte(readFile(t, "testdata/tally.golden")), &golden); err != nil {
		t.Fatal(err)
	}
	db, _ := openDB(t, true)
	projects, _ := copyFixtures(t)
	snap := fixtureIndex(t)
	snap.LastScan = nil
	attr, err := Attribute(snap, db)
	if err != nil {
		t.Fatal(err)
	}
	orgOf := func(s string) string {
		if s == "sess-eee" {
			return "cursor"
		}
		return "claude"
	}
	got := map[string]any{"snapshot": snap, "attribution": attr, "rollup": Rollup(snap, attr, orgOf)}
	tu, gu := map[string]any{}, map[string]any{}
	for _, id := range []string{"T-0001", "T-0002", "T-0003", "T-9999"} {
		r, err := TaskUsage(snap, attr, db, id, fixedNow)
		if err != nil {
			t.Fatal(err)
		}
		tu[id] = r
	}
	for _, id := range []string{"G-001", "G-002", "G-404"} {
		r, err := GoalUsage(snap, attr, db, id, fixedNow)
		if err != nil {
			t.Fatal(err)
		}
		gu[id] = r
	}
	got["task_usage"], got["goal_usage"] = tu, gu
	nr, _ := TaskUsage(&Snapshot{}, attr, db, "T-0001", fixedNow)
	got["task_usage_not_ready"] = nr
	st, stk, lc := map[string]any{}, map[string]any{}, map[string]any{}
	for _, s := range []string{"sess-aaa", "sess-bbb", "sess-nosuch", "bad/id", ""} {
		st[s] = SessionTokens(s, projects)
		if n, ok := SessionTokensKnown(s, projects); ok {
			stk[s] = n
		} else {
			stk[s] = nil
		}
		if n, ok := LatestContext(s, projects, 0); ok {
			lc[s] = n
		} else {
			lc[s] = nil
		}
	}
	got["session_tokens"], got["session_tokens_known"], got["latest_context"] = st, stk, lc
	got["usage_totals"] = UsageTotals(projects)
	costs := []any{}
	for _, c := range golden["cost"].([]any) {
		a := c.([]any)
		u := a[1].(map[string]any)
		n := func(k string) int64 { f, _ := u[k].(float64); return int64(f) }
		speed, _ := a[2].(string)
		v, ok := Cost(a[0].(string), Usage{n("input"), n("output"), n("cache_read"), n("cache_write_5m"), n("cache_write_1h")}, speed)
		var cv any
		if ok {
			cv = v
		}
		costs = append(costs, []any{a[0], a[1], a[2], cv})
	}
	got["cost"] = costs
	g := toGeneric(t, got).(map[string]any)
	delete(g["snapshot"].(map[string]any), "last_scan") // the clock reading; the golden has none
	for k := range golden {
		if !reflect.DeepEqual(g[k], golden[k]) {
			gb, _ := json.MarshalIndent(g[k], "", " ")
			wb, _ := json.MarshalIndent(golden[k], "", " ")
			t.Errorf("%s differs from the Python golden\n--- go\n%s\n--- python\n%s", k, gb, wb)
		}
	}
	if len(g) != len(golden) {
		t.Errorf("keys: go %d, golden %d", len(g), len(golden))
	}
}

// TestIncrementalRefresh: a second Refresh reads only the appended bytes, a partial last line waits for its newline,
// a replaced (shorter) file starts over, and a vanished file leaves the index.
func TestIncrementalRefresh(t *testing.T) {
	setup(t)
	root := t.TempDir()
	f := filepath.Join(root, "proj", "s1.jsonl")
	line := func(id string, in int) string {
		return `{"type":"assistant","timestamp":"2026-11-10T09:00:00Z","requestId":"` + id + `","message":{"id":"` + id + `","model":"claude-sonnet-5-5","usage":{"input_tokens":` + string(rune('0'+in)) + `,"output_tokens":1}}}`
	}
	writeLines(t, f, line("a", 1))
	ix := NewTokenIndex(root, nil, "", "")
	ix.Refresh()
	s := ix.Snapshot()
	if len(s.Rows) != 1 || s.Rows[0].Input != 1 || s.Rows[0].Requests != 1 || s.BytesRead == 0 {
		t.Fatalf("first pass: %+v", s)
	}
	first := s.BytesRead
	fh, _ := os.OpenFile(f, os.O_APPEND|os.O_WRONLY, 0)
	fh.WriteString(line("b", 2)[:20]) // partial line
	fh.Close()
	touchFuture(t, f)
	ix.Refresh()
	if s := ix.Snapshot(); s.Rows[0].Requests != 1 || s.BytesRead != 0 {
		t.Fatalf("partial line must not be read: %+v", s)
	}
	fh, _ = os.OpenFile(f, os.O_APPEND|os.O_WRONLY, 0)
	fh.WriteString(line("b", 2)[20:] + "\n")
	fh.Close()
	touchFuture(t, f)
	ix.Refresh()
	s = ix.Snapshot()
	if s.Rows[0].Requests != 2 || s.Rows[0].Input != 3 || s.BytesTotal <= first {
		t.Fatalf("append: %+v", s)
	}
	// same request id again in another file of the session: counted once
	writeLines(t, filepath.Join(root, "proj", "s1", "subagents", "x.jsonl"), line("b", 2))
	ix.Refresh()
	if s := ix.Snapshot(); s.Rows[0].Requests != 2 {
		t.Fatalf("dedupe across files: %+v", s)
	}
	// replaced by a shorter file: the inode changes, counting starts over (as in Python, request b was owned by the
	// replaced file, so the subagent copy that was skipped as a duplicate is not counted afterwards)
	os.Remove(f)
	writeLines(t, f, line("c", 5))
	ix.Refresh()
	if s := ix.Snapshot(); s.Rows[0].Input != 5 || s.Rows[0].Requests != 1 {
		t.Fatalf("replaced file: %+v", s.Rows)
	}
	os.RemoveAll(filepath.Join(root, "proj"))
	ix.Refresh()
	if s := ix.Snapshot(); len(s.Rows) != 0 || s.Files != 0 {
		t.Fatalf("vanished: %+v", s)
	}
}

// TestLongLineSkipped: a line longer than MaxReadBytes is skipped (counted as an error), the next one is read.
func TestLongLineSkipped(t *testing.T) {
	setup(t)
	old := MaxReadBytes
	MaxReadBytes = 100
	t.Cleanup(func() { MaxReadBytes = old })
	root := t.TempDir()
	f := filepath.Join(root, "p", "s.jsonl")
	ok := `{"type":"assistant","uuid":"u1","message":{"usage":{"input_tokens":4}}}`
	writeLines(t, f, strings.Repeat("x", 250), ok)
	ix := NewTokenIndex(root, nil, "", "")
	for i := 0; i < 10; i++ {
		ix.Refresh()
	}
	s := ix.Snapshot()
	if s.Errors != 1 || len(s.Rows) != 1 || s.Rows[0].Input != 4 {
		t.Fatalf("long line: %+v rows=%+v", s, s.Rows)
	}
}

// TestWorkerConfinement: a worker's symlink or hardlink to a file outside its own dir is never read (C3: a file that
// must be rejected and one that must be accepted, through Refresh).
func TestWorkerConfinement(t *testing.T) {
	setup(t)
	base := t.TempDir()
	workers := filepath.Join(base, "workers")
	secret := filepath.Join(base, "outside.jsonl")
	line := `{"type":"assistant","uuid":"x","message":{"usage":{"input_tokens":9}}}`
	writeLines(t, secret, line)
	proj := filepath.Join(workers, "w1", "claude", "projects", "p")
	writeLines(t, filepath.Join(proj, "good.jsonl"), `{"type":"assistant","uuid":"g","message":{"usage":{"input_tokens":1}}}`)
	if err := os.Symlink(secret, filepath.Join(proj, "link.jsonl")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(secret, filepath.Join(proj, "hard.jsonl")); err != nil {
		t.Fatal(err)
	}
	ix := NewTokenIndex(filepath.Join(base, "empty"), nil, workers, "")
	ix.Refresh()
	s := ix.Snapshot()
	if len(s.Rows) != 1 || s.Rows[0].Session != "good" || s.Rows[0].Input != 1 {
		t.Fatalf("only good.jsonl may count: %+v", s.Rows)
	}
	// an agent dir that is a symlink out of the workers root is skipped
	other := filepath.Join(base, "elsewhere", "claude", "projects", "p")
	writeLines(t, filepath.Join(other, "evil.jsonl"), line)
	if err := os.Symlink(filepath.Join(base, "elsewhere"), filepath.Join(workers, "w2")); err != nil {
		t.Fatal(err)
	}
	ix.Refresh()
	for _, r := range ix.Snapshot().Rows {
		if r.Session == "evil" {
			t.Fatalf("agent dir outside the workers root was read: %+v", r)
		}
	}
}

// TestSnapshotIsACopy: mutating a returned snapshot never changes the published one.
func TestSnapshotIsACopy(t *testing.T) {
	setup(t)
	s := fixtureIndex(t)
	if len(s.Rows) == 0 {
		t.Fatal("no rows")
	}
	projects, _ := copyFixtures(t)
	ix := NewTokenIndex(projects, nil, "", "")
	ix.Refresh()
	a := ix.Snapshot()
	a.Rows[0].Input = -1
	for k := range a.Sessions {
		a.Sessions[k].Project = "x"
	}
	b := ix.Snapshot()
	if b.Rows[0].Input == -1 {
		t.Fatal("rows share memory")
	}
	for _, v := range b.Sessions {
		if v.Project == "x" {
			t.Fatal("sessions share memory")
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	d, _ := os.Getwd()
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(d, "web", "tokens.py")); err == nil {
			return d
		}
		d = filepath.Dir(d)
	}
	t.Fatal("web/tokens.py not found above the test directory")
	return ""
}

func python(t *testing.T, script string, args ...string) string {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed: cache interop with web.tokens is not checked here")
	}
	cmd := exec.Command(py, append([]string{"-I", "-c", script, repoRoot(t)}, args...)...)
	cmd.Env = []string{"TZ=UTC", "PATH=" + os.Getenv("PATH")}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("python3: %v\n%s", err, out)
	}
	return string(out)
}

// TestCacheInterop: the snapshot JSON (the index's cache format) written by Go is accepted by web.tokens.rollup, and a
// snapshot written by Python's TokenIndex is read by Go with the same totals. The cursor limit state in the config
// table is shared the same way (written by Go, read by web.cursor_limits.load_state; and the reverse).
func TestCacheInterop(t *testing.T) {
	setup(t)
	dir := t.TempDir()
	snap := fixtureIndex(t)

	goFile := filepath.Join(dir, "go-snapshot.json")
	b, _ := json.Marshal(snap)
	os.WriteFile(goFile, b, 0o644)
	out := python(t, `
import json, sys
sys.path.insert(0, sys.argv[1])
from web import tokens
snap = json.load(open(sys.argv[2]))
print(json.dumps(tokens.rollup(snap, {})["total"]))
`, goFile)
	var pyTotal Bucket
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &pyTotal); err != nil {
		t.Fatalf("python output %q: %v", out, err)
	}
	goTotal := Rollup(snap, map[string]Attr{}, nil).Total
	if !reflect.DeepEqual(pyTotal, *goTotal) {
		t.Errorf("Python rollup of the Go snapshot differs: py %+v go %+v", pyTotal, *goTotal)
	}

	projects, workers := copyFixtures(t)
	pyFile := filepath.Join(dir, "py-snapshot.json")
	python(t, `
import json, sys
sys.path.insert(0, sys.argv[1])
from web import tokens
idx = tokens.TokenIndex(sys.argv[2], workers_root=sys.argv[3]); idx.refresh()
json.dump(idx.snapshot(), open(sys.argv[4], "w"))
`, projects, workers, pyFile)
	loaded, err := LoadSnapshot([]byte(readFile(t, pyFile)))
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Ready || len(loaded.Rows) == 0 || len(loaded.Sessions) == 0 {
		t.Fatalf("Python snapshot not read: %+v", loaded)
	}
	loaded.LastScan, snap.LastScan = nil, nil
	if !reflect.DeepEqual(toGeneric(t, loaded), toGeneric(t, snap)) {
		t.Errorf("Go's snapshot of the fixtures differs from the one Python wrote")
	}
	if !reflect.DeepEqual(Rollup(loaded, map[string]Attr{}, nil).Total, goTotal) {
		t.Errorf("rollup of the Python snapshot differs from the Go one")
	}
}
