package cursor

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	ch "factory/go/internal/cursorharness"
)

// Port of tests/test_cursor_mcp.py (11 cases; see ../ported_cursor.txt).
// adapted: the Python adapter module is not importable from Go, so the boss case compares the adapter's answer with
// the answer of the real worker-guard hook run on the payload mcp_to_guard_payload builds (the same two runs the
// Python test compares), instead of asserting a mocked run_guard call.

const matcher = "Shell|Write|Delete|Read|Grep|StrReplace|EditNotebook"

var mcpPayload = map[string]any{"tool_name": "mcp_auth", "tool_input": "{}", "mcp_server_name": "plugin-higgsfield", "cwd": "/tmp"}

func TestCursorMcpWorksMatrixMcpItemIsWorks(t *testing.T) {
	for _, r := range ch.WorksNeedsWork() {
		if r.Item == "MCP strict empty (user MCP blind)" {
			eq(t, "status", r.Status, "WORKS")
			contains(t, "detail", r.Detail, "beforeMCPExecution")
			return
		}
	}
	t.Fatal("works matrix has no MCP strict empty row")
}

func TestCursorMcpHooksJSONHasBeforeMcpFailClosed(t *testing.T) {
	hooks := sub(t, hooksOf(t, "coder", false), "hooks")
	entries := list(t, hooks, "beforeMCPExecution")
	eq(t, "entries", len(entries), 1)
	e := entries[0].(map[string]any)
	if fc, _ := e["failClosed"].(bool); !fc {
		t.Fatalf("failClosed = %v", e["failClosed"])
	}
	cmd, _ := e["command"].(string)
	contains(t, "command", cmd, "beforeMCPExecution")
	contains(t, "command", cmd, "--event")
	for event, v := range hooks {
		for _, it := range v.([]any) {
			if fc, _ := it.(map[string]any)["failClosed"].(bool); !fc {
				t.Errorf("%s entry %v must set failClosed", event, it)
			}
		}
	}
}

func TestCursorMcpMaterializedPolicyBeforeMcpCommand(t *testing.T) {
	info := materialize(t, t.TempDir(), "coder", ch.MaterializeOpts{Deny: []string{"Bash(sudo:*)"}, Allow: []string{}})
	hooks := sub(t, readJSON(t, info.Hooks), "hooks")
	cmd, _ := list(t, hooks, "beforeMCPExecution")[0].(map[string]any)["command"].(string)
	contains(t, "beforeMCPExecution command", cmd, "beforeMCPExecution")
	m, _ := list(t, hooks, "preToolUse")[0].(map[string]any)["matcher"].(string)
	eq(t, "matcher", m, matcher)
}

func TestCursorMcpNonBossRolesDenyMcpExitTwo(t *testing.T) {
	for _, role := range []string{"pm", "coder", "lead"} {
		t.Run(role, func(t *testing.T) {
			r := adapterEvent(t, "beforeMCPExecution", mcpPayload, role, ch.HQRoles[role], nil)
			body := lastJSON(t, r.Stdout)
			eq(t, "permission", body["permission"], any("deny"))
			msg, _ := body["agent_message"].(string)
			contains(t, "agent_message", msg, "MCP disabled for factory role")
			contains(t, "agent_message", msg, role)
			eq(t, "exit", r.Code, 2)
		})
	}
}

