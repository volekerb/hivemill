package install

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// Timeouts carried over from install.py.
const (
	httpStatusTimeout  = 10 * time.Second // Host.http_status urlopen timeout=10
	downloadIdleLimit  = 60 * time.Second // Host.download urlopen timeout=60 (a per-socket-operation limit)
	userAgent          = "hivemill-install/1.0"
	defaultPathSetting = ":/bin:/usr/bin" // os.defpath
)

// StepFail is a failed step: exit 1 and `RESULT FAIL step=<name>`.
type StepFail struct {
	Msg string
	Err error
}

func (e *StepFail) Error() string { return e.Msg }
func (e *StepFail) Unwrap() error { return e.Err }

func stepf(format string, a ...any) error { return &StepFail{Msg: fmt.Sprintf(format, a...)} }

// stepWrap is a StepFail whose message is "<what>: <err>" and which unwraps to err.
func stepWrap(err error, what string) error {
	return &StepFail{Msg: what + ": " + err.Error(), Err: err}
}

// Stop ends the run with this exit code and message and no RESULT line (refusals and hand-offs to the user).
type Stop struct {
	Code int
	Msg  string
}

func (s *Stop) Error() string { return s.Msg }

func stopf(code int, format string, a ...any) error {
	return &Stop{Code: code, Msg: fmt.Sprintf(format, a...)}
}

// Host is everything that touches the machine. Tests replace it; --check and --dry-run only use the read methods.
type Host interface {
	Home() string
	Environ() map[string]string
	Euid() int
	Machine() string
	// GOOS is the operating system: "linux", "darwin" or another runtime.GOOS value.
	GOOS() string
	MemGiB() (float64, error)
	CPUCount() int
	Exists(path string) bool
	ReadFile(path string) ([]byte, error)
	// Run runs argv and returns its exit code and merged stdout+stderr. timeout 0 means none. It never reads stdin.
	Run(argv []string, cwd string, timeout time.Duration, mutate bool) (int, string)
	HTTPStatus(url string) int
	Sleep(d time.Duration)
	Mkdirs(path string) error
	Rename(src, dst string) error
	Mkdtemp() (string, error)
	Rmtree(path string)
	Download(url, dest string) error
	InstallFile(src, dest string, mode os.FileMode) error
	ExtractMember(tarball, member, dest string) error
	ExtractZipMember(zipfile, member, dest string) error
	Symlink(target, link string) error
	Readlink(path string) (string, error)
	Popen(argv []string, logpath string) error
	WriteText(path, text string) error
	// IsTerminal reports whether stdin, stdout and stderr are all terminals (character devices).
	IsTerminal() bool
	// RunTTY runs argv attached to the user's terminal (stdin, stdout, stderr) and returns its exit code.
	// The installer sees none of its output.
	RunTTY(argv []string) int
	// WaitEnter blocks until the user presses Enter; an error means stdin is closed.
	WaitEnter() error
}

// OSHost is the real machine.
type OSHost struct {
	HomeDir string
	Env     map[string]string
	// DownloadIdle and StatusTimeout override the 60 s and 10 s defaults (tests use short ones).
	DownloadIdle  time.Duration
	StatusTimeout time.Duration
	tlsConfig     *tls.Config   // tests only: trust a local TLS server
	stdin         *bufio.Reader // WaitEnter's one reader, so nothing typed ahead is dropped between prompts
}

// NewOSHost reads the home directory and environment of the invoking user.
func NewOSHost() (*OSHost, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot find the home directory: %w", err)
	}
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	return &OSHost{HomeDir: home, Env: env}, nil
}

func (h *OSHost) GOOS() string                 { return runtime.GOOS }
func (h *OSHost) Home() string                 { return h.HomeDir }
func (h *OSHost) Environ() map[string]string   { return h.Env }
func (h *OSHost) Euid() int                    { return os.Geteuid() }
func (h *OSHost) CPUCount() int                { return max(runtime.NumCPU(), 1) }
func (h *OSHost) Sleep(d time.Duration)        { time.Sleep(d) }
func (h *OSHost) Mkdirs(path string) error     { return os.MkdirAll(path, 0o777) }
func (h *OSHost) Rename(src, dst string) error { return os.Rename(src, dst) }
func (h *OSHost) Rmtree(path string)           { _ = os.RemoveAll(path) } // best effort, like shutil.rmtree(ignore_errors=True)

