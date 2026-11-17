package tick

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"factory/go/internal/ghx"
	"factory/go/internal/gitx"
	"factory/go/internal/herdr"
	"factory/go/internal/store"
)

// Unit idle_coders (_tick_u_idle_coders -> reap_idle_coders in bin/factory_cli.py), per config idle_coder_reap
// off|dry-run|on (unknown = dry-run): a live coder the host reports idle/done for more than idle_coder_reap_s (the
// first-seen-idle clock is kept in `throttle`, reset when it works), on an in_review/in_progress task with a PR, free of
// unread mail, clean tree, local HEAD == the PR head, nothing running in its pane, is stopped with a reason and a task
// note. dry-run logs one "idle-coder-would-stop" per idle episode and stops nothing. At most 3 stops per tick.

const (
	idleCoderMaxPerTick = 3 // IDLE_CODER_MAX_PER_TICK
	idleReapErrorMax    = 300
)

var (
	idleCoderAgentProcs = map[string]bool{"claude": true, "node": true}
	idleShells          = map[string]bool{"zsh": true, "bash": true, "sh": true, "dash": true, "fish": true}
)

// reapPRState is pr_state: `gh pr view <url> --json ...`, decoded. A seam so tests need no gh.
var reapPRState = func(url string) (map[string]any, error) {
	v, err := ghx.JSON(0, "pr", "view", url, "--json",
		"number,state,headRefOid,headRefName,baseRefName,mergeable,isDraft,statusCheckRollup,url")
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("gh pr view %s: not a JSON object", url)
	}
	return m, nil
}

func init() { registerGo("idle_coders", idleCodersUnit) }

func idleCodersUnit(u *UnitCtx) error {
	agents, _, err := stateAgents(u.State)
	if err != nil {
		return err
	}
	db, closeDB, err := pruneOpen(u)
	if err != nil {
		return err
	}
	defer closeDB()
	// The stop below is one BEGIN IMMEDIATE ... COMMIT spanning store calls: all statements must share one connection,
	// as they do on the factory DB (factorydb.Open pins one).
	db.SetMaxOpenConns(1)
	r, err := newReapDB(db)
	if err != nil {
		return err
	}
	h, st := newHerdrClient(r)
	rerr := reapIdleCoders(r, h, u, agents)
	if rerr == nil {
		rerr = st.Err()
	}
	if rerr != nil {
		// housekeeping must never stop the tick
		if err := r.eventTick("tick", "idle-coder-reap", "idle-coder-reap-failed", truncRunes(rerr.Error(), idleReapErrorMax)); err != nil {
			fmt.Fprintf(os.Stderr, "factory: idle-coder-reap-failed event not recorded: %v\n", err)
		}
	}
	return nil
}

type coderRow struct {
	name, goal, task sql.NullString
	pane             sql.NullString
	stopRequested    sql.NullString
}

