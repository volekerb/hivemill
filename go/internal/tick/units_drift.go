package tick

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"factory/go/internal/ghx"
	"factory/go/internal/store"
)

// Unit 24 drift (tick_drift, drift_items and drift_pr_states in bin/factory_cli.py; docs/lifecycle.md). At most hourly,
// one message per goal that has drift items (D1-D4) to its live (else parked) lead of the goal's org, else the org's
// director. An identical item set is not re-sent for 24 h. Nothing here stops the tick: a failure becomes one
// drift-failed event and the unit returns nil, as Python's catch-all does.

// The constants of Python (factory_cli.py:9339-9345), same values.
const (
	driftThrottleS   = 3600      // DRIFT_THROTTLE_S: the tick looks for drift at most once an hour ("drift" throttle key)
	driftDedupeS     = 24 * 3600 // DRIFT_DEDUPE_S: an identical item set for a goal is not re-sent inside this window
	driftIdleS       = 2 * 3600  // DRIFT_IDLE_S: D3, no agent and no activity for this long
	driftBlockedS    = 24 * 3600 // DRIFT_BLOCKED_S: D4, blocked for this long
	driftGhBudgetS   = 60        // DRIFT_GH_BUDGET_S: all `gh pr view` calls of one run together (env FACTORY_DRIFT_GH_BUDGET_S)
	driftGhCallS     = 30        // DRIFT_GH_CALL_S: one call, never longer than what is left of the budget
	driftUncheckedMx = 80        // Python str(e)[:80]
)

// driftGoalOver is DRIFT_GOAL_OVER.
var driftGoalOver = []string{"done", "closed", "cancelled"}

// driftNow is the real clock (Python time.time() and datetime.now()); driftMono is time.monotonic(). Tests replace them.
var (
	driftNow  = time.Now
	driftMono = time.Now
	// driftGh is `gh pr view <url> --json state` with a timeout; tests replace it only where the real gh stub is not
	// enough. The default is ghx.Run.
	driftGh = func(timeout time.Duration, args ...string) (ghx.Result, error) {
		return ghx.Run(nil, "", timeout, args...)
	}
)

func init() { registerGo("drift", driftUnit) }

// driftUnit is _tick_u_drift: tick_drift after the reap (a just-reaped agent must not make D3 see a task with no agent).
func driftUnit(u *UnitCtx) error {
	r, err := newReapDB(u.DB)
	if err != nil {
		// no event clock, no event: the same refusal Python raises in now() before any write; the tick ends
		return fmt.Errorf("drift: %w", err)
	}
	tickDrift(r)
	return nil
}

// tickDrift never returns an error: a failure is recorded as the drift-failed event of Python.
func tickDrift(r *reapDB) {
	if err := driftRun(r); err != nil {
		// Python swallows a failing event() here too: the tick goes on either way.
		_ = r.eventOwner("tick", "drift", "drift-failed", err.Error())
	}
}

// pyG6 is Python's format(x, "g"): 6 significant digits, no trailing zeros.
func pyG6(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	}
	return strconv.FormatFloat(f, 'g', 6, 64)
}

// driftBudget is the run's gh budget: the env override, else 60. An invalid override falls back to the default.
func driftBudget() float64 {
	raw := os.Getenv("FACTORY_DRIFT_GH_BUDGET_S")
	if raw == "" {
		return driftGhBudgetS
	}
	if f, ok := pyFloatStr(raw); ok {
		return f
	}
	return driftGhBudgetS
}

// pyMin30 is min(DRIFT_GH_CALL_S, left): Python's min keeps the first argument unless the second is smaller (NaN: 30).
func pyMinCall(left float64) float64 {
	if left < driftGhCallS {
		return left
	}
	return driftGhCallS
}

