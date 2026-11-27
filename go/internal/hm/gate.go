package hm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"factory/go/internal/citools/risktier"
	"factory/go/internal/guard"
	"factory/go/internal/shlex"
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
	rc   int    // of the proof's command; with out, what a run on the base commit is compared with
	out  string
	took time.Duration
}

// baseRunBudget caps the head run time of the proofs run again on the base commit, so a gate with slow proofs stays
// inside the Stop hook's timeout.
var baseRunBudget = 5 * time.Minute

// specDefect starts the problem of a proof that cannot pass as written; reasonSpecDefect is the reason_kind of a task
// that fails with one.
const (
	specDefect       = "spec defect:"
	reasonSpecDefect = "spec_defect"
)

var durationRe = regexp.MustCompile(`\b\d+(\.\d+)?(ns|µs|ms|s)\b`)

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
	// Undecided: ssh to the run's clone-mode machine failed during the checks, so they say nothing about the work
	Undecided bool `json:"undecided,omitempty"`
}

var (
	resolutionRe = regexp.MustCompile(`(?i)^(fixed|not-a-bug|deferred|duplicate)\b`)
	ranRe        = regexp.MustCompile(`(?m)^Ran (\d+) tests? in |(\d+) passed`)
	commitRe     = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
	testDefRe    = `^[[:space:]]+(async[[:space:]]+)?def[[:space:]]+test_`
	severities   = map[string]bool{"high": true, "medium": true, "low": true, "info": true}
)

// CheckFindings validates .hivemill/findings.json.
func CheckFindings(path string) ([]string, []Finding) {
	probs, f, _ := checkFindings(path)
	return probs, f
}

func checkFindings(path string) ([]string, []Finding, string) {
	return parseFindings(os.ReadFile(path))
}

