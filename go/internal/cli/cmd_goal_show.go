package cli

import (
	"fmt"
	"sort"
	"strings"
)

// `factory goal show <ID> [--full]`: the Go port of cmd_goal action "show" (show_goal, latest_audit). Every other argv
// form runs in Python.

func init() {
	register("goal", "show", handler{parse: parseGoalShow, run: runGoalShow})
}

type goalShowCmd struct {
	id   string
	full bool
}

func parseGoalShow(rest []string) (any, bool) {
	var c goalShowCmd
	have := false
	for _, a := range rest {
		switch {
		case a == "--full":
			c.full = true
		case a == "" || strings.HasPrefix(a, "-") || have:
			return nil, false
		default:
			c.id, have = a, true
		}
	}
	return c, have
}

// gsTallyCounters is TALLY_COUNTERS; TestTallyCountersMatchPython compares it with the Python literal.
var gsTallyCounters = []string{"input", "output", "cache_write_5m", "cache_write_1h"}

// gsStandard is the task statuses compact `goal show` lists first, in this order.
var gsStandard = []string{"todo", "in_progress", "in_review", "done", "cancelled"}

// gsRow is one SELECT * row by column name.
type gsRow map[string]any

func gsQuery(c *ctx, q string, args ...any) ([]gsRow, error) {
	rs, err := c.db.Query(q, args...)
	if err != nil {
		return nil, errDelegate
	}
	cols, rows, err := scanRows(rs)
	if err != nil {
		return nil, err
	}
	out := make([]gsRow, len(rows))
	for i, r := range rows {
		m := gsRow{}
		for j, k := range cols {
			m[k] = r[j]
		}
		out[i] = m
	}
	return out, nil
}

// gsStr is str(v) of a column value (taskPyStr); gsStrs applies it to several keys of one row.
func gsStrs(r gsRow, keys ...string) ([]string, error) {
	out := make([]string, len(keys))
	for i, k := range keys {
		s, err := taskPyStr(r[k])
		if err != nil {
			return nil, err
		}
		out[i] = s
	}
	return out, nil
}

func runGoalShow(c *ctx, p any) (int, error) {
	cmd := p.(goalShowCmd)
	// Python evaluates compact_view before show_goal, so detection (and any event it would record) comes first.
	compact, del := compactView(c, cmd.full)
	if del {
		return 0, errDelegate
	}
	gs, err := gsQuery(c, "SELECT * FROM goals WHERE id=?", cmd.id)
	if err != nil {
		return 0, err
	}
	if len(gs) == 0 {
		return 0, fmt.Errorf("no goal %s", cmd.id)
	}
	g := gs[0]
	h, err := gsStrs(g, "id", "title", "status", "priority", "project_id")
	if err != nil {
		return 0, err
	}
	var out strings.Builder
	fmt.Fprintf(&out, "# %s — %s\nstatus=%s priority=%s project=%s", h[0], h[1], h[2], h[3], h[4])
	if _, ok := g["org"]; ok {
		o, err := taskPyStr(g["org"])
		if err != nil {
			return 0, err
		}
		fmt.Fprintf(&out, " org=%s", o)
	}
	out.WriteString("\n")
	if tl, ok := g["token_limit"]; ok {
		on, err := truthy(tl)
		if err != nil {
			return 0, err
		}
		if on {
			if err := gsTokenLine(c, &out, cmd.id, tl); err != nil {
				return 0, err
			}
		}
	}
	for _, k := range []string{"intent", "success_criteria"} {
		on, err := truthy(g[k])
		if err != nil {
			return 0, err
		}
		if on {
			s, err := taskPyStr(g[k])
			if err != nil {
				return 0, err
			}
			fmt.Fprintf(&out, "\n## %s\n%s\n", k, s)
		}
	}
	ps, err := gsQuery(c, "SELECT * FROM plans WHERE goal_id=? ORDER BY version DESC LIMIT 1", cmd.id)
	if err != nil {
		return 0, err
	}
	aus, err := gsQuery(c, "SELECT * FROM audits WHERE goal_id=? ORDER BY version DESC LIMIT 1", cmd.id)
	if err != nil {
		return 0, err
	}
	var so gsRow
	if len(aus) > 0 {
		sos, err := gsQuery(c, "SELECT * FROM signoffs WHERE audit_id=? ORDER BY id DESC LIMIT 1", aus[0]["id"])
		if err != nil {
			return 0, err
		}
		if len(sos) > 0 {
			so = sos[0]
		}
	}
	if compact {
		err = gsCompact(c, &out, cmd.id, ps, aus, so)
	} else {
		err = gsFull(c, &out, cmd.id, ps, aus, so)
	}
	if err != nil {
		return 0, err
	}
	c.out.Write([]byte(out.String()))
	return 0, nil
}

