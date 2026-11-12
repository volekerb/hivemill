package cli

import (
	"database/sql"
	"fmt"
	"strings"

	"factory/go/internal/identity"
)

// factory lesson list and lesson show (cmd_lesson in bin/factory_cli.py). Every other lesson action, and every
// argv shape argparse would treat differently (--flag=value, abbreviations, repeated flags, empty values, an id on
// list, no id on show), parses to ok=false and runs in Python. The worker refusal in cmd_lesson only applies to
// the write actions, so these two need only RequireKnown.

// lessonRoles is ROLES and lessonStatuses is LESSON_STATUSES of bin/factory_cli.py (TestLessonLiteralsMatchPython).
var (
	lessonRoles    = []string{"boss", "director", "lead", "pm", "coder", "qa", "reviewer", "judge", "cleaner", "staff-engineer", "researcher", "scout", "retro", "evaluator"}
	lessonStatuses = []string{"open", "applied", "verified"}
)

func init() {
	register("lesson", "list", handler{parse: parseLessonList, run: runLessonList})
	register("lesson", "show", handler{parse: parseLessonShow, run: runLessonShow})
}

type lessonListCmd struct{ role, status string }

// plainArg is a value argparse takes literally: non-empty and not option-looking.
func plainArg(s string) bool { return s != "" && !strings.HasPrefix(s, "-") }

func parseLessonList(rest []string) (any, bool) {
	var c lessonListCmd
	seenRole, seenStatus := false, false
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "--role", "--status":
			if i+1 >= len(rest) || !plainArg(rest[i+1]) {
				return nil, false
			}
			if rest[i] == "--role" {
				if seenRole {
					return nil, false
				}
				seenRole, c.role = true, rest[i+1]
			} else {
				if seenStatus {
					return nil, false
				}
				seenStatus, c.status = true, rest[i+1]
			}
			i++
		default:
			return nil, false
		}
	}
	return c, true
}

func parseLessonShow(rest []string) (any, bool) {
	if len(rest) != 1 || !plainArg(rest[0]) {
		return nil, false
	}
	return rest[0], true
}

func inList(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func runLessonList(c *ctx, p any) (int, error) {
	a := p.(lessonListCmd)
	if a.role != "" && a.role != "all" && !inList(lessonRoles, a.role) {
		return 0, &identity.ExitError{Code: 2, Msg: "unknown role " + a.role + "; valid: " + strings.Join(append(append([]string{}, lessonRoles...), "all"), ", ")}
	}
	q := "SELECT id,status,roles,links,recurrences,title FROM (SELECT l.*, (SELECT COUNT(*) FROM lesson_links k WHERE k.lesson_id=l.id) AS links FROM lessons l"
	var args []any
	if a.status != "" {
		if !inList(lessonStatuses, a.status) {
			return 0, &identity.ExitError{Code: 2, Msg: "--status must be one of " + strings.Join(lessonStatuses, ", ")}
		}
		q += " WHERE l.status=?"
		args = append(args, a.status)
	}
	rows, err := c.db.Query(q+" ORDER BY l.id)", args...)
	if err != nil {
		return 0, errDelegate
	}
	defer rows.Close()
	var out [][]string
	for rows.Next() {
		var v [6]any
		if err := rows.Scan(&v[0], &v[1], &v[2], &v[3], &v[4], &v[5]); err != nil {
			return 0, errDelegate
		}
		r := make([]string, 6)
		for i := range v {
			if r[i], err = cell(v[i]); err != nil {
				return 0, err
			}
		}
		if a.role != "" {
			have := strings.Split(r[2], ",")
			if !inList(have, a.role) && !inList(have, "all") {
				continue
			}
		}
		out = append(out, r)
	}
	if rows.Err() != nil {
		return 0, errDelegate
	}
	Table(c.out, []string{"id", "status", "roles", "links", "recurrences", "title"}, out)
	return 0, nil
}

// pyStr is str(value) for a column that may be NULL: None prints as "None".
func pyStr(v any) (string, error) {
	if v == nil {
		return "None", nil
	}
	return cell(v)
}

func scanAny(rows *sql.Rows, n int) ([]any, error) {
	v := make([]any, n)
	ptr := make([]any, n)
	for i := range v {
		ptr[i] = &v[i]
	}
	if err := rows.Scan(ptr...); err != nil {
		return nil, errDelegate
	}
	return v, nil
}

func runLessonShow(c *ctx, p any) (int, error) {
	id := p.(string)
	rows, err := c.db.Query("SELECT * FROM lessons WHERE id=?", id)
	if err != nil {
		return 0, errDelegate
	}
	cols, err := rows.Columns()
	if err != nil {
		rows.Close()
		return 0, errDelegate
	}
	if !rows.Next() {
		rows.Close()
		return 0, &identity.ExitError{Code: 2, Msg: "no lesson " + id}
	}
	vals, err := scanAny(rows, len(cols))
	rows.Close()
	if err != nil {
		return 0, err
	}
	r := map[string]string{}
	for i, k := range cols {
		s, err := cell(vals[i])
		if err != nil {
			return 0, err
		}
		r[k] = s // NULL is "", which the loops below skip exactly like Python's None
	}
	for _, k := range cols {
		if r[k] != "" && k != "what" && k != "rule" && k != "evidence" {
			fmt.Fprintf(c.out, "%s: %s\n", k, r[k])
		}
	}
	for _, k := range []string{"what", "rule", "evidence"} {
		if r[k] != "" {
			fmt.Fprintf(c.out, "\n## %s\n%s\n", k, r[k])
		}
	}
	fmt.Fprint(c.out, "\n## links\n")
	lr, err := c.db.Query("SELECT created,task_id,pr_url,via,merged_at,author FROM lesson_links WHERE lesson_id=? ORDER BY id", id)
	if err != nil {
		return 0, errDelegate
	}
	defer lr.Close()
	n := 0
	for lr.Next() {
		v, err := scanAny(lr, 6)
		if err != nil {
			return 0, err
		}
		created, err := pyStr(v[0])
		if err != nil {
			return 0, err
		}
		line := "- " + created + " "
		var parts []string
		for i, k := range []string{"task_id", "pr_url", "via", "merged_at", "author"} {
			s, err := cell(v[i+1])
			if err != nil {
				return 0, err
			}
			if s != "" {
				parts = append(parts, k+"="+s)
			}
		}
		fmt.Fprintln(c.out, line+strings.Join(parts, " "))
		n++
	}
	if lr.Err() != nil {
		return 0, errDelegate
	}
	if n == 0 {
		fmt.Fprintln(c.out, "_none_")
	}
	fmt.Fprint(c.out, "\n## history\n")
	er, err := c.db.Query("SELECT ts,actor,action,detail FROM events WHERE entity='lesson' AND entity_id=? ORDER BY id", id)
	if err != nil {
		return 0, errDelegate
	}
	defer er.Close()
	for er.Next() {
		v, err := scanAny(er, 4)
		if err != nil {
			return 0, err
		}
		var s [4]string
		for i := range s {
			if i == 3 {
				s[i], err = cell(v[i])
			} else {
				s[i], err = pyStr(v[i])
			}
			if err != nil {
				return 0, err
			}
		}
		line := "- " + s[0] + " " + s[1] + " " + s[2]
		if s[3] != "" {
			line += ": " + s[3]
		}
		fmt.Fprintln(c.out, line)
	}
	if er.Err() != nil {
		return 0, errDelegate
	}
	return 0, nil
}
