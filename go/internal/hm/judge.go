package hm

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Verdict is the judge's decision (docs/schemas/judge-verdict.schema.json).
type Verdict struct {
	Verdict          string           `json:"verdict"`
	Reasons          []string         `json:"reasons"`
	CriteriaAssessed []map[string]any `json:"criteria_assessed"`
	Changes          []Change         `json:"changes"`
	Head             string           `json:"head,omitempty"`
	TS               string           `json:"ts,omitempty"`
	Invalid          bool             `json:"invalid,omitempty"`
	// JudgeVerdict is what the judge wrote when the runner overruled it (a MERGE with an unmapped change).
	JudgeVerdict string `json:"judge_verdict,omitempty"`
}

// Change is one behaviour change the judge found in the diff, with the task criterion that asked for it (nil = none).
type Change struct {
	Change    string  `json:"change"`
	Criterion *string `json:"criterion"`
}

// Verdicts the judge may give. HOLD parks the PR for the owner: the change may be fine, but nothing in the task asked
// for it.
const (
	VerdictMerge  = "MERGE"
	VerdictReject = "REJECT"
	VerdictHold   = "HOLD"
)

const verdictShape = `{"verdict": "MERGE"|"REJECT"|"HOLD", "reasons": [...], "criteria_assessed": [{"id": 1, "proven": true, "note": "..."}], ` +
	`"changes": [{"change": "<quote from the diff>", "criterion": "<quote of the criterion's requirement>" or null}]}`

const diffMax = 60000

var jsonBlockRe = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*?\\})\\s*```")

// ParseVerdict reads the last JSON block of the judge's final message.
func ParseVerdict(text string) (*Verdict, error) {
	blocks := jsonBlockRe.FindAllStringSubmatch(text, -1)
	raw := ""
	if len(blocks) > 0 {
		raw = blocks[len(blocks)-1][1]
	} else if i := strings.LastIndex(text, `{"verdict"`); i >= 0 {
		raw = text[i:]
	}
	if raw == "" {
		return nil, errors.New("no JSON verdict block in your last message")
	}
	var v Verdict
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, fmt.Errorf("the verdict block is not valid JSON (%v)", err)
	}
	if v.Verdict != VerdictMerge && v.Verdict != VerdictReject && v.Verdict != VerdictHold {
		return nil, errors.New(`verdict must be "MERGE", "REJECT" or "HOLD"`)
	}
	if len(v.Reasons) == 0 {
		return nil, errors.New("reasons must be a non-empty list of strings")
	}
	if v.CriteriaAssessed == nil {
		return nil, errors.New("criteria_assessed must be a list")
	}
	if v.Changes == nil {
		return nil, errors.New("changes must be a list: every behaviour change in the diff, each with the criterion that asked for it or null")
	}
	for i, c := range v.Changes {
		if strings.TrimSpace(c.Change) == "" {
			return nil, fmt.Errorf("changes[%d].change is empty", i)
		}
	}
	return &v, nil
}

// ChangeProblems checks a verdict's change list against the task. Unmapped are the changes that name no criterion;
// misquoted are criterion quotes that are not in the task's requirements (a quote of a proof command counts as one:
// a change made so that a proof runs was not asked for). A MERGE needs a non-empty list and neither kind of problem.
func ChangeProblems(v *Verdict, t *Task) (unmapped, misquoted []string) {
	var reqs []string
	for _, c := range Criteria(t.Acceptance) {
		reqs = append(reqs, normQuote(requirement(c)))
	}
	spec, acc := normQuote(t.Spec), normQuote(t.Acceptance)
	for _, c := range v.Changes {
		if c.Criterion == nil || strings.TrimSpace(*c.Criterion) == "" {
			unmapped = append(unmapped, truncate(c.Change, 200))
			continue
		}
		q := normQuote(critPrefixRe.ReplaceAllString(strings.TrimSpace(*c.Criterion), ""))
		found := q != "" && strings.Contains(spec, q)
		for _, r := range reqs {
			found = found || (q != "" && strings.Contains(r, q))
		}
		switch {
		case found:
		case q != "" && strings.Contains(acc, q):
			misquoted = append(misquoted, fmt.Sprintf("%q quotes a proof, not a requirement (change: %s)", truncate(*c.Criterion, 120), truncate(c.Change, 120)))
		default:
			misquoted = append(misquoted, fmt.Sprintf("%q is not in the task's spec or criteria (change: %s)", truncate(*c.Criterion, 120), truncate(c.Change, 120)))
		}
	}
	return unmapped, misquoted
}

var (
	critPrefixRe = regexp.MustCompile(`(?i)^(?:criterion\s*|AC)?\d+\s*[.):]\s*`)
	quoteCharsRe = regexp.MustCompile("[`\"'“”‘’]")
)

