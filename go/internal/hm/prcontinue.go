package hm

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// A re-filed task (`task add --refile-of`) continues the old task's branch and PR, and a PR that no task will finish
// is closed with the reason. The branch is always kept.

const (
	prDropGrace = 30 * time.Minute // how long a failed run waits for a re-file before its PR is closed
	prSweepCap  = 10               // PRs looked at in one pass: the gh rate limit is shared
	prClosedKey = "pr_closed:"     // + URL: when the runner closed this PR, so a run that continues it may reopen it
	prTriedKey  = "pr_tried:"      // + task: when closing its PR last failed; the next try waits prDropGrace
	prClockSkew = 5 * time.Minute  // between gh's closedAt and the runner's record of its own close
	prSweptKey  = "pr_swept:"      // + task: the Dispatched of the run whose PR is settled
)

var taskBranch = regexp.MustCompile(`^factory/(t-[0-9]+)$`)

// refileOrigins maps each re-filed task to the task it files again: the --refile-of flag, else the title marker.
func refileOrigins(tasks []refileRow) map[string]string {
	project, flagged := map[string]string{}, map[string]bool{}
	for _, t := range tasks {
		project[t.id] = t.project
		flagged[t.of] = true
	}
	origin := map[string]string{}
	for _, t := range tasks {
		of := t.of
		// The flag beats titles, also when its task is dead: on a split, only the flagged piece takes over.
		if m := refileTitle.FindStringSubmatch(t.title); of == "" && m != nil {
			if id := fmt.Sprintf("T-%04d", taskNum(strings.ToUpper(m[1]))); !flagged[id] {
				of = id
			}
		}
		// only a later id of the same project re-files an earlier one, so the chains cannot loop
		if of != "" && project[of] == t.project && taskNum(t.id) > taskNum(of) {
			origin[t.id] = of
		}
	}
	return origin
}

// refileChain lists the tasks that task files again, the nearest first.
func (r *Runner) refileChain(task string) []string {
	s, err := r.sq()
	if err != nil {
		return nil
	}
	rows, err := s.refileRows()
	if err != nil {
		r.printf("re-files of %s: %v\n", task, err)
		return nil
	}
	origin := refileOrigins(rows)
	var out []string
	for o := origin[task]; o != ""; o = origin[o] {
		out = append(out, o)
	}
	return out
}

// continuedTask is the lower-case id of the older task whose branch the run continues; "" for a run on its own branch.
// Start fixes it on the run: a later edit of a title or a re-file link cannot move a live session's push.
func (r *Runner) continuedTask(run *Run) string { return run.Continues }

// continuesFrom is Run.Continues of a new run of task on branch.
func (r *Runner) continuesFrom(task, branch string) string {
	m := taskBranch.FindStringSubmatch(branch)
	if m == nil || m[1] == strings.ToLower(task) {
		return ""
	}
	for _, o := range r.refileChain(task) {
		if strings.ToLower(o) == m[1] {
			return m[1]
		}
	}
	return ""
}

// branchUser is another task whose unended (or owner-held) run works on the branch or on the PR; "" = none.
func (r *Runner) branchUser(task, repo, branch, url string) (string, error) {
	runs, err := r.Store.Runs()
	if err != nil {
		return "", err
	}
	for _, x := range runs {
		if x.Task != task && (Active(x.State) || heldForOwner(&x)) && x.Repo == repo &&
			(x.Branch == branch || (url != "" && x.PRURL == url)) {
			return x.Task, nil
		}
	}
	return "", nil
}

// continuable: no other run works on the branch or its PR, and the PR is not merged. An unreadable PR is an error:
// the start waits for a later pass instead of pushing to a branch that may have landed.
func (r *Runner) continuable(t *Task, branch, url string) (bool, error) {
	user, err := r.branchUser(t.ID, t.GHRepo, branch, url)
	if err != nil || user != "" {
		return false, err
	}
	if url == "" {
		return true, nil
	}
	pr, err := PRView(r.Tools, url)
	if err != nil {
		return false, err
	}
	return pr.State != "MERGED", nil
}

