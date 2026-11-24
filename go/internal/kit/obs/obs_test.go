package obs

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"factory/go/internal/kit/spend"
	"factory/go/internal/prflow"
)

var t0 = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

type req struct {
	id, model string
	at        time.Time
	out, read int64
	write     int64
}

func entry(r req) string {
	usage := map[string]any{"input_tokens": 10, "output_tokens": r.out, "cache_read_input_tokens": r.read,
		"cache_creation_input_tokens": r.write, "cache_creation": map[string]any{"ephemeral_1h_input_tokens": r.write,
			"ephemeral_5m_input_tokens": 0}}
	b, _ := json.Marshal(map[string]any{"type": "assistant", "timestamp": r.at.Format(time.RFC3339Nano),
		"requestId": "q" + r.id, "message": map[string]any{"id": r.id, "model": r.model, "usage": usage}})
	return string(b)
}

func userLine(text string) string {
	b, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"content": text}})
	return string(b)
}

func writeFile(t *testing.T, root, rel string, lines ...string) string {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func appendFile(t *testing.T, p, s string) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func ingest(t *testing.T, home, root string, now time.Time) *IngestStats {
	t.Helper()
	st, err := Ingest(IngestOptions{Paths: PathsFor(home), Root: root, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.006 }

// fixtureRoot holds a coder session (with a subagent), a reviewer session and a session without a kickoff.
func fixtureRoot(t *testing.T) string {
	root := t.TempDir()
	m := "claude-sonnet-5-5"
	writeFile(t, root, "-home-x-factory-worktrees-G-7-T-70/s-coder.jsonl",
		userLine("You are `claude-coder-t-70`, the factory coder. Build T-70."),
		entry(req{id: "c1", model: m, at: t0.Add(-3 * time.Hour), out: 2000, read: 50_000, write: 40_000}),
		entry(req{id: "c2", model: m, at: t0.Add(-2 * time.Hour), out: 50, read: 90_000}),
		entry(req{id: "c3", model: m, at: t0.Add(-1 * time.Hour), out: 900, read: 90_000}))
	writeFile(t, root, "-home-x-factory-worktrees-G-7-T-70/s-coder/subagents/agent-a1.jsonl",
		userLine("review this diff"),
		entry(req{id: "a1", model: "claude-haiku-5-5", at: t0.Add(-90 * time.Minute), out: 400, read: 10_000, write: 8_000}))
	writeFile(t, root, "-home-x-factory/s-rev.jsonl",
		userLine("You are `claude-reviewer-t-70`, the factory reviewer."),
		entry(req{id: "r1", model: "claude-opus-5-5", at: t0.Add(-5 * time.Hour), out: 3000, read: 20_000, write: 30_000}),
		entry(req{id: "r2", model: "claude-opus-5-5", at: t0.Add(-30 * time.Hour), out: 3000, read: 20_000}))
	writeFile(t, root, "-home-x-other/s-misc.jsonl",
		userLine("hello"),
		entry(req{id: "x1", model: m, at: t0.Add(-4 * time.Hour), out: 10, read: 5_000, write: 1_000}))
	return root
}

func TestIngestMatchesHmSpendBuckets(t *testing.T) {
	root, home := fixtureRoot(t), t.TempDir()
	st := ingest(t, home, root, t0)
	if st.Files != 4 || st.Requests != 7 {
		t.Fatalf("stats %+v, want 4 files and 7 requests", st)
	}
	want, err := spend.Scan(root, t0.Add(-24*time.Hour), t0, 10)
	if err != nil {
		t.Fatal(err)
	}
	a, err := Spend(PathsFor(home).Store, SpendQuery{Hours: 24, Group: "role", Now: t0})
	if err != nil {
		t.Fatal(err)
	}
	c := a.Current
	if c.Requests != want.Total.Requests || !near(c.Cost, want.Total.Cost) || !near(c.Spawn, want.Total.SpawnCost) ||
		!near(c.Polling, want.Total.PollingCost) || !near(c.Work, want.Total.WorkCost) {
		t.Fatalf("obs %+v\nhm-spend %+v", c, want.Total)
	}
	got := map[string]float64{}
	for _, r := range a.Rows {
		got[r.Key] = r.Cost
	}
	for _, r := range want.ByRole {
		if !near(got[r.Role], r.Cost) {
			t.Fatalf("role %s: obs %v, hm-spend %v (all %v)", r.Role, got[r.Role], r.Cost, got)
		}
	}
	// The 30 h old reviewer request is in the previous window.
	if a.Previous.Requests != 1 {
		t.Fatalf("previous window %+v", a.Previous)
	}
}

func TestIngestIsIncrementalAndWaitsForWholeLines(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	p := writeFile(t, root, "proj/s1.jsonl", userLine("You are `claude-qa-t-1`, the factory qa."),
		entry(req{id: "q1", model: "claude-sonnet-5-5", at: t0.Add(-time.Hour), out: 20_000, write: 200_000}))
	if st := ingest(t, home, root, t0); st.Requests != 1 {
		t.Fatalf("first run %+v", st)
	}
	if st := ingest(t, home, root, t0); st.FilesRead != 0 || st.Requests != 0 {
		t.Fatalf("unchanged file read again: %+v", st)
	}
	line := entry(req{id: "q2", model: "claude-sonnet-5-5", at: t0.Add(-time.Minute), out: 20_000})
	appendFile(t, p, line[:40]) // a line still being written
	if st := ingest(t, home, root, t0); st.Requests != 0 {
		t.Fatalf("partial line counted: %+v", st)
	}
	appendFile(t, p, line[40:]+"\n")
	if st := ingest(t, home, root, t0); st.Requests != 1 {
		t.Fatalf("completed line not counted once: %+v", st)
	}
	a, err := Spend(PathsFor(home).Store, SpendQuery{Hours: 24, Group: "role", Now: t0})
	if err != nil {
		t.Fatal(err)
	}
	// Only the first request of the file is a spawn; its role comes from the kickoff read in the first run.
	if a.Current.Requests != 2 || len(a.Rows) != 1 || a.Rows[0].Key != "qa" || a.Rows[0].Spawn == 0 || a.Rows[0].Work == 0 {
		t.Fatalf("answer %+v rows %+v", a.Current, a.Rows)
	}
	// A truncated (rewritten) file is read again from the start; requests dedupe.
	writeFile(t, root, "proj/s1.jsonl", entry(req{id: "q1", model: "claude-sonnet-5-5", at: t0.Add(-time.Hour), out: 20_000, write: 200_000}))
	if st := ingest(t, home, root, t0); st.FilesRead != 1 || st.Requests != 0 {
		t.Fatalf("rewritten file: %+v", st)
	}
}

func TestIngestRefusesASecondRun(t *testing.T) {
	home := t.TempDir()
	p := PathsFor(home)
	if err := os.MkdirAll(filepath.Dir(p.Lock), 0o755); err != nil {
		t.Fatal(err)
	}
	lk, err := os.OpenFile(p.Lock, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Close()
	if err := syscall.Flock(int(lk.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := Main([]string{"ingest", "--home", home, "--root", t.TempDir(), "--flow-every", "0"}, &out, &errb); code != ExitBusy {
		t.Fatalf("exit %d, want %d (%s)", code, ExitBusy, errb.String())
	}
}

const factoryDDL = `
CREATE TABLE projects(id TEXT PRIMARY KEY, gh_repo TEXT);
CREATE TABLE goals(id TEXT PRIMARY KEY, project_id TEXT);
CREATE TABLE tasks(id TEXT PRIMARY KEY, goal_id TEXT, pr_url TEXT);
CREATE TABLE agents(name TEXT, role TEXT, goal_id TEXT, task_id TEXT, session_id TEXT, started TEXT, ended TEXT);
CREATE TABLE events(id INTEGER PRIMARY KEY, entity TEXT, entity_id TEXT, detail TEXT);
`

func factoryDB(t *testing.T, home string, stmts ...string) {
	t.Helper()
	p := PathsFor(home).FactoryDB
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range append([]string{factoryDDL}, stmts...) {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

func TestAttributionOrder(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	m := "claude-sonnet-5-5"
	at := func(id string) string { return entry(req{id: id, model: m, at: t0.Add(-time.Hour), out: 500}) }
	writeFile(t, root, "p/s-sess.jsonl", at("1"))
	writeFile(t, root, "p/s-event.jsonl", at("2"))
	writeFile(t, root, "p/s-name.jsonl", `{"type":"agent-name","agentName":"claude-qa-t-3"}`, at("3"))
	writeFile(t, root, "p/s-kick.jsonl", userLine("You are `claude-judge-t-4`, the factory judge."), at("4"))
	writeFile(t, root, "-home-x-factory-worktrees-G-9-T-5/s-wt.jsonl", at("5"))
	writeFile(t, root, "p/s-none.jsonl", at("6"))
	writeFile(t, root, "p/s-sess/subagents/agent-z.jsonl", at("7"))
	// The kickoff names an agent whose run ended before the session: not that agent.
	writeFile(t, root, "p/s-late.jsonl", userLine("You are `claude-old-t-8`, the factory coder."), at("8"))
	start := t0.Add(-2 * time.Hour).Format("2006-01-02 15:04:05")
	factoryDB(t, home,
		`INSERT INTO projects VALUES('hm','o/hm')`,
		`INSERT INTO goals VALUES('G-1','hm'),('G-9','hm')`,
		`INSERT INTO tasks VALUES('T-1','G-1','https://github.com/o/hm/pull/1'),('T-5','G-9','')`,
		`INSERT INTO agents VALUES('claude-coder-t-1','coder','G-1','T-1','s-sess','`+start+`',NULL),
			('claude-pm-t-2','pm','G-1','T-1',NULL,'`+start+`',NULL),
			('claude-qa-t-3','qa','G-1','T-1',NULL,'`+start+`',NULL),
			('claude-judge-t-4','judge','G-1','T-1',NULL,'`+start+`',NULL),
			('claude-old-t-8','coder','G-1','T-1',NULL,'2026-01-01 00:00:00','2026-01-02 00:00:00')`,
		`INSERT INTO events(entity, entity_id, detail) VALUES('agent','claude-pm-t-2','{"stored":"s-event"}')`)
	ingest(t, home, root, t0)
	db, err := openRO(PathsFor(home).Store, "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	want := map[string][5]string{ // via, agent, role, goal, project
		"s-sess":  {ViaSession, "claude-coder-t-1", "coder", "G-1", "hm"},
		"s-event": {ViaEvent, "claude-pm-t-2", "pm", "G-1", "hm"},
		"s-name":  {ViaName, "claude-qa-t-3", "qa", "G-1", "hm"},
		"s-kick":  {ViaKickoff, "claude-judge-t-4", "judge", "G-1", "hm"},
		"s-wt":    {ViaWorktree, "", "other", "G-9", "hm"},
		"s-none":  {ViaUnattributed, "", "other", "", ""},
		"agent-z": {ViaSession, "claude-coder-t-1", "coder", "G-1", "hm"},
		"s-late":  {ViaUnattributed, "", "coder", "", ""},
	}
	for s, w := range want {
		var got [5]string
		var pr string
		if err := db.QueryRow(`SELECT via, agent, role, goal, project, pr FROM sessions WHERE session=?`, s).Scan(
			&got[0], &got[1], &got[2], &got[3], &got[4], &pr); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		if got != w {
			t.Errorf("%s: got %v, want %v", s, got, w)
		}
		if s == "s-sess" && pr != "https://github.com/o/hm/pull/1" {
			t.Errorf("s-sess pr %q", pr)
		}
	}
	a, err := Spend(PathsFor(home).Store, SpendQuery{Hours: 24, Group: "goal", Now: t0})
	if err != nil {
		t.Fatal(err)
	}
	var labels []string
	for _, r := range a.Rows {
		labels = append(labels, r.Label)
	}
	if !strings.Contains(strings.Join(labels, ","), "Not attributed") || a.UnattributedPct != 25 {
		t.Fatalf("labels %v unattributed %v%%", labels, a.UnattributedPct)
	}
	// Drill: goal G-1 by role.
	d, err := Spend(PathsFor(home).Store, SpendQuery{Hours: 24, Group: "role", Filters: map[string]string{"goal": "G-1"}, Now: t0})
	if err != nil {
		t.Fatal(err)
	}
	if d.Current.Requests != 5 || d.Drill != "model" {
		t.Fatalf("drill %+v %q", d.Current, d.Drill)
	}
}

// TestPrivacyNoContentIsStored puts secrets everywhere content can be and checks that no byte of them reaches
// the store, while the names that may be stored do.
func TestPrivacyNoContentIsStored(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	secrets := []string{"SECRET_KICKOFF_TAIL", "SECRET_ASSISTANT_TEXT", "SECRET_BASH_ARG", "SECRET_FILE_BODY",
		"SECRET_TOOL_OUTPUT", "SECRET_CWD", "SECRET_PROMPT", "SECRET_SKILL_ARGS", "SECRET_SUBCMD"}
	m := "claude-sonnet-5-5"
	asst := func(id string, content any) string {
		b, _ := json.Marshal(map[string]any{"type": "assistant", "timestamp": t0.Add(-time.Hour).Format(time.RFC3339),
			"requestId": "q" + id, "cwd": "/srv/x/SECRET_CWD/factory/worktrees/G-2-T-3",
			"message": map[string]any{"id": id, "model": m, "content": content,
				"usage": map[string]any{"input_tokens": 1, "output_tokens": 900}}})
		return string(b)
	}
	user := func(content any) string {
		b, _ := json.Marshal(map[string]any{"type": "user", "cwd": "/SECRET_CWD", "message": map[string]any{"content": content}})
		return string(b)
	}
	writeFile(t, root, "-home-x-proj/s1.jsonl",
		user("You are `claude-coder-t-3`, the factory coder. SECRET_KICKOFF_TAIL"),
		asst("m1", []any{
			map[string]any{"type": "text", "text": "SECRET_ASSISTANT_TEXT"},
			map[string]any{"type": "tool_use", "id": "tu1", "name": "Bash",
				"input": map[string]any{"command": "API_KEY=SECRET_BASH_ARG git push origin SECRET_BASH_ARG"}},
			map[string]any{"type": "tool_use", "id": "tu2", "name": "Write",
				"input": map[string]any{"file_path": "/x", "content": "SECRET_FILE_BODY"}},
			map[string]any{"type": "tool_use", "id": "tu3", "name": "Skill",
				"input": map[string]any{"skill": "coder-skills:self-review", "args": "SECRET_SKILL_ARGS"}},
			map[string]any{"type": "tool_use", "id": "tu4", "name": "Bash",
				"input": map[string]any{"command": "git SECRET_SUBCMD"}},
			map[string]any{"type": "tool_use", "id": "tu5", "name": "SECRET_PROMPT tool"},
		}),
		user([]any{map[string]any{"type": "tool_result", "tool_use_id": "tu1", "content": "SECRET_TOOL_OUTPUT 12345"}}),
		user("SECRET_PROMPT"))
	ingest(t, home, root, t0)
	db, err := openRO(PathsFor(home).Store, "")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT tool, skill, head, COALESCE(result_bytes,-1) FROM tool_calls ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var tool, skill, head string
		var n int
		if err := rows.Scan(&tool, &skill, &head, &n); err != nil {
			t.Fatal(err)
		}
		got = append(got, strings.Join([]string{tool, skill, head}, "|")+"|"+itoa(n))
	}
	rows.Close()
	var role, task string
	if err := db.QueryRow(`SELECT role, wt_task FROM files`).Scan(&role, &task); err != nil {
		t.Fatal(err)
	}
	db.Close()
	want := "Bash||git push|24,Write|||-1,Skill|coder-skills:self-review||-1,Bash||git|-1,other|||-1"
	if strings.Join(got, ",") != want || role != "coder" || task != "T-3" {
		t.Fatalf("tool calls %v role %q task %q\nwant %s", got, role, task, want)
	}
	// No secret in any byte of the store, its WAL or its shared memory.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		b, err := os.ReadFile(PathsFor(home).Store + suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range append(secrets, "SECRET") {
			if bytes.Contains(b, []byte(s)) {
				t.Fatalf("obs.db%s holds %q", suffix, s)
			}
		}
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestBashHead(t *testing.T) {
	for in, want := range map[string]string{
		"git status --short":         "git status",
		"FOO=1 BAR=2 go test ./...":  "go test",
		"/usr/bin/git log -1":        "git log",
		"git secret-thing":           "git",
		"curl https://x.example/?t=": "curl",
		"supersecret --flag":         "other",
		"":                           "other",
		"A=1":                        "other",
	} {
		if got := BashHead(in); got != want {
			t.Errorf("BashHead(%q) = %q, want %q", in, got, want)
		}
	}
}

func stubFlow(t *testing.T, reps map[int]prflow.Report) *int {
	t.Helper()
	calls := 0
	old := RunFlow
	RunFlow = func(o prflow.Options) (*prflow.Report, error) {
		calls++
		r, ok := reps[int(o.Since/time.Hour)]
		if !ok {
			r = prflow.Report{}
		}
		return &r, nil
	}
	t.Cleanup(func() { RunFlow = old })
	return &calls
}

func TestFlowSnapshotsBannersAndStuck(t *testing.T) {
	home := t.TempDir()
	factoryDB(t, home, `INSERT INTO projects VALUES('hm','o/hm'),('none',''),('dup','o/hm')`)
	rep24 := prflow.Report{PRs: 10, MultiRoundPRs: 4,
		OpenToMerge: []prflow.MergeStats{{Org: "all", N: 6, Median: 3.5, P90: 9}},
		OpenOver6h: []prflow.OpenPR{{Number: 7, AgeHours: 8, Stage: "judge", Task: "T-7"},
			{Number: 9, AgeHours: 20, Stage: "ci_wait", Task: "T-9"}},
		StageHours: []prflow.StageRow{{Stage: "ci_wait", All: 40}, {Stage: "review_qa", All: 30},
			{Stage: "judge", All: 20}, {Stage: "hold", All: 10}},
		MergedPerDay: []prflow.DayRow{{Day: "2026-11-17", All: 6}}}
	calls := stubFlow(t, map[int]prflow.Report{24: rep24,
		48: {OpenToMerge: []prflow.MergeStats{{Org: "all", N: 10}}}})
	st, err := Ingest(IngestOptions{Paths: PathsFor(home), Root: t.TempDir(), Now: t0, FlowEvery: 15 * time.Minute})
	if err != nil || st.Flow != "refreshed" || *calls != len(FlowWindows) {
		t.Fatalf("flow %q calls %d err %v", st.Flow, *calls, err)
	}
	st, _ = Ingest(IngestOptions{Paths: PathsFor(home), Root: t.TempDir(), Now: t0.Add(time.Minute), FlowEvery: 15 * time.Minute})
	if st.Flow != "fresh" || *calls != len(FlowWindows) {
		t.Fatalf("second run flow %q calls %d", st.Flow, *calls)
	}
	f, err := Flow(PathsFor(home).Store, 24, "")
	if err != nil {
		t.Fatal(err)
	}
	if f.Merged != 6 || *f.MedianH != 3.5 || *f.P90H != 9 || f.MultiRound != 4 || f.PRs != 10 || f.OpenOver6h != 2 {
		t.Fatalf("flow %+v", f)
	}
	if f.Stuck[0].Number != 9 || f.StageHours != 100 || f.Stages[0].Label != "CI wait" || f.Stages[0].Share != 40 {
		t.Fatalf("stuck %+v stages %+v", f.Stuck, f.Stages)
	}
	if len(f.Banners) != 2 || !strings.HasPrefix(f.Banners[0], "! 4 of 10 PRs (40%)") ||
		!strings.HasPrefix(f.Banners[1], "! CI wait holds 40% of stage hours (40 of 100 h)") {
		t.Fatalf("banners %q", f.Banners)
	}
	// Spend takes merged PRs from the snapshots: 6 now, 10-6 in the previous window.
	a, err := Spend(PathsFor(home).Store, SpendQuery{Hours: 24, Group: "role", Now: t0})
	if err != nil {
		t.Fatal(err)
	}
	if a.Current.MergedPRs == nil || *a.Current.MergedPRs != 6 || *a.Previous.MergedPRs != 4 {
		t.Fatalf("merged %v %v", a.Current.MergedPRs, a.Previous.MergedPRs)
	}
}

func TestPollingBanner(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	var lines []string
	for i := 0; i < 5; i++ {
		lines = append(lines, entry(req{id: "p" + itoa(i), model: "claude-sonnet-5-5", at: t0.Add(-time.Hour), out: 10, read: 100_000}))
	}
	writeFile(t, root, "p/s.jsonl", append([]string{entry(req{id: "w", model: "claude-sonnet-5-5", at: t0.Add(-2 * time.Hour), out: 400})}, lines...)...)
	ingest(t, home, root, t0)
	a, err := Spend(PathsFor(home).Store, SpendQuery{Hours: 24, Group: "role", Now: t0})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Banners) != 1 || !strings.HasPrefix(a.Banners[0], "! Polling is ") || !a.Rows[0].PollingWarn {
		t.Fatalf("banners %q rows %+v", a.Banners, a.Rows)
	}
	if len(a.Bins) != 24 || len(a.Rows[0].Spark) != 24 {
		t.Fatalf("bins %d spark %d", len(a.Bins), len(a.Rows[0].Spark))
	}
}

func TestMainUsageAndErrors(t *testing.T) {
	var out, errb bytes.Buffer
	cases := []struct {
		args []string
		code int
	}{
		{nil, ExitUsage},
		{[]string{"--help"}, ExitOK},
		{[]string{"nope"}, ExitUsage},
		{[]string{"api", "spend", "--home", t.TempDir(), "--hours", "5"}, ExitError},
		{[]string{"api", "spend", "--home", t.TempDir(), "--group", "bogus"}, ExitError},
		{[]string{"api", "flow", "--home", t.TempDir()}, ExitError}, // no store yet: says to run ingest
		{[]string{"ingest", "--home", ""}, ExitUsage},
		{[]string{"ingest", "--home", t.TempDir(), "--now", "yesterday"}, ExitUsage},
	}
	for _, c := range cases {
		out.Reset()
		errb.Reset()
		if got := Main(c.args, &out, &errb); got != c.code {
			t.Errorf("%v: exit %d, want %d (%s)", c.args, got, c.code, errb.String())
		}
	}
	errb.Reset()
	Main([]string{"api", "flow", "--home", t.TempDir()}, &out, &errb)
	if !strings.Contains(errb.String(), "run hm-obs ingest first") {
		t.Fatalf("missing store error: %s", errb.String())
	}
	home, root := t.TempDir(), fixtureRoot(t)
	if code := Main([]string{"ingest", "--home", home, "--root", root, "--now", t0.Format(time.RFC3339), "--flow-every", "0"}, &out, &errb); code != ExitOK {
		t.Fatalf("ingest exit %d: %s", code, errb.String())
	}
	out.Reset()
	if code := Main([]string{"api", "spend", "--home", home, "--now", t0.Format(time.RFC3339), "--group", "model", "--role", "coder"}, &out, &errb); code != ExitOK {
		t.Fatalf("api exit %d: %s", code, errb.String())
	}
	var a SpendAnswer
	if err := json.Unmarshal(out.Bytes(), &a); err != nil || a.Group != "model" || a.Filters["role"] != "coder" || a.Current.Requests != 4 {
		t.Fatalf("api answer %+v err %v", a, err)
	}
}
