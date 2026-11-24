package installer

// Port of tests/test_hivemill_install.py (plugins/hivemill/scripts/install.py) onto go/internal/install and
// go/cmd/hivemill-install. Every machine effect goes through a fake Host over a temporary HOME; nothing here touches
// the real home, network, herdr or systemd. The real entry point is the built hivemill-install binary.
//
// Adaptations: Python imported the module and patched it (mock.patch of Host.mkdirs / HERDR_ASSETS / GH_ASSETS);
// here the fake Host fails mkdirs/symlink on request and the exported pin tables are swapped for the test. The
// Python resolve_latest / redact / suggested_max_agents unit calls are checked through the installer's output.
// The Python source-text rules are applied to the Go sources of the installer.
//
// not ported: TestSourceRules.test_python38_syntax - ast.parse of the Python source with feature_version 3.8; there is no Go counterpart
// not ported: TestFailureModes.test_download_builds_opener_with_https_only_redirect - mock.patch of urllib.request.build_opener (a Python internal); its wiring is covered through the real OSHost.Download redirect tests
//
// Extra (no Python case): dangling-symlink and symlink-loop checks on the tarball member, on install destinations and on
// the factory link, where the Go code extracts files and links.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"factory/go/internal/install"
)

const (
	hiRepo   = "https://github.com/volekerb/hivemill.git"
	hiOther  = "192.0.2.7" // documentation address standing in for a LAN bind the user chose
	hiAgents = "NAME ORG ROLE STATUS\nboss claude boss live\ndirector claude director live\n"
	hiTags   = "aaaa\trefs/tags/v0.1.0\naaaa\trefs/tags/v0.10.0\naaaa\trefs/tags/v0.9.0\naaaa\trefs/tags/v1.0.0-rc1\n"
	hiToken  = "ghp_" + "abcdefghijklmnopqrstuvwxyzABCDEFGHIJ" // ghp_ + 36 letters
)

var hiResultPass = regexp.MustCompile(`^RESULT PASS url=http://127\.0\.0\.1:[0-9]+/ version=[0-9]+\.[0-9]+\.[0-9]+$`)

type hiRule struct {
	sub  string
	resp func(argv []string) (int, string)
}

func hiRC(rc int, out string) func([]string) (int, string) {
	return func([]string) (int, string) { return rc, out }
}

type hiCall struct {
	argv   []string
	mutate bool
}

type hiMut struct {
	kind, arg string
	argv      []string
}

// hiHost records every call. rules map a substring of the joined argv to an answer.
type hiHost struct {
	*install.OSHost
	t                     *testing.T
	euid                  int
	tools                 map[string]bool
	ghAuth, systemd       bool
	tags, origin          string
	mem                   float64
	cpus                  int
	machine               string
	rules                 []hiRule
	assets                map[string][]byte
	calls                 []hiCall
	muts                  []hiMut
	popens                [][]string
	httpFn                func(url string) int
	mkdtempFn             func() (string, error)
	mkdirsErr, symlinkErr error
}

type hiOpt func(*hiHost)

func hiTools(names ...string) hiOpt {
	return func(h *hiHost) {
		h.tools = map[string]bool{}
		for _, n := range names {
			h.tools[n] = true
		}
	}
}
func hiEnv(env map[string]string) hiOpt { return func(h *hiHost) { h.Env = env } }
func hiRules(r ...hiRule) hiOpt         { return func(h *hiHost) { h.rules = r } }
func hiRule1(sub string, rc int, out string) hiRule {
	return hiRule{sub, hiRC(rc, out)}
}