func normQuote(s string) string {
	s = quoteCharsRe.ReplaceAllString(strings.ToLower(s), "")
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimRight(s, " .…")
}

// overruleMerge turns a MERGE whose change list has an unmapped change into a REJECT: an unasked change never merges
// on the judge's word alone.
func overruleMerge(v *Verdict, unmapped []string) {
	v.JudgeVerdict, v.Verdict = v.Verdict, VerdictReject
	reasons := []string{"the judge said MERGE, but these changes map to no task criterion; remove them, or the owner " +
		"adds a criterion that asks for them: " + strings.Join(unmapped, " | ")}
	v.Reasons = append(reasons, v.Reasons...)
}

// Packet assembles the judge's input deterministically from the DB, the gate record and git.
func (r *Runner) Packet(run *Run) (string, error) {
	t, err := r.Store.Task(run.Task)
	if err != nil {
		return "", err
	}
	g := run.LastGate
	if g == nil {
		g = &GateRecord{}
	}
	_, stat, _ := r.Tools.Git(run.Worktree, "diff", "--stat", "origin/"+run.Base+"...HEAD")
	_, diff, _ := r.Tools.Git(run.Worktree, "diff", "origin/"+run.Base+"...HEAD")
	if len(diff) > diffMax {
		diff = diff[:diffMax] + fmt.Sprintf("\n... diff cut at %d characters; read the rest in your cwd if needed\n", diffMax)
	}
	reruns := map[int]Rerun{}
	for _, x := range g.Reruns {
		reruns[x.ID] = x
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Judge packet: %s %s\n\nPR: %s  head %.12s\nRisk tier: %s (%s)\nTests (def test_ under tests/): base %d, head %d\n",
		t.ID, t.Title, run.PRURL, run.Head, or(g.Tier, "unknown"), or(g.TierWhy, "-"), g.TestsBase, g.TestsHead)
	ci, gate := "green on the head", fmt.Sprintf("passed in round %d", run.Round)
	if c := run.LocalCI; c != nil && r.ciNone(run.Project) {
		ci = fmt.Sprintf("no PR checks (ci_gate none); the project gate_cmd exited %d on head %.12s", c.RC, c.Head)
	}
	if run.CINote != "" {
		ci = run.CINote
	}
	if len(g.Problems) > 0 {
		gate = fmt.Sprintf("%d problems (listed under Gate checks)", len(g.Problems))
	}
	fmt.Fprintf(&b, "CI: %s. Gate: %s.\n\n## Spec\n%s\n\n## Criteria, proofs and the factory's re-run\n", ci, gate, strings.TrimSpace(t.Spec))
	for _, c := range Criteria(t.Acceptance) {
		x := reruns[c.ID]
		res := "not re-run"
		if x.OK != nil {
			res = map[bool]string{true: "PASS", false: "FAIL"}[*x.OK]
		}
		exp := ""
		if c.Expect != "" {
			exp = " => " + c.Expect
		}
		fmt.Fprintf(&b, "%d. %s\n   proof %s: %s%s\n   re-run: %s (%s)\n", c.ID, c.Text, c.Kind, c.Proof, exp, res, or(x.Note, "-"))
	}
	b.WriteString("\n## Review findings and resolutions\n")
	if len(g.Findings) == 0 {
		b.WriteString("- none reported\n")
	}
	for _, f := range g.Findings {
		line := 0
		if f.Line != nil {
			line = *f.Line
		}
		fmt.Fprintf(&b, "- [%s] %s:%d %s -> %s\n", f.Severity, f.File, line, f.Issue, f.Resolution)
	}
	if len(run.History) > 0 {
		b.WriteString("\n## Earlier rounds\n")
		for _, h := range run.History {
			fmt.Fprintf(&b, "- %s\n", h)
		}
	}
	b.WriteString("\n## Gate checks\n")
	for _, k := range sortedKeys(g.Checks) {
		fmt.Fprintf(&b, "- %s: %s\n", k, truncate(g.Checks[k], 200))
	}
	_, names, _ := r.Tools.Git(run.Worktree, "diff", "--name-only", "origin/"+run.Base+"...HEAD")
	b.WriteString("\n## Scope check (mechanical)\n")
	b.WriteString(ScopeSection(DeclaredScope(t), strings.Fields(names)))
	fmt.Fprintf(&b, "\n## Diff stat\n```\n%s\n```\n\n## Diff\n```diff\n%s\n```\n", strings.TrimSpace(stat), diff)
	return b.String(), nil
}

