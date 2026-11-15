// Package holdexpiry clears a manual judge hold (tasks.judge_hold_reason) that sits on a PR with reviewer PASS and
// QA PASS for longer than the expiry time, unless the hold text names a valid boss decision. It only reads and
// writes the factory database: it starts no agent, merges nothing and calls no external tool. The rule is in
// docs/judge-hold-expiry.md.
package holdexpiry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// ConfigKey is the config row with the expiry time in minutes ("0" turns the rule off).
	ConfigKey = "judge_hold_expiry_min"
	// DefaultExpiryMin is used when the row is missing or not a non-negative whole number.
	DefaultExpiryMin = 120

	// ActionSet is the event `factory task update --judge-hold` writes; its ts is when the hold started.
	ActionSet = "judge-hold-set"
	// ActionClear is the same event `--judge-hold-clear` writes; log_task_judge_hold in bin/factory_cli.py reads it
	// to reset its dedupe, so the name must not change.
	ActionClear = "judge-hold-clear"
	// ActionExpired is the event only this package writes (JSON detail, see expiredDetail).
	ActionExpired = "judge-hold-expired"

	actor      = "tick"
	defaultOrg = "claude" // goals.org default (scout/orgs.py DEFAULT_ORG)
	tsLayout   = "2006-01-02T15:04:05-07:00"
	reasonClip = 200
)

// markerRE finds the boss approvals in a hold text: "boss-approved M<digits>", case-sensitive.
var markerRE = regexp.MustCompile(`boss-approved M([0-9]+)`)

// prNumberRE reads the PR number off a pull request URL.
var prNumberRE = regexp.MustCompile(`/pull/([0-9]+)/?$`)

// Deps is what Run needs. Now is the clock (nil means time.Now).
type Deps struct {
	DB  *sql.DB
	Now func() time.Time
}

// Expired is one hold Run cleared.
type Expired struct {
	Task    string
	PR      int // 0 when the PR URL carries no number
	PRURL   string
	HeldMin int
	SetBy   string
	Reason  string
	Marker  string // "none" or "invalid:M<id>"
	To      string // recipient of the report
}

// Skip is one PASS/PASS hold Run could not decide; the hold is kept.
type Skip struct{ Task, Why string }

// Result is what one Run did.
type Result struct {
	ExpiryMin int // 0: the rule is off
	Expired   []Expired
	Skipped   []Skip
}

type task struct{ id, goal, prURL, reason string }

type setEvent struct {
	ts    string
	actor string
}

// Run applies the rule once. A task whose transaction fails keeps its hold (nothing of it is written); Run goes on
// with the next task and returns all such errors joined, together with what it did clear. A nil ctx is
// context.Background().
func Run(ctx context.Context, d Deps) (Result, error) {
	var res Result
	if ctx == nil {
		ctx = context.Background()
	}
	if d.DB == nil {
		return res, errors.New("holdexpiry: no database")
	}
	now := time.Now
	if d.Now != nil {
		now = d.Now
	}
	expiry, err := ExpiryMin(ctx, d.DB)
	if err != nil {
		return res, err
	}
	res.ExpiryMin = expiry
	if expiry == 0 {
		return res, nil
	}
	tasks, err := heldTasks(ctx, d.DB)
	if err != nil {
		return res, err
	}
	var errs []error
	for _, t := range tasks {
		exp, skip, err := decide(ctx, d.DB, t, now(), expiry)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("holdexpiry: %s: %w", t.id, err))
		case skip != "":
			res.Skipped = append(res.Skipped, Skip{t.id, skip})
		case exp != nil:
			done, err := expire(ctx, d.DB, t, exp, now(), expiry)
			if err != nil {
				errs = append(errs, fmt.Errorf("holdexpiry: %s: %w", t.id, err))
			} else if done {
				res.Expired = append(res.Expired, *exp)
			}
		}
	}
	return res, errors.Join(errs...)
}

