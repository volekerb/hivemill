package guard

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"unicode/utf8"
)

// The hook entry point of the Go worker guard (moved unchanged from cmd/worker-guard/main.go so that `hivemill guard`
// runs the same code in-process): argv (--role ROLE | --hq), a PreToolUse JSON payload on stdin, exit 0 = allow,
// exit 2 = block with the reason on stderr.

// MaxHookInput caps the stdin payload. The Python hook has no cap: this is a TIGHTENED case (a payload
// above 1 MiB is blocked here and analysed there), recorded for the T7 differential gate.
const MaxHookInput = 1 << 20

// HookTestPanicEnv makes HookMain panic, to prove the recover below fails closed. A panic only ever blocks.
const HookTestPanicEnv = "WORKER_GUARD_GO_TEST_PANIC"

// HookMain is main() of worker-guard.py with the arguments and streams passed in: exit 0 allows, 2 blocks, and a panic
// or any internal error is 2. cmd/worker-guard and `hivemill guard` both run it.
func HookMain(args []string, stdin io.Reader, stdout, stderr io.Writer) (code int) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(stderr, "worker-guard(go): internal error: %v\n", r)
			code = 2
		}
	}()
	if len(args) > 0 {
		switch args[0] {
		case "--cursor-adapter":
			return CursorAdapter(args[1:], stdin, stdout, stderr, inProcessGuard)
		case "--boss-inbox":
			return BossInbox(stdin, stdout, ExecInbox)
		}
	}
	role, hq := ParseHookArgs(args)
	pyHook, passArgs := splitPyHook(args)
	SetPyHook(pyHook)
	raw, err := io.ReadAll(io.LimitReader(stdin, MaxHookInput+1))
	if err != nil {
		return failClosed(stderr, "hook error, failing closed: reading stdin: "+err.Error())
	}
	if len(raw) > MaxHookInput {
		return failClosed(stderr, "hook error, failing closed: input larger than 1 MiB")
	}
	if os.Getenv(HookTestPanicEnv) != "" {
		panic("injected by " + HookTestPanicEnv)
	}
	// Python reads stdin as UTF-8 text and fails closed on bad bytes; encoding/json would replace them.
	if !utf8.Valid(raw) {
		return failClosed(stderr, "hook error, failing closed: stdin is not valid UTF-8")
	}
	// Python's json.load error text is reproduced (PyJSONError) so stderr matches the Python hook's.
	if msg := PyJSONError(string(raw)); msg != "" {
		return failClosed(stderr, "hook error, failing closed: "+msg)
	}
	var data map[string]any // json.Unmarshal also refuses trailing data, as json.load does
	if err := json.Unmarshal(raw, &data); err != nil || data == nil {
		reason := "ValueError: not an object"
		var ute *json.UnmarshalTypeError
		if err != nil && !errors.As(err, &ute) {
			reason = "invalid payload: " + err.Error()
		}
		return failClosed(stderr, "hook error, failing closed: "+reason)
	}
	switch tool := data["tool_name"].(type) {
	case []any, map[string]any:
		// Python: `tool_name in {set}` raises TypeError on an unhashable value, which fails closed.
		return failClosed(stderr, "hook error, failing closed: TypeError: unhashable type: '"+pyTypeName(tool)+"'")
	case string:
		return dispatch(tool, data, role, hq, passArgs, raw, stdout, stderr)
	}
	return 0 // no tool name, or a non-string scalar: Python allows (it is not Bash)
}

