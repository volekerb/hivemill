// Command worker-guard is the Go port of roles/hooks/worker-guard.py (G-016 SC4, flag-gated; not wired
// anywhere yet). It keeps the hook's I/O contract: argv (--role ROLE | --hq), a PreToolUse JSON payload on
// stdin, exit 0 = allow, exit 2 = block with the reason on stderr.
//
// Every rule is ported. The decisions that read the factory DB (own goal, test_sandbox, public-text repos, the
// coder's task worktree) are the Python hook's: the binary re-runs the payload through it (--py-hook <path>, given
// by the shim) and returns its exit code and stderr, or exits 2 when that cannot be done. A panic or any internal
// error also exits 2.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"unicode/utf8"

	"factory/go/internal/guard"
)

// maxInput caps the stdin payload. The Python hook has no cap: this is a TIGHTENED case (a payload
// above 1 MiB is blocked here and analysed there), recorded for the T7 differential gate.
const maxInput = 1 << 20

// testPanicEnv makes run panic, to prove the recover below fails closed. A panic only ever blocks.
const testPanicEnv = "WORKER_GUARD_GO_TEST_PANIC"

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

// run is main() of worker-guard.py with the arguments and streams passed in.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) (code int) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(stderr, "worker-guard(go): internal error: %v\n", r)
			code = 2
		}
	}()
	role, hq := parseArgs(args)
	pyHook, passArgs := splitPyHook(args)
	guard.SetPyHook(pyHook)
	raw, err := io.ReadAll(io.LimitReader(stdin, maxInput+1))
	if err != nil {
		return failClosed(stderr, "hook error, failing closed: reading stdin: "+err.Error())
	}
	if len(raw) > maxInput {
		return failClosed(stderr, "hook error, failing closed: input larger than 1 MiB")
	}
	if os.Getenv(testPanicEnv) != "" {
		panic("injected by " + testPanicEnv)
	}
	// Python reads stdin as UTF-8 text and fails closed on bad bytes; encoding/json would replace them.
	if !utf8.Valid(raw) {
		return failClosed(stderr, "hook error, failing closed: stdin is not valid UTF-8")
	}
	var data map[string]any // json.Unmarshal also refuses trailing data, as json.load does
	if err := json.Unmarshal(raw, &data); err != nil || data == nil {
		reason := "not an object"
		if err != nil {
			reason = err.Error()
		}
		return failClosed(stderr, "hook error, failing closed: invalid payload: "+reason)
	}
	switch tool := data["tool_name"].(type) {
	case []any, map[string]any:
		// Python: `tool_name in {set}` raises TypeError on an unhashable value, which fails closed.
		return failClosed(stderr, "hook error, failing closed: tool_name is not a string")
	case string:
		return dispatch(tool, data, role, hq, passArgs, raw, stdout, stderr)
	}
	return 0 // no tool name, or a non-string scalar: Python allows (it is not Bash)
}

func dispatch(tool string, data map[string]any, role string, hq bool, passArgs []string, raw []byte, stdout, stderr io.Writer) int {
	switch {
	case !hq && guard.IsPersistEditTool(tool):
		return finish(guard.PersistEdit(data, role))(stderr)
	case !hq && (tool == "Read" || tool == "Grep"):
		if guard.IsTermsRole(role) {
			code, msg, py := guard.TermsTool(data)
			if py {
				return guard.RunPython(passArgs, raw, stdout, stderr)
			}
			return finish(code, msg)(stderr)
		}
		return 0
	case tool != "Bash":
		return 0
	}
	ti, _ := data["tool_input"].(map[string]any)
	cmd, ok := ti["command"].(string)
	if !ok {
		return failClosed(stderr, "hook error, failing closed: no command string")
	}
	cwd := ""
	switch c := data["cwd"].(type) {
	case nil:
	case string:
		cwd = c
	default:
		// Python passes any other value to Analyzer, where a truthy one crashes and fails closed.
		return failClosed(stderr, "hook error, failing closed: cwd is not a string")
	}
	guard.SessionID, _ = data["session_id"].(string)
	if err := guard.Check(cmd, cwd, role, hq); err != nil {
		if guard.IsDelegate(err) {
			return guard.RunPython(passArgs, raw, stdout, stderr)
		}
		fmt.Fprint(stderr, guard.Explain(err))
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

// parseArgs is the argv handling of worker-guard.py:main: --hq anywhere, --role VALUE not in last position.
func parseArgs(args []string) (role string, hq bool) {
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

func failClosed(stderr io.Writer, why string) int {
	fmt.Fprintf(stderr, "worker-guard(go): blocked (%s). Workers may not run herdr or manage factory/herdr systemd units "+
		"directly; use the factory CLI (factory done, factory msg, ...). Tell your lead if this is wrong.\n", why)
	return 2
}
