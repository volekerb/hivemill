package skilleval

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"factory/go/internal/sandbox"
	"factory/go/internal/scout"
)

// ExitError is a refusal with the exit code Python's die(msg, code) used (2 usage/eligibility, 3 sandbox, 4 pre-screen).
type ExitError struct {
	Code int
	Msg  string
}

func (e *ExitError) Error() string { return e.Msg }

func die(code int, format string, a ...any) error {
	return &ExitError{Code: code, Msg: fmt.Sprintf(format, a...)}
}

// Info is what a Builder returns for one built trial sandbox.
type Info struct {
	Dir, Work, Plugin, Agent, Shim string
	Ns                             *sandbox.Cfg
}

// Builder builds and preflights the sandbox of one trial (scout/sandbox.py build_sandbox and preflight).
type Builder interface {
	Build(db *sql.DB, t Trial, root, home, unshare string) (Info, error)
	Preflight(info Info) error
}

// Spawner starts the evaluator agent through the factory spawn path (the gate may queue it). It returns
// *SpawnRefused when the spawn was refused.
type Spawner func(db *sql.DB, name, cwd, prompt string, envExtra [][2]string, pluginDirs []string) error

// Env is everything the CLI-level operations need from the surrounding factory (Python: cfg(), now(), event(), actor(),
// HOME, eval_test_seams(), spawn and force_stop).
type Env struct {
	Collector
	ControlDir string // <repo>/skilleval/controls/marker
	BinDir     string // the dir holding the factory CLI, put on the evaluator's PATH
	Actor      string
	Home       string // HOME: the factory home
	PasswdHome string // eval_test_seams()[0]
	UnshareBin string // eval_test_seams()[1]
	Out        io.Writer
	Api        API
	NewGh      func() *scout.Gh
	Builder    Builder
	Spawn      Spawner
}

func (e *Env) printf(format string, a ...any) {
	if _, err := fmt.Fprintf(e.Out, format, a...); err != nil {
		return // a closed stdout is the caller's problem; the DB work is already done
	}
}

func reprNull(s sql.NullString) string {
	if !s.Valid {
		return "None"
	}
	return pyRepr(s.String)
}

func vcand(c scout.Candidate) scout.VCand {
	return scout.VCand{ID: c.ID, Repo: c.Repo.String, Path: c.Path.String, HeadSha: c.HeadSha.String, Licence: c.Licence.String,
		InstallRisk: c.InstallRisk.String, Status: c.Status}
}

func sha9(s string) string {
	if len(s) > 9 {
		return s[:9]
	}
	return s
}

func (e *Env) root(db *sql.DB) (string, error) {
	v, err := e.Api.Cfg(db, "skilleval_root")
	if err != nil {
		return "", fmt.Errorf("read skilleval_root: %w", err)
	}
	r, rerr := scout.ResolveRoot(v, e.Home)
	if rerr != nil {
		return "", die(scout.ExitRefused, "%s", rerr.Error())
	}
	return r, nil
}

func getCand(db *sql.DB, cid string) (scout.Candidate, error) {
	c, ok, err := scout.GetCandidate(db, cid)
	if err != nil {
		return scout.Candidate{}, fmt.Errorf("load candidate %s: %w", cid, err)
	}
	if !ok {
		return scout.Candidate{}, die(2, "no candidate %s", cid)
	}
	return c, nil
}

