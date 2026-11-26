package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"factory/go/internal/hm"
)

// stableStep is the stable binary that must run this call instead of this process ("" = run here): a session hook, or a
// run, gate, judge, merge or eval step of one task, of a stable-channel project.
func stableStep(r *hm.Runner, args []string, actor string) string {
	arg := func(i int) string {
		if len(args) > i {
			return args[i]
		}
		return ""
	}
	task := func(a string) string {
		if !taskRe.MatchString(a) {
			return ""
		}
		return r.DelegateTask(strings.ToUpper(a))
	}
	switch args[0] {
	case "run":
		switch arg(1) {
		case "hook-session":
			return r.DelegateAgent(actor)
		case "stop":
			return task(arg(2))
		}
		return task(arg(1))
	case "gate":
		if arg(1) == "hook" {
			return r.DelegateAgent(actor)
		}
		return task(arg(1))
	case "judge":
		if arg(1) == "hook" {
			return r.DelegateAgent(actor)
		}
		return task(arg(2))
	case "merge", "eval":
		return task(arg(1))
	}
	return ""
}

// runStable runs this call with the stable binary, stdin and outputs passed through. started is false when it could not
// be started at all (nothing was read from stdin: the caller runs the call itself).
func runStable(bin, home string, args []string, stdin io.Reader, stdout, stderr io.Writer) (code int, started bool) {
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "FACTORY_HOME="+home)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(stderr, "hivemill: stable binary: %v; running here\n", err)
		return 0, false
	}
	err := cmd.Wait()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, true
	case errors.As(err, &ee) && ee.ExitCode() >= 0:
		return ee.ExitCode(), true
	}
	fmt.Fprintf(stderr, "hivemill: stable binary: %v\n", err)
	return 1, true
}
