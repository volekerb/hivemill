package guard

import "strings"

// uvValued is worker-guard.py:UV_VALUED: uv options that take a value.
var uvValued = set("--with", "--with-requirements", "-p", "--python", "--project", "--directory", "--env-file",
	"--extra", "--group", "--package", "--only-group", "--no-group", "--with-editable", "-w", "--index",
	"--config-file", "--from")

// uvInner is a port of worker-guard.py:uv_inner (T-0880): the words after `uv [opts] run` / `uv [opts] tool run`
// / `uvx`, or none when uv runs nothing.
func uvInner(name string, rest []string) []string {
	if name == "uvx" {
		return rest
	}
	for i, x := range rest {
		if x == "run" {
			return rest[i+1:]
		}
	}
	return nil
}

// uvCheck is a port of Analyzer.uv_check (T-0880): `uv run [opts] [--] CMD...` / `uvx [opts] CMD...` is checked like
// CMD run directly, for the coder and the no-push roles only.
func (a *Analyzer) uvCheck(name string, rest []string, depth int, piped, inLoose bool, hstrs, assigns []string) error {
	if a.HQ || !(a.Role == "coder" || in(noPushRoles, a.Role)) {
		return nil
	}
	words := uvInner(name, rest)
	i := 0
	for i < len(words) {
		x := words[i]
		if x == "--" {
			i++
			break
		}
		if !strings.HasPrefix(x, "-") {
			break
		}
		if in(uvValued, x) {
			i += 2
		} else {
			i++
		}
	}
	for k := i; k < len(words); k++ { // git / inline code: later words only, so values of known options are never scanned
		cand := words[k:]
		base := pyBasename(cand[0])
		if base == "git" && k > i && in(set(cand[1:]...), "push") {
			return blockf(name + " running git push behind an unknown uv option; push only your own task branch " +
				"with a plain `git push origin HEAD:factory/<task>`")
		}
		if in(noPushRoles, a.Role) {
			if base == "gh" && matchInlineGhWrite(strings.Join(cand, " ")) {
				return blockf(name + " running a gh write (pr merge/create, repo, api with a write method); " +
					"no-push roles never write to GitHub; ask your lead")
			}
			if k > i {
				if err := a.interpInlinePush(base, cand[1:]); err != nil {
					return err
				}
			}
		}
	}
	if i < len(words) {
		return a.cmd(words[i:], depth+1, piped, inLoose, hstrs, assigns)
	}
	return nil
}
