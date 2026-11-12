package cli

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Goal-scoped digest of bin/factory_cli.py: build_digest (goal path), digest_goal_block, digest_goal_org, goal_closed_at,
// drift_items, drift_pr_states, stale_agents, md_table and enabled_orgs (G-041 S2s owns this file; the factory-wide
// digest task reuses it and never edits it). Every input this port cannot reproduce byte for byte returns errDelegate.

// dgOrgNames is orgs.NAMES; dgDefaultOrg is orgs.DEFAULT_ORG (scout/orgs.py).
var dgOrgNames = []string{"claude", "cursor"}

const dgDefaultOrg = "claude"

// Drift constants (Python DRIFT_*).
const (
	dgDriftIdle    = 2 * time.Hour
	dgDriftBlocked = 24 * time.Hour
)

func dgIsOrg(s string) bool { return s == "claude" || s == "cursor" }

func dgCut(s string, n int) string { return pyTrunc(s, n) }

func dgStr(v sql.NullString) string {
	if !v.Valid {
		return "None"
	}
	return v.String
}

// dgMDTable is md_table(rows, cols) for already stringified rows (None is "").
func dgMDTable(rows [][]string, cols []string) string {
	if len(rows) == 0 {
		return "_none_\n"
	}
	out := "| " + strings.Join(cols, " | ") + " |\n|" + strings.Repeat("---|", len(cols)) + "\n"
	for _, r := range rows {
		cells := make([]string, len(r))
		for i, c := range r {
			cells[i] = dgCut(strings.ReplaceAll(strings.ReplaceAll(c, "|", "\\|"), "\n", " "), 120)
		}
		out += "| " + strings.Join(cells, " | ") + " |\n"
	}
	return out
}

// dgDeps is set(json.loads(depends_on or "[]")): the string members plus whether any member is not a string.
type dgDeps struct {
	ids   map[string]bool
	extra bool
}

func dgParseDeps(raw string) (dgDeps, error) {
	d := dgDeps{ids: map[string]bool{}}
	if raw == "" {
		return d, nil
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return d, errDelegate // Python raises (or accepts NaN); either way it is not ours
	}
	list, ok := v.([]any)
	if !ok {
		return d, errDelegate
	}
	for _, x := range list {
		switch s := x.(type) {
		case string:
			d.ids[s] = true
		case []any, map[string]any:
			return d, errDelegate // unhashable: Python raises TypeError
		default:
			d.extra = true
		}
	}
	return d, nil
}

// subsetOf is `deps <= other`.
func (d dgDeps) subsetOf(other map[string]bool) bool {
	if d.extra {
		return false
	}
	for k := range d.ids {
		if !other[k] {
			return false
		}
	}
	return true
}

type dgTask struct {
	id, goal, title, status, waits string
	assignee, prURL                sql.NullString
	attempts                       int64
	dependsOn, updated             string
}

func dgLoadTasks(db *sql.DB, goalID string) ([]dgTask, error) {
	rs, err := db.Query("SELECT id, goal_id, title, status, waits, assignee, pr_url, attempts, depends_on, updated "+
		"FROM tasks WHERE goal_id=? ORDER BY id", goalID)
	if err != nil {
		return nil, errDelegate
	}
	defer rs.Close()
	var out []dgTask
	for rs.Next() {
		var t dgTask
		var dep, upd sql.NullString
		if err := rs.Scan(&t.id, &t.goal, &t.title, &t.status, &t.waits, &t.assignee, &t.prURL, &t.attempts, &dep, &upd); err != nil {
			return nil, errDelegate
		}
		t.dependsOn, t.updated = dep.String, upd.String
		out = append(out, t)
	}
	if rs.Err() != nil {
		return nil, errDelegate
	}
	return out, nil
}

type dgEvent struct {
	id                    int64
	ts                    string
	t                     time.Time
	entity, entityID, act string
	detail                string
	hasEntity, hasEID     bool
}

type dgMerge struct {
	ts time.Time
	id string
}

