package guard

import (
	"errors"
	"io"
	"os"
	"os/user"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Section 2 of the port: the command analyser (cmd and everything it calls). The decisions that read the factory DB
// (owngoal.go) are the Python hook's: they return a Delegate, never an allow.

var (
	reDashCeEr   = regexp.MustCompile(`^-[A-Za-z]*[ceEr]\n?$`)
	reFactoryM   = regexp.MustCompile(`^-[A-Za-z]*m\n?$`)
	reFactoryMod = regexp.MustCompile(`^-[A-Za-z]*mfactory_cli(\.py)?\n?$`)
	reDashLettM  = regexp.MustCompile(`^-[A-Za-z]*m`)
	reLiveAbbrev = regexp.MustCompile(`^--l[a-z-]*(=|\n?$)`)
)

// claudeCLI is a port of CLAUDE_CLI.search(a): claude-code/(?:[\w.-]+/)*[\w.-]+\.(?:c|m)?js$
func claudeCLI(a string) bool {
	const lit = "claude-code/"
	for from := 0; ; {
		k := strings.Index(a[from:], lit)
		if k < 0 {
			return false
		}
		p := from + k + len(lit)
		// the rest must be (?:[\w.-]+/)*[\w.-]+\.(?:c|m)?js then end (or before a final newline)
		tail := strings.TrimSuffix(a[p:], "\n")
		if cliTail(tail) {
			return true
		}
		from += k + 1
	}
}

func cliTail(t string) bool {
	segOK := func(s string) bool {
		if s == "" {
			return false
		}
		for _, r := range s {
			if !(isWord(r) || r == '.' || r == '-') {
				return false
			}
		}
		return true
	}
	parts := strings.Split(t, "/")
	for _, p := range parts[:len(parts)-1] {
		if !segOK(p) {
			return false
		}
	}
	last := parts[len(parts)-1]
	for _, ext := range []string{".js", ".cjs", ".mjs"} {
		if strings.HasSuffix(last, ext) && segOK(strings.TrimSuffix(last, ext)) {
			return true
		}
	}
	return false
}

// pythonName is re.fullmatch(r"python[\d.]*", name).
func pythonName(name string) bool {
	rest, ok := strings.CutPrefix(name, "python")
	if !ok {
		return false
	}
	for _, r := range rest {
		if r != '.' && !isDecimal(r) {
			return false
		}
	}
	return true
}

// expandUser is os.path.expanduser.
func expandUser(p string) string {
	if !strings.HasPrefix(p, "~") {
		return p
	}
	i := strings.IndexByte(p, '/')
	if i < 0 {
		i = len(p)
	}
	var home string
	if i == 1 {
		home = os.Getenv("HOME")
		if home == "" {
			u, err := user.Current()
			if err != nil {
				return p
			}
			home = u.HomeDir
		}
	} else {
		u, err := user.Lookup(p[1:i])
		if err != nil {
			return p
		}
		home = u.HomeDir
	}
	home = strings.TrimRight(home, "/")
	if out := home + p[i:]; out != "" {
		return out
	}
	return "/"
}

// pyJoin is os.path.join(a, b) for two components.
func pyJoin(a, b string) string {
	switch {
	case strings.HasPrefix(b, "/"):
		return b
	case a == "" || strings.HasSuffix(a, "/"):
		return a + b
	}
	return a + "/" + b
}

// ───────────────────────────── small pure rules ─────────────────────────────

// checkScrub is a port of Analyzer.check_scrub. It returns the new scrub_with_factory value ("" = unchanged).
func checkScrub(name string, rest []string) (string, error) {
	if name == "unset" && anyOf(rest, func(x string) bool { return in(ScrubVars, x) }) {
		return "", blockf("unset of FACTORY_ROLE/HERDR_PANE_ID")
	}
	if name != "env" {
		return "", nil
	}
	scrub := ""
	for j, x := range rest {
		if !strings.HasPrefix(x, "-") && !IsAssign(x) {
			break
		}
		if (x == "-u" || x == "--unset") && j+1 < len(rest) && in(ScrubVars, rest[j+1]) {
			return "", blockf("env -u FACTORY_ROLE/HERDR_PANE_ID")
		}
		if strings.HasPrefix(x, "-u") && in(ScrubVars, x[2:]) || strings.HasPrefix(x, "--unset=") && in(ScrubVars, x[8:]) {
			return "", blockf("env -u FACTORY_ROLE/HERDR_PANE_ID")
		}
		if x == "-" || x == "--ignore-environment" || isEnvIFlag(x) {
			scrub = "env -i"
		}
	}
	return scrub, nil
}

// isEnvIFlag is re.match(r"^-[A-Za-z]*i[A-Za-z]*$", a).
func isEnvIFlag(a string) bool {
	a = strings.TrimSuffix(a, "\n")
	if len(a) < 2 || a[0] != '-' {
		return false
	}
	seen := false
	for i := 1; i < len(a); i++ {
		c := a[i]
		if c == 'i' {
			seen = true
		} else if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z') {
			return false
		}
	}
	return seen
}

