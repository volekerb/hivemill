package cli

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// Task dependency helpers shared by the task write commands: the Go ports of split_after, dep_cycle, check_after and
// task_ordered in bin/factory_cli.py (3327-3366, 3415). Frozen after merge: later tasks reuse, never edit.

// splitAfter is split_after: a comma list to ids, whitespace-trimmed, empty items and duplicates dropped, order kept.
func splitAfter(raw string) []string {
	var out []string
	seen := map[string]bool{}
	for _, d := range strings.Split(raw, ",") {
		d = pyStrip(d)
		if d != "" && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// taskDepsOf is json.loads(row["depends_on"] or "[]") for task id; a missing task has no dependencies. overlay
// holds ids whose dependencies are not stored yet. A value that is not a JSON array of strings makes Python
// crash or misbehave, so Go hands the command over (before any write).
func taskDepsOf(c *ctx, id string, overlay map[string][]string) ([]string, error) {
	if d, ok := overlay[id]; ok {
		return d, nil
	}
	var raw sql.NullString
	err := c.db.QueryRow("SELECT depends_on FROM tasks WHERE id=?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if raw.String == "" {
		return nil, nil
	}
	var v []any
	if err := json.Unmarshal([]byte(raw.String), &v); err != nil {
		return nil, errDelegate
	}
	out := make([]string, 0, len(v))
	for _, x := range v {
		s, ok := x.(string)
		if !ok {
			return nil, errDelegate
		}
		out = append(out, s)
	}
	return out, nil
}

// depCycle is dep_cycle: the path tid -> ... -> tid if making tid depend on deps closes a loop, else nil.
func depCycle(c *ctx, tid string, deps []string) ([]string, error) {
	seen := map[string]bool{}
	var walk func(n string, path []string) ([]string, error)
	walk = func(n string, path []string) ([]string, error) {
		if n == tid {
			return path, nil
		}
		if seen[n] {
			return nil, nil
		}
		seen[n] = true
		ds, err := taskDepsOf(c, n, nil)
		if err != nil {
			return nil, err
		}
		for _, d := range ds {
			found, err := walk(d, append(append([]string{}, path...), d))
			if err != nil || found != nil {
				return found, err
			}
		}
		return nil, nil
	}
	for _, d := range deps {
		found, err := walk(d, []string{tid, d})
		if err != nil || found != nil {
			return found, err
		}
	}
	return nil, nil
}

// checkAfter is check_after: exit 2 unless every dep exists in the goal, is not tid itself and closes no loop.
func checkAfter(c *ctx, tid, goalID string, deps []string) error {
	for _, d := range deps {
		if d == tid {
			return dieErr(2, tid+" cannot depend on itself")
		}
		var one int
		err := c.db.QueryRow("SELECT 1 FROM tasks WHERE id=? AND goal_id=?", d, goalID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return dieErr(2, "--after: no task "+d+" in goal "+goalID)
		}
		if err != nil {
			return err
		}
	}
	cyc, err := depCycle(c, tid, deps)
	if err != nil {
		return err
	}
	if cyc != nil {
		return dieErr(2, "dependency cycle: "+strings.Join(cyc, " -> "))
	}
	return nil
}

// taskOrdered is task_ordered: true when a depends on b or b on a, directly or through other tasks. overlay supplies
// the dependencies of a task that is not stored yet.
func taskOrdered(c *ctx, a, b string, overlay map[string][]string) (bool, error) {
	reaches := func(src, dst string) (bool, error) {
		seen, todo := map[string]bool{}, []string{src}
		for len(todo) > 0 {
			n := todo[len(todo)-1]
			todo = todo[:len(todo)-1]
			ds, err := taskDepsOf(c, n, overlay)
			if err != nil {
				return false, err
			}
			for _, d := range ds {
				if d == dst {
					return true, nil
				}
				if !seen[d] {
					seen[d] = true
					todo = append(todo, d)
				}
			}
		}
		return false, nil
	}
	if ok, err := reaches(a, b); ok || err != nil {
		return ok, err
	}
	return reaches(b, a)
}