func TestCursorMcpBossMatchesWorkerGuard(t *testing.T) {
	// mcp_to_guard_payload: tool_input "{}" is parsed to an object, cwd is kept.
	guardIn := `{"tool_name": "mcp_auth", "tool_input": {}, "cwd": "/tmp"}`
	home := t.TempDir()
	guard := runPython(t, guardIn, map[string]string{"FACTORY_HOME": home}, filepath.Join(hooksDir(t), ch.GuardName), "--hq")
	// the adapter must call run_guard exactly once with role boss and hq true (Python patches run_guard and checks this)
	inv := runPython(t, "", map[string]string{"FACTORY_HOME": home}, "-c", `import sys, json, io, subprocess, importlib.util
from unittest import mock
spec = importlib.util.spec_from_file_location("adapter_t1686", sys.argv[1])
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)
real = subprocess.run([sys.executable, sys.argv[2], "--hq"], input=sys.argv[3], capture_output=True, text=True)
calls = []
def fake(payload, role, hq):
    calls.append([role, hq])
    return real
with mock.patch.object(mod, "run_guard", side_effect=fake), mock.patch("sys.stdin", io.StringIO(sys.argv[4])):
    rc = mod.main(["--hq", "--role", "boss", "--event", "beforeMCPExecution"])
print(json.dumps({"calls": calls, "rc": rc, "guard_rc": real.returncode}))
`, filepath.Join(hooksDir(t), ch.AdapterName), filepath.Join(hooksDir(t), ch.GuardName), guardIn, string(mustJSON(t, mcpPayload)))
	eq(t, "inspect exit", inv.Code, 0)
	got := lastJSON(t, inv.Stdout)
	if !reflect.DeepEqual(got["calls"], []any{[]any{"boss", true}}) {
		t.Fatalf("run_guard calls = %v, want one call (boss, true)", got["calls"])
	}
	wantRC := 0.0
	if got["guard_rc"].(float64) != 0 {
		wantRC = 2
	}
	eq(t, "adapter rc follows the guard", got["rc"].(float64), wantRC)
	r := adapterEvent(t, "beforeMCPExecution", mcpPayload, "boss", true, map[string]string{"FACTORY_HOME": home})
	if guard.Code == 0 {
		eq(t, "permission", lastJSON(t, r.Stdout)["permission"], any("allow"))
		eq(t, "exit", r.Code, 0)
	} else {
		eq(t, "permission", lastJSON(t, r.Stdout)["permission"], any("deny"))
		eq(t, "exit", r.Code, 2)
	}
}

func TestCursorMcpBeforeMcpMalformedStdinExitTwoDeny(t *testing.T) {
	r := runAdapter(t, "not json", map[string]string{"FACTORY_HOME": t.TempDir()}, "--event", "beforeMCPExecution", "--role", "pm")
	eq(t, "permission", lastJSON(t, r.Stdout)["permission"], any("deny"))
	eq(t, "exit", r.Code, 2)
}

func preToolUse(t *testing.T, tool string, input map[string]any) adapterRun {
	t.Helper()
	return adapterEvent(t, "preToolUse", map[string]any{"tool_name": tool, "tool_input": input, "cwd": "/tmp"}, "pm", false, nil)
}

func TestCursorMcpPretooluseStrreplaceBashrcDenied(t *testing.T) {
	r := preToolUse(t, "StrReplace", map[string]any{"file_path": passwdHome(t) + "/.bashrc", "old_string": "a", "new_string": "b"})
	eq(t, "permission", lastJSON(t, r.Stdout)["permission"], any("deny"))
	eq(t, "exit", r.Code, 2)
}

func TestCursorMcpPretooluseStrreplaceTempAllowed(t *testing.T) {
	r := preToolUse(t, "StrReplace", map[string]any{"file_path": "/tmp/cursor-ok.txt", "old_string": "a", "new_string": "b"})
	eq(t, "permission", lastJSON(t, r.Stdout)["permission"], any("allow"))
	eq(t, "exit", r.Code, 0)
}

func TestCursorMcpPretooluseStrreplaceDecoyFilePathRealPathDenied(t *testing.T) {
	r := preToolUse(t, "StrReplace", map[string]any{"file_path": "/tmp/cursor-ok.txt", "path": passwdHome(t) + "/.bashrc",
		"old_string": "a", "new_string": "b"})
	eq(t, "permission", lastJSON(t, r.Stdout)["permission"], any("deny"))
	eq(t, "exit", r.Code, 2)
}

func TestCursorMcpPretooluseEditnotebookDecoyFilePathTargetDenied(t *testing.T) {
	r := preToolUse(t, "EditNotebook", map[string]any{"file_path": "/tmp/cursor-ok.ipynb", "target_notebook": passwdHome(t) + "/.bashrc"})
	eq(t, "permission", lastJSON(t, r.Stdout)["permission"], any("deny"))
	eq(t, "exit", r.Code, 2)
}

// C4: drop beforeMCPExecution from HooksJSON and this test fails.
func TestCursorMcpBeforeMcpHookRequiredInPolicy(t *testing.T) {
	if _, present := sub(t, hooksOf(t, "coder", false), "hooks")["beforeMCPExecution"]; !present {
		t.Fatal("HooksJSON has no beforeMCPExecution")
	}
	if !strings.Contains(string(mustRead(t, filepath.Join(hooksDir(t), ch.AdapterName))), "beforeMCPExecution") {
		t.Fatal("the adapter does not know beforeMCPExecution")
	}
}

func mustRead(t testing.TB, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	ok(t, err)
	return b
}