// factoryScript is a port of Analyzer.factory_script: the index of the `factory` script word in an
// interpreter's args, else -1.
func factoryScript(name string, rest []string) int {
	for k, x := range rest {
		if k+1 < len(rest) && reFactoryM.MatchString(x) && containsStr(FactoryNames, pyBasename(rest[k+1])) {
			return k + 1
		}
		if reFactoryMod.MatchString(x) {
			return k
		}
	}
	si, _, unknown := InterpArgs(name, rest)
	if unknown {
		for k, x := range rest {
			if containsStr(FactoryNames, pyBasename(x)) {
				return k
			}
		}
		return -1
	}
	if si < 0 || !containsStr(FactoryNames, pyBasename(rest[si])) {
		return -1
	}
	return si
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// factoryInitLiveHome is the first block of Analyzer.cfg_write: `factory init --live-home` (also through an
// interpreter running the factory script) writes ~/.config/factory.
func factoryInitLiveHome(name string, rest []string) error {
	if name != "factory" && !(interpreterFull(name) && factoryScript(name, rest) >= 0) {
		return nil
	}
	abbrev := false
	for _, x := range rest {
		if reLiveAbbrev.MatchString(x) {
			opt := x
			if k := strings.IndexByte(x, '='); k >= 0 {
				opt = x[:k]
			}
			if strings.HasPrefix("--live-home", opt) {
				abbrev = true
			}
		}
	}
	if containsStr(rest, "init") && abbrev { // argparse accepts any unambiguous prefix of the option
		return blockf("`factory init --live-home` writes ~/.config/factory: it holds live_home, which decides what counts " +
			"as the live home; only the owner changes it with `factory init --live-home`")
	}
	return nil
}

// isInPlaceFlag is re.match(r"^-[A-Za-z]*i", a) or a.startswith("--in-place") or a == "inplace".
func isInPlaceFlag(a string) bool {
	if a == "inplace" || strings.HasPrefix(a, "--in-place") {
		return true
	}
	if len(a) < 2 || a[0] != '-' {
		return false
	}
	for i := 1; i < len(a); i++ {
		c := a[i]
		if c == 'i' {
			return true
		}
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z') {
			return false
		}
	}
	return false
}

// ───────────────────────────── test runner routing (T-0675) ─────────────────────────────

// pythonMSplit is a port of Analyzer.python_m_split.
func pythonMSplit(rest []string) (mod string, args []string, has bool) {
	i := 0
	for i < len(rest) {
		tok := rest[i]
		i++
		if tok == "--" || tok == "-" || !strings.HasPrefix(tok, "-") {
			return "", nil, false
		}
		if strings.HasPrefix(tok, "--") {
			if tok == "--check-hash-based-pycs" {
				i++
			} else if !(strings.HasPrefix(tok, "--check-hash-based-pycs=") || tok == "--version" || strings.HasPrefix(tok, "--help")) {
				return "?", sliceFrom(rest, i), true
			}
			continue
		}
	flags:
		for k := 1; k < len(tok); k++ {
			ch := tok[k]
			switch {
			case strings.IndexByte("bBdEhiIOPqRsSuvVx?", ch) >= 0:
			case ch == 'W' || ch == 'X':
				if k == len(tok)-1 {
					i++ // value is the next token, even one that starts with "-"
				}
				break flags
			case ch == 'm':
				if k+1 < len(tok) {
					return tok[k+1:], sliceFrom(rest, i), true
				}
				if i < len(rest) {
					return rest[i], sliceFrom(rest, i+1), true
				}
				return "", nil, false
			case ch == 'c':
				return "", nil, false
			default:
				return "?", sliceFrom(rest, i), true
			}
		}
	}
	return "", nil, false
}

func sliceFrom(s []string, i int) []string {
	if i >= len(s) {
		return nil
	}
	return s[i:]
}

func pythonMModule(rest []string) (string, bool) {
	m, _, ok := pythonMSplit(rest)
	return m, ok
}

func firstDot(m string) string { return strings.SplitN(m, ".", 2)[0] }

func goSubcommand(rest []string) (string, bool) {
	for i := 0; i < len(rest); {
		x := rest[i]
		if x == "-C" || x == "--C" { // T-0735: `-C <dir>` / `--C <dir>` takes a value
			i += 2
		} else {
			i++
		}
		if !strings.HasPrefix(x, "-") {
			return x, true
		}
	}
	return "", false
}

func uvRunRunner(rest []string) string {
	valued := set("--with", "--with-requirements", "-p", "--python", "--project", "--directory", "--extra", "--group",
		"--env-file", "--package", "--only-group", "--no-group", "--with-editable", "-w", "--index", "--config-file")
	for i := 0; i < len(rest); {
		x := rest[i]
		i++
		if x == "-m" && i < len(rest) {
			x, rest = "python", append([]string{"-m"}, rest[i:]...)
			i = 0
		} else if in(valued, x) {
			i++
			continue
		} else if strings.HasPrefix(x, "-") {
			continue
		}
		if x == "pytest" || x == "py.test" {
			return "uv run " + x
		}
		if pythonName(x) {
			if mod, ok := pythonMModule(sliceFrom(rest, i)); ok && mod != "" && (firstDot(mod) == "unittest" || firstDot(mod) == "pytest") {
				return "uv run " + x + " -m " + mod
			}
		}
		return ""
	}
	return ""
}