// ScopeSection is the packet's scope check: what the task declares and every changed file outside it, flagged.
func ScopeSection(s Scope, files []string) string {
	var b strings.Builder
	for _, x := range s.Bad {
		fmt.Fprintf(&b, "- unreadable declaration: %s\n", x)
	}
	if !s.Declared() {
		b.WriteString("- no declared scope: the task names no FILES: line and no scope criterion\n")
		return b.String()
	}
	for _, x := range s.Sources {
		fmt.Fprintf(&b, "- declared: %s\n", x)
	}
	out := s.OutOfScope(files)
	if len(out) == 0 {
		fmt.Fprintf(&b, "- all %d changed files are inside the declared scope\n", len(files))
		return b.String()
	}
	fmt.Fprintf(&b, "- FLAGGED: %d of %d changed files are outside the declared scope. Each change in them must map to a "+
		"criterion, or the verdict is REJECT (or HOLD when the change is plausibly fine):\n", len(out), len(files))
	for _, x := range out {
		fmt.Fprintf(&b, "  - %s\n", x)
	}
	return b.String()
}

func or(a, b string) string {
	if a == "" {
		return b
	}
	return a
}

// StartJudge launches a fresh, read-only judge session on the run's PR.
func (r *Runner) StartJudge(run *Run) error {
	n := len(run.Judges) + 1
	name := fmt.Sprintf("claude-task-judge-%s-r%d", strings.ToLower(run.Task), n)
	packet, err := r.Packet(run)
	if err != nil {
		return err
	}
	rel := fmt.Sprintf(".hivemill/judge-packet-r%d.md", n)
	if err := os.WriteFile(filepath.Join(run.Worktree, rel), []byte(packet), 0o644); err != nil {
		return err
	}
	// The judge's model follows the files the diff really changes (and the declared scope), not the task's words.
	t, err := r.Store.Task(run.Task)
	if err != nil {
		return err
	}
	_, names, _ := r.Tools.Git(run.Worktree, "diff", "--name-only", "origin/"+run.Base+"...HEAD")
	risky, _ := Risk(r.Env.Home, t, strings.Fields(names))
	model, effort, err := r.ModelFor(risky || run.Tier == "T3")
	if err != nil {
		return err
	}
	run.Judges = append(run.Judges, name)
	run.JudgeHead, run.JudgeBlocks, run.State = run.Head, 0, Judging
	kick := fmt.Sprintf("You are `%s`, the factory task-judge. Judge %s (%s). Read the judge packet %s in your cwd, then answer "+
		"with the verdict JSON block.", name, run.Task, run.PRURL, rel)
	_, err = r.StartSession(run, Launch{Role: "task-judge", Name: name, Cwd: run.Worktree, Model: model, Effort: effort,
		PromptFile: filepath.Join(r.Env.Home, "roles", "hivemill", "task-judge.md"), Kickoff: kick})
	if err == nil {
		r.recordPrompt(run, "judge", "judge", packet) // the judge reads it from the file the kickoff names
	}
	return err
}

// LastAssistantText is the judge's final message: from the hook input, else from the transcript.
func LastAssistantText(h HookInput) string {
	if strings.TrimSpace(h.LastAssistantMessage) != "" {
		return h.LastAssistantMessage
	}
	f, err := os.Open(h.TranscriptPath)
	if err != nil {
		return ""
	}
	defer f.Close()
	text := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var d struct {
			Type    string `json:"type"`
			Message struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &d) != nil || d.Type != "assistant" {
			continue
		}
		var parts []string
		for _, c := range d.Message.Content {
			if c.Type == "text" && strings.TrimSpace(c.Text) != "" {
				parts = append(parts, c.Text)
			}
		}
		if len(parts) > 0 {
			text = strings.Join(parts, "\n")
		}
	}
	return text
}

