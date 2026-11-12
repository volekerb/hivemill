package cli

import (
	"factory/go/internal/identity"
	"fmt"
	"time"
)

// scorecard [--days N] (Python cmd_scorecard, line 6547). Only that argv shape runs here; everything else is Python's.

func init() {
	register("scorecard", "", handler{parse: parseScorecard, run: runScorecard})
}

type scorecardCmd struct{ days string }

func parseScorecard(rest []string) (any, bool) {
	c := scorecardCmd{days: "1"}
	ok := flagParser{vals: map[string]*string{"--days": &c.days}}.parse(rest)
	return c, ok
}

var scorecardCols = []string{"team", "status", "tasks", "done", "merged", "rejections", "retries", "score", "lead"}

func runScorecard(c *ctx, p any) (int, error) {
	a := p.(scorecardCmd)
	days, ok, err := pyIntStr(a.days)
	if err != nil {
		return 0, err
	}
	if !ok {
		days = 0
	}
	if days < 1 {
		return 0, &identity.ExitError{Code: 2, Msg: fmt.Sprintf("--days must be an integer >= 1, got %s", a.days)}
	}
	if days > 100000 {
		return 0, errDelegate // Python's timedelta arithmetic may overflow
	}
	loc, err := sbLocation(c.env.Getenv)
	if err != nil {
		return 0, err
	}
	now, err := sbClock(c.env.Getenv, c.env.Now, loc)
	if err != nil {
		return 0, err
	}
	rows, err := sbScoreRows(c.db)
	if err != nil {
		return 0, err
	}
	cells := make([][]string, len(rows))
	for i, r := range rows {
		cells[i] = []string{r.Team, r.Status, r.Tasks, r.Done, r.Merged, r.Rejections, r.Retries, r.Score, r.Lead}
	}
	Table(c.out, scorecardCols, cells)
	row, err := sbDirectorRow(c.db, now.Add(-time.Duration(days)*24*time.Hour), now, int(days)*24, loc)
	if err != nil {
		return 0, err
	}
	fmt.Fprintf(c.out, "\n%s\n", sbDirectorLine(row))
	return 0, nil
}