// dgWindow is the (start, end] rule of inwin.
type dgWindow struct {
	start, end time.Time
	loc        *time.Location
}

// in is inwin(s): a value that does not parse is outside.
func (w dgWindow) in(s string) (time.Time, bool, error) {
	t, ok, err := sbParseTS(s, w.loc)
	if err != nil || !ok {
		return t, false, err
	}
	return t, t.After(w.start) && !t.After(w.end), nil
}

// dgDriftItems is drift_items(con, goal, now_dt, pr_states).
func dgDriftItems(db *sql.DB, gid, gstatus string, tasks []dgTask, now time.Time, loc *time.Location, prStates map[string]any) ([]string, error) {
	var items []string
	if gstatus == "proposed" || gstatus == "planning" { // D1
		var why []string
		for _, t := range tasks {
			if t.status == "done" {
				why = append(why, "done tasks")
				break
			}
		}
		var team string
		err := db.QueryRow("SELECT id FROM teams WHERE goal_id=? AND status='active' ORDER BY id", gid).Scan(&team)
		if err != nil && err != sql.ErrNoRows {
			return nil, errDelegate
		}
		if err == nil {
			why = append(why, "live team "+team)
		}
		if len(why) > 0 {
			items = append(items, fmt.Sprintf("D1 %s is %s but has %s", gid, gstatus, strings.Join(why, " and ")))
		}
	}
	if gstatus == "done" || gstatus == "closed" || gstatus == "cancelled" {
		return items, nil
	}
	doneIDs, cancelled := map[string]bool{}, map[string]bool{}
	for _, t := range tasks {
		switch t.status {
		case "done":
			doneIDs[t.id] = true
		case "cancelled":
			cancelled[t.id] = true
		}
	}
	idleSince := func(t dgTask) (time.Time, error) {
		stamps := []sql.NullString{{String: t.updated, Valid: true}}
		var evMax sql.NullString
		if err := db.QueryRow("SELECT max(ts) FROM events WHERE entity='task' AND entity_id=?", t.id).Scan(&evMax); err != nil {
			return now, errDelegate
		}
		var st, en sql.NullString
		if err := db.QueryRow("SELECT max(started), max(ended) FROM agents WHERE task_id=?", t.id).Scan(&st, &en); err != nil {
			return now, errDelegate
		}
		stamps = append(stamps, evMax, st, en)
		best, have := now, false
		for _, s := range stamps {
			if !s.Valid {
				continue
			}
			d, ok, err := sbParseTS(s.String, loc)
			if err != nil {
				return now, err
			}
			if ok && (!have || d.After(best)) {
				best, have = d, true
			}
		}
		return best, nil
	}
	for _, t := range tasks {
		if t.status == "in_review" && t.prURL.Valid && t.prURL.String != "" {
			if st, ok := prStates[t.prURL.String].(string); ok && (st == "MERGED" || st == "CLOSED") { // D2
				u := strings.TrimRight(t.prURL.String, "/")
				items = append(items, fmt.Sprintf("D2 %s in_review but PR #%s %s", t.id, u[strings.LastIndex(u, "/")+1:], st))
			}
		}
		if gstatus != "active" {
			continue
		}
		if t.status == "todo" || t.status == "in_progress" { // D3
			since, err := idleSince(t)
			if err != nil {
				return nil, err
			}
			if now.Sub(since) > dgDriftIdle {
				deps, err := dgParseDeps(t.dependsOn)
				if err != nil {
					return nil, err
				}
				var dead []string
				if t.status == "todo" {
					for k := range deps.ids {
						if cancelled[k] {
							dead = append(dead, k)
						}
					}
					sort.Strings(dead)
				}
				if len(dead) > 0 {
					items = append(items, fmt.Sprintf("D3 %s todo but its dependency %s is cancelled", t.id, strings.Join(dead, ", ")))
					continue
				}
				waiting := t.status == "todo" && !deps.subsetOf(doneIDs)
				var one int
				err = db.QueryRow("SELECT 1 FROM agents WHERE task_id=? AND status IN ('live','queued','starting')", t.id).Scan(&one)
				if err != nil && err != sql.ErrNoRows {
					return nil, errDelegate
				}
				live := err == nil
				if !waiting && !live {
					items = append(items, fmt.Sprintf("D3 %s %s with no live or queued agent and no activity for over 2 h", t.id, t.status))
				}
			}
		}
		if t.status == "blocked" { // D4
			since, ok, err := sbParseTS(t.updated, loc)
			if err != nil {
				return nil, err
			}
			if ok && now.Sub(since) > dgDriftBlocked {
				items = append(items, fmt.Sprintf("D4 %s blocked for over 24 h", t.id))
			}
		}
	}
	sort.Strings(items)
	return items, nil
}

