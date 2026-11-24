package hm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"factory/go/internal/citools/risktier"
	"factory/go/internal/guard"
)

// Finding is one review finding (docs/schemas/review-findings.schema.json).
type Finding struct {
	Severity   string `json:"severity"`
	File       string `json:"file,omitempty"`
	Line       *int   `json:"line,omitempty"`
	Issue      string `json:"issue"`
	Resolution string `json:"resolution"`
}

// Rerun is the gate's own run of one proof.
type Rerun struct {
	ID   int    `json:"id"`
	Kind string `json:"kind"`
	OK   *bool  `json:"ok"` // nil = not re-run (flow, manual)
	Note string `json:"note"`
	Log  string `json:"log,omitempty"`
}

// GateRecord is what one gate run found (kept on the run for the judge packet and the report).
type GateRecord struct {
	Head      string            `json:"head"`
	PRURL     string            `json:"pr_url"`
	Tier      string            `json:"tier"`
	TierWhy   string            `json:"tier_why"`
	Checks    map[string]string `json:"checks"`
	Findings  []Finding         `json:"findings"`
	Reruns    []Rerun           `json:"reruns"`
	TestsBase int               `json:"tests_base"`
	TestsHead int               `json:"tests_head"`
	Problems  []string          `json:"problems"`
}

var (
	resolutionRe = regexp.MustCompile(`(?i)^(fixed|not-a-bug|deferred|duplicate)\b`)
	ranRe        = regexp.MustCompile(`(?m)^Ran (\d+) tests? in |(\d+) passed`)
	testDefRe    = `^[[:space:]]+(async[[:space:]]+)?def[[:space:]]+test_`
	severities   = map[string]bool{"high": true, "medium": true, "low": true, "info": true}
)

// CheckFindings validates .hivemill/findings.json.
func CheckFindings(path string) ([]string, []Finding) {
	probs, f, _ := checkFindings(path)
	return probs, f
}

func checkFindings(path string) ([]string, []Finding, string) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []string{"no .hivemill/findings.json: run /code-review, fix the findings, then write the file (see your role steps)"}, nil, ""
	}
	var doc struct {
		Head     string    `json:"head"`
		Findings []Finding `json:"findings"`
	}
	if err != nil || json.Unmarshal(b, &doc) != nil || doc.Findings == nil && !bytes.Contains(b, []byte(`"findings"`)) {
		return []string{`.hivemill/findings.json must be valid JSON: {"head": "<sha>", "findings": [...]}`}, nil, ""
	}
	var probs []string
	for i, f := range doc.Findings {
		if !severities[f.Severity] {
			probs = append(probs, fmt.Sprintf("finding %d: severity must be high, medium, low or info", i+1))
		}
		if strings.TrimSpace(f.Issue) == "" {
			probs = append(probs, fmt.Sprintf("finding %d: issue is empty", i+1))
		}
		if !resolutionRe.MatchString(f.Resolution) {
			probs = append(probs, fmt.Sprintf("finding %d: resolution must start with fixed, not-a-bug, deferred or duplicate", i+1))
		}
	}
	return probs, doc.Findings, doc.Head
}

// ReviewRan reports whether a /code-review ran in this round: Claude Code records a forked skill run as
// <projects>/<dir>/<session>/subagents/agent-*.forked-skill.json with "skillName": "code-review". The file must belong
// to one of the task's sessions and be newer than the round start.
func (r *Runner) ReviewRan(run *Run) bool {
	since, err := time.Parse(time.RFC3339, run.RoundStart)
	if err != nil {
		since = time.Time{}
	}
	sess, _ := r.Store.Sessions(run.Task)
	for _, s := range sess {
		if s.Role != "task" {
			continue
		}
		m, _ := filepath.Glob(filepath.Join(r.Env.UserHome, ".claude", "projects", "*", s.SID, "subagents", "*.forked-skill.json"))
		for _, f := range m {
			fi, err := os.Stat(f)
			if err != nil || fi.ModTime().Before(since) {
				continue
			}
			b, _ := os.ReadFile(f)
			var d struct {
				SkillName string `json:"skillName"`
			}
			if json.Unmarshal(b, &d) == nil && d.SkillName == "code-review" {
				return true
			}
		}
	}
	return false
}

