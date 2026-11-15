package cli

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"factory/go/internal/ghx"
	"factory/go/internal/merge"
)

// judge (G-048 P1k): cmd_judge and _judge_gate_and_merge in bin/factory_cli.py, the only merge path. The gate is the
// Python gate decision by decision (package merge); what this port does not decide the same way is handed to Python
// before anything is written: --train, --record-merged, --dry-run, --no-code-change, abbreviations and every other
// argv shape (parseJudge), a local-gate project, a public_text_repos config, a git config Python might refuse, a head
// that does not contain main (refresh and behind_ok), and any gh answer of a shape Python
// reads differently. Once the merge is attempted nothing is handed over any more.

func init() {
	register("judge", "", handler{parse: parseJudge, run: runJudge})
}

type judgeCmd struct {
	task, reason string
	cause        *string
	reject       bool
}

// parseJudge accepts `judge [TASK] (--merge | --reject) [--reason V] [--cause V]` with exact long flags, each at most
// once. Everything argparse could read differently fails the parse: Python answers.
func parseJudge(rest []string) (any, bool) {
	var c judgeCmd
	var haveMerge, haveReject, haveTask bool
	seen := map[string]bool{}
	for i := 0; i < len(rest); i++ {
		w := rest[i]
		if !utf8.ValidString(w) {
			return nil, false
		}
		switch w {
		case "--merge", "--reject":
			if haveMerge || haveReject {
				return nil, false
			}
			haveMerge, haveReject = w == "--merge", w == "--reject"
			continue
		}
		if !strings.HasPrefix(w, "-") || w == "-" {
			if haveTask {
				return nil, false
			}
			haveTask, c.task = true, w
			continue
		}
		name, val, hasEq := strings.Cut(w, "=")
		if (name != "--reason" && name != "--cause") || seen[name] {
			return nil, false
		}
		if !hasEq {
			if i+1 >= len(rest) {
				return nil, false
			}
			i++
			val = rest[i]
		}
		if (strings.HasPrefix(val, "-") && val != "-") || !utf8.ValidString(val) {
			return nil, false
		}
		seen[name] = true
		if name == "--reason" {
			c.reason = val
		} else {
			for _, r := range val {
				if r >= 0x80 {
					return nil, false
				}
			}
			c.cause = strp(val)
		}
	}
	if haveMerge == haveReject {
		return nil, false
	}
	c.reject = haveReject
	return c, true
}

type judgeTask struct {
	id, goal, prURL            string
	branch, worktree, assignee sql.NullString
}

func judgeCfg(c *ctx, key string) (string, error) { return cfgText(c, key) }

// judgeRefuse is rejection_gate_refusal + die: the judge-blocked / merge-failed event and its incident row.
func judgeRefuse(c *ctx, task, action, detail string, code int, msg string) error {
	cause := merge.ClassifyGateRefusalCause(detail)
	res, err := c.exec("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
		eventTS(c), c.who, "task", task, action, detail)
	if err != nil {
		return err
	}
	eid, err := res.LastInsertId()
	if err != nil {
		return err
	}
	if _, err := c.exec("INSERT INTO rejection_incidents(cause,kind,task_id,verdict_id,event_id,created) VALUES(?,?,?,?,?,?)",
		cause, "gate", task, nil, eid, eventTS(c)); err != nil {
		return err
	}
	return dieErr(code, msg)
}

var judgePRURL = regexp.MustCompile(`^https?://[^/]+/([^/]+/[^/]+)/pull/`)

// judgeHasGateProject is gate_project_for(...) is not None: a local-gate project (non-empty gate_cmd).
func judgeHasGateProject(c *ctx, prURL string) (bool, error) {
	var cmd sql.NullString
	err := c.db.QueryRow("SELECT p.gate_cmd FROM tasks t JOIN goals g ON g.id=t.goal_id JOIN projects p ON p.id=g.project_id "+
		"WHERE t.pr_url=?", prURL).Scan(&cmd)
	found := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if m := judgePRURL.FindStringSubmatch(prURL); !found && m != nil {
		err = c.db.QueryRow("SELECT gate_cmd FROM projects WHERE gh_repo=?", m[1]).Scan(&cmd)
		found = err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
	}
	return found && pyStrip(cmd.String) != "", nil
}

