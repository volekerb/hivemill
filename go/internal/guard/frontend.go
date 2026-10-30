package guard

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"factory/go/internal/shlex"
)

// Block is the Go form of worker-guard.py:Block, the exception every rule raises to refuse a command.
type Block struct{ Msg string }

func (b *Block) Error() string { return b.Msg }

func blockf(msg string) error { return &Block{Msg: msg} }

// Module constants of worker-guard.py (the pure-literal ones; the sets that depend on the passwd entry
// or the live home are added with the sections that use them).
const (
	// MaxDepth is worker-guard.py:MAX_DEPTH, the nesting limit of Analyzer.script.
	MaxDepth = 6
	// Claude package named by the Claude rule: worker-guard.py:CLAUDE_PKG.
	ClaudePkg = "@anthropic-ai/claude-code"
	// HeredocWordEnd is worker-guard.py:HEREDOC_WORD_END.
	HeredocWordEnd = " \t\n;&|()<>"
)

var (
	FactoryUnitCmds  = set("up", "tick", "relaunch", "spawn", "clean")
	PrivWords        = set("sudo", "su", "doas", "pkexec", "docker")
	WrapData         = set("alias", "ln", "cp", "mv", "install", "rsync", "nc", "ncat", "netcat", "socat")
	PersistEditTools = set("Edit", "Write", "MultiEdit", "NotebookEdit")
	TermsRoles       = set("coder", "qa", "reviewer", "judge", "pm")
	Shells           = set("sh", "bash", "zsh", "dash", "ksh", "ash", "fish", "csh", "tcsh", "rbash")
	Wrappers         = set("sudo", "doas", "pkexec", "su", "runuser", "env", "exec", "nohup", "timeout", "xargs",
		"command", "builtin", "busybox", "nice", "ionice", "stdbuf", "setsid", "chrt", "taskset", "setpriv", "unshare",
		"nsenter", "chroot", "flock", "watch", "script", "unbuffer", "ssh", "tmux", "screen", "parallel", "at",
		"systemd-run", "strace", "ltrace", "gdb", "valgrind", "perf", "fakeroot", "bwrap", "firejail",
		"xvfb-run", "dbus-run-session", "cgexec", "proot", "expect", "alias", "ln", "cp", "mv", "install",
		"rsync", "nc", "ncat", "netcat", "socat")
	Reserved = set("if", "then", "else", "elif", "fi", "do", "done", "while", "until", "time", "!", "{", "}",
		"coproc", "function")
	SystemctlVerbs = set("enable", "start", "stop", "restart", "reload", "daemon-reload", "daemon-reexec", "disable",
		"kill", "mask", "unmask", "link", "reenable", "preset", "try-restart", "reload-or-restart",
		"try-reload-or-restart", "isolate", "edit", "set-property", "revert")
	UnitMutating = set("start", "stop", "restart", "reload", "try-restart", "reload-or-restart", "enable", "disable",
		"reenable", "daemon-reload", "daemon-reexec", "edit", "mask", "unmask", "set-property", "revert",
		"link", "preset", "kill", "reset-failed", "isolate", "set-environment", "unset-environment",
		"import-environment", "try-reload-or-restart", "preset-all", "add-wants", "add-requires")
	SystemctlNoUnit = set("daemon-reload", "daemon-reexec")
	Copiers         = set("cat", "dd", "cp", "install", "rsync", "tee", "ln", "mv", "base64", "xxd", "head", "tail")
	DataCmds        = set("git", "gh", "grep", "rg", "cat", "less", "echo", "printf", "factory")
	Launchers       = set("env", "sudo", "nohup", "timeout", "command", "exec", "nice", "ionice", "setsid", "stdbuf", "xargs")
	FindExec        = set("-exec", "-execdir", "-ok", "-okdir")
	Awks            = set("awk", "gawk", "mawk", "nawk", "busybox-awk")
	Editors         = set("vim", "vi", "nvim", "view", "ex", "emacs", "nano", "ed", "less", "more", "man")
	Declarers       = set("export", "declare", "typeset", "readonly", "local")
	ScrubVars       = set("FACTORY_ROLE", "HERDR_PANE_ID")
	IdentVars       = []string{"FACTORY_AGENT", "FACTORY_ROLE", "HERDR_PANE_ID"}
	FactoryNames    = []string{"factory", "factory_cli.py", "factory_cli"}
	PersistNames    = []string{".gitconfig", ".bashrc", ".zshrc", ".zshenv", ".profile", ".bash_profile", ".zprofile"}
	CfgWriters      = set("touch", "mkdir", "rmdir", "rm", "unlink", "shred", "truncate", "mv", "ln", "cp", "install", "tee",
		"chmod", "chown", "chgrp", "chattr", "setfacl", "rsync", "dd", "patch", "mkfifo", "mknod")
	CfgRemovers     = set("rm", "rmdir", "unlink", "shred")
	RemoteWrite     = set("add", "set-url", "rename", "remove", "rm", "set-head", "set-branches")
	ConfigValueOpts = set("--type", "--default", "--comment", "--blob")
	GitValueOpts    = set("-C", "-c", "--git-dir", "--work-tree", "--namespace", "--exec-path", "--super-prefix", "--config-env")
	GitReadOnly     = set("status", "log", "diff", "show", "rev-parse", "ls-files", "ls-tree", "cat-file", "blame", "grep",
		"describe", "fetch", "shortlog", "rev-list", "show-ref", "merge-base", "ls-remote",
		"for-each-ref", "diff-tree", "diff-index", "name-rev", "version", "help")
	BranchListLong  = set("--list", "--all", "--remotes")
	BranchPlainLong = set("--show-current", "--verbose", "--ignore-case", "--color", "--no-color", "--column",
		"--no-column", "--abbrev", "--no-abbrev", "--quiet")
	BranchValueLong = set("--contains", "--no-contains", "--merged", "--no-merged", "--points-at", "--sort", "--format")
	ConfigRead      = set("--get", "--get-all", "--get-regexp", "--get-urlmatch", "--list", "-l", "--show-origin",
		"--show-scope", "--name-only")
	ConfigWrite = set("--unset", "--unset-all", "--add", "--replace-all", "--edit", "-e", "--rename-section",
		"--remove-section")
	GhValueOpts = set("-H", "--header", "-q", "--jq", "-t", "--template", "--hostname", "--cache", "-R", "--repo",
		"-f", "--raw-field", "-F", "--field", "--input", "-X", "--method", "-p", "--preview")
	GhMutating  = set("PUT", "PATCH", "POST", "DELETE")
	GhRepoVerbs = set("edit", "rename", "archive", "unarchive", "delete")
	GitCmdKeys  = set("core.pager", "core.editor", "core.sshcommand", "core.askpass", "core.fsmonitor", "sequence.editor",
		"gpg.program", "diff.external", "credential.helper", "pager.log", "pager.diff")
	PyFlags = set("b", "B", "d", "E", "h", "i", "I", "O", "P", "q", "s", "S", "u", "v", "V", "x", "?")

	// BroadReads is worker-guard.py:BROAD_READS: subcommand -> second words that are blocked; a nil set
	// means any (or no) second word.
	BroadReads = map[string]map[string]struct{}{
		"res": nil, "agents": nil, "scorecard": nil, "digest": nil, "config": nil,
		"goal": set("list"), "task": set("list", "ready"), "team": set("list"),
		"project": set("list"), "lesson": set("list"),
	}
	// WrapValueOpts is worker-guard.py:WRAP_VALUE_OPTS: wrapper -> options that take a value as a separate word.
	WrapValueOpts = map[string]map[string]struct{}{
		"env": set("-u", "-C", "--unset", "--chdir"), "exec": set("-a"), "nohup": set(), "command": set(), "builtin": set(),
		"setsid": set(), "timeout": set("-s", "-k", "--signal", "--kill-after"), "nice": set("-n", "--adjustment"),
		"ionice": set("-c", "-n", "-p", "-P", "-u", "--class", "--classdata"), "stdbuf": set("-i", "-o", "-e"),
		"xargs": set("-I", "-J", "-n", "-P", "-d", "-L", "-l", "-s", "-E", "-a", "-R", "-S", "--max-args", "--max-procs",
			"--delimiter", "--max-lines", "--max-chars", "--arg-file", "--eof", "--process-slot-var"),
		"watch": set("-n", "--interval", "-w"), "busybox": set(),
	}
)

