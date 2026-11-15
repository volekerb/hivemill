package cli

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"factory/go/internal/scout"
)

// scout list | show | runs (Python cmd_scout). fetch, pending, triage and report need the gh/readme/send paths that
// move with the P4-L dispatch: their argv shapes are not parsed here, so Python answers them.
// Registered for P4-L: unreachable until "scout" is added to cli_go_cmds.

func init() {
	register("scout", "list", handler{parse: parseScoutList, run: runScoutList})
	register("scout", "show", handler{parse: parseScoutShow, run: runScoutShow})
	register("scout", "runs", handler{parse: parseScoutRuns, run: runScoutRuns})
}

type scoutListCmd struct {
	onlyNew          bool
	rec, risk, since string
}

type scoutShowCmd struct{ id string }

func parseScoutList(rest []string) (any, bool) {
	var c scoutListCmd
	ok := flagParser{vals: map[string]*string{"--rec": &c.rec, "--risk": &c.risk, "--since": &c.since},
		bools: map[string]*bool{"--new": &c.onlyNew}}.parse(rest)
	return c, ok
}

func parseScoutShow(rest []string) (any, bool) {
	if len(rest) != 1 || rest[0] == "" || rest[0][0] == '-' {
		return nil, false
	}
	return scoutShowCmd{id: rest[0]}, true
}

func parseScoutRuns(rest []string) (any, bool) { return nil, len(rest) == 0 }

// scoutGate is the shared front of list/show/runs: a worker other than the scout is Python's to refuse (the stub
// exemption is not reproduced), then the role check.
func scoutGate(c *ctx) error {
	w, del := callerIsWorker(c)
	if del || (w != "" && w != "scout") {
		return errDelegate
	}
	_, err := requireRole(c, "scout", "director", "lead", "boss")
	return err
}

func runScoutList(c *ctx, p any) (int, error) {
	a := p.(scoutListCmd)
	if err := scoutGate(c); err != nil {
		return 0, err
	}
	rows, err := scout.ListCandidates(c.db, a.onlyNew, a.rec, a.risk, a.since)
	if err != nil {
		var se *scout.ScoutError
		if errors.As(err, &se) {
			return 0, dieErr(2, se.Msg)
		}
		return 0, err
	}
	for _, r := range rows {
		spd := "-"
		if r.StarsPerDay.Valid {
			spd = fmt.Sprintf("%+.1f/d", r.StarsPerDay.Float64)
		}
		stars := "-"
		if r.Stars.Valid {
			stars = fmt.Sprint(r.Stars.Int64)
		}
		fmt.Fprintf(c.out, "%s %-9s %-5s %s* %s risk=%s %s\n", r.ID, r.Status, orDash(r.Rec.String), stars, spd,
			orDash(r.InstallRisk.String), r.Key)
	}
	if len(rows) == 0 {
		fmt.Fprintln(c.out, "(none)")
	}
	return 0, nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// pyStrOfAny is str(v) for a value scanned from SQLite.
func pyStrOfAny(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case []byte:
		return string(x)
	case string:
		return x
	case float64:
		return pyFloatRepr(x)
	case bool:
		if x {
			return "True"
		}
		return "False"
	}
	return fmt.Sprint(v)
}

func runScoutShow(c *ctx, p any) (int, error) {
	a := p.(scoutShowCmd)
	if err := scoutGate(c); err != nil {
		return 0, err
	}
	rows, err := c.db.Query("SELECT * FROM scout_candidates WHERE id=?", a.id)
	if err != nil {
		return 0, err
	}
	cols, err := rows.Columns()
	if err != nil {
		rows.Close()
		return 0, err
	}
	if !rows.Next() {
		rows.Close()
		if err := rows.Err(); err != nil {
			return 0, err
		}
		return 0, dieErr(2, "no candidate "+a.id)
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for i, k := range cols {
		fmt.Fprintf(c.out, "%s: %s\n", k, pyStrOfAny(vals[i]))
	}
	fmt.Fprintln(c.out, "## star snapshots")
	if err := scoutRows(c.db, "SELECT ts, stars FROM scout_stars WHERE candidate_id=? ORDER BY julianday(ts), ts", []any{a.id}, 2,
		func(v []any) { fmt.Fprintf(c.out, "- %s %s\n", pyStrOfAny(v[0]), pyStrOfAny(v[1])) }); err != nil {
		return 0, err
	}
	fmt.Fprintln(c.out, "## triage")
	err = scoutRows(c.db, "SELECT created, author, recommendation, target_roles, eval_suggested, purpose, overlap, rationale "+
		"FROM scout_triage WHERE candidate_id=? ORDER BY id", []any{a.id}, 8, func(v []any) {
		fmt.Fprintf(c.out, "- %s %s rec=%s roles=%s eval_suggested=%s\n  purpose: %s\n  overlap: %s\n  why: %s\n", pyStrOfAny(v[0]),
			pyStrOfAny(v[1]), pyStrOfAny(v[2]), pyStrOfAny(v[3]), pyStrOfAny(v[4]), pyStrOfAny(v[5]), pyStrOfAny(v[6]), pyStrOfAny(v[7]))
	})
	return 0, err
}

// scoutRows runs a query and hands each row (n columns) to fn.
func scoutRows(db *sql.DB, q string, args []any, n int, fn func([]any)) error {
	rows, err := db.Query(q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		vals := make([]any, n)
		ptrs := make([]any, n)
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		fn(vals)
	}
	return rows.Err()
}

func runScoutRuns(c *ctx, _ any) (int, error) {
	if err := scoutGate(c); err != nil {
		return 0, err
	}
	n := 0
	err := scoutRows(c.db, "SELECT slot, status, agent, new_candidates, triaged, tokens, started, ended, stop_reason "+
		"FROM scout_runs ORDER BY id DESC LIMIT 20", nil, 9, func(v []any) {
		n++
		tokens := "-"
		if v[5] != nil {
			tokens = pyStrOfAny(v[5])
		}
		line := fmt.Sprintf("%s %-8s agent=%s new=%s triaged=%s tokens=%s started=%s ended=%s %s", pyStrOfAny(v[0]), pyStrOfAny(v[1]),
			orDash(strOrEmpty(v[2])), pyStrOfAny(v[3]), pyStrOfAny(v[4]), tokens, orDash(strOrEmpty(v[6])), orDash(strOrEmpty(v[7])),
			strOrEmpty(v[8]))
		fmt.Fprintln(c.out, strings.TrimRightFunc(line, pyIsSpace))
	})
	if err != nil {
		return 0, err
	}
	if n == 0 {
		fmt.Fprintln(c.out, "(none)")
	}
	return 0, nil
}

// strOrEmpty is `value or ”` for a text column.
func strOrEmpty(v any) string {
	if v == nil {
		return ""
	}
	return pyStrOfAny(v)
}