// CheckEvidence requires an evidence entry for every criterion id.
func CheckEvidence(path string, crit []Criterion) []string {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []string{"no .hivemill/evidence.json: map each criterion to its proof, command and exit code"}
	}
	var doc struct {
		Criteria []struct {
			ID json.Number `json:"id"`
		} `json:"criteria"`
	}
	if err != nil || json.Unmarshal(b, &doc) != nil {
		return []string{`.hivemill/evidence.json must be valid JSON: {"criteria": [...]}`}
	}
	have := map[int]bool{}
	for _, c := range doc.Criteria {
		if n, err := strconv.Atoi(string(c.ID)); err == nil {
			have[n] = true
		}
	}
	var missing []string
	for _, c := range crit {
		if !have[c.ID] {
			missing = append(missing, strconv.Itoa(c.ID))
		}
	}
	if len(missing) > 0 {
		return []string{".hivemill/evidence.json has no entry for criteria " + strings.Join(missing, ", ")}
	}
	return nil
}

// TestArgv is how a test: proof runs: Python unittest, a dotted id under tests/ or a tests/ file.
func TestArgv(wt, name string) ([]string, string) {
	if strings.HasSuffix(name, ".py") {
		dir := filepath.Dir(name)
		if dir == "." {
			dir = "tests"
		}
		return []string{"python3", "-m", "unittest", "discover", "-s", dir, "-p", filepath.Base(name)}, wt
	}
	if fi, err := os.Stat(filepath.Join(wt, "tests")); err != nil || !fi.IsDir() {
		return nil, ""
	}
	return []string{"python3", "-m", "unittest", strings.TrimPrefix(name, "tests.")}, filepath.Join(wt, "tests")
}

func cleanEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "CLAUDE") || strings.HasPrefix(kv, "GH_TOKEN=") || strings.HasPrefix(kv, "GITHUB_TOKEN=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// RerunProofs runs every test: and cmd: proof of the DB criteria (never the session's evidence file).
func (r *Runner) RerunProofs(wt string, crit []Criterion, logdir string) []Rerun {
	var out []Rerun
	for _, c := range crit {
		x := Rerun{ID: c.ID, Kind: c.Kind}
		var argv []string
		dir := wt
		switch c.Kind {
		case "manual", "flow":
			x.Note = "not re-run (" + c.Kind + ")"
			out = append(out, x)
			continue
		case "test":
			argv, dir = TestArgv(wt, c.Proof)
			if argv == nil {
				f := false
				x.OK, x.Note = &f, "no Python tests/ dir: write this proof as cmd:"
				out = append(out, x)
				continue
			}
		default:
			argv = []string{"bash", "-c", c.Proof}
		}
		rc, text := r.Tools.Sh(dir, 15*time.Minute, cleanEnv(), argv...)
		x.Log = filepath.Join(logdir, fmt.Sprintf("proof-%d.log", c.ID))
		_ = os.WriteFile(x.Log, []byte(text), 0o600)
		ok := rc == 0
		x.Note = fmt.Sprintf("exit %d", rc)
		if c.Kind == "cmd" {
			ok, x.Note = ExpectOK(c.Expect, rc, text)
		}
		if rc == 124 {
			ok, x.Note = false, "timed out after 15m"
		}
		x.OK = &ok
		out = append(out, x)
	}
	return out
}

func (r *Runner) testCount(wt, ref string) int {
	rc, out, _ := r.Tools.Git(wt, "grep", "-h", "-E", testDefRe, ref, "--", "tests")
	if rc != 0 && rc != 1 {
		return -1
	}
	if out == "" {
		return 0
	}
	return len(strings.Split(out, "\n"))
}

func (r *Runner) findPR(run *Run, t *Task) string {
	if t.PRURL != "" {
		return t.PRURL
	}
	rc, out, _ := r.Tools.GH("", "pr", "list", "-R", run.Repo, "--head", run.Branch, "--state", "open", "--json", "url", "-q", ".[0].url")
	if rc != 0 {
		return ""
	}
	return strings.TrimSpace(out)
}

// affected runs the project's affected tests (ci/affected.py of the factory repo) and waits, not fails, while the box
// has no test slot or is over its load cap.
func (r *Runner) affected(wt, base string) (bool, string) {
	if rc, _, _ := r.Tools.Git(wt, "cat-file", "-e", "origin/"+base+":ci/affected.py"); rc != 0 {
		return true, "n/a: no ci/affected.py in this project"
	}
	deadline := time.Now().Add(time.Duration(cfgInt(r.Store, "gate_wait_s")) * time.Second)
	for {
		rc, text := r.Tools.Sh(wt, 30*time.Minute, cleanEnv(), "python3", "ci/affected.py", "--base", "origin/"+base, "--quick", "--run")
		tail := lastLines(text, 8)
		switch {
		case rc == 4: // EXIT_BUDGET: what ran passed; slow or over-budget modules are left to CI
			return true, "ran the fast part; slow modules left to CI (" + firstLine(lastLines(text, 1)) + ")"
		case strings.Contains(text, "FULL needed"):
			return true, "not run locally (" + firstLine(tail) + "); CI runs the full suite"
		case rc == 0:
			n := 0
			for _, m := range ranRe.FindAllStringSubmatch(text, -1) {
				v, _ := strconv.Atoi(m[1] + m[2])
				n += v
			}
			if n == 0 && !strings.Contains(text, "no tests") {
				return false, "ci/affected.py exited 0 but reported no tests run:\n" + tail
			}
			return true, fmt.Sprintf("ci/affected.py ok, ran %d tests", n)
		case rc == 5 || strings.Contains(text, "test slot"):
			if time.Now().After(deadline) {
				return true, "undecided (" + firstLine(tail) + "); CI decides"
			}
			time.Sleep(time.Minute)
		default:
			return false, fmt.Sprintf("ci/affected.py exited %d:\n%s", rc, tail)
		}
	}
}

func lastLines(s string, n int) string {
	ls := strings.Split(strings.TrimSpace(s), "\n")
	if len(ls) > n {
		ls = ls[len(ls)-n:]
	}
	return strings.Join(ls, "\n")
}

// publicText runs the public-text guard (owner terms, secrets) over what the branch publishes and over the PR text.
func (r *Runner) publicText(wt, base string, pr *PR) (bool, string) {
	terms := r.Store.FactoryCfg("scrub_terms_file")
	args := []string{}
	if terms != "" {
		args = append(args, "--terms-file", terms)
	}
	allow := filepath.Join(r.Env.Home, "ci", "scrub_allowlist.txt")
	var out, errb bytes.Buffer
	rc := guard.CIPublicTextGuardMain(append(append([]string{}, args...), "push", "origin/"+base, "HEAD", "--repo", wt, "--allowlist", allow),
		strings.NewReader(""), &out, &errb)
	if rc != 0 {
		return false, fmt.Sprintf("public-text guard on the commits exited %d: %s", rc, lastLines(out.String()+errb.String(), 6))
	}
	if pr != nil {
		out.Reset()
		errb.Reset()
		rc = guard.CIPublicTextGuardMain(append(append([]string{}, args...), "text", "-"), strings.NewReader(pr.Title+"\n"+pr.Body), &out, &errb)
		if rc != 0 {
			return false, fmt.Sprintf("public-text guard on the PR title/body exited %d: %s", rc, lastLines(out.String(), 6))
		}
	}
	return true, "no hits"
}

// Gate runs every check in the task clone and returns the record (Problems empty = pass).
func (r *Runner) Gate(run *Run) *GateRecord {
	g := r.Tools.Git
	wt, base := run.Worktree, run.Base
	rec := &GateRecord{Checks: map[string]string{}}
	t, err := r.Store.Task(run.Task)
	if err != nil {
		rec.Problems = []string{err.Error()}
		return rec
	}
	crit := Criteria(t.Acceptance)
	logdir := filepath.Join(r.Env.StateDir(), "runs", run.Task, fmt.Sprintf("round-%d", run.Round))
	_ = os.MkdirAll(logdir, 0o700)
	add := func(name string, ok bool, note string) {
		rec.Checks[name] = note
		if !ok {
			rec.Problems = append(rec.Problems, name+": "+note)
		}
	}
	if _, out, _ := g(wt, "status", "--porcelain", "--untracked-files=no"); out != "" {
		add("committed", false, "uncommitted tracked changes: commit them")
	}
	_, rec.Head, _ = g(wt, "rev-parse", "HEAD")
	g(wt, "fetch", "-q", "origin", base)
	rec.PRURL = r.findPR(run, t)
	var pr *PR
	if rec.PRURL == "" {
		add("pr", false, fmt.Sprintf("no open PR for %s: push, then `factory pr create --title '%s: ...' --body -`", run.Branch, run.Task))
	} else if pr, err = PRView(r.Tools, rec.PRURL); err != nil {
		add("pr", false, "cannot read the PR: "+err.Error())
	} else if pr.State != "OPEN" {
		add("pr", false, "the PR is "+pr.State)
	} else if pr.HeadRefOid != rec.Head {
		add("pushed", false, fmt.Sprintf("PR head %.9s != local HEAD %.9s: push first", pr.HeadRefOid, rec.Head))
	}
	// fresh: HEAD holds origin/base, or a merge of it is clean
	if rc, _, _ := g(wt, "merge-base", "--is-ancestor", "origin/"+base, "HEAD"); rc == 0 {
		rec.Checks["fresh"] = "HEAD contains origin/" + base
	} else if rc, out, _ := g(wt, "merge-tree", "--write-tree", "--name-only", "origin/"+base, "HEAD"); rc == 1 {
		add("fresh", false, "merging origin/"+base+" conflicts ("+strings.Join(strings.Split(out, "\n")[1:], ", ")+"): merge it, resolve, push")
	} else {
		rec.Checks["fresh"] = "behind origin/" + base + "; a merge is clean"
	}
	// identity: every commit noreply
	_, logs, _ := g(wt, "log", "--format=%H%x09%ae%x09%ce", "origin/"+base+"..HEAD")
	if logs == "" {
		add("identity", false, "no commits beyond origin/"+base)
	}
	for _, ln := range strings.Split(logs, "\n") {
		f := strings.Split(ln, "\t")
		if len(f) == 3 && (!r.isNoreply(f[1]) || !r.isNoreply(f[2])) {
			add("identity", false, fmt.Sprintf("commit %.9s is not authored and committed as %s", f[0], or(r.Store.FactoryCfg("noreply_email"), "a noreply address")))
			break
		}
	}
	// tests and tier
	_, mb, _ := g(wt, "merge-base", "HEAD", "origin/"+base)
	rec.TestsBase, rec.TestsHead = r.testCount(wt, mb), r.testCount(wt, "HEAD")
	if pr != nil {
		dropped := rec.TestsBase >= 0 && rec.TestsHead >= 0 && rec.TestsHead < rec.TestsBase
		if probs := BodyProblems(pr.Body, dropped); len(probs) > 0 {
			add("body", false, strings.Join(probs, "; "))
		} else {
			rec.Checks["body"] = "follows docs/pr-descriptions.md"
		}
	}
	if rules, err := risktier.LoadRules(filepath.Join(r.Env.Home, "ci", "tier_rules.json")); err == nil {
		tier, why, err := risktier.ClassifyRange(wt, "origin/"+base, "HEAD", rules)
		if err == nil {
			rec.Tier, rec.TierWhy = tier, strings.Join(why, "; ")
		}
	}
	ok, note := r.publicText(wt, base, pr)
	add("public-text", ok, note)
	if rec.Tier == "T0" {
		rec.Checks["affected"] = "n/a: docs only (T0)"
	} else {
		ok, note = r.affected(wt, base)
		add("affected", ok, note)
	}
	probs, findings, fhead := checkFindings(filepath.Join(wt, ".hivemill", "findings.json"))
	rec.Findings = findings
	for _, p := range probs {
		add("findings", false, p)
	}
	if len(probs) == 0 && fhead != rec.Head {
		add("review", false, fmt.Sprintf("findings.json is for head %.9s, not HEAD %.9s: run /code-review on the current head and rewrite "+
			"the file with \"head\": the output of `git rev-parse HEAD`", fhead, rec.Head))
	}
	if !r.ReviewRan(run) {
		add("review", false, "no /code-review ran in this round: run /code-review, fix the findings, commit, then write .hivemill/findings.json")
	} else {
		rec.Checks["review"] = "/code-review ran in this round"
	}
	ev := CheckEvidence(filepath.Join(wt, ".hivemill", "evidence.json"), crit)
	for _, p := range ev {
		add("evidence", false, p)
	}
	if len(ev) == 0 {
		rec.Reruns = r.RerunProofs(wt, crit, logdir)
		for _, x := range rec.Reruns {
			if x.OK != nil && !*x.OK {
				rec.Problems = append(rec.Problems, fmt.Sprintf("criterion %d (%s): the factory re-ran its proof and it failed: %s", x.ID, x.Kind, x.Note))
			}
		}
	}
	b, _ := json.MarshalIndent(rec, "", " ")
	_ = os.WriteFile(filepath.Join(logdir, "gate.json"), b, 0o600)
	return rec
}

// isNoreply: the email is the configured noreply_email (any GitHub noreply address when none is configured).
func (r *Runner) isNoreply(email string) bool {
	want := strings.ToLower(strings.TrimSpace(r.Store.FactoryCfg("noreply_email")))
	if want == "" {
		return noreply(email)
	}
	return strings.ToLower(strings.TrimSpace(email)) == want
}

func noreply(email string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(email)), "@users.noreply.github.com")
}

