package obs

import (
	"encoding/json"
	"regexp"
	"strings"
)

// The only strings taken from a transcript's content are names checked against these rules. Everything else in a
// message (text, prompts, tool input and output, file contents, paths) is read, used for a decision, and dropped.

// nameRe is what a tool or skill name may look like to be stored; anything else is stored as "other".
var nameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.:-]{0,63}$`)

func safeName(s string) string {
	if nameRe.MatchString(s) {
		return s
	}
	return "other"
}

// bashHeads are the commands a Bash call is stored as; any other command is "other". Arguments are always dropped.
var bashHeads = map[string]bool{
	"awk": true, "cat": true, "cd": true, "cargo": true, "cp": true, "curl": true, "diff": true, "echo": true,
	"factory": true, "find": true, "gh": true, "git": true, "go": true, "gofmt": true, "grep": true, "head": true,
	"jq": true, "ls": true, "make": true, "mkdir": true, "mv": true, "node": true, "npm": true, "npx": true,
	"pytest": true, "python3": true, "python": true, "rg": true, "rm": true, "sed": true, "sleep": true, "sort": true,
	"tail": true, "test": true, "timeout": true, "wc": true, "hm-spend": true, "hm-obs": true, "pr-flow": true,
}

// bashSubs are the subcommands kept after a head; any other subcommand is dropped (the head alone is stored).
var bashSubs = map[string]map[string]bool{
	"git": set("add", "branch", "checkout", "commit", "diff", "fetch", "log", "merge", "pull", "push", "rebase",
		"reset", "restore", "rev-parse", "show", "stash", "status", "switch", "worktree"),
	"gh":  set("api", "auth", "issue", "pr", "release", "repo", "run", "workflow"),
	"go":  set("build", "fmt", "mod", "run", "test", "vet"),
	"npm": set("ci", "install", "run", "test"),
	"factory": set("bus", "done", "gate", "goal", "inbox", "judge", "msg", "plan", "pr", "prcheck", "qa", "res",
		"review", "spawn", "task", "team", "tick", "verdict", "whoami"),
}

func set(xs ...string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// BashHead reduces a shell command to its allowlisted head ("git push", "ls", "other"). Leading VAR=value
// assignments are skipped; a path to a command counts as its base name.
func BashHead(cmd string) string {
	fields := strings.Fields(cmd)
	i := 0
	for i < len(fields) && strings.Contains(fields[i], "=") && !strings.HasPrefix(fields[i], "=") {
		i++
	}
	if i >= len(fields) {
		return "other"
	}
	head := fields[i]
	if j := strings.LastIndex(head, "/"); j >= 0 {
		head = head[j+1:]
	}
	if !bashHeads[head] {
		return "other"
	}
	if subs := bashSubs[head]; subs != nil && i+1 < len(fields) && subs[fields[i+1]] {
		return head + " " + fields[i+1]
	}
	return head
}

// toolUse is one tool call of an assistant line, reduced to what may be stored.
type toolUse struct {
	ID, Tool, Skill, Head string
}

// toolResult is the size of one tool result of a user line.
type toolResult struct {
	ID    string
	Bytes int
}

type block struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
}

// contentLine is the part of a transcript line ingest looks at besides usage.
type contentLine struct {
	Type      string `json:"type"`
	AgentName string `json:"agentName"`
	Cwd       string `json:"cwd"`
	Message   struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// tools returns the tool calls (assistant line) or tool result sizes (user line) of one decoded line.
func tools(l *contentLine) (uses []toolUse, results []toolResult) {
	if len(l.Message.Content) == 0 || l.Message.Content[0] != '[' {
		return nil, nil
	}
	var blocks []block
	if json.Unmarshal(l.Message.Content, &blocks) != nil {
		return nil, nil
	}
	for _, b := range blocks {
		switch {
		case b.Type == "tool_use" && b.ID != "":
			u := toolUse{ID: safeName(b.ID), Tool: safeName(b.Name)}
			switch b.Name {
			case "Skill":
				var in struct {
					Skill string `json:"skill"`
				}
				if json.Unmarshal(b.Input, &in) == nil && in.Skill != "" {
					u.Skill = safeName(in.Skill)
				}
			case "Bash":
				var in struct {
					Command string `json:"command"`
				}
				if json.Unmarshal(b.Input, &in) == nil {
					u.Head = BashHead(in.Command)
				}
			}
			uses = append(uses, u)
		case b.Type == "tool_result" && b.ToolUseID != "":
			results = append(results, toolResult{ID: safeName(b.ToolUseID), Bytes: resultSize(b.Content)})
		}
	}
	return uses, results
}

// resultSize is the size in bytes of a tool result's text: the string itself, or its text blocks summed.
func resultSize(raw json.RawMessage) int {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return len(s)
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		n := 0
		for _, p := range parts {
			n += len(p.Text)
		}
		return n
	}
	return len(raw)
}

// kickoffAgentRe finds the agent name in a factory kickoff ("You are `claude-coder-t-1826`, the factory coder.").
var kickoffAgentRe = regexp.MustCompile("You are `([a-z0-9][a-z0-9-]{0,63})`")

// taskRe finds a factory worktree's goal and task in a path (web/tokens.py TASK_RE). Only the two ids are kept.
var taskRe = regexp.MustCompile(`worktrees[-/](G-\d+)-(T-\d+)`)
