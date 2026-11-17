package views

import (
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"factory/go/internal/tokens"
	"factory/go/internal/web/data"
)

// Tasks page constants (web/views/tasks.py).
const (
	titleTasks = "Tasks"
	reasonMax  = 120
	goalChipsN = 6
)

var (
	taskStatuses = []string{"todo", "in_progress", "in_review", "blocked", "done", "cancelled"}
	statusRe     = regexp.MustCompile(`^status=([a-z_]+)`)
	shaRe        = regexp.MustCompile(`^@?([0-9a-f]{7,40})\n?\z`)
	ciKind       = map[string]string{"green": "ok", "green at merge": "ok", "failing": "bad", "pending": "warn", "unknown": "muted"}
	ciWords      = map[string]string{"green": "CI passed", "green at merge": "CI passed", "failing": "CI failing", "pending": "CI pending"}
	verdictOK    = map[string]bool{"pass": true, "merge": true}
	// metricKeys are the sort keys that sort a metric column; unknown values (nil) sort last in both directions.
	metricKeys = map[string]bool{"cost": true, "tokens": true, "wall": true}
	columns    = []Col{
		{Key: "status", Label: "", Cls: "c-st"},
		{Key: "id", Label: "ID", Cls: "c-id mono"},
		{Key: "title", Label: "Task", Cls: "c-title", Sortable: true},
		{Key: "goal", Label: "Goal", Cls: "c-who", Sortable: true, PhoneLabel: true},
		{Key: "assignee", Label: "Assignee", Cls: "c-who", Sortable: true, PhoneLabel: true},
		{Key: "pr", Label: "PR", Cls: "c-who", Sortable: true, PhoneLabel: true},
		{Key: "rejections", Label: "Rejections", Cls: "c-who", Sortable: true, PhoneLabel: true},
		{Key: "cost", Label: "Cost", Cls: "c-who num", Sortable: true, PhoneLabel: true},
		{Key: "tokens", Label: "Tokens", Cls: "c-who num", Sortable: true, PhoneLabel: true},
		{Key: "wall", Label: "Wall time", Cls: "c-who num", Sortable: true, PhoneLabel: true},
		{Key: "updated", Label: "Updated", Cls: "c-age num", Sortable: true},
	}
)

func isWordRune(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r) }

// wordBefore reports whether the rune just before byte offset i is a Python \w character.
func wordBefore(s string, i int) bool {
	r, sz := utf8.DecodeLastRuneInString(s[:i])
	return sz > 0 && isWordRune(r)
}

func wordAt(s string, i int) bool {
	r, sz := utf8.DecodeRuneInString(s[i:])
	return sz > 0 && isWordRune(r)
}

// prSet is the Python search of \bpr=|\bstatus=in_review\b (Unicode \b, which Go's RE2 does not have).
func prSet(detail string) bool {
	for from := 0; ; {
		i := strings.Index(detail[from:], "pr=")
		if i < 0 {
			break
		}
		if !wordBefore(detail, from+i) {
			return true
		}
		from += i + 1
	}
	const lit = "status=in_review"
	for from := 0; ; {
		i := strings.Index(detail[from:], lit)
		if i < 0 {
			break
		}
		s := from + i
		if !wordBefore(detail, s) && !wordAt(detail, s+len(lit)) {
			return true
		}
		from = s + 1
	}
	return false
}

// taskRefs is re.findall(r"\bT-\d+\b") with Python's Unicode \d and \b, sorted and de-duplicated.
func taskRefs(detail string) []string {
	seen := map[string]bool{}
	var out []string
	for from := 0; ; {
		i := strings.Index(detail[from:], "T-")
		if i < 0 {
			break
		}
		s := from + i
		e := s + 2
		for {
			r, sz := utf8.DecodeRuneInString(detail[e:])
			if sz == 0 || !unicode.Is(unicode.Nd, r) {
				break
			}
			e += sz
		}
		if e > s+2 && !wordBefore(detail, s) && !wordAt(detail, e) {
			if id := detail[s:e]; !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
			from = e
		} else {
			from = s + 1
		}
	}
	sort.Strings(out)
	return out
}

// digitValue is int() of one Unicode decimal digit: the offset in its run of digits modulo ten.
func digitValue(r rune) int {
	n := 0
	for k := r - 1; unicode.Is(unicode.Nd, k); k-- {
		n++
	}
	return n % 10
}