// hiHome is a temporary HOME with the Claude credentials file Python's Base.setUp creates.
func hiHome(t *testing.T) string {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

func hiNew(t *testing.T, home string, opts ...hiOpt) *hiHost {
	t.Helper()
	h := &hiHost{
		OSHost: &install.OSHost{HomeDir: home, Env: map[string]string{"PATH": filepath.Join(home, ".local", "bin") + ":/usr/bin"}},
		t:      t, euid: 1000, ghAuth: true, systemd: true, tags: hiTags, origin: hiRepo, mem: 16, cpus: 8, machine: "x86_64",
		tools: map[string]bool{"git": true, "herdr": true, "gh": true, "claude": true}, assets: map[string][]byte{},
	}
	for _, o := range opts {
		o(h)
	}
	return h
}

func (h *hiHost) Euid() int                { return h.euid }
func (h *hiHost) Machine() string          { return h.machine }
func (h *hiHost) GOOS() string             { return "linux" }
func (h *hiHost) MemGiB() (float64, error) { return h.mem, nil }
func (h *hiHost) CPUCount() int            { return h.cpus }
func (h *hiHost) Sleep(time.Duration)      {}
func (h *hiHost) HTTPStatus(url string) int {
	if h.httpFn != nil {
		return h.httpFn(url)
	}
	return 200
}
func (h *hiHost) Mkdtemp() (string, error) {
	if h.mkdtempFn != nil {
		return h.mkdtempFn()
	}
	return h.OSHost.Mkdtemp()
}
func (h *hiHost) Mkdirs(path string) error {
	h.muts = append(h.muts, hiMut{kind: "mkdirs", arg: path})
	if h.mkdirsErr != nil {
		return h.mkdirsErr
	}
	return h.OSHost.Mkdirs(path)
}
func (h *hiHost) Rmtree(path string) {
	h.muts = append(h.muts, hiMut{kind: "rmtree", arg: path})
	h.OSHost.Rmtree(path)
}
func (h *hiHost) Symlink(target, link string) error {
	h.muts = append(h.muts, hiMut{kind: "symlink", arg: link})
	if h.symlinkErr != nil {
		return h.symlinkErr
	}
	return h.OSHost.Symlink(target, link)
}
func (h *hiHost) Download(url, dest string) error {
	h.muts = append(h.muts, hiMut{kind: "download", arg: url})
	b, ok := h.assets[url]
	if !ok {
		return &install.StepFail{Msg: "cannot fetch " + url}
	}
	return os.WriteFile(dest, b, 0o644)
}
func (h *hiHost) InstallFile(src, dest string, mode os.FileMode) error {
	h.muts = append(h.muts, hiMut{kind: "install_file", arg: dest})
	if err := h.OSHost.InstallFile(src, dest, mode); err != nil {
		return err
	}
	h.tools[filepath.Base(dest)] = true
	return nil
}
func (h *hiHost) Rename(src, dst string) error {
	h.muts = append(h.muts, hiMut{kind: "rename", arg: dst})
	return h.OSHost.Rename(src, dst)
}
func (h *hiHost) Popen(argv []string, logpath string) error {
	h.muts = append(h.muts, hiMut{kind: "popen", argv: append([]string(nil), argv...)})
	h.popens = append(h.popens, append([]string(nil), argv...))
	return nil
}

func (h *hiHost) Run(argv []string, cwd string, timeout time.Duration, mutate bool) (int, string) {
	argv = append([]string(nil), argv...)
	h.calls = append(h.calls, hiCall{argv, mutate})
	if mutate {
		h.muts = append(h.muts, hiMut{kind: "run", argv: argv})
	}
	joined := strings.Join(argv, " ")
	for _, r := range h.rules {
		if strings.Contains(joined, r.sub) {
			return r.resp(argv)
		}
	}
	a0, rest := filepath.Base(argv[0]), argv[1:]
	eq := func(want ...string) bool { return strings.Join(rest, "\x00") == strings.Join(want, "\x00") }
	has := func(s string) bool {
		for _, r := range rest {
			if r == s {
				return true
			}
		}
		return false
	}
	switch {
	case a0 == "git" && !h.tools["git"]: // a box without git answers nothing, like Host.run on OSError
		return 127, "git: not found"
	case a0 == "python3" && eq("--version"):
		return 0, "Python 3.12.1"
	case (a0 == "git" || a0 == "herdr" || a0 == "gh" || a0 == "claude") && len(rest) >= 1 && rest[0] == "--version":
		if h.tools[a0] {
			return 0, a0 + " 1.0\n"
		}
		return 127, "not found"
	case a0 == "gh" && len(rest) >= 2 && rest[0] == "auth" && rest[1] == "status":
		if h.ghAuth {
			return 0, "Logged in"
		}
		return 1, "You are not logged in"
	case a0 == "go":
		return 127, "not found"
	case a0 == "systemctl" && len(rest) >= 2 && rest[0] == "--user" && rest[1] == "show-environment":
		if h.systemd {
			return 0, ""
		}
		return 1, "Failed to connect to bus"
	case a0 == "git" && has("ls-remote"):
		return 0, h.tags
	case a0 == "git" && len(rest) >= 3 && strings.Join(rest[len(rest)-3:], " ") == "remote get-url origin":
		if h.origin != "" {
			return 0, h.origin + "\n"
		}
		return 128, "fatal: not a git repository"
	case a0 == "git" && len(rest) >= 1 && rest[0] == "clone":
		if err := os.MkdirAll(argv[len(argv)-1], 0o755); err != nil {
			h.t.Fatal(err)
		}
		return 0, ""
	case a0 == "factory":
		switch {
		case eq("init"):
			db := filepath.Join(h.HomeDir, "factory", "db")
			if err := os.MkdirAll(db, 0o755); err != nil {
				h.t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(db, "factory.db"), nil, 0o644); err != nil {
				h.t.Fatal(err)
			}
			return 0, "initialised /x\n"
		case eq("--version"):
			return 0, "hivemill 0.3.0 (git abc123)\n"
		case eq("agents"):
			return 0, hiAgents
		case len(rest) >= 2 && rest[0] == "project" && rest[1] == "list":
			return 0, "ID NAME\n"
		case len(rest) == 2 && rest[0] == "config":
			return 0, ""
		}
	}
	return 0, ""
}

func (h *hiHost) argvs() []string {
	out := make([]string, len(h.calls))
	for i, c := range h.calls {
		out[i] = strings.Join(c.argv, " ")
	}
	return out
}

func (h *hiHost) mutKinds() []string {
	out := make([]string, len(h.muts))
	for i, m := range h.muts {
		out[i] = m.kind
	}
	return out
}

func (h *hiHost) mutCount(kind string) int {
	n := 0
	for _, m := range h.muts {
		if m.kind == kind {
			n++
		}
	}
	return n
}

func (h *hiHost) mutateCalls() int {
	n := 0
	for _, c := range h.calls {
		if c.mutate {
			n++
		}
	}
	return n
}

// hiRun runs install.Main over the fake host and returns the exit code, stdout and stderr.
func hiRun(h *hiHost, argv ...string) (int, string, string) {
	var out, errb bytes.Buffer
	rc := install.Main(argv, h, &out, &errb)
	return rc, out.String(), errb.String()
}

func hiLast(out string) string {
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	return lines[len(lines)-1]
}

func hiIn(t *testing.T, s, sub string) {
	t.Helper()
	if !strings.Contains(s, sub) {
		t.Errorf("want %q in:\n%s", sub, s)
	}
}

func hiNotIn(t *testing.T, s, sub string) {
	t.Helper()
	if strings.Contains(s, sub) {
		t.Errorf("did not want %q in:\n%s", sub, s)
	}
}

func hiEq[T comparable](t *testing.T, got, want T, what string) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %v, want %v", what, got, want)
	}
}

