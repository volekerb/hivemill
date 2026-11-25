package hm

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"factory/go/internal/shlex"
)

// The planner's PreToolUse hook (`hivemill planner hook`) is an allow-list: the planner plans, it never changes code or
// the machine. Bash may run only `hivemill goal|task|run status|report|health|ask add|list|withdraw`, read-only git and
// gh reads, optionally piped into a read-only filter; Edit, Write and NotebookEdit are refused; Read, Grep and Glob are
// refused under hidden directories of the user's home (credentials).

// PlannerHook reads the hook JSON and returns the reason to block ("" = allow). An unreadable input blocks.
func PlannerHook(in io.Reader, userHome string) string {
	var h struct {
		Tool  string         `json:"tool_name"`
		Input map[string]any `json:"tool_input"`
	}
	if err := json.NewDecoder(in).Decode(&h); err != nil {
		return "planner hook: unreadable hook input"
	}
	str := func(k string) string { v, _ := h.Input[k].(string); return v }
	switch h.Tool {
	case "Bash":
		return PlannerBash(str("command"))
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		return "the planner never edits files: file a task (`hivemill task add`) for the change"
	case "Read", "Grep", "Glob":
		for _, k := range []string{"file_path", "path", "notebook_path"} {
			if p := str(k); p != "" && !plannerPathOK(p, userHome) {
				return fmt.Sprintf("the planner does not read %s (hidden files in the home directory hold credentials)", p)
			}
		}
	}
	return ""
}

func plannerPathOK(p, userHome string) bool {
	if strings.HasPrefix(p, "~/") || p == "~" {
		p = filepath.Join(userHome, strings.TrimPrefix(p, "~"))
	}
	if !filepath.IsAbs(p) || userHome == "" {
		return true
	}
	rel, err := filepath.Rel(filepath.Clean(userHome), filepath.Clean(p))
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return true
	}
	return !strings.HasPrefix(rel, ".")
}

var heredocRe = regexp.MustCompile(`<<-?\s*(['"])([A-Za-z_][A-Za-z0-9_]*)['"]\s*$`)

// PlannerBash returns why a planner Bash command is refused ("" = allowed).
func PlannerBash(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return "empty command"
	}
	// One quoted heredoc (no expansion inside) may feed stdin: `hivemill task add ... --acceptance-file - <<'EOF'`.
	if i := unquotedNewline(cmd); i >= 0 {
		first, rest := cmd[:i], cmd[i+1:]
		m := heredocRe.FindStringSubmatchIndex(first)
		if m == nil {
			return "one command per call: no newlines outside quotes, except one quoted heredoc (<<'EOF')"
		}
		tag := first[m[4]:m[5]]
		lines := strings.Split(rest, "\n")
		if strings.TrimSpace(lines[len(lines)-1]) != tag {
			return "the heredoc must end with its tag on the last line"
		}
		cmd = strings.TrimSpace(first[:m[0]])
	}
	if why := unquotedMeta(cmd); why != "" {
		return why
	}
	segs := splitPipes(cmd)
	for i, seg := range segs {
		argv, err := shlex.Split(seg)
		if err != nil || len(argv) == 0 {
			return "cannot parse the command"
		}
		if i == 0 {
			if why := plannerCommand(argv); why != "" {
				return why
			}
		} else if why := plannerFilter(argv); why != "" {
			return why
		}
	}
	return ""
}

// unquotedNewline is the index of the first newline outside quotes (-1 when none).
func unquotedNewline(s string) int {
	var q byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q == '\'':
			if c == '\'' {
				q = 0
			}
		case c == '\\':
			i++
		case q == '"':
			if c == '"' {
				q = 0
			}
		case c == '\'' || c == '"':
			q = c
		case c == '\n':
			return i
		}
	}
	return -1
}

