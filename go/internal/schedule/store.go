package schedule

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Row is one schedules table row for list output.
type Row struct {
	ID            string
	Hour          int
	Days          string
	GoalID        string
	TokenCap      int
	Job           string
	CreatedBy     string
	Created       string
	RemovedAt     sql.NullString
	LastFiredDate sql.NullString
	LastOutcome   sql.NullString
	LastOutcomeAt sql.NullString
}

const maxActive = 5

// Add inserts a schedule after validation; uses one BEGIN IMMEDIATE transaction.
func Add(db *sql.DB, ts time.Time, who string, hour int, days, goalID, job string, tokenCap int) (string, error) {
	if err := ValidateHour(hour); err != nil {
		return "", err
	}
	days, err := ValidateDays(days)
	if err != nil {
		return "", err
	}
	if err := ValidateJob(job); err != nil {
		return "", err
	}
	if _, err := db.Exec("BEGIN IMMEDIATE"); err != nil {
		return "", err
	}
	defer func() { _, _ = db.Exec("ROLLBACK") }()
	var status, org string
	err = db.QueryRow("SELECT status, org FROM goals WHERE id=?", goalID).Scan(&status, &org)
	if errors.Is(err, sql.ErrNoRows) {
		return "", &ValidationError{Msg: "no goal " + goalID}
	}
	if err != nil {
		return "", err
	}
	if status != "active" {
		return "", &ValidationError{Msg: fmt.Sprintf("goal %s is %s; schedules need an active goal", goalID, status)}
	}
	if org != "claude" {
		return "", &ValidationError{Msg: fmt.Sprintf("goal %s is in org %s; schedules need a claude-org goal", goalID, org)}
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM schedules WHERE goal_id=? AND removed_at IS NULL", goalID).Scan(&n); err != nil {
		return "", err
	}
	if n > 0 {
		return "", &ValidationError{Msg: fmt.Sprintf("goal %s already has an active schedule", goalID)}
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM schedules WHERE removed_at IS NULL").Scan(&n); err != nil {
		return "", err
	}
	if n >= maxActive {
		return "", &ValidationError{Msg: fmt.Sprintf("at most %d active schedules; remove one first", maxActive)}
	}
	id, err := nextScheduleID(db)
	if err != nil {
		return "", err
	}
	created := ts.Format("2006-01-02T15:04:05-07:00")
	if _, err := db.Exec(`INSERT INTO schedules(id,hour,days,job,goal_id,token_cap,created_by,created)
		VALUES(?,?,?,?,?,?,?,?)`, id, hour, days, job, goalID, tokenCap, who, created); err != nil {
		return "", err
	}
	detail := fmt.Sprintf("hour=%d goal=%s cap=%d", hour, goalID, tokenCap)
	if _, err := db.Exec("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
		created, who, "schedule", id, "schedule-add", detail); err != nil {
		return "", err
	}
	if _, err := db.Exec("COMMIT"); err != nil {
		return "", err
	}
	return id, nil
}

func nextScheduleID(db *sql.DB) (string, error) {
	rows, err := db.Query("SELECT id FROM schedules WHERE id LIKE 'S-%'")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	max := 0
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
		parts := strings.SplitN(id, "-", 2)
		if len(parts) != 2 {
			continue
		}
		n, err := strconv.Atoi(parts[1])
		if err != nil || n < 0 {
			continue
		}
		if n > max {
			max = n
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return fmt.Sprintf("S-%03d", max+1), nil
}

// Remove soft-removes a schedule.
func Remove(db *sql.DB, ts time.Time, who, id string) error {
	if _, err := db.Exec("BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer func() { _, _ = db.Exec("ROLLBACK") }()
	var removed sql.NullString
	err := db.QueryRow("SELECT removed_at FROM schedules WHERE id=?", id).Scan(&removed)
	if errors.Is(err, sql.ErrNoRows) {
		return &ValidationError{Msg: "no schedule " + id}
	}
	if err != nil {
		return err
	}
	if removed.Valid && removed.String != "" {
		return &ValidationError{Msg: "schedule " + id + " already removed"}
	}
	now := ts.Format("2006-01-02T15:04:05-07:00")
	if _, err := db.Exec("UPDATE schedules SET removed_at=? WHERE id=?", now, id); err != nil {
		return err
	}
	if _, err := db.Exec("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
		now, who, "schedule", id, "schedule-remove", nil); err != nil {
		return err
	}
	_, err = db.Exec("COMMIT")
	return err
}

// List returns schedule rows; activeOnly hides removed rows.
func List(db *sql.DB, activeOnly bool) ([]Row, error) {
	q := `SELECT id,hour,days,job,goal_id,token_cap,created_by,created,removed_at,
		last_fired_date,last_outcome,last_outcome_at FROM schedules`
	if activeOnly {
		q += " WHERE removed_at IS NULL"
	}
	q += " ORDER BY id"
	rows, err := db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.ID, &r.Hour, &r.Days, &r.Job, &r.GoalID, &r.TokenCap, &r.CreatedBy, &r.Created,
			&r.RemovedAt, &r.LastFiredDate, &r.LastOutcome, &r.LastOutcomeAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