func hiFind(list []string, pred func(string) bool) []string {
	var out []string
	for _, s := range list {
		if pred(s) {
			out = append(out, s)
		}
	}
	return out
}

func hiSub(sub string) func(string) bool {
	return func(s string) bool { return strings.Contains(s, sub) }
}

func hiExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func hiIndex(list []string, s string) int {
	for i, e := range list {
		if e == s {
			return i
		}
	}
	return -1
}

// hiMakeInstall makes ~/factory with (db) or without (partial clone) the database.
func hiMakeInstall(t *testing.T, home string, db bool) {
	t.Helper()
	fh := filepath.Join(home, "factory")
	sub := "bin"
	if db {
		sub = "db"
	}
	if err := os.MkdirAll(filepath.Join(fh, sub), 0o755); err != nil {
		t.Fatal(err)
	}
	if db {
		if err := os.WriteFile(filepath.Join(fh, "db", "factory.db"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// hiBin runs the built hivemill-install with exactly env (no inherited variables).
func hiBin(t *testing.T, env []string, args ...string) (int, string, string) {
	t.Helper()
	c := exec.Command(buildCmd(t, "hivemill-install"), args...)
	c.Env = env
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	done := make(chan error, 1)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { done <- c.Wait() }()
	select {
	case err := <-done:
		rc := 0
		if err != nil {
			var ee *exec.ExitError
			if !errors.As(err, &ee) {
				t.Fatal(err)
			}
			rc = ee.ExitCode()
		}
		return rc, out.String(), errb.String()
	case <-time.After(60 * time.Second):
		_ = c.Process.Kill()
		t.Fatalf("hivemill-install %v hung", args)
	}
	return -1, "", ""
}

func hiPlainEnv(home string) []string {
	return []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
}

func hiSHA(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