type interpSpec struct {
	value, code, longCode map[string]struct{}
}

// interpOpts is worker-guard.py:INTERP_OPTS (python is modelled in code, not here).
var interpOpts = map[string]interpSpec{
	"perl": {value: set("-I"), code: set("e", "E")},
	"ruby": {value: set("-r", "-I", "-C", "-E", "-F"), code: set("e")},
	"lua":  {value: set("-l"), code: set("e")},
	"php":  {value: set("-d", "-c", "-z", "-f", "-F"), code: set("r", "R", "B", "E")},
	"node": {value: set("-r", "--require", "--import", "--loader", "--experimental-loader", "--input-type", "--stack-size"),
		code: set("e", "p"), longCode: set("--eval", "--print")},
}

// ───────────────────────────── small helpers ─────────────────────────────

// BraceExpand is a port of worker-guard.py:brace_expand. Like the original it grows
// exponentially with the number of brace groups.
func BraceExpand(t string) []string {
	start, end := -1, -1
	for i := 0; i < len(t) && start < 0; i++ {
		if t[i] != '{' {
			continue
		}
		j := i + 1
		for j < len(t) && t[j] != '{' && t[j] != '}' {
			j++
		}
		if j < len(t) && t[j] == '}' && strings.Contains(t[i+1:j], ",") {
			start, end = i, j+1
		}
	}
	if start < 0 {
		return []string{t}
	}
	var out []string
	for _, alt := range strings.Split(t[start+1:end-1], ",") {
		out = append(out, BraceExpand(t[:start]+alt+t[end:])...)
	}
	return out
}

