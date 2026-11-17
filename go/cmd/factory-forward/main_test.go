package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"factory/go/internal/forward"
)

// The test binary plays three programs, chosen by the name it is run under (symlinks in a temp bin dir):
//   - ssh:             a fake ssh that checks its argv and runs the receiver locally with a clean "HQ" env
//   - factory-forward: the real main() (client, or server with --serve)
//   - factory:         a fake bin/factory that reports argv, env, cwd and a stdin digest as JSON, or exits a code
func TestMain(m *testing.M) {
	switch filepath.Base(os.Args[0]) {
	case "ssh":
		syscall.Exit(fakeSSH(os.Args[1:])) // syscall.Exit: no 1 s race-detector exit sleep in the fakes
	case "factory-forward":
		main()
		return
	case "factory":
		syscall.Exit(fakeFactory(os.Args[1:]))
	}
	os.Exit(m.Run())
}

const hqHost = "hqhost"

// raceNoSleep keeps `go test -race` from adding a 1 s exit sleep to every factory-forward process.
const raceNoSleep = "GORACE=atexit_sleep_ms=0"

// fakeSSH stands in for ssh to the HQ machine. FAKE_SSH_MODE=connfail fails like a refused connection.
func fakeSSH(args []string) int {
	if os.Getenv("FAKE_SSH_MODE") == "connfail" {
		fmt.Fprintln(os.Stderr, "ssh: connect to host hqhost port 22: Connection refused")
		return 255
	}
	receiver := os.Getenv("FAKE_SSH_RECEIVER")
	want := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "--", hqHost, receiver, "--serve"}
	if !slices.Equal(args, want) {
		fmt.Fprintf(os.Stderr, "fake ssh: unexpected argv %q\n", args)
		return 255
	}
	cmd := exec.Command(receiver, "--serve")
	// The remote side gets its own env, nothing from the client process.
	cmd.Env = []string{
		"HOME=" + os.Getenv("FAKE_SSH_SERVER_HOME"),
		"PATH=/usr/bin:/bin",
		"FORWARD_CONFIG=" + os.Getenv("FAKE_SSH_SERVER_CONFIG"),
		"SERVER_ONLY_SECRET=hq-side-value",
		"FACTORY_AGENT=hq-own-agent",
		raceNoSleep,
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake ssh:", err)
		return 255
	}
	return 0
}

type report struct {
	Argv      []string `json:"argv"`
	Env       []string `json:"env"`
	Cwd       string   `json:"cwd"`
	StdinSHA  string   `json:"stdin_sha256"`
	StdinSize int      `json:"stdin_size"`
}

// fakeFactory: `exit N` exits N; otherwise it prints a report. It reads stdin only when an argument is "-".
func fakeFactory(args []string) int {
	if len(args) == 2 && args[0] == "exit" {
		n, err := strconv.Atoi(args[1])
		if err != nil {
			return 99
		}
		fmt.Fprintf(os.Stderr, "fake factory exits %d\n", n)
		return n
	}
	cwd, err := os.Getwd()
	if err != nil {
		return 98
	}
	r := report{Argv: args, Env: os.Environ(), Cwd: cwd}
	if slices.Contains(args, "-") {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return 97
		}
		sum := sha256.Sum256(b)
		r.StdinSHA, r.StdinSize = hex.EncodeToString(sum[:]), len(b)
	}
	if err := json.NewEncoder(os.Stdout).Encode(r); err != nil {
		return 96
	}
	return 0
}

