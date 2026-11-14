package cli

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"factory/go/internal/identity"
)

// The S2q reads: team list, project list, alerts [--json], gate show/list, goal tokens <G>. Each answers only the exact
// argv shapes below (no --x=v, no abbreviation, no extra positional); every other form, and `gate run`, runs in Python.
// None of them writes. Python: cmd_team (3263), cmd_project (2881), cmd_alerts (13191), cmd_gate (13683), gate_show_row,
// GATE_COLS, goal_tokens (13028), tally_tokens. The tally columns are gsTallyCounters (cmd_goal_show.go).

func init() {
	register("team", "list", handler{parse: r2NoArgs, run: runTeamList})
	register("project", "list", handler{parse: r2NoArgs, run: runProjectList})
	register("alerts", "", handler{parse: parseAlerts, run: runAlerts})
	register("gate", "show", handler{parse: parseGateShow, run: runGateShow})
	register("gate", "list", handler{parse: parseGateList, run: runGateList})
	register("goal", "tokens", handler{parse: parseGoalTokens, run: runGoalTokens})
}

func r2NoArgs(rest []string) (any, bool) { return nil, len(rest) == 0 }

// r2Str is str(v) for a value read from SQLite where Python prints None as "None".
func r2Str(v any) (string, error) {
	if v == nil {
		return "None", nil
	}
	return cell(v)
}

// r2Or is `v or "-"`: None and "" give the dash.
func r2Or(v any) (string, error) {
	switch n := v.(type) { // Python truthiness: 0 and 0.0 are falsy too
	case int64:
		if n == 0 {
			return "-", nil
		}
	case float64:
		if n == 0 {
			return "-", nil
		}
	}
	s, err := cell(v)
	if err != nil {
		return "", err
	}
	if s == "" {
		return "-", nil
	}
	return s, nil
}

func r2Table(c *ctx, q string, args []any, cols []string) error {
	rs, err := c.db.Query(q, args...)
	if err != nil {
		return errDelegate
	}
	names, rows, err := scanRows(rs)
	if err != nil {
		return err
	}
	idx := map[string]int{}
	for i, n := range names {
		idx[n] = i
	}
	cells := make([][]string, len(rows))
	for i, r := range rows {
		cells[i] = make([]string, len(cols))
		for j, col := range cols {
			k, ok := idx[col]
			if !ok {
				return errDelegate
			}
			if f, isF := r[k].(float64); isF && col == "score" {
				cells[i][j] = pyFloatRepr(f)
				continue
			}
			if cells[i][j], err = cell(r[k]); err != nil {
				return err
			}
		}
	}
	Table(c.out, cols, cells)
	return nil
}

var teamListCols = []string{"id", "goal_id", "status", "lead_agent", "score", "scope"}

func runTeamList(c *ctx, _ any) (int, error) {
	return 0, r2Table(c, "SELECT t.id,t.goal_id,t.status,t.lead_agent,"+
		"(SELECT avg(score) FROM evaluations e WHERE e.team_id=t.id) AS score,t.scope "+
		"FROM teams t ORDER BY t.id", nil, teamListCols)
}

var projectListCols = []string{"id", "name", "repo_path", "gh_repo", "base_branch", "auto_merge", "required_checks",
	"gate_cmd", "gate_count_cmd", "gate_timeout_s"}

func runProjectList(c *ctx, _ any) (int, error) {
	role, err := actorRole(c)
	if err != nil {
		return 0, errDelegate
	}
	if role == "retro" {
		return 0, &identity.ExitError{Code: 3, Msg: fmt.Sprintf("role 'retro' (%s) may not run `factory project`; ask your lead", c.who)}
	}
	return 0, r2Table(c, "SELECT id,name,repo_path,gh_repo,base_branch,auto_merge,required_checks,"+
		"gate_cmd,gate_count_cmd,gate_timeout_s FROM projects", nil, projectListCols)
}

type alertsCmd struct{ json bool }

func parseAlerts(rest []string) (any, bool) {
	var c alertsCmd
	ok := flagParser{bools: map[string]*bool{"--json": &c.json}}.parse(rest)
	return c, ok
}

var alertCols = []string{"id", "org", "kind", "ref", "since", "link"}

func runAlerts(c *ctx, p any) (int, error) {
	q := "SELECT id, org, kind, ref, since, link FROM owner_alert_feed WHERE closed IS NULL ORDER BY id DESC"
	if !p.(alertsCmd).json {
		return 0, r2Table(c, q, nil, alertCols)
	}
	rs, err := c.db.Query(q)
	if err != nil {
		return 0, errDelegate
	}
	_, rows, err := scanRows(rs)
	if err != nil {
		return 0, err
	}
	arr := &jnode{kind: 'a'}
	for _, r := range rows {
		o := &jnode{kind: 'o', keys: alertCols}
		for _, v := range r {
			switch x := v.(type) {
			case nil:
				o.vals = append(o.vals, nullNode())
			case int64:
				o.vals = append(o.vals, &jnode{kind: 'i', s: strconv.FormatInt(x, 10)})
			case string:
				if loneSurrogate.MatchString(x) {
					return 0, errDelegate
				}
				o.vals = append(o.vals, strNode(x))
			default:
				return 0, errDelegate
			}
		}
		arr.arr = append(arr.arr, o)
	}
	fmt.Fprintln(c.out, dumpIndent1(arr))
	return 0, nil
}