func dispatch(tool string, data map[string]any, role string, hq bool, passArgs []string, raw []byte, stdout, stderr io.Writer) int {
	switch {
	case !hq && IsResearcher(role) && (IsPersistEditTool(tool) || tool == "Read" || tool == "Grep"):
		SessionID, _ = data["session_id"].(string)
		code, msg := ResearcherTool(data)
		if code == 0 && IsPersistEditTool(tool) {
			code, msg = PersistEdit(data, role)
		}
		return finish(code, msg)(stderr)
	case !hq && IsPersistEditTool(tool):
		return finish(PersistEdit(data, role))(stderr)
	case !hq && (tool == "Read" || tool == "Grep"):
		if IsTermsRole(role) {
			code, msg, closed := TermsTool(data)
			if closed {
				return closedExit(stderr, msg)
			}
			return finish(code, msg)(stderr)
		}
		return 0
	case tool != "Bash":
		return 0
	}
	// (data.get("tool_input") or {}).get("command"): a truthy non-dict has no .get
	ti, isMap := data["tool_input"].(map[string]any)
	if v := data["tool_input"]; !isMap && pyTruthy(v) {
		return failClosed(stderr, "hook error, failing closed: AttributeError: '"+pyTypeName(v)+"' object has no attribute 'get'")
	}
	cmd, ok := ti["command"].(string)
	if !ok {
		return failClosed(stderr, "hook error, failing closed: ValueError: no command string")
	}
	cwd := ""
	switch c := data["cwd"].(type) {
	case nil:
	case string:
		cwd = c
	default:
		// Python passes any other value to Analyzer, where a truthy one crashes and fails closed; a falsy one is no cwd.
		if pyTruthy(c) {
			why := "expected str, bytes or os.PathLike object, not " + pyTypeName(c)
			if t := pyTypeName(c); t == "list" || t == "dict" {
				why = "unhashable type: '" + t + "'"
			}
			return failClosed(stderr, "hook error, failing closed: TypeError: "+why)
		}
	}
	SessionID, _ = data["session_id"].(string)
	if err := Check(cmd, cwd, role, hq); err != nil {
		if IsDelegate(err) {
			if IsClosed(err) {
				return closedExit(stderr, err.Error())
			}
			return RunPython(passArgs, raw, stdout, stderr)
		}
		fmt.Fprint(stderr, Explain(err))
		return 2
	}
	return 0
}

// finish writes the stderr text of a tool rule and returns its exit code.
func finish(code int, msg string) func(io.Writer) int {
	return func(stderr io.Writer) int {
		fmt.Fprint(stderr, msg)
		return code
	}
}

// splitPyHook takes `--py-hook <path>` out of the arguments: the path of roles/hooks/worker-guard.py (the binary
// lives outside the repo and cannot find it), and the arguments the Python hook itself is run with.
func splitPyHook(args []string) (path string, rest []string) {
	for i := 0; i < len(args); i++ {
		if args[i] == "--py-hook" && i+1 < len(args) {
			path = args[i+1]
			i++
			continue
		}
		rest = append(rest, args[i])
	}
	return path, rest
}

// ParseHookArgs is the argv handling of worker-guard.py:main: --hq anywhere, --role VALUE not in last position.
func ParseHookArgs(args []string) (role string, hq bool) {
	for _, a := range args {
		if a == "--hq" {
			hq = true
		}
	}
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--role" {
			return args[i+1], hq
		}
	}
	return "", hq
}

// closedExit is the exit 2 for a decision that needs the guard export when the export is unusable (T-0767).
func closedExit(stderr io.Writer, why string) int {
	fmt.Fprintf(stderr, "worker-guard(go): blocked (hook error, failing closed: %s)\n", why)
	return 2
}

func failClosed(stderr io.Writer, why string) int {
	fmt.Fprintf(stderr, "worker-guard(go): blocked (%s). Workers may not run herdr or manage factory/herdr systemd units "+
		"directly; use the factory CLI (factory done, factory msg, ...). Tell your lead if this is wrong.\n", why)
	return 2
}

// pyTypeName is type(v).__name__ for a decoded JSON value (an integral number is an int, any other a float).
func pyTypeName(v any) string {
	switch n := v.(type) {
	case string:
		return "str"
	case bool:
		return "bool"
	case float64:
		if n == math.Trunc(n) {
			return "int"
		}
		return "float"
	case []any:
		return "list"
	case map[string]any:
		return "dict"
	}
	return "NoneType"
}

// inProcessGuard runs the guard (HookMain) over one payload for the Cursor adapter, without a subprocess.
func inProcessGuard(args []string, payload []byte) (int, string, string) {
	var so, se bytes.Buffer
	code := HookMain(args, bytes.NewReader(payload), &so, &se)
	return code, so.String(), se.String()
}
