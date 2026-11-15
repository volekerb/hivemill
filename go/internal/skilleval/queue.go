package skilleval

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"factory/go/internal/sandbox"
	"factory/go/internal/scout"
	"factory/go/internal/tokens"
)

var (
	// \d in Python str patterns is Unicode (category Nd); Go's \d is ASCII.
	trialTargetRE = regexp.MustCompile(`^(?:tr0*)?([\p{Nd}]+)$`)
	candTargetRE  = regexp.MustCompile(`^C-[\p{Nd}]+$`)
	trialDirRE    = regexp.MustCompile(`^tr[\p{Nd}]+$`)
)

// PyInt is Python's int(str) for the digits the patterns above let through and for config values: optional sign,
// Unicode decimal digits, single underscores between digits, surrounding whitespace.
func PyInt(s string) (int64, bool) {
	s = pyStrip(s)
	neg := false
	if s != "" && (s[0] == '+' || s[0] == '-') {
		neg, s = s[0] == '-', s[1:]
	}
	if s == "" {
		return 0, false
	}
	var n int64
	prevUnderscore, seen := true, false
	for _, r := range s {
		if r == '_' {
			if prevUnderscore {
				return 0, false
			}
			prevUnderscore = true
			continue
		}
		d, ok := digitValue(r)
		if !ok || n > (math.MaxInt64-int64(d))/10 {
			return 0, false
		}
		n = n*10 + int64(d)
		prevUnderscore, seen = false, true
	}
	if prevUnderscore || !seen {
		return 0, false
	}
	if neg {
		n = -n
	}
	return n, true
}

// digitValue is the decimal value of a Unicode Nd character: Nd characters come in aligned runs of ten.
func digitValue(r rune) (int, bool) {
	if !unicode.Is(unicode.Nd, r) {
		return 0, false
	}
	n := 0
	for c := r - 1; unicode.Is(unicode.Nd, c); c-- {
		n++
	}
	return n % 10, true
}

// ParseTrialTarget is re.fullmatch(r"(?:tr0*)?(\d+)", target) and int(group).
func ParseTrialTarget(target string) (int64, bool) {
	m := trialTargetRE.FindStringSubmatch(target)
	if m == nil {
		return 0, false
	}
	return PyInt(m[1])
}