// JudgeHook is the judge session's Stop hook: it records the verdict, or refuses the stop once when there is none.
func (r *Runner) JudgeHook(in io.Reader, out io.Writer) error {
	h := readHook(in)
	found := r.runFor(r.Env.Actor)
	if found == nil {
		return nil
	}
	l := r.lock(found.Task, true)
	if l != nil {
		defer l.Close()
	}
	run, err := r.Store.Run(found.Task)
	if err != nil || run == nil || run.State != Judging || last(run.Judges) != r.Env.Actor {
		return err
	}
	defer func() { _ = r.Store.PutRun(run) }()
	r.noteSession(run, h.SessionID, r.Env.Actor)
	v, perr := ParseVerdict(LastAssistantText(h))
	if perr == nil && v.Verdict == VerdictMerge {
		perr = r.mergeChangeCheck(run, v)
	}
	if perr != nil {
		run.JudgeBlocks++
		if run.JudgeBlocks < 2 {
			return json.NewEncoder(out).Encode(map[string]string{"decision": "block", "reason": perr.Error() +
				". End with one fenced JSON block: " + verdictShape})
		}
		v = &Verdict{Verdict: VerdictReject, Reasons: []string{"judge gave no valid verdict (" + perr.Error() + ")"}, Invalid: true,
			Changes: []Change{}}
	}
	return r.RecordVerdict(run, v, h.SessionID, r.Env.Actor)
}

// mergeChangeCheck holds a MERGE to its change list. A change mapped to no criterion overrules it to REJECT at once.
// An empty list, or a criterion quote that is not in the task, is a malformed verdict: the stop is refused once so the
// judge can fix it, and a second one counts as REJECT.
func (r *Runner) mergeChangeCheck(run *Run, v *Verdict) error {
	if len(v.Changes) == 0 {
		return errors.New("a MERGE needs a non-empty changes list: list every behaviour change in the diff, each with the criterion it serves")
	}
	t, err := r.Store.Task(run.Task)
	if err != nil {
		return nil // the runner cannot read the task: the mapping stays unchecked, the merge gate still applies
	}
	unmapped, misquoted := ChangeProblems(v, t)
	if len(unmapped) > 0 {
		overruleMerge(v, unmapped)
		return nil
	}
	if len(misquoted) > 0 {
		return errors.New("each criterion must quote the task's own words for what was asked, never a proof command: " +
			strings.Join(misquoted, "; ") + ". Quote the requirement word for word, or set criterion to null and give REJECT or HOLD")
	}
	return nil
}

// RecordVerdict writes the verdict row (role task-judge, pass = MERGE), the run record and the event.
func (r *Runner) RecordVerdict(run *Run, v *Verdict, sid, author string) error {
	v.Head, v.TS = run.JudgeHead, nowISO()
	dir := filepath.Join(r.Env.StateDir(), "runs", run.Task)
	_ = os.MkdirAll(dir, 0o700)
	b, _ := json.MarshalIndent(v, "", " ")
	_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("verdict-r%d.json", len(run.Judges))), b, 0o600)
	// Only MERGE is "pass": a HOLD must never read as a merge verdict to the merge decision or the old merger.
	verdict := "fail"
	if v.Verdict == VerdictMerge {
		verdict = "pass"
	}
	// An eval run writes no verdicts row: nothing outside the runner may ever read an eval MERGE as a merge verdict.
	if !run.Eval {
		if err := r.Store.Verdict(run.Task, run.PRURL, run.JudgeHead, verdict, truncate(strings.Join(v.Reasons, "; "), 2000), author); err != nil {
			return err
		}
	}
	_ = r.Store.Event(r.actor(), "task", run.Task, "run-judge-verdict", map[string]string{"verdict": v.Verdict, "head": truncate(run.JudgeHead, 12)})
	run.Verdicts = append(run.Verdicts, *v)
	run.State = Judged
	data := map[string]any{"verdict": v.Verdict, "reasons": v.Reasons, "criteria_assessed": v.CriteriaAssessed, "changes": v.Changes}
	if v.JudgeVerdict != "" {
		data["judge_verdict"] = v.JudgeVerdict
	}
	return r.Log.Emit("judge.verdict", run, Event{Session: sid, Role: "judge", Data: data})
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
