package main

import (
	"strings"
	"testing"
)

// T-0352 B2: a bad payload fails closed with the same reason text as the Python hook (only "worker-guard(go)" differs);
// each reason below is what roles/hooks/worker-guard.py printed for the payload.
func TestPayloadErrorReasonsMatchPython(t *testing.T) {
	const bash = `"tool_name": "Bash"`
	for _, c := range []struct{ payload, reason string }{
		{``, "JSONDecodeError: Expecting value: line 1 column 1 (char 0)"},
		{`{not json`, "JSONDecodeError: Expecting property name enclosed in double quotes: line 1 column 2 (char 1)"},
		{`[]`, "ValueError: not an object"},
		{`null`, "ValueError: not an object"},
		{`{"tool_name": ["x"]}`, "TypeError: unhashable type: 'list'"},
		{`{"tool_name": {"a": 1}}`, "TypeError: unhashable type: 'dict'"},
		{`{` + bash + `}`, "ValueError: no command string"},
		{`{` + bash + `, "tool_input": {"command": 5}}`, "ValueError: no command string"},
		{`{` + bash + `, "tool_input": "x"}`, "AttributeError: 'str' object has no attribute 'get'"},
		{`{` + bash + `, "tool_input": [1]}`, "AttributeError: 'list' object has no attribute 'get'"},
		{`{` + bash + `, "tool_input": {"command": "ls"}, "cwd": 5}`, "TypeError: expected str, bytes or os.PathLike object, not int"},
		{`{` + bash + `, "tool_input": {"command": "ls"}, "cwd": true}`, "TypeError: expected str, bytes or os.PathLike object, not bool"},
		{`{` + bash + `, "tool_input": {"command": "ls"}, "cwd": [1]}`, "TypeError: unhashable type: 'list'"},
	} {
		code, stderr := call(t, []byte(c.payload), nil, "--role", "coder")
		want := "worker-guard(go): blocked (hook error, failing closed: " + c.reason + "). "
		if code != 2 || !strings.HasPrefix(stderr, want) {
			t.Errorf("payload %q: exit %d stderr %q, want exit 2 starting %q", c.payload, code, stderr, want)
		}
	}
	for _, payload := range []string{
		`{"tool_name": "Bash", "tool_input": {"command": "ls"}, "cwd": 0}`,
		`{"tool_name": "Bash", "tool_input": {"command": "ls"}, "cwd": {}}`,
		`{"tool_name": "Bash", "tool_input": {"command": "ls"}, "cwd": ""}`,
	} {
		if code, stderr := call(t, []byte(payload), nil, "--role", "coder"); code != 0 {
			t.Errorf("a falsy cwd is no cwd (Python allows it): %s -> exit %d %q", payload, code, stderr)
		}
	}
}