type rig struct {
	t                         *testing.T
	bin, serverHome, clientWD string
	serverCfg, clientCfg      string
	pathFrom, pathTo          string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	r := &rig{t: t, bin: filepath.Join(root, "bin"), serverHome: filepath.Join(root, "hqhome")}
	for _, d := range []string{r.bin, r.serverHome} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"ssh", "factory-forward", "factory"} {
		if err := os.Symlink(self, filepath.Join(r.bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	// Client tree <root>/home/user/factory/worktrees/X maps to <root>/srv/f/worktrees/X on the "HQ" side.
	r.pathFrom = filepath.Join(root, "home", "user", "factory")
	r.pathTo = filepath.Join(root, "srv", "f")
	r.clientWD = filepath.Join(r.pathFrom, "worktrees", "X")
	for _, d := range []string{r.clientWD, filepath.Join(r.pathTo, "worktrees", "X")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r.serverCfg = writeJSON(t, filepath.Join(root, "server.json"), map[string]any{
		"factory_bin": filepath.Join(r.bin, "factory"),
		"path_map":    [][]string{{r.pathFrom, r.pathTo}},
	})
	r.clientCfg = writeJSON(t, filepath.Join(root, "client.json"), map[string]any{
		"hq_host":         hqHost,
		"remote_receiver": filepath.Join(r.bin, "factory-forward"),
	})
	return r
}

func writeJSON(t *testing.T, path string, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type result struct {
	code           int
	stdout, stderr string
	took           time.Duration
}

// client runs `factory-forward args...` from dir with the extra env on top of the rig's env.
func (r *rig) client(dir string, stdin io.Reader, extraEnv []string, args ...string) result {
	r.t.Helper()
	cmd := exec.Command(filepath.Join(r.bin, "factory-forward"), args...)
	cmd.Dir = dir
	cmd.Env = append([]string{
		"PATH=" + r.bin,
		"HOME=" + r.t.TempDir(),
		forward.ConfigEnv + "=" + r.clientCfg,
		"FAKE_SSH_RECEIVER=" + filepath.Join(r.bin, "factory-forward"),
		"FAKE_SSH_SERVER_HOME=" + r.serverHome,
		"FAKE_SSH_SERVER_CONFIG=" + r.serverCfg,
		raceNoSleep,
	}, extraEnv...)
	cmd.Stdin = stdin
	return r.run(cmd)
}

// serve runs `factory-forward --serve` directly with a hand-made stdin (no client in front).
func (r *rig) serve(stdin []byte) result {
	r.t.Helper()
	cmd := exec.Command(filepath.Join(r.bin, "factory-forward"), "--serve")
	cmd.Env = []string{"HOME=" + r.serverHome, "PATH=/usr/bin:/bin", forward.ConfigEnv + "=" + r.serverCfg, raceNoSleep}
	cmd.Stdin = bytes.NewReader(stdin)
	return r.run(cmd)
}

func (r *rig) run(cmd *exec.Cmd) result {
	r.t.Helper()
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	start := time.Now()
	err := cmd.Run()
	res := result{stdout: out.String(), stderr: errb.String(), took: time.Since(start)}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		res.code = ee.ExitCode()
	default:
		r.t.Fatalf("run %v: %v", cmd.Args, err)
	}
	return res
}

func (res result) report(t *testing.T) report {
	t.Helper()
	if res.code != 0 {
		t.Fatalf("exit %d, stderr %q", res.code, res.stderr)
	}
	var rep report
	if err := json.Unmarshal([]byte(res.stdout), &rep); err != nil {
		t.Fatalf("stdout is not one fake-factory report: %v\n%q", err, res.stdout)
	}
	return rep
}

func envOf(rep report, key string) (string, bool) {
	for _, kv := range rep.Env {
		if k, v, _ := strings.Cut(kv, "="); k == key {
			return v, true
		}
	}
	return "", false
}

// rawHeader builds a wire header from any JSON body, bypassing the client's own checks.
func rawHeader(body []byte) []byte {
	out := make([]byte, 8, 8+len(body))
	copy(out, forward.Magic)
	binary.BigEndian.PutUint32(out[4:], uint32(len(body)))
	return append(out, body...)
}

// Acceptance 3.
func TestForwardAllowedEnvPasses(t *testing.T) {
	r := newRig(t)
	rep := r.client(r.clientWD, nil, []string{"FACTORY_AGENT=a", "FACTORY_ROLE=coder", "FACTORY_TASK=T-1"}, "task", "show", "T-1").report(t)
	var got []string
	for _, kv := range rep.Env {
		if strings.HasPrefix(kv, "FACTORY_") && !strings.HasPrefix(kv, "FACTORY_FORWARDED=") {
			got = append(got, kv)
		}
	}
	want := []string{"FACTORY_AGENT=a", "FACTORY_ROLE=coder", "FACTORY_TASK=T-1"}
	if !slices.Equal(got, want) {
		t.Fatalf("FACTORY_* in the CLI env = %q, want exactly %q (the HQ's own FACTORY_AGENT must not leak)", got, want)
	}
	if v, _ := envOf(rep, forward.ForwardedEnv); v != "1" {
		t.Fatalf("FACTORY_FORWARDED = %q, want 1", v)
	}
	if !slices.Equal(rep.Argv, []string{"task", "show", "T-1"}) {
		t.Fatalf("argv = %q", rep.Argv)
	}
}

// Acceptance 4, first half: non-allow-listed client env never reaches the CLI.
func TestForwardDisallowedEnvDropped(t *testing.T) {
	r := newRig(t)
	rep := r.client(r.clientWD, nil, []string{
		"HOME=/client/home", "GH_TOKEN=client-leak-1", "ANTHROPIC_API_KEY=client-leak-2",
		"FACTORY_FORWARDED=0", "FACTORY_HOME=/client/fh", "FACTORY_AGENT=a",
	}, "inbox").report(t)
	for _, k := range []string{"GH_TOKEN", "ANTHROPIC_API_KEY", "SERVER_ONLY_SECRET", "FACTORY_HOME"} {
		if v, ok := envOf(rep, k); ok {
			t.Errorf("%s reached the CLI (value %q)", k, v)
		}
	}
	for _, kv := range rep.Env {
		if strings.Contains(kv, "client-") || strings.Contains(kv, "/client/") {
			t.Errorf("client value leaked: %q", kv)
		}
	}
	if v, _ := envOf(rep, "HOME"); v != r.serverHome {
		t.Errorf("HOME = %q, want the HQ home %q", v, r.serverHome)
	}
	if v, _ := envOf(rep, forward.ForwardedEnv); v != "1" {
		t.Errorf("FACTORY_FORWARDED = %q, want 1 (the client cannot set it)", v)
	}
}

// Acceptance 4, second half: a hand-made header with a key outside the list is refused and runs nothing.
func TestForwardServerRefusesEnvKey(t *testing.T) {
	r := newRig(t)
	for _, key := range []string{"GH_TOKEN", "FACTORY_FORWARDED", "HOME", "PATH", "LD_PRELOAD"} {
		t.Run(key, func(t *testing.T) {
			body := fmt.Sprintf(`{"argv":["inbox"],"env":{"FACTORY_AGENT":"a",%q:"value-must-not-print"},"cwd":%q}`, key, r.clientWD)
			res := r.serve(rawHeader([]byte(body)))
			if res.code != 2 {
				t.Fatalf("exit %d, want 2; stderr %q", res.code, res.stderr)
			}
			if want := "forward: env key not allowed: " + key; !strings.Contains(res.stderr, want) {
				t.Fatalf("stderr %q does not contain %q", res.stderr, want)
			}
			if strings.Contains(res.stderr, "value-must-not-print") {
				t.Fatalf("stderr shows the value: %q", res.stderr)
			}
			if res.stdout != forward.Ack {
				t.Fatalf("stdout %q: something ran", res.stdout)
			}
		})
	}
}

// The server accepts a well-formed hand-made header (the accept side of the guard above).
func TestForwardServerAcceptsAllowedHeader(t *testing.T) {
	r := newRig(t)
	body := fmt.Sprintf(`{"argv":["inbox"],"env":{"FACTORY_AGENT":"a","FACTORY_ORG":"claude"},"cwd":%q}`, r.clientWD)
	res := r.serve(rawHeader([]byte(body)))
	res.stdout = strings.TrimPrefix(res.stdout, forward.Ack)
	rep := res.report(t)
	if v, _ := envOf(rep, "FACTORY_ORG"); v != "claude" {
		t.Fatalf("FACTORY_ORG = %q", v)
	}
}

// Acceptance 5.
func TestForwardExitCodes(t *testing.T) {
	r := newRig(t)
	for _, n := range []int{0, 1, 2, 3, 42, 255} {
		res := r.client(r.clientWD, nil, nil, "exit", strconv.Itoa(n))
		if res.code != n {
			t.Errorf("CLI exit %d: client exit %d (stderr %q)", n, res.code, res.stderr)
		}
		if want := fmt.Sprintf("fake factory exits %d\n", n); res.stderr != want {
			t.Errorf("CLI exit %d: stderr %q, want the CLI's own %q", n, res.stderr, want)
		}
		if res.stdout != "" {
			t.Errorf("CLI exit %d: stdout %q, want empty (Ack stripped)", n, res.stdout)
		}
	}
}

// Acceptance 5: ssh fails before the server runs.
func TestForwardSSHFailure(t *testing.T) {
	r := newRig(t)
	res := r.client(r.clientWD, nil, []string{"FAKE_SSH_MODE=connfail"}, "inbox")
	if res.code != 255 {
		t.Fatalf("exit %d, want 255", res.code)
	}
	if !strings.HasPrefix(res.stderr, "forward: ssh to hqhost failed") {
		t.Fatalf("stderr %q does not start with the forward line", res.stderr)
	}
	if strings.Count(res.stderr, "\n") != 1 || !strings.Contains(res.stderr, "Connection refused") {
		t.Fatalf("stderr %q: want one line carrying ssh's reason", res.stderr)
	}
}

// ssh missing from PATH is an ssh failure too.
func TestForwardSSHMissing(t *testing.T) {
	r := newRig(t)
	if err := os.Remove(filepath.Join(r.bin, "ssh")); err != nil {
		t.Fatal(err)
	}
	res := r.client(r.clientWD, nil, nil, "inbox")
	if res.code != 255 || !strings.HasPrefix(res.stderr, "forward: ssh to hqhost failed") {
		t.Fatalf("exit %d stderr %q", res.code, res.stderr)
	}
}

// Acceptance 6: a 1 MiB '-' payload arrives byte-identical.
func TestForwardStdinOneMiB(t *testing.T) {
	r := newRig(t)
	payload := make([]byte, 1<<20)
	rand.New(rand.NewSource(1582)).Read(payload)
	sum := sha256.Sum256(payload)
	// A real pipe, as a heredoc gives the shell.
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		defer pw.Close()
		if _, err := pw.Write(payload); err != nil {
			t.Errorf("write payload: %v", err)
		}
	}()
	res := r.client(r.clientWD, pr, nil, "plan", "G-1", "-")
	if err := pr.Close(); err != nil {
		t.Fatal(err)
	}
	rep := res.report(t)
	if rep.StdinSize != len(payload) || rep.StdinSHA != hex.EncodeToString(sum[:]) {
		t.Fatalf("stdin arrived as %d bytes sha %s, want %d bytes sha %x", rep.StdinSize, rep.StdinSHA, len(payload), sum)
	}
}

// Acceptance 6: no stdin, an empty closed pipe, and an open pipe the CLI never reads all return within 2 s.
func TestForwardNoStdinNoHang(t *testing.T) {
	r := newRig(t)
	empty, ew, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := ew.Close(); err != nil {
		t.Fatal(err)
	}
	openR, openW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { openW.Close(); openR.Close(); empty.Close() })
	cases := []struct {
		name  string
		stdin io.Reader
		args  []string
		size  int
	}{
		{"no stdin, reads -", nil, []string{"plan", "G-1", "-"}, 0},
		{"empty pipe, reads -", empty, []string{"plan", "G-1", "-"}, 0},
		{"open pipe, CLI does not read", openR, []string{"inbox"}, -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := r.client(r.clientWD, c.stdin, nil, c.args...)
			if res.took > 2*time.Second {
				t.Fatalf("took %v, want < 2s", res.took)
			}
			rep := res.report(t)
			if c.size >= 0 && rep.StdinSize != c.size {
				t.Fatalf("stdin size %d, want %d", rep.StdinSize, c.size)
			}
		})
	}
}

