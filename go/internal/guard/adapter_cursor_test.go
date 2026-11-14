package guard

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// G-048 P2a: the Cursor adapter and boss-inbox modes of the worker-guard binary. The Python references are
// roles/hooks/cursor-guard-adapter.py (tests/test_cursor_guard_adapter.py, test_cursor_pilot.py, test_cursor_mcp.py)
// and roles/hooks/boss-inbox.py (tests/test_boss_mail.py).

type adapterCase struct {
	Name       string  `json:"name"`
	Role       *string `json:"role"`
	HQ         bool    `json:"hq"`
	Event      *string `json:"event"`
	Stdin      string  `json:"stdin"`
	Permission *string `json:"permission"`
	Exit       *int    `json:"exit"`
}

func loadAdapterCases(t *testing.T) []adapterCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/adapter/cursor_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	var cases []adapterCase
	if err := json.Unmarshal(bytes.ReplaceAll(raw, []byte("__HOME__"), []byte(u.HomeDir)), &cases); err != nil {
		t.Fatal(err)
	}
	return cases
}

func (c adapterCase) args() []string {
	var a []string
	if c.Role != nil {
		a = append(a, "--role", *c.Role)
	}
	if c.HQ {
		a = append(a, "--hq")
	}
	if c.Event != nil {
		a = append(a, "--event", *c.Event)
	}
	return a
}

var (
	binOnce sync.Once
	binPath string
	binErr  error
)

// guardBinary builds cmd/worker-guard once per test run (offline, stdlib only).
func guardBinary(t *testing.T) string {
	t.Helper()
	binOnce.Do(func() {
		dir, err := os.MkdirTemp("", "wg-bin-")
		if err != nil {
			binErr = err
			return
		}
		binPath = filepath.Join(dir, "wg")
		cmd := exec.Command("go", "build", "-o", binPath, "../../cmd/worker-guard")
		cmd.Env = append(os.Environ(), "GOPROXY=off")
		if out, err := cmd.CombinedOutput(); err != nil {
			binErr = &buildError{err, string(out)}
		}
	})
	if binErr != nil {
		t.Fatalf("building worker-guard: %v", binErr)
	}
	return binPath
}

type buildError struct {
	err error
	out string
}

func (b *buildError) Error() string { return b.err.Error() + ": " + b.out }

type runResult struct {
	stdout, stderr string
	code           int
}

func runCmd(t *testing.T, env []string, stdin string, name string, args ...string) runResult {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = env
	cmd.Stdin = strings.NewReader(stdin)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return runResult{so.String(), se.String(), code}
}

func adapterEnv(t *testing.T, extra ...string) []string {
	t.Helper()
	return append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "FACTORY_HOME=" + t.TempDir()}, extra...)
}

func pythonAdapter(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed: the parity test needs the Python reference")
	}
	p, err := filepath.Abs("../../../roles/hooks/cursor-guard-adapter.py")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Outcomes through the real binary: item 3 (blocked and allowed) and item 4 (malformed input fails closed).
func TestCursorAdapterOutcomes(t *testing.T) {
	bin := guardBinary(t)
	for _, c := range loadAdapterCases(t) {
		if c.Permission == nil {
			continue
		}
		t.Run(c.Name, func(t *testing.T) {
			r := runCmd(t, adapterEnv(t), c.Stdin, bin, append([]string{"--cursor-adapter"}, c.args()...)...)
			lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
			var body map[string]any
			if err := json.Unmarshal([]byte(lines[len(lines)-1]), &body); err != nil {
				t.Fatalf("stdout %q is not a JSON answer: %v", r.stdout, err)
			}
			if body["permission"] != *c.Permission || r.code != *c.Exit {
				t.Errorf("got %v exit %d, want %s exit %d (stderr %q)", body["permission"], r.code, *c.Permission, *c.Exit, r.stderr)
			}
			if *c.Permission == "deny" && body["agent_message"] == "" {
				t.Errorf("deny without a reason: %q", r.stdout)
			}
		})
	}
}

// Every fixture through the Python adapter and the Go mode: equal stdout and exit.
func TestCursorAdapterParity(t *testing.T) {
	py := pythonAdapter(t)
	bin := guardBinary(t)
	for _, c := range loadAdapterCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			env := adapterEnv(t)
			want := runCmd(t, env, c.Stdin, "python3", append([]string{py}, c.args()...)...)
			got := runCmd(t, env, c.Stdin, bin, append([]string{"--cursor-adapter"}, c.args()...)...)
			if got.stdout != want.stdout || got.code != want.code {
				t.Errorf("go: exit %d %q\npython: exit %d %q", got.code, got.stdout, want.code, want.stdout)
			}
		})
	}
}