// judgeRequiredChecks is required_checks_for(con, pr_url).
func judgeRequiredChecks(c *ctx, prURL string) (string, error) {
	var rc sql.NullString
	err := c.db.QueryRow("SELECT p.required_checks FROM tasks t JOIN goals g ON g.id=t.goal_id JOIN projects p ON p.id=g.project_id "+
		"WHERE t.pr_url=?", prURL).Scan(&rc)
	found := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if m := judgePRURL.FindStringSubmatch(prURL); !found && m != nil {
		err = c.db.QueryRow("SELECT required_checks FROM projects WHERE gh_repo=?", m[1]).Scan(&rc)
		found = err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	}
	if found && pyStrip(rc.String) != "" {
		return pyStrip(rc.String), nil
	}
	return judgeCfg(c, "required_checks")
}

func runJudge(c *ctx, parsed any) (int, error) {
	a := parsed.(judgeCmd)
	if a.task == "" || a.reason == "" {
		return 0, dieErr(2, "judge needs a task and --reason (or --train T-a T-b ... [--dry-run])")
	}
	if _, err := requireRole(c, "judge"); err != nil {
		return 0, err
	}
	var t judgeTask
	var pr sql.NullString
	err := c.db.QueryRow("SELECT id, goal_id, pr_url, branch, worktree, assignee FROM tasks WHERE id=?", a.task).
		Scan(&t.id, &t.goal, &pr, &t.branch, &t.worktree, &t.assignee)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, dieErr(1, "no task "+a.task)
	}
	if err != nil {
		return 0, err
	}
	if pr.String == "" {
		return 0, dieErr(1, a.task+" has no PR")
	}
	t.prURL = pr.String
	j, err := newJudgeEnv(c)
	if err != nil {
		return 0, err
	}
	if a.reject {
		return 0, judgeReject(c, j, a, t)
	}
	return 0, judgeMerge(c, j, a, t)
}

// judgeHard turns an uncaught gh_json failure into Python's exit.
func judgeHard(err error) error {
	var gf *ghFail
	if errors.As(err, &gf) {
		return gf.asExit()
	}
	return err
}

// judgeReject is the --reject branch of cmd_judge (no --no-code-change: that one runs in Python).
func judgeReject(c *ctx, j *judgeEnv, a judgeCmd, t judgeTask) error {
	pr, err := j.prState(t.prURL)
	if err != nil {
		return judgeHard(err)
	}
	sha := pr.HeadSHA
	rp, err := readText(c, &a.reason, false)
	if err != nil {
		return err
	}
	reason := *rp
	cause, err := verdictNormalizeCause(a.cause)
	if err != nil {
		return err
	}
	if cause == "" {
		return dieErr(2, fmt.Sprintf("rejection refused, nothing recorded: pass --cause (%s)", verdictCauseHelp))
	}
	res, err := c.exec("INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) VALUES(?,?,?,?,?,?,?,?)",
		a.task, t.prURL, sha, "judge", "reject", reason, c.who, eventTS(c))
	if err != nil {
		return err
	}
	vid, err := res.LastInsertId()
	if err != nil {
		return err
	}
	if _, err := c.exec("INSERT INTO rejection_incidents(cause,kind,task_id,verdict_id,event_id,created) VALUES(?,?,?,?,?,?)",
		cause, "judge", a.task, vid, nil, eventTS(c)); err != nil {
		return err
	}
	if _, err := c.exec("UPDATE tasks SET status='in_progress', updated=? WHERE id=?", eventTS(c), a.task); err != nil {
		return err
	}
	if err := event(c, "task", a.task, "judge-reject", strp(reason)); err != nil {
		return err
	}
	if err := lcGoalAutoActive(c, t.goal, fmt.Sprintf("task %s in_progress (judge reject)", a.task)); err != nil {
		return err
	}
	if t.assignee.String != "" {
		body := fmt.Sprintf("Task %s status is in_progress after judge reject.\n\n%s\n\nAfter the fix is pushed: factory task update %s --status in_review (existing PR checks apply).",
			a.task, reason, a.task)
		goal, task := t.goal, a.task
		if _, _, err := msgSend(c, t.assignee.String, fmt.Sprintf("judge reject: %s in_progress", a.task), &body, "request", &goal, &task, ""); err != nil {
			return err
		}
	}
	fmt.Fprintf(c.out, "%s REJECTED @ %s\n", a.task, verdictHead8(sha))
	return nil
}

