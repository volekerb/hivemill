package tick

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"factory/go/internal/herdr"
	"factory/go/internal/store"
)

const (
	startupGraceS      = 180 // STARTUP_GRACE_S (factory_cli.py)
	cursorStartLogMaxS = 600 // CURSOR_START_LOG_MAX_S
)

func init() { registerGo("reap_loop", reapLoopUnit) }

// reapLoopUnit is _tick_u_reap_loop (factory_cli.py:18698). Unit 20 (pending_nudges) writes st["t"]; by_name and
// by_pane are recomputed here (reapIndex) because they are driver state between the two units, not handoff keys.
func reapLoopUnit(u *UnitCtx) error {
	agents, _, err := stateAgents(u.State)
	if err != nil {
		return err
	}
	hcBack, err := stateBool(u.State, "hc_back")
	if err != nil {
		return err
	}
	byName, byPane, err := reapIndex(u.State)
	if err != nil {
		return err
	}
	t, err := stateFloat(u.State, "t")
	if err != nil {
		return err
	}
	hcByName, err := stateAgentMap(u.State, "hc_by_name")
	if err != nil {
		return err
	}
	hcListOK, err := stateBoolOr(u.State, "hc_list_ok", true)
	if err != nil {
		return err
	}
	tuiByName, err := stateAgentMap(u.State, "tui_by_name")
	if err != nil {
		return err
	}
	tuiByPane, err := stateAgentMap(u.State, "tui_by_pane")
	if err != nil {
		return err
	}
	tuiListOK, err := stateBoolOr(u.State, "tui_list_ok", true)
	if err != nil {
		return err
	}
	db, closeDB, err := pruneOpen(u)
	if err != nil {
		return err
	}
	defer closeDB()
	r, err := newReapDB(db)
	if err != nil {
		return err
	}
	c, st := newHerdrClient(r)
	loop := &reapLoop{r: r, c: c, u: u, agents: agents, hcBack: hcBack, byName: byName, byPane: byPane, t: t,
		hcByName: hcByName, hcListOK: hcListOK, tuiByName: tuiByName, tuiByPane: tuiByPane, tuiListOK: tuiListOK}
	if err := loop.run(); err != nil {
		return err
	}
	return st.Err()
}

type reapLoop struct {
	r                      *reapDB
	c                      *herdr.Client
	u                      *UnitCtx
	agents                 []herdr.Agent
	hcBack                 bool
	byName, byPane         map[string]herdr.Agent
	t                      float64
	hcByName               map[string]herdr.Agent
	hcListOK               bool
	tuiByName, tuiByPane   map[string]herdr.Agent
	tuiListOK              bool
	remote                 map[string]herdr.Agent
	remoteRead, remoteFail bool
	hq                     string
}

// reapRows is `SELECT * FROM agents WHERE status IN ('live','done')`, fetched up front like fetchall().
func (l *reapLoop) rows() ([]*dlgAgent, error) {
	rs, err := l.r.DB.Query("SELECT " + dlgAgentCols + " FROM agents WHERE status IN ('live','done')")
	if err != nil {
		return nil, fmt.Errorf("read agents: %w", err)
	}
	var out []*dlgAgent
	for rs.Next() {
		a := &dlgAgent{}
		var role sql.NullString
		d := a.dests()
		d[1] = &role
		if err := rs.Scan(d...); err != nil {
			rs.Close()
			return nil, fmt.Errorf("read agents: %w", err)
		}
		a.role = role.String
		out = append(out, a)
	}
	if err := rs.Err(); err != nil {
		rs.Close()
		return nil, fmt.Errorf("read agents: %w", err)
	}
	if err := rs.Close(); err != nil {
		return nil, fmt.Errorf("read agents: %w", err)
	}
	return out, nil
}

func (l *reapLoop) run() error {
	rows, err := l.rows()
	if err != nil {
		return err
	}
	for _, a := range rows {
		if err := l.one(a); err != nil {
			return err
		}
	}
	return nil
}

func (l *reapLoop) exec(q string, args ...any) error {
	if _, err := l.r.DB.Exec(q, args...); err != nil {
		return fmt.Errorf("%s: %w", q, err)
	}
	return nil
}