// uvxScan is a port of worker-guard.py:uvx_scan (T-0748): fail closed. Any word after uvx / `uv [global opts] tool run`
// naming pytest/py.test/unittest is the runner, after stripping extras, specifiers and version suffixes.
func uvxScan(words []string, prefix string) string {
	if prefix == "uv tool run" {
		for i, w := range words {
			if w == "tool" {
				words = words[i+1:]
				break
			}
		}
	}
	for _, t := range words {
		if strings.HasPrefix(t, "-") {
			_, t, _ = strings.Cut(t, "=")
		}
		// re.split(r"[\s\[@=<>~!;(]", basename, maxsplit=1)[0]
		base := pyBasename(t)
		if i := strings.IndexFunc(base, func(r rune) bool { return pyIsSpace(r) || strings.ContainsRune("[@=<>~!;(", r) }); i >= 0 {
			base = base[:i]
		}
		base = strings.ToLower(base)
		if base == "pytest" || base == "py.test" || base == "unittest" {
			return prefix + " " + base
		}
	}
	return ""
}

// sandboxRoute is a port of Analyzer.sandbox_route; the test_sandbox config row is read by the Python hook (delegate).
func (a *Analyzer) sandboxRoute(name string, rest []string) error {
	if a.Role != "coder" && a.Role != "qa" {
		return nil
	}
	isRunner := func(m string) bool { return firstDot(m) == "unittest" || firstDot(m) == "pytest" }
	runner := ""
	switch {
	case pythonName(name):
		mod, _ := pythonMModule(rest)
		if mod == "?" { // unknown interpreter option: fail closed on any later -m naming a runner
			mod = ""
			for j, t := range rest {
				m := ""
				if strings.HasPrefix(t, "-m") && len(t) > 2 {
					m = t[2:]
				} else if t == "-m" && j+1 < len(rest) {
					m = rest[j+1]
				}
				if m != "" && isRunner(m) {
					mod = m
					break
				}
			}
		}
		if mod != "" && isRunner(mod) {
			runner = "python3 -m " + mod
		} else { // T-0745: python3 -m coverage run -m pytest
			cmod, cargs, _ := pythonMSplit(rest)
			for cmod == "?" {
				cmod, cargs, _ = pythonMSplit(cargs)
			}
			if cmod == "coverage" && len(cargs) > 0 && cargs[0] == "run" {
				if cov, ok := coverageRunModule(cargs[1:]); ok {
					runner = "python3 -m coverage run -m " + cov
				}
			}
		}
	case name == "pytest" || name == "py.test":
		runner = name
	case name == "go":
		if sub, ok := goSubcommand(rest); ok && sub == "test" {
			runner = "go test"
		}
	case name == "tox":
		runner = "tox"
	case name == "coverage" && len(rest) > 0 && rest[0] == "run":
		if cov, ok := coverageRunModule(rest[1:]); ok {
			runner = "coverage run -m " + cov
		}
	case name == "uv" && len(rest) > 0 && rest[0] == "run":
		runner = uvRunRunner(rest[1:])
	case name == "uv" && in(set(rest...), "tool"): // T-0745, T-0748: global options may sit between tool and run
		runner = uvxScan(rest, "uv tool run")
	case name == "uvx": // T-0745, T-0748
		runner = uvxScan(rest, "uvx")
	}
	if runner != "" && !dbMissing() { // test_sandbox_on(): a config row of the factory DB, read by the Python hook
		return delegate("sandbox_route: config test_sandbox is in the factory DB")
	}
	return nil
}

// coverageRunModule is a port of Analyzer.coverage_run_module (T-0745): fail closed. Any word after `coverage run`
// naming pytest/py.test/unittest is the runner; no option model, so flags, valued options and clusters cannot hide it.
func coverageRunModule(args []string) (string, bool) {
	for _, t := range args {
		var cand string
		switch {
		case strings.HasPrefix(t, "--module="):
			cand = t[9:]
		case strings.HasPrefix(t, "-") && !strings.HasPrefix(t, "--"):
			if i := strings.Index(t, "m"); i >= 0 {
				cand = t[i+1:] // -mpytest, -pmpytest
			}
		case strings.HasPrefix(t, "-"):
			_, cand, _ = strings.Cut(t, "=")
		default:
			cand = t
		}
		base := pyBasename(cand)
		if base == "py.test" || firstDot(base) == "pytest" || firstDot(base) == "unittest" {
			return cand, true
		}
	}
	return "", false
}

var reTestModule = regexp.MustCompile(`^test_(.+)\.py$`)

