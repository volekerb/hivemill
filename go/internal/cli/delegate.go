package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"factory/go/internal/identity"
)

// ExecFunc replaces the process (syscall.Exec); it only returns on failure. Tests inject a recorder.
type ExecFunc func(path string, argv []string, env []string) error

// errDelegate is returned by command code that cannot answer faithfully; Run then execs Python.
var errDelegate = identity.ErrDelegate

// pyEntry is <dir of the running bin/factory>/factory: argv[0] is the bin/factory path the Python dispatcher was
// started as (symlinks resolved, like the Python side's realpath).
func (d *Deps) pyEntry() string {
	a0 := d.Argv0
	if !strings.Contains(a0, "/") {
		if p, err := d.lookPath(a0); err == nil {
			a0 = p
		}
	}
	return filepath.Join(filepath.Dir(identity.Realpath(a0)), "factory")
}

func (d *Deps) lookPath(f string) (string, error) {
	if d.LookPath != nil {
		return d.LookPath(f)
	}
	return exec.LookPath(f)
}

// delegate runs the original argv in Python: execv python3 <bin dir>/factory args... with FACTORY_CLI_IMPL=python
// (which keeps the dispatcher from sending the command straight back here). It never returns on success.
func (d *Deps) delegate() int {
	py, err := d.lookPath("python3")
	if err != nil {
		fmt.Fprintf(d.Stderr, "factory: cannot hand the command to python: %v; install python3 or fix PATH\n", err)
		return 1
	}
	argv := append([]string{"python3", d.pyEntry()}, d.Args...)
	env := make([]string, 0, len(d.Environ)+1)
	for _, kv := range d.Environ {
		if !strings.HasPrefix(kv, "FACTORY_CLI_IMPL=") {
			env = append(env, kv)
		}
	}
	env = append(env, "FACTORY_CLI_IMPL=python")
	ex := d.Exec
	if ex == nil {
		if len(d.StdinReplay) > 0 {
			if err := replayStdin(d.StdinReplay); err != nil {
				fmt.Fprintf(d.Stderr, "factory: cannot replay stdin for python: %v; rerun with --reason text\n", err)
				return 1
			}
		}
		ex = syscall.Exec
	}
	err = ex(py, argv, env)
	if err == nil {
		return 0 // only a test recorder returns nil
	}
	var pe *os.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	fmt.Fprintf(d.Stderr, "factory: exec python3 %s failed: %v; run it with python3 directly\n", argv[1], err)
	return 1
}

// replayStdinMax is far below the smallest pipe buffer (one 4 KiB page under the user pipe quota), so the write below
// never blocks before Python has a reader.
const replayStdinMax = 4000

// replayStdin makes fd 0 a pipe holding b, so Python reads what Go already consumed.
func replayStdin(b []byte) error {
	if len(b) > replayStdinMax {
		return fmt.Errorf("text of %d bytes is too large to replay (limit %d)", len(b), replayStdinMax)
	}
	r, w, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("pipe: %w", err)
	}
	if _, err := w.Write(b); err != nil {
		return fmt.Errorf("write pipe: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close pipe: %w", err)
	}
	if err := syscall.Dup3(int(r.Fd()), 0, 0); err != nil { // Dup3 exists on every linux arch, Dup2 not on arm64
		return fmt.Errorf("dup3: %w", err)
	}
	return nil
}