// TaskNames is task_names(): the eval tasks that have a meta.json, sorted.
func TaskNames(tasksDir string) ([]string, error) {
	ents, err := os.ReadDir(tasksDir)
	if err != nil {
		return nil, fmt.Errorf("list eval tasks in %s: %w", tasksDir, err)
	}
	var out []string
	for _, e := range ents {
		if st, err := os.Stat(filepath.Join(tasksDir, e.Name(), "meta.json")); err == nil && st.Mode().IsRegular() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// QueueTrials inserts pending with/without trials for each task (a task's two conditions back to back). It returns
// (new ids, all trials of the candidate at that sha): the UNIQUE key makes a repeat a no-op, so new is empty then.
func QueueTrials(db *sql.DB, cid, sha string, tasks []string, author, stamp string) ([]int64, []Trial, error) {
	var added []int64
	for _, task := range tasks {
		for _, cond := range []string{"with", "without"} {
			res, err := db.Exec("INSERT OR IGNORE INTO skill_trials(candidate_id,vendor_sha,task,condition,status,queued,author) "+
				"VALUES(?,?,?,?,'pending',?,?)", cid, sha, task, cond, stamp, author)
			if err != nil {
				return nil, nil, fmt.Errorf("queue trial %s %s %s: %w", cid, task, cond, err)
			}
			if n, err := res.RowsAffected(); err != nil {
				return nil, nil, fmt.Errorf("count queued trials: %w", err)
			} else if n == 1 {
				id, err := res.LastInsertId()
				if err != nil {
					return nil, nil, fmt.Errorf("read queued trial id: %w", err)
				}
				added = append(added, id)
			}
		}
	}
	if len(added) > 0 {
		if _, err := db.Exec("UPDATE scout_candidates SET status='queued-eval' WHERE id=?", cid); err != nil {
			return nil, nil, fmt.Errorf("mark %s queued-eval: %w", cid, err)
		}
	}
	rows, err := QueryTrials(db, "WHERE candidate_id=? AND vendor_sha=? ORDER BY id", cid, sha)
	return added, rows, err
}

// Col is one column assignment of Transition.
type Col struct {
	Name string
	Val  any
}

// Transition atomically moves trial tid from one of the statuses frm to `to`: BEGIN IMMEDIATE, then an UPDATE guarded
// by the old status. True only for the one caller whose UPDATE changed the row; the loser must back off. The db must
// be a single-connection pool (factorydb.Open).
func Transition(db *sql.DB, tid int64, to string, frm []string, cols ...Col) (won bool, err error) {
	sets := []string{"status=?"}
	args := []any{to}
	for _, c := range cols {
		sets = append(sets, c.Name+"=?")
		args = append(args, c.Val)
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(frm)), ",")
	args = append(args, tid)
	for _, f := range frm {
		args = append(args, f)
	}
	if _, err := db.Exec("BEGIN IMMEDIATE"); err != nil {
		return false, fmt.Errorf("begin transition of trial %d: %w", tid, err)
	}
	res, uerr := db.Exec("UPDATE skill_trials SET "+strings.Join(sets, ", ")+" WHERE id=? AND status IN ("+marks+")", args...)
	if uerr != nil {
		if _, rerr := db.Exec("ROLLBACK"); rerr != nil {
			return false, fmt.Errorf("transition of trial %d: %v; and the rollback failed: %w", tid, uerr, rerr)
		}
		return false, fmt.Errorf("transition of trial %d: %w", tid, uerr)
	}
	n, nerr := res.RowsAffected()
	if _, cerr := db.Exec("COMMIT"); cerr != nil {
		return false, fmt.Errorf("commit transition of trial %d: %w", tid, cerr)
	}
	if nerr != nil {
		return false, fmt.Errorf("count transitioned trials: %w", nerr)
	}
	return n == 1, nil
}

// Claim moves a trial pending -> starting, atomically: the only way a trial begins to start (tick and `try` alike).
func Claim(db *sql.DB, tid int64, stamp string) (bool, error) {
	return Transition(db, tid, "starting", []string{"pending"}, Col{"started", stamp})
}

// Cancel cancels one trial (id or trNNNN) or every open trial of a candidate (C-xxxx): pending ones become
// cancelled, running ones are stopped through CollectTrial(reason cancelled). It returns (lines, errors).
func (c Collector) Cancel(db *sql.DB, target, root string, api API) (lines, errs []string, err error) {
	var rows []Trial
	if id, ok := ParseTrialTarget(target); ok {
		rows, err = QueryTrials(db, "WHERE id=?", id)
	} else if candTargetRE.MatchString(target) {
		rows, err = QueryTrials(db, "WHERE candidate_id=? ORDER BY id", target)
	} else {
		return nil, nil, cerr("usage: factory skilleval cancel <trial id|trNNNN|C-xxxx>")
	}
	if err != nil {
		return nil, nil, err
	}
	if len(rows) == 0 {
		return nil, nil, cerr("nothing to cancel: no trial or candidate %s", target)
	}
	for _, r := range rows {
		switch r.Status {
		case "pending":
			won, err := Transition(db, r.ID, "cancelled", []string{"pending"}, Col{"passed", 0}, Col{"ended", api.Now()})
			if err != nil {
				return nil, nil, err
			}
			if !won {
				errs = append(errs, fmt.Sprintf("trial %d: was claimed for start meanwhile; retry the cancel", r.ID))
				continue
			}
			t, _, err := GetTrial(db, r.ID)
			if err != nil {
				return nil, nil, err
			}
			lines = append(lines, TrialLine(t))
		case "running":
			t, collectErr := c.CollectTrial(db, r.ID, root, api, "cancelled")
			var ce *CollectError
			if errors.As(collectErr, &ce) {
				errs = append(errs, fmt.Sprintf("trial %d: %s", r.ID, ce.Msg))
				continue
			}
			if collectErr != nil {
				return nil, nil, collectErr
			}
			lines = append(lines, TrialLine(t))
		case "starting":
			errs = append(errs, fmt.Sprintf("trial %d: is starting right now; retry the cancel in a minute", r.ID))
		}
		// a terminal trial is left as it is
	}
	return lines, errs, nil
}

// Voided is the row a void returns plus the status it had.
type Voided struct {
	Trial
	Prev string
}

// Void voids one finished trial (id or trNNNN) whose result says nothing about the skill (reason infra): status
// becomes voided, every recorded field is kept, summaries ignore it and `try` may re-run it.
func Void(db *sql.DB, target, reason, why string) (Voided, error) {
	id, ok := ParseTrialTarget(target)
	if !ok {
		return Voided{}, cerr("usage: factory skilleval void <trial id|trNNNN> --reason infra --why <text>")
	}
	if reason != "infra" {
		return Voided{}, cerr("void needs --reason infra")
	}
	r, found, err := GetTrial(db, id)
	if err != nil {
		return Voided{}, err
	}
	if !found {
		return Voided{}, cerr("no trial %s", target)
	}
	if !in(Voidable, r.Status) {
		hint := "nothing to do"
		if r.Status == "pending" || r.Status == "running" || r.Status == "starting" {
			hint = "use `factory skilleval cancel`"
		}
		return Voided{}, cerr("trial %d is %s: only a finished trial (%s) can be voided; %s", r.ID, r.Status,
			strings.Join(Voidable, ", "), hint)
	}
	won, err := Transition(db, r.ID, "voided", []string{r.Status}, Col{"void_reason", reason}, Col{"void_why", why})
	if err != nil {
		return Voided{}, err
	}
	if !won {
		return Voided{}, cerr("trial %d changed meanwhile; look at `factory skilleval list` and retry", r.ID)
	}
	out, _, err := GetTrial(db, r.ID)
	return Voided{Trial: out, Prev: r.Status}, err
}

func runePrefix(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func cfgInt(db *sql.DB, api API, key string) (int64, error) {
	v, err := api.Cfg(db, key)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", key, err)
	}
	n, ok := PyInt(v)
	if !ok {
		return 0, fmt.Errorf("%s must be an integer, got %s", key, pyRepr(v))
	}
	return n, nil
}

func tokensOf(d string) (int64, error) {
	if d == "" {
		return 0, nil
	}
	projects := filepath.Join(d, "cfg", "projects")
	if st, err := os.Lstat(projects); err != nil || !st.IsDir() {
		return 0, nil
	}
	if err := CleanProjects(projects); err != nil {
		return 0, err
	}
	u := tokens.UsageTotals(projects)
	return u.Input + u.Output + u.CacheWrite, nil
}

func (c Collector) collectRunning(db *sql.DB, api API, root string, clock time.Time) error {
	minutes, err := cfgInt(db, api, "skilleval_trial_minutes")
	if err != nil {
		return err
	}
	limit, err := cfgInt(db, api, "skilleval_trial_max_tokens")
	if err != nil {
		return err
	}
	rows, err := QueryTrials(db, "WHERE status='running' ORDER BY id")
	if err != nil {
		return err
	}
	for _, r := range rows {
		d := TrialDir(root, r)
		agent := r.Agent.String
		var liveStatus, liveStarted sql.NullString
		haveLive := false
		if agent != "" {
			err := db.QueryRow("SELECT status, started FROM agents WHERE name=?", agent).Scan(&liveStatus, &liveStarted)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("load agent %s: %w", agent, err)
			}
			haveLive = err == nil
		}
		done := false
		if d != "" && agent != "" {
			done, _ = TempDone(filepath.Join(d, "fh"), agent)
		}
		reason := ""
		if !done && haveLive && in([]string{"live", "queued", "starting"}, liveStatus.String) {
			if liveStatus.String != "live" {
				continue // waiting at the gate: no clock, no tokens yet
			}
			// the wall runs from the agent going live, not from the gate queue
			started, ok := scout.TS(liveStarted.String)
			if !ok {
				started, ok = scout.TS(r.Started.String)
			}
			if ok && clock.Sub(started).Seconds() > float64(minutes*60) {
				reason = "timeout"
			} else if n, err := tokensOf(d); err != nil {
				return err
			} else if n > limit {
				reason = "over-budget"
			} else {
				continue
			}
		}
		if _, err := c.CollectTrial(db, r.ID, root, api, reason); err != nil {
			var ce *CollectError
			if !errors.As(err, &ce) {
				return err
			}
			// e.g. the stop failed: the trial stays running and is retried next tick
			if eerr := api.Event(db, "skilleval", r.CandidateID, "collect-retry",
				fmt.Sprintf("trial %d: %s", r.ID, runePrefix(ce.Msg, 300))); eerr != nil {
				return fmt.Errorf("record collect-retry: %w", eerr)
			}
		}
	}
	return nil
}