// Machine is uname -m: x86_64 or aarch64 on Linux, arm64 on macOS.
func (h *OSHost) Machine() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	case "arm64":
		if runtime.GOOS == "darwin" {
			return "arm64"
		}
		return "aarch64"
	}
	return runtime.GOARCH
}

// MemGiB reads MemTotal from /proc/meminfo.
func (h *OSHost) MemGiB() (float64, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, fmt.Errorf("cannot read memory size: %w", err)
	}
	defer f.Close() //nolint:errcheck // read-only handle
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "MemTotal:") {
			fields := strings.Fields(sc.Text())
			if len(fields) < 2 {
				return 0, errors.New("bad MemTotal line in /proc/meminfo")
			}
			kib, err := strconv.ParseFloat(fields[1], 64)
			if err != nil {
				return 0, fmt.Errorf("bad MemTotal in /proc/meminfo: %w", err)
			}
			return kib / (1024.0 * 1024.0), nil
		}
	}
	if err := sc.Err(); err != nil {
		return 0, fmt.Errorf("reading /proc/meminfo: %w", err)
	}
	return 0, errors.New("no MemTotal in /proc/meminfo")
}

// Exists is os.path.lexists.
func (h *OSHost) Exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func (h *OSHost) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func (h *OSHost) childEnv() []string {
	env := map[string]string{}
	for k, v := range h.Env {
		env[k] = v
	}
	p, ok := env["PATH"]
	if !ok {
		p = defaultPathSetting
	}
	env["PATH"] = filepath.Join(h.HomeDir, ".local", "bin") + ":" + p
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

// lookPath resolves file against pathVar (the child's PATH, not ours), like execvpe does.
func lookPath(file, pathVar string) (string, error) {
	if strings.Contains(file, "/") {
		return file, nil
	}
	for _, dir := range strings.Split(pathVar, ":") {
		if dir == "" {
			dir = "."
		}
		cand := filepath.Join(dir, file)
		if st, err := os.Stat(cand); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
			return cand, nil
		}
	}
	return "", fmt.Errorf("%s: executable file not found in PATH", file)
}

func pathOf(env []string) string {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			return v
		}
	}
	return defaultPathSetting
}

// normText is Python's errors="replace" decoding plus universal newlines.
func normText(b []byte) string {
	s := strings.ToValidUTF8(string(b), string(utf8.RuneError))
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

func (h *OSHost) Run(argv []string, cwd string, timeout time.Duration, mutate bool) (int, string) {
	_ = mutate // only labels the call for tests
	env := h.childEnv()
	path, err := lookPath(argv[0], pathOf(env))
	if err != nil {
		return 127, err.Error()
	}
	ctx := context.Background()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, path, argv[1:]...)
	cmd.Dir = cwd
	cmd.Env = env
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	cmd.WaitDelay = 2 * time.Second
	err = cmd.Run()
	if err == nil {
		return 0, normText(buf.Bytes())
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return 124, fmt.Sprintf("timed out after %ds", int(timeout/time.Second))
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return -int(ws.Signal()), normText(buf.Bytes())
		}
		return ee.ExitCode(), normText(buf.Bytes())
	}
	return 127, err.Error()
}