// paneHost is _pane_host: the host entry for the row, by name, else by pane id, never a pane another live row holds.
func (l *reapLoop) paneHost(byName, byPane map[string]herdr.Agent, a *dlgAgent) (h herdr.Agent, foreign bool, err error) {
	if h := byName[a.name]; len(h) > 0 {
		return h, false, nil
	}
	h = byPane[a.pane.String]
	if !a.pane.Valid {
		h = nil
	}
	other := ""
	if len(h) > 0 {
		other = h.Name()
	}
	if other != "" && other != a.name {
		var status string
		err := l.r.DB.QueryRow("SELECT status FROM agents WHERE name=?", other).Scan(&status)
		if err == nil {
			switch status {
			case "live", "starting", "queued", "stopped":
				return nil, false, nil
			}
			return nil, true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, false, fmt.Errorf("read agent %s: %w", other, err)
		}
	}
	return h, false, nil
}

func (l *reapLoop) one(a *dlgAgent) error {
	name := a.name
	rowExe := hostExe(l.c, name, a)
	sa, _ := store.SpawnArgsMap(a.spawnArgs.String)
	if sa == nil {
		sa = map[string]any{}
	}
	rm, _ := sa["machine"].(string)
	if m, ok := sa["machine"]; ok && !pyTruthy(m) {
		rm = ""
	}
	var h herdr.Agent
	var foreign bool
	var err error
	if rm != "" && l.hq == "" {
		raw, err := l.r.Cfg("machine_hq")
		if err != nil {
			return err
		}
		l.hq = strings.TrimSpace(raw)
		if l.hq == "" {
			l.hq = "hqhost"
		}
	}
	switch {
	case rm != "" && rm != l.hq:
		// G-057: started on a worker machine; hc lists it as <machine>/tN, never under its bare name here.
		if !l.remoteRead {
			l.remoteRead = true
			l.remote, l.remoteFail = hcRemoteTerminals(l.u)
		}
		if l.remoteFail {
			return nil // hc cannot be read: a later tick decides, never 'lost' on a failed read
		}
		h = l.remote[rm+"/"+name]
	case l.hcBack:
		if rowExe == "herdr" {
			if !l.tuiListOK {
				return nil
			}
			h, foreign, err = l.paneHost(l.tuiByName, l.tuiByPane, a)
		} else {
			h, foreign, err = l.paneHost(l.byName, l.byPane, a)
		}
	case rowExe == "hc":
		if !l.hcListOK {
			return nil
		}
		h = l.hcByName[name]
	default:
		l.tuiByName, l.tuiByPane = herdr.Index(l.agents)
		h, foreign, err = l.paneHost(l.tuiByName, l.tuiByPane, a)
	}
	if err != nil {
		return err
	}
	if foreign && a.status == "done" {
		foreign = false // the done path closes by host NAME only, so another row's pane is never touched
	}
	if foreign {
		seen, err := exists(l.r.DB, "SELECT 1 FROM events WHERE action='pane-foreign-skip' AND entity_id=? AND detail=?",
			name, pyStrOrNone(a.pane))
		if err != nil {
			return err
		}
		if !seen {
			return l.r.eventOwner("agent", name, "pane-foreign-skip", pyStrOrNone(a.pane))
		}
		return nil
	}
	if a.status == "done" {
		return l.reapDone(a, rowExe)
	}
	return l.live(a, h, sa)
}

// pyStrOrNone is str(x) for a nullable TEXT column.
func pyStrOrNone(s sql.NullString) string {
	if !s.Valid {
		return "None"
	}
	return s.String
}

func (l *reapLoop) markExited(a *dlgAgent, clearPane bool) error {
	q := "UPDATE agents SET status='exited', ended=coalesce(ended,?) WHERE name=?"
	if clearPane {
		q = "UPDATE agents SET status='exited', pane_id=NULL, ended=coalesce(ended,?) WHERE name=?"
	}
	if err := l.exec(q, l.r.Now(), a.name); err != nil {
		return err
	}
	ReapAgentTests(l.r.DB, a.name, "done")
	return l.r.eventOwner("agent", a.name, "reaped", nullable(a.pane))
}

func (l *reapLoop) reapDone(a *dlgAgent, rowExe string) error {
	name := a.name
	if rowExe == "hc" {
		src := l.hcByName
		if l.hcBack {
			src = l.byName
		}
		if _, ok := src[name]; ok {
			listed := make([]herdr.Agent, 0, len(src))
			keys := make([]string, 0, len(src))
			for k := range src {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				listed = append(listed, src[k])
			}
			if l.c.KillTerminal(name, listed) == "unknown" {
				return nil
			}
		}
		return l.markExited(a, true)
	}
	if a.pane.Valid && a.pane.String != "" {
		state, _, err := l.c.ClosePane(name, a.pane.String)
		if err != nil {
			return fmt.Errorf("close pane of %s: %w", name, err)
		}
		if state == "unknown" {
			return nil // the host cannot be asked: nothing closed, the row stays 'done' and is retried next tick
		}
	}
	return l.markExited(a, false)
}

