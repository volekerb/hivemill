// Command worker-guard is the Go port of roles/hooks/worker-guard.py (G-016 SC4, flag-gated; not wired
// anywhere yet). It keeps the hook's I/O contract: argv (--role ROLE | --hq), a PreToolUse JSON payload on
// stdin, exit 0 = allow, exit 2 = block with the reason on stderr.
//
// Until the analysis sections land (T-0347 front end, then T6b commands, T6c git, T6d config/persist/factory)
// every call the Python hook would have to analyse is refused with "not ported": a partial port never allows
// more than Python. A panic or any internal error also exits 2.
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

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stderr)) }

// run is main() of worker-guard.py with the arguments and streams passed in.
func run(args []string, stdin io.Reader, stderr io.Writer) (code int) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(stderr, "worker-guard(go): internal error: %v\n", r)
			code = 2
		}
	}()
	role, hq := parseArgs(args)
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
		return dispatch(tool, data, role, hq, stderr)
	}
	return 0 // no tool name, or a non-string scalar: Python allows (it is not Bash)
}

func dispatch(tool string, data map[string]any, role string, hq bool, stderr io.Writer) int {
	switch {
	case !hq && guard.IsPersistEditTool(tool):
		return notPorted(stderr, "persist_edit (T6d)")
	case !hq && (tool == "Read" || tool == "Grep"):
		if guard.IsTermsRole(role) {
			return notPorted(stderr, "terms_tool (T6d)")
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
	if _, _, err := guard.Preprocess(cmd, hq, nil); err != nil {
		return failClosed(stderr, err.Error())
	}
	return notPorted(stderr, "bash analysis (T6b-T6d)")
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

func notPorted(stderr io.Writer, section string) int {
	fmt.Fprintf(stderr, "worker-guard(go): not ported: %s; failing closed. Use the Python hook (guard_impl=python).\n", section)
	return 2
}
