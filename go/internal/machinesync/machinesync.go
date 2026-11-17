// Package machinesync copies an owner-supplied allow-list of secret files to a worker machine over ssh.
// File content is never printed, logged or hashed; the remote file is created 0600 from its first byte.
package machinesync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// Size and time bounds. A secret file larger than MaxFileBytes, a run larger than MaxTotalBytes or a list file larger
// than MaxListBytes is refused (exit 2). Every ssh call is killed after DefaultTimeout (Options.Timeout), and the whole
// run after DefaultRunTimeout (Options.RunTimeout); each call's deadline is the earlier of the two.
const (
	MaxFileBytes      = 1 << 20
	MaxTotalBytes     = 16 << 20
	MaxListBytes      = 1 << 20
	DefaultTimeout    = 60 * time.Second
	DefaultRunTimeout = 10 * time.Minute
	connectTimeout    = "10"
)

// Entry is one allow-list item: absolute local src and absolute remote dst.
type Entry struct {
	Src string `json:"src"`
	Dst string `json:"dst"`
}

// RefusedError marks a validation refusal (CLI exit 2). The message names a path or machine, never content.
type RefusedError struct{ Msg string }

func (e *RefusedError) Error() string { return e.Msg }

func refuse(format string, a ...any) error { return &RefusedError{Msg: fmt.Sprintf(format, a...)} }

var machineRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidateMachine refuses names containing "factory" (case-insensitive, rule R2) and names ssh could read as an option.
func ValidateMachine(name string) error {
	if strings.Contains(strings.ToLower(name), "factory") {
		return refuse(`refused: machine name must not contain "factory"`)
	}
	if !machineRE.MatchString(name) {
		return refuse("refused: machine name %q must match [A-Za-z0-9][A-Za-z0-9._-]*", name)
	}
	return nil
}

// LoadList reads the JSON allow-list: an array of {src, dst}.
func LoadList(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, refuse("refused: cannot read list file %s", path)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxListBytes+1))
	if err != nil {
		return nil, refuse("refused: cannot read list file %s", path)
	}
	if len(b) > MaxListBytes {
		return nil, refuse("refused: list file %s is larger than %d bytes", path, MaxListBytes)
	}
	var es []Entry
	if err := json.Unmarshal(b, &es); err != nil {
		return nil, refuse("refused: list file %s is not a JSON array of {src,dst}", path)
	}
	if len(es) == 0 {
		return nil, refuse("refused: list file %s has no entries", path)
	}
	return es, nil
}

func under(path, root string) bool {
	return path != root && strings.HasPrefix(path, strings.TrimSuffix(root, "/")+"/")
}

func plainAbs(p string) bool {
	return filepath.IsAbs(p) && filepath.Clean(p) == p && !strings.ContainsAny(p, "\n\r\x00")
}

// CheckSrc refuses anything but a regular, non-symlink file of at most MaxFileBytes whose real path is under localHome.
func CheckSrc(src, localHome string) error {
	_, err := checkSrc(src, localHome)
	return err
}

// checkSrc is CheckSrc returning the Lstat info, so the later read can prove it opened the same file (dev/ino).
func checkSrc(src, localHome string) (os.FileInfo, error) {
	if !plainAbs(src) {
		return nil, refuse("refused: src %s is not a clean absolute path", src)
	}
	realHome, err := filepath.EvalSymlinks(localHome)
	if err != nil {
		return nil, refuse("refused: cannot resolve local home %s", localHome)
	}
	fi, err := os.Lstat(src)
	if err != nil {
		return nil, refuse("refused: src %s cannot be read (missing?)", src)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, refuse("refused: src %s is a symlink", src)
	}
	if !fi.Mode().IsRegular() {
		return nil, refuse("refused: src %s is not a regular file", src)
	}
	real, err := filepath.EvalSymlinks(src)
	if err != nil || !under(real, realHome) {
		return nil, refuse("refused: src %s is outside the local home", src)
	}
	if fi.Size() > MaxFileBytes {
		return nil, refuse("refused: src %s is larger than %d bytes", src, MaxFileBytes)
	}
	return fi, nil
}

