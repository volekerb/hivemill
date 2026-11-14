package herdr

import (
	"os"
	"strings"
	"testing"
)

// FuzzParseResult checks that no stdout, however malformed, panics, and that the hc path always
// returns a map (Python falls back to {"raw": ...}).
func FuzzParseResult(f *testing.F) {
	for _, s := range []string{"", "\n", `{"result":{"a":1}}`, "x\n{\"result\":[1]}", "{", `{"id":1}`, "null", "\xff\xfe", `{"result":null}` + "\r\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		for _, exe := range []string{"herdr", "hc"} {
			for _, argv := range [][]string{nil, {"read", "p"}, {"ls"}, {"agent", "list"}} {
				if m, ok := parseResult(exe, argv, s).(map[string]any); exe == "hc" && (!ok || m == nil) {
					t.Fatalf("hc parse returned nil for %q", s)
				}
			}
		}
	})
}

// FuzzHcParseHostStdout covers the hc stdout parser directly.
func FuzzHcParseHostStdout(f *testing.F) {
	for _, s := range []string{"", `{"terminals":[]}`, `{"terminals":[{"name":"a"}]}`, "garbage\n[1,2]", `{"ok":true}`, "\r\n\r\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		for _, argv := range [][]string{nil, {"read", "p"}, {"ls"}, {"kill", "a"}} {
			if hcParseHostStdout(argv, s) == nil {
				t.Fatalf("nil map for %q", s)
			}
		}
	})
}

// TestClosePaneGateRefusal: a gate refusal of the pane close is returned, never reported as closed.
func TestClosePaneGateRefusal(t *testing.T) {
	s := newStub(t)
	c, _ := newClient("herdr")
	s.set("herdr.agent_list", "out", envelope(`{"agents":[{"name":"a","pane_id":"hostp1"}]}`))
	c.Gate = func(args []string) error {
		if len(args) > 1 && args[0] == "pane" && args[1] == "close" {
			return os.ErrPermission
		}
		return nil
	}
	state, pane, err := c.ClosePane("a", "stale")
	if err == nil || state == "closed" || pane != "" {
		t.Fatalf("refusal swallowed: %s %q %v", state, pane, err)
	}
	for _, call := range s.calls() {
		if strings.HasPrefix(call, "herdr pane close") {
			t.Fatalf("gated close reached the stub: %v", s.calls())
		}
	}
}