// dgPRStates is drift_pr_states: the config row as an object; a missing row or invalid JSON is {}.
func dgPRStates(db *sql.DB) (map[string]any, error) {
	var raw sql.NullString
	err := db.QueryRow("SELECT value FROM config WHERE key='drift_pr_states'").Scan(&raw)
	if err == sql.ErrNoRows {
		return map[string]any{}, nil
	}
	if err != nil || !raw.Valid {
		return nil, errDelegate
	}
	var v any
	if err := json.Unmarshal([]byte(raw.String), &v); err != nil {
		if strings.Contains(raw.String, "NaN") || strings.Contains(raw.String, "Infinity") {
			return nil, errDelegate
		}
		return map[string]any{}, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errDelegate
	}
	return m, nil
}

// dgEnabledOrgs is enabled_orgs(con).
func dgEnabledOrgs(db *sql.DB) (map[string]bool, error) {
	raw, err := sbCfg(db, "orgs_enabled")
	if err != nil {
		return nil, err
	}
	var s string
	switch x := raw.(type) {
	case nil:
	case string:
		s = x
	default:
		return nil, errDelegate
	}
	out := map[string]bool{}
	for _, o := range strings.Split(s, ",") {
		if o = pyStrip(o); dgIsOrg(o) {
			out[o] = true
		}
	}
	if len(out) == 0 {
		out[dgDefaultOrg] = true
	}
	return out, nil
}

// dgGoalOrg is goal_org(con, id) or DEFAULT_ORG.
func dgGoalOrg(org string) string {
	if dgIsOrg(org) {
		return org
	}
	return dgDefaultOrg
}