func driftRun(r *reapDB) error {
	now := driftNow()
	t := epochSeconds(now)
	var last any
	switch err := r.DB.QueryRow("SELECT ts FROM throttle WHERE key='drift'").Scan(&last); {
	case errors.Is(err, sql.ErrNoRows):
		last = nil
	case err != nil:
		return fmt.Errorf("read throttle drift: %w", err)
	}
	if last != nil {
		lf, err := driftFloat(last)
		if err != nil {
			return err
		}
		if t-lf < driftThrottleS {
			return nil
		}
	}
	if _, err := r.DB.Exec("INSERT INTO throttle(key,ts) VALUES('drift',?) ON CONFLICT(key) DO UPDATE SET ts=excluded.ts",
		store.PyFloatRepr(t)); err != nil {
		return fmt.Errorf("write throttle drift: %w", err)
	}
	goals, err := driftGoals(r.DB)
	if err != nil {
		return err
	}
	states, unchecked, err := driftCheckPRs(r.DB)
	if err != nil {
		return err
	}
	blob, err := store.PyDumps(sortedStates(states))
	if err != nil {
		return err
	}
	cur, err := driftSavedStates(r.Store)
	if err != nil {
		return err
	}
	if blob != cur {
		if _, err := r.DB.Exec("INSERT INTO config(key,value) VALUES('drift_pr_states',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
			blob); err != nil {
			return fmt.Errorf("write config drift_pr_states: %w", err)
		}
	}
	for _, g := range goals {
		items, err := driftItems(r.DB, g, now, states)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			continue
		}
		ia := make([]any, len(items))
		for i, s := range items {
			ia[i] = s
		}
		key, err := store.PyDumps([]any{g.id, ia})
		if err != nil {
			return err
		}
		seen, err := driftRecentSeen(r.DB, g.id, key, now)
		if err != nil {
			return err
		}
		if seen {
			continue
		}
		gorg, err := r.GoalOrg(g.id)
		if err != nil {
			return err
		}
		if gorg == "" {
			// goal_org(...) or DEFAULT_ORG: OrgFor with nothing to go on is the default org (store keeps it private)
			if gorg, err = r.OrgFor("", "", "", ""); err != nil {
				return err
			}
		}
		team, err := driftLeadTeam(r.Store, g.id, gorg)
		if err != nil {
			return err
		}
		to := "director"
		if team != "" {
			to = "lead:" + team
		}
		lines := append(append([]string(nil), items...), uncheckedLines(unchecked[g.id])...)
		body := strings.Join(lines, "\n")
		if _, _, err := r.Send(store.Msg{To: to, Subject: fmt.Sprintf("drift %s: %d items", g.id, len(items)), Body: &body,
			Kind: "alert", Goal: strp(g.id), Sender: tickActor}); err != nil {
			return err
		}
		if err := r.eventOwner("goal", g.id, "drift", key); err != nil {
			return err
		}
	}
	return nil
}

func uncheckedLines(u []string) []string {
	var out []string
	for _, s := range u {
		out = append(out, "D2 unchecked "+s)
	}
	return out
}

// driftFloat is float(value) of a throttle.ts cell.
func driftFloat(v any) (float64, error) {
	switch x := v.(type) {
	case int64:
		return float64(x), nil
	case float64:
		return x, nil
	case string:
		return driftFloatStr(x)
	case []byte:
		return driftFloatStr(string(x))
	}
	return 0, fmt.Errorf("float() argument must be a string or a real number, not '%T'", v)
}

func driftFloatStr(s string) (float64, error) {
	if f, ok := pyFloatStr(s); ok {
		return f, nil
	}
	return 0, fmt.Errorf("could not convert string to float: '%s'", s)
}

type driftGoal struct{ id, status string }

func driftGoals(db *sql.DB) ([]driftGoal, error) {
	rows, err := db.Query("SELECT id, status FROM goals WHERE status NOT IN ('done','closed','cancelled') ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("read goals: %w", err)
	}
	defer rows.Close()
	var out []driftGoal
	for rows.Next() {
		var g driftGoal
		var st sql.NullString
		if err := rows.Scan(&g.id, &st); err != nil {
			return nil, fmt.Errorf("read goals: %w", err)
		}
		g.status = st.String
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read goals: %w", err)
	}
	return out, nil
}

// driftCheckPRs asks gh for the state of each in_review PR of a live goal, within the run's budget. states maps a PR url
// to its state; unchecked maps a goal to its "<task> (<why>)" texts, reported as "D2 unchecked".
func driftCheckPRs(db *sql.DB) (map[string]string, map[string][]string, error) {
	budget := driftBudget()
	started := driftMono()
	type row struct{ id, goal, url string }
	rows, err := db.Query("SELECT t.id, t.goal_id, t.pr_url FROM tasks t JOIN goals g ON g.id=t.goal_id WHERE t.status='in_review' " +
		"AND t.pr_url IS NOT NULL AND g.status NOT IN ('done','closed','cancelled') ORDER BY t.id")
	if err != nil {
		return nil, nil, fmt.Errorf("read in_review tasks: %w", err)
	}
	var list []row
	for rows.Next() {
		var x row
		var goal sql.NullString
		if err := rows.Scan(&x.id, &goal, &x.url); err != nil {
			rows.Close()
			return nil, nil, fmt.Errorf("read in_review tasks: %w", err)
		}
		x.goal = goal.String
		list = append(list, x)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, fmt.Errorf("read in_review tasks: %w", err)
	}
	rows.Close()
	states, unchecked := map[string]string{}, map[string][]string{}
	for _, x := range list {
		left := budget - driftMono().Sub(started).Seconds()
		if left <= 0 {
			unchecked[x.goal] = append(unchecked[x.goal], fmt.Sprintf("%s (gh time budget of %ss used up)", x.id, pyG6(budget)))
			continue
		}
		st, err := driftPRState(x.url, pyMinCall(left))
		if err != nil {
			var bad *driftBadJSON
			if errors.As(err, &bad) && bad.fatal {
				return nil, nil, err // Python: a TypeError is not caught per call; it reaches the catch-all
			}
			unchecked[x.goal] = append(unchecked[x.goal], fmt.Sprintf("%s (%s)", x.id, truncRunes(err.Error(), driftUncheckedMx)))
			continue
		}
		states[x.url] = st
	}
	return states, unchecked, nil
}