// IsPunct is a port of worker-guard.py:is_punct.
func IsPunct(tok string) bool {
	return tok != "" && strings.Trim(tok, "();<>|&") == ""
}

// IsStub is a port of worker-guard.py:is_stub.
func IsStub(path string) bool {
	return strings.Contains("/"+pyNormpath(path), "/tests/stubs/")
}

// pyNormpath is os.path.normpath for the shapes IsStub sees (the leading "//" quirk is irrelevant
// there because the caller prefixes a slash and looks for an interior substring).
func pyNormpath(p string) string {
	if p == "" {
		return "."
	}
	rooted := strings.HasPrefix(p, "/")
	var parts []string
	for _, c := range strings.Split(p, "/") {
		switch {
		case c == "" || c == ".":
		case c == "..":
			if len(parts) > 0 && parts[len(parts)-1] != ".." {
				parts = parts[:len(parts)-1]
			} else if !rooted {
				parts = append(parts, "..")
			}
		default:
			parts = append(parts, c)
		}
	}
	out := strings.Join(parts, "/")
	if rooted {
		out = "/" + out
	}
	if out == "" {
		return "."
	}
	return out
}

// IsHerdr is a port of worker-guard.py:is_herdr.
func IsHerdr(word string) bool {
	base := pyBasename(strings.TrimRight(word, "/"))
	if base == "herdr" {
		return !IsStub(word)
	}
	if base != "[" && base != "[[" && strings.ContainsAny(base, "*?[") {
		return fnmatchCase("herdr", base)
	}
	return false
}

// IsClaude is a port of worker-guard.py:is_claude.
func IsClaude(word string) bool { return pyBasename(strings.TrimRight(word, "/")) == "claude" }

// Under is a port of worker-guard.py:under.
func Under(path, root string) bool {
	return path == root || strings.HasPrefix(path, strings.TrimRight(root, "/")+"/")
}

// IsAssign is worker-guard.py:ASSIGN.match(s): ^[A-Za-z_][A-Za-z0-9_]*\+?=
func IsAssign(s string) bool {
	i := 0
	for i < len(s) && (s[i] == '_' || s[i] >= 'A' && s[i] <= 'Z' || s[i] >= 'a' && s[i] <= 'z' || i > 0 && s[i] >= '0' && s[i] <= '9') {
		i++
	}
	if i == 0 {
		return false
	}
	if i < len(s) && s[i] == '+' {
		i++
	}
	return i < len(s) && s[i] == '='
}

// FoldMktemp is a port of worker-guard.py:fold_mktemp.
func FoldMktemp(toks []string) []string {
	var out []string
	for i := 0; i < len(toks); {
		t := toks[i]
		if IsAssign(t) && strings.HasSuffix(t, "=$") && i+1 < len(toks) && toks[i+1] == "(" {
			j := i + 2
			for j < len(toks) && !IsPunct(toks[j]) {
				j++
			}
			inner := toks[i+2 : j]
			if j < len(toks) && toks[j] == ")" && len(inner) > 0 && inner[0] == "mktemp" {
				ok := true
				for _, x := range inner[1:] {
					if strings.HasPrefix(x, "-") && x != "-d" && x != "-p" {
						ok = false
					}
				}
				if ok {
					out = append(out, t+"("+strings.Join(inner, " ")+")")
					i = j + 1
					continue
				}
			}
		}
		out = append(out, t)
		i++
	}
	return out
}