func (h *OSHost) HTTPStatus(url string) int {
	d := h.StatusTimeout
	if d == 0 {
		d = httpStatusTimeout
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := (&http.Client{Timeout: d}).Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close() //nolint:errcheck // status only, body unused
	return resp.StatusCode
}

func (h *OSHost) Mkdtemp() (string, error) { return os.MkdirTemp("", "hivemill-install-") }

// idleReader cancels the request when no byte arrives for the idle limit, which is what a per-operation socket
// timeout does in Python's urlopen(timeout=60). A slow but steady download is never cut off.
type idleReader struct {
	r   io.Reader
	t   *time.Timer
	d   time.Duration
	ctx context.Context
}

func (i *idleReader) Read(p []byte) (int, error) {
	n, err := i.r.Read(p)
	// The idle timer cancels ctx. A cancelled connection can surface as a clean EOF, so a read that ends while ctx is
	// cancelled is reported as the timeout it was, never as a complete body.
	if cerr := i.ctx.Err(); cerr != nil && (err == nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)) {
		return n, cerr
	}
	i.t.Reset(i.d)
	return n, err
}

// Download fetches an https URL to dest; a redirect away from https is refused.
func (h *OSHost) Download(url, dest string) error {
	if !strings.HasPrefix(url, "https://") {
		return stepf("refusing non-https download %s", url)
	}
	idle := h.DownloadIdle
	if idle == 0 {
		idle = downloadIdleLimit
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	timer := time.AfterFunc(idle, cancel)
	defer timer.Stop()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return stepWrap(err, "cannot fetch "+url)
	}
	req.Header.Set("User-Agent", userAgent)
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			TLSClientConfig:       h.tlsConfig,
			DialContext:           (&net.Dialer{Timeout: idle}).DialContext,
			TLSHandshakeTimeout:   idle,
			ResponseHeaderTimeout: idle,
		},
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if !strings.HasPrefix(strings.ToLower(r.URL.String()), "https://") {
				return errors.New("refusing redirect away from https")
			}
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			return nil
		},
	}
	fail := func(err error) error {
		// The transport's own timeouts use the same limit and can fire before the idle timer cancels ctx.
		var ne net.Error
		if ctx.Err() != nil || (errors.As(err, &ne) && ne.Timeout()) {
			return stepWrap(fmt.Errorf("timed out after %s without data: %w", idle, err), "cannot fetch "+url)
		}
		return stepWrap(err, "cannot fetch "+url)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fail(err)
	}
	defer resp.Body.Close() //nolint:errcheck // read side
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return stepf("HTTP %d for %s", resp.StatusCode, url)
	}
	f, err := os.Create(dest)
	if err != nil {
		return stepWrap(err, "cannot fetch "+url)
	}
	if _, err := io.Copy(f, &idleReader{r: resp.Body, t: timer, d: idle, ctx: ctx}); err != nil {
		_ = f.Close() // the copy error is the one reported
		return fail(err)
	}
	if err := f.Close(); err != nil {
		return stepWrap(err, "cannot fetch "+url)
	}
	return nil
}

func (h *OSHost) InstallFile(src, dest string, mode os.FileMode) error {
	tmp := dest + ".part"
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("install %s: %w", dest, err)
	}
	defer in.Close() //nolint:errcheck // read side
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		return fmt.Errorf("install %s: %w", dest, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close() // the copy error is the one reported
		return fmt.Errorf("install %s: %w", dest, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("install %s: %w", dest, err)
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return fmt.Errorf("install %s: %w", dest, err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		return fmt.Errorf("install %s: %w", dest, err)
	}
	return nil
}

// ExtractMember copies one regular file out of a .tar.gz.
func (h *OSHost) ExtractMember(tarball, member, dest string) error {
	f, err := os.Open(tarball)
	if err != nil {
		return fmt.Errorf("open %s: %w", tarball, err)
	}
	defer f.Close() //nolint:errcheck // read side
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("read %s: %w", filepath.Base(tarball), err)
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return stepf("%s not found in %s", member, filepath.Base(tarball))
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", filepath.Base(tarball), err)
		}
		if hdr.Name != member {
			continue
		}
		if hdr.Typeflag != tar.TypeReg {
			return stepf("%s in %s is not a regular file", member, filepath.Base(tarball))
		}
		out, err := os.Create(dest)
		if err != nil {
			return fmt.Errorf("extract %s: %w", member, err)
		}
		if _, err := io.Copy(out, tr); err != nil { //nolint:gosec // size is checked against the pinned asset size beforehand
			_ = out.Close() // the copy error is the one reported
			return fmt.Errorf("extract %s: %w", member, err)
		}
		if err := out.Close(); err != nil {
			return fmt.Errorf("extract %s: %w", member, err)
		}
		return nil
	}
}

