//go:build linux

package sandbox2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	sbx "factory/go/internal/sandbox"
)

// The nsbox tests run real stage-1 namespaces with this very test binary as the stage-1 helper (Cfg.Self), the way
// internal/sandbox's TestMain does it. A package may have only one TestMain, so the dispatch lives in an init():
// `self --setup <cfg> -- argv` is stage 1 (sbx.Main), `self --nsb-probe <probes...>` is the in-box probe that
// stands in for the Python `python -c` snippets (no python3 needed).
func init() {
	if len(os.Args) > 2 && os.Args[1] == "--nsb-probe" {
		os.Exit(nsbProbe(os.Args[2:], os.Stdout))
	}
	if len(os.Args) > 1 {
		if code, handled := sbx.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); handled {
			os.Exit(code)
		}
	}
}

var nsbMiB = bytes.Repeat([]byte("x"), 1<<20)

func nsbErrName(err error) string {
	var en syscall.Errno
	if errors.As(err, &en) {
		switch en {
		case syscall.EROFS:
			return "EROFS"
		case syscall.ENOSPC:
			return "ENOSPC"
		case syscall.EACCES:
			return "EACCES"
		case syscall.EPERM:
			return "EPERM"
		case syscall.ENOENT:
			return "ENOENT"
		}
	}
	return err.Error()
}

// nsbProbe runs probes of the form kind:arg and prints `probe=result` lines.
//
//	r:path     readable | blocked       c:path   connected | blocked (unix socket)
//	w:path     ok | <errno name>        (create, then remove)
//	a:path     ok | <errno name>        (open for append, no create, nothing written)
//	f:path:mib ok | <errno name>        (write mib MiB into path/big)
//	l:path     sorted names, comma separated     x:path  true | false
//	caps       <CapEff as a number> <mount rc> ; cwd   the real cwd
func nsbProbe(probes []string, w io.Writer) int {
	for _, p := range probes {
		kind, arg, _ := strings.Cut(p, ":")
		res := ""
		switch kind {
		case "r":
			if _, err := os.ReadFile(arg); err != nil {
				res = "blocked"
			} else {
				res = "readable"
			}
		case "c":
			conn, err := net.Dial("unix", arg)
			if err != nil {
				res = "blocked"
			} else {
				conn.Close()
				res = "connected"
			}
		case "w":
			f, err := os.OpenFile(arg, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
			if err != nil {
				res = nsbErrName(err)
				break
			}
			f.Write([]byte("x"))
			f.Close()
			os.Remove(arg)
			res = "ok"
		case "a":
			f, err := os.OpenFile(arg, os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				res = nsbErrName(err)
				break
			}
			f.Close()
			res = "ok"
		case "f":
			i := strings.LastIndex(arg, ":")
			mib, _ := strconv.Atoi(arg[i+1:])
			f, err := os.Create(arg[:i] + "/big")
			if err != nil {
				res = nsbErrName(err)
				break
			}
			res = "ok"
			for n := 0; n < mib; n++ {
				if _, err := f.Write(nsbMiB); err != nil {
					res = nsbErrName(err)
					break
				}
			}
			if err := f.Close(); err != nil && res == "ok" {
				res = nsbErrName(err)
			}
		case "l":
			ents, err := os.ReadDir(arg)
			if err != nil {
				res = "error " + err.Error()
				break
			}
			var names []string
			for _, e := range ents {
				names = append(names, e.Name())
			}
			sort.Strings(names)
			res = strings.Join(names, ",")
		case "x":
			_, err := os.Lstat(arg)
			res = strconv.FormatBool(err == nil)
		case "caps":
			st, _ := os.ReadFile("/proc/self/status")
			m := regexp.MustCompile(`CapEff:\s*(\w+)`).FindSubmatch(st)
			v := uint64(1 << 63)
			if m != nil {
				v, _ = strconv.ParseUint(string(m[1]), 16, 64)
			}
			rc := 0
			if err := syscall.Mount("tmpfs", "/tmp", "tmpfs", 0, ""); err != nil {
				rc = -1
			}
			res = fmt.Sprintf("%d %d", v, rc)
		case "cwd":
			res, _ = os.Readlink("/proc/self/cwd")
		default:
			res = "unknown probe"
		}
		fmt.Fprintf(w, "%s=%s\n", p, res)
	}
	return 0
}

// nsbParse reads probe output into a map probe -> result.
func nsbParse(out string) map[string]string {
	m := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			m[k] = v
		}
	}
	return m
}

func nsbExe(t testing.TB) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return exe
}

func nsbUnshare() string {
	if u := os.Getenv("NSBOX_TEST_UNSHARE"); u != "" {
		return u
	}
	return "unshare"
}

// nsbNeedUserns skips t with the reason where the real stage-1 namespaces cannot run.
func nsbNeedUserns(t testing.TB) {
	t.Helper()
	if ok, why := sbx.UsernsAvailable(nsbUnshare()); !ok {
		t.Skipf("user namespaces unavailable: %s", why)
	}
	if _, err := exec.LookPath("setpriv"); err != nil {
		t.Skip("setpriv not installed")
	}
}

