package guard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// This file is the Go port of roles/hooks/cursor-guard-adapter.py (G-048 P2a): Cursor hook JSON on stdin ->
// the worker-guard PreToolUse shape -> a guard decision -> the Cursor answer on stdout. Unlike the Python file it
// calls the Go guard in-process (the GuardRunner the binary passes in) instead of exec-ing the hook.

// GuardRunner runs the worker-guard with the given argv (--role/--hq/--py-hook) over one PreToolUse payload and
// returns its exit code and output. cmd/worker-guard passes its own run().
type GuardRunner func(args []string, payload []byte) (code int, stdout, stderr string)

// adapterNow is the clock of the hook log (injected by tests; C2).
var adapterNow = time.Now

var cursorPathKeys = []string{"file_path", "path", "notebook_path", "target_notebook"}

var cursorWriteTools = set("Write", "Edit", "MultiEdit", "NotebookEdit", "Delete", "StrReplace", "EditNotebook")

var cursorPathCheckTools = set("Write", "Edit", "MultiEdit", "NotebookEdit", "Delete", "StrReplace", "EditNotebook", "Read", "Grep")

type cursorAdapter struct {
	role, pyHook string
	hq           bool
	stdout       io.Writer
}

// CursorAdapter is `worker-guard --cursor-adapter [--role R] [--hq] [--event E] [--py-hook P]`. It returns the exit
// code of the Python adapter for the same input: deny by a bad payload, an unsupported event or a guard failure
// is exit 2 with the deny body on stdout.
func CursorAdapter(args []string, stdin io.Reader, stdout, stderr io.Writer, run GuardRunner) (code int) {
	a := &cursorAdapter{stdout: stdout}
	var event string
	var haveEvent bool
	for i := 0; i < len(args); i++ {
		name, val, hasVal := args[i], "", false
		if k := strings.IndexByte(name, '='); strings.HasPrefix(name, "--") && k > 0 {
			name, val, hasVal = name[:k], name[k+1:], true
		}
		need := func() bool {
			if hasVal {
				return true
			}
			if i+1 >= len(args) {
				return false
			}
			i++
			val = args[i]
			return true
		}
		switch name {
		case "--hq":
			a.hq = true
		case "--role", "--event", "--py-hook":
			if !need() {
				return adapterUsage(stderr, "argument "+name+": expected one argument")
			}
			switch name {
			case "--role":
				a.role = val
			case "--event":
				event, haveEvent = val, true
			default:
				a.pyHook = val
			}
		case "-h", "--help":
			fmt.Fprintln(stdout, "usage: worker-guard --cursor-adapter [-h] [--role ROLE] [--hq] [--event EVENT] [--py-hook PATH]")
			return 0
		default:
			return adapterUsage(stderr, "unrecognized arguments: "+args[i])
		}
	}
	defer func() {
		if r := recover(); r != nil { // an internal error is a deny, never an allow
			code = a.denyDirect(event, fmt.Sprintf("cursor-guard-adapter: guard failed (%v)", r))
		}
	}()
	flagEvent := any(nil)
	if haveEvent && event != "" {
		flagEvent = event
	}
	raw, err := io.ReadAll(stdin)
	if err == nil && !utf8.Valid(raw) {
		err = fmt.Errorf("UnicodeDecodeError: stdin is not valid UTF-8")
	}
	var data map[string]any
	if err == nil {
		if msg := PyJSONError(string(raw)); msg != "" {
			err = fmt.Errorf("%s", strings.TrimPrefix(msg, "JSONDecodeError: ")) // str(e) of the exception
		}
	}
	if err != nil {
		return a.denyDirect(event, "cursor-guard-adapter: bad JSON on stdin ("+err.Error()+")")
	}
	var anyData any
	if err := json.Unmarshal(raw, &anyData); err != nil {
		return a.denyDirect(event, "cursor-guard-adapter: bad JSON on stdin ("+err.Error()+")")
	}
	data, ok := anyData.(map[string]any)
	if !ok {
		return a.denyDirect(event, "cursor-guard-adapter: stdin JSON must be an object")
	}

	ev := flagEvent
	for _, k := range []string{"hook_event_name", "event"} {
		if truthy(ev) {
			break
		}
		ev = data[k]
	}
	evs, _ := ev.(string)
	if evs != "beforeShellExecution" && evs != "preToolUse" && evs != "beforeMCPExecution" {
		return a.denyDirect(evs, "cursor-guard-adapter: unsupported event "+pyReprVal(ev))
	}

	guardArgs := []string{}
	if a.hq {
		guardArgs = append(guardArgs, "--hq")
	}
	if a.role != "" {
		guardArgs = append(guardArgs, "--role", a.role)
	}
	if a.pyHook != "" {
		guardArgs = append(guardArgs, "--py-hook", a.pyHook)
	}
	runOne := func(p map[string]any) (int, string) {
		b, err := json.Marshal(p)
		if err != nil {
			return 2, "payload not encodable: " + err.Error()
		}
		c, so, se := run(guardArgs, b)
		// the Go guard words its own errors "worker-guard(go):"; the Cursor answer carries the Python hook's text
		reason := strings.TrimFunc(strings.Replace(se, "worker-guard(go):", "worker-guard:", 1), pyIsSpace)
		if reason == "" {
			reason = strings.TrimFunc(so, pyIsSpace)
		}
		if reason == "" {
			reason = "worker-guard exit " + strconv.Itoa(c)
		}
		return c, reason
	}

	if evs == "beforeMCPExecution" {
		role := a.role
		if role == "" {
			role = "worker"
		}
		if role != "boss" {
			return a.emit(evs, false, "MCP disabled for factory role "+role)
		}
		if c, reason := runOne(mcpGuardPayload(data)); c != 0 {
			return a.emit(evs, false, reason)
		}
		return a.emit(evs, true, "")
	}

	payload := toClaudePayload(data, evs)
	if payload == nil {
		return a.emit(evs, true, "")
	}
	tool := data["tool_name"]
	payloads := []map[string]any{payload}
	if ts, isStr := tool.(string); isStr && in(cursorPathCheckTools, ts) {
		payloads = guardPayloadsForAllPaths(payload, data)
	} else if !isStr && tool != nil {
		// Python: `tool in frozenset` raises TypeError for an unhashable tool_name (a traceback, exit 1);
		// here that is a deny (TIGHTENED, recorded for the parity tests).
		if _, unhashable := tool.([]any); unhashable {
			return a.denyDirect(evs, "cursor-guard-adapter: guard failed (TypeError: unhashable type: 'list')")
		}
		if _, unhashable := tool.(map[string]any); unhashable {
			return a.denyDirect(evs, "cursor-guard-adapter: guard failed (TypeError: unhashable type: 'dict')")
		}
	}
	for _, p := range payloads {
		if c, reason := runOne(p); c != 0 {
			return a.emit(evs, false, reason)
		}
	}
	return a.emit(evs, true, "")
}

