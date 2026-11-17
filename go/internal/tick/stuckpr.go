package tick

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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
	if err != nil {
		// One bad PR must not fail the whole tick: report each error and let the next tick retry.
		fmt.Fprintf(os.Stderr, "stuck_pr: %v\n", err)
	}
	return nil
}

func stuckPRClock() (float64, error) {
	return float64(time.Now().UnixNano()) / 1e9, nil
}

func stuckPRView(ctx context.Context, prURL string) (*stuckpr.PRView, error) {
	_ = ctx
	if !stuckpr.ValidPRURL(prURL) {
		return nil, fmt.Errorf("pr_url %q is not a github.com/volekerb/hivemill pull URL", prURL)
	}
	r, err := ghx.Run(nil, "", stuckPRGhTimeout, "pr", "view", prURL, "--json", "number,state,headRefOid,mergeable,statusCheckRollup,url,commits")
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

// stuckPRRelaunch restarts one existing agent (factory relaunch); a var so tests stay at the process boundary.
var stuckPRRelaunch = func(name string) error {
	out, err := exec.Command("factory", "relaunch", name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("factory relaunch %s: %w: %s", name, err, firstLine(string(out)))
	}
	return nil
}

// stuckPRWake restarts the task's own earlier coder. It never starts a new coder:
// with no coder on record it mails the team lead to decide.
func stuckPRWake(ctx context.Context, db *sql.DB, task stuckpr.TaskRow, pr *stuckpr.PRView) error {
	var prior string
	err := db.QueryRowContext(ctx, `SELECT name FROM agents WHERE task_id=? AND role='coder' ORDER BY started DESC LIMIT 1`, task.ID).Scan(&prior)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if prior != "" {
		return stuckPRRelaunch(prior)
	}
	to := "director"
	if task.TeamID != "" {
		to = "lead:" + task.TeamID
	}
	subject := fmt.Sprintf("stuck PR, no coder on record: %s PR #%d", task.ID, pr.Number)
	body := fmt.Sprintf("CI red on %s (%s); no coder agent is recorded for %s. Spawn a fix coder or block the task.", pr.URL, strings.Join(pr.FailedChecks, ", "), task.ID)
	_, err = db.ExecContext(ctx, `INSERT INTO messages(sender,recipient,kind,subject,body,goal_id,task_id,created) VALUES(?,?,?,?,?,?,?,?)`,
		"tick", to, "request", subject, body, task.GoalID, task.ID, time.Now().UTC().Format(time.RFC3339))
	return err
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
