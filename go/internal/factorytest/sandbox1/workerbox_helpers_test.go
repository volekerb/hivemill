package sandbox1

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"factory/go/internal/sandbox"
)

// Helpers of the tests/test_workerbox.py port (every name here starts with wb). The Python probe (a script standing in
// for claude) and the fake `factory` stub are this very test binary: wbProbeInit, an init() that runs before TestMain,
// turns the process into the probe when it is exec'd as `claude <ops>` / `claude --session-id ...` or as `factory`.

const (
	wbOAuth = "oauth-A1b2A1b2A1b2A1b2A1b2A1b2A1b2A1b2"
	wbGhRO  = "ghro-C3d4C3d4C3d4C3d4C3d4C3d4C3d4C3d4"
	wbGhRW  = "ghrw-E5f6E5f6E5f6E5f6E5f6E5f6E5f6E5f6"
	// wbTermsBody is the fake scrub-terms file.
	wbTermsBody = "dummy-term-one\ndummy-term-two\n"
	// wbControlRel is the herdr control socket path relative to the home (DEFAULT_CONFIG["herdr_socket"][2:]).
	wbControlRel = ".config/herdr/herdr.sock"
	wbAgentRel   = wbControlRel + ".agent" // the SSH_AUTH_SOCK convention of the owner's panes
)

var (
	wbSlugs = []string{"volekerb/hivemill"}
	wbIdent = [2]string{"Test Owner", "owner@example.invalid"}
	// wbHostRunSockets are the container-engine and ssh-unix-local sockets under the host /run.
	wbHostRunSockets = []string{"/run/docker.sock", "/var/run/docker.sock", "/run/ssh-unix-local/socket"}
)

func init() {
	if wbProbeMain() {
		os.Exit(0)
	}
}

// ---- the probe ------------------------------------------------------------------------------------------------

func wbErrName(err error) string {
	var en syscall.Errno
	if errors.As(err, &en) {
		for n, e := range map[string]syscall.Errno{"ENOENT": syscall.ENOENT, "EROFS": syscall.EROFS, "EACCES": syscall.EACCES,
			"EPERM": syscall.EPERM, "ECONNREFUSED": syscall.ECONNREFUSED, "EBUSY": syscall.EBUSY, "EXDEV": syscall.EXDEV,
			"EEXIST": syscall.EEXIST, "ENOTDIR": syscall.ENOTDIR, "EISDIR": syscall.EISDIR} {
			if en == e {
				return n
			}
		}
		return "errno" + strconv.Itoa(int(en))
	}
	return fmt.Sprintf("%T", err)
}

func wbProbeStr(o map[string]any, k string) string { s, _ := o[k].(string); return s }

func wbProbeRun(o map[string]any, trim bool) (any, error) {
	var argv []string
	for _, a := range o["argv"].([]any) {
		argv = append(argv, a.(string))
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = wbProbeStr(o, "cwd")
	if extra, ok := o["env"].(map[string]any); ok {
		cmd.Env = os.Environ()
		for k, v := range extra {
			cmd.Env = append(cmd.Env, k+"="+v.(string))
		}
	}
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		code = ee.ExitCode()
	case err != nil:
		return nil, err
	}
	out, errs := so.String(), se.String()
	if trim {
		out, errs = strings.TrimSpace(out), strings.TrimSpace(errs)
	}
	return []any{code, out, errs}, nil
}