// nsbRoot is a fresh temp dir with symlinks resolved.
func nsbRoot(t testing.TB) string {
	t.Helper()
	return sbx.Realpath(t.TempDir())
}

func nsbMkdir(t testing.TB, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
	}
}

func nsbWrite(t testing.TB, path, data string) {
	t.Helper()
	nsbMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func nsbSymlink(t testing.TB, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink %s -> %s: %v", link, target, err)
	}
}

func nsbRead(t testing.TB, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func nsbExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func nsbContains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func nsbEq(t testing.TB, what string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %q, want %q", what, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s = %q, want %q", what, got, want)
		}
	}
}

func nsbHas(t testing.TB, what, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("%s = %q, want it to contain %q", what, got, want)
	}
}

// nsbOpts is Opts for an allow-mode make_cfg call: the Python defaults, this binary as stage 1, and no hide files.
func nsbOpts(trial, home, trialsRoot string) sbx.Opts {
	o := sbx.NewOpts(trial)
	o.Home, o.TrialsRoot = home, trialsRoot
	o.Mode, o.Unshare, o.Self = "allow", nsbUnshare(), "/nsb/self"
	return o
}

func nsbMake(t testing.TB, o sbx.Opts) *sbx.Cfg {
	t.Helper()
	cfg, err := sbx.MakeCfg(o)
	if err != nil {
		t.Fatalf("MakeCfg: %v", err)
	}
	return cfg
}

// nsbRefused is the error text of a MakeCfg call that must fail.
func nsbRefused(t testing.TB, what string, o sbx.Opts) string {
	t.Helper()
	cfg, err := sbx.MakeCfg(o)
	if err == nil {
		t.Fatalf("%s: MakeCfg accepted it (cfg %+v), want an error", what, cfg)
	}
	return err.Error()
}

type nsbResult struct {
	Out, Err string
	RC       int
}

// nsbRun runs argv inside the stage-1 view of cfg, with only PATH in the environment (extra adds KEY=VAL pairs).
func nsbRun(t testing.TB, cfg *sbx.Cfg, argv []string, extra ...string) nsbResult {
	t.Helper()
	argv1, err := sbx.ComposeStage1(cfg, argv)
	if err != nil {
		t.Fatalf("ComposeStage1: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv1[0], argv1[1:]...)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, extra...)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	rc := 0
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("run stage 1: %v", err)
		}
		rc = ee.ExitCode()
	}
	return nsbResult{so.String(), se.String(), rc}
}

// nsbProbeRun runs the in-box probes under cfg and returns the parsed results.
func nsbProbeRun(t testing.TB, cfg *sbx.Cfg, probes ...string) map[string]string {
	t.Helper()
	r := nsbRun(t, cfg, append([]string{nsbExe(t), "--nsb-probe"}, probes...))
	if r.RC != 0 {
		t.Fatalf("probe run rc=%d stderr=%s stdout=%s", r.RC, r.Err, r.Out)
	}
	return nsbParse(r.Out)
}

// nsbMarkerArgv is a target that creates marker: it must never run when the view fails closed.
func nsbMarkerArgv(marker string) []string {
	return []string{"/bin/sh", "-c", "touch '" + marker + "'"}
}

// nsbFill writes mib MiB into dir/big inside the box of cfg and returns ok or the errno name.
func nsbFill(t testing.TB, cfg *sbx.Cfg, dir string, mib int) string {
	t.Helper()
	p := "f:" + dir + ":" + strconv.Itoa(mib)
	return nsbProbeRun(t, cfg, p)[p]
}

// nsbSockets binds unix sockets at the given paths for the length of the test.
func nsbSockets(t testing.TB, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if len(p) > 100 {
			t.Fatalf("socket path too long for sockaddr_un (%d bytes): %s", len(p), p)
		}
		l, err := net.Listen("unix", p)
		if err != nil {
			t.Fatalf("listen %s: %v", p, err)
		}
		t.Cleanup(func() { l.Close() })
	}
}

// nsbCanConnect reports whether this process can connect to the unix socket p.
func nsbCanConnect(p string) bool {
	c, err := net.Dial("unix", p)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// nsbIsDir reports whether p is a directory (following links).
func nsbIsDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// nsbRunKeepDirs is the /run keep-list MakeCfg adds for /etc/resolv.conf on this host: only directories that exist.
func nsbRunKeepDirs() []string {
	var out []string
	for _, d := range sbx.RunKeepRO("/etc/resolv.conf") {
		if nsbIsDir(d) {
			out = append(out, d)
		}
	}
	return out
}

func lookPath(name string) (string, error) { return exec.LookPath(name) }

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

func nsbAtoi(s string) (int, error) { return strconv.Atoi(s) }

// nsbStart runs argv with only PATH in the environment and returns its pid and stdout.
func nsbStart(argv []string) (int, string, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	var so bytes.Buffer
	cmd.Stdout = &so
	if err := cmd.Start(); err != nil {
		return 0, "", fmt.Errorf("start stage 1: %w", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		return pid, so.String(), fmt.Errorf("stage 1: %w", err)
	}
	return pid, so.String(), nil
}

func nsbCopyFile(src, dst string, mode os.FileMode) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	if err := os.WriteFile(dst, b, mode); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return nil
}