// prNumber is int(pr_label(url)[1:]) for a "#<digits>" label, 0 otherwise.
func prNumber(url string) int64 {
	l := PRLabel(url)
	if !strings.HasPrefix(l, "#") {
		return 0
	}
	var n int64
	for _, r := range l[1:] {
		n = n*10 + int64(digitValue(r))
	}
	return n
}

// ── list ──

type taskListRow struct {
	ID, GoalID, Title, Status, Waits, PRURL, CI, Assignee, Updated, Created, Org string
	Rejections, Attempts                                                         int64
	Cost                                                                         *float64
	Tokens, WallS                                                                *int64
	NA                                                                           bool
}

// TasksData is what the Tasks page renders.
type TasksData struct {
	Org, Goal, Status string
	Goals             []string
	Tasks             []taskListRow
	Counts            map[string]int
	GoalCounts        []goalCount
	Total             int
	SortKey, SortDir  string
}

type goalCount struct {
	Goal string
	N    int
}

func (c *Ctx) ciEntry(url string) *data.CIEntry {
	if c.CI == nil {
		return nil
	}
	return c.CI(url)
}

// ciFor is the CI label for a task row; only open PRs consult the cache (which never blocks).
func (c *Ctx) ciFor(t data.Task, merged map[string]bool) (string, []data.Check) {
	if t.Status == "done" && merged[t.ID] {
		return "green at merge", nil
	}
	if t.Status == "in_review" && deref(t.PRURL) != "" {
		e := c.ciEntry(*t.PRURL)
		var checks []data.Check
		if e != nil {
			checks = e.Checks
		}
		return data.CISummarize(e), checks
	}
	return "", nil
}

func (c *Ctx) mergedTasks() (map[string]bool, error) {
	out := map[string]bool{}
	rows, err := c.DB.Query("SELECT DISTINCT entity_id FROM events WHERE entity='task' AND action='merged'")
	if err != nil {
		return nil, fmt.Errorf("views: read merged events: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("views: scan merged events: %w", err)
		}
		out[id] = true
	}
	return out, rows.Err()
}