// ExpiryMin reads the expiry time: a missing row or a value that is not a non-negative whole number is 120, "0"
// is off.
func ExpiryMin(ctx context.Context, db *sql.DB) (int, error) {
	var v string
	err := db.QueryRowContext(ctx, "SELECT value FROM config WHERE key=?", ConfigKey).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultExpiryMin, nil
	}
	if err != nil {
		return 0, fmt.Errorf("holdexpiry: read config %s: %w", ConfigKey, err)
	}
	return parseExpiry(v), nil
}

func parseExpiry(v string) int {
	v = strings.TrimSpace(v)
	if v == "" {
		return DefaultExpiryMin
	}
	for _, c := range v {
		if c < '0' || c > '9' {
			return DefaultExpiryMin
		}
	}
	n, err := strconv.Atoi(v)
	if err != nil || n > 60*24*365 { // overflow or absurd: not a usable number
		return DefaultExpiryMin
	}
	return n
}

// heldTasks lists the in_review tasks with a PR and a non-empty hold.
func heldTasks(ctx context.Context, db *sql.DB) ([]task, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, COALESCE(goal_id,''), pr_url, judge_hold_reason FROM tasks
		WHERE status='in_review' AND TRIM(COALESCE(pr_url,''))<>'' AND TRIM(COALESCE(judge_hold_reason,''))<>''
		ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("holdexpiry: list held tasks: %w", err)
	}
	defer rows.Close()
	var out []task
	for rows.Next() {
		var t task
		if err := rows.Scan(&t.id, &t.goal, &t.prURL, &t.reason); err != nil {
			return nil, fmt.Errorf("holdexpiry: list held tasks: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("holdexpiry: list held tasks: %w", err)
	}
	return out, nil
}

// decide returns the expiry to carry out, a reason the task cannot be decided (hold kept), or neither (hold kept
// by the rule).
func decide(ctx context.Context, db *sql.DB, t task, now time.Time, expiry int) (*Expired, string, error) {
	for _, role := range []string{"reviewer", "qa"} {
		v, err := latestVerdict(ctx, db, t.id, role)
		if err != nil {
			return nil, "", err
		}
		if v != "PASS" {
			return nil, "", nil
		}
	}
	valid, invalid, err := approval(ctx, db, t)
	if err != nil {
		return nil, "", err
	}
	if valid {
		return nil, "", nil
	}
	set, err := latestEvent(ctx, db, t.id, ActionSet)
	if err != nil {
		return nil, "", err
	}
	if set == nil {
		return nil, "no " + ActionSet + " event: the age of the hold is unknown", nil
	}
	setAt, err := time.Parse(time.RFC3339, set.ts)
	if err != nil {
		return nil, fmt.Sprintf("%s event ts %q is not a timestamp", ActionSet, set.ts), nil
	}
	held := int(now.Sub(setAt) / time.Minute)
	if held < 0 {
		held = 0
	}
	if now.Sub(setAt) < time.Duration(expiry)*time.Minute {
		again, err := resetAfterExpiry(ctx, db, t.id)
		if err != nil {
			return nil, "", err
		}
		if !again {
			return nil, "", nil
		}
	}
	marker := "none"
	if invalid != "" {
		marker = "invalid:" + invalid
	}
	setBy := strings.TrimSpace(set.actor)
	if setBy == "" {
		setBy = "unknown"
	}
	e := &Expired{Task: t.id, PRURL: t.prURL, HeldMin: held, SetBy: setBy, Reason: t.reason, Marker: marker}
	if m := prNumberRE.FindStringSubmatch(strings.TrimSpace(t.prURL)); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			e.PR = n
		}
	}
	e.To, err = recipient(ctx, db, setBy, t.goal)
	if err != nil {
		return nil, "", err
	}
	return e, "", nil
}

