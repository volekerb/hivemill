package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"factory/go/internal/factorydb"
	"factory/go/internal/identity"
)

// Deps is everything Run touches outside its own memory; tests replace the process-level parts.
type Deps struct {
	Args     []string // os.Args[1:]
	Argv0    string   // os.Args[0]: the bin/factory path the Python dispatcher was started as
	Environ  []string // os.Environ()
	Env      identity.Env
	Stdout   io.Writer
	Stderr   io.Writer
	Exec     ExecFunc                     // nil: syscall.Exec
	LookPath func(string) (string, error) // nil: exec.LookPath
	Getpid   func() int                   // nil: os.Getpid
	Getppid  func() int                   // nil: os.Getppid

	// Write-port seams (G-041 S2g).
	Stdin  io.Reader              // nil: os.Stdin
	Getwd  func() (string, error) // nil: os.Getwd
	RunCmd RunCmdFunc             // nil: os/exec, never a shell
	Trace  *[]Stmt                // non-nil: every ctx.exec appends the (verb, table) of its statement
}

// RunCmdFunc runs name with argv (the arguments after name) in dir with env (nil: the process environment) and
// returns its output and exit code. err is only for failing to start or a timeout; a non-zero exit is code != 0.
// name is looked up on the PATH of env, so a stub directory on that PATH is found.
type RunCmdFunc func(name string, argv []string, dir string, env []string, timeout time.Duration) (stdout, stderr string, code int, err error)

func (d *Deps) stdin() io.Reader {
	if d.Stdin != nil {
		return d.Stdin
	}
	return os.Stdin
}

func (d *Deps) getwd() (string, error) {
	if d.Getwd != nil {
		return d.Getwd()
	}
	return os.Getwd()
}

func (d *Deps) runCmd(name string, argv []string, dir string, env []string, timeout time.Duration) (string, string, int, error) {
	if d.RunCmd != nil {
		return d.RunCmd(name, argv, dir, env, timeout)
	}
	return osRunCmd(name, argv, dir, env, timeout)
}

// lookPathIn finds name in the PATH entries of env (the process PATH when env has none).
func lookPathIn(name string, env []string) (string, error) {
	if strings.Contains(name, "/") {
		return name, nil
	}
	path := os.Getenv("PATH")
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
		}
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			dir = "."
		}
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode().Perm()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s: not found on PATH", name)
}

func osRunCmd(name string, argv []string, dir string, env []string, timeout time.Duration) (string, string, int, error) {
	bin, err := lookPathIn(name, env)
	if err != nil {
		return "", "", 0, err
	}
	cctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, argv...)
	cmd.Dir, cmd.Env = dir, env
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return out.String(), errb.String(), 0, nil
	case cctx.Err() != nil:
		return out.String(), errb.String(), 0, fmt.Errorf("%s timed out after %s", name, timeout)
	case errors.As(err, &ee):
		return out.String(), errb.String(), ee.ExitCode(), nil
	}
	return out.String(), errb.String(), 0, err
}

func (d *Deps) pid() int {
	if d.Getpid != nil {
		return d.Getpid()
	}
	return os.Getpid()
}

func (d *Deps) ppid() int {
	if d.Getppid != nil {
		return d.Getppid()
	}
	return os.Getppid()
}

// rawExit ends the command with Code after Run wrote Stderr verbatim: no "factory: " prefix, no added newline.
type rawExit struct {
	Code   int
	Stderr string
}

func (e *rawExit) Error() string { return e.Stderr }

// Run executes one factory command and returns its exit code. It never returns when it hands over to Python.
func Run(d Deps) int {
	var out, errb bytes.Buffer
	d.Env.Stderr = &errb
	code, err := d.run(&out, &errb)
	var ee *identity.ExitError
	var re *rawExit
	switch {
	case errors.Is(err, errDelegate):
		return d.delegate() // buffered output is dropped: Python redoes the whole command
	case errors.As(err, &ee):
		fmt.Fprintf(&errb, "factory: %s\n", ee.Msg)
		code = ee.Code
	case errors.As(err, &re):
		errb.WriteString(re.Stderr)
		code = re.Code
	case err != nil:
		fmt.Fprintf(&errb, "factory: %v\n", err)
		code = 1
	}
	d.Stdout.Write(out.Bytes())
	d.Stderr.Write(errb.Bytes())
	return code
}

func (d *Deps) run(out, errb *bytes.Buffer) (int, error) {
	if err := d.Env.EvaluatorHomeGuard(); err != nil {
		return 0, err
	}
	if d.Env.Getenv("FACTORY_CLI_IMPL") == "python" {
		return 0, errDelegate // the owner (or a delegating parent) forced Python: never answer here, never loop
	}
	h, p, ok := lookup(d.Args)
	if !ok {
		return 0, errDelegate
	}
	path := factorydb.ResolvePath(d.Env.Home())
	if _, err := os.Stat(path); err != nil {
		return 0, errDelegate // Python says what to do: `factory init`, or that the file moved
	}
	db, err := factorydb.Open(path) // a user_version mismatch (ErrSchemaMismatch) lands here too
	if err != nil {
		return 0, errDelegate
	}
	defer db.Close()
	who, err := d.Env.Resolve(func() (*factorydb.DB, error) { return db, nil })
	if err != nil {
		return 0, err
	}
	if err := identity.RequireKnown(who, h.identity(p)); err != nil {
		return 0, err
	}
	return h.run(&ctx{db: db.DB, who: who, env: d.Env, deps: d, out: out, errb: errb, trace: d.Trace}, p)
}