// refiledBranch is the branch of the task that t files again, when t can continue it; "" = none.
func (r *Runner) refiledBranch(t *Task) (string, error) {
	for _, o := range r.refileChain(t.ID) {
		old, err := r.Store.Task(o)
		if err != nil {
			return "", err
		}
		branch, url := old.Branch, old.PRURL
		run, err := r.Store.Run(o)
		if err != nil {
			return "", err
		}
		if run != nil {
			if run.Eval {
				continue
			}
			branch, url = or(run.Branch, branch), or(run.PRURL, url)
			if run.State == Merged {
				break
			}
		}
		if branch == "" {
			continue // it never ran: the one before it
		}
		if !taskBranch.MatchString(branch) {
			break // the worker guard allows a push to factory/t-N only
		}
		if ok, err := r.continuable(t, branch, url); err != nil || !ok {
			return "", err
		}
		return branch, nil
	}
	return "", nil
}

// startBranch is the branch of a new run: the one recorded for the task, else the branch of the task it files again,
// else factory/<id>. A continued branch is checked again on every start; an own branch that another run continues
// refuses the start.
func (r *Runner) startBranch(t *Task) (string, error) {
	own := "factory/" + strings.ToLower(t.ID)
	switch {
	case t.Branch == "":
		if branch, err := r.refiledBranch(t); err != nil || branch != "" {
			return branch, err
		}
	case t.Branch != own:
		if !taskBranch.MatchString(t.Branch) {
			return t.Branch, nil
		}
		if ok, err := r.continuable(t, t.Branch, t.PRURL); err != nil || ok {
			return t.Branch, err
		}
	}
	user, err := r.branchUser(t.ID, t.GHRepo, own, "")
	if err != nil {
		return "", err
	}
	if user != "" {
		return "", fmt.Errorf("%w: %s: the run of %s works on %s", ErrRefused, t.ID, user, own)
	}
	return own, nil
}

func refileNote(task, old string) string {
	return fmt.Sprintf("Re-filed as %s: it continues the branch and PR of %s.", task, strings.ToUpper(old))
}

func withNote(body, note string) string {
	if strings.Contains(body, note) {
		return body
	}
	return strings.TrimRight(body, "\n") + "\n\n" + note + "\n"
}

// continuedNote tells the session of a re-filed task where its work goes.
func continuedNote(run *Run, old string) string {
	pr := "has no open PR yet"
	if run.PRURL != "" {
		pr = "has the open PR " + run.PRURL
	}
	return fmt.Sprintf("\n\nThis task files %s again and continues its branch %s, which %s. Build on the commits there, push with "+
		"`git push origin HEAD:%s`, and run `hivemill pr create` as usual: it updates that PR instead of opening a second one.",
		strings.ToUpper(old), run.Branch, pr, run.Branch)
}

// closedByUs: the PR's last close is the runner's own (a person who reopened and closed it again moved closedAt).
func (r *Runner) closedByUs(url string, pr *PR) bool {
	ours, err1 := time.Parse(time.RFC3339, r.Store.Meta(prClosedKey+url))
	closed, err2 := time.Parse(time.RFC3339, pr.ClosedAt)
	return err1 == nil && err2 == nil && ours.Sub(closed).Abs() < prClockSkew
}