func spawnRuntimeOf(sa map[string]any) string {
	if s, ok := sa["runtime"].(string); ok && s != "" {
		return s
	}
	return "claude"
}

func (l *reapLoop) live(a *dlgAgent, h herdr.Agent, sa map[string]any) error {
	name := a.name
	rt := spawnRuntimeOf(sa)
	if rt != "claude" && sa["launch"] == "subprocess" {
		return l.subprocessExit(a, sa, rt)
	}
	if len(h) == 0 && a.started.Valid && a.started.String != "" && stillStarting(a.started.String) {
		return nil // still starting: herdr hasn't registered it yet
	}
	if len(h) == 0 && a.stopRequested.Valid && a.stopRequested.String != "" {
		return nil // being stopped: step 1c decides, never 'lost'
	}
	if len(h) == 0 {
		if err := l.exec("UPDATE agents SET status='lost', ended=? WHERE name=?", l.r.Now(), name); err != nil {
			return err
		}
		if err := l.r.eventOwner("agent", name, "lost", nullable(a.pane)); err != nil {
			return err
		}
		if a.role == "director" {
			return nil
		}
		body := fmt.Sprintf("pane %s no longer hosts it. Task %s. Decide: respawn or reassign.%s",
			pyStrOrNone(a.pane), pyStrOrNone(a.task), l.cursorLostLogHint(a, sa, rt))
		return l.r.tickSend("director", "agent "+name+" vanished", body, "alert", optStr(a.goal), optStr(a.task), "")
	}
	if as, ok := h["agent_session"].(map[string]any); ok {
		if sess, ok := as["value"].(string); ok && sess != "" && (!a.session.Valid || sess != a.session.String) {
			if a.session.Valid && a.session.String != "" {
				if err := l.r.eventOwner("agent", name, "session-mismatch", store.Ordered{
					{K: "name", V: name}, {K: "stored", V: a.session.String}, {K: "herdr", V: sess}}); err != nil {
					return err
				}
			}
			if err := l.exec("UPDATE agents SET session_id=? WHERE name=?", sess, name); err != nil {
				return err
			}
		}
	}
	st, _ := h["agent_status"].(string)
	if st != "blocked" {
		if a.blockedSince.Valid && a.blockedSince.Float64 != 0 {
			return l.exec("UPDATE agents SET blocked_since=NULL, blocked_alerted=0 WHERE name=?", name)
		}
		return nil
	}
	fresh, err := readDlgAgent(l.r.DB, name)
	if err != nil {
		return err
	}
	if fresh != nil && autoClearDialog(l.r, l.c, l.u, fresh, l.t) {
		return nil // the tick just denied it and told the lead: no second ">10 min" alert from the stale row
	}
	if !a.blockedSince.Valid || a.blockedSince.Float64 == 0 {
		return l.exec("UPDATE agents SET blocked_since=? WHERE name=?", l.t, name)
	}
	raw, err := l.r.Cfg("blocked_alert_s")
	if err != nil {
		return err
	}
	limit, err := pyIntStr(raw)
	if err != nil {
		return fmt.Errorf("blocked_alert_s: %w", err)
	}
	if l.t-a.blockedSince.Float64 > float64(limit) && !(a.blockedAlerted.Valid && a.blockedAlerted.Int64 != 0) {
		return l.blockedAlert(a)
	}
	return nil
}

func (l *reapLoop) blockedAlert(a *dlgAgent) error {
	text, tail, err := paneText(l.c, a.name, paneReadSourceUnwrap, paneReadLinesAlert)
	if err != nil {
		return err
	}
	if text == "" {
		d, err := store.PyDumps(sortedOrdered(tail))
		if err != nil {
			return fmt.Errorf("dump pane tail of %s: %w", a.name, err)
		}
		text = d
	}
	to, hint, err := blockedAlertTarget(l.r, a)
	if err != nil {
		return err
	}
	rs := []rune(text)
	if len(rs) > blockedAlertTailRunes {
		rs = rs[len(rs)-blockedAlertTailRunes:]
	}
	if err := l.r.tickSend(to, a.name+" blocked >10 min", hint+string(rs), "alert", optStr(a.goal), optStr(a.task), ""); err != nil {
		return err
	}
	return l.exec("UPDATE agents SET blocked_alerted=1 WHERE name=?", a.name)
}

