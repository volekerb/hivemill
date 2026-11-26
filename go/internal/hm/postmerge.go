package hm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// A project's post_merge_cmd (`hivemill project profile ID --post-merge-cmd CMD`): after each merge the runner runs it in
// the project's main checkout with the merge commit in $HIVEMILL_MERGE_SHA (a deploy wait, a smoke check). Exit 0: main
// is green for the project. Anything else: main is red, as after a red push run (merges hold, auto_revert applies), and
// one fix-forward task is filed. It runs in the background, one per project at a time, and the project's next merge
// waits for it.

// States of Run.PostMerge.
const (
	PostMergePending = "pending"
	PostMergeOK      = "ok"
	PostMergeRed     = "red"
)

const (
	postMergeRunning = "post_merge_cmd of the project's last merge has not finished"
	postMergeHeld    = "main is red: post_merge_cmd failed; only its fix-forward or revert task merges"
	postMergeSource  = "post_merge" // "source" of a red-main record post_merge_cmd wrote
	postMergeTail    = 60           // lines of output in the fix-forward task
	postMergeLine    = 500          // bytes of one such line
)

// a token's prefix, a URL with a password, a bearer value, a credential flag, or a credential's name assigned a value
// (also inside AWS_SECRET_ACCESS_KEY)
var secretLine = regexp.MustCompile(`\b(ghp_|gho_|ghs_|ghu_|ghr_|github_pat_|sk-|xox[abprs]-)[A-Za-z0-9_-]{4,}|\bAKIA[0-9A-Z]{12,}|` +
	`://[^/\s:]*:[^@\s]+@|(?i)bearer[ \t]+[A-Za-z0-9._~+/=-]{8,}|--(token|password|secret|api-?key)[ \t]+[^\s<]|` +
	`(authorization|token|password|passwd|_pass|pgpass|_pwd|secret|api[_-]?key|private[_-]?key)[A-Za-z0-9_-]*["']?[ \t]*[:=][ \t]*["']?[^\s<"']`)

const redactedLine = "<redacted: looks like a credential>"

// redactOutput drops the lines that look like they carry a credential, PEM blocks included, and bounds the others: the
// text goes into a task spec.
func (r *Runner) redactOutput(text string) string {
	lines := strings.Split(r.redactHomes(text), "\n")
	inPEM := false
	for i, l := range lines {
		begin, end := strings.Contains(l, "-----BEGIN"), strings.Contains(l, "-----END")
		if end && !inPEM && !begin { // the tail starts inside a block: everything before this line is part of it
			for j := range i {
				lines[j] = redactedLine
			}
		}
		if inPEM || begin || end || secretLine.MatchString(l) {
			lines[i] = redactedLine
		} else {
			lines[i] = truncate(l, postMergeLine)
		}
		inPEM = (inPEM || begin) && !end
	}
	return strings.Join(lines, "\n")
}

// mayMergeRed: the task may merge while post_merge_cmd's record holds the repo: its fix-forward task, the revert task,
// the task the director named (`factory redmain fix`), or the fix task of the monitor's record underneath.
func mayMergeRed(st map[string]any, task string) bool {
	prev, _ := st["prev"].(map[string]any)
	return task != "" && (st["task"] == task || st["revert"] == task || st["named"] == task || prev["task"] == task)
}

// isPostMergeFix: the task is the one filed to clear its repo's post_merge red.
func (r *Runner) isPostMergeFix(id string) bool {
	t, err := r.Store.Task(id)
	if err != nil || t == nil || t.GHRepo == "" {
		return false
	}
	st, red := r.Store.RedMain(strings.ToLower(t.GHRepo))
	return red && fromPostMerge(st) && st["task"] == id
}

// nameRevert lets the revert task merge. A post_merge record keeps its fix-forward task and gets the revert beside it.
func (r *Runner) nameRevert(slug, id string) error {
	if l := r.lock("merge", true); l != nil {
		defer l.Close()
	}
	if st, red := r.Store.RedMain(slug); red && fromPostMerge(st) {
		st["revert"] = id
		return r.Store.SetRedMain(slug, st)
	}
	return r.Store.SetRedMainFix(slug, id)
}