// latestVerdict is the verdict of the highest-id row of the role, "" when there is none.
func latestVerdict(ctx context.Context, db *sql.DB, taskID, role string) (string, error) {
	var v string
	err := db.QueryRowContext(ctx, "SELECT verdict FROM verdicts WHERE task_id=? AND role=? ORDER BY id DESC LIMIT 1",
		taskID, role).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s verdict: %w", role, err)
	}
	return v, nil
}

func latestEvent(ctx context.Context, db *sql.DB, taskID, action string) (*setEvent, error) {
	var e setEvent
	err := db.QueryRowContext(ctx, `SELECT ts, COALESCE(actor,'') FROM events
		WHERE entity='task' AND entity_id=? AND action=? ORDER BY id DESC LIMIT 1`, taskID, action).Scan(&e.ts, &e.actor)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s event: %w", action, err)
	}
	return &e, nil
}

// resetAfterExpiry reports whether the hold was set again after this package already expired one on the task, with
// no FAIL verdict since. A FAIL verdict whose time cannot be compared counts as new (the hold then gets the full
// expiry time).
func resetAfterExpiry(ctx context.Context, db *sql.DB, taskID string) (bool, error) {
	exp, err := latestEvent(ctx, db, taskID, ActionExpired)
	if err != nil || exp == nil {
		return false, err
	}
	expAt, err := time.Parse(time.RFC3339, exp.ts)
	if err != nil {
		return false, nil
	}
	rows, err := db.QueryContext(ctx, "SELECT created FROM verdicts WHERE task_id=? AND verdict='FAIL'", taskID)
	if err != nil {
		return false, fmt.Errorf("read FAIL verdicts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var created string
		if err := rows.Scan(&created); err != nil {
			return false, fmt.Errorf("read FAIL verdicts: %w", err)
		}
		at, err := time.Parse(time.RFC3339, created)
		if err != nil || !at.Before(expAt) {
			return false, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("read FAIL verdicts: %w", err)
	}
	return true, nil
}

// approval checks every "boss-approved M<id>" marker of the hold text. valid is true when one names a decision
// message from the owner or a boss that is about the task; otherwise invalid is the first marker ("M41"), "" when
// the text has none.
func approval(ctx context.Context, db *sql.DB, t task) (valid bool, invalid string, err error) {
	for _, m := range markerRE.FindAllStringSubmatch(t.reason, -1) {
		ok, err := validDecision(ctx, db, m[1], t.id)
		if err != nil {
			return false, "", err
		}
		if ok {
			return true, "", nil
		}
		if invalid == "" {
			invalid = "M" + m[1]
		}
	}
	return false, invalid, nil
}

func validDecision(ctx context.Context, db *sql.DB, digits, taskID string) (bool, error) {
	id, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return false, nil // more digits than any message id
	}
	var kind, sender, msgTask, subject, body string
	err = db.QueryRowContext(ctx, `SELECT kind, sender, COALESCE(task_id,''), subject, COALESCE(body,'')
		FROM messages WHERE id=?`, id).Scan(&kind, &sender, &msgTask, &subject, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read message M%d: %w", id, err)
	}
	if kind != "decision" {
		return false, nil
	}
	if sender != "owner" {
		var role string
		err := db.QueryRowContext(ctx, "SELECT role FROM agents WHERE name=?", sender).Scan(&role)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("read agent %s: %w", sender, err)
		}
		if role != "boss" {
			return false, nil
		}
	}
	if msgTask == taskID {
		return true, nil
	}
	word, err := regexp.Compile(`(^|[^A-Za-z0-9_-])` + regexp.QuoteMeta(taskID) + `($|[^A-Za-z0-9_-])`)
	if err != nil {
		return false, fmt.Errorf("task id %q: %w", taskID, err)
	}
	return word.MatchString(subject) || word.MatchString(body), nil
}

