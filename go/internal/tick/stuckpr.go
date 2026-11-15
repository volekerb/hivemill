package tick

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"factory/go/internal/ghx"
	"factory/go/internal/stuckpr"
)

const stuckPRGhTimeout = 15 * time.Second

func init() {
	for _, u := range walkUnits() {
		if u.Name == "stuck_pr" {
			registerGo("stuck_pr", stuckPRUnit)
			return
		}
	}
}

func stuckPRUnit(u *UnitCtx) error {
	if u.DB == nil {
		return fmt.Errorf("stuck_pr: no database")
	}
	_, err := stuckpr.Run(context.Background(), stuckpr.Deps{
		DB:        u.DB,
		Now:       stuckPRClock,
		ViewPR:    stuckPRView,
		WakeCoder: stuckPRWake,
		Escalate:  stuckpr.DefaultEscalate,
	})
	return err
}

func stuckPRClock() (float64, error) {
	return float64(time.Now().UnixNano()) / 1e9, nil
}

func stuckPRView(ctx context.Context, prURL string) (*stuckpr.PRView, error) {
	_ = ctx
	r, err := ghx.Run(nil, "", stuckPRGhTimeout, "pr", "view", prURL, "--json", "number,state,headRefOid,mergeable,statusCheckRollup,url")
	if err != nil {
		return nil, err
	}
	if r.ExitCode != 0 {
		return nil, fmt.Errorf("gh pr view: %s", firstLine(r.Stderr))
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(r.Stdout), &m); err != nil {
		return nil, fmt.Errorf("gh pr view JSON: %w", err)
	}
	return stuckpr.ParsePRView(m)
}

func stuckPRWake(ctx context.Context, db *sql.DB, task stuckpr.TaskRow, pr *stuckpr.PRView) error {
	_ = ctx
	var prior string
	err := db.QueryRow(`SELECT name FROM agents WHERE task_id=? AND role='coder' ORDER BY started DESC LIMIT 1`, task.ID).Scan(&prior)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if prior == "" {
		prior = "cursor-coder-" + strings.ToLower(task.ID)
	}
	subject := fmt.Sprintf("stuck PR wake: %s PR #%d", task.ID, pr.Number)
	body := fmt.Sprintf("CI red on %s; wake %s to fix (%s).", pr.URL, prior, strings.Join(pr.FailedChecks, ", "))
	_, err = db.Exec(`INSERT INTO messages(sender,recipient,kind,subject,body,goal_id,task_id,created) VALUES(?,?,?,?,?,?,?,?)`,
		"tick", prior, "request", subject, body, task.GoalID, task.ID, time.Now().UTC().Format(time.RFC3339))
	return err
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
