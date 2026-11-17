package views

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"factory/go/internal/tokens"
	"factory/go/internal/web/data"
)

type step struct {
	TS, Actor, Kind, Label, Text, Sha, Tone string
	Links                                   []string
}

type costData struct {
	State  string // cursor | not_ready | none | unknown | ok
	Cost   *float64
	Tokens int64
}

// TaskDetailData is what the task detail page renders. Found false is the calm not-found state.
type TaskDetailData struct {
	ID    string
	Found bool

	Title, GoalID, Role, Status, Waits, Assignee, TeamID, PRURL, FollowUpOf string
	FollowUps                                                               []data.Task
	CI                                                                      string
	Rejections, Retries, Attempts, Reopens                                  int64
	TotalS                                                                  *float64
	Steps                                                                   []step
	Cost                                                                    costData
	Lifecycle                                                               *data.Lifecycle
	Usage                                                                   *tokens.TaskUsageResult
	Incidents                                                               map[string]int
}

var verdictWords = map[string]string{"pass": "passed", "fail": "failed", "merge": "merged", "reject": "rejected"}

func splitFirst(s string) (string, string) {
	if i := strings.IndexByte(s, ' '); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

// stepText is the label plus the few structured details that are safe to show (never free-text notes).
func stepText(i data.CycleItem) (text, sha string) {
	lab, det := i.Label, deref(i.Detail)
	switch {
	case i.Kind == "verdict":
		role, verdict := splitFirst(lab)
		w := "fail"
		if verdictOK[verdict] {
			w = "pass"
		}
		return fmt.Sprintf("%s: %s (%s)", role, w, verdict), deref(i.Head)
	case i.Kind == "agent":
		return lab, ""
	case lab == "update":
		if m := statusRe.FindStringSubmatch(det); m != nil {
			return "status -> " + m[1], ""
		}
		return "update", ""
	case lab == "judge-blocked":
		return "judge blocked: " + trunc(det, reasonMax), ""
	case lab == "merged" || lab == "merge-failed" || lab == "merge-post-error":
		text = strings.ReplaceAll(lab, "merge-post-error", "merge post-step error")
		if m := shaRe.FindStringSubmatch(det); m != nil {
			s := m[1]
			if len(s) > 8 {
				s = s[:8]
			}
			return text, s
		}
		return text, ""
	case lab == "add":
		return "task created", ""
	}
	return lab, ""
}

func failKind(i data.CycleItem) string {
	lab := i.Label
	if i.Kind == "verdict" {
		_, v := splitFirst(lab)
		if verdictOK[v] {
			return "ok"
		}
		return "bad"
	}
	switch lab {
	case "merged":
		return "ok"
	case "judge-reject", "judge-blocked", "merge-failed", "reopen":
		return "bad"
	}
	return "muted"
}

// costOf is the task's list-price cost, from the TokenIndex snapshot like /tokens (rollup by_task over attribute()).
func costOf(ctx *Ctx, t data.Task) (costData, error) {
	gorgs, err := ctx.DB.GoalOrgs()
	if err != nil {
		return costData{}, fmt.Errorf("views: goal orgs: %w", err)
	}
	if data.OrgOfGoal(gorgs, t.GoalID) == "cursor" {
		return costData{State: "cursor"}, nil
	}
	var snap *tokens.Snapshot
	if ctx.Tokens != nil {
		snap = ctx.Tokens()
	}
	if snap == nil || !snap.Ready {
		return costData{State: "not_ready"}, nil
	}
	att, err := tokens.Attribute(snap, ctx.DB.DB)
	if err != nil {
		return costData{}, fmt.Errorf("views: token attribution: %w", err)
	}
	mine := map[string]bool{}
	for sid, a := range att {
		if deref(a.Task) == t.ID && a.Task != nil {
			mine[sid] = true
		}
	}
	var rows []tokens.Row
	for _, r := range snap.Rows {
		if mine[r.Session] {
			rows = append(rows, r)
		}
	}
	e := tokens.Rollup(&tokens.Snapshot{Ready: true, Rows: rows}, att, nil).ByTask[t.ID]
	if e == nil {
		return costData{State: "none"}, nil
	}
	tok := tokenSum(e)
	if tok == 0 {
		return costData{State: "none"}, nil
	}
	if e.Cost == nil || e.CostUnknown {
		return costData{State: "unknown", Cost: e.Cost, Tokens: tok}, nil
	}
	return costData{State: "ok", Cost: e.Cost, Tokens: tok}, nil
}

// LoadTaskDetail gathers the data of the task detail page.
func LoadTaskDetail(ctx *Ctx, id string) (*TaskDetailData, error) {
	d := &TaskDetailData{ID: id}
	rows, err := ctx.DB.Tasks("", "")
	if err != nil {
		return nil, fmt.Errorf("views: tasks: %w", err)
	}
	var t *data.Task
	for i := range rows {
		if rows[i].ID == id {
			t = &rows[i]
			break
		}
	}
	if t == nil {
		return d, nil
	}
	cyc, err := ctx.DB.PRCycle(id)
	if err != nil {
		return nil, fmt.Errorf("views: pr cycle: %w", err)
	}
	var mergedEv []data.CycleItem
	for _, i := range cyc.Items {
		if i.Kind == "event" && i.Label == "merged" {
			mergedEv = append(mergedEv, i)
		}
	}
	merged := map[string]bool{}
	if len(mergedEv) > 0 {
		merged[id] = true
	}
	d.CI, _ = ctx.ciFor(*t, merged)
	start, sok := data.ParseTS(t.Created)
	var end time.Time
	var eok bool
	switch {
	case len(mergedEv) > 0:
		end, eok = data.ParseTS(mergedEv[len(mergedEv)-1].TS)
	case t.Status == "done" || t.Status == "cancelled":
		end, eok = data.ParseTS(t.Updated)
	default:
		end, eok = ctx.Now, true
	}
	if sok && eok {
		// clock skew / out-of-order timestamps give an unknown total, never a clamped one
		if s := end.Sub(start).Seconds(); s >= 0 {
			d.TotalS = &s
		}
	}
	d.Found, d.Title, d.GoalID, d.Role, d.Status, d.Waits = true, t.Title, t.GoalID, t.Role, t.Status, t.Waits
	d.Assignee, d.TeamID, d.PRURL, d.FollowUpOf = deref(t.Assignee), deref(t.TeamID), deref(t.PRURL), deref(t.FollowUpOf)
	for _, r := range rows {
		if deref(r.FollowUpOf) == id {
			d.FollowUps = append(d.FollowUps, r)
		}
	}
	d.Rejections, d.Retries, d.Attempts, d.Reopens = cyc.Rejections, cyc.Retries, cyc.Attempts, cyc.Reopens
	items := append([]data.CycleItem(nil), cyc.Items...)
	// a merge verdict and the merged event usually share a second: the merge itself comes last
	when := func(i data.CycleItem) time.Time {
		if p, ok := data.ParseTS(i.TS); ok {
			return p
		}
		return ctx.Now
	}
	sort.SliceStable(items, func(a, b int) bool {
		ta, tb := when(items[a]), when(items[b])
		if !ta.Equal(tb) {
			return ta.Before(tb)
		}
		return items[a].Label != "merged" && items[b].Label == "merged"
	})
	prSeen := false
	for _, i := range items {
		if i.Kind == "event" && i.Label == "note" {
			continue
		}
		text, sha := stepText(i)
		tone := failKind(i)
		if !prSeen && i.Kind == "event" && i.Label == "update" && prSet(deref(i.Detail)) {
			prSeen, text, tone = true, "PR opened", "info"
		}
		s := step{TS: i.TS, Actor: deref(i.Actor), Kind: i.Kind, Label: i.Label, Text: text, Sha: sha, Tone: tone}
		if i.Label == "follow-up" {
			s.Links = taskRefs(deref(i.Detail))
		}
		d.Steps = append(d.Steps, s)
	}
	if d.Cost, err = costOf(ctx, *t); err != nil {
		return nil, err
	}
	if d.Lifecycle, err = ctx.DB.TaskLifecycle(id, ctx.Now); err != nil {
		return nil, fmt.Errorf("views: task lifecycle: %w", err)
	}
	var snap *tokens.Snapshot
	if ctx.Tokens != nil {
		snap = ctx.Tokens()
	}
	if snap != nil && snap.Ready {
		att, err := tokens.Attribute(snap, ctx.DB.DB)
		if err != nil {
			return nil, fmt.Errorf("views: token attribution: %w", err)
		}
		if d.Usage, err = tokens.TaskUsage(snap, att, ctx.DB.DB, id, ctx.Now); err != nil {
			return nil, fmt.Errorf("views: task usage: %w", err)
		}
	}
	if d.Incidents, err = incidentCauses(ctx, id); err != nil {
		return nil, err
	}
	return d, nil
}

// incidentCauses is cause -> count of this task's rejection_incidents rows (structured cause only, never free text).
func incidentCauses(ctx *Ctx, id string) (map[string]int, error) {
	rows, err := ctx.DB.Query("SELECT cause FROM rejection_incidents WHERE task_id=? ORDER BY id", id)
	if err != nil {
		return nil, fmt.Errorf("views: read rejection incidents: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, fmt.Errorf("views: scan rejection incidents: %w", err)
		}
		out[c]++
	}
	return out, rows.Err()
}

// ── render ──

func roleName(r string) string {
	if r == "qa" {
		return "QA"
	}
	return pyCapitalize(r)
}

func isReject(s step) bool {
	if s.Label == "judge-reject" {
		return true
	}
	if s.Kind == "verdict" {
		_, v := splitFirst(s.Label)
		return v == "fail" || v == "reject"
	}
	return false
}

func outcomeWord(v string) string {
	if w, ok := verdictWords[v]; ok {
		return w
	}
	return v
}

// verb is the human verb of a timeline step: "Reviewer passed", "Judge merged", "Coder started", ...
func verb(s step) string {
	switch {
	case s.Kind == "verdict":
		role, v := splitFirst(s.Label)
		return roleName(role) + " " + outcomeWord(v)
	case s.Label == "judge-reject":
		return "Judge rejected"
	case s.Kind == "agent":
		what, role := splitFirst(s.Label)
		w := "finished"
		if what == "spawn" {
			w = "started"
		}
		return roleName(role) + " " + w
	case s.Label == "merged":
		return "Merged"
	}
	r := []rune(s.Text)
	if len(r) == 0 {
		return ""
	}
	return strings.ToUpper(string(r[:1])) + string(r[1:])
}

// lineKey: a judge verdict row and the judge-reject event of one rejection share a second and collapse to one line (the
// event actor and the verdict author can differ). Every other line is kept (ok=false).
func lineKey(s step) (string, bool) {
	if isReject(s) && strings.Contains(s.Label, "judge") {
		r := []rune(s.TS)
		if len(r) > 19 {
			r = r[:19]
		}
		return "judge-reject|" + string(r), true
	}
	return "", false
}

// attemptBlocks groups timeline steps by attempt: an attempt ends at a rejection, a coder spawned again or a reopen
// starts the next. Duplicate lines (same second, see lineKey) merge. Oldest first.
func attemptBlocks(steps []step) [][]step {
	var blocks [][]step
	var cur []step
	seen := map[string]bool{}
	rejected := false
	for _, s := range steps {
		key, has := lineKey(s)
		if has && seen[key] {
			continue
		}
		spawn := s.Label == "spawn coder"
		late := s.Kind == "agent" && strings.HasPrefix(s.Label, "end ") // the failed attempt's own coder ending
		hasSpawn := false
		for _, c := range cur {
			hasSpawn = hasSpawn || c.Label == "spawn coder"
		}
		if len(cur) > 0 && ((rejected && !late && !isReject(s)) || s.Label == "reopen" || (spawn && hasSpawn)) {
			blocks = append(blocks, cur)
			cur, rejected = nil, false
		}
		if has {
			seen[key] = true
		}
		cur = append(cur, s)
		rejected = rejected || isReject(s)
	}
	if len(cur) > 0 {
		blocks = append(blocks, cur)
	}
	return blocks
}

type verdictLine struct{ Role, Word, TS string }

// verdictLines is (role, outcome word, ts) per verdict, duplicates merged; a merged event with no judge merge verdict
// counts as one.
func verdictLines(steps []step) []verdictLine {
	var out []verdictLine
	seen := map[string]bool{}
	for _, s := range steps {
		var role, word string
		switch {
		case s.Kind == "verdict":
			var v string
			role, v = splitFirst(s.Label)
			word = outcomeWord(v)
		case s.Label == "judge-reject":
			role, word = "judge", "rejected"
		case s.Label == "merged":
			role, word = "judge", "merged"
		default:
			continue
		}
		key, has := lineKey(s)
		if word == "merged" {
			key, has = "merged|"+role, true
		}
		judgeMerged := false
		for _, l := range out {
			judgeMerged = judgeMerged || (l.Role == "judge" && l.Word == "merged")
		}
		if (has && seen[key]) || (word == "merged" && judgeMerged) {
			continue
		}
		if has {
			seen[key] = true
		}
		out = append(out, verdictLine{role, word, s.TS})
	}
	return out
}

func runs(words []string) [][2]any {
	var out [][2]any
	for _, w := range words {
		if n := len(out); n > 0 && out[n-1][0] == w {
			out[n-1][1] = out[n-1][1].(int) + 1
		} else {
			out = append(out, [2]any{w, 1})
		}
	}
	return out
}

func phrase(words []string) string {
	rs := runs(words)
	var parts []string
	for n, r := range rs {
		w, c := r[0].(string), r[1].(int)
		if c > 1 || (len(rs) > 1 && n == 0) {
			times := fmt.Sprintf("%d times", c)
			if c == 1 {
				times = "once"
			}
			w += " " + times
		}
		parts = append(parts, w)
	}
	return strings.Join(parts, ", then ")
}

// verdictSummary is the C12 line: "Reviewer passed · QA failed once, then passed · CI passed · Judge merged".
func verdictSummary(d *TaskDetailData) any {
	lines := verdictLines(d.Steps)
	byRole := map[string][]string{}
	var roleOrder []string
	for _, l := range lines {
		if _, ok := byRole[l.Role]; !ok {
			roleOrder = append(roleOrder, l.Role)
		}
		byRole[l.Role] = append(byRole[l.Role], l.Word)
	}
	var order []string
	for _, r := range []string{"reviewer", "qa"} {
		if _, ok := byRole[r]; ok {
			order = append(order, r)
		}
	}
	for _, r := range roleOrder {
		if r != "reviewer" && r != "qa" && r != "judge" {
			order = append(order, r)
		}
	}
	var parts []string
	for _, r := range order {
		parts = append(parts, roleName(r)+" "+phrase(byRole[r]))
	}
	if w, ok := ciWords[d.CI]; ok {
		parts = append(parts, w)
	}
	if w, ok := byRole["judge"]; ok {
		parts = append(parts, "Judge "+phrase(w))
	}
	if len(parts) == 0 {
		return nil
	}
	text := strings.Join(parts, " · ")
	if len(lines) == 0 {
		return El("p", text, Cls("summary-line"))
	}
	var lis []any
	for _, l := range lines {
		lis = append(lis, El("li", roleName(l.Role)+" "+l.Word+" · ", Ts(l.TS)))
	}
	return Disclosure(text, "task-verdicts", nil, false, El("ul", lis, Cls("lrow-list")))
}

func attemptEl(n int, block []step, last bool) Safe {
	var items []any
	var prev time.Time
	var havePrev bool
	for _, s := range block {
		at, aok := data.ParseTS(s.TS)
		was, wok := prev, havePrev
		if aok {
			prev, havePrev = at, true
		}
		var stepEl any
		if aok && wok && !at.Before(was) {
			stepEl = El("span", "+"+Dur(at.Sub(was).Seconds(), true), Cls("muted"))
		}
		var extra []any
		if s.Sha != "" {
			extra = append(extra, El("span", " @"+s.Sha, Cls("mono")))
		}
		for _, x := range s.Links {
			extra = append(extra, El("span", " ", Link("/task/"+x, x)))
		}
		args := []any{Ts(s.TS), " ", Badge(verb(s), s.Tone), " ", El("span", s.Actor, Cls("muted"))}
		args = append(args, extra...)
		args = append(args, " ", stepEl, Cls("timeline-item"), At("data-actor", s.Actor), At("data-kind", s.Label), At("data-ts", s.TS))
		items = append(items, El("li", args...))
	}
	first, fok := data.ParseTS(block[0].TS)
	end, eok := data.ParseTS(block[len(block)-1].TS)
	span := ""
	if fok && eok && end.After(first) {
		span = Dur(end.Sub(first).Seconds(), true)
	}
	outcome := "ended"
	switch {
	case isReject(block[len(block)-1]):
		outcome = "rejected"
	case block[len(block)-1].Label == "merged":
		outcome = "merged"
	case last:
		outcome = "in progress"
	}
	disc := outcome
	if span != "" {
		disc += " · " + span
	}
	summ := El("summary", El("span", fmt.Sprintf("Attempt %d", n)), El("span", disc, Cls("disc-n")))
	var op any
	if last {
		op = true
	}
	return El("details", summ, El("ol", items, Cls("timeline")), Cls("disc attempt"), At("data-key", fmt.Sprintf("task-attempt-%d", n)), At("open", op))
}

var ciGlyph = map[string]string{"green": "done", "green at merge": "done", "failing": "blocked", "pending": "queued"}

func fmtCI(label string) any {
	g, ok := ciGlyph[label]
	if !ok {
		return nil
	}
	w := ciWords[label]
	if w == "" {
		w = label
	}
	return El("span", Glyph(g), w, Cls("st"))
}

func metaLine(d *TaskDetailData) Safe {
	var bits []any
	bits = append(bits, Status(d.Status, false))
	if d.GoalID != "" {
		bits = append(bits, Link("/goal/"+d.GoalID, d.GoalID))
	}
	if d.TeamID != "" {
		bits = append(bits, d.TeamID)
	}
	who := d.Assignee
	if who == "" {
		who = d.Role
	}
	if who != "" {
		bits = append(bits, who)
	}
	if d.PRURL != "" {
		bits = append(bits, Link(d.PRURL, PRLabel(d.PRURL)))
	}
	if c := fmtCI(d.CI); c != nil {
		bits = append(bits, c)
	}
	var out []any
	for i, b := range bits {
		if i > 0 {
			out = append(out, " · ")
		}
		out = append(out, b)
	}
	return El("p", append(out, Cls("metarow"))...)
}

func costLine(c costData) string {
	switch c.State {
	case "cursor":
		return "Cost: not available. The Cursor CLI reports no token counts."
	case "not_ready":
		return "Cost: not ready (the token index is still warming up)."
	case "none":
		return "Cost: none recorded (no tokens attributed to this task)."
	}
	note := fmt.Sprintf(" · estimated at list prices as of %s (API-equivalent, not a bill)", tokens.PricesAsOf)
	if c.State == "unknown" {
		part := ""
		if c.Cost != nil && *c.Cost != 0 {
			part = fmt.Sprintf("at least $%.2f, ", *c.Cost)
		}
		return "Cost: unknown (" + part + "some tokens are on a model without a price)" + note
	}
	if c.Cost == nil {
		return "Cost: unknown" + note
	}
	return fmt.Sprintf("Cost: $%.2f", *c.Cost) + note
}

func share(sec *int64, total *int64) string {
	if sec != nil && total != nil && *total != 0 {
		return pct0(float64(*sec) / float64(*total))
	}
	return "unknown"
}

func lifecycleSection(d *TaskDetailData) any {
	lc := d.Lifecycle
	if lc == nil {
		return nil
	}
	total := lc.Total.Seconds
	var rows [][]any
	for _, st := range lc.Stages {
		rows = append(rows, []any{st.Stage, Td{DurP(st.Span.Seconds), "num"}, Td{share(st.Span.Seconds, total), "num"}})
	}
	for _, w := range lc.Waits {
		kind := w.Kind
		switch kind {
		case "ci":
			kind = "waiting: CI"
		case "blocked":
			kind = "waiting: blocked"
		}
		if w.Kind == "blocked" {
			kind += " (" + w.Waits + ")"
		}
		rows = append(rows, []any{El("span", kind, " ", El("span", "inside a stage", Cls("muted"))), Td{DurP(w.Span.Seconds), "num"}, Td{share(w.Span.Seconds, total), "num"}})
	}
	if lc.GapsS != nil {
		rows = append(rows, []any{"other (rework, idle)", Td{DurP(lc.GapsS), "num"}, Td{share(lc.GapsS, total), "num"}})
	}
	label := "total"
	if lc.Total.Open {
		label += " (open)"
	}
	pct := "unknown"
	if total != nil && *total != 0 {
		pct = "100%"
	}
	rows = append(rows, []any{El("strong", label), Td{DurP(total), "num"}, Td{pct, "num"}})
	return Section("Lifecycle", nil, Table([]string{"Stage", "Duration", "Share"}, rows, "table"))
}

const notAvailable = "not available"

func nCell(v *int64) string {
	if v == nil {
		return notAvailable
	}
	return commas(*v)
}

func moneyCell(v *float64) string {
	if v == nil {
		return "unknown"
	}
	return money(*v)
}

func usageCells(state string, in, out, cr, cw *int64, cost *float64) []any {
	if state == tokens.NotAvailable {
		return []any{Td{notAvailable, "num"}, Td{notAvailable, "num"}, Td{notAvailable, "num"}, Td{notAvailable, "num"}, Td{notAvailable, "num"}}
	}
	return []any{Td{nCell(in), "num"}, Td{nCell(out), "num"}, Td{nCell(cr), "num"}, Td{nCell(cw), "num"}, Td{moneyCell(cost), "num"}}
}

func reworkLine(d *TaskDetailData) string {
	cost := "unknown"
	if d.Usage != nil && d.Usage.Totals != nil {
		if t := d.Usage.Totals; t.ReworkCost != nil && t.ReworkShare != nil {
			cost = fmt.Sprintf("%s (%s of cost)", moneyCell(t.ReworkCost), pct0(*t.ReworkShare))
		}
	}
	var causes []string
	keys := make([]string, 0, len(d.Incidents))
	sum := 0
	for c, n := range d.Incidents {
		keys = append(keys, c)
		sum += n
	}
	sort.Strings(keys)
	for _, c := range keys {
		causes = append(causes, fmt.Sprintf("%s ×%d", c, d.Incidents[c]))
	}
	if missing := int(d.Rejections) - sum; missing > 0 {
		causes = append(causes, fmt.Sprintf("cause not recorded ×%d", missing))
	}
	if len(causes) == 0 && d.Rejections == 0 {
		return "Rework: none (no rejections)"
	}
	return fmt.Sprintf("Rework: %s · causes: %s", cost, strings.Join(causes, ", "))
}

func usageSection(d *TaskDetailData) Safe {
	u := d.Usage
	rework := El("p", reworkLine(d), Cls("muted"))
	if u == nil {
		return Section("Cost and tokens", nil, El("p", "Not ready (the token index is still warming up).", Cls("muted")), rework)
	}
	head := El("p", fmt.Sprintf("Estimated at list prices as of %s (API-equivalent, not a bill)", tokens.PricesAsOf), Cls("muted"))
	if len(u.Rows) == 0 {
		return Section("Cost and tokens", nil, head, EmptyState("No tokens attributed to this task.", nil), rework)
	}
	var rows [][]any
	for _, r := range u.Rows {
		role := "-"
		if r.Role != nil && *r.Role != "" {
			role = *r.Role
		}
		row := []any{role, r.Agent, Td{fmt.Sprint(r.Sessions), "num"}}
		rows = append(rows, append(row, usageCells(r.State, r.Input, r.Output, r.CacheRead, r.CacheWrite, r.Cost)...))
	}
	t := u.Totals
	if t == nil {
		t = &tokens.RowTotals{}
	}
	rows = append(rows, append([]any{El("strong", "Total"), "", Td{fmt.Sprint(t.Sessions), "num"}}, usageCells(t.State, t.Input, t.Output, t.CacheRead, t.CacheWrite, t.Cost)...))
	var note any
	if t.CursorRows != 0 {
		note = El("p", "Totals cover the Claude agents only; Cursor reports no token counts.", Cls("muted"))
	}
	return Section("Cost and tokens", nil, head, Table([]string{"Role", "Agent", "Sessions", "Input", "Output", "Cache read", "Cache write", "Cost"}, rows, "table"), note, rework)
}

// RenderTaskDetail renders the task detail body.
func RenderTaskDetail(ctx *Ctx, d *TaskDetailData) Safe {
	id := d.ID
	crumbID := id
	if crumbID == "" {
		crumbID = "?"
	}
	crumb := Crumbs(Crumb{"/tasks", "Tasks"}, Crumb{"/task/" + id, crumbID})
	if !d.Found {
		return notFound("task", id, "/tasks", "Back to Tasks", crumb)
	}
	var totalS float64
	totalOK := d.TotalS != nil
	if totalOK {
		totalS = *d.TotalS
	}
	tiles := El("div", Tile("Total time", Dur(totalS, totalOK), TileOpts{}), Tile("Rejections", d.Rejections, TileOpts{}), Tile("Attempts", d.Attempts, TileOpts{}), Cls("tiles"))
	blocks := attemptBlocks(d.Steps)
	var cycle []any
	if len(blocks) == 0 {
		cycle = []any{EmptyState("No events yet.", nil)}
	}
	for n, b := range blocks {
		cycle = append(cycle, attemptEl(n+1, b, n+1 == len(blocks)))
	}
	var vs any = verdictSummary(d)
	if vs == nil {
		vs = EmptyState("No verdicts yet.", nil)
	}
	parts := []any{El("div", crumb, metaLine(d), El("p", costLine(d.Cost), Cls("muted")), Cls("tblock tblock--wide")), Section("Verdicts", nil, vs), tiles,
		lifecycleSection(d), usageSection(d), Section("PR cycle", nil, cycle...)}
	var fu []any
	if d.FollowUpOf != "" {
		fu = append(fu, El("span", "follow-up of ", Link("/task/"+d.FollowUpOf, d.FollowUpOf), " "))
	}
	for _, f := range d.FollowUps {
		fu = append(fu, El("span", Link("/task/"+f.ID, f.ID), " ", f.Title, " ", TaskBadge(f.Status, f.Waits), " "))
	}
	if len(fu) > 0 {
		parts = append(parts, Section("Follow-ups", nil, El("p", fu...)))
	}
	return El("div", parts...)
}

type taskDetailView struct{}

// Render implements View for "/task/<id>"; params["id"] is the task id.
func (taskDetailView) Render(ctx *Ctx, params map[string]string) (string, Safe, error) {
	d, err := LoadTaskDetail(ctx, params["id"])
	if err != nil {
		return "", "", err
	}
	title := "Task"
	if d.Found && d.Title != "" {
		title = d.Title
	}
	return title, RenderTaskDetail(ctx, d), nil
}