func failStart(db *sql.DB, api API, r Trial, action, msg string) error {
	if _, err := db.Exec("UPDATE skill_trials SET status='error', check_output=? WHERE id=? AND status='starting'",
		runePrefix(msg, 400), r.ID); err != nil {
		return fmt.Errorf("fail start of trial %d: %w", r.ID, err)
	}
	return api.Event(db, "skilleval", r.CandidateID, action, fmt.Sprintf("trial %d: %s", r.ID, runePrefix(msg, 300)))
}

func reapStarting(db *sql.DB, api API, clock time.Time) error {
	rows, err := QueryTrials(db, "WHERE status='starting'")
	if err != nil {
		return err
	}
	for _, r := range rows {
		if t, ok := scout.TS(r.Started.String); ok && clock.Sub(t).Seconds() > StartingMax*60 {
			if err := failStart(db, api, r, "start-failed",
				fmt.Sprintf("stuck in starting for over %d min; its process died", StartingMax)); err != nil {
				return err
			}
		}
	}
	return nil
}

func startedToday(db *sql.DB, clock time.Time) (int, error) {
	rows, err := db.Query("SELECT started FROM skill_trials WHERE started IS NOT NULL")
	if err != nil {
		return 0, fmt.Errorf("list trial starts: %w", err)
	}
	defer rows.Close()
	day := clock.Format("2006-01-02")
	n := 0
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return 0, fmt.Errorf("scan trial start: %w", err)
		}
		if t, ok := scout.TS(s); ok && t.In(clock.Location()).Format("2006-01-02") == day {
			n++
		}
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("read trial starts: %w", err)
	}
	return n, nil
}