// IsDynamic is a port of worker-guard.py:is_dynamic.
func IsDynamic(word string, strict bool) bool {
	base := pyBasename(strings.TrimRight(word, "/"))
	if strings.Contains(word, "`") {
		return true
	}
	if strict {
		return strings.ContainsAny(word, "${")
	}
	return strings.HasPrefix(base, "$")
}

// StripPrefix is a port of worker-guard.py:strip_prefix.
func StripPrefix(words []string) []string {
	words = append([]string(nil), words...)
	for len(words) > 0 && (in(Reserved, words[0]) || IsAssign(words[0])) {
		kw := words[0]
		words = words[1:]
		if kw == "time" {
			for len(words) > 0 && strings.HasPrefix(words[0], "-") {
				words = words[1:]
			}
		} else if kw == "function" && len(words) > 0 {
			words = words[1:]
		}
	}
	return words
}

// InterpArgs is a port of worker-guard.py:interp_args. script and code are indexes into rest, -1 for None.
func InterpArgs(name string, rest []string) (script, code int, unknown bool) {
	name = trimDigitsDots(name)
	_, known := interpOpts[name]
	for i := 0; i < len(rest); {
		a := rest[i]
		if a == "--" {
			if i+1 < len(rest) {
				return i + 1, -1, false
			}
			return -1, -1, false
		}
		if !known && name != "python" {
			return -1, -1, true
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			return i, -1, false
		}
		if name == "python" {
			if strings.HasPrefix(a, "--") {
				switch a {
				case "--version", "--help", "--help-env", "--help-xoptions", "--help-all":
					i++
					continue
				}
				if strings.HasPrefix(a, "--check-hash-based-pycs") {
					if strings.Contains(a, "=") {
						i++
					} else {
						i += 2
					}
					continue
				}
				return -1, -1, true
			}
			for j := 1; j < len(a); j++ {
				ch := a[j]
				if ch == 'c' {
					if j+1 < len(a) {
						return -1, i, false
					}
					if i+1 < len(rest) {
						return -1, i + 1, false
					}
					return -1, -1, false
				}
				if ch == 'm' {
					return -1, -1, false
				}
				if ch == 'W' || ch == 'X' {
					if j+1 >= len(a) {
						i++
					}
					break
				}
				if !in(PyFlags, string(ch)) {
					return -1, -1, true
				}
			}
			i++
			continue
		}
		spec := interpOpts[name]
		runes := []rune(a)
		isCode := in(spec.longCode, a)
		if !isCode && !strings.HasPrefix(a, "--") && len(runes) > 1 && in(spec.code, string(runes[len(runes)-1])) {
			isCode = len(runes) == 2 || isAlpha(string(runes[1:len(runes)-1]))
		}
		if isCode {
			if i+1 < len(rest) {
				return -1, i + 1, false
			}
			return -1, -1, false
		}
		if in(spec.value, a) {
			i += 2
		} else {
			i++
		}
	}
	return -1, -1, false
}

// isAlpha is str.isalpha(): non-empty and every character a letter.
func isAlpha(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return true
}

// trimDigitsDots is re.sub(r"[\d.]+$", "", name): a trailing run of digits and dots, also before a final newline.
func trimDigitsDots(name string) string {
	end := len(name)
	if strings.HasSuffix(name, "\n") {
		trimmed := trimRunDots(name[:end-1])
		if len(trimmed) < end-1 {
			return trimmed + "\n"
		}
	}
	return trimRunDots(name)
}

func trimRunDots(s string) string {
	i := len(s)
	for i > 0 {
		r, size := utf8.DecodeLastRuneInString(s[:i])
		if r != '.' && !isDecimal(r) {
			break
		}
		i -= size
	}
	return s[:i]
}

// ───────────────────────────── preprocessing ─────────────────────────────

// Heredoc is one entry of the bodies list of worker-guard.py:preprocess: the command line up to the
// heredoc's newline and the body.
type Heredoc struct{ Head, Body string }

