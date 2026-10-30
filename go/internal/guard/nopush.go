package guard

import (
	"regexp"
	"strings"
)

// T-0755 (worker-guard.py): NO_PUSH_ROLES run only read-only git, fail closed.

// noPushRoles is worker-guard.py:NO_PUSH_ROLES.
var noPushRoles = set("qa", "reviewer", "pm", "staff-engineer", "scout", "evaluator")

var (
	gitOptsAllowed = set("-P", "--no-pager", "--no-optional-locks", "--version", "--help")
	gitAllowPlain  = func() map[string]struct{} {
		m := set("merge-tree")
		for k := range GitReadOnly {
			if k != "fetch" && k != "ls-remote" {
				m[k] = struct{}{}
			}
		}
		return m
	}()
	gitForbidLong     = []string{"--ext-diff", "--output"}
	gitConfigReadOnly = set("--get", "--get-all", "--get-regexp", "--list", "-l")
	gitFetchFlags     = set("--all", "--prune", "-p", "--tags", "-t", "--no-tags", "-q", "--quiet", "-v", "--verbose", "-n",
		"--dry-run", "-f", "--force", "--no-recurse-submodules", "--prune-tags", "--unshallow")
	gitLsRemoteFlags = set("--heads", "--refs", "--symref", "--exit-code", "--get-url")

	// Python's \s / \S are unicode-aware; RE2's are ASCII, so the class is spelled out (str.isspace).
	gitSp       = `\t-\r\x1c-\x1f \x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}`
	reGitWord   = regexp.MustCompile(`(?:^|[` + gitSp + `/;&|])git(?:-[^` + gitSp + `]*)?(?:[` + gitSp + `]|$)`)
	reGitConfig = regexp.MustCompile(`^(GIT_CONFIG_COUNT|GIT_CONFIG_KEY_.*|GIT_CONFIG_VALUE_.*|GIT_CONFIG_PARAMETERS)\+?=`)
	reGitSens   = regexp.MustCompile(`^(HOME|XDG_CONFIG_HOME|PATH|PAGER|GIT_[\p{L}\p{N}_]*)(\+?=.*)?\n?$`)
	reGitDepth  = regexp.MustCompile(`^--(depth|deepen)=[0-9]+$`)
	reGitRemote = regexp.MustCompile(`^[\p{L}\p{N}_.-]+@[\p{L}\p{N}_.-]+:`)
	reGrepO     = regexp.MustCompile(`^-[^-]*O`)
)

func (a *Analyzer) noPush() bool { return in(noPushRoles, a.Role) && !a.HQ }

// gitHooksPath is a port of worker-guard.py:git_hooks_path: p is <x>/.git/hooks, <x>/.git/info or anything under them,
// outside the worker's scratchpad.
func gitHooksPath(p string) bool {
	for _, c := range uniq([]string{pyNormpath(p), pyRealpath(p)}) {
		if inScratchpad(c) {
			continue
		}
		parts := strings.Split(c, "/")
		for i := 0; i+1 < len(parts); i++ {
			if parts[i] == ".git" && (parts[i+1] == "hooks" || parts[i+1] == "info") {
				return true
			}
		}
	}
	return false
}

// gitDeny is a port of Analyzer.git_deny.
func (a *Analyzer) gitDeny(why string) error {
	return blockf(a.Role + " may not run git push, or any git form outside the read-only allow-list (" + why +
		"): read-only roles only inspect (status, log, diff, show, fetch ...); report results with factory done / " +
		"factory msg, or ask your lead")
}

// gitAbbrev is a port of Analyzer.git_abbrev: the first option in args that is, or git would accept as an
// abbreviation of, one of longs; stops at `--`. "" where Python returns None.
func gitAbbrev(args, longs []string) string {
	for _, a := range args {
		if a == "--" {
			return ""
		}
		if strings.HasPrefix(a, "--") && len(a) > 2 {
			head := a
			if k := strings.IndexByte(a, '='); k >= 0 {
				head = a[:k]
			}
			for _, x := range longs {
				if strings.HasPrefix(x, head) {
					return a
				}
			}
		}
	}
	return ""
}

