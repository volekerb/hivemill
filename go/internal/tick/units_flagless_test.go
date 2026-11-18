package tick

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Tests for units 25 flagless and 26 claude_audit on a fake proc tree ($FACTORY_PROC_DIR, off the live home) and a
// temp DB. Throttle clocks are injected (reapTime); events use the fixed clock of reapTestDB.

const (
	fakeNow  = 1_800_000_000.0
	agentTok = "/srv/fh/roles/settings/coder.json"
)

// procEnv gives a fake proc dir on a non-live home and fixes the throttle clock.
func procEnv(t *testing.T) (root string, now *float64) {
	t.Helper()
	root = t.TempDir()
	t.Setenv("FACTORY_PROC_DIR", root)
	withPruneIdentity(t, pruneTestEnv(t.TempDir(), t.TempDir(), "2026-11-17T09:30:15+00:00", time.Now))
	n := fakeNow
	prev := reapTime
	reapTime = func() float64 { return n }
	t.Cleanup(func() { reapTime = prev })
	return root, &n
}

func writeProc(t *testing.T, root string, pid int, ppid int, start string, argv ...string) {
	t.Helper()
	d := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := strings.Join(argv, "\x00") + "\x00"
	fields := []string{"S", strconv.Itoa(ppid)}
	for len(fields) < 19 {
		fields = append(fields, "0")
	}
	fields = append(fields, start)
	stat := fmt.Sprintf("%d (claude) %s\n", pid, strings.Join(fields, " "))
	for f, v := range map[string]string{"cmdline": cmd, "stat": stat} {
		if err := os.WriteFile(filepath.Join(d, f), []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func scalar(t *testing.T, db *sql.DB, q string, args ...any) string {
	t.Helper()
	var v sql.NullString
	if err := db.QueryRow(q, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return v.String
}

func runUnit(t *testing.T, db *sql.DB, fn GoFunc) {
	t.Helper()
	if err := fn(&UnitCtx{DB: db, Home: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
}

func addLive(t *testing.T, db *sql.DB, name, role, session, extra string) {
	t.Helper()
	mustExec(t, db, "INSERT INTO agents(name,role,status,session_id,pane_id,task_id,goal_id,spawn_args) VALUES(?,?,?,?,?,?,?,?)",
		name, role, "live", session, "p-"+name, "T-9", "G-9", extra)
}

func TestUnitTableFlaglessAudit(t *testing.T) {
	for _, u := range []string{"flagless", "claude_audit"} {
		if _, ok := goUnits[u]; !ok {
			t.Errorf("unit %s is not Go-capable", u)
		}
	}
	if unitTableIndex("flagless") != 25 || unitTableIndex("claude_audit") != 26 {
		t.Errorf("units 25/26 are at %d/%d", unitTableIndex("flagless"), unitTableIndex("claude_audit"))
	}
	for _, u := range []string{"judge_ci_prune", "hold_expiry"} { // Go on origin/main: still Go
		if _, ok := goUnits[u]; !ok {
			t.Errorf("%s lost its Go flag", u)
		}
	}
	for _, u := range []string{"pending_nudges", "reap_loop", "exited_panes", "idle_coders", "stop_fallback", "dequeue"} {
		if _, ok := goUnits[u]; ok {
			t.Errorf("%s must stay a Python unit in this PR", u)
		}
	}
}

func TestFlaglessScan(t *testing.T) {
	root, now := procEnv(t)
	db, _ := reapTestDB(t)
	addLive(t, db, "claude-coder-a", "coder", "sess-a", "")    // bare resume: flagless
	addLive(t, db, "claude-coder-b", "coder", "sess-b", "")    // has its role flags: ok
	addLive(t, db, "claude-eval-c", "evaluator", "sess-c", "") // evaluator: skipped
	addLive(t, db, "claude-coder-d", "coder", "sess-d", "")    // checked 10 min ago: untouched
	addLive(t, db, "claude-coder-e", "coder", "sess-e", "")    // flagged before, now fixed
	mustExec(t, db, "UPDATE agents SET flagless=1, flagless_since='2026-01-01T00:00:00+00:00' WHERE name='claude-coder-e'")
	addLive(t, db, "claude-coder-f", "coder", "sess-f", "") // no process: unknown, nothing changes
	writeProc(t, root, 101, 1, "5", "claude", "--resume", "sess-a")
	writeProc(t, root, 102, 1, "5", "claude", "--resume", "sess-b", "--settings", agentTok)
	writeProc(t, root, 103, 1, "5", "claude", "--resume", "sess-c")
	writeProc(t, root, 104, 1, "5", "claude", "--resume", "sess-d")
	writeProc(t, root, 105, 1, "5", "claude", "--session-id=sess-e", "--settings="+agentTok)
	mustExec(t, db, "INSERT INTO throttle(key,ts) VALUES('flagless_check:claude-coder-d',?)", strconv.FormatFloat(*now-600, 'f', 1, 64))
	mustExec(t, db, "INSERT INTO agents(name,role,status) VALUES('director','director','live')") // no session: not scanned

	runUnit(t, db, runFlagless)

	if got := scalar(t, db, "SELECT flagless FROM agents WHERE name='claude-coder-a'"); got != "1" {
		t.Errorf("flagless claude: flagless=%s, want 1", got)
	}
	if got := scalar(t, db, "SELECT flagless_since FROM agents WHERE name='claude-coder-a'"); got != time.Date(2026, 11, 17, 9, 30, 15, 0, time.UTC).In(time.Now().Location()).Format("2006-01-02T15:04:05-07:00") {
		t.Errorf("flagless_since=%q", got)
	}
	for _, n := range []string{"claude-coder-b", "claude-eval-c", "claude-coder-d", "claude-coder-f"} {
		if got := scalar(t, db, "SELECT flagless FROM agents WHERE name=?", n); got != "0" {
			t.Errorf("%s must stay untouched, flagless=%s", n, got)
		}
	}
	if got := scalar(t, db, "SELECT flagless FROM agents WHERE name='claude-coder-e'"); got != "0" {
		t.Errorf("fixed claude still flagless=%s", got)
	}
	if got := scalar(t, db, "SELECT flagless_since FROM agents WHERE name='claude-coder-e'"); got != "" {
		t.Errorf("flagless_since of a cleared agent = %q, want NULL", got)
	}
	ev := scalar(t, db, "SELECT group_concat(actor||'|'||entity||'|'||entity_id||'|'||action||'|'||detail, char(10)) FROM events ORDER BY id")
	want := "owner|agent|claude-coder-a|flagless|{\"name\": \"claude-coder-a\", \"pane\": \"p-claude-coder-a\", \"pid\": 101}\n" +
		"owner|agent|claude-coder-e|flagless-cleared|{\"pid\": 105}"
	if ev != want {
		t.Errorf("events:\n%s\nwant:\n%s", ev, want)
	}
	if n := scalar(t, db, "SELECT count(*) FROM messages"); n != "1" {
		t.Fatalf("alerts = %s, want 1", n)
	}
	row := scalar(t, db, "SELECT sender||'|'||recipient||'|'||kind||'|'||subject||'|'||body FROM messages")
	wantMsg := "tick|director|alert|flagless agents after restore|flagless agents after restore: claude-coder-a. " +
		"Relaunch needs owner approval: close pane + factory up (or `factory relaunch <name>`)."
	if row != wantMsg {
		t.Errorf("alert:\n%s\nwant:\n%s", row, wantMsg)
	}
	if got := scalar(t, db, "SELECT ts FROM throttle WHERE key='flagless_check:claude-coder-a'"); got != "1800000000.0" {
		t.Errorf("throttle ts = %q", got)
	}
	if got := scalar(t, db, "SELECT count(*) FROM throttle WHERE key='flagless_check:claude-eval-c'"); got != "0" {
		t.Errorf("an evaluator row must not be throttled, rows=%s", got)
	}

	// a second tick inside the hour changes nothing
	runUnit(t, db, runFlagless)
	if n := scalar(t, db, "SELECT count(*) FROM messages"); n != "1" {
		t.Errorf("alerts after an immediate rerun = %s, want 1", n)
	}
	// an hour later: d (throttle expired) is checked and flagged, a (already flagged) raises no second alert
	*now += 3601
	runUnit(t, db, runFlagless)
	if n := scalar(t, db, "SELECT count(*) FROM events WHERE action='flagless' AND entity_id='claude-coder-a'"); n != "1" {
		t.Errorf("flagless events of a = %s, want 1", n)
	}
	if got := scalar(t, db, "SELECT body FROM messages ORDER BY id DESC LIMIT 1"); !strings.HasPrefix(got, "flagless agents after restore: claude-coder-d. ") {
		t.Errorf("second alert = %q, want one naming only claude-coder-d", got)
	}
}

func TestFlaglessThrottleBoundary(t *testing.T) {
	root, now := procEnv(t)
	db, _ := reapTestDB(t)
	addLive(t, db, "claude-coder-a", "coder", "sess-a", "")
	writeProc(t, root, 101, 1, "5", "claude", "--resume", "sess-a")
	mustExec(t, db, "INSERT INTO throttle(key,ts) VALUES('flagless_check:claude-coder-a',?)", strconv.FormatFloat(*now-3600, 'f', 1, 64))
	runUnit(t, db, runFlagless) // exactly 3600 s ago: no longer throttled (Python `< 3600`)
	if got := scalar(t, db, "SELECT flagless FROM agents WHERE name='claude-coder-a'"); got != "1" {
		t.Errorf("a check exactly 3600 s old must run, flagless=%s", got)
	}
}

func TestFlaglessBadThrottleEndsUnit(t *testing.T) {
	procEnv(t)
	db, _ := reapTestDB(t)
	addLive(t, db, "claude-coder-a", "coder", "sess-a", "")
	mustExec(t, db, "INSERT INTO throttle(key,ts) VALUES('flagless_check:claude-coder-a','garbage')")
	err := runFlagless(&UnitCtx{DB: db, Home: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "could not convert string to float: 'garbage'") {
		t.Fatalf("want Python's ValueError text, got %v", err)
	}
}

func TestFlaglessCursor(t *testing.T) {
	root, _ := procEnv(t)
	db, _ := reapTestDB(t)
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	hooks := `{"hooks": {"beforeShellExecution": [{"command": "x", "failClosed": true}]}}`
	if err := os.WriteFile(filepath.Join(ws, ".cursor", "hooks.json"), []byte(hooks), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := t.TempDir()
	if err := os.MkdirAll(filepath.Join(bad, ".cursor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, ".cursor", "hooks.json"), []byte(`{"hooks": {"x": [{"failClosed": false}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	addLive(t, db, "cursor-coder-a", "coder", "s1", `{"runtime": "cursor", "cursor_workspace": "`+ws+`"}`)
	addLive(t, db, "cursor-coder-b", "coder", "s2", `{"runtime": "cursor", "cursor_workspace": "`+bad+`"}`)
	writeProc(t, root, 201, 1, "5", "cursor-agent", "--workspace", ws)
	writeProc(t, root, 202, 1, "5", "agent", "--workspace="+bad)
	runUnit(t, db, runFlagless)
	if got := scalar(t, db, "SELECT flagless FROM agents WHERE name='cursor-coder-a'"); got != "0" {
		t.Errorf("cursor agent with fail-closed hooks flagged: %s", got)
	}
	if got := scalar(t, db, "SELECT flagless FROM agents WHERE name='cursor-coder-b'"); got != "1" {
		t.Errorf("cursor agent with an open hook not flagged: %s", got)
	}
}

func TestHooksDocOK(t *testing.T) {
	for doc, want := range map[string]bool{
		`{"hooks":{"a":[{"failClosed":true}]}}`:        true,
		`{"hooks":{"a":[{"failClosed":true}],"b":[]}}`: true,
		`{"hooks":{"a":[{"failClosed":1}]}}`:           false,
		`{"hooks":{}}`:                                 false,
		`{}`:                                           false,
		`{"hooks":{"a":"x"}}`:                          false,
		`{"hooks":{"a":[null]}}`:                       false,
		`[1]`:                                          false,
		`{bad`:                                         false,
	} {
		if got := hooksDocOK([]byte(doc)); got != want {
			t.Errorf("hooksDocOK(%s) = %v, want %v", doc, got, want)
		}
	}
}

func auditSetup(t *testing.T) (string, *float64, *sql.DB) {
	t.Helper()
	root, now := procEnv(t)
	db, _ := reapTestDB(t)
	addLive(t, db, "claude-coder-a", "coder", "sess-a", "")
	return root, now, db
}

func TestClaudeAudit(t *testing.T) {
	root, now, db := auditSetup(t)
	// the agent's own claude, started by spawn (--session-id, snapshot off): fine
	writeProc(t, root, 100, 1, "5", "claude", "--session-id", "sess-a", "--system-prompt-snapshot", "off")
	// a claude child of the agent's claude that spawn did not start
	writeProc(t, root, 110, 100, "5", "claude", "-p", "hello")
	// a resume of the agent's session without the snapshot flag
	writeProc(t, root, 120, 1, "5", "node", "/usr/bin/claude", "--resume", "sess-a")
	// a claude of nobody, and a `grep claude` that is not claude
	writeProc(t, root, 130, 1, "5", "claude", "--resume", "other")
	writeProc(t, root, 140, 100, "5", "grep", "claude")

	runUnit(t, db, runClaudeAudit)

	ev := scalar(t, db, "SELECT group_concat(entity_id||'|'||action||'|'||detail, char(10)) FROM events WHERE action='claude-outside-spawn'")
	want := "claude-coder-a|claude-outside-spawn|{\"pid\": 110, \"argv\": [\"claude\", \"-p\", \"hello\"], \"reasons\": [\"outside-spawn\"]}\n" +
		"claude-coder-a|claude-outside-spawn|{\"pid\": 120, \"argv\": [\"node\", \"/usr/bin/claude\", \"--resume\", \"sess-a\"], \"reasons\": [\"resume-without-snapshot-off\"]}"
	if ev != want {
		t.Errorf("events:\n%s\nwant:\n%s", ev, want)
	}
	msgs := scalar(t, db, "SELECT group_concat(sender||'|'||recipient||'|'||kind||'|'||subject||'|'||goal_id||'|'||task_id||'|'||body, char(10)) FROM messages")
	wantMsgs := "tick|director|alert|claude outside factory spawn under claude-coder-a|G-9|T-9|claude started outside factory spawn under claude-coder-a (task T-9): pid 110 claude -p hello. Only factory spawn starts Claude.\n" +
		"tick|director|alert|claude resumed without --system-prompt-snapshot off under claude-coder-a|G-9|T-9|claude resumed without --system-prompt-snapshot off (stale recorded system prompt) under claude-coder-a (task T-9): pid 120 node /usr/bin/claude --resume sess-a. Resumed without --system-prompt-snapshot off: stale role prompt; fix with factory relaunch claude-coder-a --when-idle."
	if msgs != wantMsgs {
		t.Errorf("alerts:\n%s\nwant:\n%s", msgs, wantMsgs)
	}
	for _, p := range []string{"claude-audit", "claude-outside:110", "claude-outside:120"} {
		if scalar(t, db, "SELECT count(*) FROM throttle WHERE key=?", p) != "1" {
			t.Errorf("throttle row %s missing", p)
		}
	}

	// a second run inside 300 s scans nothing: delete the throttle rows of the pids to prove it does not even look
	mustExec(t, db, "DELETE FROM throttle WHERE key LIKE 'claude-outside:%'")
	*now += 299
	runUnit(t, db, runClaudeAudit)
	if n := scalar(t, db, "SELECT count(*) FROM messages"); n != "2" {
		t.Errorf("a run inside 300 s sent alerts: %s", n)
	}
	// after 300 s it scans again, and the pid throttle rows (recreated above) keep it to one alert per pid
	*now += 1
	runUnit(t, db, runClaudeAudit)
	runUnit(t, db, runClaudeAudit)
	if n := scalar(t, db, "SELECT count(*) FROM messages"); n != "4" {
		t.Errorf("alerts after the rescan = %s, want 4 (2 new, none on the immediate rerun)", n)
	}
	if n := scalar(t, db, "SELECT count(*) FROM events WHERE action='claude-audit-failed'"); n != "0" {
		t.Errorf("unexpected audit failure events: %s", n)
	}
}

func TestClaudeAuditAncestorWalk(t *testing.T) {
	root, _, db := auditSetup(t)
	writeProc(t, root, 100, 1, "5", "claude", "--session-id", "sess-a", "--system-prompt-snapshot=off")
	// 300 -> 250 (not claude, only in the proc dir) -> 100: found through the plain parent read
	if err := os.MkdirAll(filepath.Join(root, "250"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "250", "stat"), []byte("250 (bash) S 100 "), 0o644); err != nil {
		t.Fatal(err)
	}
	writeProc(t, root, 300, 250, "5", "claude", "--resume", "zzz", "--system-prompt-snapshot", "off")
	runUnit(t, db, runClaudeAudit)
	if got := scalar(t, db, "SELECT detail FROM events WHERE action='claude-outside-spawn'"); !strings.Contains(got, `"pid": 300`) || !strings.Contains(got, "outside-spawn") {
		t.Errorf("grandchild not reported: %q", got)
	}
}

func TestClaudeAuditHeadKeepsResumeID(t *testing.T) {
	root, _, db := auditSetup(t)
	argv := []string{"claude", "a", "b", "c", "d", "e", "f", "--resume", "sess-a"}
	writeProc(t, root, 100, 1, "5", argv...)
	runUnit(t, db, runClaudeAudit)
	// Python: head = argv[:8] has the flag at index 7, so argv[7:9] is appended once more
	want := `{"pid": 100, "argv": ["claude", "a", "b", "c", "d", "e", "f", "--resume", "--resume", "sess-a"], "reasons": ["resume-without-snapshot-off"]}`
	if got := scalar(t, db, "SELECT detail FROM events WHERE action='claude-outside-spawn'"); got != want {
		t.Errorf("detail %s\nwant %s", got, want)
	}
}

func TestClaudeAuditErrorContinues(t *testing.T) {
	_, _, db := auditSetup(t)
	mustExec(t, db, "INSERT INTO throttle(key,ts) VALUES('claude-audit','not-a-time')")
	if err := runClaudeAudit(&UnitCtx{DB: db, Home: t.TempDir()}); err != nil {
		t.Fatalf("the audit must never stop the tick: %v", err)
	}
	got := scalar(t, db, "SELECT actor||'|'||entity||'|'||entity_id||'|'||detail FROM events WHERE action='claude-audit-failed'")
	if got != "owner|tick|claude-audit|could not convert string to float: 'not-a-time'" {
		t.Errorf("failed event = %q", got)
	}
	if n := scalar(t, db, "SELECT count(*) FROM events WHERE action='claude-audit-failed'"); n != "1" {
		t.Errorf("failed events = %s, want 1", n)
	}
}

func TestClaudeAuditUnreadableProcDir(t *testing.T) {
	root, _, db := auditSetup(t)
	t.Setenv("FACTORY_PROC_DIR", filepath.Join(root, "missing"))
	if err := runClaudeAudit(&UnitCtx{DB: db, Home: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	// Python reads an unlistable proc dir as "no claude process" (_scan_claude() or {}): no finding, no failure event
	if n := scalar(t, db, "SELECT count(*) FROM events"); n != "0" {
		t.Errorf("events after an unreadable proc dir: %s", n)
	}
}

func TestPruneEscapeThrottle(t *testing.T) {
	root, now, db := auditSetup(t)
	writeProc(t, root, 500, 1, "777", "claude", "--session-id", "sess-a")
	ins := func(k string, age float64) {
		mustExec(t, db, "INSERT INTO throttle(key,ts) VALUES(?,?)", k, strconv.FormatFloat(*now-age, 'f', 1, 64))
	}
	ins("sandbox-escaped:500:777", 10)       // live, same start: kept
	ins("sandbox-escaped:500:778", 10)       // pid reused (other start): dropped
	ins("sandbox-escaped:501:777", 10)       // process gone: dropped
	ins("sandbox-escaped:502:None", 10)      // unreadable start, young: kept
	ins("sandbox-escaped:503:None", 8*86400) // unreadable start, old: dropped
	ins("sandbox-escaped:500:777x", 8*86400) // old: dropped
	ins("sandbox-escaped:weird", 10)         // not pid:start: kept
	ins("other:501:1", 10)                   // another prefix: kept
	if err := pruneEscapeThrottle(db, root, *now); err != nil {
		t.Fatal(err)
	}
	got := scalar(t, db, "SELECT group_concat(key, ',') FROM (SELECT key FROM throttle ORDER BY key)")
	if want := "other:501:1,sandbox-escaped:500:777,sandbox-escaped:502:None,sandbox-escaped:weird"; got != want {
		t.Errorf("kept %s, want %s", got, want)
	}
}

func TestSandboxEscapeNeedsSameUserNS(t *testing.T) {
	root, _, db := auditSetup(t)
	_, r := reapTestDB(t)
	r.DB = db
	writeProc(t, root, 100, 1, "5", "claude", "--session-id", "sess-a")
	link := func(pid, target string) {
		d := filepath.Join(root, pid, "ns")
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(d, "user")); err != nil {
			t.Fatal(err)
		}
	}
	link("100", "user:[4026531837]")
	link("1", "user:[4026532999]") // another namespace: inside the sandbox, nothing to do
	if err := sandboxEscapeClose(r, root, "claude-coder-a", "", 100); err != nil {
		t.Fatal(err)
	}
	if n := scalar(t, db, "SELECT count(*) FROM events"); n != "0" {
		t.Errorf("a sandboxed claude in its own namespace raised %s events", n)
	}
	// same namespace, no pane row: event and alert, once per (pid, start)
	if err := os.Remove(filepath.Join(root, "1", "ns", "user")); err != nil {
		t.Fatal(err)
	}
	link("1", "user:[4026531837]")
	for i := 0; i < 2; i++ {
		if err := sandboxEscapeClose(r, root, "claude-coder-a", "", 100); err != nil {
			t.Fatal(err)
		}
	}
	if n := scalar(t, db, "SELECT count(*) FROM events WHERE action='sandbox-escaped'"); n != "1" {
		t.Errorf("sandbox-escaped events = %s, want 1", n)
	}
	body := scalar(t, db, "SELECT subject||'|'||body FROM messages")
	want := "sandbox escaped: claude-coder-a closed|claude-coder-a is a sandboxed agent but its claude (pid 100) runs in the host user namespace, outside the sandbox. It has no pane row to close; stop the process. Check its shell rc and multiplexer auto-resume, then relaunch."
	if body != want {
		t.Errorf("alert:\n%s\nwant:\n%s", body, want)
	}
	if scalar(t, db, "SELECT count(*) FROM throttle WHERE key='sandbox-escaped:100:5'") != "1" {
		t.Errorf("escape throttle row missing")
	}
}

func TestProcDirIgnoredOnLiveHome(t *testing.T) {
	db, r := reapTestDB(t)
	_ = db
	pw := t.TempDir()
	live := filepath.Join(pw, "factory")
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	withPruneIdentity(t, pruneTestEnv(pw, live, "", time.Now))
	t.Setenv("FACTORY_PROC_DIR", t.TempDir())
	t.Setenv("FACTORY_AGENT", "")
	procDirIgnored = false
	t.Cleanup(func() { procDirIgnored = false })
	if got := procRoot(r); got != "/proc" {
		t.Errorf("a live home must ignore FACTORY_PROC_DIR, got %s", got)
	}
	procRoot(r)
	if n := scalar(t, r.DB, "SELECT count(*) FROM events WHERE action='proc-dir-ignored' AND entity_id='unknown'"); n != "1" {
		t.Errorf("proc-dir-ignored events = %s, want 1 per process", n)
	}
}

func TestProcUnicodeParity(t *testing.T) {
	// Python: int("٣")==3, int("1_0")==10, int("+5")==5, int("1__0") and int("_1") raise, "²".isdigit() is True but int fails
	for s, want := range map[string]int{"٣": 3, "1_0": 10, "+5": 5, " 7 ": 7, " 7\u000b": 7, "𝟓": 5, "١٢": 12} {
		if got, ok := pyIntStr(s); !ok || got != want {
			t.Errorf("pyIntStr(%q) = %d,%v want %d", s, got, ok, want)
		}
	}
	for _, s := range []string{"", "1__0", "_1", "1_", "²", "a", "-", "1.5"} {
		if _, ok := pyIntStr(s); ok {
			t.Errorf("pyIntStr(%q) must fail", s)
		}
	}
	// stat fields split on any Unicode whitespace (NBSP, \v): ppid = 4
	if got, ok := parsePpid("9 (a b) S 4\u000b0 0"); !ok || got != 4 {
		t.Errorf("parsePpid with NBSP/VT = %d,%v", got, ok)
	}
	// a pid dir named with Arabic-Indic digits is pid 3 (int() accepts it)
	root, _ := procEnv(t)
	writeProc(t, root, 1, 1, "5", "x")
	if err := os.RemoveAll(filepath.Join(root, "1")); err != nil {
		t.Fatal(err)
	}
	d := filepath.Join(root, "٣")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "cmdline"), []byte("claude\x00--resume\x00s\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	if m := scanClaude(root); len(m) != 1 || m[3].Argv[0] != "claude" || m[3].HasPpid {
		t.Errorf("scan = %+v", m)
	}
	if got := pyDecode([]byte("a\xe2\x82b\xffc")); got != "a�b�c" {
		t.Errorf("pyDecode truncated sequence = %q", got)
	}
}

func TestSnapshotOff(t *testing.T) {
	for argv, want := range map[string]bool{
		"claude --system-prompt-snapshot off": true,
		"claude --system-prompt-snapshot=off": true,
		"claude --system-prompt-snapshot on":  false,
		"claude --system-prompt-snapshot":     false,
		"claude off --system-prompt-snapshot": false,
	} {
		if got := snapshotOff(strings.Fields(argv)); got != want {
			t.Errorf("snapshotOff(%q) = %v", argv, got)
		}
	}
}

func FuzzProcParse(f *testing.F) {
	for _, s := range []string{"", "1 (a) S 4 0", "9 (a b) S 4\u000b0", "x)", ")", "claude\x00--resume\x00s\x00", "\xff\xe2\x82", "١٢", "1_0"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		_, _ = parsePpid(s)
		_, _ = statAfterComm(s)
		_, _ = pyIntStr(s)
		_ = pyDecode([]byte(s))
		argv := splitArgv([]byte(s))
		_ = isClaudeCmd(argv)
		_ = isCursorAgentArgv(argv)
		_ = snapshotOff(argv)
		_ = hasRoleFlags(argv, "coder")
		_ = argvWorkspace(argv)
		_ = hooksDocOK([]byte(s))
		_, _ = parsePyFloat(s)
		_ = pyRepr(s)
	})
}

func TestClaudeAuditSandboxedAgentEndToEnd(t *testing.T) {
	root, _, db := auditSetup(t)
	mustExec(t, db, "UPDATE agents SET spawn_args='{\"sandbox\": true}', pane_id=NULL")
	writeProc(t, root, 100, 1, "9", "claude", "--session-id", "sess-a", "--system-prompt-snapshot", "off")
	for _, pid := range []string{"100", "1"} {
		if err := os.MkdirAll(filepath.Join(root, pid, "ns"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("user:[1]", filepath.Join(root, pid, "ns", "user")); err != nil {
			t.Fatal(err)
		}
	}
	runUnit(t, db, runClaudeAudit)
	if n := scalar(t, db, "SELECT count(*) FROM events WHERE action='sandbox-escaped'"); n != "1" {
		t.Errorf("sandbox-escaped events = %s, want 1", n)
	}
	if n := scalar(t, db, "SELECT count(*) FROM throttle WHERE key='sandbox-escaped:100:9'"); n != "1" {
		t.Errorf("escape throttle row missing")
	}
}