// adoptPR records on a new run the PR it continues: the open one of the re-filed branch, or one the runner closed
// itself, which it reopens. A PR somebody else closed stays closed, and the task forgets it.
func (r *Runner) adoptPR(run *Run, t *Task) {
	old := r.continuedTask(run)
	var urls []string
	add := func(task string) {
		if x, err := r.Store.Run(task); err == nil && x != nil {
			urls = append(urls, x.PRURL)
		}
		if x, err := r.Store.Task(task); err == nil {
			urls = append(urls, x.PRURL)
		}
	}
	add(t.ID)
	if old != "" {
		for _, o := range r.refileChain(t.ID) {
			add(o)
		}
		urls = append(urls, r.openPR(run))
	}
	seen := map[string]bool{"": true}
	for _, url := range urls {
		if seen[url] || (old == "" && r.Store.Meta(prClosedKey+url) == "") {
			continue
		}
		seen[url] = true
		pr, err := PRView(r.Tools, url)
		if err != nil {
			r.printf("%s: %v\n", t.ID, err)
			continue
		}
		if pr.HeadRefName != "" && pr.HeadRefName != run.Branch {
			continue
		}
		if pr.State == "CLOSED" && r.closedByUs(url, pr) {
			if rc, _, errs := r.Tools.GH("", "pr", "reopen", url); rc != 0 {
				r.printf("%s: reopen %s: %s\n", t.ID, url, firstLine(errs))
				continue
			}
		} else if pr.State != "OPEN" {
			continue
		}
		_ = r.Store.SetMeta(prClosedKey+url, "")
		run.PRURL, run.RescuedHead = url, pr.HeadRefOid // the old head is not this run's work to rescue
		_ = r.Store.TaskPR(t.ID, url, "")
		if old != "" {
			if body := withNote(pr.Body, refileNote(t.ID, old)); body != pr.Body {
				if rc, _, errs := r.Tools.GH(body, "pr", "edit", url, "--body-file", "-"); rc != 0 {
					r.printf("%s: note the re-file on %s: %s\n", t.ID, url, firstLine(errs))
				}
			}
		}
		return
	}
	// the gate prefers the task's recorded PR: a closed one, or one of another branch, would fail the new run
	if t.PRURL == "" {
		return
	}
	if pr, err := PRView(r.Tools, t.PRURL); err == nil && (pr.State == "CLOSED" || (pr.HeadRefName != "" && pr.HeadRefName != run.Branch)) {
		_ = r.Store.TaskPR(t.ID, "", "")
	}
}

// prSweep is one reading of the tasks, runs and re-files.
type prSweep struct {
	runs      map[string]*Run
	status    map[string]string
	refiledBy map[string][]string
}

func (r *Runner) readSweep() (*prSweep, error) {
	s, err := r.sq()
	if err != nil {
		return nil, err
	}
	rows, err := s.refileRows()
	if err != nil {
		return nil, err
	}
	runs, err := s.Runs()
	if err != nil {
		return nil, err
	}
	w := &prSweep{runs: map[string]*Run{}, status: map[string]string{}, refiledBy: map[string][]string{}}
	for i := range runs {
		w.runs[runs[i].Task] = &runs[i]
	}
	origin := refileOrigins(rows)
	for _, t := range rows {
		w.status[t.id] = t.status
		if o := origin[t.id]; o != "" {
			w.refiledBy[o] = append(w.refiledBy[o], t.id)
		}
	}
	return w, nil
}

// gone: nothing will finish this task: it is cancelled, or its run failed longer ago than the grace.
func (w *prSweep) gone(task string) bool {
	run := w.runs[task]
	if w.status[task] == "cancelled" {
		return run == nil || !Active(run.State)
	}
	if run == nil || run.State != Failed || heldForOwner(run) || w.status[task] != "blocked" {
		return false
	}
	ended, err := time.Parse(time.RFC3339, run.Ended)
	return err == nil && time.Since(ended) >= prDropGrace
}

// carried: a re-file of the task, or a re-file of that one, may still finish the work.
func (w *prSweep) carried(task string) bool {
	return slices.ContainsFunc(w.refiledBy[task], func(id string) bool { return !w.gone(id) || w.carried(id) })
}

// landed is the merged run of a re-file of the task; nil = none.
func (w *prSweep) landed(task string) *Run {
	for _, id := range w.refiledBy[task] {
		if run := w.runs[id]; run != nil && run.State == Merged && !run.Eval {
			return run
		}
		if run := w.landed(id); run != nil {
			return run
		}
	}
	return nil
}

