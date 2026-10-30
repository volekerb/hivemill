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

// PersistEdit is a port of persist_edit (T-0334): an Edit/Write/MultiEdit/NotebookEdit call on a protected
// config/init file is refused. A payload without a readable path cannot be decided and fails closed.
func PersistEdit(data map[string]any) (int, string) {
	ti := toolInput(data)
	path := ti["file_path"]
	if !pyTruthy(path) {
		path = ti["notebook_path"]
	}
	ps, ok := path.(string)
	if !ok || ps == "" {
		return 2, "worker-guard: blocked (edit tool call without a readable file_path, failing closed)\n"
	}
	p := cfgExpand(ps)
	cwd := payloadCwd(data)
	if !strings.HasPrefix(p, "/") {
		p = pyJoin(cwd, p)
	}
	if persistPath(p, false) || (strings.Contains(p, "$") && PersistCode(p)) {
		why := persistWhy
		if repoCfgPath(pyRealpath(p)) || repoCfgPath(pyNormpath(p)) {
			why = repoCfgWhy
		}
		return 2, "worker-guard: blocked (" + pyRepr(ps) + "): " + why + ". Tell your lead if this is wrong.\n"
	}
	return 0, ""
}

// TermsTool is a port of terms_tool: a Read/Grep tool call that would read the owner-terms file (Grep over a
// directory holding it counts).
//
// The third result is true when the decision is the Python hook's (no exported terms file, but a DB that may name it).
func TermsTool(data map[string]any) (int, string, bool) {
	ti := toolInput(data)
	cwd := payloadCwd(data)
	paths := termsPaths()
	if len(paths) == 0 {
		return 0, "", termsFromDBOnly()
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