func wbProbeOp(o map[string]any) (any, error) {
	path := wbProbeStr(o, "path")
	switch wbProbeStr(o, "op") {
	case "open":
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		return true, f.Close()
	case "write":
		data := "x"
		if d, ok := o["data"].(string); ok {
			data = d
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
		if err != nil {
			return nil, err
		}
		_, werr := f.WriteString(data)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		return true, werr
	case "read":
		b, err := os.ReadFile(path)
		return string(b), err
	case "unlink":
		return true, os.Remove(path)
	case "rename":
		return true, os.Rename(wbProbeStr(o, "src"), wbProbeStr(o, "dst"))
	case "listdir":
		ents, err := os.ReadDir(path)
		names := []string{}
		for _, e := range ents {
			names = append(names, e.Name())
		}
		sort.Strings(names)
		return names, err
	case "connect":
		c, err := net.Dial("unix", path)
		if err != nil {
			return nil, err
		}
		return true, c.Close()
	case "environ":
		b, err := os.ReadFile("/proc/self/environ")
		m := map[string]string{}
		for _, kv := range strings.Split(string(b), "\x00") {
			if k, v, ok := strings.Cut(kv, "="); ok {
				m[k] = v
			}
		}
		return m, err
	case "status":
		b, err := os.ReadFile("/proc/self/status")
		m := map[string]string{}
		for _, l := range strings.Split(string(b), "\n") {
			if k, v, ok := strings.Cut(l, ":\t"); ok {
				m[k] = v
			}
		}
		return m, err
	case "cmdline":
		b, err := os.ReadFile("/proc/self/cmdline")
		return strings.Split(string(b), "\x00"), err
	case "environ_of":
		f, err := os.Open(fmt.Sprintf("/proc/%d/environ", int(o["pid"].(float64))))
		if err != nil {
			return nil, err
		}
		_, rerr := f.Read(make([]byte, 1))
		if errors.Is(rerr, io.EOF) {
			rerr = nil
		}
		f.Close()
		return true, rerr
	case "which":
		p, err := exec.LookPath(wbProbeStr(o, "name"))
		if err != nil {
			return nil, nil
		}
		return p, nil
	case "run":
		return wbProbeRun(o, true)
	case "runraw":
		return wbProbeRun(o, false)
	case "cwd":
		return os.Getwd()
	case "exists":
		_, err := os.Stat(path)
		return err == nil, nil
	case "env":
		v, ok := os.LookupEnv(wbProbeStr(o, "name"))
		if !ok {
			return nil, nil
		}
		return v, nil
	}
	return nil, fmt.Errorf("bad op %v", o["op"])
}

// wbProbeMain runs the probe or the factory stub when this process was started as one; false = a normal test run.
func wbProbeMain() bool {
	args := os.Args
	if filepath.Base(args[0]) == "factory" { // STUB_FACTORY
		db := filepath.Join(os.Getenv("FACTORY_HOME"), "db")
		if len(args) > 1 && args[1] == "write" {
			if err := os.WriteFile(filepath.Join(db, "written"), []byte("from inside"), 0o644); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
		ents, _ := os.ReadDir(db)
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		fmt.Printf("agent=%s role=%s db=%v\n", os.Getenv("FACTORY_AGENT"), os.Getenv("FACTORY_ROLE"), names)
		return true
	}
	if len(args) >= 2 && args[1] == "--session-id" {
		comm, _ := os.ReadFile("/proc/self/comm")
		info := map[string]any{"pid": os.Getpid(), "pgid": syscall.Getpgrp(), "sid": wbGetsid(0), "fg": wbTcgetpgrp(0),
			"comm": strings.TrimSpace(string(comm)), "argv": args[1:]}
		b, _ := json.Marshal(info)
		fmt.Println("PTY:" + string(b))
		return true
	}
	if len(args) == 2 && strings.HasPrefix(args[1], "[") {
		fmt.Println("RAN")
		var ops []map[string]any
		if err := json.Unmarshal([]byte(args[1]), &ops); err != nil {
			fmt.Fprintln(os.Stderr, "probe: bad ops:", err)
			os.Exit(1)
		}
		out := []any{}
		for _, o := range ops {
			v, err := wbProbeOp(o)
			if err != nil {
				out = append(out, map[string]any{"ok": false, "err": wbErrName(err)})
			} else {
				out = append(out, map[string]any{"ok": true, "val": v})
			}
		}
		b, _ := json.Marshal(out)
		fmt.Println("RESULT:" + string(b))
		return true
	}
	return false
}

// ---- file helpers ------------------------------------------------------------------------------------------------

func wbMust(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func wbPut(t testing.TB, path, text string, mode os.FileMode) string {
	t.Helper()
	wbMust(t, os.MkdirAll(filepath.Dir(path), 0o755))
	wbMust(t, os.WriteFile(path, []byte(text), 0o644))
	if mode != 0 {
		wbMust(t, os.Chmod(path, mode))
	}
	return path
}

func wbRead(t testing.TB, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	wbMust(t, err)
	return string(b)
}

func wbSha(t testing.TB, path string) string {
	t.Helper()
	h := sha256.Sum256([]byte(wbRead(t, path)))
	return hex.EncodeToString(h[:])
}

func wbExists(path string) bool { _, err := os.Lstat(path); return err == nil }

func wbMode(t testing.TB, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	wbMust(t, err)
	return fi.Mode().Perm()
}

func wbNames(t testing.TB, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	wbMust(t, err)
	out := []string{}
	for _, e := range ents {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func wbEq(t testing.TB, what string, got, want any) {
	t.Helper()
	if fmt.Sprintf("%#v", got) != fmt.Sprintf("%#v", want) {
		t.Fatalf("%s = %#v, want %#v", what, got, want)
	}
}

func wbHas(t testing.TB, what, text, sub string) {
	t.Helper()
	if !strings.Contains(text, sub) {
		t.Fatalf("%s = %q, want it to contain %q", what, text, sub)
	}
}

func wbHasNot(t testing.TB, what, text, sub string) {
	t.Helper()
	if strings.Contains(text, sub) {
		t.Fatalf("%s = %q, must not contain %q", what, text, sub)
	}
}

func wbIn(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func wbRealpath(t testing.TB, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	wbMust(t, err)
	return r
}

// wbLink hard-links the test binary to dst (copy when the link crosses a filesystem).
func wbLink(t testing.TB, dst string) {
	t.Helper()
	self, err := os.Executable()
	wbMust(t, err)
	wbMust(t, os.MkdirAll(filepath.Dir(dst), 0o755))
	if os.Link(self, dst) == nil {
		return
	}
	b, err := os.ReadFile(self)
	wbMust(t, err)
	wbMust(t, os.WriteFile(dst, b, 0o755))
}

// wbNeedUserns skips the test where the stage-1 namespaces are unavailable (and inside the boxed gate).
func wbNeedUserns(t testing.TB) {
	t.Helper()
	if ok, why := sandbox.UsernsAvailable("unshare"); !ok {
		t.Skipf("user namespaces unavailable: %s", why)
	}
}

// ---- the world ---------------------------------------------------------------------------------------------------

// wbWorld is a fake home that mirrors the owner's layout, a fake factory home inside it, tokens and listening sockets.
type wbWorld struct {
	t                                    *testing.T
	base, home, runtime, fh, own, tokens string
	claude                               string
	bus, control, agentSock              string
}

func wbBase(t testing.TB, prefix string) string {
	t.Helper()
	base, err := os.MkdirTemp("/var/tmp", prefix)
	if err != nil {
		t.Skipf("cannot create a fixture under /var/tmp (/tmp is a hide tree): %v", err)
	}
	t.Cleanup(func() {
		// a Go build inside the box leaves read-only module cache dirs: make them removable first
		_ = filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error { // best effort: RemoveAll reports what is left
			if err == nil && d.IsDir() {
				_ = os.Chmod(p, 0o700)
			}
			return nil
		})
		os.RemoveAll(base)
	})
	return wbRealpath(t, base)
}

func wbNewWorld(t *testing.T) *wbWorld {
	t.Helper()
	b := wbBase(t, "zz-test-workerbox-")
	w := &wbWorld{t: t, base: b, home: b + "/home", runtime: b + "/run"}
	w.fh = w.home + "/factory"
	wbPut(t, w.home+"/.claude/.credentials.json", `{"note": "fake-credentials"}`, 0)
	wbPut(t, w.home+"/.ssh/id_ed25519", "fake-private-key", 0)
	wbPut(t, w.home+"/.config/gh/hosts.yml", "github.com: fake", 0)
	wbPut(t, w.home+"/.zshrc", "# owner zshrc\n", 0)
	real := w.home + "/.local/share/claude/versions/1"
	wbLink(t, real)
	w.claude = w.home + "/.local/bin/claude"
	wbMust(t, os.MkdirAll(filepath.Dir(w.claude), 0o755))
	wbMust(t, os.Symlink(real, w.claude))
	wbLink(t, w.fh+"/bin/factory")
	wbMust(t, os.Symlink(w.fh+"/bin/factory", w.home+"/.local/bin/factory"))
	wbPut(t, w.fh+"/roles/settings.json", "{}", 0)
	wbPut(t, w.fh+"/db/factory.db", "ROWS", 0)
	wbPut(t, w.fh+"/worktrees/own/file.txt", "own", 0)
	wbPut(t, w.fh+"/worktrees/other/file.txt", "other", 0)
	w.own = w.fh + "/worktrees/own"
	w.tokens = w.home + "/.config/factory"
	for name, tok := range map[string]string{"worker-oauth-token": wbOAuth, "gh-worker-ro-token": wbGhRO, "gh-worker-rw-token": wbGhRW} {
		wbPut(t, w.tokens+"/"+name, tok+"\n", 0o600)
	}
	wbPut(t, w.tokens+"/"+sandbox.TermsName, wbTermsBody, 0o600)
	w.bus = w.listen(w.runtime + "/bus")
	w.control = w.listen(w.home + "/" + wbControlRel)
	w.agentSock = w.listen(w.home + "/" + wbAgentRel)
	return w
}

// listen makes a unix socket that accepts and drops connections, so a connect from outside succeeds.
func (w *wbWorld) listen(path string) string {
	w.t.Helper()
	wbMust(w.t, os.MkdirAll(filepath.Dir(path), 0o755))
	ln, err := net.Listen("unix", path)
	wbMust(w.t, err)
	w.t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	return path
}

// args are the default PrepareAgent arguments of the world (Python World.prepare).
func (w *wbWorld) args() sandbox.PrepareAgentArgs {
	id := wbIdent
	return sandbox.PrepareAgentArgs{Agent: "coder-t-1", Role: "coder", Cwd: w.own, FactoryHome: w.fh, DBDir: w.fh + "/db",
		RealClaude: w.claude, RwCwd: true, Home: w.home, Runtime: w.runtime, Identity: &id, GhSlugs: wbSlugs,
		Seams: sandbox.Opts{UID: os.Getuid(), GID: os.Getgid(), Runtime: w.runtime}}
}

func (w *wbWorld) prepareWith(mod func(*sandbox.PrepareAgentArgs)) (string, error) {
	a := w.args()
	if mod != nil {
		mod(&a)
	}
	return sandbox.PrepareAgent(a)
}

func (w *wbWorld) prepare(mod func(*sandbox.PrepareAgentArgs)) string {
	w.t.Helper()
	d, err := w.prepareWith(mod)
	wbMust(w.t, err)
	return d
}

func (w *wbWorld) callerEnv(extra map[string]string) []string {
	m := map[string]string{"PATH": w.home + "/.local/bin:/usr/bin:/bin", "LANG": "C.UTF-8", "TERM": "xterm", "LC_ALL": "C.UTF-8",
		"FACTORY_AGENT": "coder-t-1", "FACTORY_ROLE": "coder", "FACTORY_HOME": w.fh, "FACTORY_DB": w.fh + "/db",
		"HERDR_PANE_ID": "p_42", "DISABLE_AUTOUPDATER": "0", "FOO_SECRET": "x", "SSH_AUTH_SOCK": w.home + "/" + wbAgentRel,
		"TMPDIR": "/tmp", "DBUS_SESSION_BUS_ADDRESS": "unix:path=" + w.runtime + "/bus", "HOME": "/nonexistent",
		"HTTPS_PROXY": "http://example.invalid:1", "ANTHROPIC_API_KEY": "dummy-anthropic-value"}
	for k, v := range extra {
		m[k] = v
	}
	var out []string
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// wbRes is one probe result.
type wbRes struct {
	Ok  bool   `json:"ok"`
	Val any    `json:"val"`
	Err string `json:"err"`
}

type wbRun struct {
	Code           int
	Stdout, Stderr string
	Res            []wbRes // nil when no RESULT line
}

// run starts <agent dir>/bin/claude with the probe op list (a nil env = the world's caller env, cwd "" = own).
func (w *wbWorld) run(agentDir string, ops []map[string]any, cwd string, env []string) wbRun {
	w.t.Helper()
	if ops == nil {
		ops = []map[string]any{}
	}
	js, err := json.Marshal(ops)
	wbMust(w.t, err)
	return wbRunCmd(w.t, exec.Command(agentDir+"/bin/claude", string(js)), cwd, env, w.own, w.callerEnv(nil))
}

func wbRunCmd(t testing.TB, cmd *exec.Cmd, cwd string, env []string, defCwd string, defEnv []string) wbRun {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, cmd.Path, cmd.Args[1:]...)
	c.Dir, c.Env = cwd, env
	if c.Dir == "" {
		c.Dir = defCwd
	}
	if c.Env == nil {
		c.Env = defEnv
	}
	var so, se bytes.Buffer
	c.Stdout, c.Stderr = &so, &se
	c.WaitDelay = 5 * time.Second
	err := c.Run()
	r := wbRun{Stdout: so.String(), Stderr: se.String()}
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		r.Code = ee.ExitCode()
	case err != nil:
		t.Fatalf("run %s: %v", cmd.Path, err)
	}
	for _, line := range strings.Split(r.Stdout, "\n") {
		if rest, ok := strings.CutPrefix(line, "RESULT:"); ok {
			if err := json.Unmarshal([]byte(rest), &r.Res); err != nil {
				t.Fatalf("bad RESULT line %q: %v", rest, err)
			}
		}
	}
	return r
}

// probe runs ops and requires exit 0 and a RESULT line.
func (w *wbWorld) probe(agentDir string, ops ...map[string]any) []wbRes {
	w.t.Helper()
	r := w.run(agentDir, ops, "", nil)
	if r.Code != 0 {
		w.t.Fatalf("probe exit %d: %s", r.Code, r.Stderr)
	}
	if r.Res == nil {
		w.t.Fatalf("no RESULT: %s%s", r.Stdout, r.Stderr)
	}
	return r.Res
}

func wbOp(op string, kv ...string) map[string]any {
	m := map[string]any{"op": op}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return m
}

func wbEnvOf(r wbRes) map[string]any { m, _ := r.Val.(map[string]any); return m }

func wbVals(r wbRes) []any { v, _ := r.Val.([]any); return v }

// wbShimJSON is the cfg baked into <agent dir>/bin/claude as a generic map (the keys are the Python cfg's).
func wbShimJSON(t testing.TB, agentDir string) map[string]any {
	t.Helper()
	text := wbRead(t, agentDir+"/bin/claude")
	i := strings.Index(text, "worker-shim '")
	j := strings.LastIndex(text, "' \"$@\"")
	if i < 0 || j < i {
		t.Fatalf("shim has no cfg: %q", text)
	}
	var m map[string]any
	wbMust(t, json.Unmarshal([]byte(text[i+len("worker-shim '"):j]), &m))
	return m
}

func wbStrs(v any) []string {
	var out []string
	l, _ := v.([]any)
	for _, x := range l {
		out = append(out, x.(string))
	}
	return out
}
