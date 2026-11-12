package cli

import (
	"database/sql"
	"errors"
	"fmt"
	"unicode/utf8"
)

// `factory plan <G> <body>`: the Go port of cmd_plan in bin/factory_cli.py. Registered with action "" (argv[1] is the
// goal id, not an action word).

func init() {
	register("plan", "", handler{parse: parsePlan, run: runPlan})
}

type planCmd struct{ goal, body string }

func parsePlan(rest []string) (any, bool) {
	if len(rest) != 2 {
		return nil, false
	}
	for _, a := range rest {
		if !utf8.ValidString(a) || !gwDashValue(a) {
			return nil, false
		}
	}
	return planCmd{rest[0], rest[1]}, true
}

func runPlan(c *ctx, p any) (int, error) {
	pc := p.(planCmd)
	if _, err := requireRole(c, "pm", "lead", "director"); err != nil {
		return 0, err
	}
	// The plans.goal_id foreign key makes Python crash on an unknown goal, after reading the body: delegate before
	// stdin is touched so Python sees it whole.
	var g string
	if err := c.db.QueryRow("SELECT id FROM goals WHERE id=?", pc.goal).Scan(&g); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, errDelegate
		}
		return 0, err
	}
	body, err := readText(c, &pc.body, false)
	if err != nil {
		return 0, err
	}
	if *body == "" {
		return 0, dieErr(1, "empty plan")
	}
	var mv sql.NullInt64
	if err := c.db.QueryRow("SELECT max(version) FROM plans WHERE goal_id=?", pc.goal).Scan(&mv); err != nil {
		return 0, err
	}
	v := mv.Int64 + 1
	if _, err := c.exec("INSERT INTO plans(goal_id,version,body,author,created) VALUES(?,?,?,?,?)",
		pc.goal, v, *body, c.who, eventTS(c)); err != nil {
		return 0, err
	}
	if _, err := c.exec("UPDATE goals SET updated=?, status=CASE WHEN status='proposed' THEN 'planning' ELSE status END WHERE id=?",
		eventTS(c), pc.goal); err != nil {
		return 0, err
	}
	if err := event(c, "plan", pc.goal, fmt.Sprintf("v%d", v), nil); err != nil {
		return 0, err
	}
	fmt.Fprintf(c.out, "%s plan v%d\n", pc.goal, v)
	return 0, nil
}