// unittestFull is a port of Analyzer.unittest_full (T-0642).
func (a *Analyzer) unittestFull(name string, rest []string) error {
	if a.Role != "coder" && a.Role != "qa" && a.Role != "reviewer" || !pythonName(name) {
		return nil
	}
	mod, args, _ := pythonMSplit(rest)
	for mod == "?" { // unknown interpreter option: keep looking for a later -m
		mod, args, _ = pythonMSplit(args)
	}
	if mod != "unittest" && mod != "unittest.__main__" {
		return nil
	}
	const advice = "; CI runs the full suite. Locally run `python3 ci/affected.py --base origin/main --quick --run`, or one " +
		"module: `python3 -m unittest discover -s tests -p test_X.py` (T-0642)"
	if len(args) > 0 && args[0] == "discover" {
		var pats []string
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-p" || args[i] == "--pattern" {
				pats = append(pats, args[i+1])
			}
		}
		for _, x := range args {
			if v, ok := strings.CutPrefix(x, "--pattern="); ok {
				pats = append(pats, v)
			}
		}
		if len(pats) > 0 && allOf(pats, singleModulePattern) { // the last pattern wins, so all must be single-module
			return nil
		}
		return blockf("`python3 -m unittest discover` without a single-module -p pattern runs the full suite" + advice)
	}
	skip, mods := false, 0
	for _, x := range args {
		switch {
		case skip:
			skip = false
		case x == "-k" || x == "--start-directory" || x == "-s" || x == "-t" || x == "-p":
			skip = true
		case !strings.HasPrefix(x, "-"):
			mods++
		}
	}
	if mods == 0 {
		return blockf("`python3 -m unittest` with no test module runs the full suite" + advice)
	}
	return nil
}

// singleModulePattern is re.fullmatch(r"test_[\w]+\.py", x).
func singleModulePattern(x string) bool {
	body, ok := strings.CutPrefix(x, "test_")
	if !ok {
		return false
	}
	body, ok = strings.CutSuffix(body, ".py")
	if !ok || body == "" {
		return false
	}
	for _, r := range body {
		if !isWord(r) {
			return false
		}
	}
	return true
}

// ───────────────────────────── cmd ─────────────────────────────

// ptPeel is the wrapper peeling at the top of PublicText.command: in HQ mode the wrappers with a modelled option
// set are skipped, so the command word comes first.
func ptPeel(words []string, hq bool) []string {
	words = append([]string(nil), words...)
	for hq && len(words) > 0 && WrapValueOpts[pyBasename(words[0])] != nil {
		name := pyBasename(words[0])
		words = words[1:]
		for len(words) > 0 && (strings.HasPrefix(words[0], "-") || IsAssign(words[0]) ||
			((name == "timeout" || name == "nice") && isDurationWord(words[0]))) {
			opt := words[0]
			words = words[1:]
			if in(WrapValueOpts[name], opt) && len(words) > 0 {
				words = words[1:]
			}
		}
	}
	return words
}

// isDurationWord is re.fullmatch(r"[\d.]+[smhd]?", w).
func isDurationWord(w string) bool {
	rs := []rune(w)
	if n := len(rs); n > 0 && strings.ContainsRune("smhd", rs[n-1]) {
		rs = rs[:n-1]
	}
	if len(rs) == 0 {
		return false
	}
	for _, r := range rs {
		if r != '.' && !isDecimal(r) {
			return false
		}
	}
	return true
}