type gateShowCmd struct{ task string }

func parseGateShow(rest []string) (any, bool) {
	if len(rest) > 1 || (len(rest) == 1 && (rest[0] == "" || rest[0][0] == '-')) {
		return nil, false
	}
	c := gateShowCmd{}
	if len(rest) == 1 {
		c.task = rest[0]
	}
	return c, true
}

var gateShowCols = []string{"id", "kind", "project_id", "task_id", "status", "head_sha", "main_sha", "exit_code",
	"test_count", "main_count", "cmd", "log_path", "note", "actor", "created", "started", "finished"}

func runGateShow(c *ctx, p any) (int, error) {
	task := p.(gateShowCmd).task
	if task == "" {
		return 0, &identity.ExitError{Code: 2, Msg: "gate show needs a task id or a run id"}
	}
	var rows []gsRow
	var err error
	switch {
	case isASCIIDigits(task):
		n, e := strconv.ParseInt(task, 10, 64)
		if e != nil {
			return 0, errDelegate
		}
		rows, err = gsQuery(c, "SELECT * FROM gate_runs WHERE id=?", n)
	case hasNonASCII(task):
		return 0, errDelegate // str.isdigit accepts other digits; Python decides
	default:
		rows, err = gsQuery(c, "SELECT * FROM gate_runs WHERE task_id=? ORDER BY id DESC LIMIT 1", task)
	}
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, fmt.Errorf("no gate run for %s: see `factory gate list`", task)
	}
	r := rows[0]
	for _, k := range gateShowCols {
		if _, ok := r[k]; !ok {
			return 0, errDelegate
		}
	}
	s := map[string]string{}
	for _, k := range []string{"id", "kind", "project_id", "status", "cmd", "log_path", "actor", "created"} {
		if s[k], err = r2Str(r[k]); err != nil {
			return 0, err
		}
	}
	for _, k := range []string{"task_id", "head_sha", "main_sha", "note", "started", "finished"} {
		if s[k], err = r2Or(r[k]); err != nil {
			return 0, err
		}
	}
	exit := "-"
	if r["exit_code"] != nil {
		if exit, err = cell(r["exit_code"]); err != nil {
			return 0, err
		}
	}
	counts := "not counted (project has no gate_count_cmd)"
	if r["test_count"] != nil {
		tc, err := cell(r["test_count"])
		if err != nil {
			return 0, err
		}
		counts = tc + " on head"
		if r["main_count"] != nil {
			mc, err := cell(r["main_count"])
			if err != nil {
				return 0, err
			}
			counts += ", " + mc + " on main"
		}
	}
	o := c.out
	fmt.Fprintf(o, "gate run #%s  %s  project=%s  task=%s  status=%s\n", s["id"], s["kind"], s["project_id"], s["task_id"], s["status"])
	fmt.Fprintf(o, "  head   %s\n  main   %s\n", s["head_sha"], s["main_sha"])
	fmt.Fprintf(o, "  exit   %s\n  tests  %s\n", exit, counts)
	fmt.Fprintf(o, "  cmd    %s\n  log    %s\n  note   %s\n", s["cmd"], s["log_path"], s["note"])
	fmt.Fprintf(o, "  actor  %s  created %s  started %s  finished %s\n", s["actor"], s["created"], s["started"], s["finished"])
	return 0, nil
}

func isASCIIDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func hasNonASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 0x7f {
			return true
		}
	}
	return false
}

type gateListCmd struct {
	project string
	limit   int64
}

// argparse reads "-5" after --limit as a negative number (the parser has no option that looks like one).
var r2NegInt = regexp.MustCompile(`^-[0-9]+$`)

func parseGateList(rest []string) (any, bool) {
	c := gateListCmd{limit: 20}
	seenP, seenL := false, false
	for i := 0; i < len(rest); i += 2 {
		if i+1 >= len(rest) || rest[i+1] == "" {
			return nil, false
		}
		v := rest[i+1]
		switch rest[i] {
		case "--project":
			if seenP || v[0] == '-' {
				return nil, false
			}
			seenP, c.project = true, v
		case "--limit":
			if seenL || (v[0] == '-' && !r2NegInt.MatchString(v)) {
				return nil, false
			}
			n, ok, err := pyIntStr(v)
			if err != nil || !ok {
				return nil, false
			}
			seenL, c.limit = true, n
		default:
			return nil, false
		}
	}
	return c, true
}

