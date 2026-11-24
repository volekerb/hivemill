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
	Head             string           `json:"head,omitempty"`
	TS               string           `json:"ts,omitempty"`
	Invalid          bool             `json:"invalid,omitempty"`
}

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
	if v.Verdict != "MERGE" && v.Verdict != "REJECT" {
		return nil, errors.New(`verdict must be "MERGE" or "REJECT"`)
	}
	if len(v.Reasons) == 0 {
		return nil, errors.New("reasons must be a non-empty list of strings")
	}
	if v.CriteriaAssessed == nil {
		return nil, errors.New("criteria_assessed must be a list")
	}
	return &v, nil
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
	fmt.Fprintf(&b, "CI: green on the head. Gate: passed in round %d.\n\n## Spec\n%s\n\n## Criteria, proofs and the factory's re-run\n",
		run.Round, strings.TrimSpace(t.Spec))
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
	fmt.Fprintf(&b, "\n## Diff stat\n```\n%s\n```\n\n## Diff\n```diff\n%s\n```\n", strings.TrimSpace(stat), diff)
	return b.String(), nil
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
	model, effort, err := r.ModelFor(run.Risky || run.Tier == "T3")
	if err != nil {
		return err
	}
	run.Judges = append(run.Judges, name)
	run.JudgeHead, run.JudgeBlocks, run.State = run.Head, 0, Judging
	kick := fmt.Sprintf("You are `%s`, the factory task-judge. Judge %s (%s). Read the judge packet %s in your cwd, then answer "+
		"with the verdict JSON block.", name, run.Task, run.PRURL, rel)
	_, err = r.StartSession(run, Launch{Role: "task-judge", Name: name, Cwd: run.Worktree, Model: model, Effort: effort,
		PromptFile: filepath.Join(r.Env.Home, "roles", "hivemill", "task-judge.md"), Kickoff: kick})
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
	if perr != nil {
		run.JudgeBlocks++
		if run.JudgeBlocks < 2 {
			return json.NewEncoder(out).Encode(map[string]string{"decision": "block", "reason": perr.Error() +
				`. End with one fenced JSON block: {"verdict": "MERGE"|"REJECT", "reasons": [...], "criteria_assessed": [{"id": 1, "proven": true, "note": "..."}]}`})
		}
		v = &Verdict{Verdict: "REJECT", Reasons: []string{"judge gave no valid verdict (" + perr.Error() + ")"}, Invalid: true}
	}
	return r.RecordVerdict(run, v, h.SessionID, r.Env.Actor)
}

// RecordVerdict writes the verdict row (role task-judge, pass = MERGE), the run record and the event.
func (r *Runner) RecordVerdict(run *Run, v *Verdict, sid, author string) error {
	v.Head, v.TS = run.JudgeHead, nowISO()
	dir := filepath.Join(r.Env.StateDir(), "runs", run.Task)
	_ = os.MkdirAll(dir, 0o700)
	b, _ := json.MarshalIndent(v, "", " ")
	_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("verdict-r%d.json", len(run.Judges))), b, 0o600)
	verdict := map[string]string{"MERGE": "pass", "REJECT": "fail"}[v.Verdict]
	if err := r.Store.Verdict(run.Task, run.PRURL, run.JudgeHead, verdict, truncate(strings.Join(v.Reasons, "; "), 2000), author); err != nil {
		return err
	}
	_ = r.Store.Event(r.actor(), "task", run.Task, "run-judge-verdict", map[string]string{"verdict": v.Verdict, "head": truncate(run.JudgeHead, 12)})
	run.Verdicts = append(run.Verdicts, *v)
	run.State = Judged
	return r.Log.Emit("judge.verdict", run, Event{Session: sid, Role: "judge", Data: map[string]any{"verdict": v.Verdict,
		"reasons": v.Reasons, "criteria_assessed": v.CriteriaAssessed}})
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