// parseFindings is checkFindings over the result of a read, here or on a clone-mode machine.
func parseFindings(b []byte, err error) ([]string, []Finding, string) {
	if errors.Is(err, fs.ErrNotExist) {
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

// onlyBaseMerges reports whether head is the reviewed commit plus nothing but clean merges of origin/base, so the review
// still holds. Each commit on head's first-parent path down to reviewed must merge a commit of origin/base, and its tree
// must be exactly git's own merge of its two parents: a conflict resolution or any edit in the merge needs a new review.
// A local branch named like the base, a replace ref or a local merge rule must not widen this by accident, so the base is
// named by its full ref, replace objects are off and such rules refuse. This is no defence against a forged clone: the
// head in findings.json is the session's own statement. at is where the worktree is.
func onlyBaseMerges(at place, wt, base, reviewed, head string) bool {
	g := func(args ...string) (int, string, string) {
		return at.git(wt, append([]string{"--no-replace-objects", "-c", "core.attributesFile=/dev/null"}, args...)...)
	}
	if !commitRe.MatchString(reviewed) || localMergeRules(at, wt) { // session text: only a full commit id reaches git
		return false
	}
	rc, log, _ := g("log", "--first-parent", "--no-show-signature", "--format=%H %P %T", reviewed+".."+head)
	if rc != 0 || log == "" {
		return false
	}
	next := head
	for _, ln := range strings.Split(strings.TrimSpace(log), "\n") {
		f := strings.Fields(ln) // commit, first parent, second parent, tree
		if len(f) != 4 || f[0] != next {
			return false
		}
		if rc, _, _ := g("merge-base", "--is-ancestor", f[2], "refs/remotes/origin/"+base); rc != 0 {
			return false
		}
		// merge-tree reads .gitattributes from the checkout: they must be the ones this merge was made with.
		if rc, _, _ := g("diff", "--quiet", f[1], "--", ":(glob)**/.gitattributes"); rc != 0 {
			return false
		}
		if rc, out, _ := g("merge-tree", "--write-tree", f[1], f[2]); rc != 0 || strings.TrimSpace(out) != f[3] {
			return false
		}
		next = f[1]
	}
	return next == reviewed
}

// localMergeRules reports whether the clone has uncommitted rules that change how git merges (a merge driver, a default
// driver, info/attributes, an untracked .gitattributes over tracked files), or cannot be asked: git would then repeat a merge the review
// never saw, such as a union of two conflicting sides.
func localMergeRules(at place, wt string) bool {
	g := at.git
	if rc, _, _ := g(wt, "config", "--get-regexp", `^merge\.(default$|.*\.driver$)`); rc != 1 { // 1 = no such key
		return true
	}
	rc, attrs, _ := g(wt, "rev-parse", "--git-path", "info/attributes")
	if attrs = strings.TrimSpace(attrs); rc != 0 || attrs == "" {
		return true
	}
	if !filepath.IsAbs(attrs) {
		attrs = filepath.Join(wt, attrs)
	}
	if _, err := at.read(attrs); !errors.Is(err, fs.ErrNotExist) {
		return true
	}
	// Ignored files count too. One whose directory holds no tracked file (an installed dependency's own) shapes no merge.
	rc, others, _ := g(wt, "ls-files", "-z", "--others", "--", ":(glob)**/.gitattributes")
	if rc != 0 {
		return true
	}
	var dirs []string
	for _, f := range strings.Split(others, "\x00") {
		if f != "" {
			dirs = append(dirs, ":(literal)"+path.Dir(f))
		}
	}
	if len(dirs) == 0 {
		return false
	}
	rc, tracked, _ := g(wt, append([]string{"ls-files", "--"}, dirs...)...)
	return rc != 0 || strings.TrimSpace(tracked) != ""
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
		if run.cloned() {
			if r.remoteReviewRan(run.Machine, s.SID, since) {
				return true
			}
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

// remoteReviewRan is ReviewRan's look at the Claude Code records of one session on a clone-mode machine.
func (r *Runner) remoteReviewRan(machine, sid string, since time.Time) bool {
	rc, _, _ := r.rsh(machine, "", 60*time.Second, fmt.Sprintf(`sid=%s; since=%d
for f in "$HOME"/.claude/projects/*/"$sid"/subagents/*.forked-skill.json; do
  [ -f "$f" ] || continue
  m=$(stat -c %%Y "$f" 2>/dev/null || stat -f %%m "$f") || continue
  if [ "$m" -ge "$since" ] && grep -qE '"skillName"[[:space:]]*:[[:space:]]*"code-review"' "$f"; then exit 0; fi
done
exit 1`, shlex.Quote(sid), max(since.Unix(), 0)))
	return rc == 0
}

// CheckEvidence requires an evidence entry for every criterion id.
func CheckEvidence(path string, crit []Criterion) []string {
	b, err := os.ReadFile(path)
	return parseEvidence(b, err, crit)
}

func parseEvidence(b []byte, err error, crit []Criterion) []string {
	if errors.Is(err, fs.ErrNotExist) {
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

// TestArgv is how a test: proof runs. A proof with a space in it is a shell command run in the clone, passing on
// exit 0 (`test:pnpm --filter web test`, `test:go test ./internal/x`: any project's own test or gate command); else
// it is Python unittest: a tests/ file (`test:tests/test_x.py`) or a dotted id under tests/ (`test:tests.test_x.T.t`).
func TestArgv(wt, name string) ([]string, string) {
	return testArgv(place{}, wt, name)
}

func testArgv(p place, wt, name string) ([]string, string) {
	if strings.ContainsAny(strings.TrimSpace(name), " \t") {
		return []string{"bash", "-c", name}, wt
	}
	if strings.HasSuffix(name, ".py") {
		dir := filepath.Dir(name)
		if dir == "." {
			dir = "tests"
		}
		return []string{"python3", "-m", "unittest", "discover", "-s", dir, "-p", filepath.Base(name)}, wt
	}
	if !p.isDir(filepath.Join(wt, "tests")) {
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

// RerunProofs runs every test: and cmd: proof of the DB criteria (never the session's evidence file) in the run's
// clone, on the run's machine (proofrun.go).
func (r *Runner) RerunProofs(run *Run, crit []Criterion, logdir string) []Rerun {
	return r.rerunProofs(r.at(run), run, crit, logdir)
}

// rerunProofs is RerunProofs with the place of the worktree: a clone-mode run's proofs run there over ssh, and the logs
// are written here.
func (r *Runner) rerunProofs(at place, run *Run, crit []Criterion, logdir string) []Rerun {
	var out []Rerun
	wt := run.Worktree
	proofs := &proofRunner{r: r, run: run}
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
			argv, dir = testArgv(at, wt, c.Proof)
			if argv == nil {
				f := false
				x.OK, x.Note = &f, "no Python tests/ dir: write the proof as the project's test command (test:pnpm test) or as cmd:"
				out = append(out, x)
				continue
			}
		default:
			argv = []string{"bash", "-c", c.Proof}
		}
		var rc int
		var text string
		limit := proofTimeout
		began := time.Now()
		if run.cloned() {
			rc, text = at.sh(dir, proofTimeout, cleanEnv(), argv...)
		} else {
			rc, text, limit = proofs.sh(c.ID, dir, argv)
		}
		x.rc, x.out, x.took = rc, text, time.Since(began)
		x.Log = filepath.Join(logdir, fmt.Sprintf("proof-%d.log", c.ID))
		_ = os.WriteFile(x.Log, []byte(text), 0o600)
		ok := rc == 0
		x.Note = fmt.Sprintf("exit %d", rc)
		if c.Kind == "cmd" {
			ok, x.Note = ExpectOK(c.Expect, rc, text)
		}
		if rc == 124 {
			ok, x.Note = false, timeoutNote(limit)
		}
		x.OK = &ok
		out = append(out, x)
	}
	return out
}

func baseCheckout(wt string) string { return filepath.Join(wt, ".hivemill", "base") }

// dropBaseCheckout removes the base checkout, also one an interrupted gate left half made: a proof that searches the
// clone must not see the old code.
func dropBaseCheckout(at place, wt string) {
	if !at.isDir(baseCheckout(wt)) {
		return
	}
	at.sh(wt, time.Minute, cleanEnv(), "rm", "-rf", baseCheckout(wt))
	at.git(wt, "worktree", "prune")
}

// specDefects says which failed proofs are the spec's fault, by criterion id, and why: the command exits 2 or 127 (usage
// error, command not found), or it fails with the same exit code and output on the base commit mb, checked out under
// .hivemill for this. A proof that timed out, never ran or is over baseRunBudget is not run on the base.
func (r *Runner) specDefects(at place, run *Run, crit []Criterion, reruns []Rerun, mb, logdir string) map[int]string {
	out := map[int]string{}
	failed := map[int]Rerun{}
	var onBase []Criterion
	var spent time.Duration
	for i, x := range reruns { // reruns[i] is the run of crit[i]
		switch {
		case x.OK == nil || *x.OK || x.Log == "" || x.rc == 124:
		case x.rc == 2 || x.rc == 127:
			out[x.ID] = fmt.Sprintf("exit %d: a usage error or a command that does not exist", x.rc)
		case spent+x.took <= baseRunBudget:
			spent += x.took
			failed[x.ID] = x
			onBase = append(onBase, crit[i])
		}
	}
	if len(onBase) == 0 || !commitRe.MatchString(mb) {
		return out
	}
	wt := run.Worktree
	dir := baseCheckout(wt)
	if rc, _, errs := at.git(wt, "worktree", "add", "-q", "--detach", dir, mb); rc != 0 {
		r.printf("%s: no checkout of the base commit: %s\n", run.Task, errs)
		return out
	}
	defer dropBaseCheckout(at, wt)
	base := *run
	base.Worktree = dir
	logs := filepath.Join(logdir, "base")
	_ = os.MkdirAll(logs, 0o700)
	same := func(s string) string { return durationRe.ReplaceAllString(strings.ReplaceAll(s, dir, wt), "T") }
	for _, x := range r.rerunProofs(at, &base, onBase, logs) {
		head := failed[x.ID]
		if x.OK != nil && !*x.OK && x.Log != "" && x.rc == head.rc && same(x.out) == same(head.out) {
			out[x.ID] = fmt.Sprintf("it fails the same way on the base commit %.9s", mb)
		}
	}
	return out
}

func (r *Runner) testCount(p place, wt, ref string) int {
	rc, out, _ := p.git(wt, "grep", "-h", "-E", testDefRe, ref, "--", "tests")
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

// affected runs the project's affected tests (the selection of ci/affected.py of the factory repo, through its Go port
// `hivemill affected`) and waits, not fails, while the box has no test slot or is over its load cap.
func (r *Runner) affected(wt, base string) (bool, string) {
	return r.affectedAt(place{r: r}, "", wt, base)
}

// affectedAt is affected where the worktree is; bin is the hivemill of a clone-mode machine.
func (r *Runner) affectedAt(p place, bin, wt, base string) (bool, string) {
	if rc, _, _ := p.git(wt, "cat-file", "-e", "origin/"+base+":ci/affected.py"); rc != 0 {
		return true, "n/a: no ci/affected.py in this project"
	}
	deadline := time.Now().Add(time.Duration(cfgInt(r.Store, "gate_wait_s")) * time.Second)
	for {
		argv := []string{"python3", "ci/affected.py", "--base", "origin/" + base, "--quick", "--run"}
		self, err := SelfBinary()
		if bin != "" {
			self, err = bin, nil
		}
		if err == nil && filepath.IsAbs(self) {
			argv = []string{self, "affected", "--root", wt, "--base", "origin/" + base, "--quick", "--run"} // the Go port
		}
		rc, text := p.sh(wt, 30*time.Minute, cleanEnv(), argv...)
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

// publicText runs the public-text guard over what the branch publishes and over the PR text: owner terms and secrets
// for a public_text_repos repo, the secret checks alone for any other. A failure names the part that failed.
func (r *Runner) publicText(repo, wt, base string, pr *PR) (bool, string) {
	public, err := r.publicTextRepo(repo)
	if err != nil {
		return false, err.Error()
	}
	scan, args := guard.CISecretsGuardMain, []string(nil)
	terms, why := r.guardArgs()
	switch {
	case public && why != "":
		return false, why
	case public:
		scan, args = guard.CIPublicTextGuardMain, terms
	case why == "": // private repo: the terms only mask what the note shows
		args = terms
	}
	check := func(what, stdin string, mode ...string) (bool, string) {
		var out, errb bytes.Buffer
		rc := scan(append(append([]string{}, args...), mode...), strings.NewReader(stdin), &out, &errb)
		if rc == 0 {
			return true, ""
		}
		// no hit line (the guard could not decide): name every check that was running
		part := "secrets"
		switch owner, secrets := guard.CIHitParts(out.String()); {
		case owner && secrets, public && !owner && !secrets:
			part = "owner terms and secrets"
		case owner:
			part = "owner terms"
		}
		return false, fmt.Sprintf("public-text guard (%s) on %s exited %d: %s", part, what, rc, lastLines(out.String()+errb.String(), 6))
	}
	allow := filepath.Join(r.Env.Home, "ci", "scrub_allowlist.txt")
	if ok, note := check("the commits", "", "push", "origin/"+base, "HEAD", "--repo", wt, "--allowlist", allow); !ok {
		return false, note
	}
	if pr != nil {
		if ok, note := check("the PR title/body", pr.Title+"\n"+pr.Body, "text", "-"); !ok {
			return false, note
		}
	}
	if !public {
		return true, "no secrets (owner terms not checked: the repo is not in public_text_repos)"
	}
	return true, "no hits"
}

// guardArgs are the public-text guard's owner-terms arguments; why says that there are no terms, so nothing may pass.
func (r *Runner) guardArgs() (args []string, why string) {
	terms := r.Store.FactoryCfg("scrub_terms_file")
	if terms == "" && os.Getenv("SCRUB_TERMS_FILE") == "" {
		return nil, "no owner terms: set them with `hivemill config scrub_terms_file /abs/path` (the check never passes without them)"
	}
	if terms != "" {
		args = []string{"--terms-file", terms}
	}
	return args, ""
}

// guardText runs the public-text guard over one text.
func (r *Runner) guardText(args []string, text string) (int, string) {
	var out, errb bytes.Buffer
	rc := guard.CIPublicTextGuardMain(append(append([]string{}, args...), "text", "-"), strings.NewReader(text), &out, &errb)
	return rc, out.String()
}

// Gate runs every check in the task clone and returns the record (Problems empty = pass).
func (r *Runner) Gate(run *Run) *GateRecord {
	at := r.at(run) // the worktree, its commands and its .hivemill files may be on a clone-mode machine
	lost := false
	at.lost = &lost
	g := at.git
	wt, base := run.Worktree, run.Base
	rec := &GateRecord{Checks: map[string]string{}}
	t, err := r.Store.Task(run.Task)
	if err != nil {
		rec.Problems = []string{err.Error()}
		return rec
	}
	crit := Criteria(t.Acceptance)
	logdir := r.logDir(run)
	_ = os.MkdirAll(logdir, 0o700)
	add := func(name string, ok bool, note string) {
		rec.Checks[name] = note
		if !ok {
			rec.Problems = append(rec.Problems, name+": "+note)
		}
	}
	_, dirty, _ := g(wt, "status", "--porcelain", "--untracked-files=no")
	if dirty != "" {
		add("committed", false, "uncommitted tracked changes: commit them")
	}
	_, rec.Head, _ = g(wt, "rev-parse", "HEAD")
	g(wt, "fetch", "-q", "origin", base)
	rec.PRURL = r.findPR(run, t)
	var pr *PR
	if rec.PRURL == "" {
		add("pr", false, fmt.Sprintf("no open PR for %s: push, then `hivemill pr create --title '%s: ...' --body -`", run.Branch, run.Task))
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
	rec.TestsBase, rec.TestsHead = r.testCount(at, wt, mb), r.testCount(at, wt, "HEAD")
	if pr != nil {
		dropped := rec.TestsBase >= 0 && rec.TestsHead >= 0 && rec.TestsHead < rec.TestsBase
		if probs := BodyProblems(pr.Body, dropped); len(probs) > 0 {
			add("body", false, strings.Join(probs, "; "))
		} else {
			rec.Checks["body"] = "follows docs/pr-descriptions.md"
		}
	}
	// the tier rules and the public-text terms are read here: a clone-mode run is checked in the coordinator's mirror
	var mirrored error
	if run.cloned() {
		mirrored = r.syncMirror(run, rec.Head)
	}
	if rules, _, err := r.TierRules(t.Project); err == nil && mirrored == nil {
		tier, why, err := risktier.ClassifyRange(run.coordWT(), "origin/"+base, "HEAD", rules)
		if err == nil {
			rec.Tier, rec.TierWhy = tier, strings.Join(why, "; ")
		}
	}
	ok, note := false, ""
	if mirrored != nil {
		note = mirrored.Error()
	} else {
		ok, note = r.publicText(run.Repo, run.coordWT(), base, pr)
	}
	add("public-text", ok, note)
	none, modeErr := r.ciMode(t.Project)
	cmd, limit := r.projectGate(t.Project)
	if modeErr != nil {
		add("affected", false, modeErr.Error())
	} else if none && strings.TrimSpace(cmd) == "" {
		add("affected", false, "ci_gate none and the project has no gate_cmd: never merged untested")
	} else if none {
		// gate_cmd is the only CI here, so it runs for every tier, on a clean checkout of HEAD
		ok, note, err := r.waitLocalCI(run, rec.Head, cmd, limit, logdir)
		if err != nil {
			note = err.Error()
			lost = lost || errors.Is(err, errLocalCIMachine)
		}
		add("affected", ok, note)
	} else if rec.Tier == "T0" {
		rec.Checks["affected"] = "n/a: docs only (T0)"
	} else if cmd != "" {
		ok, note = r.runProjectGateAt(at, wt, cmd, limit, logdir)
		add("affected", ok, note)
	} else {
		bin := ""
		if run.cloned() {
			bin = remoteBin(run.RemoteHome)
		}
		ok, note = r.affectedAt(at, bin, wt, base)
		add("affected", ok, note)
	}
	probs, findings, fhead := parseFindings(at.read(filepath.Join(wt, ".hivemill", "findings.json")))
	rec.Findings = findings
	for _, p := range probs {
		add("findings", false, p)
	}
	if len(probs) == 0 && fhead != rec.Head && !onlyBaseMerges(at, wt, base, fhead, rec.Head) {
		add("review", false, fmt.Sprintf("findings.json is for head %.9s, not HEAD %.9s: run /code-review on the current head and rewrite "+
			"the file with \"head\": the output of `git rev-parse HEAD`", fhead, rec.Head))
	}
	if !r.ReviewRan(run) {
		add("review", false, "no /code-review ran in this round: run /code-review, fix the findings, commit, then write .hivemill/findings.json")
	} else {
		rec.Checks["review"] = "/code-review ran in this round"
	}
	b, err := at.read(filepath.Join(wt, ".hivemill", "evidence.json"))
	ev := parseEvidence(b, err, crit)
	for _, p := range ev {
		add("evidence", false, p)
	}
	if len(ev) == 0 {
		dropBaseCheckout(at, wt)
		rec.Reruns = r.rerunProofs(at, run, crit, logdir)
		defects := r.specDefects(at, run, crit, rec.Reruns, mb, logdir)
		var first []string // spec defects lead, so a cut failure reason still names them
		for _, x := range rec.Reruns {
			if x.OK == nil || *x.OK {
				continue
			}
			p := fmt.Sprintf("criterion %d (%s): the factory re-ran its proof and it failed: %s", x.ID, x.Kind, x.Note)
			if why := defects[x.ID]; why != "" {
				first = append(first, fmt.Sprintf("%s %s (%s)", specDefect, p, why))
			} else {
				rec.Problems = append(rec.Problems, p)
			}
		}
		rec.Problems = append(first, rec.Problems...)
	}
	if lost {
		rec.Undecided, rec.Problems = true, []string{"machine: ssh to " + run.Machine + " failed during the checks; the gate is not decided"}
	}
	b, _ = json.MarshalIndent(rec, "", " ")
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

const (
	guardDenial    = "worker-guard: blocked"
	transcriptTail = 256 << 10
)

// failOnGuardBlock fails the run when the session stopped on a worker-guard denial: a denied command stays denied, so
// the session would only sit idle until the idle watch failed it as stalled, without the reason.
func (r *Runner) failOnGuardBlock(run *Run, h HookInput) bool {
	line := stoppedOnGuard(h)
	if line == "" {
		return false
	}
	r.fail(run, "guard block: "+truncate(line, 300))
	return true
}

// stoppedOnGuard returns the denial line when the last tool result in the transcript's tail is a worker-guard denial,
// else "". Without a readable transcript it looks for the line in the last assistant message.
func stoppedOnGuard(h HookInput) string {
	tail, err := readTail(h.TranscriptPath, transcriptTail)
	if err != nil {
		for _, l := range strings.Split(h.LastAssistantMessage, "\n") {
			if strings.HasPrefix(l, guardDenial) {
				return l
			}
		}
		return ""
	}
	denial := ""
	for _, raw := range bytes.Split(tail, []byte("\n")) {
		var d struct {
			Sidechain bool `json:"isSidechain"`
			Message   struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(raw, &d) != nil || d.Sidechain {
			continue
		}
		var blocks []struct {
			Type    string          `json:"type"`
			IsError bool            `json:"is_error"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(d.Message.Content, &blocks) != nil {
			continue // a plain text message
		}
		for _, b := range blocks {
			if b.Type != "tool_result" {
				continue
			}
			denial = ""
			if b.IsError {
				denial = denialLine(b.Content)
			}
		}
	}
	return denial
}

// denialLine is the guard message of a failed tool result (a string or a list of text blocks), from its first line:
// bare, or behind Claude Code's "PreToolUse:<tool> hook error: [<command>]: " prefix. Command output that only quotes
// a denial further down does not match.
func denialLine(content json.RawMessage) string {
	var text string
	if json.Unmarshal(content, &text) != nil {
		var parts []struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(content, &parts) != nil || len(parts) == 0 {
			return ""
		}
		text = parts[0].Text
	}
	first, _, _ := strings.Cut(text, "\n")
	if strings.HasPrefix(first, "PreToolUse:") {
		_, first, _ = strings.Cut(first, "]: ")
	}
	if !strings.HasPrefix(first, guardDenial) {
		return ""
	}
	return first
}

// readTail returns at most the last n bytes of the file. A line cut at the start does not parse and is skipped.
func readTail(path string, n int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() > n {
		if _, err := f.Seek(-n, io.SeekEnd); err != nil {
			return nil, err
		}
	}
	return io.ReadAll(io.LimitReader(f, n))
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
		if r.failOnGuardBlock(run, h) {
			return nil
		}
		_ = r.Log.Emit("gate.deferred", run, Event{Session: h.SessionID, Role: "task", Data: map[string]any{"why": "no PR yet"}})
		return nil
	}
	// A PR exists (a takeover of an older branch, or a later round) but no findings are written yet: the session is
	// still mid-work (a background /code-review writes its run record when it starts, so that record alone does not
	// mean the review is done). The stop is not a gate attempt; the idle watch nudges and then fails a session that
	// stays idle like this.
	if run.Eval && run.EvalMode == EvalReview {
		if _, findings, _ := parseFindings(r.at(run).read(filepath.Join(run.Worktree, ".hivemill", "findings.json"))); findings != nil {
			run.LastGate = &GateRecord{Findings: findings}
			run.FindingsTotal = len(findings)
			r.evalFinish(run, Evaluated, fmt.Sprintf("review: %d findings", len(findings)))
		}
		return nil
	}
	if _, err := r.at(run).read(filepath.Join(run.Worktree, ".hivemill", "findings.json")); err != nil {
		if r.failOnGuardBlock(run, h) {
			return nil
		}
		_ = r.Log.Emit("gate.deferred", run, Event{Session: h.SessionID, Role: "task", Data: map[string]any{"why": "no findings.json yet"}})
		return nil
	}
	rec := r.Gate(run)
	if rec.Undecided { // neither a pass nor an attempt: the idle watch brings the session back to stop again
		_ = r.Log.Emit("gate.deferred", run, Event{Session: h.SessionID, Role: "task", Data: map[string]any{"why": rec.Problems[0]}})
		return nil
	}
	if len(rec.Problems) == 0 {
		run.State, run.Head, run.PRURL, run.Tier, run.Gated, run.LastGate = Gated, rec.Head, rec.PRURL, rec.Tier, nowISO(), rec
		run.LastBlock = "" // a later failure is not about an earlier block
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
	if r.Env.Actor == PlannerName && h.SessionID != "" {
		return r.notePlannerSession(h.SessionID)
	}
	run := r.runFor(r.Env.Actor)
	if run == nil || h.SessionID == "" {
		return nil
	}
	r.noteSession(run, h.SessionID, r.Env.Actor)
	return r.Store.AgentSession(r.Env.Actor, h.SessionID)
}
