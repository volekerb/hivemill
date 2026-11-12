package cli

import (
	"database/sql"
	"encoding/json"
)

// The S2b commands: task list, task ready and goal list. Each answers only the exact argv shapes below; every other
// form (abbreviated or --x=v flags, -h, extra positionals, a repeated flag, an empty or "-"-leading value, a missing
// value) parses to ok=false and runs in Python.

func init() {
	register("task", "list", handler{parse: parseTaskList, run: runTaskList})
	register("task", "ready", handler{parse: parseTaskReady, run: runTaskReady})
	register("goal", "list", handler{parse: parseGoalList, run: runGoalList})
}

type taskListCmd struct {
	goal, status string
	all          bool
}

type taskReadyCmd struct{ goal string }

type goalListCmd struct {
	org string
	all bool
}

// flagParser walks the words after the action. A value flag takes the next word; the word must be non-empty and must
// not start with "-" (argparse would read it as an option). Anything unknown or repeated fails the parse.
type flagParser struct {
	vals  map[string]*string
	bools map[string]*bool
	seen  map[string]bool
}

func (p flagParser) parse(rest []string) bool {
	p.seen = map[string]bool{}
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		if p.seen[a] {
			return false
		}
		if v, ok := p.vals[a]; ok {
			if i+1 >= len(rest) || rest[i+1] == "" || rest[i+1][0] == '-' {
				return false
			}
			i++
			*v = rest[i]
		} else if b, ok := p.bools[a]; ok {
			*b = true
		} else {
			return false
		}
		p.seen[a] = true
	}
	return true
}

func parseTaskList(rest []string) (any, bool) {
	var c taskListCmd
	ok := flagParser{vals: map[string]*string{"--goal": &c.goal, "--status": &c.status},
		bools: map[string]*bool{"--all": &c.all}}.parse(rest)
	return c, ok
}

func parseTaskReady(rest []string) (any, bool) {
	var c taskReadyCmd
	ok := flagParser{vals: map[string]*string{"--goal": &c.goal}}.parse(rest)
	return c, ok
}

func parseGoalList(rest []string) (any, bool) {
	var c goalListCmd
	ok := flagParser{vals: map[string]*string{"--org": &c.org}, bools: map[string]*bool{"--all": &c.all}}.parse(rest)
	return c, ok
}

// queryRows runs q and returns the named columns as table cells, in order.
func queryRows(db *sql.DB, q string, args []any, cols []string) ([][]string, error) {
	rs, err := db.Query(q, args...)
	if err != nil {
		return nil, errDelegate
	}
	defer rs.Close()
	got, err := rs.Columns()
	if err != nil {
		return nil, errDelegate
	}
	idx := map[string]int{}
	for i, n := range got {
		idx[n] = i
	}
	var rows [][]string
	for rs.Next() {
		vals := make([]any, len(got))
		ptrs := make([]any, len(got))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			return nil, errDelegate
		}
		row := make([]string, len(cols))
		for i, c := range cols {
			j, ok := idx[c]
			if !ok {
				return nil, errDelegate
			}
			if row[i], err = cell(vals[j]); err != nil {
				return nil, err
			}
		}
		rows = append(rows, row)
	}
	if rs.Err() != nil {
		return nil, errDelegate
	}
	return rows, nil
}

var taskListCols = []string{"id", "goal_id", "status", "waits", "role", "assignee", "attempts", "pr_url", "title"}

func runTaskList(c *ctx, p any) (int, error) {
	a := p.(taskListCmd)
	q := "SELECT id,goal_id,status,role,assignee,attempts,pr_url,title, " +
		"CASE WHEN status='blocked' THEN waits ELSE '' END AS waits FROM tasks WHERE 1=1"
	var args []any
	if a.goal != "" {
		q += " AND goal_id=?"
		args = append(args, a.goal)
	}
	if a.status != "" {
		q += " AND status=?"
		args = append(args, a.status)
	} else if !a.all {
		q += " AND status NOT IN ('done','cancelled')"
	}
	rows, err := queryRows(c.db, q+" ORDER BY id", args, taskListCols)
	if err != nil {
		return 0, err
	}
	Table(c.out, taskListCols, rows)
	return 0, nil
}

var taskReadyCols = []string{"id", "goal_id", "role", "title"}

func runTaskReady(c *ctx, p any) (int, error) {
	a := p.(taskReadyCmd)
	q := "SELECT id,goal_id,role,title,depends_on FROM tasks WHERE status='todo'"
	var args []any
	if a.goal != "" {
		q += " AND goal_id=?"
		args = append(args, a.goal)
	}
	rows, err := queryRows(c.db, q+" ORDER BY id", args, append(append([]string{}, taskReadyCols...), "depends_on"))
	if err != nil {
		return 0, err
	}
	doneRows, err := queryRows(c.db, "SELECT id FROM tasks WHERE status='done'", nil, []string{"id"})
	if err != nil {
		return 0, err
	}
	done := map[string]bool{}
	for _, r := range doneRows {
		done[r[0]] = true
	}
	var ready [][]string
	for _, r := range rows {
		var deps []string
		if json.Unmarshal([]byte(r[4]), &deps) != nil {
			return 0, errDelegate // not a JSON array of strings: Python decides (and may crash) itself
		}
		ok := true
		for _, d := range deps {
			if !done[d] {
				ok = false
				break
			}
		}
		if ok {
			ready = append(ready, r[:4])
		}
	}
	Table(c.out, taskReadyCols, ready)
	return 0, nil
}

var goalListCols = []string{"id", "org", "status", "priority", "project_id", "done", "total", "title"}

func runGoalList(c *ctx, p any) (int, error) {
	a := p.(goalListCmd)
	q := "SELECT g.id,g.org,g.status,g.priority,g.project_id,g.title," +
		"(SELECT count(*) FROM tasks t WHERE t.goal_id=g.id AND t.status='done') AS done," +
		"(SELECT count(*) FROM tasks t WHERE t.goal_id=g.id AND t.status!='cancelled') AS total FROM goals g"
	var conds []string
	if !a.all {
		conds = append(conds, "g.status NOT IN ('done','cancelled')")
	}
	var args []any
	if a.org != "" {
		conds = append(conds, "g.org=?")
		args = append(args, a.org)
	}
	for i, cnd := range conds {
		if i == 0 {
			q += " WHERE " + cnd
		} else {
			q += " AND " + cnd
		}
	}
	rows, err := queryRows(c.db, q+" ORDER BY g.priority, g.id", args, goalListCols)
	if err != nil {
		return 0, err
	}
	Table(c.out, goalListCols, rows)
	return 0, nil
}
