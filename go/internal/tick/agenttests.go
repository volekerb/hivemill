package tick

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"factory/go/internal/store"
)

// ReapAgentTests is reap_agent_tests: when agent `name` just ended (trigger "done": status exited; "stop": status
// stopped) signal its own local test runners per config orphan_reap_mode. "dry-run" (the default, and any unknown
// value) logs "agent-tests-would-reap" once per (pid, start time); "on" sends SIGTERM, SIGKILL after the grace period,
// and logs "agent-tests-reaped"; "off" scans nothing. A candidate is a same-user process whose FACTORY_AGENT is name,
// attributed to the agent's own task, a test runner, and not the caller, its ancestors, the serve MainPID or a pane
// tree. The identity (argv + start time) is re-read before each signal; the group goes too only when its leader passes
// the same checks. It never fails: every error ends in one "agent-tests-reap-failed" event.
func ReapAgentTests(db *sql.DB, name, trigger string) {
	r, err := newReapDB(db)
	if err != nil {
		// no event clock, so no event either; Python's now() would raise inside the same except: nothing to record
		fmt.Fprintf(os.Stderr, "factory: agent-tests-reap: %v\n", err)
		return
	}
	defer func() {
		if rec := recover(); rec != nil {
			agentTestsFailed(r, name, fmt.Errorf("panic: %v", rec))
		}
	}()
	if err := reapAgentTests(r, name, trigger); err != nil {
		agentTestsFailed(r, name, err)
	}
}

// agentTestsFailed is _agent_tests_failed: a recording failure must not break the caller either.
func agentTestsFailed(r *reapDB, name string, e error) {
	msg := e.Error()
	if msg == "" {
		msg = fmt.Sprintf("%T", e)
	}
	if err := r.event("agent", name, "agent-tests-reap-failed", msg); err != nil {
		fmt.Fprintf(os.Stderr, "factory: agent-tests-reap-failed event not recorded: %v\n", err)
	}
}

// orphanModes is ORPHAN_MODES; reapMode maps a config value to the mode, anything unknown to dry-run.
func reapMode(v string) string {
	switch v {
	case "on", "off", "dry-run":
		return v
	}
	return "dry-run"
}

// orphanHQRoles is ORPHAN_HQ_ROLES.
var orphanHQRoles = map[string]bool{"boss": true, "director": true, "lead": true, "staff-engineer": true}

type endedRow struct {
	name, role, status, taskID string
	hasTask                    bool
	started, ended             sql.NullString
}

func reapAgentTests(r *reapDB, name, trigger string) error {
	v, err := r.Cfg("orphan_reap_mode")
	if err != nil {
		return err
	}
	mode := reapMode(v)
	if mode == "off" {
		return nil
	}
	var row endedRow
	var task sql.NullString
	err = r.DB.QueryRow("SELECT name,role,status,task_id,started,ended FROM agents WHERE name=?", name).
		Scan(&row.name, &row.role, &row.status, &task, &row.started, &row.ended)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read agent %s: %w", name, err)
	}
	row.taskID, row.hasTask = task.String, task.String != ""
	want, known := map[string]string{"done": "exited", "stop": "stopped"}[trigger]
	if !known || row.status != want || orphanHQRoles[row.role] || !row.hasTask {
		return nil
	}
	p := newProc(r.DB)
	skip, err := p.skipSet(mainPID(p.env))
	if err != nil {
		return err
	}
	grace := serveKillGraceS
	if g := p.env.Getenv("FACTORY_SERVE_KILL_GRACE_S"); g != "" {
		f, ok := pyFloatStr(g)
		if !ok {
			return fmt.Errorf("could not convert string to float: %s", pyReprStr(g))
		}
		grace = f
	}
	pids, err := p.pids()
	if err != nil {
		return err
	}
	for _, pid := range pids {
		j := p.testJudge(pid, skip, row)
		if j == nil {
			continue
		}
		if mode == "dry-run" {
			key := dedupeKey(j)
			var one int
			err := r.DB.QueryRow("SELECT 1 FROM events WHERE action='agent-tests-would-reap' AND substr(detail,1,?)=?",
				len([]rune(key)), key).Scan(&one)
			if err == nil {
				continue
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("read events: %w", err)
			}
			if err := r.event("agent", name, "agent-tests-would-reap", payload(j, "none", trigger)); err != nil {
				return err
			}
			continue
		}
		targets, group := []target{{pid, j.ident}}, 0
		if _, pg, _, ok := p.stat(pid); ok && pg > 1 && pg != reapGetpgrp() {
			var lead *judged
			if pg == pid {
				lead = j
			} else {
				lead = p.testJudge(pg, skip, row)
			}
			if lead != nil {
				group = pg
				if pg != pid {
					targets = append(targets, target{pg, lead.ident})
				}
			}
		}
		if !p.signal(syscall.SIGTERM, targets, group) {
			continue
		}
		how := p.termKill(pid, j.ident, targets, group, grace)
		if err := r.event("agent", name, "agent-tests-reaped", payload(j, how, trigger)); err != nil {
			return err
		}
	}
	return nil
}