func judgeColumnExists(c *ctx, table, col string) (bool, error) {
	rows, err := c.db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == col {
			return true, nil
		}
	}
	return false, rows.Err()
}

// judgeWaitCI is judge_wait_ci without a new head and without a local gate: poll while the only non-green checks are
// unfinished, for judge_ci_wait_s seconds.
func judgeWaitCI(c *ctx, j *judgeEnv, url string, pr merge.PR) (merge.PR, int, error) {
	waiting := func(pr merge.PR) (string, error) {
		bad, err := merge.ChecksOK(pr.Rollup)
		if err != nil {
			return "", errDelegate
		}
		if merge.Pending(bad) {
			return strings.Join(bad, ", "), nil
		}
		return "", nil
	}
	what, err := waiting(pr)
	if err != nil || what == "" {
		return pr, 0, err
	}
	raw, err := judgeCfg(c, "judge_ci_wait_s")
	if err != nil {
		return pr, 0, err
	}
	limit, ok := judgeInt(raw)
	if !ok {
		return pr, 0, errDelegate // Python's int() / its "must be an integer" refusal
	}
	poll := float64(judgePollDefault)
	if v := c.env.Getenv("FACTORY_JUDGE_POLL_S"); v != "" {
		if poll, err = strconv.ParseFloat(v, 64); err != nil {
			return pr, 0, errDelegate
		}
	}
	start := time.Now()
	for what != "" && time.Since(start).Seconds() < float64(limit) {
		el := time.Since(start).Seconds()
		fmt.Fprintf(c.out, "waiting for CI (%.0fs of %ds): %s\n", el, limit, what)
		d := poll
		if rest := float64(limit) - time.Since(start).Seconds(); rest < d {
			d = rest
		}
		if d > 0 {
			time.Sleep(time.Duration(d * float64(time.Second)))
		}
		if pr, err = j.prState(url); err != nil {
			return pr, 0, judgeHard(err)
		}
		if what, err = waiting(pr); err != nil {
			return pr, 0, err
		}
	}
	return pr, int(time.Since(start).Seconds()), nil
}

// judgeLock is task_merge_lock: the per-task flock shared with the mechanical merger. The caller closes the file.
func judgeLock(j *judgeEnv, task string) (*os.File, error) {
	dir := filepath.Join(j.home, "run")
	if err := os.Mkdir(dir, 0o777); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, errDelegate
	}
	f, err := os.OpenFile(filepath.Join(dir, "merge-"+task+".lock"), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		return nil, errDelegate
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close() // nothing was written through f; the lock is not ours
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, dieErr(4, fmt.Sprintf("merge refused: another judge or mechanical merger is merging %s right now (run/merge-%s.lock); nothing merged", task, task))
		}
		return nil, errDelegate
	}
	return f, nil
}

func judgeVerdict(c *ctx, task, role string) (merge.Verdict, error) {
	var v, h sql.NullString
	err := c.db.QueryRow("SELECT verdict,head_sha FROM verdicts WHERE task_id=? AND role=? ORDER BY id DESC LIMIT 1", task, role).Scan(&v, &h)
	if errors.Is(err, sql.ErrNoRows) {
		return merge.Verdict{}, nil
	}
	if err != nil {
		return merge.Verdict{}, err
	}
	return merge.Verdict{Present: true, Verdict: v.String, Head: h.String}, nil
}

func judgeRedState(c *ctx) func(slug string) merge.RedMain {
	return func(slug string) merge.RedMain {
		var v sql.NullString
		if err := c.db.QueryRow("SELECT value FROM config WHERE key=?", "red_main:"+slug).Scan(&v); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return merge.RedMain{}
			}
			return merge.ParseRedMain("not json") // an unreadable store keeps the freeze (Python raises: nothing merges)
		}
		return merge.ParseRedMain(v.String)
	}
}

