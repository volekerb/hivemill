package procx

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"
)

// cmdTimeout bounds every external command.
const cmdTimeout = 60 * time.Second

// Absolute tool paths; never resolved through PATH, never via a shell.
const (
	lsofPath = "/usr/sbin/lsof"
	psPath   = "/bin/ps"
)

// runner runs one external command. err is non-nil when the command could not
// be run to completion (missing binary, timeout, killed); a non-zero exit is
// reported through exit with err == nil.
type runner func(path string, args ...string) (stdout, stderr []byte, exit int, err error)

func defaultRunner(path string, args ...string) ([]byte, []byte, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) && ctx.Err() == nil {
		return out.Bytes(), errb.Bytes(), ee.ExitCode(), nil
	}
	if err != nil {
		return out.Bytes(), errb.Bytes(), -1, err
	}
	return out.Bytes(), errb.Bytes(), 0, nil
}
