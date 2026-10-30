package guard

import (
	"os"
	"strings"
)

// repoCfgWhy is worker-guard.py:REPO_CFG_WHY.
const repoCfgWhy = "every worktree shares the repo's .git/config, so a key such as filter.<x>.smudge, diff.<x>.textconv, " +
	"core.hooksPath, core.fsmonitor or core.sshCommand written from a worktree would run as every agent's git; " +
	"use `git config --file <file in your scratchpad>` or a clone inside your scratchpad. This guard is " +
	"accident prevention, not a security boundary (T-0226)"

// braceExpand is a port of brace_expand: the leftmost {a,b} group (no nested braces, at least one comma) is
// expanded, then the rest again.
func braceExpand(t string) []string {
	for i := 0; i < len(t); i++ {
		if t[i] != '{' {
			continue
		}
		end := -1
		for j := i + 1; j < len(t); j++ {
			if t[j] == '{' {
				break
			}
			if t[j] == '}' {
				end = j
				break
			}
		}
		if end < 0 || !strings.Contains(t[i+1:end], ",") {
			continue
		}
		var out []string
		for _, alt := range strings.Split(t[i+1:end], ",") {
			out = append(out, braceExpand(t[:i]+alt+t[end+1:])...)
		}
		return out
	}
	return []string{t}
}

// repoCfgPath is a port of repo_cfg_path (T-0226): c, a normalised absolute path, is repo git config outside the
// worker's scratchpad: <x>/.git/config*, <x>/.git/worktrees/<n>/config*, <x>/.git/info/attributes, or the same
// files of a bare repo (HEAD and objects/ beside). T-0770: scratchOK=false (no-push roles) drops the scratchpad
// exemption: a scratchpad repo's config runs as git too.
func repoCfgPath(c string, scratchOK bool) bool {
	if scratchOK && inScratchpad(c) {
		return false
	}
	parts := strings.Split(c, "/")
	base, parent := parts[len(parts)-1], ""
	if len(parts) > 1 {
		parent = parts[len(parts)-2]
	}
	if strings.HasPrefix(base, "config") && (parent == ".git" || (len(parts) > 3 && parts[len(parts)-3] == "worktrees" && parts[len(parts)-4] == ".git")) {
		return true
	}
	if base == "attributes" && parent == "info" && len(parts) > 2 && parts[len(parts)-3] == ".git" {
		return true
	}
	var gd string
	switch {
	case base == "config" || base == "config.worktree":
		gd = strings.Join(parts[:len(parts)-1], "/")
	case base == "attributes" && parent == "info":
		gd = strings.Join(parts[:len(parts)-2], "/")
	default:
		return false
	}
	return gd != "" && isFile(pyJoin(gd, "HEAD")) && isDir(pyJoin(gd, "objects"))
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func hasGlob(s string) bool { return strings.ContainsAny(s, "*?[") }

// persistPath is a port of persist_path: p (absolute; `..` and symlinks resolved, glob patterns matched) is a
// protected file or inside a protected tree.
func persistPath(p string, ancestor, scratchOK bool) bool {
	h := mustHost()
	for _, t := range braceExpand(p) {
		cands := []string{pyNormpath(t)}
		if !hasGlob(t) {
			cands = append(cands, pyRealpath(t))
		}
		for _, c := range cands {
			if _, ok := h.persistAnc[c]; ancestor && ok {
				return true
			}
			if repoCfgPath(c, scratchOK) {
				return true
			}
			if hasGlob(c) {
				parts := strings.Split(strings.Trim(c, "/"), "/")
				for f := range h.persistFiles {
					if fnmatchCase(f, c) {
						return true
					}
				}
				for _, d := range h.persistTrees {
					dp := strings.Split(strings.Trim(d, "/"), "/")
					if len(parts) < len(dp) {
						continue
					}
					all := true
					for k := range dp {
						if !fnmatchCase(dp[k], parts[k]) {
							all = false
							break
						}
					}
					if all {
						return true
					}
				}
				continue
			}
			if _, ok := h.persistFiles[c]; ok {
				return true
			}
			for _, d := range h.persistTrees {
				if c == d || Under(c, d) {
					return true
				}
			}
		}
	}
	return false
}

// substVars is re.sub(r"\$\{?(\w+)\}?", lambda m: vars.get(m.group(1), m.group(0)), s).
func substVars(s string, vars map[string]string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '$' {
			b.WriteByte(s[i])
			i++
			continue
		}
		j := i + 1
		if j < len(s) && s[j] == '{' {
			j++
		}
		k := j
		for k < len(s) && wordAt(s, k) {
			k += runeLen(s, k)
		}
		if k == j {
			b.WriteByte('$')
			i++
			continue
		}
		name := s[j:k]
		if k < len(s) && s[k] == '}' {
			k++
		}
		if v, ok := vars[name]; ok {
			b.WriteString(v)
		} else {
			b.WriteString(s[i:k])
		}
		i = k
	}
	return b.String()
}