// rejections is task id -> rejections, counted like PRCycle (verdict rows, plus judge-reject events without a judge verdict).
func (c *Ctx) rejections() (map[string]int64, error) {
	rej, judgeV, judgeE := map[string]int64{}, map[string]int64{}, map[string]int64{}
	rows, err := c.DB.Query("SELECT task_id, role, COUNT(*) n FROM verdicts WHERE verdict IN ('fail','reject') GROUP BY task_id, role")
	if err != nil {
		return nil, fmt.Errorf("views: read verdicts: %w", err)
	}
	for rows.Next() {
		var id, role string
		var n int64
		if err := rows.Scan(&id, &role, &n); err != nil {
			rows.Close()
			return nil, fmt.Errorf("views: scan verdicts: %w", err)
		}
		rej[id] += n
		if role == "judge" {
			judgeV[id] = n
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("views: read verdicts: %w", err)
	}
	rows.Close()
	rows, err = c.DB.Query("SELECT entity_id, COUNT(*) n FROM events WHERE entity='task' AND action='judge-reject' GROUP BY entity_id")
	if err != nil {
		return nil, fmt.Errorf("views: read judge rejects: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var n int64
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("views: scan judge rejects: %w", err)
		}
		judgeE[id] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("views: read judge rejects: %w", err)
	}
	for id, n := range judgeE {
		if d := n - judgeV[id]; d > 0 {
			rej[id] += d
		}
	}
	return rej, nil
}

func tokenSum(b *tokens.Bucket) int64 {
	return b.Input + b.Output + b.CacheRead + b.CacheWrite5m + b.CacheWrite1h
}

// metrics is task id -> {cost, tokens, wall_s, na}: one rollup of the TokenIndex snapshot per page load (never waits: an
// index that is not ready gives nil, shown "-"). Cursor tasks have no token counts: na, shown "n/a", never 0.
// wall_s = created -> merge event for merged tasks.
func (c *Ctx) metrics(tasks []data.Task) (map[string]taskListRow, error) {
	var snap *tokens.Snapshot
	if c.Tokens != nil {
		snap = c.Tokens()
	}
	byTask := map[string]*tokens.Bucket{}
	if snap != nil && snap.Ready {
		att, err := tokens.Attribute(snap, c.DB.DB)
		if err != nil {
			return nil, fmt.Errorf("views: token attribution: %w", err)
		}
		byTask = tokens.Rollup(snap, att, nil).ByTask
	}
	mergedAt := map[string]string{}
	rows, err := c.DB.Query("SELECT entity_id, MAX(ts) ts FROM events WHERE entity='task' AND action='merged' GROUP BY entity_id")
	if err != nil {
		return nil, fmt.Errorf("views: read merge times: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var ts sql.NullString
		if err := rows.Scan(&id, &ts); err != nil {
			return nil, fmt.Errorf("views: scan merge times: %w", err)
		}
		mergedAt[id] = ts.String
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("views: read merge times: %w", err)
	}
	gorgs, err := c.DB.GoalOrgs()
	if err != nil {
		return nil, fmt.Errorf("views: goal orgs: %w", err)
	}
	out := map[string]taskListRow{}
	for _, t := range tasks {
		m := taskListRow{NA: data.OrgOfGoal(gorgs, t.GoalID) == "cursor"}
		if e := byTask[t.ID]; e != nil && !m.NA {
			if tok := tokenSum(e); tok != 0 {
				m.Tokens = &tok
				if e.Cost != nil && !e.CostUnknown {
					m.Cost = e.Cost
				}
			}
		}
		a, aok := data.ParseTS(t.Created)
		b, bok := data.ParseTS(mergedAt[t.ID])
		if aok && bok && !b.Before(a) {
			w := pyRound(b.Sub(a).Seconds())
			m.WallS = &w
		}
		out[t.ID] = m
	}
	return out, nil
}

// LoadTasks gathers the data of the Tasks page (web/views/tasks.py data()).
func LoadTasks(ctx *Ctx) (*TasksData, error) {
	org := ctx.org()
	gorgs, err := ctx.DB.GoalOrgs()
	if err != nil {
		return nil, fmt.Errorf("views: goal orgs: %w", err)
	}
	goalRows, err := ctx.DB.Query("SELECT id FROM goals")
	if err != nil {
		return nil, fmt.Errorf("views: read goals: %w", err)
	}
	known := map[string]bool{}
	for goalRows.Next() {
		var id string
		if err := goalRows.Scan(&id); err != nil {
			goalRows.Close()
			return nil, fmt.Errorf("views: scan goals: %w", err)
		}
		if org == "" || data.OrgOfGoal(gorgs, id) == org {
			known[id] = true
		}
	}
	if err := goalRows.Err(); err != nil {
		goalRows.Close()
		return nil, fmt.Errorf("views: read goals: %w", err)
	}
	goalRows.Close()
	goal, status := ctx.Query["goal"], ctx.Query["status"]
	if !known[goal] {
		goal = ""
	}
	okStatus := false
	for _, s := range taskStatuses {
		okStatus = okStatus || s == status
	}
	if !okStatus {
		status = ""
	}
	merged, err := ctx.mergedTasks()
	if err != nil {
		return nil, err
	}
	rej, err := ctx.rejections()
	if err != nil {
		return nil, err
	}
	every, err := ctx.DB.Tasks("", "")
	if err != nil {
		return nil, fmt.Errorf("views: tasks: %w", err)
	}
	d := &TasksData{Org: org, Goal: goal, Status: status, Counts: map[string]int{}}
	for _, s := range taskStatuses {
		d.Counts[s] = 0
	}
	goalCounts := map[string]int{}
	var shown []data.Task
	for _, t := range every {
		if org != "" && !known[t.GoalID] {
			continue
		}
		inGoal := goal == "" || t.GoalID == goal
		inStatus := status == "" || t.Status == status
		if inGoal {
			d.Total++
			d.Counts[t.Status]++
		}
		if inStatus {
			goalCounts[t.GoalID]++
		}
		if inGoal && inStatus {
			shown = append(shown, t)
		}
	}
	var gc []string
	for g := range goalCounts {
		gc = append(gc, g)
	}
	sort.Strings(gc)
	for _, g := range gc {
		d.GoalCounts = append(d.GoalCounts, goalCount{g, goalCounts[g]})
	}
	var all []string
	for g := range known {
		all = append(all, g)
	}
	sort.Strings(all)
	d.Goals = all
	met, err := ctx.metrics(shown)
	if err != nil {
		return nil, err
	}
	for _, t := range shown {
		label, _ := ctx.ciFor(t, merged)
		r := met[t.ID]
		r.ID, r.GoalID, r.Title, r.Status, r.Waits, r.PRURL = t.ID, t.GoalID, t.Title, t.Status, t.Waits, deref(t.PRURL)
		r.CI, r.Rejections, r.Attempts, r.Assignee, r.Updated, r.Created = label, rej[t.ID], t.Attempts, deref(t.Assignee), t.Updated, t.Created
		r.Org = data.OrgOfGoal(gorgs, t.GoalID)
		d.Tasks = append(d.Tasks, r)
	}
	d.SortKey, d.SortDir = "updated", "desc"
	k, dir := ctx.Query["sort"], ctx.Query["dir"]
	if (dir == "asc" || dir == "desc") && (k == "title" || k == "goal" || k == "assignee" || k == "pr" || k == "rejections" || k == "updated" || metricKeys[k]) {
		d.SortKey, d.SortDir = k, dir
	}
	return d, nil
}

func ciBadge(label string) any {
	if label == "" {
		return "-"
	}
	return Badge(label, ciKind[label])
}

func ciWord(label string) any {
	if w, ok := ciWords[label]; ok {
		return El("span", w, Cls("muted"))
	}
	return ""
}

func (t taskListRow) cell(field string) string {
	switch field {
	case "cost":
		if t.Cost != nil {
			return money(*t.Cost)
		}
	case "tokens":
		if t.Tokens != nil {
			return commas(*t.Tokens)
		}
	case "wall":
		if t.WallS != nil {
			return Dur(float64(*t.WallS), true)
		}
	}
	if t.NA && field != "wall" {
		return "n/a"
	}
	return "-"
}

func taskRowCells(t taskListRow, ctx *Ctx) []any {
	var pr any = ""
	if t.PRURL != "" {
		pr = El("span", Link(t.PRURL, PRLabel(t.PRURL)), " ", ciWord(t.CI))
	}
	rej := ""
	if n := t.Rejections; n != 0 {
		s := "s"
		if n == 1 {
			s = ""
		}
		rej = fmt.Sprintf("%d rejection%s", n, s)
		if t.Attempts > 1 {
			rej += fmt.Sprintf(" · %d tries", t.Attempts)
		}
	}
	var blocked any
	if t.Status == "blocked" {
		blocked = TaskBadge("blocked", t.Waits)
	}
	title := El("span", El("a", At("href", "/task/"+t.ID), t.Title), " ", blocked)
	var goal any = ""
	if t.GoalID != "" {
		goal = El("span", Link("/goal/"+t.GoalID, t.GoalID), " ", OrgBadge(t.Org, ""), Cls("orgrow"))
	}
	return []any{RowStatus(t.Status), t.ID, title, goal, t.Assignee, pr, rej, t.cell("cost"), t.cell("tokens"), t.cell("wall"),
		El("span", Ago(t.Updated, ctx.Now, ""), At("title", t.Updated))}
}

func sortTasks(rows []taskListRow, key, dir string) []taskListRow {
	out := append([]taskListRow(nil), rows...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	desc := dir == "desc"
	lessStr := func(f func(taskListRow) string) {
		sort.SliceStable(out, func(i, j int) bool {
			a, b := f(out[i]), f(out[j])
			if desc {
				return a > b
			}
			return a < b
		})
	}
	lessNum := func(f func(taskListRow) int64) {
		sort.SliceStable(out, func(i, j int) bool {
			a, b := f(out[i]), f(out[j])
			if desc {
				return a > b
			}
			return a < b
		})
	}
	switch key {
	case "title":
		lessStr(func(t taskListRow) string { return strings.ToLower(t.Title) })
	case "goal":
		lessStr(func(t taskListRow) string { return t.GoalID })
	case "assignee":
		lessStr(func(t taskListRow) string { return t.Assignee })
	case "pr":
		lessNum(func(t taskListRow) int64 { return prNumber(t.PRURL) })
	case "rejections":
		lessNum(func(t taskListRow) int64 { return t.Rejections })
	case "updated":
		lessStr(func(t taskListRow) string { return t.Updated })
	case "cost", "tokens", "wall":
		val := func(t taskListRow) (float64, bool) {
			switch key {
			case "cost":
				if t.Cost != nil {
					return *t.Cost, true
				}
			case "tokens":
				if t.Tokens != nil {
					return float64(*t.Tokens), true
				}
			default:
				if t.WallS != nil {
					return float64(*t.WallS), true
				}
			}
			return 0, false
		}
		var known, unknown []taskListRow
		for _, t := range out {
			if _, ok := val(t); ok {
				known = append(known, t)
			} else {
				unknown = append(unknown, t)
			}
		}
		sort.SliceStable(known, func(i, j int) bool {
			a, _ := val(known[i])
			b, _ := val(known[j])
			if desc {
				return a > b
			}
			return a < b
		})
		return append(known, unknown...)
	}
	return out
}

func taskChips(d *TasksData, q map[string]string) []any {
	var keep []KV
	for _, k := range []string{"sort", "dir"} {
		if v, ok := q[k]; ok {
			keep = append(keep, KV{k, v})
		}
	}
	// go builds the /tasks link: status and goal first (overridden by the arguments), then the kept sort keys.
	goHref := func(status, goal string) string {
		params := append([]KV{{"status", status}, {"goal", goal}}, keep...)
		return OrgHref("/tasks", d.Org, params...)
	}
	items := []Chip{{"All", N(d.Total), goHref("", d.Goal), d.Status == ""}}
	for _, s := range taskStatuses {
		items = append(items, Chip{Labels[s], N(d.Counts[s]), goHref(s, d.Goal), d.Status == s})
	}
	statusChips := Chips("Status", items)
	var shown, more []goalCount
	for i, g := range d.GoalCounts {
		if i < goalChipsN {
			shown = append(shown, g)
		} else {
			more = append(more, g)
		}
	}
	if d.Goal != "" {
		for i, g := range more {
			if g.Goal == d.Goal {
				shown = append(shown, g)
				more = append(more[:i:i], more[i+1:]...)
				break
			}
		}
	}
	goalItems := []Chip{{"All", nil, goHref(d.Status, ""), d.Goal == ""}}
	for _, g := range shown {
		goalItems = append(goalItems, Chip{g.Goal, N(g.N), goHref(d.Status, g.Goal), d.Goal == g.Goal})
	}
	rest := []any{Chips("Goal", goalItems)}
	if len(more) > 0 {
		var mi []Chip
		for _, g := range more {
			mi = append(mi, Chip{g.Goal, N(g.N), goHref(d.Status, g.Goal), d.Goal == g.Goal})
		}
		rest = append(rest, Disclosure(fmt.Sprintf("%d more goals", len(more)), "tasks-goals", nil, false, Chips("More goals", mi)))
	}
	active := 0
	if d.Goal != "" {
		active = 1
	}
	return []any{FilterBar("tasks-filters", active, []any{statusChips}, rest...)}
}

// RenderTasks renders the Tasks page body.
func RenderTasks(ctx *Ctx, d *TasksData) Safe {
	ss := SortState{d.SortKey, d.SortDir}
	sortHref := func(col string) string {
		nxt := "asc"
		if ss.Key == col && ss.Dir == "asc" {
			nxt = "desc"
		}
		if ss.Key != col && col == "updated" {
			nxt = "desc"
		}
		return OrgHref("/tasks", d.Org, KV{"status", d.Status}, KV{"goal", d.Goal}, KV{"sort", col}, KV{"dir", nxt})
	}
	var groups []Group
	for _, s := range GroupOrder {
		var rows []taskListRow
		for _, t := range d.Tasks {
			if t.Status == s {
				rows = append(rows, t)
			}
		}
		if len(rows) == 0 {
			continue
		}
		var cells [][]any
		for _, t := range sortTasks(rows, ss.Key, ss.Dir) {
			cells = append(cells, taskRowCells(t, ctx))
		}
		groups = append(groups, Group{Key: s, Label: Labels[s], Count: len(rows), Rows: cells, Collapsed: Collapsed[s] && d.Status != s})
	}
	var note any
	clear := OrgHref("/tasks", d.Org)
	if d.Goal != "" || d.Status != "" {
		anyOf := func(s string) string {
			if s == "" {
				return "any"
			}
			return s
		}
		note = El("p", fmt.Sprintf("Filtered: goal=%s, status=%s · ", anyOf(d.Goal), anyOf(d.Status)), El("a", At("href", clear), "Clear filters"), Cls("muted"))
	}
	line := fmt.Sprintf("%d tasks", d.Total)
	if d.Status != "" {
		line = fmt.Sprintf("%d of %d tasks", len(d.Tasks), d.Total)
	}
	table := DataTable(columns, groups, &ss, sortHref, "tasks", "No tasks match these filters.")
	if len(groups) == 0 && (d.Goal != "" || d.Status != "") {
		table = EmptyState("No tasks match these filters.", &EmptyAction{clear, "Clear filters"})
	}
	return El("div", El("p", line, Cls("summary-line")), taskChips(d, ctx.Query), note, table)
}

type tasksView struct{}

// Render implements View for "/tasks".
func (tasksView) Render(ctx *Ctx, _ map[string]string) (string, Safe, error) {
	d, err := LoadTasks(ctx)
	if err != nil {
		return "", "", err
	}
	return titleTasks, RenderTasks(ctx, d), nil
}
