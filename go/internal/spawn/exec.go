package spawn

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Exec part of spawn (G-048 P1h): runs a SpawnPlan that passed the gate (spawn() / _spawn_start() in bin/factory_cli.py).
// Every statement keeps the order Python runs it in. What Go does not run yet is ErrNotPorted, returned BEFORE any side
// effect so the caller hands the whole spawn to Python: a sandboxed or gh-credential start, the cursor runtime, the
// evaluator, a new herdr pane (place/ensure_hq/ensure_goal_ws) and the worktree of a coder whose git-config guard is not
// injected.

// ErrNotPorted marks a spawn shape the Go exec does not run; nothing was changed when it is returned.
var ErrNotPorted = errors.New("spawn shape not ported to Go: hand the spawn to Python")

// ErrPlanNotRunnable is Exec on a plan that did not pass the gate (refused, queued, bounced or requeued).
var ErrPlanNotRunnable = errors.New("spawn plan is not startable: it was refused, queued, bounced or requeued")

// Python timeouts carried over: herdr agent start --timeout 90000 (ASYNC_START_TIMEOUT_MS when tick_async_start),
// FACTORY_KICKOFF_DELAY_S (2), FACTORY_SHELL_SETTLE_S (1), FACTORY_HC_SPAWN_SCRIPT_ROOT.
const (
	StartTimeoutMS      = "90000"
	AsyncStartTimeoutMS = "3001"
	pending1            = "agent_not_ready"
	pending2            = "timed out"
)

// hcSpawnDrop is HC_SPAWN_DROP: parent hc env that must never reach the agent child.
var hcSpawnDrop = []string{"CLAUDE_CODE_MESSAGING_TOKEN", "HC_SOCKET", "HERDR_PANE_ID", "SSH_AUTH_SOCK", "HERDR_SESSION",
	"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "CLAUDECODE"}

// Execer is the part of the DB handle Exec writes through (ctx.exec in the CLI, *sql.DB in tests).
type Execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// Host is the agent host client (the herdr package Client satisfies it; this package does not import it).
type Host interface {
	Herdr(check bool, args ...string) (any, error)
	StartError(args ...string) (any, string)
}

// PaneFinder finds an existing herdr pane (find_pane): ws, tab, ok.
type PaneFinder func(pane string) (ws, tab string, ok bool)

// RunSettings sets the hc run context of the next `agent start` (Python's _HC_RUN_* globals) and clears it again.
type RunSettings func(cwd string, env []KV, name string)

// ExecDeps is everything Exec touches outside the plan. Nil hooks mean "not ported": the shapes that need them return
// ErrNotPorted.
type ExecDeps struct {
	DB      Execer
	Host    Host
	Now     func() time.Time // the DB timestamp clock
	Epoch   func() float64   // time.time() for start_pending
	Sleep   func(time.Duration)
	HostExe string // "hc" or "herdr": the host the agent starts on (spawn_host)
	Home    string // FACTORY_HOME (the --add-dir and the child env are in the plan already)
	Actor   string // the acting agent: the actor column of every event (Python event() uses actor())
	Stdout  io.Writer
	// Claimed: the row was claimed 'starting' by spawn(); a failed start returns it to 'queued'.
	Claimed bool
	// Existing is the agents row of the name before this spawn (flagless, session_id), nil = none.
	Existing *ExistingRow
	// Pane: start in this existing herdr pane (spawn(pane=)); "" = place a new one (hc only here).
	Pane     string
	FindPane PaneFinder
	// SetRun installs the hc run context; it returns the function that clears it. nil = the client needs none (tests).
	SetRun     RunSettings
	AsyncStart bool // config tick_async_start == "1" (and the start is not sandboxed or evaluator: always true here)
	// Send inserts the kickoff message (send(con, name, ..., sender="factory")).
	Send func(to, subject, body, kind, goal, task string) error
	// Worktree resolves cwd for a plan with NeedWorktree (prepare_worktree). nil = ErrNotPorted.
	Worktree func() (string, error)
	// KickoffDelay is FACTORY_KICKOFF_DELAY_S (2 when unset); SettleDelay is FACTORY_SHELL_SETTLE_S (1).
	KickoffDelay, SettleDelay time.Duration
	// TaskGoal is tasks.goal_id of the plan task (task_auto_start moves THAT goal), "" = none.
	TaskGoal string
	// CheckStart runs right after the start call; an error aborts the spawn before any later row, event or output.
	CheckStart func() error
	// TrustClone is trust_task_clone for a coder, qa or reviewer started in its own task worktree; nil = not ported.
	TrustClone func(cwd string) error
	// WorktreesDir is WORKTREES: T-1657 trusts the final cwd of every unsandboxed claude launch under it. "" = none.
	WorktreesDir string
	// ScriptRoot is FACTORY_HC_SPAWN_SCRIPT_ROOT ("" = RunDir).
	ScriptRoot string
	// Live is stats["live"] of the gate decision (the spawn event's live count is Live+1).
	Live int
}

// ExistingRow is what _spawn_start reads of the previous agents row.
type ExistingRow struct {
	SessionID string
	Flagless  bool
}

// pyJSON writes a flat object like json.dumps: ", " and ": ", ensure_ascii.
func pyJSON(pairs ...any) string {
	var b strings.Builder
	b.WriteByte('{')
	for i := 0; i+1 < len(pairs); i += 2 {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(`"` + jsonEscape(pairs[i].(string)) + `": `)
		switch v := pairs[i+1].(type) {
		case nil:
			b.WriteString("null")
		case string:
			b.WriteString(`"` + jsonEscape(v) + `"`)
		case bool:
			b.WriteString(strconv.FormatBool(v))
		case int:
			b.WriteString(strconv.Itoa(v))
		default:
			b.WriteString(`"` + jsonEscape(fmt.Sprint(v)) + `"`)
		}
	}
	b.WriteByte('}')
	return b.String()
}

// underWorktrees is Python's realpath(cwd).startswith(realpath(WORKTREES)+os.sep).
func (d ExecDeps) underWorktrees(cwd string) bool {
	if d.WorktreesDir == "" || cwd == "" {
		return false
	}
	real := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return filepath.Clean(p)
	}
	return strings.HasPrefix(real(cwd), real(d.WorktreesDir)+string(os.PathSeparator))
}