// persistHit is a port of Analyzer.persist_hit (T-0334): word (or the value of --opt=value / of=value) names a
// protected persistence path. A word with an expansion we cannot resolve counts when it spells a protected name.
func (a *Analyzer) persistHit(word string, ancestor, gitfile bool) bool {
	h := mustHost()
	for _, w := range uniq([]string{word, valueOf(word)}) {
		t := cfgExpand(substVars(w, a.vars))
		if strings.ContainsAny(t, "$`") {
			// an expansion we cannot resolve: refuse when it spells a protected name or sits under the home
			if PersistCode(t) || strings.HasPrefix(t, "~") || strings.HasPrefix(t, h.pwDir) || strings.Contains(t, "~") || strings.Contains(t, "HOME") {
				return true
			}
			continue
		}
		if t == "" {
			continue
		}
		if !strings.HasPrefix(t, "/") {
			if !a.ccwdKnown {
				if PersistCode(t) {
					return true
				}
				continue
			}
			t = pyJoin(a.ccwd, t)
		}
		noPush := a.noPush() // T-0770: no scratchpad exemption for git config/hooks
		if persistPath(t, ancestor, !noPush) || (noPush && gitHooksPath(t, false)) {
			return true
		}
		if noPush && gitfile && gitfilePath(t) { // T-0783: a write target named .git
			return true
		}
	}
	return false
}

// persistRedirect is a port of Analyzer.persist_redirect.
func (a *Analyzer) persistRedirect(target string) error {
	if a.persistHit(target, false, true) {
		return blockf("a redirect into a protected config/init file: " + persistWhy)
	}
	return nil
}

// persistWrite is a port of Analyzer.persist_write (T-0334): refuse Bash writes to ~/.gitconfig, ~/.config/git/**,
// /etc/gitconfig and the shell init files. Reads (cat, grep) stay allowed.
func (a *Analyzer) persistWrite(name string, rest []string) error {
	var args []string
	for _, x := range rest {
		if x != "--" {
			args = append(args, x)
		}
	}
	switch {
	case in(CfgWriters, name):
		destOnly := name == "cp" || name == "install" || name == "rsync" || name == "dd"
		if name == "ln" {
			for _, x := range args {
				if x == "--symbolic" || dashLetters(x, "s") {
					destOnly = true
				}
			}
		}
		var checked []int // indexes into args: a destination-only command looks at the destination words only
		for j := range args {
			if !destOnly || isDestArg(args, j) {
				checked = append(checked, j)
			}
		}
		for _, j := range checked {
			if a.persistHit(args[j], name == "ln" || name == "mv", true) {
				return blockf("`" + name + "` on a protected config/init file: " + persistWhy)
			}
		}
		if name == "cp" || name == "mv" || name == "install" || name == "ln" || name == "rsync" {
			// a directory destination: the file lands as dest/<source name>
			var opts, dests []string
			for _, x := range args {
				if !strings.HasPrefix(x, "-") {
					opts = append(opts, x)
				}
			}
			for _, j := range checked {
				if j == len(args)-1 || (j > 0 && (args[j-1] == "-t" || args[j-1] == "--target-directory")) {
					dests = append(dests, args[j])
				}
			}
			for _, x := range args {
				if v, ok := strings.CutPrefix(x, "--target-directory="); ok {
					dests = append(dests, v)
				}
			}
			for _, d := range dests {
				for _, src := range opts {
					if src != d && a.persistHit(strings.TrimRight(d, "/")+"/"+pyBasename(strings.TrimRight(src, "/")), false, true) {
						return blockf("`" + name + "` into a protected directory under a protected file name: " + persistWhy)
					}
				}
			}
		}
	case (name == "sed" || name == "perl" || name == "gawk" || in(Awks, name)) && (in(Awks, name) || anyInPlace(rest)):
		cand := rest
		if in(Awks, name) && !containsStr(rest, "inplace") { // operands are data files: reading one is fine
			cand = nil
			hasFile := false
			for _, x := range rest {
				if strings.HasPrefix(x, "-f") || x == "--file" {
					hasFile = true
				}
			}
			if !hasFile {
				for _, x := range rest {
					if !strings.HasPrefix(x, "-") {
						cand = []string{x}
						break
					}
				}
			}
		}
		for _, x := range cand {
			if a.persistHit(x, false, true) || PersistCode(x) {
				return blockf("`" + name + "` writing a protected config/init file: " + persistWhy)
			}
		}
	case interpreterFull(name) || editorWriter(name):
		for _, x := range rest {
			if a.persistHit(x, false, !interpreterFull(name)) || PersistCode(x) {
				return blockf("`" + name + "` naming a protected config/init file (code there cannot be inspected; read it " +
					"with cat): " + persistWhy)
			}
		}
	}
	return nil
}
