package guard

import (
	"regexp"
	"strings"
)

// T-0206: port of worker-guard.py GIT_TRANSPORT_CMDS, GIT_DIFF_CMDS, GIT_EXPANSION and Analyzer.git_expansion.
var gitExpansionCmds = set("fetch", "pull", "push", "clone", "ls-remote", "remote", "send-pack", "fetch-pack", "submodule",
	"diff", "show", "log", "whatchanged", "diff-tree", "diff-index", "diff-files", "format-patch", "difftool",
	"range-diff", "shortlog", "blame")

// `$SUB` is the marker the parser leaves where it split a command substitution out (Python: \$SUB\b).
var gitExpansionRe = regexp.MustCompile("\\$SUB(?:\\z|[^\\p{L}\\p{N}_])|\\$\\(|`|\\$\\{|\\$'|\\{[^{}]*,[^{}]*\\}")

var gitExpansionValueOpts = set("-C", "-c", "--git-dir", "--work-tree", "--namespace", "--exec-path")

// gitExpansion refuses, in worker mode for push-capable roles, a git subcommand token (and every token of a transport or
// diff command) that holds a shell expansion: it could become an option (--upload-pack, --output) at run time.
func (a *Analyzer) gitExpansion(rest []string) error {
	i := 0
	for i < len(rest) && strings.HasPrefix(rest[i], "-") && !gitExpansionRe.MatchString(rest[i]) {
		if in(gitExpansionValueOpts, rest[i]) {
			i += 2
		} else {
			i++
		}
	}
	if i >= len(rest) {
		return nil
	}
	sub, args := rest[i], rest[i+1:]
	listed := in(gitExpansionCmds, sub)
	if listed && strings.Contains(a.text, "$'") {
		return blockf("git " + sub + ": an ANSI-C quote ($'...') in the command could become an option at run time; " +
			"write the option and its value literally")
	}
	toks := []string{sub}
	if listed {
		toks = append(toks, args...)
	}
	for _, tok := range toks {
		if gitExpansionRe.MatchString(tok) {
			name := "<subcommand>"
			if listed {
				name = sub
			}
			return blockf("git " + name + ": token " + pyRepr(tok) + " contains a shell expansion " +
				"(command substitution, backtick, ${...}, $'...' or {a,b}) that could become an option at run " +
				"time (--upload-pack, --output, --ext-diff); write the option and its value literally")
		}
	}
	return nil
}