// cmd is a port of Analyzer.cmd. envAssigns are the FACTORY_HOME= assignments an enclosing wrapper passes down.
func (a *Analyzer) cmd(words []string, depth int, piped, inLoose bool, hstrs, envAssigns []string) error {
	assigns := append([]string(nil), envAssigns...)
	stripped := StripPrefix(words)
	for _, x := range words[:len(words)-len(stripped)] {
		if IsAssign(x) {
			assigns = append(assigns, x)
		}
	}
	words = stripped
	if a.noPush() {
		if err := a.gitConfigEnv(assigns, words); err != nil {
			return err
		}
		if err := a.builtinAssign(assigns, words, hstrs); err != nil {
			return err
		}
		if err := a.gitLaunch(assigns, words); err != nil {
			return err
		}
	}
	if len(words) == 0 {
		for _, x := range assigns { // NAME=value assigned earlier in the command: lets the persist detector resolve $NAME
			if IsAssign(x) {
				k := strings.IndexByte(x, '=')
				if !strings.HasSuffix(x[:k], "+") {
					a.vars[x[:k]] = x[k+1:]
				}
			}
		}
		if !a.HQ {
			return a.checkIdentity(assigns)
		}
		return nil
	}
	if pyBasename(words[0]) == "git" {
		if err := a.gitPushPin(words[1:]); err != nil { // before the public-text push check: names the one allowed push
			return err
		}
	}
	if a.pt {
		if err := a.ptCommand(words); err != nil {
			return err
		}
	}
	if !inLoose {
		if err := a.priv(words, depth, piped, hstrs); err != nil {
			return err
		}
		if a.HQ {
			return nil
		}
	}
	w, rest := words[0], words[1:]
	name := pyBasename(w)
	var extra []string
	if in(Declarers, name) {
		extra = filterStr(rest, IsAssign)
	}
	for _, x := range extra {
		k := strings.IndexByte(x, '=')
		a.vars[x[:k]] = x[k+1:]
	}
	if err := a.checkIdentity(append(append([]string(nil), assigns...), extra...)); err != nil {
		return err
	}
	if name == "setsid" {
		return blockf("setsid: re-parenting out of the agent process tree")
	}
	if name == "disown" {
		a.scrubWithFactory = "disown"
	}
	scrub, err := checkScrub(name, rest)
	if err != nil {
		return err
	}
	if scrub != "" {
		a.scrubWithFactory = scrub
	}
	si := -1
	if interpreterFull(name) {
		si = factoryScript(name, rest)
	}
	if name == "factory" || si >= 0 || (in(Shells, name) && firstNonOptBase(rest) == "factory") {
		a.ranFactory = true
	}
	if IsDynamic(w, !inLoose) {
		return &DynamicBlock{Msg: "dynamic command word " + pyRepr(w) + " could expand to herdr or claude"}
	}
	if IsHerdr(w) {
		return blockf("direct herdr invocation")
	}
	if IsClaude(w) {
		return &ClaudeBlock{Msg: "direct claude invocation"}
	}
	if (name == "cd" || name == "pushd" || name == "popd") && !inLoose {
		a.gitCd(name, rest)
		a.cfgCd(name, rest)
	}
	if err := a.claudeLaunchers(name, rest); err != nil {
		return err
	}
	if err := a.sandboxRoute(name, rest); err != nil {
		return err
	}
	if err := factoryInitLiveHome(name, rest); err != nil {
		return err
	}
	if err := a.cfgWrite(name, rest); err != nil {
		return err
	}
	if err := a.persistWrite(name, rest); err != nil {
		return err
	}
	if err := a.factoryUnit(name, rest, assigns); err != nil {
		return err
	}
	if in(Copiers, name) { // R3: no copying/encoding/reading the real herdr binary
		for _, x := range rest {
			if strings.HasPrefix(x, "if=") || strings.HasPrefix(x, "of=") {
				x = x[3:]
			}
			if IsHerdr(x) || strings.Contains(x, "HERDR_BIN_PATH") {
				return blockf(name + " on the herdr binary")
			}
		}
	}
	switch {
	case in(Shells, name):
		savedCwd, savedDyn, savedC, savedCK := a.gcwd, a.gdyn, a.ccwd, a.ccwdKnown // a cd inside `sh -c` does not move the caller
		err := a.shell(rest, depth, piped, hstrs)
		a.gcwd, a.gdyn, a.ccwd, a.ccwdKnown = savedCwd, savedDyn, savedC, savedCK
		if err != nil {
			return err
		}
	case name == "eval":
		if strings.Contains(strings.Join(rest, " "), "$") {
			return blockf("eval of an expansion: content cannot be inspected")
		}
		if err := a.script(strings.Join(rest, " "), depth+1); err != nil {
			return err
		}
	case name == "source" || name == ".":
		if len(rest) > 0 {
			if err := a.file(rest[0], depth); err != nil {
				return err
			}
		}
	case name == "systemctl":
		if err := a.systemctl(rest); err != nil {
			return err
		}
	case interpreterFull(name):
		if err := a.interpPriv(name, rest); err != nil {
			return err
		}
		if err := a.unittestFull(name, rest); err != nil {
			return err
		}
		for j := 0; j+1 < len(rest); j++ {
			if (reDashCeEr.MatchString(rest[j]) || rest[j] == "--eval") && strings.Contains(rest[j+1], "herdr") {
				return blockf("herdr in inline interpreter code")
			}
		}
	case in(Awks, name) || in(Editors, name):
		if anyOf(rest, func(x string) bool { return strings.Contains(x, "herdr") }) {
			return blockf("herdr in " + name + " program text (can run shell commands)")
		}
	case name == "sed":
		for _, x := range rest {
			if strings.Contains(x, "herdr") && sedHerdr(x) {
				return blockf("herdr in a sed e command")
			}
		}
	case name == "git":
		if err := a.gitShared(words, assigns); err != nil { // first: its reason names the own-worktree advice
			return err
		}
		if err := a.gitDelete(rest); err != nil {
			return err
		}
		if a.noPush() { // T-0755: after gitDelete, whose reason names the branch-deletion rule
			if err := a.gitAllow(rest); err != nil {
				return err
			}
		}
		if err := a.gitPersist(rest, depth); err != nil {
			return err
		}
		if err := gitHerdrInConfig(rest); err != nil {
			return err
		}
	case name == "pkill" || name == "killall":
		return blockf(name + ": pattern kills hit other agents' processes (G-008 M1932); kill your own job " +
			"with `kill %<job>` or `kill $!`")
	case name == "kill":
		if err := a.kill(rest); err != nil {
			return err
		}
	case strings.Contains(w, "/") && !inLoose:
		if err := a.execPath(w, depth); err != nil {
			return err
		}
	}
	if name == "command" && len(rest) > 0 && (rest[0] == "-v" || rest[0] == "-V" || rest[0] == "-pv") {
		return nil
	}
	if name == "find" {
		for j, x := range rest {
			if in(FindExec, x) {
				if err := a.loose(rest[j+1:], depth, nil); err != nil {
					return err
				}
			}
		}
	} else if in(Wrappers, name) && !inLoose {
		if name == "xargs" {
			if err := xargs(rest); err != nil {
				return err
			}
		}
		return a.loose(rest, depth, assigns)
	}
	return nil
}

func filterStr(ws []string, f func(string) bool) []string {
	var out []string
	for _, w := range ws {
		if f(w) {
			out = append(out, w)
		}
	}
	return out
}