// ExtractZipMember copies one regular file out of a .zip. Callers verify the archive's size and sha256 first.
func (h *OSHost) ExtractZipMember(zipfile, member, dest string) error {
	zr, err := zip.OpenReader(zipfile)
	if err != nil {
		return fmt.Errorf("read %s: %w", filepath.Base(zipfile), err)
	}
	defer zr.Close() //nolint:errcheck // read side
	for _, zf := range zr.File {
		if zf.Name != member {
			continue
		}
		if !zf.Mode().IsRegular() {
			return stepf("%s in %s is not a regular file", member, filepath.Base(zipfile))
		}
		in, err := zf.Open()
		if err != nil {
			return fmt.Errorf("extract %s: %w", member, err)
		}
		defer in.Close() //nolint:errcheck // read side
		out, err := os.Create(dest)
		if err != nil {
			return fmt.Errorf("extract %s: %w", member, err)
		}
		if _, err := io.Copy(out, in); err != nil { //nolint:gosec // the archive size is checked against the pinned asset size beforehand
			_ = out.Close() // the copy error is the one reported
			return fmt.Errorf("extract %s: %w", member, err)
		}
		if err := out.Close(); err != nil {
			return fmt.Errorf("extract %s: %w", member, err)
		}
		return nil
	}
	return stepf("%s not found in %s", member, filepath.Base(zipfile))
}

func (h *OSHost) Symlink(target, link string) error {
	if cur, err := os.Readlink(link); err == nil {
		if cur == target {
			return nil
		}
		return stepf("%s already points elsewhere; remove it and re-run with --resume", link)
	}
	if h.Exists(link) {
		return stepf("%s exists and is not a symlink; move it away and re-run with --resume", link)
	}
	return os.Symlink(target, link)
}

func (h *OSHost) Readlink(path string) (string, error) { return os.Readlink(path) }

// Popen starts argv detached in its own session with output appended to logpath.
func (h *OSHost) Popen(argv []string, logpath string) error {
	log, err := os.OpenFile(logpath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open %s: %w", logpath, err)
	}
	defer log.Close() //nolint:errcheck // the child holds its own descriptor
	env := h.childEnv()
	path, err := lookPath(argv[0], pathOf(env))
	if err != nil {
		return err
	}
	cmd := exec.Command(path, argv[1:]...)
	cmd.Stdout, cmd.Stderr, cmd.Env = log, log, env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", argv[0], err)
	}
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("detach %s: %w", argv[0], err)
	}
	return nil
}

// WriteText writes via a .part file and a rename.
func (h *OSHost) WriteText(path, text string) error {
	tmp := path + ".part"
	if err := os.WriteFile(tmp, []byte(text), 0o666); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func isCharDevice(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// IsTerminal also needs stderr: gh prompts there, and a redirected stderr would hide the prompts and keep gh's output.
func (h *OSHost) IsTerminal() bool {
	return isCharDevice(os.Stdin) && isCharDevice(os.Stdout) && isCharDevice(os.Stderr)
}

func (h *OSHost) RunTTY(argv []string) int {
	if !isCharDevice(os.Stdin) { // the stdin rule: hand stdin to a child only when it is a terminal
		return 1
	}
	env := h.childEnv()
	path, err := lookPath(argv[0], pathOf(env))
	if err != nil {
		return 127
	}
	cmd := exec.Command(path, argv[1:]...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	// Ctrl-C at gh's prompts ends gh, not the installer: catch SIGINT here (the child still gets the default action)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	err = cmd.Run()
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return 1
}

func (h *OSHost) WaitEnter() error {
	if !isCharDevice(os.Stdin) { // the stdin rule: read stdin only when it is a terminal
		return errors.New("stdin is not a terminal")
	}
	if h.stdin == nil {
		h.stdin = bufio.NewReader(os.Stdin)
	}
	_, err := h.stdin.ReadString('\n')
	return err
}