// Acceptance 7.
func TestForwardCwdMapping(t *testing.T) {
	r := newRig(t)
	rep := r.client(r.clientWD, nil, nil, "inbox").report(t)
	if want := filepath.Join(r.pathTo, "worktrees", "X"); rep.Cwd != want {
		t.Fatalf("mapped cwd %q, want %q", rep.Cwd, want)
	}
	rep = r.client(t.TempDir(), nil, nil, "inbox").report(t)
	if rep.Cwd != r.serverHome {
		t.Fatalf("unmapped cwd %q, want the HQ home %q", rep.Cwd, r.serverHome)
	}
}

// Acceptance 8: shell metacharacters arrive as literal arguments.
func TestForwardArgvLiteral(t *testing.T) {
	r := newRig(t)
	args := []string{"msg", "x", "a; rm -rf /", "`id`", "$HOME", "it's \"q\"", "", "  spaced  ", "line1\nline2", "--serve"}
	rep := r.client(r.clientWD, nil, nil, args...).report(t)
	if !slices.Equal(rep.Argv, args) {
		t.Fatalf("argv %q, want %q", rep.Argv, args)
	}
}

// Acceptance 11: a 65 KiB header is refused and nothing runs.
func TestForwardHeaderTooLarge(t *testing.T) {
	r := newRig(t)
	body := fmt.Sprintf(`{"argv":["msg",%q],"env":{},"cwd":%q}`, strings.Repeat("a", 65<<10), r.clientWD)
	res := r.serve(rawHeader([]byte(body)))
	if res.code != 2 || !strings.Contains(res.stderr, "forward: header too large") {
		t.Fatalf("exit %d stderr %q, want 2 and the header-too-large refusal", res.code, res.stderr)
	}
	if res.stdout != forward.Ack {
		t.Fatalf("stdout %q: the CLI ran", res.stdout)
	}
	// The client refuses before it even calls ssh.
	cres := r.client(r.clientWD, nil, []string{"FAKE_SSH_MODE=connfail"}, "msg", strings.Repeat("a", 65<<10))
	if cres.code != 2 || !strings.HasPrefix(cres.stderr, "forward: header too large") {
		t.Fatalf("client: exit %d stderr %q", cres.code, cres.stderr)
	}
}

// Client config errors exit 2 and call no ssh.
func TestForwardClientConfigRefused(t *testing.T) {
	r := newRig(t)
	for name, cfg := range map[string]map[string]any{
		"factory in host": {"hq_host": "factory-hq", "remote_receiver": "/x/factory-forward"},
		"option as host":  {"hq_host": "-oProxyCommand=x", "remote_receiver": "/x/factory-forward"},
		"shell receiver":  {"hq_host": "hqhost", "remote_receiver": "/x/ff; id"},
		"unknown key":     {"hq_host": "hqhost", "remote_receiver": "/x/ff", "extra": 1},
	} {
		t.Run(name, func(t *testing.T) {
			writeJSON(t, r.clientCfg, cfg)
			res := r.client(r.clientWD, nil, []string{"FAKE_SSH_MODE=connfail"}, "inbox")
			if res.code != 2 || !strings.HasPrefix(res.stderr, "forward: config") {
				t.Fatalf("exit %d stderr %q", res.code, res.stderr)
			}
		})
	}
}