// driftBadJSON is a gh answer that is not usable. fatal: Python raises a TypeError the per-call handler does not catch.
type driftBadJSON struct {
	msg   string
	fatal bool
}

func (e *driftBadJSON) Error() string { return e.msg }

// driftPRState is `gh pr view <url> --json state` and json.loads(stdout)["state"] with Python's error texts.
func driftPRState(url string, callS float64) (string, error) {
	timeout := time.Duration(callS * float64(time.Second))
	if timeout <= 0 {
		timeout = time.Nanosecond
	}
	res, err := driftGh(timeout, "pr", "view", url, "--json", "state")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return "", errors.New("[Errno 2] No such file or directory: 'gh'") // Python FileNotFoundError text
		}
		return "", err // *gitx.TimeoutError carries Python's TimeoutExpired text
	}
	if res.ExitCode != 0 {
		if msg := store.PyStrip(res.Stderr); msg != "" {
			return "", errors.New(msg)
		}
		return "", fmt.Errorf("gh exit %d", res.ExitCode)
	}
	v, ok := pyParse(res.Stdout)
	if !ok {
		return "", &driftBadJSON{msg: jsonErrText(res.Stdout)}
	}
	if v.kind != pyObj {
		return "", &driftBadJSON{msg: "not an object", fatal: true}
	}
	s, found := v.obj["state"]
	if !found {
		return "", errors.New("'state'") // KeyError('state')
	}
	if s.kind != pyStr {
		return "", &driftBadJSON{msg: "gh state is not a string", fatal: true}
	}
	return s.s, nil
}

// jsonErrText is the text of Python's json.JSONDecodeError. Exact for empty or blank text, for text that does not start a
// value and for text with data after the value; any other malformed document gets the "Expecting value" form at the
// first non-blank character.
func jsonErrText(s string) string {
	pos := 0
	for pos < len(s) && strings.IndexByte(" \t\n\r", s[pos]) >= 0 {
		pos++
	}
	msg := "Expecting value"
	if pv := (&pyParser{s: s}); pos < len(s) {
		pv.i = pos
		if _, ok := pv.value(); ok {
			pv.ws()
			if pv.i < len(s) {
				msg, pos = "Extra data", pv.i
			}
		}
	}
	line := 1 + strings.Count(s[:pos], "\n")
	chars := func(n int) int { return len([]rune(s[:n])) } // Python counts characters, not bytes
	col := chars(pos) + 1
	if nl := strings.LastIndexByte(s[:pos], '\n'); nl >= 0 {
		col = chars(pos) - chars(nl)
	}
	return fmt.Sprintf("%s: line %d column %d (char %d)", msg, line, col, chars(pos))
}

func sortedStates(m map[string]string) store.Ordered {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	o := store.Ordered{}
	for _, k := range keys {
		o = append(o, store.KV{K: k, V: m[k]})
	}
	return o
}

// driftSavedStates is json.dumps(drift_pr_states(con), sort_keys=True): the states the last run recorded; a missing or
// unreadable row is {}.
func driftSavedStates(s *store.Store) (string, error) {
	raw, ok, err := s.CfgRow("drift_pr_states")
	if err != nil {
		return "", err
	}
	if !ok {
		return "{}", nil
	}
	if _, valid := pyParse(raw); !valid {
		return "{}", nil
	}
	return raw, nil // written by this unit (or Python) in the sorted form; a hand-edited row is rewritten
}