// firstNonOptBase is next((basename(x) for x in rest if not x.startswith("-")), None), "" for None.
func firstNonOptBase(rest []string) string {
	for _, x := range rest {
		if !strings.HasPrefix(x, "-") {
			return pyBasename(x)
		}
	}
	return ""
}

// sedHerdr is re.search(r"(?:^|[\s;{}\d$,])e\b|/[gIpmM]*e[gIpmM]*(?:\s|;|$)", a).
func sedHerdr(s string) bool {
	for i := 0; i <= len(s); i++ {
		if i < len(s) && s[i] == 'e' && boundary(s, i+1) && (i == 0) {
			return true // ^e\b
		}
		if i < len(s) {
			r := nextRune(s, i)
			if pyIsSpace(r) || strings.IndexByte(";{}$,", s[i]) >= 0 || isDecimal(r) {
				j := i + runeLen(s, i)
				if j < len(s) && s[j] == 'e' && boundary(s, j+1) {
					return true
				}
			}
		}
		if sedAlt2At(s, i) {
			return true
		}
	}
	return false
}

// ───────────────────────────── the rules cmd calls ─────────────────────────────

var (
	reSig    = regexp.MustCompile(`^-(?:[A-Za-z][A-Za-z0-9_+]*)\n?$`)
	reTarget = regexp.MustCompile(`^(?:\$!|\$\{!\})\n?$`)
)

func isSigWord(a string) bool {
	if reSig.MatchString(a) {
		return true
	}
	s := strings.TrimSuffix(a, "\n")
	if len(s) < 2 || s[0] != '-' {
		return false
	}
	for _, r := range s[1:] {
		if !isDecimal(r) {
			return false
		}
	}
	return true
}

func isAllDecimal(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !isDecimal(r) {
			return false
		}
	}
	return true
}

func isJobTarget(t string) bool {
	if reTarget.MatchString(t) {
		return true
	}
	s := strings.TrimSuffix(t, "\n")
	if !strings.HasPrefix(s, "%") {
		return false
	}
	for _, r := range s[1:] {
		if pyIsSpace(r) {
			return false
		}
	}
	return true
}

// kill is a port of Analyzer.kill (G-008 M1932).
func (a *Analyzer) kill(rest []string) error {
	var targets []string
	sigSeen := false
	for i := 0; i < len(rest); {
		x := rest[i]
		if x == "--" {
			targets = append(targets, rest[i+1:]...)
			break
		}
		if x == "-l" || x == "-L" || x == "--list" || x == "--table" {
			return nil
		}
		if x == "-s" || x == "-n" || x == "--signal" {
			sigSeen = true
			i += 2
			continue
		}
		if strings.HasPrefix(x, "--signal=") {
			sigSeen = true
		} else if isSigWord(x) && !(sigSeen && isAllDecimal(x[1:])) {
			sigSeen = true // the signal; a second -<digits> is a negative pid (a process group)
		} else {
			targets = append(targets, x)
		}
		i++
	}
	if len(targets) > 0 && allOf(targets, isJobTarget) {
		return nil
	}
	return blockf("kill: workers may only kill their own background job (`kill %<job>` or `kill $!`); a pid, 0, " +
		"-1 or a process group could hit another agent (G-008 M1932)")
}

// claudeLaunchers is a port of Analyzer.claude_launchers.
func (a *Analyzer) claudeLaunchers(name string, rest []string) error {
	if name == "npx" || name == "bunx" || name == "pnpx" ||
		((name == "pnpm" || name == "npm" || name == "yarn") && anyOf(rest, func(x string) bool { return x == "dlx" || x == "exec" })) {
		if anyOf(rest, func(x string) bool { return strings.Contains(x, ClaudePkg) }) {
			return &ClaudeBlock{Msg: name + " of " + ClaudePkg}
		}
	} else if name == "node" || name == "nodejs" || name == "bun" || name == "deno" {
		if anyOf(rest, func(x string) bool { return claudeCLI(x) || strings.Contains(x, ClaudePkg) }) {
			return &ClaudeBlock{Msg: name + " running the Claude Code cli"}
		}
	}
	return nil
}

// loose is a port of Analyzer.loose.
func (a *Analyzer) loose(rest []string, depth int, envAssigns []string) error {
	for k := range rest {
		whole := true
		if IsAssign(rest[k]) {
			v := strings.TrimLeft(rest[k][strings.IndexByte(rest[k], '=')+1:], "\\")
			if IsHerdr(v) {
				return blockf("herdr assigned as a command (alias/env)")
			}
			if IsClaude(v) {
				return &ClaudeBlock{Msg: "claude assigned as a command (alias/env)"}
			}
			if v != "" {
				// the value may be a whole command line (alias c="claude -p", alias c="ls;claude",
				// alias c='"claude"'): judge it like a top-level command, quotes and backslashes removed
				if err := a.script(v, depth+1); err != nil {
					return err
				}
				whole = false
			}
		}
		if whole && hasSpace(rest[k]) {
			if err := a.script(rest[k], depth+1); err != nil {
				return err
			}
		}
		env := append([]string(nil), envAssigns...)
		for _, x := range rest[:k] {
			if strings.HasPrefix(x, "FACTORY_HOME=") {
				env = append(env, x)
			}
		}
		if err := a.cmd(rest[k:], depth, false, true, nil, env); err != nil {
			return err
		}
	}
	return nil
}