// unquotedMeta refuses shell control outside single quotes: command separators, redirects, substitutions and `||`.
// A single `|` is allowed (pipes are checked per segment).
func unquotedMeta(s string) string {
	var q byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q == '\'':
			if c == '\'' {
				q = 0
			}
			continue
		case c == '\\':
			i++
			continue
		case q == '"':
			if c == '"' {
				q = 0
			} else if c == '`' || (c == '$' && i+1 < len(s) && s[i+1] == '(') {
				return "no command substitution"
			}
			continue
		case c == '\'' || c == '"':
			q = c
		case c == ';' || c == '&' || c == '>' || c == '<' || c == '`' || c == '\n' || c == '(' || c == ')':
			return fmt.Sprintf("one plain command per call: %q is not allowed outside quotes", c)
		case c == '$' && i+1 < len(s) && s[i+1] == '(':
			return "no command substitution"
		case c == '|' && i+1 < len(s) && s[i+1] == '|':
			return "no ||"
		}
	}
	if q != 0 {
		return "unclosed quote"
	}
	return ""
}

func splitPipes(s string) []string {
	var out []string
	var q byte
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q == '\'':
			if c == '\'' {
				q = 0
			}
		case c == '\\':
			i++
		case q == '"':
			if c == '"' {
				q = 0
			}
		case c == '\'' || c == '"':
			q = c
		case c == '|':
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

var (
	gitRead = map[string]bool{"log": true, "show": true, "diff": true, "status": true, "blame": true, "ls-files": true, "grep": true,
		"rev-parse": true, "shortlog": true, "describe": true, "ls-tree": true, "cat-file": true, "rev-list": true, "merge-base": true}
	ghRead = map[string]map[string]bool{"pr": {"list": true, "view": true, "diff": true, "checks": true, "status": true},
		"issue": {"list": true, "view": true}, "run": {"list": true, "view": true}, "repo": {"view": true}}
	hmPlanner = map[string]map[string]bool{"goal": nil, "task": nil, "report": nil, "health": nil,
		"run": {"status": true}, "ask": {"add": true, "list": true, "withdraw": true}}
)

func plannerCommand(argv []string) string {
	name := filepath.Base(argv[0])
	if strings.Contains(argv[0], "=") {
		return "no environment assignments"
	}
	switch name {
	case "hivemill":
		if len(argv) < 2 {
			return "hivemill needs a subcommand"
		}
		subs, ok := hmPlanner[argv[1]]
		if !ok {
			return fmt.Sprintf("the planner may run hivemill goal, task, run status, report, health and ask add|list|withdraw, not %q", argv[1])
		}
		if subs != nil && (len(argv) < 3 || !subs[argv[2]]) {
			return fmt.Sprintf("hivemill %s: the planner may only run %s", argv[1], strings.Join(keys(subs), ", "))
		}
		return ""
	case "git":
		args := argv[1:]
		for len(args) >= 2 && args[0] == "-C" {
			args = args[2:]
		}
		if len(args) == 0 || !gitRead[args[0]] {
			return "the planner may only run read-only git (log, show, diff, status, blame, grep, ls-files, rev-parse, ...), with -C DIR"
		}
		for _, a := range args[1:] {
			if strings.HasPrefix(a, "--output") || strings.HasPrefix(a, "--ext-diff") || strings.HasPrefix(a, "-O") ||
				strings.HasPrefix(a, "--open-files-in-pager") || strings.HasPrefix(a, "--textconv") {
				return "git option " + a + " is not read-only"
			}
		}
		return ""
	case "gh":
		if len(argv) < 3 || ghRead[argv[1]] == nil || !ghRead[argv[1]][argv[2]] {
			return "the planner may only read with gh: pr list|view|diff|checks|status, issue list|view, run list|view, repo view"
		}
		return ""
	}
	return fmt.Sprintf("the planner may only run hivemill, read-only git and gh reads, not %s", name)
}

func plannerFilter(argv []string) string {
	switch filepath.Base(argv[0]) {
	case "head", "tail", "wc", "grep", "cut", "jq":
		return ""
	case "sort":
		for _, a := range argv[1:] {
			if strings.HasPrefix(a, "-o") || strings.HasPrefix(a, "--output") {
				return "sort -o writes a file"
			}
		}
		return ""
	}
	return "after a pipe the planner may only use head, tail, wc, grep, cut, sort or jq"
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
