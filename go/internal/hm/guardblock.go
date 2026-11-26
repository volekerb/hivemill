package hm

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// A worker-guard block stops a task session (a denied command stays denied), and a generic nudge makes it refuse again
// until the run is "stalled". So the runner answers a block with the block text and a way forward, and a session that
// cannot finish says so in .hivemill/blocked.md. The guard and its rules are not changed here.

// blockedPrefix starts the fail reason of a session that wrote .hivemill/blocked.md; the planner re-files such a task.
const blockedPrefix = "blocked by policy: "

var guardMarkers = []string{"worker-guard: block", "worker-guard(go): blocked", "PreToolUse:Bash hook error"}

// guardClasses is the remediation per block class, first match wins. A text-file block is also a public-text block, so
// "read-before" comes first. "<hits>" is replaced by the match's first group.
var guardClasses = []struct {
	Class string
	Match *regexp.Regexp // nil matches every block
	Fix   string
}{
	{"read-before", regexp.MustCompile(`read before the command runs`),
		"Split it: write the file in one command, then read or use it in the next command."},
	{"public-text", regexp.MustCompile(`public text: (\d+ hits?: [^)\n]*)`),
		"The push was blocked because your diff contains private text (<hits>). Remove or generalize that text in the " +
			"files you changed (it is your content, not the command), commit and push again."},
	{"other", nil,
		"This command is blocked by policy. Do not run it in another form. Reach the same goal another way that does not " +
			"need it (for example, a different tool or a code change). If the task cannot be finished without it, write " +
			".hivemill/blocked.md with the exact command and reason, then stop."},
}

// guardRemedy is the class of a block and what to tell the session.
func guardRemedy(block string) (class, fix string) {
	for _, c := range guardClasses {
		if c.Match == nil {
			return c.Class, c.Fix
		}
		if m := c.Match.FindStringSubmatch(block); m != nil {
			if len(m) > 1 {
				return c.Class, strings.Replace(c.Fix, "<hits>", strings.TrimSpace(m[1]), 1)
			}
			return c.Class, c.Fix
		}
	}
	return "", ""
}

// lastToolResult is the text of a transcript's last tool result and whether it was an error.
func lastToolResult(path string) (text string, isError bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var d struct {
			Type      string `json:"type"`
			Sidechain bool   `json:"isSidechain"`
			Message   struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &d) != nil || d.Type != "user" || d.Sidechain {
			continue
		}
		var blocks []struct {
			Type    string          `json:"type"`
			IsError bool            `json:"is_error"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(d.Message.Content, &blocks) != nil { // a plain prompt is a string
			continue
		}
		for _, b := range blocks {
			if b.Type != "tool_result" {
				continue
			}
			isError = b.IsError
			if json.Unmarshal(b.Content, &text) == nil {
				continue
			}
			var parts []struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(b.Content, &parts)
			var s []string
			for _, p := range parts {
				s = append(s, p.Text)
			}
			text = strings.Join(s, "\n")
		}
	}
	return text, isError
}

// guardBlock is the block that the run's task session stopped on, or "" when its last tool result is not one. It reads
// the newest transcript of the run's task sessions (remote machines share ~/.claude/projects). Only an error result
// counts: a command that merely prints a marker was not blocked.
func (r *Runner) guardBlock(run *Run) string {
	sess, err := r.Store.Sessions(run.Task)
	if err != nil {
		return ""
	}
	newest, at := "", time.Time{}
	for _, s := range sess {
		if s.Role != "task" || s.Agent != run.Agent || s.SID == "" {
			continue
		}
		paths, _ := filepath.Glob(filepath.Join(r.Env.UserHome, ".claude", "projects", "*", s.SID+".jsonl"))
		for _, p := range paths {
			if fi, err := os.Stat(p); err == nil && fi.ModTime().After(at) {
				newest, at = p, fi.ModTime()
			}
		}
	}
	if newest == "" {
		return ""
	}
	text, isError := lastToolResult(newest)
	if !isError {
		return ""
	}
	for _, m := range guardMarkers {
		if strings.Contains(text, m) {
			return text
		}
	}
	return ""
}

// resumeBlocked prompts a session that stopped on a guard block with the block and its fix; false when there is none.
func (r *Runner) resumeBlocked(run *Run) bool {
	block := r.guardBlock(run)
	if block == "" {
		return false
	}
	class, fix := guardRemedy(block)
	_ = r.Prompt(run.Agent, "You stopped after a command was blocked:\n\n"+truncate(strings.TrimSpace(block), 1500)+"\n\n"+fix+
		"\n\nThen continue your role steps until the PR is open and CI is green, and stop.")
	_ = r.Log.Emit("run.guard_block", run, Event{Role: "task", Data: map[string]any{"class": class}})
	return true
}

// failBlocked fails the run when its session wrote .hivemill/blocked.md.
func (r *Runner) failBlocked(run *Run) bool {
	b, err := os.ReadFile(filepath.Join(run.Worktree, ".hivemill", "blocked.md"))
	if err != nil {
		return false
	}
	line := "no reason written"
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "#")); l != "" {
			line = l
			break
		}
	}
	r.fail(run, blockedPrefix+truncate(line, 300))
	return true
}