var gateListCols = []string{"id", "project_id", "task_id", "kind", "status", "exit_code", "head_sha", "test_count", "main_count", "finished"}

func runGateList(c *ctx, p any) (int, error) {
	a := p.(gateListCmd)
	q, args := "SELECT * FROM gate_runs", []any{}
	if a.project != "" {
		q += " WHERE project_id=?"
		args = append(args, a.project)
	}
	args = append(args, max(1, a.limit))
	return 0, r2Table(c, q+" ORDER BY id DESC LIMIT ?", args, gateListCols)
}

type goalTokensCmd struct{ id string }

func parseGoalTokens(rest []string) (any, bool) {
	if len(rest) != 1 || rest[0] == "" || rest[0][0] == '-' {
		return nil, false
	}
	return goalTokensCmd{rest[0]}, true
}

type r2Tally map[string]any

func (t r2Tally) ints(keys ...string) ([]int64, error) {
	out := make([]int64, len(keys))
	for i, k := range keys {
		v, ok := t[k].(int64)
		if !ok {
			return nil, errDelegate
		}
		out[i] = v
	}
	return out, nil
}

func (t r2Tally) total() (int64, error) {
	v, err := t.ints(gsTallyCounters...)
	if err != nil {
		return 0, err
	}
	var sum int64
	for _, x := range v {
		if (x > 0 && sum > math.MaxInt64-x) || (x < 0 && sum < math.MinInt64-x) {
			return 0, errDelegate // Python ints do not overflow
		}
		sum += x
	}
	return sum, nil
}

func runGoalTokens(c *ctx, p any) (int, error) {
	gid := p.(goalTokensCmd).id
	gs, err := gsQuery(c, "SELECT id, org FROM goals WHERE id=?", gid)
	if err != nil {
		return 0, err
	}
	if len(gs) == 0 {
		return 0, fmt.Errorf("no goal %s", gid)
	}
	org, err := cell(gs[0]["org"])
	if err != nil {
		return 0, err
	}
	if org == "" {
		org = "claude"
	}
	if org != "claude" {
		fmt.Fprintf(c.out, "%s (%s): Cursor reports agent-hours and no tokens; there is no token tally for this goal.\n", gid, org)
		return 0, nil
	}
	all, err := gsQuery(c, "SELECT * FROM goal_token_tally")
	if err != nil {
		return 0, err
	}
	if len(all) == 0 {
		fmt.Fprintf(c.out, "%s: no tally yet (set config goal_token_tally_s above 0; the tick fills the table).\n", gid)
		return 0, nil
	}
	byID := map[string]r2Tally{}
	stamp := ""
	for _, r := range all {
		id, err := cell(r["goal_id"])
		if err != nil {
			return 0, err
		}
		ts, err := cell(r["computed_at"])
		if err != nil {
			return 0, err
		}
		if ts > stamp { // ISO text; Python compares str the same way (code point order == byte order in UTF-8)
			stamp = ts
		}
		byID[id] = r2Tally(r)
	}
	if r, ok := byID[gid]; !ok {
		fmt.Fprintf(c.out, "%s: no tokens attributed to this goal as of %s.\n", gid, stamp)
	} else {
		total, err := r.total()
		if err != nil {
			return 0, err
		}
		costS := "n/a"
		if r["cost"] != nil {
			f, ok := r["cost"].(float64)
			if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
				return 0, errDelegate
			}
			costS = fmt.Sprintf("$%.2f", f)
		}
		fmt.Fprintf(c.out, "%s tally: %d tokens (%s)\n", gid, total, strings.Join(gsTallyCounters, " + "))
		vals, err := r.ints(append(append([]string{}, gsTallyCounters...), "cache_read")...)
		if err != nil {
			return 0, err
		}
		for i, k := range gsTallyCounters {
			fmt.Fprintf(c.out, "  %s: %d\n", k, vals[i])
		}
		fmt.Fprintf(c.out, "cache_read: %d (printed apart, not in the tally)\n", vals[len(vals)-1])
		fmt.Fprintf(c.out, "cost estimate: %s\n", costS)
		ts, err := cell(r["computed_at"])
		if err != nil {
			return 0, err
		}
		fmt.Fprintf(c.out, "computed at: %s\n", ts)
	}
	if u, ok := byID["unattributed"]; ok {
		total, err := u.total()
		if err != nil {
			return 0, err
		}
		cr, err := u.ints("cache_read")
		if err != nil {
			return 0, err
		}
		fmt.Fprintf(c.out, "unattributed: %d tokens (cache_read %d) not counted in any goal\n", total, cr[0])
	}
	return 0, nil
}