// driftRecentSeen reports whether the goal's drift event with this exact detail is younger than DRIFT_DEDUPE_S.
func driftRecentSeen(db *sql.DB, goal, key string, now time.Time) (bool, error) {
	rows, err := db.Query("SELECT ts, detail FROM events WHERE entity='goal' AND entity_id=? AND action='drift'", goal)
	if err != nil {
		return false, fmt.Errorf("read drift events of %s: %w", goal, err)
	}
	defer rows.Close()
	seen := false
	for rows.Next() {
		var ts, detail sql.NullString
		if err := rows.Scan(&ts, &detail); err != nil {
			return false, fmt.Errorf("read drift events of %s: %w", goal, err)
		}
		d, ok := driftTS(ts)
		if ok && now.Sub(d).Seconds() < driftDedupeS && detail.Valid && detail.String == key {
			seen = true
		}
	}
	return seen, rows.Err()
}

// driftLeadTeam is the first active team of the goal whose lead is live (live first) or parked and of the goal's org.
func driftLeadTeam(s *store.Store, goal, gorg string) (string, error) {
	rows, err := s.DB.Query("SELECT t.id, t.lead_agent FROM teams t JOIN agents a ON a.name=t.lead_agent "+
		"WHERE t.goal_id=? AND t.status='active' AND (a.status='live' OR t.parked_at IS NOT NULL) "+
		"ORDER BY a.status='live' DESC, t.id", goal)
	if err != nil {
		return "", fmt.Errorf("read teams of %s: %w", goal, err)
	}
	type tm struct{ id, lead string }
	var list []tm
	for rows.Next() {
		var x tm
		var lead sql.NullString
		if err := rows.Scan(&x.id, &lead); err != nil {
			rows.Close()
			return "", fmt.Errorf("read teams of %s: %w", goal, err)
		}
		x.lead = lead.String
		list = append(list, x)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return "", fmt.Errorf("read teams of %s: %w", goal, err)
	}
	for _, x := range list {
		o, err := s.AgentOrg(x.lead)
		if err != nil {
			return "", err
		}
		if o == gorg {
			return x.id, nil
		}
	}
	return "", nil
}

// driftTS is _ts(): an ISO stamp, a naive one in the process zone; anything unreadable (or NULL) is not a time.
func driftTS(s sql.NullString) (time.Time, bool) {
	if !s.Valid {
		return time.Time{}, false
	}
	return pyFromISO(s.String, driftNow().Location())
}