// xargs is a port of Analyzer.xargs.
func xargs(rest []string) error {
	repl, replSet := "", false
	first, firstSet := "", false
	for j := 0; j < len(rest); {
		x := rest[j]
		if x == "-I" || x == "-J" {
			repl, replSet = "", false
			if j+1 < len(rest) {
				repl, replSet = rest[j+1], true
			}
			j += 2
			continue
		}
		if strings.HasPrefix(x, "-I") && len(x) > 2 {
			repl, replSet = x[2:], true
		} else if x == "-i" || x == "--replace" {
			repl, replSet = "{}", true
		} else if v, ok := strings.CutPrefix(x, "--replace="); ok {
			repl, replSet = v, true
		} else if !strings.HasPrefix(x, "-") {
			first, firstSet = x, true
			break
		}
		j++
	}
	if replSet && firstSet && first == repl {
		return blockf("xargs replacement string used as the command word")
	}
	return nil
}

// shell is a port of Analyzer.shell.
func (a *Analyzer) shell(rest []string, depth int, piped bool, hstrs []string) error {
	i := 0
loop:
	for i < len(rest) {
		x := rest[i]
		switch {
		case x == "--":
			i++
			break loop
		case strings.HasPrefix(x, "-") && len(x) > 1:
			if !strings.HasPrefix(x, "--") && strings.Contains(x[1:], "c") {
				if i+1 >= len(rest) {
					return blockf("shell -c without a payload: content cannot be inspected")
				}
				return a.script(rest[i+1], depth+1)
			}
			if x == "-o" || x == "-O" || x == "--rcfile" || x == "--init-file" {
				i++
			}
			i++
		default:
			break loop
		}
	}
	for _, h := range hstrs {
		if err := a.script(h, depth+1); err != nil {
			return err
		}
	}
	switch {
	case i < len(rest):
		return a.file(rest[i], depth)
	case len(hstrs) > 0:
		return nil
	case piped:
		return blockf("shell reading a pipe: content cannot be inspected")
	}
	return nil
}

// read is a port of Analyzer.read: the text of a script file (universal newlines, undecodable bytes replaced).
func (a *Analyzer) read(path string) (string, error) {
	p := expandUser(path)
	if !strings.HasPrefix(p, "/") {
		p = pyJoin(a.Cwd, p)
	}
	st, err := os.Stat(p)
	if err != nil || !st.Mode().IsRegular() || st.Size() > 1<<20 {
		return "", blockf("cannot inspect script " + path)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", blockf("cannot inspect script " + path)
	}
	s := replaceInvalidUTF8(b)
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n"), nil
}

// file is a port of Analyzer.file.
func (a *Analyzer) file(path string, depth int) error {
	if IsDynamic(path, true) {
		return blockf("dynamic script path " + pyRepr(path))
	}
	text, err := a.read(path)
	if err != nil {
		return err
	}
	return a.script(text, depth+1)
}

// execPath is a port of Analyzer.exec_path: a path run as a command whose first line is a sh-family shebang is read.
func (a *Analyzer) execPath(path string, depth int) error {
	p := expandUser(path)
	if !strings.HasPrefix(p, "/") {
		p = pyJoin(a.Cwd, p)
	}
	st, err := os.Stat(p)
	if err != nil || !st.Mode().IsRegular() {
		return nil
	}
	f, err := os.Open(p)
	if err != nil {
		return blockf("cannot inspect script " + path)
	}
	defer f.Close()
	head := make([]byte, 200)
	n, err := io.ReadFull(f, head)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return blockf("cannot inspect script " + path)
	}
	first := string(head[:n])
	if k := strings.IndexByte(first, '\n'); k >= 0 {
		first = first[:k]
	}
	if strings.HasPrefix(first, "#!") && shShebang(first) {
		text, err := a.read(path)
		if err != nil {
			return err
		}
		return a.script(text, depth+1)
	}
	if a.noPush() && !strings.HasPrefix(string(head[:n]), "\x7fELF") {
		if strings.HasPrefix(first, "#!") {
			return a.gitDeny(path + " is a script for another interpreter, whose body this hook does not inspect")
		}
		text, err := a.read(path) // no shebang: the kernel hands it to sh, so read it as sh
		if err != nil {
			return err
		}
		return a.script(text, depth+1)
	}
	return nil
}

// shShebang is re.search(rb"\b(ba|z|da|k|a)?sh\b", first) with the ASCII \b of a bytes pattern.
func shShebang(s string) bool {
	ab := func(i int) bool { return i >= 0 && i < len(s) && asciiWord(rune(s[i])) }
	edge := func(i int) bool { return ab(i-1) != ab(i) }
	for i := 0; i < len(s); i++ {
		if !edge(i) {
			continue
		}
		for _, pre := range []string{"ba", "z", "da", "k", "a", ""} {
			if strings.HasPrefix(s[i:], pre+"sh") && edge(i+len(pre)+2) {
				return true
			}
		}
	}
	return false
}

