package guard2

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
)

// Port of tests/test_worker_guard_retro_mirror.py (G-036 SC4): retro Write cwd/tilde, unicode-space redirect stderr,
// FuzzCheck retro parsers.

const rmWhy = "the retro agent may not edit files under <live>/roles/; role prompts and hooks are changed only by the owner and the tick"
const rmTail = ". Workers may not run herdr or manage factory/herdr systemd units directly; use the factory CLI (factory done, factory msg, ...). Tell your lead if this is wrong.\n"

var (
	rmRedirectBlock = "worker-guard: blocked (a redirect built by a substitution into a role file: " + rmWhy + ")" + rmTail
	rmU001cBlock    = "worker-guard: blocked (a redirect into a protected config/init file: it would persist config " +
		"into the live box (git config keys like core.fsmonitor or diff.external, and shell init files, " +
		"run later as the tick and other agents); only the owner changes ~/.gitconfig, ~/.config/git " +
		"and shell rc files)" + rmTail
)

func rmWriteBlock(path string) string {
	return "worker-guard: blocked (" + path + "): " + rmWhy + ". Tell your lead if this is wrong.\n"
}

func rmWrite(t *testing.T, filePath, payloadCwd, hookCwd string, set map[string]string) res {
	t.Helper()
	p := map[string]any{"tool_name": "Write", "tool_input": map[string]any{"file_path": filePath, "content": "x"}}
	if payloadCwd != "" {
		p["cwd"] = payloadCwd
	}
	return hookJSON(t, p, hookOpts{args: roleArgs("retro"), cwd: hookCwd, set: set})
}

func TestRetroMirrorWriteUnresolvedCwd(t *testing.T) {
	tmp := realTemp(t)
	// The ~+ case needs the hook cwd to be the real live factory dir (the guard resolves the live home from the
	// account, not from HOME). A subprocess cwd must exist; like the Python test, create it only when missing.
	live := liveHome(t)
	mkdirs(t, live)
	var set map[string]string

	r := rmWrite(t, "~nosuchuser123/factory/roles/coder.md", "/tmp", tmp, set)
	wantExit(t, "~user", r, 2)
	if r.stderr != rmWriteBlock("'~nosuchuser123/factory/roles/coder.md'") {
		t.Errorf("~user stderr = %q", r.stderr)
	}
	r = rmWrite(t, "~+/roles/coder.md", "", live, set)
	wantExit(t, "~+", r, 2)
	if r.stderr != rmWriteBlock("'~+/roles/coder.md'") {
		t.Errorf("~+ stderr = %q", r.stderr)
	}
	for _, c := range []struct{ path, cwd string }{
		{"roles/coder.md", "/no/such/retro-cwd-unresolved"},
		{"/tmp/zz-retro-mirror-ok.txt", "/tmp"},
	} {
		r = rmWrite(t, c.path, c.cwd, tmp, set)
		wantExit(t, c.path, r, 0)
		if r.stderr != "" {
			t.Errorf("%s: stderr %q, want empty", c.path, r.stderr)
		}
	}
}

func TestRetroMirrorUnicodeSpaceRedirect(t *testing.T) {
	rolesTarget := liveHome(t) + "/roles/coder.md"
	run := func(cmd string) res {
		return hookJSON(t, bashPayload(cmd), hookOpts{args: roleArgs("retro")})
	}
	for _, sep := range []string{"\u001c", " "} {
		allowed := "echo x >" + sep + ` "$(printf %s /tmp/zz-retro-note.txt)"`
		r := run(allowed)
		wantExit(t, "allowed sep", r, 0)
		if r.stderr != "" {
			t.Errorf("%q allowed: stderr %q", sep, r.stderr)
		}
		blocked := "echo x >" + sep + ` "$(printf %s ` + rolesTarget + `)"`
		r = run(blocked)
		wantExit(t, "blocked sep", r, 2)
		if r.stderr != rmRedirectBlock {
			t.Errorf("%q blocked: stderr %q", sep, r.stderr)
		}
	}
	r := run("echo x >\u001c$(printf %s /tmp/zz-retro-note.txt)")
	wantExit(t, "unquoted U+001C", r, 2)
	if r.stderr != rmU001cBlock {
		t.Errorf("unquoted: stderr %q", r.stderr)
	}
}

var rmParserRE = regexp.MustCompile(`(?m)^func (retro(?:PathExpr|QuoteUnquote|SplitShellWords|PrintfSegment|ResolvePathExpr|SubstRedirectFrags))\(`)
var rmFuzzRE = regexp.MustCompile(`func FuzzCheck\(f \*testing\.F\) \{`)

func rmFuzzBody(src string) string {
	loc := rmFuzzRE.FindStringIndex(src)
	if loc == nil {
		return ""
	}
	depth := 0
	for j := loc[1] - 1; j < len(src); j++ {
		switch src[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[loc[0] : j+1]
			}
		}
	}
	return src[loc[0]:]
}

func TestRetroMirrorFuzzcheckRoleRetro(t *testing.T) {
	mod, err := ft.ModuleRoot()
	if err != nil {
		t.Fatal(err)
	}
	fuzz, err := os.ReadFile(filepath.Join(mod, "internal/guard/fuzz_test.go"))
	if err != nil {
		t.Fatalf("read fuzz_test.go: %v", err)
	}
	retro, err := os.ReadFile(filepath.Join(mod, "internal/guard/retro.go"))
	if err != nil {
		t.Fatalf("read retro.go: %v", err)
	}
	body := rmFuzzBody(string(fuzz))
	if !strings.Contains(body, "retroRole") {
		t.Error("FuzzCheck does not mention retroRole")
	}
	for _, m := range rmParserRE.FindAllStringSubmatch(string(retro), -1) {
		if !strings.Contains(body, m[1]) {
			t.Errorf("FuzzCheck does not mention %s", m[1])
		}
	}
}
