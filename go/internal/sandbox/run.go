package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ErrNoToken is returned for a missing or badly protected token file; callers map it to exit 3.
var ErrNoToken = errors.New(NoTokenMsg)

// ReadToken is the token, or an ErrNoToken error. It must be a regular file, mode 0600, owned by us, never a symlink;
// the error names the path and the rule, never the token.
func ReadToken(path string) (string, error) {
	tok := readTokenFile(path)
	if tok == "" {
		return "", fmt.Errorf("%w (%s: must exist, be a regular file, mode 0600, owned by uid %d)", ErrNoToken, path, os.Getuid())
	}
	return tok, nil
}

// readTokenFile is ReadTokenFile without ghcred's printable-token rule: nsbox only trims.
func readTokenFile(path string) string {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return ""
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0o600 || st.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return ""
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); !ok || int(sys.Uid) != os.Getuid() {
		return ""
	}
	b, _ := io.ReadAll(f)
	return strings.TrimSpace(string(b))
}

var tokenShape = regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]+`)

// Redact replaces the whole OAuth token (when known) and, with shape, anything shaped like one.
func Redact(text, token string, shape bool) string {
	if token != "" {
		text = strings.ReplaceAll(text, token, "[redacted-token]")
	}
	if shape {
		return tokenShape.ReplaceAllString(text, "[redacted-token]")
	}
	return text
}

// StartLogCap is the size limit of <trial>/log/start.log.
const StartLogCap = 64 * 1024

// StartLogAppend appends text (exact token redacted) to <trial>/log/start.log, never growing it past limit bytes
// (what does not fit is dropped). It returns false when something was dropped or could not be written and never
// panics: the log is a diagnostic. Symlinks planted by the sandboxed claude are never followed.
func StartLogAppend(trialDir, text, token string, limit int) bool {
	data := []byte(Redact(text, token, false))
	tfd, err := syscall.Open(trialDir, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	defer syscall.Close(tfd)
	if err := syscall.Mkdir(fdp(tfd)+"/log", 0o700); err != nil && !errors.Is(err, syscall.EEXIST) {
		return false
	}
	dfd, err := syscall.Open(fdp(tfd)+"/log", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	defer syscall.Close(dfd)
	fd, err := syscall.Open(fdp(dfd)+"/start.log", syscall.O_WRONLY|syscall.O_APPEND|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return false
	}
	defer syscall.Close(fd)
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil || st.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return false
	}
	room := limit - int(st.Size)
	if room < 0 {
		room = 0
	}
	if room > 0 {
		n := len(data)
		if n > room {
			n = room
		}
		if _, err := syscall.Write(fd, data[:n]); err != nil {
			return false
		}
	}
	return len(data) <= room
}

// Contained is the result of a contained run.
type Contained struct {
	ReturnCode int
	Output     string
	TimedOut   bool
}

// UsernsAvailable reports whether this box can run the stage-1 namespaces unprivileged.
func UsernsAvailable(unshare string) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, unshare, "-r", "-n", "-m", "--pid", "--fork", "--mount-proc", "true")
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return false, "unshare -r -n -m failed (user namespaces restricted?): " + strings.TrimSpace(stderr.String())
		}
		return false, fmt.Sprintf("cannot run %s: %v", unshare, err)
	}
	return true, "ok"
}

// SubidRange is the (start, count) of the largest name:start:count line of /etc/subuid or /etc/subgid for the user
// (by name or number); (0, 0) when there is none or the file is unreadable.
func SubidRange(path, user string, uid int) (int, int) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, 0
	}
	bs, bc := 0, 0
	for _, ln := range strings.Split(string(b), "\n") {
		f := strings.Split(ln, ":")
		if len(f) != 3 || (f[0] != user && f[0] != strconv.Itoa(uid)) {
			continue
		}
		s, e1 := strconv.Atoi(f[1])
		c, e2 := strconv.Atoi(f[2])
		if e1 == nil && e2 == nil && isDigits(f[1]) && isDigits(f[2]) && c > bc {
			bs, bc = s, c
		}
	}
	return bs, bc
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// RunContained runs argv inside the stage-1 view of cfg: no proxy, no forwarder, no token. env nil means an
// allow-list (EnvPass, LC_*, HOME=trial, extra), never the caller's whole environment. The tree dies on timeout.
func RunContained(cfg *Cfg, argv []string, cwd string, env []string, timeout time.Duration) (Contained, error) {
	s1, err := ComposeStage1(cfg, argv)
	if err != nil {
		return Contained{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, s1[0], s1[1:]...)
	cmd.Dir, cmd.Env = cwd, env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) } // --kill-child takes the pid ns with its init
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	runErr := cmd.Run()
	res := Contained{Output: out.String(), TimedOut: ctx.Err() != nil}
	var ee *exec.ExitError
	switch {
	case errors.As(runErr, &ee):
		res.ReturnCode = ee.ExitCode()
	case runErr != nil && !res.TimedOut:
		return res, runErr
	}
	if res.TimedOut {
		res.ReturnCode = -1
	}
	return res, nil
}

// EnvPassFrom is the allow-list env: EnvPass and LC_* from the caller's, plus HOME and the extras.
func EnvPassFrom(caller []string, home string, extra map[string]string) []string {
	var out []string
	for _, kv := range caller {
		k, _, _ := strings.Cut(kv, "=")
		if contains(EnvPass, k) || strings.HasPrefix(k, "LC_") {
			out = append(out, kv)
		}
	}
	out = append(out, "HOME="+home)
	for k, v := range extra {
		out = append(out, k+"="+v)
	}
	return out
}

// Main dispatches the stage commands of the single Go binary: `--setup <cfg> -- args` (stage 1), `--launch <cfg> --
// args` (subuid stage 0), `forward <sock>` (the loopback forwarder), `git-credential <slugs> <action>` and `gh-shim
// <real gh> <dir> args` (the credential helpers). It returns the process exit code; handled is false for any other
// first argument so the caller keeps its own commands.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) (code int, handled bool) {
	if len(args) < 2 {
		return 0, false
	}
	switch {
	case args[0] == "--setup" && len(args) >= 3 && args[2] == "--":
		var c Cfg
		if err := json.Unmarshal([]byte(args[1]), &c); err != nil {
			fmt.Fprintln(stderr, "nsbox: bad cfg:", err)
			return 2, true
		}
		fmt.Fprintln(stderr, Setup(&c, args[3:])) // returns only on failure
		return 1, true
	case args[0] == "--launch" && len(args) >= 3 && args[2] == "--":
		var c Cfg
		if err := json.Unmarshal([]byte(args[1]), &c); err != nil {
			fmt.Fprintln(stderr, "nsbox: bad cfg:", err)
			return 2, true
		}
		return Launch(&c, args[3:], stderr), true
	case args[0] == launchChildArg && len(args) >= 3 && args[2] == "--":
		var c Cfg
		if err := json.Unmarshal([]byte(args[1]), &c); err != nil {
			fmt.Fprintln(stderr, "nsbox: bad cfg:", err)
			return 2, true
		}
		return LaunchChild(&c, args[3:], stderr), true
	case args[0] == "worker-shim" && len(args) >= 2:
		var sc ShimCfg
		if err := json.Unmarshal([]byte(args[1]), &sc); err != nil {
			fmt.Fprintln(stderr, "workerbox: bad shim cfg:", err)
			return 2, true
		}
		err := ShimMain(&sc, args[2:], os.Environ())
		fmt.Fprintln(stderr, err)
		if errors.Is(err, ErrNoToken) {
			return 3, true
		}
		return 1, true
	case args[0] == "forward" && len(args) == 2:
		fmt.Fprintln(stderr, Forward(args[1], "127.0.0.1", 3128))
		return 1, true
	case args[0] == "git-credential" && len(args) == 3:
		return GitCredential(args[1], args[2], stdin, stdout, stderr, EnvMap(os.Environ())), true
	case args[0] == "gh-shim" && len(args) >= 3:
		return GhShim(args[1], args[2], args[3:], os.Environ(), stderr), true
	}
	return 0, false
}
