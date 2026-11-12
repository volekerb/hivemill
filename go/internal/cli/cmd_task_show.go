package cli

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// `factory task show <ID> [--full]`: the Go port of cmd_task action "show". Every other argv form runs in Python.

func init() {
	register("task", "show", handler{parse: parseTaskShow, run: runTaskShow})
}

type taskShowCmd struct {
	id   string
	full bool
}

func parseTaskShow(rest []string) (any, bool) {
	var c taskShowCmd
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

// taskPyStr is str(v) of a SQLite value as an f-string prints it: None for NULL; a float or blob is Python's to print.
func taskPyStr(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "None", nil
	case string:
		if !utf8.ValidString(x) {
			return "", errDelegate
		}
		return x, nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	}
	return "", errDelegate
}

// truthy is Python truthiness of a SQLite value.
func truthy(v any) (bool, error) {
	switch x := v.(type) {
	case nil:
		return false, nil
	case string:
		return x != "", nil
	case int64:
		return x != 0, nil
	}
	return false, errDelegate
}

func scanRows(rs *sql.Rows) (cols []string, rows [][]any, err error) {
	defer rs.Close()
	if cols, err = rs.Columns(); err != nil {
		return nil, nil, errDelegate
	}
	for rs.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			return nil, nil, errDelegate
		}
		rows = append(rows, vals)
	}
	if rs.Err() != nil {
		return nil, nil, errDelegate
	}
	return cols, rows, nil
}

func runTaskShow(c *ctx, p any) (int, error) {
	cmd := p.(taskShowCmd)
	rs, err := c.db.Query("SELECT * FROM tasks WHERE id=?", cmd.id)
	if err != nil {
		return 0, errDelegate
	}
	cols, trows, err := scanRows(rs)
	if err != nil {
		return 0, err
	}
	if len(trows) == 0 {
		return 0, fmt.Errorf("no task %s", cmd.id)
	}
	t := map[string]any{}
	for i, k := range cols {
		t[k] = trows[0][i]
	}
	for _, k := range cols {
		v := t[k]
		if v == nil || k == "spec" || k == "acceptance" {
			continue
		}
		if s, ok := v.(string); ok && (s == "" || s == "[]") {
			continue
		}
		if k == "waits" && t["status"] != "blocked" {
			continue
		}
		switch k {
		case "sandbox_required":
			ok, err := truthy(v)
			if err != nil {
				return 0, err
			}
			if !ok {
				continue
			}
		case "judge_hold_reason":
			switch x := v.(type) {
			case string:
				if pyStrip(x) == "" {
					continue
				}
			case int64:
				if x == 0 {
					continue // (0 or "").strip() is ""
				}
				return 0, errDelegate // Python raises AttributeError
			default:
				return 0, errDelegate
			}
		}
		s, err := taskPyStr(v)
		if err != nil {
			return 0, err
		}
		fmt.Fprintf(c.out, "%s: %s\n", k, s)
	}
	for _, k := range []string{"spec", "acceptance"} {
		ok, err := truthy(t[k])
		if err != nil {
			return 0, err
		}
		if ok {
			s, err := taskPyStr(t[k])
			if err != nil {
				return 0, err
			}
			fmt.Fprintf(c.out, "\n## %s\n%s\n", k, s)
		}
	}
	if err := showVerdicts(c, cmd.id); err != nil {
		return 0, err
	}
	rs, err = c.db.Query("SELECT ts,actor,action,detail FROM events WHERE entity='task' AND entity_id=? ORDER BY id", cmd.id)
	if err != nil {
		return 0, errDelegate
	}
	_, notes, err := scanRows(rs)
	if err != nil {
		return 0, err
	}
	fmt.Fprint(c.out, "\n## history\n")
	compact, del := compactView(c, cmd.full)
	if del {
		return 0, errDelegate
	}
	dropped := 0
	if compact {
		var others []int
		for i, n := range notes {
			if n[2] != "note" {
				others = append(others, i)
			}
		}
		keep := map[int]bool{}
		for _, i := range others[max(0, len(others)-5):] {
			keep[i] = true
		}
		dropped = len(others) - len(keep)
		var kept [][]any
		for i, n := range notes {
			if n[2] == "note" || keep[i] {
				kept = append(kept, n)
			}
		}
		notes = kept
	}
	for _, n := range notes {
		var f [3]string
		for i := range f {
			if f[i], err = taskPyStr(n[i]); err != nil {
				return 0, err
			}
		}
		line := fmt.Sprintf("- %s %s %s", f[0], f[1], f[2])
		if ok, err := truthy(n[3]); err != nil {
			return 0, err
		} else if ok {
			d, err := taskPyStr(n[3])
			if err != nil {
				return 0, err
			}
			line += ": " + d
		}
		fmt.Fprintln(c.out, line)
	}
	if dropped > 0 {
		fmt.Fprintf(c.out, "(%d earlier events: factory task show %s --full)\n", dropped, cmd.id)
	}
	return 0, nil
}

func showVerdicts(c *ctx, id string) error {
	rs, err := c.db.Query("SELECT role,verdict,head_sha,author,created,summary FROM verdicts WHERE task_id=? ORDER BY id", id)
	if err != nil {
		return errDelegate
	}
	_, vs, err := scanRows(rs)
	if err != nil {
		return err
	}
	if len(vs) == 0 {
		return nil
	}
	fmt.Fprint(c.out, "\n## verdicts\n")
	for _, v := range vs {
		verdict, ok := v[1].(string)
		if !ok || !isASCII(verdict) { // Python's upper() and Go's differ outside ASCII
			return errDelegate
		}
		sha := ""
		switch x := v[2].(type) {
		case nil:
		case string:
			if !utf8.ValidString(x) {
				return errDelegate
			}
			sha = pyTrunc(x, 8)
		default:
			return errDelegate
		}
		var f [4]string // role, author, created, summary
		for i, idx := range []int{0, 3, 4, 5} {
			if f[i], err = taskPyStr(v[idx]); err != nil {
				return err
			}
		}
		fmt.Fprintf(c.out, "- %s %s %s @%s by %s: %s\n", f[2], f[0], strings.ToUpper(verdict), sha, f[1], f[3])
	}
	return nil
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}
