package spend

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

type req struct {
	id, model                     string
	at                            time.Time
	in, out, read, write, write1h int64
}

func entry(r req) string {
	usage := map[string]any{"input_tokens": r.in, "output_tokens": r.out, "cache_read_input_tokens": r.read,
		"cache_creation_input_tokens": r.write, "cache_creation": map[string]any{"ephemeral_1h_input_tokens": r.write1h,
			"ephemeral_5m_input_tokens": r.write - r.write1h}}
	b, _ := json.Marshal(map[string]any{"type": "assistant", "timestamp": r.at.Format(time.RFC3339Nano), "requestId": "q" + r.id,
		"message": map[string]any{"id": r.id, "model": r.model, "usage": usage}})
	return string(b)
}

func writeSession(t *testing.T, root, project, session string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(root, project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, session+".jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestScanSumsDedupesAndWindows(t *testing.T) {
	root := t.TempDir()
	r1 := req{id: "m1", model: "claude-sonnet-5-5", at: t0, in: 10, out: 1000, read: 100_000, write: 50_000, write1h: 50_000}
	r2 := req{id: "m2", model: "claude-sonnet-5-5", at: t0.Add(time.Minute), out: 100, read: 150_000}
	old := req{id: "m0", model: "claude-sonnet-5-5", at: t0.Add(-48 * time.Hour), out: 5, read: 9_000_000}
	synthetic := req{id: "syn", model: "<synthetic>", at: t0}
	writeSession(t, root, "proj-a", "s1", entry(r1), entry(r2), entry(old), entry(synthetic), "not json",
		`{"type":"user","message":{"content":"x"}}`)
	// The same request again in a resumed copy of the session is counted once.
	writeSession(t, root, "proj-a", "s2-resumed", entry(r2))

	rep, err := Scan(root, t0.Add(-time.Hour), t0.Add(time.Hour), 5)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Total.Requests != 2 || rep.Sessions != 1 {
		t.Fatalf("requests %d sessions %d, want 2 and 1", rep.Total.Requests, rep.Sessions)
	}
	// sonnet: 10*2 + 1100*10 + 250000*0.10 + 50000*4 = 20 + 11000 + 25000 + 200000 = 236020 / 1e6
	if !near(rep.Total.Cost, 0.23602) {
		t.Fatalf("cost %v", rep.Total.Cost)
	}
	if !near(rep.Total.CacheWriteCost, 0.2) {
		t.Fatalf("cache write cost %v", rep.Total.CacheWriteCost)
	}
	s := rep.Top[0]
	if s.StartContext != 150_010 || s.ContextMax != 150_010 || s.AvgContext != 150_005 || s.Tiny != 1 {
		t.Fatalf("session %+v", s)
	}
}

func TestFindingsFableBigContextChatterCacheWrites(t *testing.T) {
	root := t.TempDir()
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, entry(req{id: fmt.Sprintf("f%d", i), model: "claude-fable-5-2", at: t0, out: 50, read: 400_000}))
	}
	writeSession(t, root, "p", "fat", lines...)
	writeSession(t, root, "p", "spawn", entry(req{id: "w", model: "claude-opus-5-5", at: t0, out: 2000, write: 3_000_000, write1h: 3_000_000}))
	rep, err := Scan(root, t0.Add(-time.Hour), t0.Add(time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	codes := map[string]string{}
	for _, f := range rep.Findings {
		codes[f.Code] = f.Level
	}
	for code, level := range map[string]string{"fable": "high", "big-context": "high", "chatter": "high", "cache-writes": "medium"} {
		if codes[code] != level {
			t.Errorf("finding %s = %q, want %q (all: %v)", code, codes[code], level, codes)
		}
	}
	if rep.Findings[len(rep.Findings)-1].Level != "medium" {
		t.Errorf("high findings must come first: %+v", rep.Findings)
	}
	// Fable is priced: 20 * 400000 * 0.20 + 20*50*40 = 1.6 + 0.04
	for _, m := range rep.ByModel {
		if m.Key == "claude-fable-5-2" && (!near(m.Cost, 1.64) || m.Unpriced != 0) {
			t.Errorf("fable %+v", m)
		}
	}
}

func TestUnpricedModelIsFlaggedNotFatal(t *testing.T) {
	root := t.TempDir()
	writeSession(t, root, "p", "x", entry(req{id: "u", model: "claude-nope-9", at: t0, out: 5000, read: 10}))
	rep, err := Scan(root, t0.Add(-time.Hour), t0.Add(time.Hour), 10)
	if err != nil || rep.Total.Unpriced != 1 || rep.Findings[0].Code != "unpriced" {
		t.Fatalf("%v %+v", err, rep)
	}
}

func TestMissingRootIsEmptyReport(t *testing.T) {
	rep, err := Scan(filepath.Join(t.TempDir(), "none"), t0.Add(-time.Hour), t0, 10)
	if err != nil || rep.Total.Requests != 0 || rep.Findings != nil {
		t.Fatalf("%v %+v", err, rep)
	}
}

func TestSymlinkedTranscriptIsNotFollowed(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	p := writeSession(t, outside, "p", "s", entry(req{id: "x", model: "claude-opus-5-5", at: t0, out: 1}))
	if err := os.MkdirAll(filepath.Join(root, "p"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(p, filepath.Join(root, "p", "s.jsonl")); err != nil {
		t.Skip(err)
	}
	rep, _ := Scan(root, t0.Add(-time.Hour), t0.Add(time.Hour), 10)
	if rep.Total.Requests != 0 {
		t.Fatalf("followed a symlink: %+v", rep.Total)
	}
}

func readEnv(t *testing.T, p string) map[string]any {
	t.Helper()
	var d map[string]any
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	env, _ := d["env"].(map[string]any)
	return env
}

func TestLimitsApplyKeepsSettingsBacksUpAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	orig := `{"theme":"dark","env":{"KEEP":"x","CLAUDE_CODE_DISABLE_ADVISOR_TOOL":"1"},"hooks":{"a":[1]}}`
	if err := os.WriteFile(settings, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	ch, err := Apply(settings, WorkerEnv)
	if err != nil || len(ch) != len(WorkerEnv)-1 { // the advisor was already off
		t.Fatalf("%v %v", err, ch)
	}
	env := readEnv(t, settings)
	if env["KEEP"] != "x" || env["CLAUDE_CODE_DISABLE_1M_CONTEXT"] != "1" || env["BASH_MAX_OUTPUT_LENGTH"] != "20000" {
		t.Fatalf("env %v", env)
	}
	if bak, _ := os.ReadFile(settings + ".bak-hm-spend"); string(bak) != orig {
		t.Fatalf("backup %q", bak)
	}
	if st, _ := os.Stat(settings); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	if ch, err := Apply(settings, WorkerEnv); err != nil || len(ch) != 0 {
		t.Fatalf("second apply %v %v", err, ch)
	}
	if ch, _ := Plan(settings, WorkerEnv); len(ch) != 0 {
		t.Fatalf("plan after apply %v", ch)
	}
}

func TestLimitsNeverReenableAnAdvisorTurnedOff(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	_ = os.WriteFile(p, []byte(`{"env":{"CLAUDE_CODE_DISABLE_ADVISOR_TOOL":"1"}}`), 0o644)
	ch, _ := Plan(p, map[string]string{"CLAUDE_CODE_DISABLE_ADVISOR_TOOL": "0"})
	if len(ch) != 0 {
		t.Fatalf("%v", ch)
	}
}

func TestLimitsRefuseNonObjectAndWriteNothing(t *testing.T) {
	for _, body := range []string{"[1,2]", "{not json", `{"env":"x"}`, "null"} {
		p := filepath.Join(t.TempDir(), "s.json")
		_ = os.WriteFile(p, []byte(body), 0o644)
		if _, err := Apply(p, WorkerEnv); err == nil {
			t.Fatalf("%q: no error", body)
		}
		if b, _ := os.ReadFile(p); string(b) != body {
			t.Fatalf("%q: file changed to %q", body, b)
		}
		if _, err := os.Stat(p + ".bak-hm-spend"); err == nil {
			t.Fatalf("%q: wrote a backup", body)
		}
	}
}

func TestBossEnvKeepsTheBigWindowAndWorkerEnvMatchesTheRoleSettings(t *testing.T) {
	if BossEnv["CLAUDE_CODE_DISABLE_1M_CONTEXT"] != "0" || WorkerEnv["CLAUDE_CODE_DISABLE_1M_CONTEXT"] != "1" {
		t.Fatal("boss must keep 1M, workers 200k")
	}
	if _, has := BossEnv["CLAUDE_CODE_DISABLE_ADVISOR_TOOL"]; has {
		t.Fatal("the boss file must not touch the advisor setting")
	}
	// The factory's role settings carry the same limits (roles/settings/*.json.in).
	matches, _ := filepath.Glob("../../../../roles/settings/*.json.in")
	if len(matches) == 0 {
		t.Skip("roles/ not found")
	}
	for _, m := range matches {
		var d struct{ Env map[string]string }
		b, _ := os.ReadFile(m)
		if err := json.Unmarshal(b, &d); err != nil {
			continue // templates with @TOKENS@ outside strings are checked by the spawn golden tests
		}
		want := WorkerEnv
		if strings.HasSuffix(m, "boss.json.in") {
			want = BossEnv
		}
		for k, v := range want {
			if k == "CLAUDE_CODE_DISABLE_ADVISOR_TOOL" {
				continue
			}
			if d.Env[k] != v {
				t.Errorf("%s: env %s = %q, want %q", filepath.Base(m), k, d.Env[k], v)
			}
		}
	}
}

func TestMainUsageAndCheckExitCodes(t *testing.T) {
	var out, errb bytes.Buffer
	if c := Main(nil, &out, &errb); c != ExitUsage {
		t.Fatalf("no args: %d", c)
	}
	if c := Main([]string{"nope"}, &out, &errb); c != ExitUsage {
		t.Fatalf("unknown: %d", c)
	}
	if c := Main([]string{"report", "--hours", "0"}, &out, &errb); c != ExitUsage {
		t.Fatalf("hours 0: %d", c)
	}
	dir := t.TempDir()
	s, b := filepath.Join(dir, "s.json"), filepath.Join(dir, "b.json")
	if c := Main([]string{"limits", "--settings", s, "--boss-settings", s}, &out, &errb); c != ExitUsage {
		t.Fatalf("same file: %d", c)
	}
	if c := Main([]string{"limits", "--settings", s, "--boss-settings", b, "--check"}, &out, &errb); c != ExitRefused {
		t.Fatalf("check before apply: %d", c)
	}
	if _, err := os.Stat(s); err == nil {
		t.Fatal("--check wrote a file")
	}
	if c := Main([]string{"limits", "--settings", s, "--boss-settings", b, "--apply"}, &out, &errb); c != ExitOK {
		t.Fatalf("apply: %d %s", c, errb.String())
	}
	if c := Main([]string{"limits", "--settings", s, "--boss-settings", b, "--check"}, &out, &errb); c != ExitOK {
		t.Fatalf("check after apply: %d", c)
	}
	root := t.TempDir()
	now := time.Now().UTC()
	var lines []string
	for i := 0; i < 12; i++ {
		lines = append(lines, entry(req{id: fmt.Sprintf("z%d", i), model: "claude-fable-5-2", at: now.Add(-time.Minute), out: 10, read: 300_000}))
	}
	writeSession(t, root, "p", "s", lines...)
	out.Reset()
	if c := Main([]string{"report", "--root", root, "--check"}, &out, &errb); c != ExitRefused || !strings.Contains(out.String(), "fable") {
		t.Fatalf("report --check: %d\n%s", c, out.String())
	}
	out.Reset()
	if c := Main([]string{"report", "--root", root, "--json"}, &out, &errb); c != ExitOK {
		t.Fatalf("json: %d", c)
	}
	var rep Report
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil || rep.Total.Requests != 12 {
		t.Fatalf("json report: %v %+v", err, rep.Total)
	}
}

const kickoffPM = "You are `claude-pm-t-1775`, the factory pm. Run `factory whoami` first."

func userLine(text string) string {
	b, _ := json.Marshal(map[string]any{"type": "user", "timestamp": t0.Format(time.RFC3339Nano),
		"message": map[string]any{"role": "user", "content": text}})
	return string(b)
}

func TestRoleFromKickoff(t *testing.T) {
	root := t.TempDir()
	m := "claude-sonnet-5-5"
	writeSession(t, root, "p", "with-kickoff", userLine(kickoffPM), entry(req{id: "a1", model: m, at: t0, out: 500, write: 1000}))
	writeSession(t, root, "p", "no-kickoff", userLine("hello there"), entry(req{id: "b1", model: m, at: t0, out: 500, write: 1000}))
	writeSession(t, root, "p", "unknown-role", userLine("You are `x-wizard-1`, the factory wizard."), entry(req{id: "c1", model: m, at: t0, out: 500}))
	rep, err := Scan(root, t0.Add(-time.Hour), t0.Add(time.Hour), 5)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, r := range rep.ByRole {
		got[r.Role] = r.Sessions
	}
	t.Run("kickoff gives pm", func(t *testing.T) {
		if got["pm"] != 1 {
			t.Fatalf("roles %v", got)
		}
	})
	t.Run("no kickoff gives other", func(t *testing.T) {
		if got["other"] != 2 {
			t.Fatalf("roles %v", got)
		}
	})
}

func TestRoleFromKickoffOptionB(t *testing.T) {
	for kickoff, want := range map[string]string{
		"You are `claude-coder-t-1841-a7`, the factory task.":       "task",
		"You are `claude-judge-t-1841-a7`, the factory task-judge.": "task-judge",
	} {
		if got := RoleFromKickoff(kickoff); got != want {
			t.Errorf("RoleFromKickoff(%q) = %q, want %q", kickoff, got, want)
		}
	}
}

func TestRoleSubagentTakesParent(t *testing.T) {
	root := t.TempDir()
	m := "claude-sonnet-5-5"
	writeSession(t, root, "p", "parent1", userLine("You are `claude-coder-t-1`, the factory coder."), entry(req{id: "p1", model: m, at: t0, out: 500}))
	writeSession(t, root, "p/parent1/subagents", "agent-x", userLine("review this"), entry(req{id: "s1", model: m, at: t0, out: 500}))
	writeSession(t, root, "p/gone/subagents", "agent-y", userLine("review this"), entry(req{id: "s2", model: m, at: t0, out: 500}))
	rep, err := Scan(root, t0.Add(-time.Hour), t0.Add(time.Hour), 5)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, r := range rep.ByRole {
		got[r.Role] = r.Requests
	}
	if got["coder"] != 2 || got["other"] != 1 {
		t.Fatalf("roles %v", got)
	}
}

func TestBucketSplitSumsToTotal(t *testing.T) {
	root := t.TempDir()
	m := "claude-sonnet-5-5"
	first := req{id: "f", model: m, at: t0, in: 10, out: 1000, read: 1000, write: 50_000}
	tiny := req{id: "t", model: m, at: t0.Add(time.Minute), out: 100, read: 60_000}
	long := req{id: "l", model: m, at: t0.Add(2 * time.Minute), out: 2000, read: 60_000, write: 2000}
	writeSession(t, root, "p", "s", userLine(kickoffPM), entry(first), entry(tiny), entry(long))
	rep, err := Scan(root, t0.Add(-time.Hour), t0.Add(time.Hour), 5)
	if err != nil {
		t.Fatal(err)
	}
	tot := rep.Total
	if math.Abs(tot.SpawnCost+tot.PollingCost+tot.WorkCost-tot.Cost) > 0.01 {
		t.Fatalf("buckets %v+%v+%v != %v", tot.SpawnCost, tot.PollingCost, tot.WorkCost, tot.Cost)
	}
	// sonnet: cache write 2.5/M (5m), read 0.10/M, output 10/M (see tokens prices)
	if !near(tot.SpawnCost, 0.125) { // first request's 50k 5m cache write at 2.5/M
		t.Fatalf("spawn %v", tot.SpawnCost)
	}
	if !near(tot.PollingCost, 60_000*0.1/1e6+100*10/1e6) {
		t.Fatalf("polling %v", tot.PollingCost)
	}
	if tot.WorkCost <= 0 {
		t.Fatalf("work %v", tot.WorkCost)
	}
	if rep.ByRole[0].Role != "pm" || !near(rep.ByRole[0].SpawnCost, tot.SpawnCost) {
		t.Fatalf("by role %+v", rep.ByRole)
	}
}

func TestMergedPRsFlag(t *testing.T) {
	root := t.TempDir()
	writeSession(t, root, "p", "s", userLine(kickoffPM), entry(req{id: "f", model: "claude-sonnet-5-5", at: time.Now(), out: 500, write: 10_000}))
	var out, errb bytes.Buffer
	if c := Main([]string{"report", "--root", root, "--merged-prs", "0"}, &out, &errb); c != ExitOK || !strings.Contains(out.String(), "merged PRs: not given") {
		t.Fatalf("exit %d out %s err %s", c, out.String(), errb.String())
	}
	if !strings.HasPrefix(out.String(), "Last 24h: $") || !strings.Contains(out.String(), "By role:") {
		t.Fatalf("old output changed: %s", out.String())
	}
	out.Reset()
	if c := Main([]string{"report", "--root", root, "--json", "--merged-prs", "4"}, &out, &errb); c != ExitOK {
		t.Fatalf("exit %d %s", c, errb.String())
	}
	var j struct {
		PerPR float64 `json:"per_merged_pr_usd"`
		Total struct {
			Cost  float64 `json:"cost_usd"`
			Spawn float64 `json:"spawn_usd"`
		} `json:"total"`
		ByRole []struct {
			PerPR float64 `json:"per_merged_pr_usd"`
		} `json:"by_role"`
		ByModel []any `json:"by_model"`
	}
	if err := json.Unmarshal(out.Bytes(), &j); err != nil {
		t.Fatal(err)
	}
	if !near(j.PerPR, j.Total.Cost/4) || len(j.ByRole) != 1 || !near(j.ByRole[0].PerPR, j.Total.Cost/4) || j.Total.Spawn <= 0 || len(j.ByModel) != 1 {
		t.Fatalf("json %+v", j)
	}
	out.Reset()
	if c := Main([]string{"report", "--root", root, "--merged-prs", "-1"}, &out, &errb); c != ExitUsage {
		t.Fatalf("negative: exit %d", c)
	}
}