// CheckDst refuses a dst that is not a clean absolute path under the remote home.
func CheckDst(dst, remoteHome string) error {
	if !plainAbs(dst) {
		return refuse("refused: dst %s is not a clean absolute path", dst)
	}
	if !under(dst, filepath.Clean(remoteHome)) {
		return refuse("refused: dst %s is outside the remote home", dst)
	}
	return nil
}

// Options configures Run.
type Options struct {
	Machine    string
	Entries    []Entry
	DryRun     bool
	LocalHome  string
	Timeout    time.Duration // per ssh call; 0 means DefaultTimeout
	RunTimeout time.Duration // whole run; 0 means DefaultRunTimeout
	Stdout     io.Writer
	Stderr     io.Writer
}

// afterCheck runs between validation and the first read; tests use it to swap a src after it was checked.
var afterCheck = func() {}

// ErrFailed marks a copy/verify failure (CLI exit 1). Details were already written to Stderr, redacted.
var ErrFailed = errors.New("copy failed")

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// ssh runs `ssh -o BatchMode=yes -o ConnectTimeout=.. <machine> -- <remote command>` with exec argv (no local shell),
// killed after o.Timeout. A timeout is reported as an error naming the limit.
func ssh(runCtx context.Context, o Options, remote string, stdin io.Reader) (stdout, stderr []byte, err error) {
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(runCtx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout="+connectTimeout,
		"-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=3", o.Machine, "--", remote)
	cmd.WaitDelay = 2 * time.Second // do not hang on pipes held open by a killed ssh's children
	cmd.Stdin = stdin
	var ob, eb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &ob, &eb
	err = cmd.Run()
	if runCtx.Err() == context.DeadlineExceeded {
		err = errors.New("timed out: whole-run deadline reached")
	} else if ctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("timed out after %s", timeout)
	}
	return ob.Bytes(), eb.Bytes(), err
}

// Redact replaces every line of text that contains the whole content or any line (4+ bytes) of it.
func Redact(text, content []byte) []byte {
	var needles [][]byte
	if t := bytes.TrimSpace(content); len(t) > 0 {
		needles = append(needles, t)
	}
	for _, l := range bytes.Split(content, []byte("\n")) {
		if l = bytes.TrimSpace(l); len(l) >= 4 {
			needles = append(needles, l)
			if i := bytes.IndexAny(l, "=:"); i >= 0 {
				if v := bytes.TrimSpace(l[i+1:]); len(v) >= 4 {
					needles = append(needles, v) // KEY=value: also catch the bare value
				}
			}
		}
	}
	lines := bytes.Split(text, []byte("\n"))
	for i, line := range lines {
		for _, n := range needles {
			if bytes.Contains(line, n) {
				lines[i] = []byte("[redacted: line contained file content]")
				break
			}
		}
	}
	return bytes.Join(lines, []byte("\n"))
}