// Extra is one (kind, body) pair of worker-guard.py:preprocess's extras.
type Extra struct{ Kind, Body string }

// HeredocDelim is a port of worker-guard.py:heredoc_delim. ok is false where Python returns None.
func HeredocDelim(cmd string, i int) (strip bool, delim string, end int, quoted bool, ok bool) {
	n := len(cmd)
	j := i + 2
	strip = strings.HasPrefix(cmd[j:], "-")
	if strip {
		j++
	}
	for j < n && (cmd[j] == ' ' || cmd[j] == '\t') {
		j++
	}
	var word []byte
	start := j
	for j < n && !strings.ContainsRune(HeredocWordEnd, rune(cmd[j])) {
		c := cmd[j]
		switch {
		case c == '\'':
			k := strings.IndexByte(cmd[j+1:], '\'')
			if k < 0 {
				return
			}
			word = append(word, cmd[j+1:j+1+k]...)
			quoted = true
			j = j + 1 + k + 1
		case c == '"':
			quoted = true
			j++
			for j < n && cmd[j] != '"' {
				switch {
				case cmd[j] == '\\' && j+1 < n && strings.IndexByte("$`\"\\", cmd[j+1]) >= 0:
					j++
				case cmd[j] == '\\' && j+1 < n && cmd[j+1] == '\n':
					return
				case cmd[j] == '`' || (cmd[j] == '$' && j+1 < n && strings.IndexByte("({[", cmd[j+1]) >= 0):
					return
				}
				word = append(word, cmd[j])
				j++
			}
			if j >= n {
				return
			}
			j++
		case c == '\\':
			if j+1 >= n || cmd[j+1] == '\n' {
				return
			}
			word = append(word, cmd[j+1])
			quoted = true
			j += 2
		case c == '$' && j+1 < n && (cmd[j+1] == '\'' || cmd[j+1] == '"'):
			return
		case c == '`' || (c == '$' && j+1 < n && strings.IndexByte("({[", cmd[j+1]) >= 0):
			return
		default:
			word = append(word, c)
			j++
		}
	}
	if j == start || len(word) == 0 || strings.Contains(string(word), "\n") {
		return
	}
	return strip, string(word), j, quoted, true
}

// Preprocess is a port of worker-guard.py:preprocess: make newlines separators, drop comments and non-shell
// heredoc bodies. When bodies is non-nil every heredoc is appended to it. An unbalanced quote is a *Block error.
func Preprocess(cmd string, foldTicks bool, bodies *[]Heredoc) (string, []Extra, error) {
	var out []string
	var extras []Extra
	i, n := 0, len(cmd)
	var q byte
	type pend struct {
		strip  bool
		delim  string
		quoted bool
	}
	var pending []pend
	lineStart := 0
	lastEndsWith := func(set string) bool {
		if len(out) == 0 {
			return true
		}
		l := out[len(out)-1]
		return l == "" || strings.IndexByte(set, l[len(l)-1]) >= 0
	}
	for i < n {
		c := cmd[i]
		if q == '\'' {
			out = append(out, cmd[i:i+1])
			if c == '\'' {
				q = 0
			}
			i++
			continue
		}
		if q == '"' {
			if c == '\\' && i+1 < n {
				out = append(out, cmd[i:i+2])
				i += 2
				continue
			}
			out = append(out, cmd[i:i+1])
			if c == '"' {
				q = 0
			}
			i++
			continue
		}
		if c == '\\' && i+1 < n {
			if cmd[i+1] == '\n' {
				i += 2
				continue
			}
			out = append(out, cmd[i:i+2])
			i += 2
			continue
		}
		if c == '\'' || c == '"' {
			q = c
			out = append(out, cmd[i:i+1])
			i++
			continue
		}
		if c == '`' && foldTicks {
			j := i + 1
			for j < n && cmd[j] != '`' {
				if cmd[j] == '\\' {
					j += 2
				} else {
					j++
				}
			}
			if j < n {
				out = append(out, "$("+strings.ReplaceAll(cmd[i+1:j], "\\`", "`")+")")
				i = j + 1
				continue
			}
		}
		if c == '#' && (len(out) == 0 || lastEndsWith(" \t;&|()")) {
			for i < n && cmd[i] != '\n' {
				i++
			}
			continue
		}
		if c == '<' && strings.HasPrefix(cmd[i:], "<<") && !strings.HasPrefix(cmd[i:], "<<<") {
			if strip, delim, end, quoted, ok := HeredocDelim(cmd, i); ok {
				pending = append(pending, pend{strip, delim, quoted})
				out = append(out, cmd[i:end])
				i = end
			} else {
				out = append(out, "<<")
				i += 2
			}
			continue
		}
		if c == '\n' {
			head := strings.Join(out[lineStart:], "")
			out = append(out, " ; ")
			i++
			for _, p := range pending {
				var body []string
				for i < n {
					seg := i
					j := strings.IndexByte(cmd[i:], '\n')
					var line string
					if j < 0 {
						line, i = cmd[i:], n
					} else {
						line, i = cmd[i:i+j], i+j+1
					}
					cmp := line
					for !p.quoted && i < n && (len(cmp)-len(strings.TrimRight(cmp, "\\")))&1 == 1 {
						// unquoted body: bash joins backslash-newline before comparing with the terminator
						j = strings.IndexByte(cmd[i:], '\n')
						var nxt string
						if j < 0 {
							nxt, i = cmd[i:], n
						} else {
							nxt, i = cmd[i:i+j], i+j+1
						}
						cmp = cmp[:len(cmp)-1] + nxt
					}
					t := cmp
					if p.strip {
						t = strings.TrimLeft(cmp, "\t")
					}
					if t == p.delim {
						break
					}
					body = append(body, strings.TrimRight(cmd[seg:i], "\n")) // every raw line consumed
					if cmp != line {
						body = append(body, cmp) // what bash feeds the inner command after joining
					}
				}
				b := strings.Join(body, "\n")
				if bodies != nil {
					*bodies = append(*bodies, Heredoc{Head: head, Body: b})
				}
				extras = classifyHeredoc(extras, head, b)
			}
			pending = nil
			lineStart = len(out)
			continue
		}
		out = append(out, cmd[i:i+1])
		i++
	}
	if q != 0 {
		return "", nil, blockf("unbalanced quote")
	}
	return strings.Join(out, ""), extras, nil
}

