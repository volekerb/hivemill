// Command macbox-launch drops to a worker account (via sudo) and execs
// sandbox-exec. Installed root-owned by the M5 setup script (ADR 0003 §3.2).
package main

import (
	"fmt"
	"os"
	"syscall"

	"factory/go/internal/macbox"
)

func main() { os.Exit(run(os.Args, os.Environ())) }

func run(argv, environ []string) int {
	ownerUID := macbox.OwnerUIDFromEnv(environ)
	uid := macbox.LauncherUID()
	if err := macbox.RefuseLauncherUID(uid, ownerUID); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 3
	}
	execArgv, err := macbox.SplitLauncherArgv(argv[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	envMap := macbox.EnvironMap(environ)
	filtered := macbox.FilterEnviron(environ)
	envMap = macbox.EnvironMap(filtered)
	macbox.ApplyLauncherDirs(envMap)
	syscall.Umask(0o022)
	if err := syscall.Exec(execArgv[0], execArgv, macbox.EnvList(envMap)); err != nil {
		fmt.Fprintf(os.Stderr, "macbox launcher: exec %s: %v\n", execArgv[0], err)
		return 126
	}
	return 0
}