// VendorCandidate is skilleval_vendor: vendor candidate cid (T6) and return it; it dies on a refusal or a pre-screen hit.
func (e *Env) VendorCandidate(db *sql.DB, cid string) (scout.Candidate, *scout.VendorResult, error) {
	cand, err := getCand(db, cid)
	if err != nil {
		return cand, nil, err
	}
	root, err := e.root(db)
	if err != nil {
		return cand, nil, err
	}
	var res *scout.VendorResult
	var verr error
	if cand.Key == scout.ControlKey { // repo-owned positive control: same re-checks, local source
		cand, err = RegisterControl(db, e.Api.Now(), e.ControlDir) // refresh head_sha if the control dir changed since `control`
		if err != nil {
			return cand, nil, err
		}
		res, verr = scout.VendorLocal(vcand(cand), root, e.ControlDir, ControlPlugin)
	} else {
		res, verr = scout.Vendor(e.NewGh(), vcand(cand), root)
	}
	if verr != nil {
		var ve *scout.VendorError
		if !errors.As(verr, &ve) {
			return cand, nil, fmt.Errorf("vendor %s: %w", cid, verr)
		}
		if ve.Fresh != nil { // the stored risk was stale: keep the fresh one
			if err := scout.SetRisk(db, cid, ve.Fresh.Risk, ve.Fresh.Detail); err != nil {
				return cand, nil, fmt.Errorf("store fresh risk of %s: %w", cid, err)
			}
			if err := e.Api.Event(db, "skilleval", cid, "vendor-refused", runePrefix(ve.Msg, 400)); err != nil {
				return cand, nil, fmt.Errorf("record vendor-refused: %w", err)
			}
		}
		return cand, nil, die(ve.Code, "%s", ve.Msg)
	}
	hits := res.Meta.PrescreenHits
	if res.Reused {
		e.printf("%s@%s already vendored: %s\n", cid, cand.HeadSha.String, res.Dir)
	}
	if len(hits) > 0 {
		lines := scout.HitLines(hits)
		if err := scout.SetNeedsOwner(db, cid, lines); err != nil {
			return cand, nil, fmt.Errorf("hold %s for the owner: %w", cid, err)
		}
		if err := e.Api.Event(db, "skilleval", cid, "vendor-needs-owner", fmt.Sprintf("%d pre-screen hit(s)", len(hits))); err != nil {
			return cand, nil, fmt.Errorf("record vendor-needs-owner: %w", err)
		}
		return cand, nil, die(scout.ExitPrescreen, "%s: %d pre-screen hit(s); copy kept at %s for owner review, status needs-owner (first: %s)",
			cid, len(hits), res.Dir, lines[0])
	}
	if !res.Reused {
		if err := e.Api.Event(db, "skilleval", cid, "vendor", fmt.Sprintf("%d file(s) %s", len(res.Meta.Copied), res.Dir)); err != nil {
			return cand, nil, fmt.Errorf("record vendor: %w", err)
		}
		e.printf("%s@%s vendored: %d file(s), %d skipped, 0 pre-screen hits -> %s/%s\n", cid, cand.HeadSha.String,
			len(res.Meta.Copied), len(res.Meta.Skipped), res.Dir, res.Plugin)
	}
	return cand, res, nil
}

// Rescreen is skilleval_rescreen: re-run the current pre-screen on the kept copy of a candidate held for pre-screen
// hits. 0 hits -> triaged; any hit -> stays needs-owner, exit 4. It never clears a hit by itself.
func (e *Env) Rescreen(db *sql.DB, cid string) error {
	cand, err := getCand(db, cid)
	if err != nil {
		return err
	}
	if cand.Status != "needs-owner" {
		return die(2, "%s is %s, not needs-owner; --rescreen only releases a pre-screen hold", cid, cand.Status)
	}
	held, err := decodeStringList(cand.RiskDetail.String)
	if err != nil || len(held) == 0 {
		return die(2, "%s is held for a reason other than pre-screen hits; --rescreen does not clear it", cid)
	}
	for _, x := range held {
		if !strings.HasPrefix(x, "prescreen ") && !strings.HasPrefix(x, "... and ") {
			return die(2, "%s is held for a reason other than pre-screen hits; --rescreen does not clear it", cid)
		}
	}
	root, err := e.root(db)
	if err != nil {
		return err
	}
	meta, hits, d, rerr := scout.Rescreen(root, vcand(cand))
	if rerr != nil {
		var ve *scout.VendorError
		if errors.As(rerr, &ve) {
			return die(ve.Code, "%s", ve.Msg)
		}
		return fmt.Errorf("rescreen %s: %w", cid, rerr)
	}
	if len(hits) > 0 {
		lines := scout.HitLines(hits)
		if err := scout.SetNeedsOwner(db, cid, lines); err != nil {
			return fmt.Errorf("hold %s for the owner: %w", cid, err)
		}
		if err := e.Api.Event(db, "skilleval", cid, "vendor-rescreen-hit", fmt.Sprintf("%d pre-screen hit(s) remain", len(hits))); err != nil {
			return fmt.Errorf("record vendor-rescreen-hit: %w", err)
		}
		return die(scout.ExitPrescreen, "%s: %d pre-screen hit(s) remain; status stays needs-owner\n%s", cid, len(hits), strings.Join(lines, "\n"))
	}
	old := map[string]int{}
	total := 0
	for _, h := range meta.PrescreenHits {
		old[h.Pattern]++
		total++
	}
	if err := scout.ClearHits(d); err != nil {
		return fmt.Errorf("clear recorded hits of %s: %w", cid, err)
	}
	if _, err := db.Exec("UPDATE scout_candidates SET status='triaged', risk_detail=NULL WHERE id=? AND status='needs-owner'", cid); err != nil {
		return fmt.Errorf("release %s: %w", cid, err)
	}
	keys := make([]string, 0, len(old))
	for k := range old {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s x%d", k, old[k]))
	}
	cleared := strings.Join(parts, ", ")
	if cleared == "" {
		cleared = "none recorded"
	}
	if err := e.Api.Event(db, "skilleval", cid, "vendor-rescreen-clear",
		fmt.Sprintf("%d old pre-screen hit(s), now 0; rules cleared: %s", total, cleared)); err != nil {
		return fmt.Errorf("record vendor-rescreen-clear: %w", err)
	}
	e.printf("%s@%s rescreened: 0 pre-screen hits (was %d: %s); status triaged\n", cid, cand.HeadSha.String, total, cleared)
	return nil
}