// systemctl is a port of Analyzer.systemctl.
func (a *Analyzer) systemctl(rest []string) error {
	args := filterStr(rest, func(x string) bool { return !strings.HasPrefix(x, "-") })
	if containsStr(args, "isolate") {
		return blockf("systemctl isolate")
	}
	var verbs, units []string
	for _, x := range args {
		if in(SystemctlVerbs, x) {
			verbs = append(verbs, x)
		} else {
			units = append(units, x)
		}
	}
	if len(verbs) == 0 {
		return nil
	}
	if in(SystemctlNoUnit, verbs[0]) {
		return blockf("systemctl " + verbs[0])
	}
	if len(units) == 0 {
		return blockf("systemctl " + verbs[0] + " without a unit")
	}
	for _, u := range units {
		if strings.HasPrefix(u, "factory") || strings.HasPrefix(u, "herdr") || strings.ContainsAny(u, "*?[") ||
			strings.Contains(u, "$") || strings.Contains(u, "`") {
			return blockf("systemctl " + verbs[0] + " " + u)
		}
	}
	return nil
}

// ───────────────────────────── check ─────────────────────────────

// R2PathHit is a port of worker-guard.py:r2_path_hit.
func R2PathHit(cmd string) bool {
	for _, s := range Segments(cmd) {
		if len(s.Words) == 0 || !in(DataCmds, pyBasename(s.Words[0])) {
			if hasR2Path(RuneSlice(cmd, s.Start, s.End)) {
				return true
			}
		} else if hasR2Path(strings.Join(s.Words, " ")) { // unquoted occurrences only
			return true
		}
	}
	return false
}

func hasR2Path(s string) bool {
	return strings.Contains(s, "herdr.sock") || strings.Contains(s, ".config/herdr")
}

// claudeOverPriv is a port of worker-guard.py:claude_over_priv: the Claude block of the worker pass with the
// PRIV rule switched off, or nil. A DynamicBlock and every other error are dropped; the caller re-raises the PrivBlock.
func claudeOverPriv(cmd, cwd, role string) error {
	an, err := NewAnalyzer(cwd, role, false)
	if err != nil {
		return err // fail closed: a block, never "no claude block found"
	}
	an.skipPriv = true
	err = an.script(cmd, 0)
	var d *DynamicBlock
	if errors.As(err, &d) {
		return nil
	}
	var c *ClaudeBlock
	if errors.As(err, &c) {
		return err
	}
	return nil
}

// evaluatorHomeText is a port of worker-guard.py:evaluator_home_text.
func evaluatorHomeText(cmd string) error {
	if !strings.Contains(cmd, "FACTORY_HOME") {
		return nil
	}
	if evalHomePrefix(cmd) && strings.Count(cmd, "FACTORY_HOME") == 1 {
		return nil
	}
	return blockf("the evaluator may not mention FACTORY_HOME except as one leading prefix " +
		"`FACTORY_HOME=<temp dir> factory <cmd>`; it cannot re-target or unset it")
}

// evalHomePrefix is EVAL_HOME_PREFIX.match(cmd) (MatchEvalHomePrefix, lookaround.go).
func evalHomePrefix(cmd string) bool { return MatchEvalHomePrefix(cmd) }

// pyLines is str.splitlines().
func pyLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == '\n' || r == '\r' || r == '\v' || r == '\f' || r == 0x1c || r == 0x1d || r == 0x1e || r == 0x85 || r == 0x2028 || r == 0x2029 {
			out = append(out, s[start:i])
			i += size
			if r == '\r' && i < len(s) && s[i] == '\n' {
				i++
			}
			start = i
			continue
		}
		i += size
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// replaceInvalidUTF8 is Python's errors="replace": every undecodable byte becomes one U+FFFD (strings.ToValidUTF8
// collapses a run of them into one).
func replaceInvalidUTF8(b []byte) string {
	var sb strings.Builder
	for len(b) > 0 {
		r, n := utf8.DecodeRune(b)
		if r == utf8.RuneError && n <= 1 {
			sb.WriteRune(utf8.RuneError)
			b = b[badUTF8Len(b):]
			continue
		}
		sb.Write(b[:n])
		b = b[n:]
	}
	return sb.String()
}

// badUTF8Len is the length of the maximal ill-formed subpart at the start of b: Python's errors="replace" writes ONE
// U+FFFD for a truncated or otherwise invalid multi-byte sequence, not one per byte.
func badUTF8Len(b []byte) int {
	need, lo, hi := 1, byte(0x80), byte(0xBF)
	switch c := b[0]; {
	case c >= 0xC2 && c <= 0xDF:
		need = 2
	case c == 0xE0:
		need, lo = 3, 0xA0
	case c == 0xED:
		need, hi = 3, 0x9F
	case c >= 0xE1 && c <= 0xEF:
		need = 3
	case c == 0xF0:
		need, lo = 4, 0x90
	case c >= 0xF1 && c <= 0xF3:
		need = 4
	case c == 0xF4:
		need, hi = 4, 0x8F
	default:
		return 1
	}
	n := 1
	for n < need && n < len(b) && b[n] >= lo && b[n] <= hi {
		n++
		lo, hi = 0x80, 0xBF
	}
	return n
}
