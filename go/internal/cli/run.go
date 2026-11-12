package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

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
	return h.run(&ctx{db: db.DB, who: who, env: d.Env, deps: d, out: out, errb: errb}, p)
}