func (c Collector) startNext(db *sql.DB, api API, root string, clock time.Time) error {
	var open int64
	if err := db.QueryRow("SELECT count(*) FROM skill_trials WHERE status IN ('running','starting')").Scan(&open); err != nil {
		return fmt.Errorf("count running trials: %w", err)
	}
	maxConc, err := cfgInt(db, api, "skilleval_max_concurrent")
	if err != nil {
		return err
	}
	if open >= maxConc {
		return nil
	}
	today, err := startedToday(db, clock)
	if err != nil {
		return err
	}
	maxDay, err := cfgInt(db, api, "skilleval_max_trials_per_day")
	if err != nil {
		return err
	}
	if int64(today) >= maxDay {
		return nil
	}
	pend, err := QueryTrials(db, "WHERE status='pending' ORDER BY id LIMIT 1")
	if err != nil || len(pend) == 0 {
		return err
	}
	row := pend[0]
	// a global blocker (no token, no namespaces) leaves the queue untouched instead of erroring every trial
	berr := sandbox.CheckToken(api.Home() + "/.config/factory/eval-oauth-token")
	if berr == nil {
		berr = sandbox.CheckUserns(api.Unshare())
	}
	if berr != nil {
		if !errors.Is(berr, sandbox.ErrSandboxRefused) {
			return berr
		}
		why := runePrefix(refusedText(berr), 300)
		var action, detail sql.NullString
		lerr := db.QueryRow("SELECT action, detail FROM events WHERE action IN ('skilleval-blocked','try') AND entity='skilleval' "+
			"ORDER BY id DESC LIMIT 1").Scan(&action, &detail)
		if lerr != nil && !errors.Is(lerr, sql.ErrNoRows) {
			return fmt.Errorf("read last skilleval event: %w", lerr)
		}
		// the tick runs every minute: say it once per distinct reason
		if errors.Is(lerr, sql.ErrNoRows) || action.String != "skilleval-blocked" || detail.String != why {
			return api.Event(db, "skilleval", "-", "skilleval-blocked", why)
		}
		return nil
	}
	stamp := scout.Stamp(clock) // the tick clock owns the day count
	won, err := Claim(db, row.ID, stamp)
	if err != nil || !won {
		return err // `skilleval try` or another tick took it, or it was cancelled
	}
	if lerr := api.Launch(db, row, root, api.Home(), api.Unshare()); lerr != nil {
		var sr *SpawnRefused
		switch {
		case errors.Is(lerr, sandbox.ErrSandboxRefused):
			return failStart(db, api, row, "start-refused", refusedText(lerr))
		case errors.As(lerr, &sr): // spawn refused: launch already set the row to error
			return api.Event(db, "skilleval", row.CandidateID, "start-refused", fmt.Sprintf("trial %d: spawn refused", row.ID))
		default: // e.g. an I/O error in build_sandbox: error the row so the queue is not blocked behind it
			return failStart(db, api, row, "start-failed", fmt.Sprintf("%T: %v", lerr, lerr))
		}
	}
	if _, err := db.Exec("UPDATE skill_trials SET started=? WHERE id=? AND status='running'", stamp, row.ID); err != nil {
		return fmt.Errorf("stamp started of trial %d: %w", row.ID, err)
	}
	return nil
}