// dgStale is stale_agents(con) joined as the Freshness line.
func dgFreshness(db *sql.DB) (string, error) {
	rs, err := db.Query("SELECT key, ts FROM throttle WHERE key LIKE 'fresh:%' ORDER BY key")
	if err != nil {
		return "", errDelegate
	}
	defer rs.Close()
	var parts []string
	for rs.Next() {
		var k string
		var ts any
		if err := rs.Scan(&k, &ts); err != nil {
			return "", errDelegate
		}
		s, ok := ts.(string)
		if !ok {
			return "", errDelegate
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", k[len("fresh:"):], strings.ReplaceAll(s, ",", ", ")))
	}
	if rs.Err() != nil {
		return "", errDelegate
	}
	if len(parts) == 0 {
		return "Freshness: all current", nil
	}
	return "Freshness: " + strings.Join(parts, "; "), nil
}

// dgGoalClosedAt is goal_closed_at(con, g).
func dgGoalClosedAt(db *sql.DB, id, updated string) (string, error) {
	rs, err := db.Query("SELECT ts, detail FROM events WHERE entity='goal' AND entity_id=? AND action='status' ORDER BY id DESC", id)
	if err != nil {
		return "", errDelegate
	}
	defer rs.Close()
	for rs.Next() {
		var ts string
		var d sql.NullString
		if err := rs.Scan(&ts, &d); err != nil {
			return "", errDelegate
		}
		if strings.HasPrefix(d.String, "done") || strings.HasPrefix(d.String, "cancelled") {
			return ts, nil
		}
	}
	if rs.Err() != nil {
		return "", errDelegate
	}
	return updated, nil
}

type dgGoal struct{ id, title, status, org, updated string }

// dgBuild is build_digest(con, goal=G, date, hours) for a goal id; date "" means the clock's day.
func dgBuild(c *ctx, goal, date string, hours int) (string, error) {
	db := c.db
	loc, err := sbLocation(c.env.Getenv)
	if err != nil {
		return "", err
	}
	var end time.Time
	if date != "" {
		d, err := time.Parse("2006-01-02", date)
		if err != nil || len(date) != 10 {
			return "", errDelegate // Python's strptime is more lenient, or dies with its own message
		}
		mid := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
		_, o0 := mid.Zone()
		_, o1 := mid.Add(-12 * time.Hour).Zone()
		_, o2 := mid.Add(12 * time.Hour).Zone()
		if mid.Hour() != 0 || o0 != o1 || o0 != o2 {
			return "", errDelegate // a DST change near midnight: astimezone() picks by the fold rules
		}
		end = mid.In(time.FixedZone("", o0)).Add(24*time.Hour - time.Second)
	} else {
		if end, err = sbClock(c.env.Getenv, c.env.Now, loc); err != nil {
			return "", err
		}
	}
	if _, off := end.Zone(); off%60 != 0 {
		return "", errDelegate
	}
	start := end.Add(-time.Duration(hours) * time.Hour)
	w := dgWindow{start, end, loc}

	lc, err := sbLeadCounts(db)
	if err != nil {
		return "", err
	}
	title := date
	if title == "" {
		title = end.Format("2006-01-02")
	}
	row, err := sbDirectorRow(db, start, end, hours, loc)
	if err != nil {
		return "", err
	}
	out := []string{"# Factory digest " + title, "",
		fmt.Sprintf("Leads: %d live, %d parked, %d queued; workers live %d", lc.Leads, lc.Parked, lc.Queued, lc.Workers), "",
		"## Director", "", sbDirectorLine(row), ""}

	var g dgGoal
	var org sql.NullString
	err = db.QueryRow("SELECT id, title, status, org, updated FROM goals WHERE id=?", goal).Scan(&g.id, &g.title, &g.status, &org, &g.updated)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("no goal %s", goal)
	}
	if err != nil {
		return "", errDelegate
	}
	g.org = dgGoalOrg(org.String)

	// events of the window, in id order
	rs, err := db.Query("SELECT id, ts, entity, entity_id, action, detail FROM events ORDER BY id")
	if err != nil {
		return "", errDelegate
	}
	var events []dgEvent
	for rs.Next() {
		var e dgEvent
		var ent, eid, det sql.NullString
		if err := rs.Scan(&e.id, &e.ts, &ent, &eid, &e.act, &det); err != nil {
			rs.Close()
			return "", errDelegate
		}
		t, in, werr := w.in(e.ts)
		if werr != nil {
			rs.Close()
			return "", werr
		}
		if !in {
			continue
		}
		e.t, e.entity, e.entityID, e.detail, e.hasEntity, e.hasEID = t, ent.String, eid.String, det.String, ent.Valid, eid.Valid
		events = append(events, e)
	}
	rs.Close()
	if rs.Err() != nil {
		return "", errDelegate
	}
	scoreRows, err := sbScoreRows(db)
	if err != nil {
		return "", err
	}

	var closedEarlier []string
	shown := true
	if g.status == "done" || g.status == "cancelled" {
		closedRaw, err := dgGoalClosedAt(db, g.id, g.updated)
		if err != nil {
			return "", err
		}
		closed, ok, err := sbParseTS(closedRaw, loc)
		if err != nil {
			return "", err
		}
		if !ok || closed.Before(end.Add(-24*time.Hour)) || closed.After(end) { // a fixed 24 h, whatever --hours
			if ok { // goal is set: the goal is shown in "Closed earlier" whatever the window says
				closedEarlier = append(closedEarlier, fmt.Sprintf("- %s — %s (%s, closed %s)", g.id, g.title, g.status, closedRaw))
			}
			shown = false
		}
	}
	counts := map[string]int{}
	if shown {
		counts[g.org]++
	}
	out = append(out, fmt.Sprintf("Orgs: claude %d active · cursor %d active", counts["claude"], counts["cursor"]), "")
	enabled, err := dgEnabledOrgs(db)
	if err != nil {
		return "", err
	}

	freshShown := false
	for _, o := range dgOrgNames {
		var orgGoals []dgGoal
		if shown && g.org == o {
			orgGoals = append(orgGoals, g)
		}
		if !enabled[o] && len(orgGoals) == 0 {
			continue
		}
		out = append(out, "## Org "+o, "")
		if len(orgGoals) == 0 {
			out = append(out, "_no active goals_", "")
			continue
		}
		block, err := dgGoalBlock(c, w, g, events, scoreRows, !freshShown)
		if err != nil {
			return "", err
		}
		freshShown = true
		out = append(out, block...)
	}
	if !freshShown {
		f, err := dgFreshness(db)
		if err != nil {
			return "", err
		}
		out = append(out, f, "")
	}
	if len(closedEarlier) > 0 {
		out = append(out, "## Closed earlier", "")
		out = append(out, closedEarlier...)
		out = append(out, "")
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n") + "\n", nil
}

// dgGoalBlock is digest_goal_block(g); withFresh prints the factory-wide Freshness line (the first block only).
func dgGoalBlock(c *ctx, w dgWindow, g dgGoal, events []dgEvent, scoreRows []ScoreRow, withFresh bool) ([]string, error) {
	db := c.db
	block := []string{fmt.Sprintf("## %s — %s (%s)", g.id, g.title, g.status), ""}
	tasks, err := dgLoadTasks(db, g.id)
	if err != nil {
		return nil, err
	}
	byID := map[string]dgTask{}
	for _, t := range tasks {
		byID[t.id] = t
	}
	tline := func(t dgTask, extra string) string { return fmt.Sprintf("- %s %s%s", t.id, t.title, extra) }

	// Done
	done := map[string]string{}
	for _, e := range events {
		if e.entity == "task" && e.hasEID && e.hasEntity {
			if _, ok := byID[e.entityID]; ok {
				if e.act == "merged" {
					done[e.entityID] = strings.TrimLeft(e.detail, "@")
				} else if e.act == "update" && strings.Contains(e.detail, "status=done") {
					if _, seen := done[e.entityID]; !seen {
						done[e.entityID] = ""
					}
				}
			}
		}
	}
	type verdict struct {
		taskID, created string
		head            sql.NullString
	}
	vr, err := db.Query("SELECT task_id, created, head_sha FROM verdicts WHERE verdict='merge' ORDER BY id")
	if err != nil {
		return nil, errDelegate
	}
	var mergeVerdicts []verdict
	for vr.Next() {
		var v verdict
		if err := vr.Scan(&v.taskID, &v.created, &v.head); err != nil {
			vr.Close()
			return nil, errDelegate
		}
		mergeVerdicts = append(mergeVerdicts, v)
	}
	vr.Close()
	if vr.Err() != nil {
		return nil, errDelegate
	}
	for _, v := range mergeVerdicts {
		if _, ok := byID[v.taskID]; !ok {
			continue
		}
		_, in, err := w.in(v.created)
		if err != nil {
			return nil, err
		}
		if in {
			done[v.taskID] = dgCut(v.head.String, 8)
		}
	}
	for _, t := range tasks {
		if t.status == "done" {
			_, in, err := w.in(t.updated)
			if err != nil {
				return nil, err
			}
			if in {
				if _, ok := done[t.id]; !ok {
					done[t.id] = ""
				}
			}
		}
	}
	var ids []string
	for id := range done {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var lines []string
	for _, id := range ids {
		t := byID[id]
		extra := ""
		if t.prURL.Valid && t.prURL.String != "" {
			extra += " — " + t.prURL.String
		}
		if done[id] != "" {
			extra += " @" + done[id]
		}
		lines = append(lines, tline(t, extra))
	}
	block = append(block, "### Done")
	if len(lines) == 0 {
		lines = []string{"_nothing_"}
	}
	block = append(block, lines...)
	block = append(block, "")

	// In progress
	lines = nil
	for _, t := range tasks {
		if t.status == "in_progress" || t.status == "in_review" {
			who := "unassigned"
			if t.assignee.Valid && t.assignee.String != "" {
				who = t.assignee.String
			}
			lines = append(lines, tline(t, fmt.Sprintf(" (%s, %s, attempts %d)", t.status, who, t.attempts)))
		}
	}
	sort.Strings(lines)
	if len(lines) == 0 {
		lines = []string{"_nothing_"}
	}
	block = append(block, "### In progress")
	block = append(block, lines...)
	block = append(block, "")

	// Blocked
	var merges []dgMerge
	for _, e := range events {
		if e.entity == "task" && e.act == "merged" {
			merges = append(merges, dgMerge{e.t, e.entityID})
		}
	}
	for _, v := range mergeVerdicts {
		t, ok, err := sbParseTS(v.created, w.loc)
		if err != nil {
			return nil, err
		}
		if ok {
			merges = append(merges, dgMerge{t, v.taskID})
		}
	}
	mergedAfter := func(tid string, t time.Time) bool {
		for _, m := range merges {
			if m.id == tid && m.ts.After(t) {
				return true
			}
		}
		return false
	}
	reasons := map[string]string{}
	for _, e := range events {
		if e.entity != "task" || (e.act != "judge-blocked" && e.act != "merge-failed") {
			continue
		}
		t, ok := byID[e.entityID]
		if !ok || t.status == "done" || t.status == "cancelled" || mergedAfter(e.entityID, e.t) {
			continue
		}
		reasons[e.entityID] = fmt.Sprintf(" — %s: %s", e.act, dgCut(e.detail, 160))
	}
	nOwner, nInternal, nBlocked := 0, 0, 0
	for _, t := range tasks {
		if t.status != "blocked" {
			continue
		}
		nBlocked++
		switch t.waits {
		case "owner":
			nOwner++
		case "internal":
			nInternal++
		}
		if _, ok := reasons[t.id]; ok {
			continue
		}
		er, err := db.Query("SELECT action, detail FROM events WHERE entity='task' AND entity_id=? ORDER BY id DESC", t.id)
		if err != nil {
			return nil, errDelegate
		}
		why := ""
		for er.Next() {
			var act string
			var det sql.NullString
			if err := er.Scan(&act, &det); err != nil {
				er.Close()
				return nil, errDelegate
			}
			d := det.String
			if act == "note" && pyStrip(d) != "" {
				why = d
				break
			}
			if act == "update" && strings.Contains(d, "status=blocked") && strings.Contains(d, " — ") {
				why = d[strings.Index(d, " — ")+len(" — "):]
				break
			}
		}
		er.Close()
		why = dgCut(strings.Join(strings.FieldsFunc(why, pyIsSpace), " "), 160)
		tag := ""
		if t.waits == "owner" {
			tag = " (owner)"
		}
		if why != "" {
			reasons[t.id] = fmt.Sprintf(" — blocked%s: %s", tag, why)
		} else {
			reasons[t.id] = fmt.Sprintf(" (blocked%s)", tag)
		}
	}
	var rids []string
	for id := range reasons {
		rids = append(rids, id)
	}
	sort.Strings(rids)
	lines = nil
	for _, id := range rids {
		lines = append(lines, tline(byID[id], reasons[id]))
	}
	mine := map[string]bool{}
	ar, err := db.Query("SELECT name FROM agents WHERE goal_id=?", g.id)
	if err != nil {
		return nil, errDelegate
	}
	for ar.Next() {
		var n string
		if err := ar.Scan(&n); err != nil {
			ar.Close()
			return nil, errDelegate
		}
		mine[n] = true
	}
	ar.Close()
	if ar.Err() != nil {
		return nil, errDelegate
	}
	for _, e := range events {
		if e.entity != "agent" || e.act != "lost" || !e.hasEID || !mine[e.entityID] {
			continue
		}
		var tid sql.NullString
		if err := db.QueryRow("SELECT task_id FROM agents WHERE name=?", e.entityID).Scan(&tid); err != nil {
			return nil, errDelegate
		}
		if tid.Valid && tid.String != "" {
			var st string
			err := db.QueryRow("SELECT status FROM tasks WHERE id=?", tid.String).Scan(&st)
			if err != nil && err != sql.ErrNoRows {
				return nil, errDelegate
			}
			if err == nil && (st == "done" || mergedAfter(tid.String, e.t)) {
				continue // the agent's task has finished since
			}
		}
		lines = append(lines, fmt.Sprintf("- agent %s lost at %s", e.entityID, e.ts))
	}
	fr, err := db.Query("SELECT name, flagless_since FROM agents WHERE flagless=1 AND status='live' AND goal_id=? ORDER BY name", g.id)
	if err != nil {
		return nil, errDelegate
	}
	for fr.Next() {
		var n string
		var since sql.NullString
		if err := fr.Scan(&n, &since); err != nil {
			fr.Close()
			return nil, errDelegate
		}
		lines = append(lines, fmt.Sprintf("- agent %s running without role flags since %s", n, dgStr(since)))
	}
	fr.Close()
	if fr.Err() != nil {
		return nil, errDelegate
	}
	var bl []string
	if nBlocked > 0 {
		bl = append(bl, fmt.Sprintf("Blocked tasks: %d owner-blocked, %d internal-blocked", nOwner, nInternal))
	}
	bl = append(bl, lines...)
	if len(bl) == 0 {
		bl = []string{"_nothing_"}
	}
	block = append(block, "### Blocked")
	block = append(block, bl...)
	block = append(block, "")

	// Up next
	doneIDs := map[string]bool{}
	for _, t := range tasks {
		if t.status == "done" {
			doneIDs[t.id] = true
		}
	}
	ready := 0
	for _, t := range tasks {
		if t.status != "todo" {
			continue
		}
		deps, err := dgParseDeps(t.dependsOn)
		if err != nil {
			return nil, err
		}
		if deps.subsetOf(doneIDs) {
			ready++
		}
	}
	block = append(block, "### Up next", fmt.Sprintf("%d ready todo task(s)", ready), "")
	states, err := dgPRStates(db)
	if err != nil {
		return nil, err
	}
	drift, err := dgDriftItems(db, g.id, g.status, tasks, w.end, w.loc, states)
	if err != nil {
		return nil, err
	}
	d := "none"
	if len(drift) > 0 {
		d = strings.Join(drift, "; ")
	}
	block = append(block, "Drift: "+d, "")
	if withFresh {
		f, err := dgFreshness(db)
		if err != nil {
			return nil, err
		}
		block = append(block, f, "")
	}
	tr, err := db.Query("SELECT id, parked_at FROM teams WHERE status='active' AND parked_at IS NOT NULL AND goal_id=? ORDER BY id", g.id)
	if err != nil {
		return nil, errDelegate
	}
	for tr.Next() {
		var id, parked string
		if err := tr.Scan(&id, &parked); err != nil {
			tr.Close()
			return nil, errDelegate
		}
		wakes := 0
		for _, e := range events {
			if e.act == "lead-wake" && e.hasEID && e.entityID == id {
				wakes++
			}
		}
		block = append(block, fmt.Sprintf("- %s parked since %s (%d wakes in window)", id, dgCut(parked, 16), wakes))
	}
	tr.Close()
	if tr.Err() != nil {
		return nil, errDelegate
	}
	block = append(block, "")
	var teams [][]string
	for _, r := range scoreRows {
		if r.Goal == g.id {
			teams = append(teams, []string{r.Team, r.Tasks, r.Done, r.Merged, r.Rejections, r.Retries, r.Score})
		}
	}
	cols := []string{"team", "tasks", "done", "merged", "rejections", "retries", "score"}
	ts := "_nothing_"
	if len(teams) > 0 {
		ts = strings.TrimRight(dgMDTable(teams, cols), "\n")
	}
	return append(block, "### Team scores", ts, ""), nil
}
