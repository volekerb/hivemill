package data

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func jn(s string) json.Number { return json.Number(s) }

func TestParseTS(t *testing.T) {
	ref, ok := ParseTS("2026-11-14T11:00:00Z")
	if !ok {
		t.Fatal("Z suffix must parse")
	}
	same := []string{"2026-11-14T12:00:00+01:00", "2026-11-14 11:00:00+00:00", "2026-11-14T11:00:00", "  2026-11-14T11:00:00Z  ", "2026-11-14T11:00:00.000000+00:00"}
	for _, s := range same {
		got, good := ParseTS(s)
		if !good || !got.Equal(ref) { // a naive time is local time, and the tests run with Local = UTC
			t.Errorf("ParseTS(%q) = %v, %v; want %v", s, got, good, ref)
		}
	}
	if got, _ := ParseTS("2026-11-14T11:00:00.250000Z"); got.Sub(ref) != 250*time.Millisecond {
		t.Errorf("fraction lost: %v", got.Sub(ref))
	}
	if got, good := ParseTS("2026-11-14"); !good || got.Hour() != 0 || got.Day() != 14 {
		t.Errorf("date only = %v %v", got, good)
	}
	for _, bad := range []string{"", "garbage", "2026-13-01", "2026-02-30T00:00:00", "2026-11-14T25:00:00", "2026-11-14T11:00:00+25:00"} {
		if _, good := ParseTS(bad); good {
			t.Errorf("ParseTS(%q) should fail", bad)
		}
	}
}