// judgeLessons is the lesson part of after_merge: stamp the lesson links, apply the lessons they complete.
func judgeLessons(c *ctx, task, prURL string) error {
	ts := eventTS(c)
	if _, err := c.exec("UPDATE lesson_links SET merged_at=?, pr_url=coalesce(pr_url, ?) WHERE task_id=? AND merged_at IS NULL", ts, prURL, task); err != nil {
		return err
	}
	rows, err := c.query("SELECT DISTINCT l.id FROM lessons l JOIN lesson_links k ON k.lesson_id=l.id WHERE k.task_id=? AND l.status='open' ORDER BY l.id", task)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, id := range ids {
		var one int
		switch err := c.db.QueryRow("SELECT 1 FROM lesson_links WHERE lesson_id=? AND merged_at IS NULL", id).Scan(&one); {
		case err == nil:
			continue
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		if _, err := c.exec("UPDATE lessons SET status='applied', applied_at=?, updated=? WHERE id=?", ts, ts, id); err != nil {
			return err
		}
		if err := event(c, "lesson", id, "applied", strp("auto: "+task+" "+prURL)); err != nil {
			return err
		}
	}
	return nil
}

// judgeJSONStr is json.dumps of a short ASCII-safe string (hex shas, role names).
func judgeJSONStr(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

// judgeRecordMerged is record_merged for a normal merge (no --by).
func judgeRecordMerged(c *ctx, j *judgeEnv, t judgeTask, sha, shown, reason string, carried []merge.Outcome, postErr string) error {
	for _, o := range carried {
		ms := make([]string, len(o.Merges))
		for i, m := range o.Merges {
			ms[i] = judgeJSONStr(m)
		}
		detail := fmt.Sprintf(`{"role": %s, "from": %s, "to": %s, "main": %s, "merges": [%s]}`,
			judgeJSONStr(o.Role), judgeJSONStr(o.From), judgeJSONStr(sha), judgeJSONStr(o.MainSHA), strings.Join(ms, ", "))
		if err := event(c, "task", t.id, "verdict-carried", strp(detail)); err != nil {
			return err
		}
		fmt.Fprintf(c.out, "%s PASS carried %s -> %s across %d pure origin/main merge(s)\n", o.Role, verdictHead8(o.From), verdictHead8(sha), len(o.Merges))
	}
	if _, err := c.exec("INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) VALUES(?,?,?,?,?,?,?,?)",
		t.id, t.prURL, sha, "judge", "merge", reason, c.who, eventTS(c)); err != nil {
		return err
	}
	if _, err := c.exec("UPDATE tasks SET status='done', updated=? WHERE id=?", eventTS(c), t.id); err != nil {
		return err
	}
	if err := event(c, "task", t.id, "merged", strp("@"+verdictHead8(shown))); err != nil {
		return err
	}
	// run_after_merge: a failed hook never changes the merge result
	if err := judgeAfterMerge(c, j, t); err != nil {
		if e2 := event(c, "task", t.id, "post-merge-hook-failed", strp(judgeSQLiteText(err.Error()))); e2 != nil {
			fmt.Fprintf(c.errb, "factory: post-merge-hook-failed event not recorded: %v\n", e2)
		}
	}
	if postErr != "" {
		if err := event(c, "task", t.id, "merge-post-error", strp(postErr)); err != nil {
			return err
		}
	}
	fmt.Fprintf(c.out, "%s MERGED @ %s\n", t.id, verdictHead8(shown))
	return nil
}

// judgeHold is pre_gh_merge_refusal for an LLM judge: the task's judge hold, or the judge agent's stop.
func judgeHold(c *ctx, task string) (string, error) {
	var h sql.NullString
	if err := c.db.QueryRow("SELECT judge_hold_reason FROM tasks WHERE id=?", task).Scan(&h); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if jhr := pyStrip(h.String); jhr != "" {
		fmt.Fprintf(c.out, "judge-hold %s: %s\n", task, jhr)
		var action, detail sql.NullString
		err := c.db.QueryRow("SELECT action, detail FROM events WHERE entity='task' AND entity_id=? AND action IN ('judge-hold','judge-hold-clear') ORDER BY id DESC LIMIT 1", task).Scan(&action, &detail)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
		cut := jhr
		if r := []rune(cut); len(r) > 300 {
			cut = string(r[:300])
		}
		if !(err == nil && action.String == "judge-hold" && detail.String == cut) {
			if err := event(c, "task", task, "judge-hold", strp(cut)); err != nil {
				return "", err
			}
		}
		return "judge hold: " + jhr, nil
	}
	var status, stop sql.NullString
	err := c.db.QueryRow("SELECT status, stop_requested_at FROM agents WHERE name=?", c.who).Scan(&status, &stop)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if status.String == "stopped" {
		return fmt.Sprintf("judge agent %s is stopped", c.who), nil
	}
	if stop.String != "" {
		return fmt.Sprintf("judge agent %s has stop requested", c.who), nil
	}
	return "", nil
}

func judgeMerge(c *ctx, j *judgeEnv, a judgeCmd, t judgeTask) error {
	var frozen sql.NullString
	if err := c.db.QueryRow("SELECT value FROM config WHERE key='main_freeze'").Scan(&frozen); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if frozen.String != "" {
		msg := "non-PR push on main: " + frozen.String
		return judgeRefuse(c, a.task, "judge-blocked", msg, 4, "merge refused: "+msg+"; nothing merged. Only the director or boss clears it (factory main-unfreeze)")
	}
	// Decided before the first gh call and before stdin is read: these go to Python.
	if ok, err := judgeColumnExists(c, "tasks", "judge_hold_reason"); err != nil {
		return err
	} else if !ok {
		return errDelegate
	}
	if local, err := judgeHasGateProject(c, t.prURL); err != nil {
		return err
	} else if local {
		return errDelegate
	}
	public, err := judgeCfg(c, "public_text_repos")
	if err != nil {
		return err
	}
	if len(merge.CfgSlugs(public)) > 0 {
		return errDelegate
	}
	gitRepo := j.home
	if t.worktree.String != "" {
		if st, err := os.Stat(t.worktree.String); err == nil && st.IsDir() {
			gitRepo = t.worktree.String
		}
	}
	factoryURL, err := judgeCfg(c, "factory_remote_url")
	if err != nil {
		return err
	}
	cwd, err := c.deps.getwd()
	if err != nil {
		return errDelegate
	}
	for _, r := range []string{gitRepo, cwd} {
		if !j.gitConfigClean(r, factoryURL) {
			return errDelegate
		}
	}
	// after the config check, which hands over without a single gh call
	pr, err := j.prState(t.prURL)
	if err != nil {
		return judgeHard(err)
	}
	pr, waited, err := judgeWaitCI(c, j, t.prURL, pr)
	if err != nil {
		return err
	}
	parts := strings.Split(t.prURL, "/")
	ownerRepo := ""
	if len(parts) > 3 {
		end := 5
		if len(parts) < end {
			end = len(parts)
		}
		ownerRepo = strings.Join(parts[3:end], "/")
	}
	rn := runner{j: j, repo: gitRepo}
	var lock *os.File
	defer func() {
		if lock != nil {
			lock.Close() // releases the flock; nothing is written through it
		}
	}()
	retried := false
	reason, reasonRead := "", false
	var repoURL string
	for {
		sha := pr.HeadSHA
		url1, fail1, err := j.repoURL(ownerRepo)
		if err != nil {
			return err
		}
		var unavailable *merge.Carry
		if fail1 != "" {
			unavailable = &merge.Carry{Why: "cannot verify, gh repo view failed: " + fail1}
		} else {
			repoURL = url1
		}
		carry := map[string]merge.Carry{}
		var outcomes []merge.Outcome
		for _, role := range []string{"reviewer", "qa"} {
			v, err := judgeVerdict(c, a.task, role)
			if err != nil {
				return err
			}
			o, err := merge.Evaluate(role, v, sha, carry, unavailable, url1 != "", func(x string) (merge.Carry, error) {
				return merge.VerdictCarries(rn, x, sha, pr.Number, url1, ownerRepo)
			})
			if err != nil {
				return errDelegate // a gh that cannot start: Python's traceback
			}
			outcomes = append(outcomes, o)
		}
		var stale merge.Stale
		url2, fail2, err := j.repoURL(ownerRepo)
		if err != nil {
			return err
		}
		if fail2 != "" {
			stale = merge.Stale{Why: "gh repo view failed: " + fail2}
		} else {
			repoURL = url2
			if stale, err = merge.HeadHasMain(rn, sha, pr.Number, url2, ownerRepo); err != nil {
				return errDelegate
			}
		}
		required, err := judgeRequiredChecks(c, t.prURL)
		if err != nil {
			return err
		}
		var autoMerge sql.NullInt64
		noAuto := false
		switch err := c.db.QueryRow("SELECT p.auto_merge FROM goals g LEFT JOIN projects p ON p.id=g.project_id WHERE g.id=?", t.goal).Scan(&autoMerge); {
		case err == nil:
			noAuto = autoMerge.Valid && autoMerge.Int64 == 0
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		problems, err := merge.Problems(merge.GateInput{PR: pr, Waited: waited, Outcomes: outcomes, Stale: stale, Required: required, NoAuto: noAuto})
		if err != nil {
			return errDelegate // a head that does not contain main (refresh, behind_ok) or a check shape Go does not read
		}
		if len(problems) > 0 {
			joined := strings.Join(problems, "; ")
			return judgeRefuse(c, a.task, "judge-blocked", joined, 4, "merge refused: "+joined)
		}
		var projRepo sql.NullString
		if err := c.db.QueryRow("SELECT p.gh_repo FROM projects p JOIN goals g ON g.project_id=p.id WHERE g.id=?", t.goal).Scan(&projRepo); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		slugs := merge.JudgeSlugs(ownerRepo, projRepo.String)
		frozenRepos, err := judgeCfg(c, "frozen_repos")
		if err != nil {
			return err
		}
		for _, s := range slugs {
			if why := merge.PushPolicy(frozenRepos, s); why != "" {
				return judgeRefuse(c, a.task, "judge-blocked", why, 4, "merge refused: "+why+"; nothing merged")
			}
		}
		freezeOn, err := judgeCfg(c, "red_main_freeze")
		if err != nil {
			return err
		}
		waitMain, err := judgeCfg(c, "judge_wait_main_run")
		if err != nil {
			return err
		}
		red, err := merge.RedMainRefusal(freezeOn == "1", slugs, a.task, judgeRedState(c))
		if err != nil {
			return errDelegate
		}
		if red == "" {
			if red, err = merge.MainRunRefusal(waitMain == "1", slugs, a.task, judgeRedState(c), j.mainHeadRun); err != nil {
				return errDelegate
			}
		}
		if red != "" {
			return judgeRefuse(c, a.task, "judge-blocked", red, 4, "merge refused: "+red+"; nothing merged")
		}
		if lock == nil {
			if lock, err = judgeLock(j, a.task); err != nil {
				return err
			}
			var status string
			if err := c.db.QueryRow("SELECT status FROM tasks WHERE id=?", a.task).Scan(&status); err != nil {
				return err
			}
			if status != "in_review" {
				return dieErr(4, fmt.Sprintf("merge refused: %s is no longer in_review (merged or moved by another run); nothing merged", a.task))
			}
			now, err := j.prState(t.prURL)
			if err != nil {
				return judgeHard(err)
			}
			if now.State != "OPEN" {
				return dieErr(4, "merge refused: the PR is no longer OPEN (merged by another run); nothing merged")
			}
		}
		refused, err := judgeHold(c, a.task)
		if err != nil {
			return err
		}
		if refused != "" {
			return judgeRefuse(c, a.task, "judge-blocked", refused, 4, "merge refused: "+refused+"; nothing merged")
		}
		// the reason is read once (Python reads it once, before its retry); stdin cannot be read twice, so a retry
		// pass reuses the value, and a later hand-over to Python replays it on a pipe (Deps.StdinReplay)
		if !reasonRead {
			rp, err := readText(c, &a.reason, false)
			if err != nil {
				return err
			}
			reason, reasonRead = *rp, true
			if a.reason == "-" {
				c.deps.StdinReplay = []byte(reason)
			}
		}
		res, err := ghx.Run(j.ghEnv(), "", judgeMergeTimeout, "pr", "merge", t.prURL, "--squash", "--delete-branch", "--match-head-commit", sha)
		var stderr string
		code := res.ExitCode
		switch {
		case err != nil && rn.IsTimeout(err): // outcome unknown, like a 5xx: the squash may have landed
			code, stderr = 124, "gh pr merge timed out"
		case err != nil:
			return errDelegate // gh never started: nothing was merged
		default:
			stderr = res.Stderr
		}
		if code != 0 && strings.Contains(stderr, "Base branch was modified") && !retried {
			if again, err := j.prState(t.prURL); err == nil && again.HeadSHA == sha {
				pr, retried = again, true
				continue
			}
		}
		postErr, shown := "", sha
		if code != 0 {
			unknown := judgeUnknownMerge(stderr) || code == 124
			merged, still := false, false
			landed := ""
			polls := 1
			if unknown {
				polls = judgeMergePolls
				if n, ok := judgeInt(c.env.Getenv("FACTORY_JUDGE_MERGE_POLLS")); ok {
					polls = n
				}
			}
			pollS := float64(judgeMergePollS)
			if v, err := strconv.ParseFloat(c.env.Getenv("FACTORY_JUDGE_MERGE_POLL_S"), 64); err == nil {
				pollS = v
			}
			for i := 0; i < polls; i++ {
				if i > 0 && pollS > 0 {
					time.Sleep(time.Duration(pollS * float64(time.Second)))
				}
				merged = false
				if st, err := j.prState(t.prURL); err == nil {
					merged = st.State == "MERGED"
				}
				still = !merged
				if unknown {
					if repoURL != "" {
						if landed, err = merge.MainLanding(rn, pr.Number, repoURL, ownerRepo, sha); err != nil {
							landed = "" // a gh that cannot start: not verified, so not landed
						}
					}
					merged = merged || landed != ""
					shown = sha
					if landed != "" {
						shown = landed
					}
				}
				if merged {
					break
				}
			}
			if !merged {
				return judgeRefuse(c, a.task, "merge-failed", pyStrip(stderr), 5, "gh pr merge failed: "+pyStrip(stderr))
			}
			postErr = pyStrip(stderr)
			if still && landed != "" { // on main but GitHub still says OPEN: close it with the pointer
				if e := judgeClosePR(j, t.prURL, landed, reason); e != "" {
					postErr += "; PR not closed: " + e
				}
			}
		}
		var carried []merge.Outcome
		for _, o := range outcomes {
			if o.Kind == merge.Carried {
				carried = append(carried, o)
			}
		}
		return judgeRecordMerged(c, j, t, sha, shown, reason, carried, postErr)
	}
}

// judgeClosePR is close_landed_pr: "" or the error text.
func judgeClosePR(j *judgeEnv, prURL, mainSHA, reason string) string {
	comment := fmt.Sprintf("Already merged to main as %s (squash). Closing this PR; recorded by `factory judge`: %s", mainSHA, reason)
	res, err := ghx.Run(j.ghEnv(), "", judgeCloseTimeout, "pr", "close", prURL, "--comment", comment)
	if err != nil {
		return err.Error()
	}
	if res.ExitCode == 0 {
		return ""
	}
	if e := pyStrip(res.Stderr); e != "" {
		return e
	}
	return fmt.Sprintf("gh pr close exit %d", res.ExitCode)
}

var judgeSQLiteTail = regexp.MustCompile(` \(\d+\)$`)

// judgeSQLiteText turns the SQLite driver's error text into what Python's sqlite3 str(e) says: the driver prefixes the
// result class ("constraint failed: ", "SQL logic error: ") and appends the result code " (1811)".
func judgeSQLiteText(s string) string {
	for _, pre := range []string{"constraint failed: ", "SQL logic error: "} {
		if strings.HasPrefix(s, pre) {
			s = judgeSQLiteTail.ReplaceAllString(strings.TrimPrefix(s, pre), "")
			break
		}
	}
	return s
}