func (d ExecDeps) stamp() string { return d.Now().Format("2006-01-02T15:04:05-07:00") }

func (d ExecDeps) event(entity, id, action string, detail any) error {
	var det any
	if detail != nil {
		det = detail
	}
	_, err := d.DB.Exec("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
		d.stamp(), d.Actor, entity, id, action, det)
	if err != nil {
		return fmt.Errorf("event %s %s: %w", entity, action, err)
	}
	return nil
}

// HCSpawnScriptPath is _hc_spawn_script_path: <root or run dir>/<name>.hc-spawn.sh; the dir is created.
func HCSpawnScriptPath(root, runDir, name string) (string, error) {
	base := root
	if base == "" {
		base = runDir
	}
	if err := os.MkdirAll(base, 0o777); err != nil {
		return "", fmt.Errorf("hc spawn script dir %s: %w", base, err)
	}
	return filepath.Join(base, name+".hc-spawn.sh"), nil
}

// HCAgentChildEnv is _hc_agent_child_env: the spawn env minus HC_SPAWN_DROP and a live HERDR_SOCKET_PATH.
// liveSocket reports whether a configured socket names the live default.
func HCAgentChildEnv(env []KV, liveSocket func(string) bool) []KV {
	var out []KV
	for _, kv := range env {
		if inList(hcSpawnDrop, kv.Key) {
			continue
		}
		if kv.Key == "HERDR_SOCKET_PATH" && liveSocket != nil && liveSocket(kv.Value) {
			continue
		}
		out = setKV(out, kv.Key, kv.Value) // a dict: a repeated key keeps its first position, last value
	}
	return out
}