func reapIdleCoders(r *reapDB, c herdrCaller, u *UnitCtx, agents []herdr.Agent) error {
	v, err := r.Cfg("idle_coder_reap")
	if err != nil {
		return err
	}
	mode := reapMode(v)
	rs, err := r.DB.Query("SELECT name,goal_id,task_id,pane_id,stop_requested_at FROM agents WHERE status='live' AND role='coder'")
	if err != nil {
		return fmt.Errorf("read coders: %w", err)
	}
	var rows []coderRow
	for rs.Next() {
		var x coderRow
		if err := rs.Scan(&x.name, &x.goal, &x.task, &x.pane, &x.stopRequested); err != nil {
			rs.Close()
			return fmt.Errorf("read coders: %w", err)
		}
		rows = append(rows, x)
	}
	if err := rs.Err(); err != nil {
		rs.Close()
		return fmt.Errorf("read coders: %w", err)
	}
	if err := rs.Close(); err != nil {
		return fmt.Errorf("read coders: %w", err)
	}
	if mode == "off" || len(agents) == 0 {
		if mode == "off" { // flag off: forget the clocks so a later switch-on starts from zero
			if _, err := r.DB.Exec("DELETE FROM throttle WHERE key LIKE 'idlereap:%' OR key LIKE 'idlereap-dry:%'"); err != nil {
				return fmt.Errorf("clear idle clocks: %w", err)
			}
		}
		return nil
	}
	limitRaw, err := r.Cfg("idle_coder_reap_s")
	if err != nil {
		return err
	}
	limit, perr := pyIntStr(limitRaw)
	if perr != nil {
		return nil // a bad threshold never stops anyone
	}
	clock, cerr := pruneNow()
	if cerr != nil {
		clock = epochSeconds(reapNow())
	}
	byName := map[string]herdr.Agent{}
	for _, x := range agents {
		if n := x.Name(); n != "" {
			byName[n] = x
		}
	}
	live := map[string]bool{}
	for _, x := range rows {
		live["idlereap:"+x.name.String], live["idlereap-dry:"+x.name.String] = true, true
	}
	krs, err := r.DB.Query("SELECT key FROM throttle WHERE key LIKE 'idlereap:%' OR key LIKE 'idlereap-dry:%'")
	if err != nil {
		return fmt.Errorf("read idle clocks: %w", err)
	}
	var stale []string
	for krs.Next() {
		var k string
		if err := krs.Scan(&k); err != nil {
			krs.Close()
			return fmt.Errorf("read idle clocks: %w", err)
		}
		if !live[k] { // an exited/lost agent's clock must not carry over to a reused name
			stale = append(stale, k)
		}
	}
	if err := krs.Err(); err != nil {
		krs.Close()
		return fmt.Errorf("read idle clocks: %w", err)
	}
	if err := krs.Close(); err != nil {
		return fmt.Errorf("read idle clocks: %w", err)
	}
	for _, k := range stale {
		if _, err := r.DB.Exec("DELETE FROM throttle WHERE key=?", k); err != nil {
			return fmt.Errorf("drop idle clock: %w", err)
		}
	}
	type dueRow struct {
		since float64
		row   coderRow
	}
	var due []dueRow
	for _, x := range rows {
		key := "idlereap:" + x.name.String
		h, ok := byName[x.name.String]
		if s, _ := h["agent_status"].(string); !ok || (s != "idle" && s != "done") {
			if _, err := r.DB.Exec("DELETE FROM throttle WHERE key IN (?,?)", key, "idlereap-dry:"+x.name.String); err != nil {
				return fmt.Errorf("reset idle clock: %w", err)
			}
			continue
		}
		var tsRaw string
		err := r.DB.QueryRow("SELECT ts FROM throttle WHERE key=?", key).Scan(&tsRaw)
		if errors.Is(err, sql.ErrNoRows) {
			if _, err := r.DB.Exec("INSERT INTO throttle(key,ts) VALUES(?,?)", key, store.PyFloatRepr(clock)); err != nil {
				return fmt.Errorf("start idle clock: %w", err)
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("read idle clock: %w", err)
		}
		since, ok := pyFloatStr(tsRaw)
		if !ok {
			return fmt.Errorf("could not convert string to float: %s", pyReprStr(tsRaw))
		}
		if clock-since > float64(limit) {
			due = append(due, dueRow{since, x})
		}
	}
	sort.SliceStable(due, func(i, j int) bool { return due[i].since < due[j].since })
	stopped := 0
	for _, d := range due {
		if mode == "on" && stopped >= idleCoderMaxPerTick {
			break
		}
		x := d.row
		blocker, err := idleCoderBlocker(r, c, x)
		if err != nil {
			return err
		}
		if blocker != "" {
			continue
		}
		name := x.name.String
		idleS := int(clock - d.since)
		detail := store.Ordered{{K: "agent", V: name}, {K: "task", V: nullable(x.task)}, {K: "idle_s", V: idleS},
			{K: "threshold_s", V: limit}}
		if mode == "dry-run" {
			dkey := "idlereap-dry:" + name
			was, err := exists(r.DB, "SELECT 1 FROM throttle WHERE key=?", dkey)
			if err != nil {
				return err
			}
			if !was {
				if _, err := r.DB.Exec("INSERT INTO throttle(key,ts) VALUES(?,?)", dkey, store.PyFloatRepr(clock)); err != nil {
					return fmt.Errorf("write dry-run clock: %w", err)
				}
				if err := r.EventAs(tickActor, "agent", name, "idle-coder-would-stop", detail, ""); err != nil {
					return err
				}
			}
			continue
		}
		reason := fmt.Sprintf("idle_coder_reap: idle %ds (> %ds), working tree clean, PR head pushed, no mail: "+
			"nothing left for you to do; the tick brings the task back when it needs a coder", idleS, limit)
		did, err := stopIdleCoder(r, x, reason, detail)
		if err != nil {
			return err
		}
		if !did {
			continue
		}
		stopped++
		if err := escWaitNudgeDeferred(r, c, u, name, "📬 STOP from tick: run factory inbox now and follow the STOP order.", "STOP order"); err != nil {
			return err
		}
	}
	return nil
}

// stopIdleCoder is the BEGIN IMMEDIATE block of reap_idle_coders: mark the stop, send the STOP order, record the
// events and the task note, all or nothing. false when the row was no longer live and unstopped.
func stopIdleCoder(r *reapDB, x coderRow, reason string, detail store.Ordered) (did bool, err error) {
	name := x.name.String
	if _, err := r.DB.Exec("BEGIN IMMEDIATE"); err != nil {
		return false, fmt.Errorf("begin stop of %s: %w", name, err)
	}
	committed := false
	defer func() {
		if !committed {
			if _, rerr := r.DB.Exec("ROLLBACK"); rerr != nil && err == nil {
				err = fmt.Errorf("rollback stop of %s: %w", name, rerr)
			}
		}
	}()
	res, err := r.DB.Exec("UPDATE agents SET stop_requested_at=?, stop_attempt_at=NULL, stop_failed_kind=NULL "+
		"WHERE name=? AND status='live' AND stop_requested_at IS NULL", r.Now(), name)
	if err != nil {
		return false, fmt.Errorf("mark stop of %s: %w", name, err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		if err != nil {
			return false, fmt.Errorf("mark stop of %s: %w", name, err)
		}
		return false, nil
	}
	body := reason + "\nStop work now. Push any work in progress to your branch (a WIP commit is fine), then run factory done '<stopped: pushed sha|nothing; state>'"
	msg := store.Msg{To: name, Subject: "STOP", Body: &body, Kind: "decision", Sender: tickActor}
	if x.goal.Valid {
		msg.Goal = strp(x.goal.String)
	}
	if x.task.Valid {
		msg.Task = strp(x.task.String)
	}
	if _, _, err := r.Send(msg); err != nil {
		return false, err
	}
	if err := r.eventTick("agent", name, "stop-requested", store.Ordered{{K: "by", V: "tick"}, {K: "reason", V: reason}}); err != nil {
		return false, err
	}
	if err := r.eventTick("agent", name, "idle-coder-stopped", detail); err != nil {
		return false, err
	}
	if err := r.eventTick("task", x.task.String, "note", fmt.Sprintf("tick stopped idle coder %s: %s", name, reason)); err != nil {
		return false, err
	}
	if _, err := r.DB.Exec("COMMIT"); err != nil {
		return false, fmt.Errorf("commit stop of %s: %w", name, err)
	}
	committed = true
	return true, nil
}

// escWaitNudgeDeferred is esc_wait_nudge(defer=True): send Esc, record a pending nudge and return at once; a later
// tick (finish_pending_nudges) sends the nudge. It prints what Python prints.
func escWaitNudgeDeferred(r *reapDB, c herdrCaller, u *UnitCtx, name, nudge, what string) error {
	out := func(s string) {
		if u != nil && u.Stdout != nil {
			u.Stdout(s + "\n")
		}
	}
	isLive := func() (bool, error) {
		var status string
		err := r.DB.QueryRow("SELECT status FROM agents WHERE name=?", name).Scan(&status)
		if err == nil && status == "live" {
			return true, nil
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, fmt.Errorf("read agent %s: %w", name, err)
		}
		out(name + " is no longer live; remaining steps skipped")
		return false, nil
	}
	ok, err := isLive()
	if err != nil {
		return err
	}
	if ok {
		if _, err := c.Herdr(false, "agent", "send-keys", name, "esc"); err != nil {
			return err
		}
		out("sent Esc to " + name)
	}
	ok, err = isLive()
	if err != nil || !ok {
		return err
	}
	var pane, started sql.NullString
	if err := r.DB.QueryRow("SELECT pane_id, started FROM agents WHERE name=?", name).Scan(&pane, &started); err != nil {
		return fmt.Errorf("read agent %s: %w", name, err)
	}
	return r.eventTick("agent", name, "nudge-pending", store.Ordered{{K: "nudge", V: nudge}, {K: "what", V: what},
		{K: "pane_id", V: nullable(pane)}, {K: "started", V: nullable(started)}})
}

// idleCoderBlocker is idle_coder_blocker: why the live coder row must not be stopped now, "" when it may be.
func idleCoderBlocker(r *reapDB, c herdrCaller, x coderRow) (string, error) {
	if x.stopRequested.Valid && x.stopRequested.String != "" {
		return "already stopping", nil
	}
	var status, prURL, worktree sql.NullString
	err := r.DB.QueryRow("SELECT status, pr_url, worktree FROM tasks WHERE id=?", x.task.String).Scan(&status, &prURL, &worktree)
	hasTask := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("read task %s: %w", x.task.String, err)
	}
	if !hasTask || (status.String != "in_review" && status.String != "in_progress") || prURL.String == "" {
		return "no PR on an in_review/in_progress task", nil
	}
	unread, err := exists(r.DB, "SELECT 1 FROM messages WHERE recipient=? AND read_at IS NULL", x.name.String)
	if err != nil {
		return "", err
	}
	if unread {
		return "unread mail", nil
	}
	wt := worktree.String
	if fi, serr := os.Stat(wt); wt == "" || serr != nil || !fi.IsDir() {
		return "worktree unreadable", nil
	}
	opts := gitOptions()
	st, err := gitx.Run(wt, opts, "status", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("git status in %s: %w", wt, err)
	}
	if st.ExitCode != 0 || strings.TrimRight(st.Stdout, "\n") != "" {
		return "dirty or unreadable working tree", nil
	}
	hd, err := gitx.Run(wt, opts, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git rev-parse in %s: %w", wt, err)
	}
	head := strings.TrimRight(hd.Stdout, "\n")
	if hd.ExitCode != 0 || head == "" {
		return "HEAD unreadable", nil
	}
	if why := prPushedHeadWhy(prURL.String, head); why != "" {
		return why, nil
	}
	if busy, err := paneBusy(c, x.pane.String); err != nil {
		return "", err
	} else if busy {
		return "a process runs in the pane", nil
	}
	busy, err := agentShellsBusy(r, x.name.String)
	if err != nil {
		return "", err
	}
	if busy {
		return "a process runs in the pane", nil
	}
	return "", nil
}

// gitOptions is what git_run reads from the process: the live homes and HOME.
func gitOptions() gitx.Options {
	env := pruneIdentity()
	var homes []string
	if _, set, err := env.DeriveLiveHomes(); err == nil {
		for h := range set {
			homes = append(homes, h)
		}
		sort.Strings(homes)
	}
	return gitx.Options{Home: env.Home(), LiveHomes: homes}
}

// prPushedHeadWhy is pr_pushed_head_ok: "" when the PR is open and its head is head, else why not.
func prPushedHeadWhy(prURL, head string) string {
	pr, err := reapPRState(prURL)
	if err != nil {
		return "cannot read PR: " + err.Error()
	}
	if s, _ := pr["state"].(string); s != "OPEN" {
		return fmt.Sprintf("PR is %v, not OPEN", pr["state"])
	}
	if oid, _ := pr["headRefOid"].(string); oid != head {
		return "PR head != local HEAD: push first"
	}
	return ""
}

// paneBusy is _pane_busy: true when the pane's foreground has a process other than the agent; unreadable is true.
func paneBusy(c herdrCaller, pane string) (bool, error) {
	var info any
	if pane != "" {
		var err error
		if info, err = c.Herdr(false, "pane", "process-info", "--pane", pane); err != nil {
			return false, err
		}
	}
	im, _ := info.(map[string]any)
	pi, _ := im["process_info"].(map[string]any)
	fg, ok := pi["foreground_processes"].([]any)
	if !ok {
		return true, nil
	}
	for _, x := range fg {
		var n any
		var argv []any
		if m, isMap := x.(map[string]any); isMap {
			n = m["name"]
			argv, _ = m["argv"].([]any)
		} else {
			n = pyStrOf(x)
		}
		if !pyTruthy(n) {
			n = ""
			if len(argv) > 0 {
				n = argv[0]
			}
		}
		parts := strings.FieldsFunc(pyStrOf(n), store.PyIsSpace)
		first := ""
		if len(parts) > 0 {
			first = parts[0]
		}
		if !idleCoderAgentProcs[pyBasename(first)] {
			return true, nil
		}
	}
	return false, nil
}

// pyStr is str(x) for a decoded JSON value; only strings can name the agent, anything else just must not equal it.
func pyStrOf(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return "None"
	}
	return fmt.Sprint(v)
}