func adapterUsage(stderr io.Writer, why string) int {
	fmt.Fprintf(stderr, "usage: worker-guard --cursor-adapter [-h] [--role ROLE] [--hq] [--event EVENT]\nworker-guard --cursor-adapter: error: %s\n", why)
	return 2
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return x != ""
	case bool:
		return x
	case float64:
		return x != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

// pyReprVal is repr(v) of a decoded JSON value (the {event!r} of the unsupported-event message).
func pyReprVal(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case bool:
		if x {
			return "True"
		}
		return "False"
	case string:
		return pyRepr(x)
	case float64:
		if x == float64(int64(x)) && x < 1e15 && x > -1e15 {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = pyReprVal(e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sortStrings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = pyRepr(k) + ": " + pyReprVal(x[k])
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return fmt.Sprint(v)
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func basePayload(data map[string]any) map[string]any {
	base := map[string]any{}
	if cwd, ok := data["cwd"].(string); ok && cwd != "" {
		base["cwd"] = cwd
	}
	if sid, ok := data["session_id"].(string); ok {
		base["session_id"] = sid
	}
	return base
}

func withTool(base map[string]any, tool string, input map[string]any) map[string]any {
	out := map[string]any{"tool_name": tool, "tool_input": input}
	for k, v := range base {
		out[k] = v
	}
	return out
}

func pathsFromToolInput(ti map[string]any) []string {
	var out []string
	seen := map[string]bool{}
	for _, k := range cursorPathKeys {
		if v, ok := ti[k].(string); ok && v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func toolInputMap(data map[string]any) map[string]any {
	ti, _ := data["tool_input"].(map[string]any)
	if ti == nil {
		ti = map[string]any{}
	}
	return ti
}

// toClaudePayload is to_claude_payload: nil means "skip (allow)".
func toClaudePayload(data map[string]any, event string) map[string]any {
	base := basePayload(data)
	if event == "beforeShellExecution" {
		if cmd, ok := data["command"].(string); ok {
			return withTool(base, "Bash", map[string]any{"command": cmd})
		}
		return withTool(base, "Bash", map[string]any{}) // fail closed in the guard
	}
	tool, _ := data["tool_name"].(string)
	ti := toolInputMap(data)
	switch {
	case tool == "Shell" || tool == "Bash":
		cmd, ok := ti["command"].(string)
		if !ok {
			cmd, ok = data["command"].(string)
		}
		if !ok {
			return withTool(base, "Bash", map[string]any{})
		}
		return withTool(base, "Bash", map[string]any{"command": cmd})
	case in(cursorWriteTools, tool):
		path := ""
		if ps := pathsFromToolInput(ti); len(ps) > 0 {
			path = ps[0]
		}
		return withTool(base, "Write", map[string]any{"file_path": path})
	case tool == "Read" || tool == "Grep":
		var path any = ""
		if ps := pathsFromToolInput(ti); len(ps) > 0 {
			path = ps[0]
		} else if v := ti["path"]; truthy(v) {
			path = v
		}
		if tool == "Read" {
			return withTool(base, "Read", map[string]any{"file_path": path})
		}
		return withTool(base, "Grep", map[string]any{"path": path})
	}
	return nil // unknown tool: do not invent a block
}

// mcpGuardPayload is mcp_to_guard_payload.
func mcpGuardPayload(data map[string]any) map[string]any {
	tool, ok := data["tool_name"].(string)
	if !ok {
		tool = "MCP"
	}
	ti := data["tool_input"]
	if s, isStr := ti.(string); isStr {
		var v any
		if json.Unmarshal([]byte(s), &v) == nil {
			ti = v
		} else {
			ti = nil
		}
	}
	m, isMap := ti.(map[string]any)
	if !isMap {
		m = map[string]any{}
	}
	return withTool(basePayload(data), tool, m)
}

// guardPayloadsForAllPaths is _guard_payloads_for_all_paths: one guard payload per path-like value.
func guardPayloadsForAllPaths(payload, data map[string]any) []map[string]any {
	paths := pathsFromToolInput(toolInputMap(data))
	if len(paths) == 0 {
		return []map[string]any{payload}
	}
	base := map[string]any{}
	for k, v := range payload {
		if k != "tool_name" && k != "tool_input" {
			base[k] = v
		}
	}
	tool, _ := data["tool_name"].(string)
	var out []map[string]any
	for _, p := range paths {
		switch tool {
		case "Read":
			out = append(out, withTool(base, "Read", map[string]any{"file_path": p}))
		case "Grep":
			out = append(out, withTool(base, "Grep", map[string]any{"path": p}))
		default:
			out = append(out, withTool(base, "Write", map[string]any{"file_path": p}))
		}
	}
	return out
}

// logDecision appends {ts, agent, role, event, decision} to FACTORY_CURSOR_HOOK_LOG. It never fails the hook and
// never logs command text.
func (a *cursorAdapter) logDecision(event string, allow bool) {
	path := os.Getenv("FACTORY_CURSOR_HOOK_LOG")
	if path == "" {
		return
	}
	role := a.role
	if role == "" {
		role = os.Getenv("FACTORY_ROLE")
	}
	dec := "deny"
	if allow {
		dec = "allow"
	}
	rec := `{"ts": ` + pyJSONString(adapterNow().UTC().Format("2006-01-02T15:04:05")+"+00:00") +
		`, "agent": ` + pyJSONString(os.Getenv("FACTORY_AGENT")) + `, "role": ` + pyJSONString(role) +
		`, "event": ` + pyJSONString(event) + `, "decision": "` + dec + `"}` + "\n"
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o666)
	if err != nil {
		return // logging must never change the decision
	}
	_, _ = f.WriteString(rec)
	_ = f.Close()
}

func (a *cursorAdapter) emit(event string, allow bool, reason string) int {
	a.logDecision(event, allow)
	if allow {
		fmt.Fprintln(a.stdout, `{"permission": "allow"}`)
		return 0
	}
	msg := strings.TrimFunc(reason, pyIsSpace)
	if msg == "" {
		msg = "blocked by worker-guard"
	}
	fmt.Fprintln(a.stdout, `{"permission": "deny", "agent_message": `+pyJSONString(msg)+`, "user_message": `+pyJSONString(msg)+`}`)
	if event == "preToolUse" || event == "beforeMCPExecution" {
		return 2
	}
	return 0
}

func (a *cursorAdapter) denyDirect(event, msg string) int {
	if event == "" {
		event = "unknown"
	}
	a.logDecision(event, false)
	fmt.Fprintln(a.stdout, `{"permission": "deny", "agent_message": `+pyJSONString(msg)+`}`)
	return 2
}

// pyJSONString is json.dumps(s) of Python (ensure_ascii): non-ASCII as \uXXXX with surrogate pairs.
func pyJSONString(s string) string {
	var b bytes.Buffer
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\f':
			b.WriteString(`\f`)
		case r < 0x20:
			fmt.Fprintf(&b, `\u%04x`, r)
		case r < 0x80:
			b.WriteRune(r)
		case r < 0x10000:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			r -= 0x10000
			fmt.Fprintf(&b, `\u%04x\u%04x`, 0xd800+(r>>10), 0xdc00+(r&0x3ff))
		}
	}
	b.WriteByte('"')
	return b.String()
}