// gsTokenLine is the token-limit line: the tally is the sum of the gsTallyCounters columns of goal_token_tally.
func gsTokenLine(c *ctx, out *strings.Builder, id string, limit any) error {
	lim, err := taskPyStr(limit)
	if err != nil {
		return err
	}
	ts, err := gsQuery(c, "SELECT * FROM goal_token_tally WHERE goal_id=?", id)
	if err != nil {
		return err
	}
	used := "no tally yet"
	if len(ts) > 0 {
		var sum int64
		for _, k := range gsTallyCounters {
			n, ok := ts[0][k].(int64)
			if !ok {
				return errDelegate // NULL or a float: Python's sum or its error is not reproduced
			}
			sum += n
		}
		used = fmt.Sprintf("tally %d tokens", sum)
	}
	fmt.Fprintf(out, "token limit: %s tokens (%s; `factory goal tokens %s`)\n", lim, used, id)
	return nil
}

func gsCompact(c *ctx, out *strings.Builder, id string, ps, aus []gsRow, so gsRow) error {
	if len(ps) > 0 {
		p := ps[0]
		f, err := gsStrs(p, "version", "author", "created")
		if err != nil {
			return err
		}
		body, ok := p["body"].(string)
		if !ok {
			return errDelegate
		}
		fmt.Fprintf(out, "\nplan v%s by %s %s, %d lines: factory goal show %s --full\n",
			f[0], f[1], f[2], len(pySplitlines(body)), id)
	}
	if len(aus) > 0 {
		f, err := gsStrs(aus[0], "version", "verdict", "author", "created")
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "audit v%s %s (%s, %s)\n", f[0], f[1], f[2], f[3])
		if so != nil {
			s, err := gsStrs(so, "author", "created")
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "sign-off of audit v%s (%s, %s)\n", f[0], s[0], s[1])
		}
	}
	rows, err := gsQuery(c, "SELECT status, count(*) n FROM tasks WHERE goal_id=? GROUP BY status", id)
	if err != nil {
		return err
	}
	counts := map[string]int64{}
	var total int64
	var others []string
	for _, r := range rows {
		st, ok := r["status"].(string)
		n, ok2 := r["n"].(int64)
		if !ok || !ok2 {
			return errDelegate // a NULL status sorts differently in Python
		}
		counts[st] = n
		total += n
	}
	var parts []string
	std := map[string]bool{}
	for _, st := range gsStandard {
		std[st] = true
		parts = append(parts, fmt.Sprintf("%s %d", st, counts[st]))
	}
	for st := range counts {
		if !std[st] {
			others = append(others, st)
		}
	}
	sort.Strings(others) // byte order = code point order on valid UTF-8
	for _, st := range others {
		parts = append(parts, fmt.Sprintf("%s %d", st, counts[st]))
	}
	fmt.Fprintf(out, "tasks: %d (%s)\n", total, strings.Join(parts, ", "))
	return nil
}

func gsFull(c *ctx, out *strings.Builder, id string, ps, aus []gsRow, so gsRow) error {
	if len(ps) > 0 {
		f, err := gsStrs(ps[0], "version", "author", "created", "body")
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "\n## plan v%s (%s, %s)\n%s\n", f[0], f[1], f[2], f[3])
	}
	if len(aus) > 0 {
		f, err := gsStrs(aus[0], "version", "verdict", "author", "created", "body")
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "\n## audit v%s %s (%s, %s)\n%s\n", f[0], f[1], f[2], f[3], f[4])
		if so != nil {
			s, err := gsStrs(so, "author", "created")
			if err != nil {
				return err
			}
			reason := ""
			if on, err := truthy(so["reason"]); err != nil {
				return err
			} else if on {
				if reason, err = taskPyStr(so["reason"]); err != nil {
					return err
				}
			}
			fmt.Fprintf(out, "\n## sign-off of audit v%s (%s, %s)\n%s\n", f[0], s[0], s[1], reason)
		}
	}
	teamCols := []string{"id", "lead_agent", "status", "scope"}
	teams, err := queryRows(c.db, "SELECT id,lead_agent,status,scope FROM teams WHERE goal_id=?", []any{id}, teamCols)
	if err != nil {
		return err
	}
	out.WriteString("\n## teams\n")
	Table(out, teamCols, teams)
	taskCols := []string{"id", "status", "role", "assignee", "depends_on", "pr_url", "title"}
	tasks, err := queryRows(c.db, "SELECT id,status,role,assignee,depends_on,pr_url,title FROM tasks WHERE goal_id=? ORDER BY id",
		[]any{id}, taskCols)
	if err != nil {
		return err
	}
	out.WriteString("\n## tasks\n")
	Table(out, taskCols, tasks)
	return nil
}