// recipient is the setter of the hold, or the director of the goal's org when the setter is no agent. PR 714
// (stuckpr.DefaultEscalate) is not merged; until it is, the director is "<org>-director" from goals.org.
func recipient(ctx context.Context, db *sql.DB, setBy, goal string) (string, error) {
	switch setBy {
	case "owner", actor, "unknown", "":
	default:
		return setBy, nil
	}
	var org string
	err := db.QueryRowContext(ctx, "SELECT COALESCE(org,'') FROM goals WHERE id=?", goal).Scan(&org)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("read goal %s: %w", goal, err)
	}
	if org = strings.TrimSpace(org); org == "" {
		org = defaultOrg
	}
	return org + "-director", nil
}

// expiredDetail is the JSON detail of the judge-hold-expired event.
type expiredDetail struct {
	Task    string `json:"task"`
	PR      int    `json:"pr"`
	HeldMin int    `json:"held_min"`
	SetBy   string `json:"set_by"`
	Reason  string `json:"reason"`
	Marker  string `json:"marker"`
}

// expire carries out one expiry in one transaction. done is false when the hold changed since it was read (the
// transaction is rolled back and nothing is written).
func expire(ctx context.Context, db *sql.DB, t task, e *Expired, now time.Time, expiry int) (done bool, err error) {
	detail, err := json.Marshal(expiredDetail{e.Task, e.PR, e.HeldMin, e.SetBy, e.Reason, e.Marker})
	if err != nil {
		return false, err
	}
	ts := now.Format(tsLayout)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() {
		if !done {
			if rb := tx.Rollback(); rb != nil && err == nil {
				err = rb
			}
		}
	}()
	r, err := tx.ExecContext(ctx, `UPDATE tasks SET judge_hold_reason=NULL
		WHERE id=? AND status='in_review' AND judge_hold_reason=?`, t.id, t.reason)
	if err != nil {
		return false, fmt.Errorf("clear the hold: %w", err)
	}
	if n, err := r.RowsAffected(); err != nil {
		return false, fmt.Errorf("clear the hold: %w", err)
	} else if n != 1 {
		return false, nil
	}
	const ev = "INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)"
	clearDetail := fmt.Sprintf("expired after %d min: %s", e.HeldMin, clip(e.Reason, reasonClip))
	if _, err := tx.ExecContext(ctx, ev, ts, actor, "task", t.id, ActionClear, clearDetail); err != nil {
		return false, fmt.Errorf("write %s event: %w", ActionClear, err)
	}
	if _, err := tx.ExecContext(ctx, ev, ts, actor, "task", t.id, ActionExpired, string(detail)); err != nil {
		return false, fmt.Errorf("write %s event: %w", ActionExpired, err)
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO messages(sender,recipient,kind,subject,body,goal_id,task_id,created) VALUES(?,?,?,?,?,?,?,?)",
		actor, e.To, "report", Subject(e), Body(e, expiry), nullable(t.goal), t.id, ts); err != nil {
		return false, fmt.Errorf("write the report to %s: %w", e.To, err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}
	return true, nil
}

// Subject is the subject of the report: "judge hold expired: <task> PR #<n>".
func Subject(e *Expired) string {
	if e.PR == 0 {
		return fmt.Sprintf("judge hold expired: %s PR %s", e.Task, e.PRURL)
	}
	return fmt.Sprintf("judge hold expired: %s PR #%d", e.Task, e.PR)
}

// Body is the text of the report to the setter of the hold.
func Body(e *Expired, expiry int) string {
	return fmt.Sprintf("The judge hold on %s (%s) was cleared by the tick.\n"+
		"Old reason: %s\n"+
		"Held for: %d min (set by %s)\n"+
		"Rule: a hold on a reviewer+QA PASS PR expires after %s unless its text names a boss decision: boss-approved M<id>\n"+
		"To hold again, get a boss decision first.",
		e.Task, e.PRURL, e.Reason, e.HeldMin, e.SetBy, span(expiry))
}

func span(min int) string {
	if min > 0 && min%60 == 0 {
		return fmt.Sprintf("%d h", min/60)
	}
	return fmt.Sprintf("%d min", min)
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