// decodeStringList is json.loads(text or "[]") when it must be a list of strings; anything else is an error.
func decodeStringList(text string) ([]string, error) {
	if text == "" {
		text = "[]"
	}
	var raw []any
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return nil, fmt.Errorf("risk_detail is not a JSON list: %w", err)
	}
	out := make([]string, 0, len(raw))
	for _, x := range raw {
		s, ok := x.(string)
		if !ok {
			return nil, errors.New("risk_detail holds a non-string item")
		}
		out = append(out, s)
	}
	return out, nil
}

// Launch is skilleval_launch: start one trial (the row, already claimed as 'starting' by the caller): build its
// sandbox, preflight it, mark it running and spawn the evaluator through the gate (a closed gate queues the agent;
// the trial still counts as running). A sandbox refusal leaves nothing behind and the row 'starting'; a refused spawn
// sets the row to error and returns the *SpawnRefused. The caller owns the refusal handling.
func (e *Env) Launch(db *sql.DB, row Trial, root, home, unshare string) (Info, error) {
	info, err := e.Builder.Build(db, row, root, home, unshare)
	if err != nil {
		return Info{}, err
	}
	if err := e.Builder.Preflight(info); err != nil {
		removeTree(info.Dir)
		return Info{}, err
	}
	d := info.Dir
	pathParts := []string{filepath.Join(d, "bin"), e.BinDir, home + "/.local/bin", "/usr/local/bin", "/usr/bin", "/bin"}
	env := [][2]string{{"FACTORY_HOME", filepath.Join(d, "fh")}, {"HOME", filepath.Join(d, "home")},
		{"CLAUDE_CONFIG_DIR", filepath.Join(d, "cfg")}, {"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "1"},
		{"DISABLE_AUTOUPDATER", "1"}, {"PATH", strings.Join(pathParts, string(os.PathListSeparator))}}
	won, err := Transition(db, row.ID, "running", []string{"starting"}, Col{"agent", info.Agent}, Col{"sandbox", info.Dir}, Col{"started", e.Api.Now()})
	if err != nil {
		return Info{}, err
	}
	if !won {
		removeTree(info.Dir)
		return Info{}, die(3, "trial %d is no longer starting (cancelled or taken over); nothing spawned", row.ID)
	}
	if err := e.Api.Event(db, "skilleval", row.CandidateID, "try",
		fmt.Sprintf("trial %d %s %s agent %s", row.ID, row.Task, row.Condition, info.Agent)); err != nil {
		return Info{}, fmt.Errorf("record try: %w", err)
	}
	var plugins []string
	if info.Plugin != "" {
		plugins = []string{info.Plugin}
	}
	if serr := e.Spawn(db, info.Agent, info.Work, fmt.Sprintf("Run `factory task show %s` and do that task.", sandbox.SeedTask), env, plugins); serr != nil {
		var sr *SpawnRefused
		if errors.As(serr, &sr) {
			if _, err := db.Exec("UPDATE skill_trials SET status='error', check_output='spawn refused' WHERE id=? AND status='running'", row.ID); err != nil {
				return Info{}, fmt.Errorf("record refused spawn of trial %d: %w", row.ID, err)
			}
		}
		return Info{}, serr
	}
	return info, nil
}

var taskNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}$`)

// TryOpts are the arguments of `factory skilleval try`.
type TryOpts struct {
	Task, Candidate, Condition string
	HasCandidate               bool // --candidate was given
}

func listDirs(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if st, err := os.Stat(filepath.Join(dir, e.Name())); err == nil && st.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

func notEligible(cid string, c scout.Candidate, verb string) error {
	return die(2, "%s is not eligible for evaluation (install_risk %s, status %s); only prompt-only candidates that are not "+
		"needs-owner or dismissed are %s", cid, reprNull(c.InstallRisk), pyRepr(c.Status), verb)
}

// Try is skilleval_try: start one trial now, outside the queue.
func (e *Env) Try(db *sql.DB, o TryOpts) error {
	task := o.Task
	if task == "" {
		return die(2, "usage: factory skilleval try <task> [--candidate C-xxxx|none] [--condition with|without]")
	}
	if st, err := os.Stat(filepath.Join(e.TasksDir, task, "meta.json")); !taskNameRE.MatchString(task) || err != nil || !st.Mode().IsRegular() {
		return die(2, "unknown eval task %s; known: %s", pyRepr(task), strings.Join(listDirs(e.TasksDir), ", "))
	}
	baseline := !o.HasCandidate || o.Candidate == "none"
	cond := o.Condition
	if baseline {
		cond = "without"
	} else if cond == "" {
		cond = "with"
	}
	if cond != "with" && cond != "without" {
		return die(2, "--condition must be with or without")
	}
	if baseline && o.Condition == "with" {
		return die(2, "--candidate none runs the baseline only: condition without")
	}
	cid := BaselineID
	var cand scout.Candidate
	if !baseline {
		cid = o.Candidate
		var err error
		if cand, err = getCand(db, cid); err != nil {
			return err
		}
		if !scout.IsEvalAllowed(vcand(cand), "") {
			return notEligible(cid, cand, "run")
		}
	}
	// AC13: everything from here fails closed with exit 3; nothing is created or started before it passes
	token := e.PasswdHome + "/.config/factory/eval-oauth-token"
	berr := sandbox.CheckToken(token)
	if berr == nil {
		berr = sandbox.CheckUserns(e.UnshareBin)
	}
	if berr != nil {
		return die(3, "%s", refusedText(berr))
	}
	root, err := e.root(db)
	if err != nil {
		return err
	}
	var sha string
	if baseline {
		now := e.Api.Now()
		if _, err := db.Exec("INSERT OR IGNORE INTO scout_candidates(id,key,kind,status,first_seen,last_seen) VALUES(?,?,?,?,?,?)",
			BaselineID, "baseline:none", "unknown", "evaluated", now, now); err != nil {
			return fmt.Errorf("register baseline candidate: %w", err)
		}
		sha = BaselineSha
	} else {
		if cond == "with" {
			var res *scout.VendorResult
			if cand, res, err = e.VendorCandidate(db, cid); err != nil {
				return err
			}
			if res != nil && !scout.IsEvalAllowed(vcand(cand), root) {
				return die(2, "%s failed the eval gate after vendoring", cid)
			}
		}
		sha = cand.HeadSha.String
	}
	olds, err := QueryTrials(db, "WHERE candidate_id=? AND vendor_sha=? AND task=? AND condition=?", cid, sha, task, cond)
	if err != nil {
		return err
	}
	now := e.Api.Now()
	var tid int64
	if len(olds) > 0 {
		old := olds[0]
		if !in([]string{"error", "cancelled", "voided"}, old.Status) {
			return die(2, "trial %d for %s@%s %s %s already exists (status %s)", old.ID, cid, sha9(sha), task, cond, old.Status)
		}
		tid = old.ID
		won, err := Transition(db, tid, "starting", []string{"error", "cancelled", "voided"}, Col{"agent", nil}, Col{"sandbox", nil},
			Col{"queued", now}, Col{"started", now}, Col{"ended", nil}, Col{"author", e.Actor}, Col{"void_reason", nil}, Col{"void_why", nil})
		if err != nil {
			return err
		}
		if !won {
			return die(2, "trial %d for %s@%s %s %s was taken by another start meanwhile", tid, cid, sha9(sha), task, cond)
		}
	} else {
		res, err := db.Exec("INSERT INTO skill_trials(candidate_id,vendor_sha,task,condition,status,queued,started,author) "+
			"VALUES(?,?,?,?,'starting',?,?,?)", cid, sha, task, cond, now, now, e.Actor)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed") {
				return die(2, "trial for %s@%s %s %s was created by another start meanwhile", cid, sha9(sha), task, cond)
			}
			return fmt.Errorf("create trial: %w", err)
		}
		if tid, err = res.LastInsertId(); err != nil {
			return fmt.Errorf("read new trial id: %w", err)
		}
	}
	row, _, err := GetTrial(db, tid)
	if err != nil {
		return err
	}
	info, lerr := e.Launch(db, row, root, e.PasswdHome, e.UnshareBin)
	if lerr != nil {
		if errors.Is(lerr, sandbox.ErrSandboxRefused) {
			msg := refusedText(lerr)
			if _, err := db.Exec("UPDATE skill_trials SET status='error', check_output=? WHERE id=? AND status IN ('starting','running')",
				runePrefix(msg, 400), tid); err != nil {
				return fmt.Errorf("record refused try of trial %d: %w", tid, err)
			}
			if err := e.Api.Event(db, "skilleval", cid, "try-refused", fmt.Sprintf("trial %d: %s", tid, runePrefix(msg, 300))); err != nil {
				return fmt.Errorf("record try-refused: %w", err)
			}
			return die(3, "%s", msg)
		}
		return lerr
	}
	e.printf("trial %d: %s (%s, %s, %s) sandbox %s\n", tid, info.Agent, task, cond, cid, info.Dir)
	return nil
}

// Collect is skilleval_collect.
func (e *Env) Collect(db *sql.DB, target, reason string) error {
	id, ok := ParseTrialTarget(target)
	if !ok {
		return die(2, "usage: factory skilleval collect <trial id> [--reason timeout|over-budget|cancelled]")
	}
	root, err := e.root(db)
	if err != nil {
		return err
	}
	t, cerr := e.CollectTrial(db, id, root, e.Api, reason)
	var ce *CollectError
	if errors.As(cerr, &ce) {
		return die(2, "%s", ce.Msg)
	}
	if cerr != nil {
		return cerr
	}
	e.printf("%s\n", TrialLine(t))
	return nil
}

// ParseTasks is `--tasks all|t1,t2`: nil tasks means every known task.
func ParseTasks(arg string, known []string) []string {
	if arg == "" || arg == "all" {
		return known
	}
	var out []string
	for _, t := range strings.Split(arg, ",") {
		if t = pyStrip(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// QueueCandidate is skilleval_queue: pending with/without trials for the tick to start.
func (e *Env) QueueCandidate(db *sql.DB, cid, tasksArg string) error {
	if !candTargetRE.MatchString(cid) {
		return die(2, "usage: factory skilleval queue C-xxxx [--tasks all|task1,task2]")
	}
	known, err := TaskNames(e.TasksDir)
	if err != nil {
		return err
	}
	tasks := ParseTasks(tasksArg, known)
	var bad []string
	for _, t := range tasks {
		if !in(known, t) {
			bad = append(bad, t)
		}
	}
	if len(tasks) == 0 || len(bad) > 0 {
		b := strings.Join(bad, ", ")
		if b == "" {
			b = "(none given)"
		}
		return die(2, "unknown eval task %s; known: %s", b, strings.Join(known, ", "))
	}
	r, err := getCand(db, cid)
	if err != nil {
		return err
	}
	if !scout.IsEvalAllowed(vcand(r), "") {
		return notEligible(cid, r, "queued")
	}
	cand, _, err := e.VendorCandidate(db, cid) // a refusal or a pre-screen hit propagates
	if err != nil {
		return err
	}
	var uniq []string
	seen := map[string]bool{}
	for _, t := range tasks {
		if !seen[t] {
			seen[t] = true
			uniq = append(uniq, t)
		}
	}
	added, rows, err := QueueTrials(db, cid, cand.HeadSha.String, uniq, e.Actor, e.Api.Now())
	if err != nil {
		return err
	}
	if len(added) > 0 {
		set := map[string]bool{}
		for _, r := range rows {
			set[r.Task] = true
		}
		if err := e.Api.Event(db, "skilleval", cid, "queue", fmt.Sprintf("%d trial(s) for %d task(s)", len(added), len(set))); err != nil {
			return fmt.Errorf("record queue: %w", err)
		}
		e.printf("%s: queued %d trial(s); the tick starts them under the daily cap\n", cid, len(added))
	} else {
		e.printf("%s: already queued; nothing added\n", cid)
	}
	for _, r := range rows {
		e.printf("%s\n", TrialLine(r))
	}
	return nil
}

// Control is skilleval_control: register the reserved positive-control candidate and queue its trials.
func (e *Env) Control(db *sql.DB, tasksArg string) error {
	cand, err := RegisterControl(db, e.Api.Now(), e.ControlDir)
	if err != nil {
		return err
	}
	if err := e.Api.Event(db, "skilleval", cand.ID, "control", fmt.Sprintf("registered %s@%s", cand.Key, sha9(cand.HeadSha.String))); err != nil {
		return fmt.Errorf("record control: %w", err)
	}
	if tasksArg == "" {
		tasksArg = "fix-bug"
	}
	return e.QueueCandidate(db, cand.ID, tasksArg)
}

// CancelTarget is skilleval_cancel.
func (e *Env) CancelTarget(db *sql.DB, target string) error {
	root, err := e.root(db)
	if err != nil {
		return err
	}
	lines, errs, cerr := e.Cancel(db, target, root, e.Api)
	var ce *CollectError
	if errors.As(cerr, &ce) {
		return die(2, "%s", ce.Msg)
	}
	if cerr != nil {
		return cerr
	}
	for _, l := range lines {
		e.printf("%s\n", l)
	}
	if len(lines) > 0 {
		if err := e.Api.Event(db, "skilleval", target, "cancel", fmt.Sprintf("%d trial(s)", len(lines))); err != nil {
			return fmt.Errorf("record cancel: %w", err)
		}
	}
	if len(errs) > 0 {
		return die(2, "%s", strings.Join(errs, "; "))
	}
	return nil
}

// VoidTarget is skilleval_void.
func (e *Env) VoidTarget(db *sql.DB, target, reason, why string) error {
	why = pyStrip(why)
	if reason != "infra" || why == "" {
		return die(2, `usage: factory skilleval void <trial id|trNNNN> --reason infra --why "<what failed>"`)
	}
	r, err := Void(db, target, reason, why)
	var ce *CollectError
	if errors.As(err, &ce) {
		return die(2, "%s", ce.Msg)
	}
	if err != nil {
		return err
	}
	cell := func(n sql.NullInt64) string {
		if !n.Valid {
			return "None"
		}
		return fmt.Sprint(n.Int64)
	}
	detail := fmt.Sprintf("trial %d %s %s was %s (passed=%s tokens_in=%s tokens_out=%s wall_s=%s): %s: %s", r.ID, r.Task, r.Condition,
		r.Prev, cell(r.Passed), cell(r.TokensIn), cell(r.TokensOut), cell(r.WallS), reason, why)
	if err := e.Api.Event(db, "skilleval", r.CandidateID, "void", runePrefix(detail, 400)); err != nil {
		return fmt.Errorf("record void: %w", err)
	}
	e.printf("%s\n", TrialLine(r.Trial))
	return nil
}

// ListOrResults is the list / results actions: the trial lines, or "no trials".
func (e *Env) ListOrResults(db *sql.DB, candidate string) error {
	lines, err := ResultLines(db, candidate)
	if err != nil {
		return err
	}
	if len(lines) == 0 {
		e.printf("no trials\n")
		return nil
	}
	e.printf("%s\n", strings.Join(lines, "\n"))
	return nil
}
