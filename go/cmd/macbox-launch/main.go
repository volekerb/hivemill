// Command macbox-launch drops to a worker account (via sudo) and execs
// /usr/bin/sandbox-exec. Installed root-owned by the M5 setup script (ADR 0003 §3.2).
package main

import (
	"fmt"
	"io"
	"os"
	"syscall"

	"factory/go/internal/macbox"
)

// execFn replaces the process; tests swap it to record argv and env.
var execFn = syscall.Exec

// umaskFn sets the umask; tests swap it.
var umaskFn = syscall.Umask

func main() { os.Exit(run(os.Args, os.Environ(), os.Stderr)) }

func run(argv, environ []string, stderr io.Writer) int {
	if err := macbox.RefuseLauncherUID(macbox.LauncherUID(), macbox.SudoUIDFromEnv(environ)); err != nil {
		fmt.Fprintln(stderr, err)
		return 3
	}
	execArgv, err := macbox.SplitLauncherArgv(argv[1:])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	env := macbox.EnvironMap(macbox.FilterEnviron(environ))
	if err := macbox.ApplyLauncherDirs(env); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	umaskFn(0o022)
	if err := execFn(execArgv[0], execArgv, macbox.EnvList(env)); err != nil {
		fmt.Fprintf(stderr, "macbox launcher: exec %s: %v\n", execArgv[0], err)
		return 126
	}
	return 0
}
