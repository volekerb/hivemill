package hm

import (
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// A re-filed task is a new id for a task that was cancelled or failed. Tasks that depend on the old id would never
// start, so RepointRefiles moves them to the new one.

// refileTitle is the planner's title marker, read when a task was added without --refile-of.
var refileTitle = regexp.MustCompile(`(?i)\(re-file of (T-\d+)`)

// SetRefileOf records that task was filed again for the task `of`.
func (s *SQLite) SetRefileOf(task, of string) error {
	_, err := s.db.Exec(`INSERT INTO hm_refile(task,refile_of) VALUES(?,?)
		ON CONFLICT(task) DO UPDATE SET refile_of=excluded.refile_of`, task, of)
	return err
}

type refileRow struct {
	id, goal, project, title, status, depends, of string
}

func (s *SQLite) refileRows() ([]refileRow, error) {
	rows, err := s.db.Query(`SELECT t.id, t.goal_id, COALESCE(g.project_id,''), t.title, t.status, t.depends_on, COALESCE(f.refile_of,'')
		FROM tasks t LEFT JOIN goals g ON g.id=t.goal_id LEFT JOIN hm_refile f ON f.task=t.id ORDER BY t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []refileRow
	for rows.Next() {
		var x refileRow
		if err := rows.Scan(&x.id, &x.goal, &x.project, &x.title, &x.status, &x.depends, &x.of); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func taskNum(id string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(id, "T-"))
	return n
}

// RepointRefiles replaces, in every todo or draft task, a dependency on a cancelled or failed task with its newest
// re-file that is neither (following re-files of re-files). It is idempotent and logs each replacement as
// task.depends_repointed.
func (r *Runner) RepointRefiles() error {
	s, err := r.sq()
	if err != nil {
		return err
	}
	tasks, err := s.refileRows()
	if err != nil {
		return err
	}
	runs, err := s.Runs()
	if err != nil {
		return err
	}
	failed := map[string]bool{}
	for _, x := range runs {
		// a run held for the owner may still merge
		failed[x.Task] = x.State == Failed && !heldForOwner(&x)
	}
	dead, refiles, origin, flagged := map[string]bool{}, map[string][]string{}, map[string]string{}, map[string]bool{}
	for _, t := range tasks {
		// a failed run leaves its task blocked; a task set back to todo or done after it is not failed any more
		dead[t.id] = t.status == "cancelled" || (failed[t.id] && t.status == "blocked")
		flagged[t.of] = true
	}
	for _, t := range tasks {
		of := t.of
		// the flag beats titles: on a split, only the flagged piece takes the dependents
		if m := refileTitle.FindStringSubmatch(t.title); of == "" && m != nil && !flagged[strings.ToUpper(m[1])] {
			of = strings.ToUpper(m[1])
		}
		// only a later id re-files an earlier one, so the chains cannot loop
		if of != "" && taskNum(t.id) > taskNum(of) {
			refiles[of] = append(refiles[of], t.id)
			origin[t.id] = of
		}
	}
	var successor func(string) string
	successor = func(old string) string {
		for _, id := range slices.Backward(refiles[old]) {
			if !dead[id] {
				return id
			}
			if next := successor(id); next != "" {
				return next
			}
		}
		return ""
	}
	for _, t := range tasks {
		if t.status != "todo" && t.status != "draft" {
			continue
		}
		var deps []string
		if json.Unmarshal([]byte(t.depends), &deps) != nil {
			continue
		}
		next, moved := []string{}, [][2]string{}
		for _, d := range deps {
			// a dead re-file with no successor of its own: another re-file of what it replaced takes over
			for o := d; dead[d] && o != ""; o = origin[o] {
				if to := successor(o); to != "" {
					if to != t.id {
						moved = append(moved, [2]string{d, to})
						d = to
					}
					break
				}
			}
			if !slices.Contains(next, d) {
				next = append(next, d)
			}
		}
		if len(moved) == 0 {
			continue
		}
		b, _ := json.Marshal(next)
		// compare and swap: a concurrent change of the row wins, and the next call sees it
		res, err := s.db.Exec(`UPDATE tasks SET depends_on=?, updated=? WHERE id=? AND depends_on=? AND status IN ('todo','draft')`,
			string(b), s.now(), t.id, t.depends)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue
		}
		for _, m := range moved {
			r.printf("%s: depends %s -> %s (re-filed)\n", t.id, m[0], m[1])
			_ = r.Log.Emit("task.depends_repointed", nil, Event{TaskID: t.id, GoalID: t.goal, Project: t.project, Data: map[string]any{"from": m[0], "to": m[1]}})
		}
	}
	return nil
}

// repointRefiles is RepointRefiles for a runner step, where an error is only reported.
func (r *Runner) repointRefiles() {
	if err := r.RepointRefiles(); err != nil {
		r.printf("re-point re-filed tasks: %v\n", err)
	}
}
