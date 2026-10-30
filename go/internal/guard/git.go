package guard

import (
	"os"
	"os/user"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	"factory/go/internal/shlex"
)

// Section 3 of the port: the git rules (shared-checkout protection, mutating subcommands, git config/persist
// keys, branch deletion, the cleaner exceptions). What the config/persist file rules (persist_hit,
// repo_cfg_refuse), git_push_pin and the public-text rule own stays a fail-closed stub for T6d.

// SessionID is worker-guard.py:SESSION_ID, the hook payload's session_id; "" is Python's None. main sets it
// before Check, as Python's main does.
var SessionID string

// SharedAdvice is worker-guard.py:SHARED_ADVICE.
const sharedAdvice = "workers never change the shared checkout ~/factory (branch main); work in your own worktree " +
	"(your cwd under ~/factory/worktrees/)"

// GitBlock is worker-guard.py:GitBlock, a Block (same stderr text, swallowed in the same places).
type GitBlock struct{ Msg string }

func (b *GitBlock) Error() string { return b.Msg }

// Unwrap lets errors.As(&Block) see a GitBlock, as isinstance(e, Block) does.
func (b *GitBlock) Unwrap() error { return &Block{Msg: b.Msg} }

func gitBlockf(msg string) error { return &GitBlock{Msg: msg} }

// factoryBranch is worker-guard.py:FACTORY_BRANCH.match(s): ^factory/[A-Za-z0-9._/-]+$ ($ also matches before
// one trailing newline).
func factoryBranch(s string) bool {
	s = trimOneNL(s)
	if !strings.HasPrefix(s, "factory/") || len(s) == len("factory/") {
		return false
	}
	for i := len("factory/"); i < len(s); i++ {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '/' || c == '-') {
			return false
		}
	}
	return true
}

// trimOneNL drops one trailing "\n": the part of a Python `$` that the end of the string does not already cover.
func trimOneNL(s string) string { return strings.TrimSuffix(s, "\n") }

