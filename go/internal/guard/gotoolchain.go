package guard

import (
	"regexp"
	"strings"
)

// T-1498: the Go toolchain for workers (port of tc_kind and Analyzer.go_toolchain of worker-guard.py). coder, qa and
// reviewer may EXECUTE ~/.local/go/bin/go and gofmt; the tree stays protected. A fixed subcommand list, a fixed
// env-prefix list, no network, no write flag into a protected tree.

var tcRoles = set("coder", "qa", "reviewer")
var tcModSubs = set("verify", "graph", "why")

// tcPathFlags are the flags whose value is a file or directory that must stay in the caller's worktree or scratchpad.
var tcPathFlags = set("o", "coverprofile", "cpuprofile", "memprofile", "blockprofile", "mutexprofile", "trace", "outputdir")

// tcFlags is the strict flag allow-list per subcommand (default deny): bool flags and value flags. A -test.NAME spelling
// is the same flag. Same table as TC_FLAGS of worker-guard.py.
type tcTable struct{ bools, vals map[string]struct{} }

func tcUnion(ms ...map[string]struct{}) map[string]struct{} {
	r := map[string]struct{}{}
	for _, m := range ms {
		for k := range m {
			r[k] = struct{}{}
		}
	}
	return r
}

var tcBBool, tcBVal = set("race", "v", "trimpath"), set("tags", "mod", "p", "buildvcs", "C")
var tcTBool = set("short", "cover", "failfast", "json", "benchmem", "c")
var tcTVal = tcUnion(set("run", "skip", "count", "timeout", "bench", "benchtime", "cpu", "parallel", "shuffle", "coverpkg", "covermode", "list",
	"fuzz", "fuzztime"), tcPathFlags)
var tcSubFlags = map[string]tcTable{
	"build":   {tcBBool, tcUnion(tcBVal, set("o"))},
	"run":     {tcBBool, tcBVal},
	"vet":     {set("race", "v"), set("tags", "mod", "C")},
	"test":    {tcUnion(tcBBool, tcTBool), tcUnion(tcBVal, tcTVal)},
	"list":    {set("json", "m", "deps", "e", "test", "race", "v"), set("f", "tags", "mod", "C")},
	"env":     {set("json", "changed"), set("C")},
	"version": {set("m", "v"), set("C")},
	"fmt":     {set("n", "x"), set("C")},
	"mod":     {set("m", "vendor"), set("C")},
}
var tcGofmtFlags = tcTable{set("l", "d", "s", "e", "w"), set("r")}
var tcGoflagsFlags = func() tcTable {
	t := tcTable{map[string]struct{}{}, map[string]struct{}{}}
	for _, s := range tcSubFlags {
		t.bools = tcUnion(t.bools, s.bools)
		t.vals = tcUnion(t.vals, s.vals)
	}
	delete(t.vals, "C")
	return t
}()

type tcFlag struct {
	name, val string
	hasVal    bool
}

// tcProfileFlags are the flags that make `go test` keep pkg.test in the cwd like -c (testNeedBinary, cmd/go test.go:632,:1269).
var tcProfileFlags = set("cpuprofile", "memprofile", "blockprofile", "mutexprofile", "trace", "coverprofile")

var reTcParse = regexp.MustCompile(`(?s)^--?([^=\s-][^=\s]*)(=(.*))?$`)

// tcExpands is tc_expands: true when the shell could expand or split w (a `$`, a backtick, a glob character, a brace, or a
// `~` that is not the whole word or a `~/` prefix). Words arrive dequoted, so a quoted literal is refused too (default deny).
func tcExpands(w string) bool {
	return strings.ContainsAny(w, "$`*?[{") || (strings.Contains(w, "~") && w != "~" && (!strings.HasPrefix(w, "~/") || strings.Contains(w[1:], "~")))
}