// judged is the facts about a test runner of the ended agent (the dict _test_judge returns).
type judged struct {
	pid                        int
	agent, task, cwd, worktree string
	argv                       []string
	ident                      ident
	by                         string
}

type target struct {
	pid   int
	ident ident
}

// isTestRunner is _is_test_runner: only a local test run (python3 ci/affected.py, -m unittest, -m pytest,
// `factory test`, pytest / py.test).
func isTestRunner(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	b := pyBasename(argv[0])
	if b == "pytest" || b == "py.test" {
		return true
	}
	if !pyPythonRE.MatchString(b) || len(argv) < 2 {
		return false
	}
	second := ""
	if len(argv) > 2 {
		second = argv[2]
	}
	return strings.HasSuffix(argv[1], "ci/affected.py") ||
		(argv[1] == "-m" && (second == "unittest" || second == "pytest") && len(argv) > 2) ||
		((pyBasename(argv[1]) == "factory" || pyBasename(argv[1]) == "factory_cli.py") && len(argv) > 2 && argv[2] == "test")
}

// ownEndedClaude is _own_ended_claude: claude pid is the ended agent row's own, still exiting: its FACTORY_AGENT is the
// agent's name and it started inside the agent's started..ended window. Anything unreadable is false.
func (p *proc) ownEndedClaude(pid int, row endedRow) bool {
	env, ok := p.environ(pid)
	if !ok {
		env = map[string]string{}
	}
	if env["FACTORY_AGENT"] != row.name || row.started.String == "" || row.ended.String == "" {
		return false
	}
	raw, err := os.ReadFile(p.root + "/stat")
	if err != nil {
		return false
	}
	btime, found := 0, false
	for _, line := range strings.Split(decodeReplace(raw), "\n") {
		if strings.HasPrefix(line, "btime ") {
			f := strings.FieldsFunc(line, store.PyIsSpace)
			if len(f) < 2 {
				return false // IndexError
			}
			n, perr := pyIntStr(f[1])
			if perr != nil {
				return false
			}
			btime, found = n, true
			break
		}
	}
	if !found {
		return false
	}
	_, _, start, ok := p.stat(pid)
	if !ok {
		return false
	}
	began := float64(btime) + float64(start)/clkTck
	lo, ok1 := pyFromISO(row.started.String, processZone())
	hi, ok2 := pyFromISO(row.ended.String, processZone())
	if !ok1 || !ok2 {
		return false
	}
	return epochSeconds(lo)-5 <= began && began <= epochSeconds(hi)+5
}

