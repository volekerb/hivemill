package guard

import (
	"regexp"
	"strings"
)

// T-1498: the Go toolchain for workers (port of tc_kind and Analyzer.go_toolchain of worker-guard.py). coder, qa and
// reviewer may EXECUTE ~/.local/go/bin/go and gofmt; the tree stays protected. A fixed subcommand list, a fixed
// env-prefix list, no network, no write flag into a protected tree.

var tcRoles = set("coder", "qa", "reviewer")
var tcSubs = set("build", "test", "vet", "run", "list", "env", "version", "fmt", "mod")
var tcModSubs = set("verify", "graph", "why")
var tcWriteFlags = set("o", "coverprofile", "cpuprofile", "memprofile", "blockprofile", "mutexprofile", "trace", "outputdir", "modfile")
var tcEnvNames = set("GOTOOLCHAIN", "GOFLAGS", "GOPROXY", "GOCACHE", "GOMODCACHE", "CGO_ENABLED", "PATH")
var reTcHomeWord = regexp.MustCompile(`^\$(?:HOME|\{HOME\})/\.local/go/bin/(?:go|gofmt)$`)
var reTcFlag = regexp.MustCompile(`^--?([A-Za-z-]+)(=(.*))?$`)
var reTcW = regexp.MustCompile(`^--?w(=true)?$`)

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
			"fmt and mod verify|graph|why only (docs/worker-guard-go.md, Go toolchain for workers)")
	}
	if lookalike {
		return block("the path is not in its normal form (`..`, `//` or `/./`)")
	}
	args := dropDashDash(rest)
	flagHits := func(words []string) error {
		for k := 0; k < len(words); {
			m := reTcFlag.FindStringSubmatch(words[k])
			k++
			if m == nil {
				continue
			}
			if in(tcWriteFlags, m[1]) {
				v, has := m[3], m[2] != ""
				if !has && k < len(words) {
					v, has = words[k], true
				}
				if has && v != "" && a.protHit(v, false, a.ccwd, a.ccwdKnown) {
					return block("-" + m[1] + " names a protected tree")
				}
			} else if m[2] != "" && m[3] != "" && a.protHit(m[3], false, a.ccwd, a.ccwdKnown) {
				return block("a flag value names a protected tree")
			}
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
		if name == "GOFLAGS" {
			if err := flagHits(strings.Fields(val)); err != nil {
				return err
			}
		}
		env[name] = val
	}
	if tool == "gofmt" {
		write := false
		for _, x := range args {
			if reTcW.MatchString(x) {
				write = true
			}
		}
		if !write {
			return nil
		}
		var targets []string
		for _, x := range args {
			if !strings.HasPrefix(x, "-") {
				targets = append(targets, x)
			}
		}
		if len(targets) == 0 || !a.ccwdKnown {
			return block("-w needs file arguments and a known working directory")
		}
		root := pyRealpath(a.ccwd)
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
	sub, k := "", 0
	for k < len(args) {
		if args[k] == "-C" || args[k] == "--C" {
			k++
		} else if !strings.HasPrefix(args[k], "-") {
			sub = args[k]
			break
		}
		k++
	}
	if !in(tcSubs, sub) {
		return block("`go " + sub + "` is not allowed")
	}
	if sub == "mod" {
		ms := ""
		if k+1 <= len(args) {
			for _, x := range args[k+1:] {
				if !strings.HasPrefix(x, "-") {
					ms = x
					break
				}
			}
		}
		if !in(tcModSubs, ms) {
			return block("`go mod " + ms + "` is not allowed (network or a write)")
		}
	}
	modMod := false
	for _, x := range append(append([]string(nil), args...), strings.Fields(env["GOFLAGS"])...) {
		if x == "-mod=mod" {
			modMod = true
		}
	}
	if modMod && env["GOPROXY"] != "off" {
		return block("-mod=mod needs the GOPROXY=off prefix")
	}
	return flagHits(args)
}
