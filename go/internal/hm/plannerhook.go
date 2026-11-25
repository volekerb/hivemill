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
		Cwd   string         `json:"cwd"`
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
			if p := str(k); p != "" && !plannerPathOK(p, h.Cwd, userHome) {
				return fmt.Sprintf("the planner does not read %s (hidden files in the home directory hold credentials)", p)
			}
		}
	}
	return ""
}

func plannerPathOK(p, cwd, userHome string) bool {
	if strings.HasPrefix(p, "~/") || p == "~" {
		p = filepath.Join(userHome, strings.TrimPrefix(p, "~"))
	}
	if !filepath.IsAbs(p) {
		if cwd == "" {
			return false // a relative path with no cwd to resolve it against: refuse
		}
		p = filepath.Join(cwd, p)
	}
	if userHome == "" {
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
			if a == "--no-index" || strings.HasPrefix(a, "--output") || strings.HasPrefix(a, "--ext-diff") || strings.HasPrefix(a, "-O") ||
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

// filterRule is a read-only filter after a pipe: it reads stdin only. Value flags take that many following tokens;
// refused flags read files or write them; positional is how many operands (a pattern, a jq program) it may have.
type filterRule struct {
	value      map[string]int
	refused    []string
	positional int
}

var filters = map[string]filterRule{
	"head": {value: map[string]int{"-n": 1, "-c": 1, "--lines": 1, "--bytes": 1}},
	"tail": {value: map[string]int{"-n": 1, "-c": 1, "--lines": 1, "--bytes": 1}, refused: []string{"-f", "-F", "--follow"}},
	"wc":   {refused: []string{"--files0-from"}},
	"cut":  {value: map[string]int{"-d": 1, "-f": 1, "-c": 1, "-b": 1, "--delimiter": 1, "--fields": 1}},
	"sort": {value: map[string]int{"-k": 1, "-t": 1, "--key": 1}, refused: []string{"-o", "--output", "--files0-from", "-T", "--temporary-directory"}},
	"grep": {value: map[string]int{"-e": 1, "-m": 1, "-A": 1, "-B": 1, "-C": 1, "--regexp": 1, "--max-count": 1}, positional: 1,
		refused: []string{"-f", "--file", "-r", "-R", "--recursive", "--dereference-recursive", "-d", "--directories", "--exclude-from"}},
	"jq": {value: map[string]int{"--arg": 2, "--argjson": 2, "--indent": 1}, positional: 1,
		refused: []string{"-f", "--from-file", "--rawfile", "--slurpfile", "--argfile", "-L", "--jsonargs", "--args"}},
}

func plannerFilter(argv []string) string {
	name := filepath.Base(argv[0])
	rule, ok := filters[name]
	if !ok {
		return "after a pipe the planner may only use head, tail, wc, grep, cut, sort or jq"
	}
	pos := 0
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		if len(a) > 1 && strings.HasPrefix(a, "-") { // -5 (head) is a flag too
			for _, ref := range rule.refused {
				if a == ref || strings.HasPrefix(a, ref+"=") || (len(ref) == 2 && !strings.HasPrefix(a, "--") && strings.HasPrefix(a, ref)) {
					return fmt.Sprintf("%s %s reads or writes a file", name, a)
				}
			}
			if name == "grep" && (a == "-e" || strings.HasPrefix(a, "--regexp")) {
				pos = rule.positional // the pattern comes with -e: no positional pattern is left
			}
			i += rule.value[a]
			continue
		}
		pos++
	}
	if pos > rule.positional {
		return fmt.Sprintf("%s after a pipe reads stdin only: no file operands", name)
	}
	return ""
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