// gitLaunch is a port of Analyzer.git_launch: NO_PUSH_ROLES start git only as the literal `git` (or the real
// /usr/bin/git) with no env assignment before it; git-* helpers and lookalike paths are refused; xargs and
// find -exec never start git.
func (a *Analyzer) gitLaunch(assigns, words []string) error {
	if len(words) == 0 {
		return nil
	}
	raw := words[0]
	base := pyBasename(strings.NewReplacer(`\`, "", `'`, "", `"`, "").Replace(raw))
	switch {
	case base == "git" || strings.HasPrefix(base, "git-"):
		if raw != "git" && !(strings.HasPrefix(raw, "/") && pyRealpath(raw) == pyRealpath("/usr/bin/git")) {
			return a.gitDeny(pyRepr(raw) + " is not the literal `git`: no git-* helpers, relative or lookalike paths")
		}
		if len(assigns) > 0 {
			return a.gitDeny("an environment assignment before git can point it at another config")
		}
	case base == "xargs" || base == "find":
		rest := words[1:]
		hit := anyOf(rest, reGitWord.MatchString)
		if base == "find" {
			hit = hit && anyOf(rest, func(x string) bool { return in(FindExec, x) })
		}
		if hit {
			return a.gitDeny(base + " would start git with arguments this hook cannot see")
		}
	}
	return nil
}

// gitAllow is a port of Analyzer.git_allow: only read-only git, matched against an allow-list.
func (a *Analyzer) gitAllow(rest []string) error {
	i := 0
	for i < len(rest) && strings.HasPrefix(rest[i], "-") {
		o := rest[i]
		if IsDynamic(o, true) {
			return a.gitDeny("dynamic word " + pyRepr(o) + " before the subcommand")
		}
		switch {
		case o == "-C" && i+1 < len(rest) && !IsDynamic(rest[i+1], true):
			i += 2
		case in(gitOptsAllowed, o):
			i++
		default:
			return a.gitDeny("global option " + pyRepr(o) + " could change config, aliases or the repository")
		}
	}
	if i >= len(rest) {
		return nil
	}
	sub, args := rest[i], rest[i+1:]
	if IsDynamic(sub, true) || strings.ContainsAny(sub, "*?[") || strings.Contains(sub, "{") {
		return a.gitDeny("the subcommand word " + pyRepr(sub) + " is dynamic, so it could be push")
	}
	for _, x := range args {
		if IsDynamic(x, true) {
			return a.gitDeny("argument " + pyRepr(x) + " is built from an expansion and could be an option (--output, --ext-diff ...)")
		}
	}
	switch {
	case sub == "fetch" || sub == "ls-remote":
		return a.gitFetchArgs(sub, args)
	case in(gitAllowPlain, sub):
		if h := gitAbbrev(args, gitForbidLong); h != "" {
			return a.gitDeny(pyRepr(h) + " runs a program or writes a file")
		}
		if sub == "grep" {
			if gitAbbrev(args, []string{"--open-files-in-pager"}) != "" || anyOf(args, reGrepO.MatchString) {
				return a.gitDeny("git grep -O runs a program")
			}
		}
		return nil
	case sub == "branch" && !branchMutates(args):
		return nil
	case sub == "remote" && gitRemoteRead(args):
		return nil
	case sub == "worktree" && len(args) > 0 && args[0] == "list":
		return nil
	case sub == "config" && gitConfigRead(args):
		return nil
	}
	return a.gitDeny("`git " + sub + "` is not on the read-only list (aliases, push, send-pack, subtree, rebase --exec, " +
		"bisect run, submodule foreach, archive --remote ... are refused)")
}

// gitRemoteRead is a port of Analyzer.git_remote_read: read-only `git remote` forms; every word must match.
func gitRemoteRead(args []string) bool {
	name := func(a []string) bool { return len(a) == 1 && a[0] != "" && !strings.HasPrefix(a[0], "-") }
	first := func(a []string, ws ...string) bool {
		if len(a) == 0 {
			return false
		}
		for _, w := range ws {
			if a[0] == w {
				return true
			}
		}
		return false
	}
	if len(args) == 0 || (len(args) == 1 && (args[0] == "-v" || args[0] == "--verbose")) {
		return true
	}
	if args[0] == "get-url" {
		r := args[1:]
		if first(r, "--push", "--all") {
			r = r[1:]
		}
		return name(r)
	}
	if first(args, "-v", "--verbose") {
		args = args[1:]
	}
	if len(args) > 0 && args[0] == "show" {
		r := args[1:]
		if first(r, "-n") {
			r = r[1:]
		}
		return name(r)
	}
	return false
}

// gitConfigRead is a port of Analyzer.git_config_read.
func gitConfigRead(args []string) bool {
	if anyOf(args, func(x string) bool { return in(ConfigWrite, x) }) || !anyOf(args, func(x string) bool { return in(gitConfigReadOnly, x) }) {
		return false
	}
	return !configMutates(args)
}

// gitFetchArgs is a port of Analyzer.git_fetch_args.
func (a *Analyzer) gitFetchArgs(sub string, args []string) error {
	ended := false
	for _, x := range args {
		switch {
		case IsDynamic(x, true):
			return a.gitDeny("git " + sub + " argument " + pyRepr(x) + " is dynamic")
		case !ended && x == "--":
			ended = true
		case !ended && strings.HasPrefix(x, "-"):
			if !in(gitFetchFlags, x) && !(sub == "ls-remote" && in(gitLsRemoteFlags, x)) && !fullDigits(reGitDepth, x) {
				return a.gitDeny("git " + sub + " option " + pyRepr(x) + " can run a program or name another remote")
			}
		case strings.Contains(x, "::") || strings.Contains(x, "://") || strings.HasPrefix(x, "/") || strings.HasPrefix(x, ".") ||
			strings.HasPrefix(x, "~") || reGitRemote.MatchString(x):
			return a.gitDeny("git " + sub + " argument " + pyRepr(x) + " is a URL, helper or path, not origin or a refspec")
		case strings.Contains(x, ":") && !strings.HasPrefix(x[strings.IndexByte(x, ':')+1:], "refs/remotes/"):
			return a.gitDeny("git " + sub + " refspec " + pyRepr(x) + " would write a local ref other than refs/remotes/*")
		}
	}
	return nil
}

// fullDigits matches re.fullmatch(r"--(depth|deepen)=\d+") with the unicode \d of a str pattern (the ASCII regexp
// is the fast path; any other decimal digit is accepted as Python does).
func fullDigits(re *regexp.Regexp, x string) bool {
	if re.MatchString(x) {
		return true
	}
	k := strings.IndexByte(x, '=')
	if k < 0 || (x[:k] != "--depth" && x[:k] != "--deepen") || k == len(x)-1 {
		return false
	}
	for _, r := range x[k+1:] {
		if !isDecimal(r) {
			return false
		}
	}
	return true
}

// gitConfigEnv is a port of Analyzer.git_config_env: NO_PUSH_ROLES never set GIT_CONFIG_*, HOME, XDG_CONFIG_HOME,
// PATH, PAGER or any GIT_* variable, by assignment, export, declare or unset.
func (a *Analyzer) gitConfigEnv(assigns, words []string) error {
	names := append([]string(nil), assigns...)
	if len(words) > 0 {
		if b := pyBasename(words[0]); in(Declarers, b) || b == "unset" {
			names = append(names, words[1:]...)
		}
	}
	if anyOf(names, reGitConfig.MatchString) {
		return blockf(a.Role + " may not set GIT_CONFIG_COUNT/GIT_CONFIG_KEY_*/GIT_CONFIG_VALUE_*/GIT_CONFIG_PARAMETERS: " +
			"env-injected git config can alias a push; ask your lead")
	}
	if anyOf(names, reGitSens.MatchString) {
		return blockf(a.Role + " may not set, export or unset HOME, XDG_CONFIG_HOME, PATH, PAGER or GIT_*: they select " +
			"the git config, helpers and pager (an alias there hides a push); ask your lead")
	}
	return nil
}
