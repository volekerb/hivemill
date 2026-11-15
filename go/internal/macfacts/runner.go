package macfacts

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"
)

// Result is the outcome of one command.
type Result struct {
	Stdout   string
	Stderr   string
	Exit     int   // -1 when the command did not start or was killed
	TimedOut bool  // the context deadline cut the command
	Err      error // start failure (e.g. not found); nil on a normal exit
}

// Runner runs one command with stdin from /dev/null.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) Result
}

// capWriter keeps at most max bytes and drops the rest.
type capWriter struct {
	buf bytes.Buffer
	max int
}

func (w *capWriter) Write(p []byte) (int, error) {
	if room := w.max - w.buf.Len(); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		w.buf.Write(p[:room])
	}
	return len(p), nil
}

// ExecRunner runs real commands. Dir is the run's own temp dir (the working
// directory of every child).
type ExecRunner struct {
	Dir string
}

const maxCapture = 256 * 1024

// Run implements Runner. Stdin is nil, which os/exec wires to the null device.
func (r ExecRunner) Run(ctx context.Context, name string, args ...string) Result {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = r.Dir
	cmd.Stdin = nil
	out, errw := &capWriter{max: maxCapture}, &capWriter{max: maxCapture}
	cmd.Stdout, cmd.Stderr = out, errw
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	res := Result{Stdout: out.buf.String(), Stderr: errw.buf.String()}
	if err == nil {
		return res
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		res.TimedOut, res.Exit, res.Err = true, -1, err
		return res
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.Exit = ee.ExitCode()
		return res
	}
	res.Exit, res.Err = -1, err
	return res
}