func cleanup(db *sql.DB, root string, clock time.Time, keepH int64) error {
	trials := filepath.Join(sandbox.Realpath(root), "trials")
	if st, err := os.Stat(trials); err != nil || !st.IsDir() {
		return nil
	}
	rows, err := QueryTrials(db, "WHERE status IN ('error','timeout','over-budget','voided')")
	if err != nil {
		return err
	}
	for _, r := range rows {
		ended, ok := scout.TS(r.Ended.String)
		d := TrialDir(root, r)
		if d != "" && ok && clock.Sub(ended).Seconds() > float64(keepH*3600) {
			removeTree(d)
		}
	}
	known := map[string]bool{}
	ids, err := db.Query("SELECT id FROM skill_trials")
	if err != nil {
		return fmt.Errorf("list trial ids: %w", err)
	}
	for ids.Next() {
		var id int
		if err := ids.Scan(&id); err != nil {
			ids.Close()
			return fmt.Errorf("scan trial id: %w", err)
		}
		known[sandbox.TrialName(id)] = true
	}
	if err := ids.Err(); err != nil {
		ids.Close()
		return fmt.Errorf("read trial ids: %w", err)
	}
	if err := ids.Close(); err != nil {
		return fmt.Errorf("close trial ids: %w", err)
	}
	ents, err := os.ReadDir(trials)
	if err != nil {
		return fmt.Errorf("list %s: %w", trials, err)
	}
	for _, e := range ents {
		if !trialDirRE.MatchString(e.Name()) || known[e.Name()] {
			continue
		}
		if st, err := os.Lstat(filepath.Join(trials, e.Name())); err == nil && st.IsDir() {
			removeTree(filepath.Join(trials, e.Name()))
		}
	}
	return nil
}

// removeTree is shutil.rmtree(path, ignore_errors=True): cleanup of old sandboxes is best effort and retried on the
// next tick.
func removeTree(p string) {
	if err := os.RemoveAll(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return
	}
}

func evaluated(db *sql.DB) error {
	ids, err := db.Query("SELECT id FROM scout_candidates WHERE status='queued-eval'")
	if err != nil {
		return fmt.Errorf("list queued-eval candidates: %w", err)
	}
	var cands []string
	for ids.Next() {
		var id string
		if err := ids.Scan(&id); err != nil {
			ids.Close()
			return fmt.Errorf("scan candidate id: %w", err)
		}
		cands = append(cands, id)
	}
	if err := ids.Err(); err != nil {
		ids.Close()
		return fmt.Errorf("read candidates: %w", err)
	}
	if err := ids.Close(); err != nil {
		return fmt.Errorf("close candidates: %w", err)
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(Terminal)), ",")
	for _, cid := range cands {
		args := []any{cid}
		for _, t := range Terminal {
			args = append(args, t)
		}
		var nOpen int64
		if err := db.QueryRow("SELECT count(*) FROM skill_trials WHERE candidate_id=? AND status NOT IN ("+marks+")", args...).Scan(&nOpen); err != nil {
			return fmt.Errorf("count open trials of %s: %w", cid, err)
		}
		var one int
		err := db.QueryRow("SELECT 1 FROM skill_trials WHERE candidate_id=?", cid).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) || nOpen > 0 {
			continue
		}
		if err != nil {
			return fmt.Errorf("check trials of %s: %w", cid, err)
		}
		if _, err := db.Exec("UPDATE scout_candidates SET status='evaluated' WHERE id=? AND status='queued-eval'", cid); err != nil {
			return fmt.Errorf("mark %s evaluated: %w", cid, err)
		}
	}
	return nil
}

// Tick is one scheduling pass (docs/scout-eval.md): collect finished or over-budget trials, start at most one pending
// trial under the caps, clean old sandboxes, mark fully terminal candidates evaluated.
func (c Collector) Tick(db *sql.DB, api API) error {
	clock := api.Clock()
	value, err := api.Cfg(db, "skilleval_root")
	if err != nil {
		return fmt.Errorf("read skilleval_root: %w", err)
	}
	root, err := scout.ResolveRoot(value, api.HomeDir())
	if err != nil {
		return err
	}
	if err := c.collectRunning(db, api, root, clock); err != nil {
		return err
	}
	if err := reapStarting(db, api, clock); err != nil {
		return err
	}
	if err := c.startNext(db, api, root, clock); err != nil {
		return err
	}
	keep, err := cfgInt(db, api, "skilleval_keep_failed_h")
	if err != nil {
		return err
	}
	if err := cleanup(db, root, clock, keep); err != nil {
		return err
	}
	return evaluated(db)
}
