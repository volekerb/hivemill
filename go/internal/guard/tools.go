package guard

import (
	"os"
	"strings"
)

// The Edit/Write and Read/Grep tool rules of worker-guard.py (not Bash): persist_edit and terms_tool. Each returns the
// exit code and the stderr text of worker-guard.py:main.

// pyTruthy is Python truthiness of a decoded JSON value.
func pyTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

func toolInput(data map[string]any) map[string]any {
	ti, _ := data["tool_input"].(map[string]any)
	return ti
}

// payloadCwd is `data.get("cwd") if isinstance(data.get("cwd"), str) else os.getcwd()`.
func payloadCwd(data map[string]any) string {
	if c, ok := data["cwd"].(string); ok {
		return c
	}
	wd, err := os.Getwd()
	if err != nil {
		panic("cannot determine the working directory: " + err.Error()) // main recovers: exit 2
	}
	return wd
}

const exportWhy = "run/guard-export.json holds the guard's own decisions (own goal, push pin, sandbox, terms file); only " +
	"the tick writes it"

// exportPath is a port of export_path (T-0883): p is run/guard-export.json of a live home or its temp sibling.
func exportPath(p string) bool {
	for _, c := range uniq([]string{pyNormpath(p), pyRealpath(p)}) {
		for _, d := range mustHost().runExport {
			if c == d || strings.HasPrefix(c, d+".") {
				return true
			}
		}
	}
	return false
}

// PersistEdit is a port of persist_edit (T-0334): an Edit/Write/MultiEdit/NotebookEdit call on a protected
// config/init file is refused. A payload without a readable path cannot be decided and fails closed.
func PersistEdit(data map[string]any, role string) (int, string) {
	ti := toolInput(data)
	path := ti["file_path"]
	if !pyTruthy(path) {
		path = ti["notebook_path"]
	}
	ps, ok := path.(string)
	if !ok || ps == "" {
		return 2, "worker-guard: blocked (edit tool call without a readable file_path, failing closed)\n"
	}
	cwd := payloadCwd(data)
	p := tildeExpand(ps, cwd, true)
	unres := tildeUnres(ps, true) // rule A: an unresolvable `~name/...` is judged by the protected names it spells
	if !strings.HasPrefix(p, "/") {
		p = pyJoin(cwd, p)
	}
	if exportPath(p) || (strings.Contains(p, "$") && strings.Contains(p, "guard-export")) {
		return 2, "worker-guard: blocked (" + pyRepr(ps) + "): " + exportWhy + ". Tell your lead if this is wrong.\n"
	}
	if role == retroRole && (retroRolesPath(p, false) || (unres && retroRolesPath(ps, true)) ||
		(strings.ContainsAny(ps, "$`") && retroRolesSubst(ps, cwd, true))) {
		return 2, "worker-guard: blocked (" + pyRepr(ps) + "): " + retroRoleWhy + ". Tell your lead if this is wrong.\n"
	}
	if protEditPath(p, false) || (unres && protEditPath(ps, true)) { // T-0925 gap g: the Edit tools were checked against the persist paths only
		return 2, "worker-guard: blocked (" + pyRepr(ps) + "): " + protWhy + ". Tell your lead if this is wrong.\n"
	}
	// T-0957: ~/.local/bin is first on the tick's PATH; matched as written and resolved (its entries are symlinks), and
	// a path with an unexpanded `$` that names .local/bin cannot be decided, so it is refused too
	binHit := (strings.Contains(p, "$") && hasLocalBin(p)) || (unres && hasLocalBin(ps))
	for _, q := range []string{pyNormpath(p), pyRealpath(p)} {
		for _, d := range mustHost().binDirs {
			if q == d || Under(q, d) {
				binHit = true
			}
		}
	}
	if binHit {
		return 2, "worker-guard: blocked (" + pyRepr(ps) + "): ~/.local/bin is first on the tick's PATH, so a file " +
			"planted there runs with the tick's credentials; only the owner changes it. Tell your lead if this is wrong.\n"
	}
	noPush := in(noPushRoles, role) // T-0770: no scratchpad exemption for git config/hooks/info (never reached for HQ)
	if persistPath(p, false, !noPush) || (noPush && (gitHooksPath(p, false) || gitfilePath(p))) || (strings.Contains(p, "$") && PersistCode(p)) {
		why := persistWhy
		if repoCfgPath(pyRealpath(p), true) || repoCfgPath(pyNormpath(p), true) {
			why = repoCfgWhy
		}
		return 2, "worker-guard: blocked (" + pyRepr(ps) + "): " + why + ". Tell your lead if this is wrong.\n"
	}
	return 0, ""
}

// TermsTool is a port of terms_tool: a Read/Grep tool call that would read the owner-terms file (Grep over a
// directory holding it counts).
//
// The third result is true when the guard export is unusable (the DB exists but the export is missing, stale or
// unparsable): the caller exits 2, failing closed.
func TermsTool(data map[string]any) (int, string, bool) {
	ti := toolInput(data)
	cwd := payloadCwd(data)
	paths, perr := termsPaths()
	if perr != nil {
		return 0, perr.Error(), true
	}
	if len(paths) == 0 {
		return 0, "", false
	}
	var raw any
	if data["tool_name"] == "Read" {
		raw = ti["file_path"]
	} else {
		raw = ti["path"]
		if !pyTruthy(raw) {
			raw = cwd
		}
	}
	rs, ok := raw.(string)
	if !ok || rs == "" {
		return 2, "worker-guard: blocked (tool call without a readable path, failing closed)\n", false
	}
	p := pyRealpath(pyJoin(cwd, expandUser(strings.ReplaceAll(rs, "${"+termsVar+"}", "$"+termsVar))))
	hit := strings.Contains(rs, termsVar)
	if _, ok := paths[p]; ok {
		hit = true
	}
	for t := range paths {
		if strings.HasPrefix(t, strings.TrimRight(p, "/")+"/") {
			hit = true
		}
	}
	if hit {
		return 2, "worker-guard: blocked (" + termsWhy + ")\n", false
	}
	return 0, "", false
}
