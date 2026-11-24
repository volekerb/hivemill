package skilleval1

// Port of tests/test_skilleval_nsbox.py (T-0118): the helpers shared by nsbox_proxy_test.go and nsbox_ns_test.go.
//
// The Python fixtures drive scout/nsbox.py. The Go product (go/internal/sandbox) has no eval shim writer and no
// start_proxy, so the namespace scenarios are driven the Go way: the proxy is sandbox.Serve in this process, stage 1
// is the test binary itself (TestMain hands `--setup` / `forward` to sandbox.Main), and the "fake claude" is this
// same binary re-run as a helper process (TestSkillevalNsbox_HelperProcess, selected by NSB_MODE), so no Python and
// no shell script is needed. Every name here starts with nsb to stay clear of the other ports in this package.

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"factory/go/internal/sandbox"
)

// nsbToken is the dummy token; split so the repo secret scan does not flag it.
const nsbToken = "sk-ant-" + "oat01-DUMMYTOKEN-RANDOM"

// nsbMarker prefixes the one JSON line a helper process prints on stdout.
const nsbMarker = "NSB-JSON:"

// nsbUnshare is the seam of NSBOX_TEST_UNSHARE: a bogus path forces the clean-skip branch.
func nsbUnshare() string {
	if v := os.Getenv("NSBOX_TEST_UNSHARE"); v != "" {
		return v
	}
	return "unshare"
}

// nsbRequireUserns skips with the reason when the namespaces (or setpriv) are not available on this box.
func nsbRequireUserns(t *testing.T) {
	t.Helper()
	if ok, why := sandbox.UsernsAvailable(nsbUnshare()); !ok {
		t.Skip("user namespaces unavailable: " + why)
	}
	if _, err := exec.LookPath("setpriv"); err != nil {
		t.Skip("user namespaces unavailable: setpriv is not installed")
	}
}

// nsbNotInGatebox skips inside the gate box, where the outer box already hides $HOME and /proc.
func nsbNotInGatebox(t *testing.T) {
	t.Helper()
	if os.Getenv("FACTORY_GATEBOX") == "1" {
		t.Skip("inside the gate box (FACTORY_GATEBOX=1): the outer box already hides $HOME and /proc, so this nested-userns check cannot observe what it asserts; CI runs it unboxed")
	}
}

// nsbShort is a short, real temp dir (unix socket paths are limited to 107 bytes), removed when t ends.
func nsbShort(t testing.TB) string {
	t.Helper()
	d, err := os.MkdirTemp("", "nsb")
	if err != nil {
		t.Fatal(err)
	}
	d = sandbox.Realpath(d)
	t.Cleanup(func() {
		if err := os.RemoveAll(d); err != nil {
			t.Logf("cleanup %s: %v", d, err)
		}
	})
	return d
}

func nsbWrite(t testing.TB, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil { // WriteFile is subject to the umask; the mode is part of the case
		t.Fatal(err)
	}
}

func nsbMkdirs(t testing.TB, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// nsbEcho is FakeUpstream / TcpUpstream: an echo server answering "UP:" + what it got. Its address is ln.Addr().
func nsbEcho(t testing.TB, network, addr string) net.Listener {
	t.Helper()
	ln, err := net.Listen(network, addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() }) // closing a listener that is already closed is harmless
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return // the listener was closed
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 1024)
				for {
					n, err := c.Read(buf)
					if n > 0 {
						if _, werr := c.Write(append([]byte("UP:"), buf[:n]...)); werr != nil {
							return
						}
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	return ln
}

// nsbServeAsync starts sandbox.Serve; done receives its result. ready is closed once it listens.
func nsbServeAsync(o sandbox.ServeOpts) (ready chan struct{}, done chan error) {
	ready, done = make(chan struct{}), make(chan error, 1)
	o.Ready = ready
	go func() { done <- sandbox.Serve(o) }()
	return ready, done
}

// nsbServe runs a proxy until the test ends and returns its socket path.
func nsbServe(t *testing.T, allow []string, res sandbox.Resolver) string {
	t.Helper()
	sock := filepath.Join(nsbShort(t), "proxy.sock")
	stop := make(chan struct{})
	ready, done := nsbServeAsync(sandbox.ServeOpts{Sock: sock, Allow: allow, Resolver: res, MaxLife: 30 * time.Second, Stop: stop})
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("proxy exited early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("proxy never became ready")
	}
	t.Cleanup(func() { close(stop); <-done })
	return sock
}

// nsbConnect is connect_via: dial the unix socket and send the request.
func nsbConnect(t testing.TB, sock, request string) net.Conn {
	t.Helper()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() }) // closed twice by a test that closes early: harmless
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, request); err != nil {
		t.Fatal(err)
	}
	return c
}

// nsbRecv is one recv(n): whatever the next read returns, at most n bytes.
func nsbRecv(t testing.TB, c net.Conn, n int) string {
	t.Helper()
	buf := make([]byte, n)
	got, err := c.Read(buf)
	if err != nil && got == 0 {
		t.Fatalf("read: %v", err)
	}
	return string(buf[:got])
}