// sweepPRs closes the PRs of cancelled tasks, of failed runs that nobody re-filed in time, and of tasks that a landed
// re-file superseded. A gh error leaves the task for a later pass.
func (r *Runner) sweepPRs() {
	w, err := r.readSweep()
	if err != nil {
		r.printf("close dead PRs: %v\n", err)
		return
	}
	var ids []string
	for id := range w.runs {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b string) int { return taskNum(b) - taskNum(a) }) // new ones first: an old failure starves nothing
	left := prSweepCap
	for _, id := range ids {
		run := w.runs[id]
		if run.Eval || run.State == Merged || Active(run.State) || r.Store.Meta(prSweptKey+id) == run.Dispatched {
			continue
		}
		if tried, err := time.Parse(time.RFC3339, r.Store.Meta(prTriedKey+id)); err == nil && time.Since(tried) < prDropGrace {
			continue
		}
		var why, landedPR string
		// a run held for the owner is theirs to decide: only their cancel closes its PR
		if next := w.landed(id); next != nil && (!heldForOwner(run) || w.status[id] == "cancelled") {
			why, landedPR = supersededBy(next.PRURL), next.PRURL
		} else if w.gone(id) && !w.carried(id) {
			if w.status[id] == "cancelled" {
				why = id + " was cancelled."
			} else {
				why = fmt.Sprintf("the run of %s failed and no re-file took it over within %d minutes.\n\nReason: %s", id,
					int(prDropGrace.Minutes()), truncate(firstLine(run.FailReason), 300))
			}
		} else {
			continue
		}
		if r.closePR(run, why, landedPR) {
			if left--; left == 0 {
				return
			}
		}
	}
}

func supersededBy(url string) string {
	return "superseded by #" + url[strings.LastIndexByte(url, '/')+1:]
}

// closeSuperseded closes the PRs of the tasks that the landed run filed again. The run is not saved as merged yet, so
// this does not read it from the store; sweepPRs retries what fails here.
func (r *Runner) closeSuperseded(landed *Run) {
	if landed.Eval || landed.PRURL == "" {
		return
	}
	for _, o := range r.refileChain(landed.Task) {
		run, err := r.Store.Run(o)
		if err != nil || run == nil || run.Eval || run.State == Merged || Active(run.State) || heldForOwner(run) {
			continue
		}
		r.closePR(run, supersededBy(landed.PRURL), landed.PRURL)
	}
}

// closePR closes the open PR of an ended run with why as the comment, and keeps the branch. It reports whether it
// called gh, which is what the sweep's cap counts.
func (r *Runner) closePR(run *Run, why, landedPR string) (called bool) {
	settled := func() { _ = r.Store.SetMeta(prSweptKey+run.Task, run.Dispatched) }
	failed := func(format string, a ...any) {
		r.printf("%s: "+format+"\n", append([]any{run.Task}, a...)...)
		_ = r.Store.SetMeta(prTriedKey+run.Task, nowISO())
	}
	url := run.PRURL
	if t, err := r.Store.Task(run.Task); err == nil && url == "" {
		url = t.PRURL
	}
	if url == "" {
		called = true
		rc, out, errs := r.listOpenPR(run.Repo, run.Branch)
		if rc != 0 {
			failed("find its PR: %s", firstLine(errs))
			return
		}
		url = strings.TrimSpace(out)
	}
	if url == "" || url == landedPR {
		settled()
		return
	}
	if user, err := r.branchUser(run.Task, run.Repo, run.Branch, url); err != nil || user != "" {
		return // another run works on it: look again when it ends
	}
	called = true
	pr, err := PRView(r.Tools, url)
	if err != nil {
		failed("%v", err)
		return
	}
	if pr.State != "OPEN" || (pr.HeadRefName != "" && pr.HeadRefName != run.Branch) {
		settled()
		return
	}
	comment := "hivemill closed this PR: " + why
	// the fail reason is free text: a public repo gets it only when the public-text check passes
	if public, err := r.publicTextRepo(run.Repo); err != nil || (public && r.prTextHits(comment, "", run.Branch) != "") {
		comment, _, _ = strings.Cut(comment, "\n\nReason:")
	}
	comment = ScrubSecrets(comment) + "\n\nThe branch `" + run.Branch + "` is kept."
	if rc, _, errs := r.Tools.GH("", "pr", "close", url, "--comment", comment); rc != 0 {
		failed("close %s: %s", url, firstLine(errs))
		return
	}
	settled()
	_ = r.Store.SetMeta(prClosedKey+url, nowISO())
	r.printf("%s: closed %s\n", run.Task, url)
	_ = r.Log.Emit("pr.closed", run, Event{Data: map[string]any{"pr": url, "why": truncate(why, 900)}})
	return
}