// Run validates every entry, then (unless DryRun) copies each. Returns *RefusedError, ErrFailed or nil.
// Even a dry run makes one read-only ssh call to learn the remote home for the dst check.
func Run(o Options) error {
	if err := ValidateMachine(o.Machine); err != nil {
		return err
	}
	infos := make([]os.FileInfo, len(o.Entries))
	var total int64
	for i, e := range o.Entries {
		fi, err := checkSrc(e.Src, o.LocalHome)
		if err != nil {
			return err
		}
		infos[i] = fi
		if total += fi.Size(); total > MaxTotalBytes {
			return refuse("refused: total size of the list exceeds %d bytes (at src %s)", MaxTotalBytes, e.Src)
		}
	}
	runTimeout := o.RunTimeout
	if runTimeout <= 0 {
		runTimeout = DefaultRunTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()
	out, errb, err := ssh(ctx, o, `printf %s "$HOME"`, nil)
	remoteHome := strings.TrimSpace(string(out))
	if err != nil || !filepath.IsAbs(remoteHome) {
		fmt.Fprintf(o.Stderr, "ssh %s: cannot read remote home: %v\n%s\n", o.Machine, err, bytes.TrimSpace(errb))
		return ErrFailed
	}
	for _, e := range o.Entries {
		if err := CheckDst(e.Dst, remoteHome); err != nil {
			return err
		}
	}
	afterCheck()
	for i, e := range o.Entries {
		if o.DryRun {
			fmt.Fprintf(o.Stdout, "would copy %s -> %s\n", e.Src, e.Dst)
			continue
		}
		if err := copyOne(ctx, o, e, infos[i]); err != nil {
			return err
		}
	}
	return nil
}

// readChecked opens src without following a final symlink, proves via fstat that the fd is the same regular file
// (dev/ino) that checkSrc approved, and reads at most MaxFileBytes from that fd. A swapped src is refused.
func readChecked(src string, checked os.FileInfo) ([]byte, error) {
	f, err := os.OpenFile(src, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, refuse("refused: src %s became a symlink after the check", src)
		}
		return nil, refuse("refused: src %s cannot be opened", src)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, refuse("refused: src %s cannot be inspected", src)
	}
	if !fi.Mode().IsRegular() || !os.SameFile(fi, checked) {
		return nil, refuse("refused: src %s changed after the check (not the same regular file)", src)
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return nil, refuse("refused: src %s cannot be read", src)
	}
	if len(b) > MaxFileBytes {
		return nil, refuse("refused: src %s is larger than %d bytes", src, MaxFileBytes)
	}
	return b, nil
}

func copyOne(ctx context.Context, o Options, e Entry, checked os.FileInfo) error {
	content, err := readChecked(e.Src, checked)
	if err != nil {
		return err
	}
	tmp := e.Dst + ".sync-tmp"
	// The remote script (a) finds the deepest existing ancestor of the dst dir and refuses when its real path is
	// not under the real remote home (symlinked parents), (b) creates missing dirs 0700, (c) removes any stale
	// temp file, creates a fresh one with noclobber (O_EXCL, 0600 via umask) and removes it on any failure.
	remote := fmt.Sprintf(`umask 077; set -e; if [ -d %s ]; then echo "dst is a directory" >&2; exit 3; fi; a=%s; while [ ! -d "$a" ]; do a=$(dirname "$a"); done; `+
		`h=$(cd "$HOME" && pwd -P); r=$(cd "$a" && pwd -P); `+
		`case "$r/" in "$h"/*) ;; *) echo "dst resolves outside the remote home" >&2; exit 3;; esac; `+
		`mkdir -p -m 700 %s; t=%s; rm -f "$t"; trap 'rm -f "$t"' EXIT; (set -C; cat > "$t"); mv -f "$t" %s`,
		shq(e.Dst), shq(filepath.Dir(e.Dst)), shq(filepath.Dir(e.Dst)), shq(tmp), shq(e.Dst))
	_, errb, err := ssh(ctx, o, remote, bytes.NewReader(content))
	if err != nil {
		fmt.Fprintf(o.Stderr, "copy to %s failed: %v\n%s\n", e.Dst, err, bytes.TrimSpace(Redact(errb, content)))
		return ErrFailed
	}
	sout, serr, err := ssh(ctx, o, "stat -c %a "+shq(e.Dst), nil)
	if err != nil {
		fmt.Fprintf(o.Stderr, "mode check failed for %s: stat failed: %v\n%s\n", e.Dst, err, bytes.TrimSpace(Redact(serr, content)))
		return ErrFailed
	}
	if got := strings.TrimSpace(string(sout)); got != "600" {
		fmt.Fprintf(o.Stderr, "mode check failed for %s: mode is %s, want 600\n", e.Dst, Redact([]byte(got), content))
		return ErrFailed
	}
	fmt.Fprintf(o.Stdout, "copied %s mode 600\n", e.Dst)
	return nil
}