// testJudge is _test_judge: the facts about pid when it is a test runner of the ended agent row's own task, else nil.
func (p *proc) testJudge(pid int, skip map[int]bool, row endedRow) *judged {
	if skip[pid] {
		return nil
	}
	fi, err := os.Stat(p.root + "/" + strconv.Itoa(pid))
	if err != nil {
		return nil
	}
	if sys, ok := fi.Sys().(*syscall.Stat_t); !ok || int(sys.Uid) != reapGetuid() {
		return nil
	}
	link, err := os.Readlink(p.path(pid, "cwd"))
	if err != nil {
		return nil
	}
	cwd := realpath(link)
	env, okEnv := p.environ(pid)
	id, okID := p.ident(pid)
	if !okEnv || !okID || env["FACTORY_AGENT"] != row.name || !isTestRunner(id.argv) {
		return nil
	}
	goal, task, by, ok := p.orphanTask(cwd, id.argv)
	if !ok || task != row.taskID {
		return nil
	}
	cur, have := p.parent(pid)
	for i := 0; i < procChainMax; i++ { // a live pane's process tree is never ours
		if !have || cur <= 1 {
			break
		}
		if skip[cur] {
			return nil
		}
		if a := p.argv(cur); a != nil {
			if b := pyBasename(a[0]); b == "claude" || b == "herdr" {
				if b != "claude" || !p.ownEndedClaude(cur, row) {
					return nil
				}
			}
		}
		cur, have = p.parent(cur)
	}
	return &judged{pid: pid, agent: row.name, task: task, argv: id.argv, ident: id, cwd: cwd,
		worktree: p.worktrees() + "/" + goal + "-" + task, by: by}
}

// signal is _orphan_signal: send sig (to the group when group != 0) only if every target still has its scanned
// identity. Gone or not ours to signal is false.
func (p *proc) signal(sig syscall.Signal, targets []target, group int) bool {
	reapPreSignal()
	for _, t := range targets {
		if cur, ok := p.ident(t.pid); !ok || !cur.equal(t.ident) {
			return false
		}
	}
	to := targets[0].pid
	if group != 0 {
		to = -group
	}
	return reapKill(to, sig) == nil
}

// termKill is _orphan_term_kill: SIGTERM was sent; wait up to grace seconds for pid to go, then SIGKILL it if it still
// has its scanned identity. "KILL" when the SIGKILL was sent, else "TERM".
func (p *proc) termKill(pid int, id ident, targets []target, group int, grace float64) string {
	same := func() bool { cur, ok := p.ident(pid); return ok && cur.equal(id) }
	deadline := reapNow().Add(time.Duration(grace * float64(time.Second)))
	for same() && reapNow().Before(deadline) {
		reapSleep(reapTermWait)
	}
	if same() && p.signal(syscall.SIGKILL, targets, group) {
		return "KILL"
	}
	return "TERM"
}

// payload is _orphan_payload (plus the trigger): exactly these keys, in this order.
func payload(j *judged, how, trigger string) store.Ordered {
	sum := sha256.Sum256([]byte(strings.Join(j.argv, "\x00")))
	return store.Ordered{
		{K: "pid", V: j.pid}, {K: "start_time", V: j.ident.start}, {K: "argv0", V: j.argv[0]},
		{K: "cmd_sha", V: hex.EncodeToString(sum[:])[:12]}, {K: "cwd", V: j.cwd}, {K: "worktree", V: j.worktree},
		{K: "task", V: j.task}, {K: "agent", V: j.agent}, {K: "attributed_by", V: j.by}, {K: "signal", V: how},
		{K: "trigger", V: trigger},
	}
}

// dedupeKey is json.dumps({"pid": pid, "start_time": st})[:-1] + ",": the detail prefix of an earlier event.
func dedupeKey(j *judged) string {
	return `{"pid": ` + strconv.Itoa(j.pid) + `, "start_time": ` + strconv.Itoa(j.ident.start) + ","
}

// runCapture runs a program (no shell) with a timeout and returns its stdout; ok=false when it cannot start or times
// out (Python's OSError / TimeoutExpired). A non-zero exit still yields the stdout, as subprocess.run does.
func runCapture(d time.Duration, exe string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.WaitDelay = time.Second
	var out strings.Builder
	cmd.Stdout = &out
	err := cmd.Run()
	var ee *exec.ExitError
	if ctx.Err() != nil || (err != nil && !errors.As(err, &ee)) {
		return "", false
	}
	return out.String(), true
}