// sortedOrdered turns decoded JSON into store.Ordered values (keys sorted: herdr.Herdr decodes into Go maps, so the
// host's own key order is not known) so PyDumps can write it.
func sortedOrdered(v any) any {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		o := make(store.Ordered, 0, len(keys))
		for _, k := range keys {
			o = append(o, store.KV{K: k, V: sortedOrdered(x[k])})
		}
		return o
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = sortedOrdered(e)
		}
		return out
	}
	return v
}

func (l *reapLoop) subprocessExit(a *dlgAgent, sa map[string]any, rt string) error {
	alive := pidAlive(a.spawnArgs.String)
	if alive {
		return nil
	}
	// sa decodes every number as float64; Python keeps 4242 an int, so re-read pid and log with their number types.
	pidV, logV, err := pyPidLog(a.spawnArgs.String)
	if err != nil {
		return fmt.Errorf("pid of %s: %w", a.name, err)
	}
	if _, ok := sa["pid"]; !ok {
		pidV = nil
	}
	if err := l.exec("UPDATE agents SET status='done', summary=?, ended=? WHERE name=? AND status='live'",
		fmt.Sprintf("runtime=%s pid=%s exited", rt, pyStrJSON(pidV)), l.r.Now(), a.name); err != nil {
		return err
	}
	return l.r.eventOwner("agent", a.name, "runtime-exit", store.Ordered{{K: "pid", V: pidV}, {K: "runtime", V: rt}, {K: "log", V: logV}})
}

// pyPidLog returns spawn_args["pid"] and ["log"] with Python's types: an integer literal is an int (int64), a literal
// with a fraction or exponent a float64. An integer beyond int64 is an error: Python's os.kill raises OverflowError
// (not an OSError) there, so the unit fails rather than reaping. Objects come back key-sorted (Python keeps insertion
// order; accepted deviation, a pid object is not a real input).
func pyPidLog(raw string) (pid, log any, err error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, nil, fmt.Errorf("decode spawn_args: %w", err)
	}
	if pid, err = pyNumbers(m["pid"]); err != nil {
		return nil, nil, err
	}
	if log, err = pyNumbers(m["log"]); err != nil {
		return nil, nil, err
	}
	return pid, log, nil
}

func pyNumbers(v any) (any, error) {
	switch x := v.(type) {
	case json.Number:
		if strings.ContainsAny(x.String(), ".eE") {
			f, err := x.Float64()
			if err != nil {
				return nil, fmt.Errorf("float %s: %w", x, err)
			}
			return f, nil
		}
		n, err := x.Int64()
		if err != nil {
			return nil, fmt.Errorf("integer %s out of range (Python: OverflowError): %w", x, err)
		}
		return n, nil
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		o := make(store.Ordered, 0, len(keys))
		for _, k := range keys {
			e, err := pyNumbers(x[k])
			if err != nil {
				return nil, err
			}
			o = append(o, store.KV{K: k, V: e})
		}
		return o, nil
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			c, err := pyNumbers(e)
			if err != nil {
				return nil, err
			}
			out[i] = c
		}
		return out, nil
	}
	return v, nil
}