// nsbReadN reads exactly len(want) bytes (a reply that may arrive in pieces).
func nsbReadN(t testing.TB, c net.Conn, n int) string {
	t.Helper()
	buf := make([]byte, n)
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("read %d bytes: %v (got %q)", n, err, buf)
	}
	return string(buf)
}

func nsbFirstLine(s string) string {
	line, _, _ := strings.Cut(s, "\r\n")
	return line
}

// nsbStatus sends a request and returns the first line of the answer.
func nsbStatus(t testing.TB, sock, request string) string {
	t.Helper()
	c := nsbConnect(t, sock, request)
	defer c.Close()
	return nsbFirstLine(nsbRecv(t, c, 4096))
}

// nsbWaitFor polls cond every 50 ms for up to secs seconds.
func nsbWaitFor(cond func() bool, secs int) bool {
	end := time.Now().Add(time.Duration(secs) * time.Second)
	for time.Now().Before(end) {
		if cond() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return cond()
}

// nsbCfg is nsbox.make_cfg with the Python defaults (deny mode) for a trial dir; mod adjusts the options.
func nsbCfg(t testing.TB, trial, claude, home string, mod func(*sandbox.Opts)) *sandbox.Cfg {
	t.Helper()
	o := sandbox.NewOpts(trial)
	o.RealClaude, o.Home = claude, home
	if mod != nil {
		mod(&o)
	}
	cfg, err := sandbox.MakeCfg(o)
	if err != nil {
		t.Fatalf("MakeCfg: %v", err)
	}
	return cfg
}

func nsbHas(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ---- the helper process: the test binary run as the fake claude / probe inside the namespaces ----

// nsbHelperArgs is what the namespaced target gets as argv: it selects TestSkillevalNsbox_HelperProcess.
var nsbHelperArgs = []string{"-test.run=^TestSkillevalNsbox_HelperProcess$", "-test.count=1"}

// nsbEmit prints the one result line of a helper and ends the process with code.
func nsbEmit(v any, code int) {
	b, err := json.Marshal(v)
	if err != nil {
		os.Stderr.WriteString("nsb helper: " + err.Error() + "\n")
		os.Exit(99)
	}
	os.Stdout.WriteString(nsbMarker + string(b) + "\n")
	os.Exit(code)
}

// nsbParseEmit finds the helper's JSON line in the output of a contained run.
func nsbParseEmit(t testing.TB, output string, into any) {
	t.Helper()
	for _, ln := range strings.Split(output, "\n") {
		if rest, ok := strings.CutPrefix(ln, nsbMarker); ok {
			if err := json.Unmarshal([]byte(rest), into); err != nil {
				t.Fatalf("bad helper line %q: %v", ln, err)
			}
			return
		}
	}
	t.Fatalf("no helper result in the output:\n%s", output)
}

// nsbLs is the sorted listing of dir, or ("", errno text) when it cannot be read: Python's ls() / "ERR".
func nsbLs(dir string) ([]string, string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		var en syscall.Errno
		if errors.As(err, &en) {
			return nil, "ERR " + en.Error()
		}
		return nil, "ERR " + err.Error()
	}
	out := []string{}
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out, ""
}

// nsbLsAny is nsbLs as one JSON value: the list, or the string "ERR".
func nsbLsAny(dir string) any {
	names, e := nsbLs(dir)
	if e != "" {
		return "ERR"
	}
	return names
}

func nsbReadAny(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return "ERR"
	}
	return string(b)
}

func nsbLinkAny(p string) string {
	s, err := os.Readlink(p)
	if err != nil {
		return "ERR"
	}
	return s
}

// nsbPids is every numeric entry of /proc.
func nsbPids() []string {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if n := e.Name(); n != "" && strings.Trim(n, "0123456789") == "" {
			out = append(out, n)
		}
	}
	return out
}

// TestSkillevalNsbox_HelperProcess is not a test: it is the fake claude and the probes, selected by NSB_MODE, which
// the namespaced runs of this file start as `<test binary> -test.run=^TestSkillevalNsbox_HelperProcess$`. Without
// NSB_MODE (a normal run) it does nothing.
func TestSkillevalNsbox_HelperProcess(t *testing.T) {
	mode := os.Getenv("NSB_MODE")
	if mode == "" {
		return
	}
	switch mode {
	case "claude":
		nsbHelperClaude()
	case "probe":
		nsbHelperProbe()
	case "hiding":
		nsbHelperHiding()
	case "ls2":
		nsbEmit([]any{nsbLsAny(os.Getenv("NSB_A")), nsbLsAny(os.Getenv("NSB_B"))}, 0)
	case "env":
		m := map[string]string{}
		for _, kv := range os.Environ() {
			k, v, _ := strings.Cut(kv, "=")
			m[k] = v
		}
		nsbEmit(m, 0)
	case "allow":
		nsbHelperAllow()
	default:
		t.Fatalf("unknown NSB_MODE %q", mode)
	}
}