// tcParse is tc_parse: the flags and positionals of words against tbl. An error names the first word that is not in the
// table (including -args) or a value flag with no value. A value flag consumes the next word whatever it looks like.
// A value flag consumes the next word including `--` (flag.go does); a bare `--` at flag position ends the flags
// (ddEnd: the rest are positionals) or is refused (!ddEnd: `go test` hands everything after it to the test binary, GOFLAGS refuses it).
func tcParse(words []string, tbl tcTable, ddEnd bool) ([]tcFlag, []string, string) {
	var flags []tcFlag
	var pos []string
	for _, w := range words {
		if tcExpands(w) {
			return nil, nil, w + " (an expansion or glob in a word cannot be classified)"
		}
	}
	for k := 0; k < len(words); {
		w := words[k]
		k++
		if w == "--" {
			if !ddEnd {
				return nil, nil, w + " (a terminator is not allowed here)"
			}
			pos = append(pos, words[k:]...)
			break
		}
		m := reTcParse.FindStringSubmatch(w)
		if m == nil {
			if strings.HasPrefix(w, "-") && w != "-" {
				return nil, nil, w
			}
			pos = append(pos, w)
			continue
		}
		name := strings.TrimPrefix(m[1], "test.")
		switch {
		case in(tbl.bools, name):
			flags = append(flags, tcFlag{name, m[3], m[2] != ""})
		case in(tbl.vals, name):
			if m[2] != "" {
				flags = append(flags, tcFlag{name, m[3], true})
			} else if k < len(words) {
				flags = append(flags, tcFlag{name, words[k], true})
				k++
			} else {
				return nil, nil, w + " (no value)"
			}
		default:
			return nil, nil, w
		}
	}
	return flags, pos, ""
}

var tcEnvNames = set("GOTOOLCHAIN", "GOFLAGS", "GOPROXY", "GOCACHE", "GOMODCACHE", "CGO_ENABLED", "PATH")
var reTcHomeWord = regexp.MustCompile(`^\$(?:HOME|\{HOME\})/\.local/go/bin/(?:go|gofmt)$`)

func tcPathValue(v string) bool {
	h := mustHost().pwDir
	for _, p := range []string{"~", "$HOME", "${HOME}", h} {
		for _, q := range []string{"$PATH", "${PATH}"} {
			if v == p+"/.local/go/bin:"+q {
				return true
			}
		}
	}
	return false
}

// tcKind is tc_kind: ("go"|"gofmt", lookalike) when w names the toolchain binary; lookalike is true when the spelling
// is not its own normal form (`..`, `//`, `/./`).
func tcKind(w string) (string, bool, bool) {
	pw := mustHost().pwDir
	e := w
	for _, pre := range []string{"~/", "$HOME/", "${HOME}/"} {
		if strings.HasPrefix(w, pre) {
			e = pw + "/" + w[len(pre):]
			break
		}
	}
	if !strings.HasPrefix(e, "/") || strings.ContainsAny(e, "$`{*?[") {
		return "", false, false
	}
	n := pyNormpath(e)
	for _, tool := range []string{"go", "gofmt"} {
		if n == pyJoin(pyJoin(pyJoin(pyJoin(pw, ".local"), "go"), "bin"), tool) {
			return tool, e != n, true
		}
	}
	return "", false, false
}