// startBlock is runCapBlock for one task: max_waiting does not hold the fix-forward task, because the runs that wait
// may be waiting for it.
func (r *Runner) startBlock(id string) string {
	if !r.isPostMergeFix(id) {
		return r.runCapBlock()
	}
	working, _ := r.runCounts()
	if limit := cfgInt(r.Store, "max_concurrent"); working >= limit {
		return fmt.Sprintf("max_concurrent=%d runs are working", limit)
	}
	return ""
}

// StopOn makes the end of ctx kill a running post_merge_cmd, which then stays pending.
func (r *Runner) StopOn(ctx context.Context) { r.stop = ctx }

// context ends when the daemon or an interrupted pass stops.
func (r *Runner) context() context.Context {
	if r.stop != nil {
		return r.stop
	}
	return context.Background()
}

// postMergeCfg is the project's post_merge_cmd ("" = none) and its timeout. An error means unknown, never "none".
func (r *Runner) postMergeCfg(project string) (string, time.Duration, error) {
	s, ok := r.Store.(*SQLite)
	if !ok || project == "" {
		return "", 0, nil
	}
	p, err := s.Profile(project)
	if err != nil {
		return "", 0, fmt.Errorf("profile of %s: %w", project, err)
	}
	if p.PostMergeSecs <= 0 {
		p.PostMergeSecs = 900
	}
	return p.PostMergeCmd, time.Duration(p.PostMergeSecs) * time.Second, nil
}

func fromPostMerge(st map[string]any) bool { return st["source"] == postMergeSource }

// redCause and redClearer word a red-main record in the revert task and PR.
func redCause(st map[string]any) string {
	if fromPostMerge(st) {
		return "post_merge_cmd failed"
	}
	return fmt.Sprintf("main's push run %v failed", st["run"])
}

func redClearer(st map[string]any) string {
	if fromPostMerge(st) {
		return "a green post_merge_cmd"
	}
	return "the red-main monitor"
}

// postMergePending: another merge of the run's project still waits for its post_merge_cmd. Unreadable runs count as one.
func (r *Runner) postMergePending(run *Run) bool {
	runs, err := r.Store.Runs()
	if err != nil {
		return true
	}
	for _, x := range runs {
		if x.Task != run.Task && x.Project == run.Project && x.PostMerge == PostMergePending {
			return true
		}
	}
	return false
}

// postMergeHold is why post_merge_cmd holds the run's merge right now ("" = it does not).
func (r *Runner) postMergeHold(run *Run) string {
	if r.postMergePending(run) {
		return postMergeRunning
	}
	if r.postMergeRedHolds(run) {
		return postMergeHeld
	}
	return ""
}

// postMergeRedHolds: a red post_merge_cmd holds the run, which is not one of the tasks that may merge on that red.
func (r *Runner) postMergeRedHolds(run *Run) bool {
	st, red := r.Store.RedMain(strings.ToLower(run.Repo))
	return red && fromPostMerge(st) && !mayMergeRed(st, run.Task)
}

// PostMergeCheck starts the oldest pending post_merge_cmd of every project that has none running. It does not wait:
// WaitPostMerge does. A command cut off by a stop or with its process stays pending and runs again. A merge whose
// commit is not known yet is pending too (it holds the project), but there is nothing to run it on.
func (r *Runner) PostMergeCheck() {
	runs, err := r.Store.Runs()
	if err != nil {
		r.printf("post-merge check: %v\n", err)
		return
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].Merged < runs[j].Merged })
	seen := map[string]bool{}
	for _, x := range runs {
		if x.PostMerge != PostMergePending || x.MergeSHA == "" || seen[x.Project] {
			continue
		}
		seen[x.Project] = true
		l := r.lock("postmerge-"+x.Project, false)
		if l == nil {
			continue // running
		}
		r.postMerges.Add(1)
		go func(task string) {
			defer r.postMerges.Done()
			defer l.Close()
			if err := r.runPostMerge(task); err != nil {
				r.printf("%s: post_merge_cmd: %v\n", task, err)
				if r.context().Err() == nil { // not our own shutdown: the project's merges hold until someone looks
					_ = r.Store.AlertFeed("post-merge-stuck", task, err.Error())
				}
			}
		}(x.Task)
	}
}

// WaitPostMerge returns when the commands this process started have finished (a one-shot pass must not exit before).
func (r *Runner) WaitPostMerge() { r.postMerges.Wait() }