func isASCIILetter(c byte) bool { return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' }

// dashLetters is re.match(r"^-[A-Za-z]*[<chars>][A-Za-z]*$", a): a short-option bundle containing one of chars.
func dashLetters(a, chars string) bool {
	a = trimOneNL(a)
	if !strings.HasPrefix(a, "-") {
		return false
	}
	hit := false
	for i := 1; i < len(a); i++ {
		if !isASCIILetter(a[i]) {
			return false
		}
		if strings.IndexByte(chars, a[i]) >= 0 {
			hit = true
		}
	}
	return hit
}

// liveHomes is a port of worker-guard.py:derive_live_homes for the current uid: <pw_dir>/factory is always live;
// <pw_dir>/.config/factory/live_home can only add one. An error means the passwd entry is unreadable (Python
// would die at import: exit 2).
func liveHomes() ([]string, error) {
	uid := os.Getuid()
	u, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return nil, blockf("cannot read the passwd entry: " + err.Error())
	}
	homes := []string{pyRealpath(pyJoin(u.HomeDir, "factory"))}
	cfgdir := pyJoin(pyJoin(u.HomeDir, ".config"), "factory")
	path := pyJoin(cfgdir, "live_home")
	st, err := os.Lstat(path)
	if err != nil {
		return homes, nil
	}
	pst, err := os.Lstat(cfgdir)
	if err != nil {
		return homes, nil
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || !pst.IsDir() || int(sys.Uid) != uid || st.Mode().Perm()&0o022 != 0 {
		return homes, nil
	}
	b, err := os.ReadFile(path)
	if err != nil || !utf8.Valid(b) {
		return homes, nil
	}
	lines := pyLines(string(b))
	if len(lines) == 1 && strings.HasPrefix(lines[0], "/") && !strings.Contains(lines[0], "\x00") {
		homes = append(homes, pyRealpath(lines[0]))
	}
	return homes, nil
}

// gitHomes is a port of Analyzer.git_homes: the shared checkout(s). WORKER_GUARD_LIVE_HOME replaces them (tests
// only: the hook's own env, which a worker cannot change from inside a command).
func gitHomes() ([]string, error) {
	if o := os.Getenv("WORKER_GUARD_LIVE_HOME"); o != "" {
		return []string{pyRealpath(o)}, nil
	}
	return liveHomes()
}

// inScratchpad is a port of worker-guard.py:in_scratchpad. WORKER_GUARD_SCRATCH (the hook's own env, tests only)
// names the scratchpad instead.
func inScratchpad(path string) bool {
	if o := os.Getenv("WORKER_GUARD_SCRATCH"); o != "" {
		return Under(path, pyRealpath(o))
	}
	sid := SessionID
	if sid == "" || sid == "." || sid == ".." {
		return false
	}
	for _, r := range sid {
		if !isWord(r) && r != '.' && r != '-' {
			return false
		}
	}
	parts := strings.Split(path, "/")
	return len(parts) >= 6 && parts[0] == "" && parts[1] == "tmp" && parts[2] == "claude-"+strconv.Itoa(os.Getuid()) &&
		parts[4] == sid && parts[5] == "scratchpad" && parts[3] != "" && parts[3] != "." && parts[3] != ".."
}

// dynamicDir is a port of Analyzer.dynamic_dir.
func dynamicDir(path string) bool { return strings.ContainsAny(path, "$`*?[{") }

// gitCd is a port of Analyzer.git_cd: track the directory for the shared-checkout git rule.
func (a *Analyzer) gitCd(name string, rest []string) {
	if name == "popd" {
		a.gdyn = true
		return
	}
	var args []string
	for _, x := range rest {
		if !strings.HasPrefix(x, "-") || x == "-" {
			args = append(args, x)
		}
	}
	if len(args) == 0 {
		a.gcwd, a.gdyn = expandUser("~"), false
		return
	}
	a.gcwd, a.gdyn = gitJoin(a.gcwd, a.gdyn, args[0])
}

// gitJoin is a port of Analyzer.git_join.
func gitJoin(cwd string, dyn bool, path string) (string, bool) {
	if path == "-" || dynamicDir(path) {
		return cwd, true
	}
	return pyJoin(cwd, expandUser(path)), false
}

// gitTopShared is a port of Analyzer.git_top_shared: walk up from realpath(path) to the first ancestor holding
// .git: a FILE is a linked worktree (not shared), a DIRECTORY is a main checkout (shared when it is the live
// home). No git is run.
func gitTopShared(path string) (bool, error) {
	homes, err := gitHomes()
	if err != nil {
		return false, err
	}
	p := pyRealpath(path)
	for {
		g := pyJoin(p, ".git")
		if fi, err := os.Stat(g); err == nil {
			if fi.IsDir() {
				for _, h := range homes {
					if p == h {
						return true, nil
					}
				}
				return false, nil
			}
			if fi.Mode().IsRegular() {
				return false, nil
			}
		}
		parent := pyDirname(p)
		if parent == p {
			return false, nil
		}
		p = parent
	}
}

// pyDirname is os.path.dirname for a normalised absolute path.
func pyDirname(p string) string {
	i := strings.LastIndexByte(p, '/') + 1
	head := p[:i]
	if head != "" && strings.Trim(head, "/") != "" {
		head = strings.TrimRight(head, "/")
	}
	return head
}

// gitIsShared is a port of Analyzer.git_is_shared.
func gitIsShared(cwd string, dyn bool, gitDir, workTree string) (bool, error) {
	if (gitDir != "" && dynamicDir(gitDir)) || (workTree != "" && dynamicDir(workTree)) {
		return true, nil
	}
	if gitDir != "" {
		gd := pyRealpath(pyJoin(cwd, expandUser(gitDir)))
		homes, err := gitHomes()
		if err != nil {
			return false, err
		}
		for _, h := range homes {
			top := pyJoin(h, ".git")
			if Under(gd, top) && !Under(gd, pyJoin(top, "worktrees")) {
				return true, nil
			}
		}
	}
	if workTree != "" {
		return gitTopShared(pyJoin(cwd, expandUser(workTree)))
	}
	if dyn {
		return true, nil
	}
	return gitTopShared(cwd)
}

// hasGitEnvAssign is re.search(r"\bGIT_\w*=", text).
func hasGitEnvAssign(text string) bool {
	for i := 0; i+4 <= len(text); i++ {
		if text[i:i+4] != "GIT_" || wordBefore(text, i) {
			continue
		}
		j := i + 4
		for j < len(text) {
			r, n := utf8.DecodeRuneInString(text[j:])
			if !isWord(r) {
				break
			}
			j += n
		}
		if j < len(text) && text[j] == '=' {
			return true
		}
	}
	return false
}

// hasCfgEnvAssign is worker-guard.py:CFG_ENV_ASSIGN.search(text): (?<![\w])(?:GIT_CONFIG\w*|HOME|XDG_CONFIG_HOME)=
func hasCfgEnvAssign(text string) bool {
	for i := 0; i < len(text); i++ {
		if wordBefore(text, i) {
			continue
		}
		s := text[i:]
		switch {
		case strings.HasPrefix(s, "HOME="), strings.HasPrefix(s, "XDG_CONFIG_HOME="):
			return true
		case strings.HasPrefix(s, "GIT_CONFIG"):
			j := len("GIT_CONFIG")
			for j < len(s) {
				r, n := utf8.DecodeRuneInString(s[j:])
				if !isWord(r) {
					break
				}
				j += n
			}
			if j < len(s) && s[j] == '=' {
				return true
			}
		}
	}
	return false
}

// gitShared is a port of Analyzer.git_shared: a worker runs no mutating git where the effective directory belongs
// to the shared main checkout. Unknown subcommands count as mutating.
func (a *Analyzer) gitShared(words, assigns []string) error {
	rest := words[1:]
	cwd, dyn := a.gcwd, a.gdyn
	gitDir, workTree := "", ""
	for _, x := range assigns {
		if strings.HasPrefix(x, "GIT_DIR=") {
			gitDir = x[len("GIT_DIR="):]
		} else if strings.HasPrefix(x, "GIT_WORK_TREE=") {
			workTree = x[len("GIT_WORK_TREE="):]
		}
	}
	cfg := hasGitEnvAssign(a.text)
	for _, x := range assigns {
		if strings.HasPrefix(x[:strings.IndexByte(x+"=", '=')], "GIT_") {
			cfg = true
		}
	}
	i := 0
	for i < len(rest) && strings.HasPrefix(rest[i], "-") {
		o := rest[i]
		cfg = cfg || o == "-c" || o == "--config-env" || strings.HasPrefix(o, "-c") ||
			strings.HasPrefix(o, "--config-env=") || strings.HasPrefix(o, "--exec-path")
		if in(GitValueOpts, o) && i+1 < len(rest) {
			switch o {
			case "-C":
				cwd, dyn = gitJoin(cwd, dyn, rest[i+1])
			case "--git-dir":
				gitDir = rest[i+1]
			case "--work-tree":
				workTree = rest[i+1]
			}
			i += 2
			continue
		}
		if strings.HasPrefix(o, "--git-dir=") {
			gitDir = o[len("--git-dir="):]
		} else if strings.HasPrefix(o, "--work-tree=") {
			workTree = o[len("--work-tree="):]
		}
		i++
	}
	if i >= len(rest) {
		return nil
	}
	sub, args := rest[i], rest[i+1:]
	if !gitMutates(sub, args) {
		return nil
	}
	shared, err := gitIsShared(cwd, dyn, gitDir, workTree)
	if err != nil {
		return err
	}
	if !shared {
		return nil
	}
	if sub == "init" && !(cfg || gitDir != "" || workTree != "") && gitOutsideShared(args, cwd, dyn) {
		return nil // M5368: init of a scratch dir
	}
	if a.Role == "cleaner" && gitCleanerOK(sub, args) {
		return nil
	}
	shown := strings.Join(append([]string{"git", sub}, args...), " ")
	if r := []rune(shown); len(r) > 80 {
		shown = string(r[:80])
	}
	return gitBlockf("`" + shown + "`: " + sharedAdvice)
}

// gitOutsideShared is a port of Analyzer.git_outside_shared: `git init` is allowed in a shared checkout's cwd
// only for exactly one literal directory inside the worker's own scratchpad. An option or path not understood is
// unsafe.
func gitOutsideShared(args []string, cwd string, dyn bool) bool {
	if dyn {
		return false
	}
	var out []string
	k := 0
	for k < len(args) {
		x := args[k]
		k++
		if x == "--" {
			out = append(out, args[k:]...)
			break
		}
		name, eq := x, false
		if j := strings.IndexByte(x, '='); j >= 0 {
			name, eq = x[:j], true
		}
		switch {
		case x == "-q" || x == "--quiet" || x == "--bare":
		case name == "-b" || name == "--initial-branch":
			if !eq {
				if k >= len(args) {
					return false
				}
				k++
			}
		case strings.HasPrefix(x, "-") && x != "-":
			return false // --separate-git-dir, --template, an unknown option
		default:
			out = append(out, x)
		}
	}
	if len(out) != 1 {
		return false
	}
	target := out[0]
	if target == "" || dynamicDir(target) || strings.HasPrefix(target, "~") {
		return false // quoted '~' is a literal name to git: not decidable here
	}
	return inScratchpad(pyRealpath(pyJoin(cwd, target))) // M5368: only the worker's own scratchpad
}

// branchMutates is a port of Analyzer.branch_mutates.
func branchMutates(args []string) bool {
	listing, positional := false, false
	k := 0
	for k < len(args) {
		x := args[k]
		k++
		switch {
		case strings.HasPrefix(x, "--"):
			n := x
			if j := strings.IndexByte(x, '='); j >= 0 {
				n = x[:j]
			}
			switch {
			case in(BranchListLong, n):
				listing = true
			case in(BranchValueLong, n):
				listing = true
				if !strings.Contains(x, "=") {
					k++
				}
			case !in(BranchPlainLong, n):
				return true
			}
		case strings.HasPrefix(x, "-") && len(x) > 1:
			for _, r := range x[1:] {
				if !strings.ContainsRune("larviq", r) {
					return true
				}
				if strings.ContainsRune("lar", r) {
					listing = true
				}
			}
		default:
			positional = true
		}
	}
	return positional && !listing
}

// configMutates is a port of Analyzer.config_mutates.
func configMutates(args []string) bool {
	for _, x := range args {
		if in(ConfigWrite, x) {
			return true
		}
	}
	if len(args) > 0 && (args[0] == "set" || args[0] == "unset" || args[0] == "edit") {
		return true
	}
	for _, x := range args {
		if in(ConfigRead, x) {
			return false
		}
	}
	if len(args) > 0 && (args[0] == "get" || args[0] == "list") {
		return false
	}
	n := 0
	for _, x := range args {
		if !strings.HasPrefix(x, "-") {
			n++
		}
	}
	return n >= 2
}

// gitMutates is a port of Analyzer.git_mutates.
func gitMutates(sub string, args []string) bool {
	if in(GitReadOnly, sub) || strings.HasPrefix(sub, "-") { // `git --version`, `git --help`
		return false
	}
	first, has := "", len(args) > 0
	if has {
		first = args[0]
	}
	switch sub {
	case "branch":
		return branchMutates(args)
	case "worktree":
		return has && first != "list"
	case "config":
		return configMutates(args)
	case "remote":
		return has && first != "-v" && first != "--verbose" && first != "show" && first != "get-url"
	case "notes":
		return has && first != "list" && first != "show"
	}
	return true
}

// gitCleanerOK is a port of Analyzer.git_cleaner_ok: the cleaner's hand-removal of stray worktrees:
// worktree remove (no --force), worktree prune, branch -d.
func gitCleanerOK(sub string, args []string) bool {
	first := ""
	if len(args) > 0 {
		first = args[0]
	}
	if sub == "worktree" && first == "remove" {
		for _, x := range args[1:] {
			if x == "--force" || shortFlagHas(x, 'f') {
				return false
			}
		}
		return true
	}
	if sub == "worktree" && first == "prune" {
		return true
	}
	if sub == "branch" {
		if first != "-d" || len(args) < 2 {
			return false
		}
		for _, x := range args[1:] {
			if strings.HasPrefix(x, "-") {
				return false
			}
		}
		return true
	}
	return false
}

// shortFlagHas is re.match(r"^-[A-Za-z]*<c>", a): c within the leading run of ASCII letters after one dash.
func shortFlagHas(a string, c byte) bool {
	if !strings.HasPrefix(a, "-") {
		return false
	}
	for i := 1; i < len(a) && isASCIILetter(a[i]); i++ {
		if a[i] == c {
			return true
		}
	}
	return false
}

// gitDelete is a port of Analyzer.git_delete: branch deletion (T-0057). Every worker role is refused; the cleaner
// may delete only factory/* branches (local `git branch -d/-D/--delete`, remote `git push <remote> --delete|-d|:<ref>`);
// its other pushes are refused.
func (a *Analyzer) gitDelete(rest []string) error {
	i := 0
	for i < len(rest) && strings.HasPrefix(rest[i], "-") {
		switch rest[i] {
		case "-C", "-c", "--git-dir", "--work-tree", "--namespace", "--exec-path":
			i += 2
		default:
			i++
		}
	}
	if i >= len(rest) || (rest[i] != "branch" && rest[i] != "push") {
		return nil
	}
	sub, args := rest[i], rest[i+1:]
	var targets []string
	if sub == "branch" {
		found := false
		for _, x := range args {
			if dashLetters(x, "dD") || x == "--delete" {
				found = true
			}
		}
		if !found {
			return nil
		}
		for _, x := range args {
			if !strings.HasPrefix(x, "-") {
				targets = append(targets, x)
			}
		}
	} else {
		var pos []string
		for k := 0; k < len(args); {
			x := args[k]
			switch x {
			case "-o", "--push-option", "--repo", "--receive-pack", "--exec":
				k += 2
				continue
			}
			if !strings.HasPrefix(x, "-") {
				pos = append(pos, x)
			}
			k++
		}
		flag := false
		for _, x := range args {
			if dashLetters(x, "d") || x == "--delete" {
				flag = true
			}
		}
		colon := false
		var tail []string
		if len(pos) > 1 {
			tail = pos[1:]
		}
		for _, x := range tail {
			if strings.HasPrefix(x, ":") || strings.HasPrefix(x, "+:") {
				colon = true
			}
		}
		if !flag && !colon {
			if a.Role == "cleaner" {
				return blockf("the cleaner may only delete factory/* branches (git push is refused)")
			}
			return nil
		}
		targets = tail
		if !flag {
			for _, x := range targets {
				if !strings.HasPrefix(x, ":") && !strings.HasPrefix(x, "+:") {
					return blockf("git push mixing deletes and pushes")
				}
			}
			for k, x := range targets {
				targets[k] = strings.TrimLeft(x, "+:")
			}
		}
	}
	if a.Role == "cleaner" && len(targets) > 0 {
		ok := true
		for _, t := range targets {
			if !factoryBranch(t) || strings.Contains(t, "..") {
				ok = false
			}
		}
		if ok {
			return nil
		}
	}
	return blockf("branch deletion is refused (the cleaner may delete only factory/<x> branches, every target " +
		"must match; other roles never)")
}

// configArgsWrite is a port of Analyzer.config_args_write: like configMutates, but understands the get/list
// words and options that take a value (`--type bool key` is a read).
func configArgsWrite(args []string) bool {
	var pos, flags []string
	for k := 0; k < len(args); {
		x := args[k]
		k++
		if in(ConfigValueOpts, x) {
			k++
		} else if !strings.HasPrefix(x, "-") {
			pos = append(pos, x)
		}
	}
	for _, x := range args {
		if strings.HasPrefix(x, "-") {
			flags = append(flags, x)
		}
	}
	for _, x := range flags {
		if in(ConfigWrite, x) {
			return true
		}
	}
	for _, x := range flags {
		if in(ConfigRead, x) {
			return false
		}
	}
	if len(pos) > 0 && (pos[0] == "get" || pos[0] == "list") {
		return false
	}
	return len(pos) > 0 && (pos[0] == "set" || pos[0] == "unset" || pos[0] == "edit") || len(pos) >= 2
}

// gitPersist is a port of Analyzer.git_persist (T-0334): `git config --global|--system|-f <protected file>` writes,
// also behind `git -c ... config` and an alias. A write that is neither a --global/--system one nor in a command
// that assigns GIT_CONFIG*/HOME/XDG_CONFIG_HOME reaches persist_hit / repo_cfg_refuse, which T6d owns: stub.
func (a *Analyzer) gitPersist(rest []string, depth int) error {
	type aliasDef struct{ name, body string }
	var aliases []aliasDef
	i := 0
	for i < len(rest) && strings.HasPrefix(rest[i], "-") {
		o := rest[i]
		val := ""
		if in(GitValueOpts, o) && i+1 < len(rest) {
			val, i = rest[i+1], i+2
		} else {
			if strings.HasPrefix(o, "-c") && !strings.HasPrefix(o, "--") && len(o) > 2 {
				val, o = o[2:], "-c"
			} else if strings.HasPrefix(o, "--git-dir=") || strings.HasPrefix(o, "--work-tree=") {
				j := strings.IndexByte(o, '=')
				o, val = o[:j], o[j+1:]
			}
			i++
		}
		if o == "-c" && val != "" {
			if body, ok := aliasAssign(val); ok {
				name, b := body, ""
				if j := strings.IndexByte(body, '='); j >= 0 {
					name, b = body[:j], body[j+1:]
				}
				aliases = append(aliases, aliasDef{name, b})
			}
		}
	}
	for _, al := range aliases {
		var used []string
		if i < len(rest) && rest[i] == al.name {
			used = rest[i+1:]
		}
		if strings.HasPrefix(al.body, "!") {
			parts := []string{al.body[1:]}
			for _, u := range used {
				parts = append(parts, shlex.Quote(u))
			}
			if err := a.script(strings.Join(parts, " "), depth+1); err != nil {
				return err
			}
			continue
		}
		words, err := shlex.Split(al.body)
		if err != nil {
			return blockf("cannot parse git alias (" + err.Error() + ")")
		}
		if len(words) > 0 && words[0] == "config" {
			next := append(append([]string{"config"}, words[1:]...), used...)
			if err := a.gitPersist(next, depth); err != nil {
				return err
			}
		}
	}
	if i < len(rest) && rest[i] == "remote" {
		if i+1 < len(rest) && in(RemoteWrite, rest[i+1]) {
			return notPorted("git remote write: repo_cfg_refuse (T6d)")
		}
		return nil
	}
	if i >= len(rest) || rest[i] != "config" {
		return nil
	}
	var args []string
	scope := ""
	hasFiles := false
	for k := i + 1; k < len(rest); {
		x := rest[k]
		k++
		name, eq := x, false
		if j := strings.IndexByte(x, '='); j >= 0 {
			name, eq = x[:j], true
		}
		if strings.HasPrefix(x, "--") && len(name) >= 4 {
			if strings.HasPrefix("--global", name) || strings.HasPrefix("--system", name) {
				scope = name
				continue
			}
			if strings.HasPrefix("--file", name) && len(name) >= 5 {
				hasFiles = true
				if !eq && k < len(rest) {
					k++
				}
				continue
			}
		} else if strings.HasPrefix(x, "-f") && !strings.HasPrefix(x, "--") {
			hasFiles = true
			if len(x) == 2 && k < len(rest) {
				k++
			}
			continue
		}
		args = append(args, x)
	}
	if !configArgsWrite(args) {
		return nil
	}
	if scope != "" {
		return blockf("`git config " + scope + "` write: " + persistWhy)
	}
	if hasCfgEnvAssign(a.text) { // GIT_CONFIG=<file> acts as --file, HOME= moves --global
		return blockf("a `git config` write in a command that assigns GIT_CONFIG*, HOME or XDG_CONFIG_HOME (it can " +
			"redirect the write to a protected file); set them in a separate command or drop them: " + persistWhy)
	}
	if hasFiles {
		return notPorted("git config --file: persist_hit (T6d)")
	}
	return notPorted("git config write: repo_cfg_refuse (T6d)")
}

// aliasAssign is re.match(r"(?i)alias\.[^=]+=", val); it returns val[6:].
func aliasAssign(val string) (string, bool) {
	rs := []rune(val)
	if len(rs) < 8 || !strings.EqualFold(string(rs[:6]), "alias.") {
		return "", false
	}
	for k := 6; k < len(rs); k++ {
		if rs[k] == '=' {
			return string(rs[6:]), k > 6
		}
	}
	return "", false
}

// gitHerdrInConfig is the loop at the end of the `git` branch of Analyzer.cmd.
func gitHerdrInConfig(rest []string) error {
	for j, x := range rest {
		if !strings.Contains(x, "herdr") {
			continue
		}
		prevC := j >= 1 && rest[j-1] == "-c"
		cfgBefore := false
		for _, y := range rest[:j] {
			if y == "config" {
				cfgBefore = true
			}
		}
		if prevC || cfgBefore || strings.HasPrefix(x, "-c") {
			return blockf("herdr in git config (alias/pager can run shell commands)")
		}
	}
	return nil
}

// gitPushPinStub reports whether worker-guard.py:git_push_pin (a coder's push pin, which reads the factory DB)
// could act on this command: only a coder, and only when a push-like word is present. T6d owns the rule itself.
func gitPushPinStub(role string, rest []string) error {
	if role != "coder" {
		return nil
	}
	for _, x := range rest {
		switch x {
		case "push", "send-pack", "http-push":
			return notPorted("git_push_pin (T6d)")
		}
	}
	return nil
}

// ptGitSubs is worker-guard.py:PT_GIT_SUBS.
var ptGitSubs = set("commit", "tag", "push", "merge", "commit-tree", "notes")

// gitPublicTextStub reports whether PublicText.git could look at this git command: its subcommand is one of
// PT_GIT_SUBS, or a `-c alias.*` hides it. The rule reads the factory DB (public_text_repos), so it is T6d's;
// the option walk is run for both readings of `-C` (a dynamic cd makes pt skip the value) and stubs on either.
func gitPublicTextStub(args []string) error {
	for _, cKnown := range []bool{true, false} {
		j, alias := 0, false
		for j < len(args) && strings.HasPrefix(args[j], "-") {
			x := args[j]
			switch {
			case x == "-C":
				if cKnown && j+1 < len(args) {
					j++
				}
			case x == "--git-dir" || x == "--work-tree":
				j++
			case strings.HasPrefix(x, "--git-dir=") || strings.HasPrefix(x, "--work-tree="):
			case x == "-c" || x == "--namespace" || x == "--exec-path":
				if x == "-c" && j+1 < len(args) && strings.HasPrefix(strings.ToLower(args[j+1]), "alias.") {
					alias = true
				}
				j++
			}
			j++
		}
		if j < len(args) && (in(ptGitSubs, args[j]) || alias) {
			return notPorted("public text for git (T6d)")
		}
	}
	return nil
}