// agentShellsBusy is _agent_shells_busy: true when any shell runs under agent name's claude process (Bash-tool shells
// sit in their own session, so the pane foreground misses them). No claude process or /proc unreadable is true.
func agentShellsBusy(r *reapDB, name string) (bool, error) {
	p := newProc(r.DB)
	pids, err := p.pids()
	if err != nil {
		return true, nil // /proc unreadable: fail safe
	}
	kids := map[int][]int{}
	var claude []int
	for _, pid := range pids {
		if pp, ok := p.parent(pid); ok {
			kids[pp] = append(kids[pp], pid)
		}
		if av := p.argv(pid); av != nil && pyBasename(av[0]) == "claude" {
			if env, _ := p.environ(pid); env["FACTORY_AGENT"] == name {
				claude = append(claude, pid)
			}
		}
	}
	if len(claude) == 0 {
		return true, nil
	}
	todo, seen := append([]int(nil), claude...), map[int]bool{}
	for len(todo) > 0 {
		cur := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		for _, k := range kids[cur] {
			if seen[k] {
				continue
			}
			seen[k] = true
			todo = append(todo, k)
			if av := p.argv(k); av != nil && idleShells[strings.TrimLeft(pyBasename(av[0]), "-")] {
				return true, nil
			}
		}
	}
	return false, nil
}