// The decision log (FACTORY_CURSOR_HOOK_LOG) holds the same record apart from the timestamp.
func TestCursorAdapterLogParity(t *testing.T) {
	py := pythonAdapter(t)
	bin := guardBinary(t)
	in := `{"command": "git push origin main", "cwd": "/tmp"}`
	read := func(name string, run func(env []string) runResult) map[string]string {
		log := filepath.Join(t.TempDir(), name+".log")
		run(adapterEnv(t, "FACTORY_CURSOR_HOOK_LOG="+log, "FACTORY_AGENT=a1", "FACTORY_ROLE=coder"))
		raw, err := os.ReadFile(log)
		if err != nil {
			t.Fatalf("%s wrote no log: %v", name, err)
		}
		var rec map[string]string
		if err := json.Unmarshal(raw, &rec); err != nil {
			t.Fatal(err)
		}
		if _, err := time.Parse("2006-01-02T15:04:05-07:00", rec["ts"]); err != nil {
			t.Errorf("%s ts %q: %v", name, rec["ts"], err)
		}
		delete(rec, "ts")
		return rec
	}
	a := read("py", func(env []string) runResult {
		return runCmd(t, env, in, "python3", py, "--role", "coder", "--event", "beforeShellExecution")
	})
	b := read("go", func(env []string) runResult {
		return runCmd(t, env, in, bin, "--cursor-adapter", "--role", "coder", "--event", "beforeShellExecution")
	})
	if len(a) != 4 || len(a) != len(b) {
		t.Fatalf("records differ: py %v go %v", a, b)
	}
	for k, v := range a {
		if b[k] != v {
			t.Errorf("log %s: go %q, python %q", k, b[k], v)
		}
	}
	if a["decision"] != "deny" || a["agent"] != "a1" || a["role"] != "coder" {
		t.Errorf("unexpected record %v", a)
	}
}

// Arguments the Python adapter refuses (argparse) are exit 2 here too, never an allow.
func TestCursorAdapterBadArgsExit2(t *testing.T) {
	bin := guardBinary(t)
	for _, args := range [][]string{{"--bogus"}, {"--role"}, {"--event"}} {
		r := runCmd(t, adapterEnv(t), `{"command":"git status"}`, bin, append([]string{"--cursor-adapter"}, args...)...)
		if r.code != 2 || strings.Contains(r.stdout, "allow") {
			t.Errorf("%v: exit %d stdout %q", args, r.code, r.stdout)
		}
	}
}

// Boss inbox: a fake `factory` on PATH, the Python hook and the Go mode on the same inputs.
func TestBossInboxParity(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed: the parity test needs the Python reference")
	}
	py, err := filepath.Abs("../../../roles/hooks/boss-inbox.py")
	if err != nil {
		t.Fatal(err)
	}
	bin := guardBinary(t)
	long := strings.Repeat("é✓ line of mail\n", 700)
	cases := []struct{ name, script string }{
		{"empty output", "exit 0"},
		{"inbox empty", "echo 'inbox empty'"},
		{"one message", "echo 'M1 from director: hi'"},
		{"padded message", "printf '\\n\\n  M1 hi  \\n\\n'"},
		{"crlf", "printf 'a\\r\\nb\\r\\n'"},
		{"long output truncated", "printf '%s' '" + long + "'"},
		{"exactly at the limit", "head -c 7871 /dev/zero | tr '\\0' x"},
		{"one over the limit", "head -c 7872 /dev/zero | tr '\\0' x"},
		{"unicode", "echo 'привет ✓ 𝄞'"},
		{"factory fails with output", "echo 'M1 hi'; exit 3"},
		{"factory fails silently", "exit 1"},
		{"invalid utf-8", "printf '\\377\\376'"},
		{"hangs-free stderr only", "echo oops >&2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "factory"), []byte("#!/bin/sh\n"+c.script+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			env := []string{"PATH=" + dir + ":" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
			for _, stdin := range []string{"", `{"prompt":"hi"}`, "not json \xff"} {
				want := runCmd(t, env, stdin, "python3", py)
				got := runCmd(t, env, stdin, bin, "--boss-inbox")
				if got.stdout != want.stdout || got.code != want.code || got.code != 0 {
					t.Errorf("stdin %q: go exit %d %q\npython exit %d %q", stdin, got.code, got.stdout, want.code, want.stdout)
				}
			}
		})
	}
	// factory missing and an unreadable inbox: silent exit 0 on both sides
	empty := []string{"PATH=" + t.TempDir()}
	pyBin, _ := exec.LookPath("python3")
	want := runCmd(t, empty, "", pyBin, py)
	got := runCmd(t, empty, "", bin, "--boss-inbox")
	if want.code != 0 || got.code != 0 || got.stdout != want.stdout || got.stdout != "" {
		t.Errorf("missing factory: go %d %q, python %d %q", got.code, got.stdout, want.code, want.stdout)
	}
}

// Without the Python reference: the boss-inbox answer has the UserPromptSubmit shape and the truncation tail.
func TestBossInboxShape(t *testing.T) {
	var out bytes.Buffer
	run := func() ([]byte, int, error) { return []byte(strings.Repeat("x", 9000)), 0, nil }
	if code := BossInbox(strings.NewReader("junk"), &out, run); code != 0 {
		t.Fatalf("exit %d", code)
	}
	var body struct {
		H struct {
			Name string `json:"hookEventName"`
			Ctx  string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.H.Name != "UserPromptSubmit" || !strings.HasPrefix(body.H.Ctx, bossInboxHeader+"\n") || !strings.HasSuffix(body.H.Ctx, bossInboxTail) {
		t.Errorf("unexpected answer %q", out.String())
	}
	out.Reset()
	for _, run := range []InboxRunner{
		func() ([]byte, int, error) { return nil, 0, os.ErrNotExist },
		func() ([]byte, int, error) { return []byte("x"), 1, nil },
		func() ([]byte, int, error) { return []byte(" inbox empty\n"), 0, nil },
	} {
		if code := BossInbox(strings.NewReader(""), &out, run); code != 0 || out.Len() != 0 {
			t.Errorf("exit %d output %q, want silent 0", code, out.String())
		}
	}
}