// pyStrJSON is Python str(v) for a decoded JSON value, as used by f"{pid}": None, True/False, int, float repr, the bare
// string; lists and dicts as repr (strings via pyRepr).
func pyStrJSON(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case bool:
		if x {
			return "True"
		}
		return "False"
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return store.PyFloatRepr(x)
	case string:
		return x
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = pyReprAny(e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case store.Ordered:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = pyRepr(e.K) + ": " + pyReprAny(e.V)
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return fmt.Sprint(v)
}

func pyReprAny(v any) string {
	if s, ok := v.(string); ok {
		return pyRepr(s)
	}
	return pyStrJSON(v)
}

// naiveZone marks a stored timestamp without an offset: Python subtracts it from an aware now() and gets a TypeError.
var naiveZone = time.FixedZone("naive", 0)

// startedAge is (now - fromisoformat(started)).total_seconds(); ok is false for a naive or unparsable value.
func startedAge(started string) (float64, bool) {
	ts, ok := pyFromISO(started, naiveZone)
	if !ok || ts.Location() == naiveZone {
		return 0, false
	}
	return epochSeconds(reapNow()) - epochSeconds(ts), true
}

// stillStarting is still_starting (factory_cli.py:2838).
func stillStarting(started string) bool {
	age, ok := startedAge(started)
	return ok && age < startupGraceS
}

// cursorLostLogHint is _cursor_lost_log_hint (factory_cli.py:13002).
func (l *reapLoop) cursorLostLogHint(a *dlgAgent, sa map[string]any, rt string) string {
	if rt != "cursor" || sa["launch"] != "herdr-kind" {
		return ""
	}
	age, ok := startedAge(a.started.String)
	if !ok || age < startupGraceS || age > cursorStartLogMaxS {
		return ""
	}
	p := filepath.Join(l.u.Home, "logs", a.name+"-cursor-start.log")
	if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	return " Start diagnostics: " + p + "."
}

// hcTimeout bounds one "hc ls" (python subprocess.run timeout=HERDR_TIMEOUT_S, factory_cli.py:1581); a var so tests can shorten it.
var hcTimeout = herdr.DefaultTimeout

// hcRemoteTerminals is _hc_remote_terminals (factory_cli.py:1581): {"<machine>/<name>": agent row} for the terminals
// hc lists on other machines. failed is true when hc cannot be read. Timeout: herdr.DefaultTimeout (HERDR_TIMEOUT_S).
func hcRemoteTerminals(u *UnitCtx) (out map[string]herdr.Agent, failed bool) {
	ctx, cancel := context.WithTimeout(context.Background(), hcTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "hc", "ls")
	cmd.Env = os.Environ()
	cmd.WaitDelay = time.Second
	var so bytes.Buffer
	cmd.Stdout = &so
	if err := cmd.Run(); err != nil {
		return nil, true
	}
	return parseHCRemote(so.String())
}

func parseHCRemote(stdout string) (map[string]herdr.Agent, bool) {
	var last string
	var any_ bool
	sc := bufio.NewScanner(strings.NewReader(strings.TrimSpace(stdout)))
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	for sc.Scan() {
		last, any_ = sc.Text(), true
	}
	if sc.Err() != nil {
		return nil, true
	}
	var payload any = map[string]any{}
	if any_ {
		if err := json.Unmarshal([]byte(last), &payload); err != nil {
			return nil, true
		}
	}
	m, ok := payload.(map[string]any)
	if !ok {
		return nil, true
	}
	terms, ok := m["terminals"].([]any)
	if !ok {
		return nil, true
	}
	out := map[string]herdr.Agent{}
	for _, t := range terms {
		tm, ok := t.(map[string]any)
		if !ok {
			continue
		}
		addr := ""
		for _, k := range []string{"address", "id", "terminal"} {
			if s, ok := tm[k].(string); ok && strings.Contains(s, "/") {
				addr = s
				break
			}
		}
		name, _ := tm["name"].(string)
		if addr == "" || name == "" {
			continue
		}
		key := name
		if !strings.Contains(name, "/") {
			key = strings.SplitN(addr, "/", 2)[0] + "/" + name
		}
		out[key] = hcTerminalRow(tm, key)
	}
	return out, false
}

// hcTerminalRow is _normalize_agent_list_row(_hc_terminal_to_agent_row({**t, "name": key})).
func hcTerminalRow(t map[string]any, key string) herdr.Agent {
	row := herdr.Agent{"name": key}
	if ag, ok := t["agent"]; ok && ag != nil {
		if ag == "claude-code" {
			ag = "claude"
		}
		row["agent"] = ag
	}
	word := hcWord(t)
	if _, has := t["status"]; has || word != "unknown" {
		row["agent_status"] = word
	}
	if s, _ := row["agent_status"].(string); s == "" {
		row["agent_status"] = "unknown"
	}
	return row
}

func hcWord(t map[string]any) string {
	switch st := t["status"].(type) {
	case map[string]any:
		if s, _ := st["state"].(string); s == "idle" || s == "working" || s == "done" || s == "blocked" || s == "error" {
			return s
		}
	case string:
		switch st {
		case "idle", "working", "done", "blocked", "error", "unknown":
			return st
		}
	}
	return "unknown"
}

// pidAlive mirrors Python's `isinstance(pid, int) and pid > 0` then
// os.kill(pid, 0) with `except OSError: alive = False`: any error, EPERM
// included, means dead, and a JSON float such as 5.0 or 1e2 is not an int.
// A JSON true is a Python int (1).
func pidAlive(rawSpawnArgs string) bool {
	dec := json.NewDecoder(strings.NewReader(rawSpawnArgs))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return false
	}
	var pid int64
	switch p := m["pid"].(type) {
	case json.Number:
		if strings.ContainsAny(p.String(), ".eE") {
			return false
		}
		n, err := p.Int64()
		if err != nil {
			return false
		}
		pid = n
	case bool:
		if p {
			pid = 1
		}
	default:
		return false
	}
	if pid <= 0 || pid > math.MaxInt32 {
		return false
	}
	return syscall.Kill(int(pid), 0) == nil
}