// classifyHeredoc is the tail of the heredoc loop in worker-guard.py:preprocess: which extras a body becomes.
func classifyHeredoc(extras []Extra, head, body string) []Extra {
	if hasWord(head, Shells, "eval", "source") {
		return append(extras, Extra{"sh", body})
	}
	runs := wordDotRuns(head)
	if interpreterFull(strings.Join(runs, " ")) || hasInterpreterWord(head) {
		extras = append(extras, Extra{"code", body})
	}
	// re.split(r"[;&|(]", head)[-1]
	words := pyFields(head[strings.LastIndexAny(head, ";&|(")+1:])
	if len(words) > 6 {
		words = words[:6]
	}
	for _, wd := range words {
		b := pyBasename(wd)
		if interpreterFull(b) {
			return append(extras, Extra{"icode", body})
		}
		if in(DataCmds, b) || b == "tee" || b == "sed" || b == "awk" || b == "wc" {
			break
		}
	}
	return extras
}

// ───────────────────────────── $( ) and backtick extraction ─────────────────────────────

// ExtractSubs is a port of worker-guard.py:extract_subs.
func ExtractSubs(text string) ([]string, error) {
	var subs []string
	i, n := 0, len(text)
	var q byte
	for i < n {
		c := text[i]
		switch {
		case q == '\'':
			if c == '\'' {
				q = 0
			}
			i++
		case c == '\\':
			i += 2
		case c == '\'' && q == 0:
			q = '\''
			i++
		case c == '"':
			if q == '"' {
				q = 0
			} else {
				q = '"'
			}
			i++
		case strings.HasPrefix(text[i:], "$(") && q == '"':
			j, depth := i+2, 1
			for j < n && depth != 0 {
				if text[j] == '(' {
					depth++
				}
				if text[j] == ')' {
					depth--
				}
				j++
			}
			if depth != 0 {
				return nil, blockf("unbalanced $( )")
			}
			subs = append(subs, text[i+2:j-1])
			i = j
		case c == '`':
			j := i + 1
			for j < n && text[j] != '`' {
				if text[j] == '\\' {
					j += 2
				} else {
					j++
				}
			}
			if j >= n {
				return nil, blockf("unbalanced backtick")
			}
			subs = append(subs, strings.ReplaceAll(text[i+1:j], "\\`", "`"))
			i = j + 1
		default:
			i++
		}
	}
	return subs, nil
}