// shellQuote is shlex.quote.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("@%+=:,./-_", r)) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// HCSpawnScriptBody is the text _hc_write_spawn_script writes: env -i, PATH LANG TERM first, the rest sorted.
func HCSpawnScriptBody(env []KV, program string, post []string, osEnv func(string) (string, bool), liveSocket func(string) bool) string {
	ex := HCAgentChildEnv(env, liveSocket)
	pop := func(k, def string) string {
		for i, kv := range ex {
			if kv.Key == k {
				ex = append(ex[:i:i], ex[i+1:]...)
				return kv.Value
			}
		}
		return def
	}
	pick := func(k, def string) string { // os.environ.get(k, def): a set but empty value is kept
		if v, ok := osEnv(k); ok {
			return v
		}
		return def
	}
	path := pop("PATH", pick("PATH", "/usr/bin:/bin"))
	lang := pop("LANG", pick("LANG", "C.UTF-8"))
	term := pop("TERM", pick("TERM", "dumb"))
	sort.SliceStable(ex, func(i, j int) bool { return ex[i].Key < ex[j].Key })
	argv := []string{"env", "-i", "PATH=" + path, "LANG=" + lang, "TERM=" + term}
	for _, kv := range ex {
		argv = append(argv, kv.Key+"="+kv.Value)
	}
	argv = append(argv, program)
	argv = append(argv, post...)
	q := make([]string, len(argv))
	for i, a := range argv {
		q[i] = shellQuote(a)
	}
	return "#!/bin/sh\nexec " + strings.Join(q, " ") + "\n"
}

// HCWriteSpawnScript is _hc_write_spawn_script: writes the wrapper (mode 0700) and returns its path.
func HCWriteSpawnScript(root, runDir, name string, env []KV, program string, post []string, osEnv func(string) (string, bool), liveSocket func(string) bool) (string, error) {
	p, err := HCSpawnScriptPath(root, runDir, name)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(p, []byte(HCSpawnScriptBody(env, program, post, osEnv, liveSocket)), 0o700); err != nil {
		return "", fmt.Errorf("write hc spawn script %s: %w", p, err)
	}
	if err := os.Chmod(p, 0o700); err != nil { // WriteFile keeps the mode of an existing file
		return "", fmt.Errorf("chmod hc spawn script %s: %w", p, err)
	}
	return p, nil
}

// HCRunArgvTail is _hc_run_argv_tail: `sh <script>` when the spawn env is set, else [program, post...].
func HCRunArgvTail(root, runDir, name string, env []KV, program string, post []string, osEnv func(string) (string, bool), liveSocket func(string) bool) ([]string, error) {
	if len(env) == 0 {
		return append([]string{program}, post...), nil
	}
	if name == "" {
		name = "hc-spawn"
	}
	p, err := HCWriteSpawnScript(root, runDir, name, env, program, post, osEnv, liveSocket)
	if err != nil {
		return nil, err
	}
	return []string{"sh", p}, nil
}