// runPostMerge runs the command for one merged run and records the result. The caller holds the project's lock.
func (r *Runner) runPostMerge(task string) error {
	run, err := r.Store.Run(task)
	if err != nil || run == nil || run.PostMerge != PostMergePending {
		return err // another process finished it since the list was read
	}
	cmd, limit, err := r.postMergeCfg(run.Project)
	if err != nil {
		return err
	}
	if cmd == "" { // the setting was removed after the merge
		return r.recordPostMerge(task, func(x *Run) error { x.PostMerge = ""; return nil })
	}
	t, err := r.Store.Task(task)
	if err != nil || t == nil || t.RepoPath == "" {
		return fmt.Errorf("no project checkout for %s: %v", task, err)
	}
	if _, err := os.Stat(t.RepoPath); err != nil { // the command cannot run: that is not a red main
		return fmt.Errorf("project checkout for %s: %w", task, err)
	}
	// one stream: the tail of the output keeps the order the command wrote it in
	sh := r.Tools.Sh
	if r.Tools.ShGroup != nil { // a timeout or a stopping daemon kills everything the command started
		sh = func(dir string, limit time.Duration, env []string, argv ...string) (int, string) {
			return r.Tools.ShGroup(r.context(), dir, limit, env, argv...)
		}
	}
	rc, out := sh(t.RepoPath, limit, append(cleanEnv(), "HIVEMILL_MERGE_SHA="+run.MergeSHA), "bash", "-c", "exec 2>&1\n"+cmd)
	// our own stop runs it again, also when the command traps the signal and exits (with 0 too); anything else (a crash) is red
	select {
	case <-r.context().Done(): // a service stop signals the command and us at once
		return fmt.Errorf("cut off by a stop; it runs again on a later pass")
	case <-time.After(time.Second):
	}
	dir := filepath.Join(r.Env.StateDir(), "runs", task)
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "post-merge.log"), []byte(out), 0o600)
	return r.recordPostMerge(task, func(x *Run) error {
		now, _, err := r.postMergeCfg(x.Project)
		if err != nil {
			return err
		}
		if now == "" { // removed while it ran: its result no longer counts
			x.PostMerge = ""
			return nil
		}
		if rc == 0 {
			return r.postMergeGreen(x)
		}
		return r.postMergeFailed(x, cmd, rc, out)
	})
}

// recordPostMerge writes a result under the task lock and the merge lock (the order of a merge), so neither a merge nor
// the step that finished the run overwrites it or slips in between.
func (r *Runner) recordPostMerge(task string, apply func(*Run) error) error {
	for _, name := range []string{task, "merge"} {
		if l := r.lock(name, true); l != nil {
			defer l.Close()
		}
	}
	run, err := r.Store.Run(task)
	if err != nil || run == nil {
		return err
	}
	if err := apply(run); err != nil {
		return err // still pending: it runs again
	}
	return r.Store.PutRun(run)
}

// dropPostMergeRed ends a red-main record post_merge_cmd wrote and puts back the monitor's record it replaced.
func (r *Runner) dropPostMergeRed(slug string, st map[string]any) error {
	prev, _ := st["prev"].(map[string]any)
	return r.Store.SetRedMain(slug, prev)
}

// postMergeGreen clears the red-main record an earlier post_merge_cmd wrote; the monitor's own records are left to it.
func (r *Runner) postMergeGreen(run *Run) error {
	slug := strings.ToLower(run.Repo)
	if st, red := r.Store.RedMain(slug); red && fromPostMerge(st) {
		if err := r.dropPostMergeRed(slug, st); err != nil {
			return err
		}
	}
	run.PostMerge = PostMergeOK
	_ = r.Store.Event(r.actor(), "task", run.Task, "post-merge-ok", run.MergeSHA)
	_ = r.Log.Emit("post_merge.ok", run, Event{Data: map[string]any{"merge_sha": run.MergeSHA, "pr": run.PRURL}})
	return nil
}