// HookInput is what Claude Code sends a Stop or SessionStart hook on stdin.
type HookInput struct {
	SessionID            string `json:"session_id"`
	TranscriptPath       string `json:"transcript_path"`
	LastAssistantMessage string `json:"last_assistant_message"`
	StopHookActive       bool   `json:"stop_hook_active"`
}

func readHook(in io.Reader) HookInput {
	var h HookInput
	b, _ := io.ReadAll(in)
	_ = json.Unmarshal(b, &h)
	return h
}

func (r *Runner) runFor(agent string) *Run {
	runs, _ := r.Store.Runs()
	for i := range runs {
		if runs[i].Agent == agent {
			return &runs[i]
		}
		for _, j := range runs[i].Judges {
			if j == agent {
				return &runs[i]
			}
		}
	}
	return nil
}

func (r *Runner) noteSession(run *Run, sid, agent string) {
	if sid == "" {
		return
	}
	role := "task"
	if strings.Contains(agent, "judge") {
		role = "judge"
	}
	_ = r.Store.AddSession(Session{SID: sid, Task: run.Task, Role: role, Agent: agent, Started: nowISO()})
}

// GateHook is the Stop hook of a task session. It allows the stop when the gate passes and blocks with the problems
// otherwise, at most gate_max_blocks times per round; then it allows the stop and marks the task FAIL for the owner.
func (r *Runner) GateHook(in io.Reader, out io.Writer) error {
	h := readHook(in)
	found := r.runFor(r.Env.Actor)
	if found == nil || found.Agent != r.Env.Actor {
		return nil
	}
	l := r.lock(found.Task, true)
	if l != nil {
		defer l.Close()
	}
	run, err := r.Store.Run(found.Task)
	if err != nil || run == nil || run.State != Working {
		return err
	}
	r.noteSession(run, h.SessionID, r.Env.Actor)
	defer func() { _ = r.Store.PutRun(run) }()
	// No PR yet: the session is mid-work (often waiting for a background /code-review that wakes it again). Let it
	// stop without a block; the runner's idle watch nudges a session that stays idle with no PR.
	if t, err := r.Store.Task(run.Task); err == nil && t.PRURL == "" && r.findPR(run, t) == "" {
		_ = r.Log.Emit("gate.deferred", run, Event{Session: h.SessionID, Role: "task", Data: map[string]any{"why": "no PR yet"}})
		return nil
	}
	// A PR exists (a takeover of an older branch, or a later round) but no findings are written yet: the session is
	// still mid-work (a background /code-review writes its run record when it starts, so that record alone does not
	// mean the review is done). The stop is not a gate attempt; the idle watch nudges and then fails a session that
	// stays idle like this.
	if _, err := os.Stat(filepath.Join(run.Worktree, ".hivemill", "findings.json")); err != nil {
		_ = r.Log.Emit("gate.deferred", run, Event{Session: h.SessionID, Role: "task", Data: map[string]any{"why": "no findings.json yet"}})
		return nil
	}
	rec := r.Gate(run)
	if len(rec.Problems) == 0 {
		run.State, run.Head, run.PRURL, run.Tier, run.Gated, run.LastGate = Gated, rec.Head, rec.PRURL, rec.Tier, nowISO(), rec
		fixed := 0
		for _, f := range rec.Findings {
			if strings.HasPrefix(strings.ToLower(f.Resolution), "fixed") {
				fixed++
			}
		}
		run.FindingsTotal += len(rec.Findings)
		run.FindingsFixed += fixed
		run.Proven, run.Manual = 0, 0
		for _, x := range rec.Reruns {
			if x.OK != nil && *x.OK {
				run.Proven++
			}
			if x.Kind == "manual" {
				run.Manual++
			}
		}
		_ = r.Store.TaskPR(run.Task, rec.PRURL, rec.Tier)
		_ = r.Store.TaskStatus(run.Task, "in_review", "")
		_ = r.Store.Event(r.actor(), "task", run.Task, "run-gate-pass", map[string]any{"head": rec.Head, "round": run.Round})
		_ = r.Log.Emit("review.findings", run, Event{Session: h.SessionID, Role: "review", Data: map[string]any{"count": len(rec.Findings),
			"fixed": fixed, "findings": rec.Findings}})
		_ = r.Log.Emit("gate.passed", run, Event{Session: h.SessionID, Role: "task", Data: map[string]any{"head": rec.Head,
			"round": run.Round, "reruns": rec.Reruns, "tier": rec.Tier}})
		return nil
	}
	// A block counts toward the cap only when it repeats the last block's problems: a session that fixes one problem and
	// stops again (T-1858 fixed three PR-body problems in 35 s) is making progress. BlockStops bounds a loop anyway.
	probs := strings.Join(rec.Problems, "\n")
	run.BlockStops++
	if probs == run.LastBlock || run.BlockStops > 3*cfgInt(r.Store, "gate_max_blocks") || run.GateBlocks == 0 {
		run.GateBlocks++
	}
	run.LastBlock = probs
	run.GateBlocksTotal++
	for _, x := range rec.Reruns {
		if x.OK != nil && !*x.OK {
			run.RerunFailures++
		}
	}
	limit := cfgInt(r.Store, "gate_max_blocks")
	_ = r.Log.Emit("gate.blocked", run, Event{Session: h.SessionID, Role: "task", Data: map[string]any{"blocks": run.GateBlocks,
		"problems": rec.Problems}})
	if run.GateBlocks >= limit {
		r.fail(run, fmt.Sprintf("gate blocked %d times in round %d: %s", run.GateBlocks, run.Round, truncate(strings.Join(rec.Problems, "; "), 600)))
		return nil
	}
	return json.NewEncoder(out).Encode(map[string]string{"decision": "block",
		"reason": fmt.Sprintf("Factory gate (%d/%d): fix these, then stop again:\n- %s", run.GateBlocks, limit, strings.Join(rec.Problems, "\n- "))})
}

// SessionHook records a new session id (/clear, resume) so every session's spend maps to its task.
func (r *Runner) SessionHook(in io.Reader) error {
	h := readHook(in)
	run := r.runFor(r.Env.Actor)
	if run == nil || h.SessionID == "" {
		return nil
	}
	r.noteSession(run, h.SessionID, r.Env.Actor)
	return r.Store.AgentSession(r.Env.Actor, h.SessionID)
}
