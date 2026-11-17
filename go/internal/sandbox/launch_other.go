//go:build !linux

package sandbox

import (
	"fmt"
	"io"
	"runtime"
)

// Fail-closed stand-ins for the Linux-only subuid stage 0 (launch_linux.go): the command is never run.

const launchChildArg = "--launch-child"

// SubuidAvailable is always (false, why) off Linux.
func SubuidAvailable(unshare, self string) (bool, string) {
	return false, unsupportedOS(runtime.GOOS).Error()
}

// Launch prints the refusal and returns 1: the command was not run.
func Launch(c *Cfg, args []string, stderr io.Writer) int {
	fmt.Fprintln(stderr, "nsbox:", unsupportedOS(runtime.GOOS), "; the command was not run")
	return 1
}

// LaunchChild prints the refusal and returns 1: the command was not run.
func LaunchChild(c *Cfg, args []string, stderr io.Writer) int {
	fmt.Fprintln(stderr, "nsbox:", unsupportedOS(runtime.GOOS), "; the command was not run")
	return 1
}
