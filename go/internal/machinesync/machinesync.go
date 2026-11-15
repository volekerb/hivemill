// Package machinesync copies an owner-supplied allow-list of secret files to a worker machine over ssh.
// File content is never printed, logged or hashed; the remote file is created 0600 from its first byte.
package machinesync

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
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
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, refuse("refused: cannot read list file %s", path)
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

// CheckSrc refuses anything but a regular, non-symlink file whose real path is under localHome.
func CheckSrc(src, localHome string) error {
	if !plainAbs(src) {
		return refuse("refused: src %s is not a clean absolute path", src)
	}
	realHome, err := filepath.EvalSymlinks(localHome)
	if err != nil {
		return refuse("refused: cannot resolve local home %s", localHome)
	}
	fi, err := os.Lstat(src)
	if err != nil {
		return refuse("refused: src %s cannot be read (missing?)", src)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return refuse("refused: src %s is a symlink", src)
	}
	if !fi.Mode().IsRegular() {
		return refuse("refused: src %s is not a regular file", src)
	}
	real, err := filepath.EvalSymlinks(src)
	if err != nil || !under(real, realHome) {
		return refuse("refused: src %s is outside the local home", src)
	}
	return nil
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
	Machine   string
	Entries   []Entry
	DryRun    bool
	LocalHome string
	Stdout    io.Writer
	Stderr    io.Writer
}

// ErrFailed marks a copy/verify failure (CLI exit 1). Details were already written to Stderr, redacted.
var ErrFailed = errors.New("copy failed")

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// ssh runs `ssh -o BatchMode=yes <machine> -- <remote command>` with exec argv (no local shell).
func ssh(machine, remote string, stdin io.Reader) (stdout, stderr []byte, err error) {
	cmd := exec.Command("ssh", "-o", "BatchMode=yes", machine, "--", remote)
	cmd.Stdin = stdin
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err = cmd.Run()
	return o.Bytes(), e.Bytes(), err
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
	for _, e := range o.Entries {
		if err := CheckSrc(e.Src, o.LocalHome); err != nil {
			return err
		}
	}
	out, errb, err := ssh(o.Machine, `printf %s "$HOME"`, nil)
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
	for _, e := range o.Entries {
		if o.DryRun {
			fmt.Fprintf(o.Stdout, "would copy %s -> %s\n", e.Src, e.Dst)
			continue
		}
		if err := copyOne(o, e); err != nil {
			return err
		}
	}
	return nil
}

func copyOne(o Options, e Entry) error {
	content, err := os.ReadFile(e.Src)
	if err != nil {
		fmt.Fprintf(o.Stderr, "cannot read %s\n", e.Src)
		return ErrFailed
	}
	tmp := e.Dst + ".sync-tmp"
	// The remote script (a) finds the deepest existing ancestor of the dst dir and refuses when its real path is
	// not under the real remote home (symlinked parents), (b) creates missing dirs 0700, (c) removes any stale
	// temp file, creates a fresh one with noclobber (O_EXCL, 0600 via umask) and removes it on any failure.
	remote := fmt.Sprintf(`umask 077; set -e; a=%s; while [ ! -d "$a" ]; do a=$(dirname "$a"); done; `+
		`h=$(cd "$HOME" && pwd -P); r=$(cd "$a" && pwd -P); `+
		`case "$r/" in "$h"/*) ;; *) echo "dst resolves outside the remote home" >&2; exit 3;; esac; `+
		`mkdir -p -m 700 %s; t=%s; rm -f "$t"; trap 'rm -f "$t"' EXIT; (set -C; cat > "$t"); mv -f "$t" %s`,
		shq(filepath.Dir(e.Dst)), shq(filepath.Dir(e.Dst)), shq(tmp), shq(e.Dst))
	_, errb, err := ssh(o.Machine, remote, bytes.NewReader(content))
	if err != nil {
		fmt.Fprintf(o.Stderr, "copy to %s failed: %v\n%s\n", e.Dst, err, bytes.TrimSpace(Redact(errb, content)))
		return ErrFailed
	}
	sout, serr, err := ssh(o.Machine, "stat -c %a "+shq(e.Dst), nil)
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