// driftItems is drift_items: the sorted, age-free item lines of one goal. states maps a PR url to OPEN/MERGED/CLOSED.
func driftItems(db *sql.DB, g driftGoal, now time.Time, states map[string]string) ([]string, error) {
	type task struct {
		id, status, deps string
		pr, updated      sql.NullString
		depsNull         bool
	}
	rows, err := db.Query("SELECT id, status, pr_url, updated, depends_on FROM tasks WHERE goal_id=? ORDER BY id", g.id)
	if err != nil {
		return nil, fmt.Errorf("read tasks of %s: %w", g.id, err)
	}
	var tasks []task
	for rows.Next() {
		var t task
		var st, dp sql.NullString
		if err := rows.Scan(&t.id, &st, &t.pr, &t.updated, &dp); err != nil {
			rows.Close()
			return nil, fmt.Errorf("read tasks of %s: %w", g.id, err)
		}
		t.status, t.deps = st.String, dp.String
		tasks = append(tasks, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, fmt.Errorf("read tasks of %s: %w", g.id, err)
	}
	items := []string{}
	if g.status == "proposed" || g.status == "planning" { // D1
		var why []string
		for _, t := range tasks {
			if t.status == "done" {
				why = append(why, "done tasks")
				break
			}
		}
		var team string
		switch err := db.QueryRow("SELECT id FROM teams WHERE goal_id=? AND status='active' ORDER BY id", g.id).Scan(&team); {
		case err == nil:
			why = append(why, "live team "+team)
		case errors.Is(err, sql.ErrNoRows):
		default:
			return nil, fmt.Errorf("read teams of %s: %w", g.id, err)
		}
		if len(why) > 0 {
			items = append(items, fmt.Sprintf("D1 %s is %s but has %s", g.id, g.status, strings.Join(why, " and ")))
		}
	}
	for _, o := range driftGoalOver {
		if g.status == o {
			return items, nil
		}
	}
	done, cancelled := map[string]bool{}, map[string]bool{}
	for _, t := range tasks {
		switch t.status {
		case "done":
			done[t.id] = true
		case "cancelled":
			cancelled[t.id] = true
		}
	}
	for _, t := range tasks {
		if t.status == "in_review" && t.pr.Valid && t.pr.String != "" {
			if s := states[t.pr.String]; s == "MERGED" || s == "CLOSED" { // D2
				items = append(items, fmt.Sprintf("D2 %s in_review but PR #%s %s", t.id, prNumber(t.pr.String), s))
			}
		}
		if g.status != "active" {
			continue
		}
		if t.status == "todo" || t.status == "in_progress" {
			idle, err := driftIdleSince(db, t.id, t.updated, now)
			if err != nil {
				return nil, err
			}
			if now.Sub(idle).Seconds() > driftIdleS { // D3
				deps, err := driftDeps(t.deps)
				if err != nil {
					return nil, err
				}
				var dead []string
				if t.status == "todo" {
					for d := range deps {
						if cancelled[d] {
							dead = append(dead, d)
						}
					}
					sort.Strings(dead)
				}
				if len(dead) > 0 { // a cancelled dependency never completes: the task would wait forever
					items = append(items, fmt.Sprintf("D3 %s todo but its dependency %s is cancelled", t.id, strings.Join(dead, ", ")))
					continue
				}
				waiting := false
				if t.status == "todo" {
					for d := range deps {
						if !done[d] {
							waiting = true
						}
					}
				}
				var one int
				live := true
				switch err := db.QueryRow("SELECT 1 FROM agents WHERE task_id=? AND status IN ('live','queued','starting')", t.id).Scan(&one); {
				case err == nil:
				case errors.Is(err, sql.ErrNoRows):
					live = false
				default:
					return nil, fmt.Errorf("read agents of %s: %w", t.id, err)
				}
				if !waiting && !live {
					items = append(items, fmt.Sprintf("D3 %s %s with no live or queued agent and no activity for over 2 h", t.id, t.status))
				}
			}
		}
		if t.status == "blocked" { // D4
			if since, ok := driftTS(t.updated); ok && now.Sub(since).Seconds() > driftBlockedS {
				items = append(items, fmt.Sprintf("D4 %s blocked for over 24 h", t.id))
			}
		}
	}
	sort.Strings(items)
	return items, nil
}

// prNumber is url.rstrip('/').rsplit('/', 1)[-1].
func prNumber(u string) string {
	u = strings.TrimRight(u, "/")
	if i := strings.LastIndexByte(u, '/'); i >= 0 {
		return u[i+1:]
	}
	return u
}

// driftIdleSince is idle_since: the latest of the task's updated, its newest event and its agents' newest start and
// end; with none readable, now.
func driftIdleSince(db *sql.DB, id string, updated sql.NullString, now time.Time) (time.Time, error) {
	stamps := []sql.NullString{updated}
	var ev, st, en sql.NullString
	if err := db.QueryRow("SELECT max(ts) FROM events WHERE entity='task' AND entity_id=?", id).Scan(&ev); err != nil {
		return time.Time{}, fmt.Errorf("read events of %s: %w", id, err)
	}
	if err := db.QueryRow("SELECT max(started), max(ended) FROM agents WHERE task_id=?", id).Scan(&st, &en); err != nil {
		return time.Time{}, fmt.Errorf("read agents of %s: %w", id, err)
	}
	stamps = append(stamps, ev, st, en)
	best, have := time.Time{}, false
	for _, s := range stamps {
		if d, ok := driftTS(s); ok && (!have || d.After(best)) {
			best, have = d, true
		}
	}
	if !have {
		return now, nil
	}
	return best, nil
}

// driftDeps is set(json.loads(depends_on or "[]")): a list gives its strings; any other element is a value that equals
// no task id; an object gives its keys, a string its characters; anything else is Python's TypeError.
func driftDeps(raw string) (map[string]bool, error) {
	if raw == "" {
		raw = "[]"
	}
	v, ok := pyParse(raw)
	if !ok {
		return nil, fmt.Errorf("depends_on is not JSON: %s", jsonErrText(raw))
	}
	set := map[string]bool{}
	switch v.kind {
	case pyList:
		for i, e := range v.list {
			switch e.kind {
			case pyStr:
				set[e.s] = true
			case pyList, pyObj:
				return nil, errors.New("unhashable type in depends_on")
			default:
				set[fmt.Sprintf("\x00non-string:%d", i)] = true
			}
		}
	case pyObj:
		for k := range v.obj {
			set[k] = true
		}
	case pyStr:
		for _, r := range v.s {
			set[string(r)] = true
		}
	default:
		return nil, errors.New("depends_on is not iterable")
	}
	return set, nil
}