func TestDumpsMatchesPython(t *testing.T) {
	type row struct {
		A string       `json:"a"`
		B *string      `json:"b"`
		N Num          `json:"n"`
		M *OMap[int64] `json:"m"`
	}
	m := NewOMap[int64]()
	m.Set("z", 1)
	m.Set("a", 2)
	m.Set("z", 3) // an existing key keeps its place
	v := []row{{A: "é<&>\"\\\n\U0001f600", N: Float(2), M: m}, {A: "x", B: sp("y"), N: Int(7)}}
	b, err := Dumps(v)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"a": "\u00e9<&>\"\\\n\ud83d\ude00", "b": null, "n": 2.0, "m": {"z": 3, "a": 2}}, {"a": "x", "b": "y", "n": 7, "m": null}]`
	if string(b) != want {
		t.Errorf("Dumps =\n%s\nwant\n%s", b, want)
	}
	ib, _ := DumpsIndent1(map[string]any{"k": []int{}, "s": "é"})
	if got := string(ib); got != "{\n \"k\": [],\n \"s\": \"\\u00e9\"\n}" {
		t.Errorf("DumpsIndent1 = %q", got)
	}
	for in, want := range map[float64]string{0.5: "0.5", 3: "3.0", 1e-05: "1e-05", 1e16: "1e+16", -2.25: "-2.25"} {
		if got := Float(in).String(); got != want {
			t.Errorf("Float(%v) = %s, want %s", in, got, want)
		}
	}
	if Int(4).String() != "4" {
		t.Error("Int string")
	}
}

func TestOrgs(t *testing.T) {
	if OrgParam(map[string]string{"org": "cursor"}) != "cursor" || OrgParam(map[string]string{"org": "x"}) != "" || OrgParam(nil) != "" {
		t.Error("OrgParam")
	}
	cases := []struct{ col, name, rt, goal, want string }{
		{"cursor", "claude-x", "", "", "cursor"},        // agents.org wins
		{"bogus", "cursor-coder-t-1", "", "", "cursor"}, // then the name prefix
		{"", "legacy", "cursor", "claude", "cursor"},    // then an explicit runtime
		{"", "legacy", "", "cursor", "cursor"},          // then the goal org
		{"", "legacy", "", "weird", "claude"},           // then the default
	}
	for _, c := range cases {
		if got := AgentOrg(c.col, c.name, c.rt, c.goal); got != c.want {
			t.Errorf("AgentOrg(%+v) = %s", c, got)
		}
	}
	if OrgRuntime("cursor") != "cursor" || OrgRuntime("nope") != "claude" {
		t.Error("OrgRuntime")
	}
	if OrgOfGoal(map[string]string{"G-1": "cursor"}, "G-1") != "cursor" || OrgOfGoal(nil, "") != "claude" || OrgOfGoal(nil, "G-2") != "claude" {
		t.Error("OrgOfGoal")
	}
	for in, want := range map[string]string{`{"runtime":"cursor","kickoff":"k"}`: "cursor", `{"runtime":"x"}`: "claude", "junk": "claude", "": "claude"} {
		if got := AgentRuntimeOf(in); got != want {
			t.Errorf("AgentRuntimeOf(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestParseCriteriaAndTags(t *testing.T) {
	got := ParseCriteria("1. a\n2) b\n   more\n\n3.   c  ")
	want := []CriterionText{{1, "a"}, {2, "b more"}, {3, "c"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("numbered = %+v", got)
	}
	got = ParseCriteria("one\n\n two ")
	if !reflect.DeepEqual(got, []CriterionText{{1, "one"}, {2, "two"}}) {
		t.Errorf("fallback = %+v", got)
	}
	if len(ParseCriteria("")) != 0 {
		t.Error("empty")
	}
	tg := tags("[SC1,SC3] x [SC2] [SCx]")
	if !reflect.DeepEqual(tg, map[int64]bool{1: true, 2: true, 3: true}) {
		t.Errorf("tags = %v", tg)
	}
	if s := stripTags("[SC1]  hello   [SC2,SC3] world "); s != "hello world" {
		t.Errorf("stripTags = %q", s)
	}
	ids := wholeWordTaskIDs("T-1 (T-22) xT-3 T-4a T-5, -T-6 T-7.")
	if !reflect.DeepEqual(ids, []string{"T-1", "T-22", "T-5", "T-7"}) {
		t.Errorf("wholeWordTaskIDs = %v", ids)
	}
	if trunc("héllo wörld", 6) != "héllo…" || trunc("abc", 3) != "abc" {
		t.Error("trunc")
	}
	if pyRoundN(2.675, 2) != 2.67 || pyRound(2.5) != 2 || pyRound(3.5) != 4 {
		t.Error("rounding must match Python (half to even, exact binary value)")
	}
}

func TestTableSortAndSortRows(t *testing.T) {
	cols := map[string]string{"name": "asc", "n": "desc"}
	def := [2]string{"name", "asc"}
	queries := []struct {
		q    map[string]string
		want [2]string
	}{
		{nil, [2]string{"name", "asc"}},
		{map[string]string{"sort": "n"}, [2]string{"n", "desc"}},
		{map[string]string{"sort": "n", "dir": "asc"}, [2]string{"n", "asc"}},
		{map[string]string{"sort": "n", "dir": "bad"}, [2]string{"n", "desc"}},
		{map[string]string{"sort": "zzz", "dir": "desc"}, [2]string{"name", "asc"}},
	}
	for _, c := range queries {
		k, d := TableSort(c.q, cols, def)
		if [2]string{k, d} != c.want {
			t.Errorf("TableSort(%v) = %s %s, want %v", c.q, k, d, c.want)
		}
	}
	type r struct {
		name string
		n    any
	}
	rows := []r{{"a", 2}, {"b", nil}, {"c", 1}, {"d", 2}, {"e", nil}}
	g := map[string]func(r) any{"n": func(x r) any { return x.n }}
	name := func(rs []r) string {
		var s string
		for _, x := range rs {
			s += x.name
		}
		return s
	}
	if got := name(SortRows(rows, "n", "asc", g)); got != "cadbe" {
		t.Errorf("asc = %s", got)
	}
	if got := name(SortRows(rows, "n", "desc", g)); got != "adcbe" {
		t.Errorf("desc = %s (nil last, ties stable)", got)
	}
}

func fakeGh(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCI(t *testing.T) {
	roll := []any{
		map[string]any{"name": "build", "conclusion": "SUCCESS"},
		map[string]any{"context": "lint", "state": "FAILURE"},
		map[string]any{"status": "IN_PROGRESS"},
		"junk",
		map[string]any{"name": strings.Repeat("x", 90)},
	}
	got := CIChecks(roll)
	if len(got) != 4 || got[0] != (Check{"build", "success"}) || got[1] != (Check{"lint", "failure"}) ||
		got[2] != (Check{"?", "in_progress"}) || len(got[3].Name) != 80 || got[3].Conclusion != "pending" {
		t.Errorf("CIChecks = %+v", got)
	}
	cases := []struct {
		e    *CIEntry
		want string
	}{
		{nil, "unknown"}, {&CIEntry{State: "unknown"}, "unknown"}, {&CIEntry{State: "open"}, "pending"},
		{&CIEntry{State: "open", Checks: []Check{{"a", "success"}, {"b", "skipped"}}}, "green"},
		{&CIEntry{State: "open", Checks: []Check{{"a", "success"}, {"b", "pending"}}}, "pending"},
		{&CIEntry{State: "open", Checks: []Check{{"a", "pending"}, {"b", "timed_out"}}}, "failing"},
	}
	for _, c := range cases {
		if got := CISummarize(c.e); got != c.want {
			t.Errorf("CISummarize(%+v) = %s, want %s", c.e, got, c.want)
		}
	}
	// Fetch crosses a real process boundary: a fake gh on disk.
	c := NewCICache()
	c.Autostart = false
	c.Clock = func() float64 { return 1000 }
	c.Gh = fakeGh(t, `echo '{"state":"OPEN","statusCheckRollup":[{"name":"build","conclusion":"SUCCESS"}]}'`)
	if e := c.Fetch("https://x/pr/1"); e.State != "open" || len(e.Checks) != 1 || e.Fetched != 1000 {
		t.Errorf("Fetch = %+v", e)
	}
	c.Gh = fakeGh(t, `exit 3`)
	if e := c.Fetch("u"); e.State != "unknown" || len(e.Checks) != 0 {
		t.Errorf("failing gh = %+v", e)
	}
	c.Gh = fakeGh(t, `echo 'not json'`)
	if e := c.Fetch("u"); e.State != "unknown" {
		t.Errorf("bad json = %+v", e)
	}
	c.Gh = filepath.Join(t.TempDir(), "missing-gh")
	if e := c.Fetch("u"); e.State != "unknown" {
		t.Errorf("missing gh = %+v", e)
	}
}

func TestCICacheGetStep(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	now := 1000.0
	c := NewCICache()
	c.Autostart = false
	c.MaxPRs = 2
	c.TTL = 300
	c.Clock = func() float64 { mu.Lock(); defer mu.Unlock(); return now }
	c.Run = func(ctx context.Context, gh string, args ...string) ([]byte, error) {
		calls++
		if args[2] == "bad" {
			return nil, errors.New("boom")
		}
		return []byte(`{"state":"MERGED","statusCheckRollup":[]}`), nil
	}
	if c.Get("") != nil || c.Get("a") != nil {
		t.Fatal("a miss must return nil without blocking")
	}
	if calls != 0 {
		t.Fatal("Get must not call gh")
	}
	c.Get("a") // already queued: not queued twice
	if !c.Step(0) || c.Step(0) {
		t.Error("exactly one queued PR expected")
	}
	if calls != 1 {
		t.Errorf("gh calls = %d, want 1", calls)
	}
	if e := c.Get("a"); e == nil || e.State != "merged" {
		t.Errorf("cached = %+v", e)
	}
	c.Get("bad")
	if c.Get("third") != nil {
		t.Error("beyond MaxPRs nothing is tracked")
	}
	c.Step(0)
	if e := c.Get("bad"); e == nil || e.State != "unknown" {
		t.Errorf("failure must cache unknown, got %+v", e)
	}
	mu.Lock()
	now += 301
	mu.Unlock()
	c.Get("a") // expired: served stale and queued again
	if !c.Step(0) || calls != 3 {
		t.Errorf("expired entry must refetch (calls %d)", calls)
	}
	mu.Lock()
	now += 1000
	mu.Unlock()
	c.Get("zzz") // drops entries not asked for within 2*TTL
	if _, ok := c.entries["a"]; ok {
		t.Error("stale untracked entry should be dropped")
	}
}

func TestAdvisorAuditCLI(t *testing.T) {
	var out, errb bytes.Buffer
	if code := RunAdvisorAudit([]string{"--projects", projectsRoot()}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "FLAGGED=claude-fable-5-2") || !strings.Contains(out.String(), "total: 4 sessions") {
		t.Errorf("text report:\n%s", out.String())
	}
	out.Reset()
	if code := RunAdvisorAudit([]string{"--projects", projectsRoot(), "--json"}, &out, &errb); code != 0 || !strings.HasPrefix(out.String(), "[\n {\n  \"agent\"") {
		t.Errorf("json report (exit %d):\n%s", code, out.String())
	}
	// a/x.jsonl sorts before a-b/y.jsonl as Python's Path order (component-wise), unlike plain string order.
	rows, err := Audit(projectsRoot())
	if err != nil {
		t.Fatal(err)
	}
	var agents []string
	for _, r := range rows {
		agents = append(agents, r.Agent)
	}
	if ia, ib := indexOf(agents, "a"), indexOf(agents, "a-b"); ia < 0 || ib < ia {
		t.Errorf("order = %v", agents)
	}
	if AgentOf("/r/-work-wt-G-048-T-1474/x.jsonl", "/r") != "G-048-T-1474" || AgentOf("/r/x.jsonl", "/r") != "?" || AgentOf("/r/-boss/x.jsonl", "/r") != "boss" {
		t.Error("AgentOf")
	}
	errb.Reset()
	if code := RunAdvisorAudit([]string{"--projects", filepath.Join(t.TempDir(), "none")}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "transcript directory not found") {
		t.Errorf("missing dir: exit %d %q", code, errb.String())
	}
	if code := RunAdvisorAudit([]string{"--bogus"}, &out, &errb); code != 2 {
		t.Errorf("unknown flag: exit %d", code)
	}
	out.Reset()
	if RunAdvisorAudit([]string{"--projects", t.TempDir()}, &out, &errb) != 0 || !strings.Contains(out.String(), "no advisor calls") {
		t.Errorf("empty: %q", out.String())
	}
}

func TestContextCLI(t *testing.T) {
	home, _ := newFixture(t)
	t.Setenv("FACTORY_HOME", home)
	t.Setenv("FACTORY_CLAUDE_PROJECTS", projectsRoot())
	var out, errb bytes.Buffer
	if code := RunContextCLI([]string{"per-pr", "--last", "2", "--since", "2026-11-01"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "merged tasks: 2026-11-01 .. now, last 2") || !strings.Contains(out.String(), "T-11") {
		t.Errorf("per-pr text:\n%s", out.String())
	}
	out.Reset()
	if code := RunContextCLI([]string{"baseline", "--json"}, &out, &errb); code != 0 || !strings.HasPrefix(out.String(), "{\n \"window\": {") {
		t.Errorf("baseline --json (exit %d):\n%s", code, out.String())
	}
	bads := [][]string{{}, {"nope"}, {"baseline", "--since", "2026-13-40"}, {"per-pr", "--last", "0"}, {"baseline", "--last", "2"}, {"baseline", "--since"}}
	for _, bad := range bads {
		errb.Reset()
		if code := RunContextCLI(bad, &out, &errb); code != 2 || !strings.Contains(errb.String(), "web.context:") {
			t.Errorf("%v: exit %d %q", bad, code, errb.String())
		}
	}
	t.Setenv("FACTORY_HOME", t.TempDir())
	errb.Reset()
	if code := RunContextCLI([]string{"baseline"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "set FACTORY_HOME") {
		t.Errorf("missing db: exit %d %q", code, errb.String())
	}
	if d, err := ParseDay("2026-02-29"); err == nil {
		t.Errorf("ParseDay(2026-02-29) = %s", d)
	}
	if n, err := ParsePositive("3"); err != nil || n != 3 {
		t.Error("ParsePositive")
	}
}

func TestPriceCostAndHelpers(t *testing.T) {
	u := map[string]int64{"input": 1000, "output": 500, "cache_read": 20000, "cache_write_5m": 0, "cache_write_1h": 1000}
	c, ok := PriceCost("claude-opus-5-5", u, "")
	if !ok || c < 0.02599 || c > 0.02601 {
		t.Errorf("opus = %v %v", c, ok)
	}
	fast, _ := PriceCost("claude-opus-5-5", u, "fast")
	if fast != 2*c {
		t.Errorf("fast = %v, want twice %v", fast, c)
	}
	if s, _ := PriceCost("claude-sonnet-5-5", u, "fast"); s == 0 {
		t.Error("sonnet is priced and has no fast multiplier")
	}
	if _, ok := PriceCost("grok", u, ""); ok {
		t.Error("an unknown model has no price")
	}
	if !IsHQ("boss", "x") || !IsHQ("coder", "lead-g-1") || IsHQ("coder", "claude-coder") {
		t.Error("IsHQ")
	}
	if ContextOf(map[string]any{"input_tokens": jn("5"), "cache_read_input_tokens": jn("1.5"), "cache_creation_input_tokens": true}) != 5 {
		t.Error("ContextOf must ignore non-integers")
	}
}

func indexOf(ss []string, s string) int {
	for i, x := range ss {
		if x == s {
			return i
		}
	}
	return -1
}