func (a *Analyzer) goToolchain(tool string, lookalike bool, rest, assigns []string) error {
	block := func(what string) error {
		return blockf("~/.local/go/bin/" + tool + ": " + what + "; the toolchain may be run with build, test, vet, run, list, env, version, " +
			"fmt and mod verify|graph|why only, with the flags of its allow-list (docs/worker-guard-go.md, Go toolchain for workers)")
	}
	if lookalike {
		return block("the path is not in its normal form (`..`, `//` or `/./`)")
	}
	args := append([]string(nil), rest...) // N7: no pre-strip of `--`; tcParse decides (a value flag consumes it)
	root := ""
	if a.ccwdKnown {
		root = pyRealpath(a.ccwd)
	}
	// safePath: a plain path (a file for -o and the profiles, a directory for -C) in the worktree or scratchpad.
	safePath := func(v, fbase string, fbaseKnown bool, what string) error {
		v = a.cfgExpand(v)
		if v == "" || strings.ContainsAny(v, "$`*?[{~") || !fbaseKnown {
			return block(what + " needs a plain path and a known working directory")
		}
		if v == "/dev/null" && what != "-C" {
			return nil
		}
		real := pyRealpath(pyJoin(fbase, v))
		if a.protHit(v, false, fbase, fbaseKnown) || !((root != "" && Under(real, root)) || inScratchpad(real)) {
			return block(what + " " + v + " is outside your worktree and scratchpad")
		}
		return nil
	}
	env := map[string]string{}
	for _, x := range assigns {
		name, val, _ := strings.Cut(x, "=")
		if !in(tcEnvNames, name) {
			return block("the env prefix " + name + "= is not allowed (GOTOOLCHAIN, GOFLAGS, GOPROXY, GOCACHE, GOMODCACHE, CGO_ENABLED, PATH)")
		}
		if (name == "GOTOOLCHAIN" && val != "local") || (name == "GOPROXY" && val != "off") ||
			(name == "CGO_ENABLED" && val != "0") || (name == "PATH" && !tcPathValue(val)) {
			return block("the env prefix " + name + "=" + val + " is not an allowed value")
		}
		if name != "PATH" && tcExpands(val) { // PATH has its exact allowed value above; every other value is default-deny like a word
			return block("the env prefix " + name + "= holds an expansion or glob that cannot be classified")
		}
		if (name == "GOCACHE" || name == "GOMODCACHE") && a.protHit(val, false, a.ccwd, a.ccwdKnown) {
			exp := val
			if strings.HasPrefix(val, "~/") {
				exp = mustHost().pwDir + val[1:]
			}
			modcache := pyJoin(pyJoin(pyJoin(pyJoin(pyJoin(mustHost().pwDir, ".local"), "lib"), "factory"), "go"), "mod")
			if !(name == "GOMODCACHE" && Under(pyNormpath(exp), modcache)) {
				return block(name + " names a protected tree")
			}
		}
		env[name] = val
	}
	if gm, ok := env["GOMODCACHE"]; ok && env["GOPROXY"] != "off" {
		exp := gm
		if strings.HasPrefix(gm, "~/") {
			exp = mustHost().pwDir + gm[1:]
		}
		if Under(pyNormpath(exp), pyJoin(pyJoin(pyJoin(pyJoin(pyJoin(mustHost().pwDir, ".local"), "lib"), "factory"), "go"), "mod")) {
			return block("GOMODCACHE in the factory lib needs the GOPROXY=off prefix (a module download would write there)")
		}
	}
	parse := func(words []string, tbl tcTable, ddEnd bool) ([]tcFlag, []string, error) {
		f, p, bad := tcParse(words, tbl, ddEnd)
		if bad != "" {
			return nil, nil, block(bad + " is not in the flag allow-list")
		}
		return f, p, nil
	}
	gflags, gpos, err := parse(strings.Fields(env["GOFLAGS"]), tcGoflagsFlags, false)
	if err != nil {
		return err
	}
	if len(gpos) > 0 {
		return block("GOFLAGS holds a word that is not a flag")
	}
	checkFlags := func(flags []tcFlag, base string, baseKnown bool) error {
		for _, f := range flags {
			switch {
			case in(tcPathFlags, f.name):
				if err := safePath(f.val, base, baseKnown, "-"+f.name); err != nil {
					return err
				}
			case f.name == "mod":
				if f.val != "readonly" && f.val != "vendor" && !(f.val == "mod" && env["GOPROXY"] == "off") {
					return block("-mod needs readonly or vendor (mod only with the GOPROXY=off prefix)")
				}
			case f.val != "" && f.name != "C" && a.protHit(f.val, false, base, baseKnown):
				return block("-" + f.name + " value names a protected tree")
			}
		}
		return nil
	}
	if tool == "gofmt" {
		flags, targets, err := parse(args, tcGofmtFlags, true)
		if err != nil {
			return err
		}
		if err := checkFlags(append(append([]tcFlag(nil), gflags...), flags...), a.ccwd, a.ccwdKnown); err != nil {
			return err
		}
		write := false
		for _, f := range flags {
			if f.name == "w" && (!f.hasVal || !in(set("0", "f", "F", "FALSE", "false", "False"), f.val)) {
				write = true
			}
		}
		if !write {
			return nil
		}
		if len(targets) == 0 || !a.ccwdKnown {
			return block("-w needs file arguments and a known working directory")
		}
		for _, t := range targets {
			if strings.ContainsAny(t, "$`*?[{~") {
				return block("-w with a glob or an expansion")
			}
			real := pyRealpath(pyJoin(a.ccwd, t))
			if a.protHit(t, false, a.ccwd, a.ccwdKnown) || !(Under(real, root) || inScratchpad(real)) {
				return block("-w outside your worktree and scratchpad")
			}
		}
		return nil
	}
	sub, k, found := "", 0, false
	var pre []tcFlag
	for k < len(args) && !found {
		w := args[k]
		switch {
		case w == "-C" || w == "--C":
			if k+1 >= len(args) {
				return block("-C needs a value")
			}
			pre = append(pre, tcFlag{"C", args[k+1], true})
			k += 2
		case strings.HasPrefix(w, "-C=") || strings.HasPrefix(w, "--C="):
			_, v, _ := strings.Cut(w, "=")
			pre = append(pre, tcFlag{"C", v, true})
			k++
		case strings.HasPrefix(w, "-") && w != "-":
			return block(w + " before the subcommand (only -C may come there)")
		default:
			sub, found = w, true
		}
	}
	tbl, ok := tcSubFlags[sub]
	if !found || sub == "" || !ok {
		return block("`go " + sub + "` is not allowed")
	}
	flags, pos, err := parse(args[k+1:], tbl, sub != "test")
	if err != nil {
		return err
	}
	flags = append(pre, flags...)
	var cdirs []string
	for _, f := range flags { // the working directory is decided by the parsed -C flags only
		if f.name == "C" {
			if err := safePath(f.val, a.ccwd, a.ccwdKnown, "-C"); err != nil {
				return err
			}
			cdirs = append(cdirs, f.val)
		}
	}
	base, baseKnown := a.ccwd, a.ccwdKnown
	if len(cdirs) > 0 {
		c := a.cfgExpand(cdirs[len(cdirs)-1])
		if strings.HasPrefix(c, "/") {
			base, baseKnown = c, true
		} else if a.ccwdKnown {
			base = pyJoin(a.ccwd, c)
		} else {
			baseKnown = false
		}
	}
	allf := append(append([]tcFlag(nil), gflags...), flags...)
	if err := checkFlags(allf, base, baseKnown); err != nil {
		return err
	}
	if sub == "mod" && (len(pos) == 0 || !in(tcModSubs, pos[0])) {
		ms := ""
		if len(pos) > 0 {
			ms = pos[0]
		}
		return block("`go mod " + ms + "` is not allowed (network or a write)")
	}
	hasFuzz := false
	for _, f := range allf {
		if f.name == "fuzz" {
			hasFuzz = true
		}
	}
	if env["GOPROXY"] != "off" { // N12: pkg@version (go run x@latest, go list -m x@v) fetches a module
		for _, x := range pos {
			if strings.Contains(x, "@") {
				return block("`go " + sub + "` with a module@version argument needs the GOPROXY=off prefix (it downloads the module)")
			}
		}
	}
	if sub == "fmt" || hasFuzz {
		for _, x := range pos {
			if !strings.HasPrefix(x, ".") && !strings.HasPrefix(x, "/") {
				return block("`go " + sub + "` (it rewrites or creates files in the package directory) takes directory paths (./x, ../x, /x), not import paths")
			}
		}
	}
	if sub == "test" && hasFuzz { // a failing fuzz run writes testdata/ into the package directory
		if len(pos) == 0 && baseKnown && protPath(base, false) {
			return block("`go test -fuzz` in a protected tree")
		}
		for _, x := range pos {
			if a.protHit(x, false, base, baseKnown) {
				return block("`go test -fuzz` on a protected tree")
			}
		}
	}
	if sub == "fmt" { // `go fmt` rewrites files in place, like `gofmt -w`: the same worktree-or-scratchpad rule
		if !baseKnown {
			return block("`go fmt` needs a known working directory")
		}
		if protPath(base, false) {
			return block("`go fmt` in a protected tree")
		}
		if len(pos) == 0 {
			rb := pyRealpath(base)
			if !((root != "" && Under(rb, root)) || inScratchpad(rb)) {
				return block("`go fmt` with no package argument formats the -C directory, which is outside your worktree and scratchpad")
			}
		}
		for _, t := range pos {
			if strings.ContainsAny(t, "$`*?[{~") {
				return block("`go fmt` with a glob or an expansion")
			}
			real := pyRealpath(pyJoin(base, t))
			if a.protHit(t, false, base, baseKnown) || !((root != "" && Under(real, root)) || inScratchpad(real)) {
				return block("`go fmt` outside your worktree and scratchpad")
			}
		}
		return nil
	}
	hasC, hasO := false, false
	for _, f := range allf {
		if f.name == "c" && (!f.hasVal || !in(set("0", "f", "F", "FALSE", "false", "False"), f.val)) {
			hasC = true
		}
		if sub == "test" && in(tcProfileFlags, f.name) { // N8: the profile flags keep pkg.test in the cwd like -c
			hasC = true
		}
		if f.name == "o" {
			hasO = true
		}
	}
	if (sub == "build" || (sub == "test" && hasC)) && !hasO && baseKnown && protPath(base, false) { // the binary lands in the working directory
		return block("`go " + sub + "` without -o would write its output into a protected tree (use -o into your worktree)")
	}
	return nil
}