// ───────────────────────────── quote masking and segments ─────────────────────────────

// MaskQuotes is a port of worker-guard.py:mask_quotes: a same-length (in characters) copy of text with
// quoted spans blanked to 'Q' (quotes kept).
func MaskQuotes(text string) string {
	r := []rune(text)
	out := make([]rune, 0, len(r))
	var q rune
	for i := 0; i < len(r); {
		c := r[i]
		if q != 0 {
			if c == '\\' && q == '"' && i+1 < len(r) {
				out = append(out, 'Q', 'Q')
				i += 2
				continue
			}
			if c == q {
				q = 0
				out = append(out, c)
			} else if c != '\n' {
				out = append(out, 'Q')
			} else {
				out = append(out, '\n')
			}
		} else if c == '\\' && i+1 < len(r) {
			out = append(out, r[i], r[i+1])
			i += 2
			continue
		} else {
			if c == '\'' || c == '"' {
				q = c
			}
			out = append(out, c)
		}
		i++
	}
	return string(out)
}

// Segment is one (start, end, words) triple of worker-guard.py:segments. Start and End are character
// (code point) offsets, as in Python; use RuneSlice to cut the original text.
type Segment struct {
	Start, End int
	Words      []string
}

// RuneSlice is text[start:end] with Python (code point) offsets.
func RuneSlice(text string, start, end int) string { return string([]rune(text)[start:end]) }

// Segments is a port of worker-guard.py:segments (SEP = [;&|(){}`\n]+|\$\( ).
func Segments(text string) []Segment {
	m := []rune(MaskQuotes(text))
	pos := 0
	var res []Segment
	i := 0
	for i < len(m) {
		start, stop := i, i
		switch {
		case strings.ContainsRune(";&|(){}`\n", m[i]):
			for stop < len(m) && strings.ContainsRune(";&|(){}`\n", m[stop]) {
				stop++
			}
		case m[i] == '$' && i+1 < len(m) && m[i+1] == '(':
			stop = i + 2
		default:
			i++
			continue
		}
		res = append(res, Segment{pos, start, pyFields(string(m[pos:start]))})
		pos, i = stop, stop
	}
	return append(res, Segment{pos, len(m), pyFields(string(m[pos:]))})
}

// LauncherInCommandPosition is a port of worker-guard.py:launcher_in_command_position.
func LauncherInCommandPosition(line string) bool {
	for _, s := range Segments(line) {
		words := s.Words
		for len(words) > 0 && (in(Reserved, words[0]) || IsAssign(words[0])) {
			words = words[1:]
		}
		if len(words) == 0 {
			continue
		}
		name := pyBasename(words[0])
		if in(Launchers, name) {
			return true
		}
		if name == "find" {
			for _, w := range words {
				if in(FindExec, w) {
					return true
				}
			}
		}
		if in(Shells, name) {
			for _, w := range words[1:] {
				if isDashC(w) {
					return true
				}
			}
		}
	}
	return false
}

// isDashC is re.match(r"^-[A-Za-z]*c[A-Za-z]*$", w) on a word without whitespace.
func isDashC(w string) bool {
	if len(w) < 2 || w[0] != '-' {
		return false
	}
	c := false
	for i := 1; i < len(w); i++ {
		ch := w[i]
		if ch == 'c' {
			c = true
		} else if !(ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z') {
			return false
		}
	}
	return c
}

// ScriptTokens is the tokenising step of worker-guard.py:Analyzer.script: digits before a redirect are
// blanked, the text is lexed in the hook's shlex configuration (go/internal/shlex) and `NAME=$(mktemp ..)` is folded.
// An error is the Block("cannot parse command (...)") of the original.
func ScriptTokens(text string) ([]string, error) {
	toks, err := shlex.Script(BlankRedirectFds(text))
	if err != nil {
		return nil, blockf("cannot parse command (" + err.Error() + ")")
	}
	return FoldMktemp(toks), nil
}

// IsPersistEditTool reports whether tool is one of worker-guard.py:PERSIST_EDIT_TOOLS.
func IsPersistEditTool(tool string) bool { return in(PersistEditTools, tool) }

// IsTermsRole reports whether role is in worker-guard.py:TERMS_ROLES.
func IsTermsRole(role string) bool { return in(TermsRoles, role) }