// postMergeFailed marks main red first (fail closed), then files the one fix-forward task and names it the task that
// may merge while main is red.
func (r *Runner) postMergeFailed(run *Run, cmd string, rc int, out string) error {
	slug := strings.ToLower(run.Repo)
	why := fmt.Sprintf("exit %d", rc)
	if rc == 124 {
		why = "timed out"
	} else if rc < 0 {
		why = "killed by a signal"
	}
	rec := map[string]any{"source": postMergeSource, "run": postMergeSource, "sha": run.MergeSHA, "url": run.PRURL,
		"since": nowISO(), "task": ""}
	open := "" // the fix-forward of an earlier red that is still to land: it stays the one
	if old, red := r.Store.RedMain(slug); red {
		if !fromPostMerge(old) {
			rec["prev"] = old
		} else {
			for _, k := range []string{"prev", "revert", "named"} {
				if old[k] != nil {
					rec[k] = old[k]
				}
			}
			if id, _ := old["task"].(string); id != "" && id != run.Task {
				if t, err := r.Store.Task(id); err == nil && t != nil && t.Status != "done" && t.Status != "cancelled" {
					open, rec["task"] = id, id
				}
			}
		}
	}
	if err := r.Store.SetRedMain(slug, rec); err != nil {
		return err
	}
	title := fmt.Sprintf("Fix forward %s: post_merge_cmd red after its merge", run.Task)
	id, goal := open, run.Goal
	var err error
	if id == "" {
		if id, err = r.Store.TaskByTitle(run.Goal, title); err != nil {
			return err
		}
		// reused only while open (a retry after a crash): a closed one fixes nothing any more
		if t, err := r.Store.Task(id); id != "" && err == nil && t != nil && (t.Status == "done" || t.Status == "cancelled") {
			id = ""
		}
	} else if t, err := r.Store.Task(id); err != nil {
		return err
	} else {
		goal = t.Goal
	}
	if id == "" {
		spec := fmt.Sprintf("post_merge_cmd of project %s failed (%s) on %.12s, the merge of %s (%s). Main is red: the project's "+
			"merges hold; only this task and a revert or red-main fix task may merge. Find the cause and fix forward in one PR with that one purpose.\n\n"+
			"Command: `%s`\n\nLast %d lines of its output:\n\n```\n%s\n```\n", run.Project, why, run.MergeSHA, run.Task, run.PRURL,
			r.redactOutput(cmd), postMergeTail,
			strings.ReplaceAll(r.redactOutput(lastLines(out, postMergeTail)), "```", "'''")) // the output stays inside its fence
		if id, err = r.Store.NewTask(run.Goal, title, spec,
			"1. post_merge_cmd exits 0 on this task's merge commit - manual: the runner runs it after the merge"); err != nil {
			return err
		}
	}
	if err := r.Store.SetRedMainFix(slug, id); err != nil {
		return err
	}
	if err := r.reopenGoal(goal, id); err != nil {
		return err
	}
	run.PostMerge = PostMergeRed
	_ = r.Store.Event(r.actor(), "task", run.Task, "post-merge-red", why+"; fix-forward "+id)
	_ = r.Log.Emit("post_merge.red", run, Event{Data: map[string]any{"merge_sha": run.MergeSHA, "pr": run.PRURL, "why": why, "fix_task": id}})
	return nil
}

// reopenGoal: only an active goal's tasks start, and the goal may have closed while the command ran. A goal someone
// cancelled or blocked stays so, with an alert: the fix-forward task will not start by itself.
func (r *Runner) reopenGoal(goal, fix string) error {
	s, ok := r.Store.(*SQLite)
	if !ok {
		return nil
	}
	var status string
	if err := s.db.QueryRow(`SELECT status FROM goals WHERE id=?`, goal).Scan(&status); err != nil {
		return fmt.Errorf("status of goal %s: %w", goal, err)
	}
	switch status {
	case "active":
		return nil
	case "done":
		return s.SetGoalStatus(goal, "active")
	}
	return r.Store.AlertFeed("post-merge-stuck", fix, fmt.Sprintf("goal %s is %s: the fix-forward task does not start", goal, status))
}

// ClearPostMergeRed drops the red-main record post_merge_cmd wrote for a project (its command was removed).
func (r *Runner) ClearPostMergeRed(project string) error {
	s, ok := r.Store.(*SQLite)
	if !ok {
		return nil
	}
	if l := r.lock("merge", true); l != nil { // not between a finishing command's check and its write
		defer l.Close()
	}
	ps, err := s.Projects()
	if err != nil {
		return err
	}
	for _, p := range ps {
		slug := strings.ToLower(p.GHRepo)
		if st, red := r.Store.RedMain(slug); p.ID == project && red && fromPostMerge(st) {
			return r.dropPostMergeRed(slug, st)
		}
	}
	return nil
}