// writeAtomic is write_atomic: a per-process temp file next to the target, chmod 0644, rename; the temp file is
// removed when anything fails.
func writeAtomic(path, text string) (err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	tmp := filepath.Join(filepath.Dir(path), fmt.Sprintf(".%s.%d.tmp", filepath.Base(path), os.Getpid()))
	defer func() {
		if err != nil {
			_ = os.Remove(tmp) // best-effort: the write error is the result
		}
	}()
	if err := os.WriteFile(tmp, []byte(text), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Chmod(tmp, 0o644); err != nil { // WriteFile keeps the mode of a stale file
		return fmt.Errorf("chmod %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s: %w", tmp, err)
	}
	return nil
}

// sessionID is the --session-id value of a fresh spawn ("" for a resume).
func sessionID(args []string) string {
	for i, a := range args {
		if a == "--session-id" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// Exec runs plan: it refuses (ErrPlanNotRunnable, no side effect, no host call) unless the plan passed the gate, and
// returns ErrNotPorted for a shape Python must run. On success it prints the SPAWNED line and returns the name.
func Exec(plan SpawnPlan, d ExecDeps) (string, error) {
	st := plan.Start
	if plan.Refusal != nil || plan.Queue != nil || plan.Bounce != "" || plan.Requeue != "" || st == nil {
		return "", ErrPlanNotRunnable
	}
	// the shapes below are not ported: decided before any side effect
	switch {
	case st.Sandboxed, st.GhCred, st.EvaluatorSettings, st.Runtime != "claude", plan.Role == "evaluator":
		return "", ErrNotPorted
	case d.HostExe != "hc" && (d.Pane == "" || d.FindPane == nil):
		return "", ErrNotPorted // place() of a new herdr pane
	case st.NeedWorktree && d.Worktree == nil:
		return "", ErrNotPorted
	case d.TrustClone == nil && (inList([]string{"coder", "qa", "reviewer"}, plan.Role) && plan.Task != "" || !st.NeedWorktree && d.underWorktrees(st.Cwd)):
		return "", ErrNotPorted // trust_task_clone writes the claude trust entry (T-1657: any role, any cwd under WORKTREES)
	case d.DB == nil || d.Host == nil || d.Now == nil || d.Sleep == nil || d.Epoch == nil || d.Stdout == nil || (plan.Resume == "" && d.Send == nil):
		return "", fmt.Errorf("exec: missing DB, host or clock")
	}
	out, err := execStart(plan, d)
	if err != nil && d.Claimed {
		// a failed start returns the row to the queue (the tick retries); never stuck in 'starting'
		if _, rerr := d.DB.Exec("UPDATE agents SET status='queued', spawn_args=json_remove(spawn_args,'$.claimed_at') WHERE name=? AND status='starting'", plan.Name); rerr != nil {
			return "", fmt.Errorf("%w (requeue of %s failed too: %w)", err, plan.Name, rerr)
		}
	}
	return out, err
}

func execStart(plan SpawnPlan, d ExecDeps) (string, error) {
	st, name, role := plan.Start, plan.Name, plan.Role
	hc := d.HostExe == "hc"
	var ws, tab, pane string
	if hc {
		pane = name // place(): hc has no workspace or tab
	} else {
		// look the pane up first: an unknown pane hands over to Python before any side effect (M36655)
		w, t, ok := d.FindPane(d.Pane)
		if !ok {
			return "", ErrNotPorted
		}
		ws, tab, pane = w, t, d.Pane
	}
	// every launch re-renders the settings, before any worktree, pane or row exists
	if err := writeAtomic(st.SettingsPath, st.SettingsText); err != nil {
		return "", fmt.Errorf("render settings for %s: %w", role, err)
	}
	cwd := st.Cwd
	if st.NeedWorktree {
		wt, err := d.Worktree()
		if err != nil {
			return "", fmt.Errorf("worktree for %s: %w", name, err)
		}
		if wt == "" {
			wt = d.Home
		}
		cwd = wt
	}
	if d.TrustClone != nil && (inList([]string{"coder", "qa", "reviewer"}, role) && plan.Task != "" || d.underWorktrees(cwd)) {
		if err := d.TrustClone(cwd); err != nil {
			return "", fmt.Errorf("trust task clone %s: %w", cwd, err)
		}
	}
	env := st.Env
	if !hc {
		if _, err := d.Host.Herdr(true, "pane", "run", pane, exportCmd(env[:st.EnvBase])+" && cd "+shellQuote(cwd)+" && clear"); err != nil {
			return "", fmt.Errorf("export env in pane %s: %w", pane, err)
		}
		d.Sleep(d.SettleDelay)
	}
	if err := os.WriteFile(st.PromptPath, []byte(st.PromptText), 0o666); err != nil { // Python write_text: plain
		return "", fmt.Errorf("write prompt %s: %w", st.PromptPath, err)
	}
	sid := sessionID(st.ClaudeArgs)
	args := plan.ArgsJSON
	args = addRuntimeArgs(args, hc, d.HostExe)
	var sidv, wsv, tabv any
	if sid != "" {
		sidv = sid
	}
	if ws != "" {
		wsv = ws
	}
	if tab != "" {
		tabv = tab
	}
	if _, err := d.DB.Exec("INSERT INTO agents(name,role,goal_id,team_id,task_id,status,pane_id,workspace_id,tab_id,model,session_id,cwd,spawn_args,started) "+
		"VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(name) DO UPDATE SET status='live', pane_id=excluded.pane_id, "+
		"workspace_id=excluded.workspace_id, tab_id=excluded.tab_id, model=excluded.model, "+
		"session_id=coalesce(excluded.session_id, agents.session_id), cwd=excluded.cwd, "+
		"spawn_args=excluded.spawn_args, started=excluded.started, ended=NULL, summary=NULL, blocked_since=NULL, blocked_alerted=0, "+
		"stop_requested_at=NULL, stop_attempt_at=NULL, stop_failed_kind=NULL, start_pending=NULL, start_pending_info=NULL",
		name, role, nz(plan.Goal), nz(plan.Team), nz(plan.Task), "live", pane, wsv, tabv, st.Model, sidv, cwd, args, d.stamp()); err != nil {
		return "", fmt.Errorf("insert agent row %s: %w", name, err)
	}
	if role == "lead" && plan.Team != "" {
		if _, err := d.DB.Exec("UPDATE teams SET lead_agent=?, tab_id=?, parked_at=NULL WHERE id=?", name, tabv, plan.Team); err != nil {
			return "", fmt.Errorf("update team %s: %w", plan.Team, err)
		}
	}
	if plan.Task != "" && role == "coder" {
		if _, err := d.DB.Exec("UPDATE tasks SET assignee=?, updated=? WHERE id=?", name, d.stamp(), plan.Task); err != nil {
			return "", fmt.Errorf("update task %s: %w", plan.Task, err)
		}
	}
	if err := d.taskAutoStart(plan.Task, "spawn "+name, role, d.TaskGoal); err != nil {
		return "", err
	}
	startArgs := []string{"agent", "start", name, "--kind", "claude", "--pane", pane, "--timeout", StartTimeoutMS, "--"}
	if d.AsyncStart {
		startArgs[7] = AsyncStartTimeoutMS
	}
	startArgs = append(startArgs, st.ClaudeArgs...)
	clear := func() {}
	if d.SetRun != nil {
		d.SetRunClear(&clear, cwd, env, name)
	}
	var startErr string
	var r any
	if d.AsyncStart {
		r, startErr = d.Host.StartError(startArgs...)
	} else {
		var err error
		r, err = d.Host.Herdr(false, startArgs...)
		if err != nil {
			clear()
			return "", fmt.Errorf("start %s: %w", name, err)
		}
	}
	clear()
	if d.CheckStart != nil {
		if err := d.CheckStart(); err != nil {
			return "", err
		}
	}
	switch {
	case d.AsyncStart && startErr != "" && !pendingErr(startErr):
		if err := d.event("agent", name, "start-failed", pyJSON("pane", pane, "error", truncRunes(startErr, 300))); err != nil {
			return "", err
		}
	case d.AsyncStart:
		old := nilJSON(d.Existing != nil, func() string { return d.Existing.SessionID })
		kick := "null"
		if plan.Resume == "" {
			kick = `"` + jsonEscape(st.Kickoff) + `"`
		}
		info := fmt.Sprintf(`{"fresh": %s, "old_sid": %s, "new_sid": %s, "flagless": %s, "kick": %s}`,
			strconv.FormatBool(plan.Fresh), old, nilJSON(sid != "", func() string { return sid }),
			strconv.FormatBool(d.Existing != nil && d.Existing.Flagless), kick)
		if _, err := d.DB.Exec("UPDATE agents SET start_pending=?, start_pending_info=? WHERE name=?", d.Epoch(), info, name); err != nil {
			return "", fmt.Errorf("mark %s start pending: %w", name, err)
		}
	case r == nil:
		// agent_not_ready usually means a startup dialog; the tick alerts if it stays blocked
		if err := d.event("agent", name, "start-uncertain", pane); err != nil {
			return "", err
		}
	case plan.Fresh:
		old := nilJSON(d.Existing != nil, func() string { return d.Existing.SessionID })
		det := fmt.Sprintf(`{"old_sid": %s, "new_sid": %s}`, old, nilJSON(sid != "", func() string { return sid }))
		if err := d.event("agent", name, "relaunched-fresh", det); err != nil {
			return "", err
		}
	}
	if !d.AsyncStart && r != nil && d.Existing != nil && d.Existing.Flagless {
		if _, err := d.DB.Exec("UPDATE agents SET flagless=0, flagless_since=NULL WHERE name=?", name); err != nil {
			return "", fmt.Errorf("clear flagless of %s: %w", name, err)
		}
		if err := d.event("agent", name, "flagless-cleared", pyJSON("via", "spawn")); err != nil {
			return "", err
		}
	}
	if !hc {
		label := runesN(role+"·"+firstNonEmpty(plan.Task, plan.Team, plan.Goal, "hq"), 28)
		if _, err := d.Host.Herdr(false, "pane", "report-metadata", pane, "--source", "factory", "--display-agent", label); err != nil {
			return "", fmt.Errorf("pane metadata of %s: %w", pane, err)
		}
		if role == "boss" || role == "director" || role == "lead" {
			if _, err := d.Host.Herdr(false, "tab", "rename", tab, firstNonEmpty(plan.Team, role)); err != nil {
				return "", fmt.Errorf("tab rename %s: %w", tab, err)
			}
		}
	}
	if plan.Resume == "" {
		subject := strings.TrimSpace("kickoff: " + role + " " + firstNonEmpty(plan.Task, plan.Team, plan.Goal))
		if d.Send != nil {
			if err := d.Send(name, subject, st.Kickoff, "request", plan.Goal, plan.Task); err != nil {
				return "", fmt.Errorf("kickoff message to %s: %w", name, err)
			}
		}
		if !d.AsyncStart {
			d.Sleep(d.KickoffDelay)
			pr, err := d.Host.Herdr(false, "agent", "prompt", name, st.Kickoff)
			if err != nil {
				return "", fmt.Errorf("kickoff prompt to %s: %w", name, err)
			}
			if pr != nil {
				if _, err := d.DB.Exec("UPDATE agents SET last_nudge=? WHERE name=?", d.Epoch(), name); err != nil {
					return "", fmt.Errorf("set last_nudge of %s: %w", name, err)
				}
			}
		}
	}
	if err := d.event("agent", name, "spawn", pyJSON("pane", pane, "model", st.Model, "cwd", cwd, "live", d.Live+1)); err != nil {
		return "", err
	}
	if _, err := d.DB.Exec("UPDATE agents SET org=? WHERE name=?", plan.Org, name); err != nil {
		return "", fmt.Errorf("set org of %s: %w", name, err)
	}
	fmt.Fprintf(d.Stdout, "SPAWNED %s (%s, %s) in %s cwd=%s\n", name, role, st.Model, pane, cwd)
	return name, nil
}

// SetRunClear installs the hc run context and stores the function that clears it.
func (d ExecDeps) SetRunClear(clear *func(), cwd string, env []KV, name string) {
	d.SetRun(cwd, env, name)
	*clear = func() { d.SetRun("", nil, "") }
}

func nz(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nilJSON(ok bool, f func() string) string {
	if !ok {
		return "null"
	}
	v := f()
	if v == "" {
		return "null"
	}
	return `"` + jsonEscape(v) + `"`
}

func pendingErr(s string) bool {
	l := pyLower(s)
	return strings.Contains(l, pending1) || strings.Contains(l, pending2)
}

func truncRunes(s string, n int) string { return runesN(s, n) }

func runesN(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// exportCmd is export_cmd: the shell text that exports env and drops the command-hash cache.
func exportCmd(env []KV) string {
	parts := make([]string, len(env))
	for i, kv := range env {
		parts[i] = kv.Key + "=" + shellQuote(kv.Value)
	}
	return "export " + strings.Join(parts, " ") + " && hash -r"
}

// addRuntimeArgs is `args["host_backend"] = spawn_host` then args.setdefault("runtime", "claude"): both land at the end
// of the spawn_args object, after the keys spawn() set (host_backend is replaced in place when already there).
func addRuntimeArgs(argsJSON string, hc bool, hostExe string) string {
	host := "herdr"
	if hc {
		host = "hc"
	}
	_ = hostExe
	body := strings.TrimSuffix(argsJSON, "}")
	if i := strings.Index(body, `"host_backend": "`); i >= 0 {
		j := i + len(`"host_backend": "`)
		k := strings.Index(body[j:], `"`)
		body = body[:j] + host + body[j+k:]
		if !strings.Contains(body, `"runtime"`) {
			body += `, "runtime": "claude"`
		}
		return body + "}"
	}
	sep := ", "
	if body == "{" {
		sep = ""
	}
	body += sep + `"host_backend": "` + host + `"`
	if !strings.Contains(body, `"runtime"`) {
		body += `, "runtime": "claude"`
	}
	return body + "}"
}

func (d ExecDeps) taskAutoStart(task, trigger, role, goal string) error {
	if task == "" || !inList(workerRoles, role) {
		return nil
	}
	res, err := d.DB.Exec("UPDATE tasks SET status='in_progress', attempts=attempts+1, updated=? WHERE id=? AND status='todo'", d.stamp(), task)
	if err != nil {
		return fmt.Errorf("task auto start %s: %w", task, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("task auto start %s: %w", task, err)
	}
	if n == 0 {
		return nil
	}
	if err := d.event("task", task, "task-auto-start", trigger); err != nil {
		return err
	}
	if goal == "" {
		return nil
	}
	res, err = d.DB.Exec("UPDATE goals SET status='active', updated=? WHERE id=? AND status IN ('proposed','planning')", d.stamp(), goal)
	if err != nil {
		return fmt.Errorf("goal auto active %s: %w", goal, err)
	}
	if n, err = res.RowsAffected(); err != nil {
		return fmt.Errorf("goal auto active %s: %w", goal, err)
	} else if n > 0 {
		return d.event("goal", goal, "goal-auto-active", fmt.Sprintf("task %s in_progress (%s)", task, trigger))
	}
	return nil
}

// workerRoles is WORKER_ROLES.
var workerRoles = []string{"pm", "coder", "qa", "reviewer", "judge", "cleaner", "staff-engineer", "researcher", "scout", "retro", "evaluator"}

// ApplyQueue runs the queued branch of spawn(): the agents upsert (a from-queue upsert leaves a cancelled row alone),
// the org, the queued event and the QUEUED line. started=false: a from-queue claim lost the race, nothing else ran.
func ApplyQueue(plan SpawnPlan, fromQueue bool, d ExecDeps) (bool, error) {
	q := plan.Queue
	if q == nil {
		return false, ErrPlanNotRunnable
	}
	sql := "INSERT INTO agents(name,role,goal_id,team_id,task_id,status,spawn_args,queued_at) VALUES(?,?,?,?,?,?,?,?) " +
		"ON CONFLICT(name) DO UPDATE SET status='queued', pane_id=NULL, spawn_args=excluded.spawn_args, " +
		"queued_at=CASE WHEN agents.status IN ('queued','starting') AND agents.queued_at IS NOT NULL " +
		"THEN agents.queued_at ELSE excluded.queued_at END, " +
		"stop_requested_at=NULL, stop_attempt_at=NULL, stop_failed_kind=NULL"
	if fromQueue {
		sql += " WHERE agents.status != 'cancelled'"
	}
	res, err := d.DB.Exec(sql, q.Name, q.Role, nz(q.Goal), nz(q.Team), nz(q.Task), "queued", q.ArgsJSON, d.stamp())
	if err != nil {
		return false, fmt.Errorf("queue row %s: %w", q.Name, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return false, fmt.Errorf("queue row %s: %w", q.Name, err)
	} else if fromQueue && n == 0 {
		return false, nil
	}
	if _, err := d.DB.Exec("UPDATE agents SET org=? WHERE name=?", q.Org, q.Name); err != nil {
		return false, fmt.Errorf("set org of %s: %w", q.Name, err)
	}
	if err := d.event("agent", q.Name, "queued", q.Reason); err != nil {
		return false, err
	}
	fmt.Fprintln(d.Stdout, q.Message)
	return true, nil
}

// EmitEvents writes the events a plan collected (refusal events, resume-dropped, ...) in order.
func EmitEvents(plan SpawnPlan, d ExecDeps) error {
	for _, e := range plan.Events {
		if e.Action == "queued" && plan.Queue != nil {
			continue // ApplyQueue writes it in Python's position
		}
		if err := d.event(e.Entity, e.ID, e.Action, e.Detail); err != nil {
			return err
		}
	}
	return nil
}
