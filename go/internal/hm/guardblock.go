package hm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// A worker-guard block stops a task session (a denied command stays denied), and a generic nudge makes it refuse again
// until the run is "stalled". So the runner answers the run's first block with the block text and a way forward, fails
// the run at its second, and a session that cannot finish says so in .hivemill/blocked.md. The guard and its rules are
// not changed here.

// blockedPrefix starts the fail reason of a session that wrote .hivemill/blocked.md; the planner re-files such a task.
const blockedPrefix = "blocked by policy: "

var guardMarkers = []string{"worker-guard: block", "worker-guard(go): blocked"}

// guardNote starts the run history entry of a block; the entries are the run's block count.
const guardNote = "stopped on a guard block: "

const (
	guardDenial    = "worker-guard: blocked"
	transcriptTail = 256 << 10
)

// isGuardBlock reports whether an error result is a block: it starts with a marker (a hook of another tool starts with
// "PreToolUse:<tool> hook error" and names the guard). A failed command that only prints a marker further down is not.
func isGuardBlock(text string) bool {
	text = strings.TrimSpace(text)
	for _, m := range guardMarkers {
		if strings.HasPrefix(text, m) {
			return true
		}
	}
	return strings.HasPrefix(text, "PreToolUse:") && strings.Contains(text, "worker-guard")
}

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
			"files you changed (it is your content, not the command), commit and push again. Every unpushed commit is " +
			"scanned, so rewrite the unpushed commits that still hold it. If the blocked command was not a push, the text " +
			"is in what that command publishes (a commit message, PR body or comment): fix it there and run it again."},
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

// guardLine is the first line of a block without Claude Code's "PreToolUse:<tool> hook error: [<command>]: " prefix.
func guardLine(block string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(block), "\n")
	if strings.HasPrefix(line, "PreToolUse:") {
		if _, after, ok := strings.Cut(line, "]: "); ok {
			return after
		}
		if _, after, ok := strings.Cut(line, "hook error: "); ok {
			return after
		}
	}
	return line
}

// lastToolResult is the text of the last tool result in the transcript's tail and whether it was an error.
func lastToolResult(path string) (text string, isError bool, err error) {
	tail, err := readTail(path, transcriptTail)
	if err != nil {
		return "", false, err
	}
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
			isError, text = b.IsError, ""
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
	return text, isError, nil
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

// stoppedOnGuard is the block a Stop hook's session stopped on, or "". Without a readable transcript it looks for a
// denial line in the last assistant message.
func stoppedOnGuard(h HookInput) string {
	text, isError, err := lastToolResult(h.TranscriptPath)
	if err != nil {
		for _, l := range strings.Split(h.LastAssistantMessage, "\n") {
			if strings.HasPrefix(l, guardDenial) {
				return l
			}
		}
		return ""
	}
	if !isError || !isGuardBlock(text) {
		return ""
	}
	return text
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
	text, isError, _ := lastToolResult(newest)
	if !isError || !isGuardBlock(text) {
		return ""
	}
	return text
}

// onGuardBlock records a block of the run. For the first one it returns the prompt that resumes the session with the
// block and its fix; a later one fails the run, and it returns "".
func (r *Runner) onGuardBlock(run *Run, block string) string {
	class, fix := guardRemedy(block)
	again := slices.ContainsFunc(run.History, func(h string) bool {
		_, entry, _ := strings.Cut(h, ": ")
		return strings.HasPrefix(entry, guardNote)
	})
	line := truncate(guardLine(block), 300)
	run.History = append(run.History, fmt.Sprintf("round %d: %s%s", run.Round, guardNote, line))
	_ = r.Log.Emit("run.guard_block", run, Event{Role: "task", Data: map[string]any{"class": class}})
	if again {
		r.fail(run, "guard block: "+line)
		return ""
	}
	return "You stopped after a command was blocked:\n\n" + truncate(strings.TrimSpace(block), 1500) + "\n\n" + fix +
		"\n\nThen continue your role steps until the PR is open and CI is green, and stop."
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
