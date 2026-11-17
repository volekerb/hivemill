//go:build !linux

package sandbox

import (
	"fmt"
	"runtime"
)

// Fail-closed stand-ins for the Linux-only stage 1 (setup_linux.go): nothing here runs a command.

// Setup refuses: there is no namespace sandbox off Linux.
func Setup(c *Cfg, args []string) error { return unsupportedOS(runtime.GOOS) }

// TreeBytes refuses: it only sizes a tmpfs copy, which does not exist off Linux.
func TreeBytes(path string) (int64, error) { return 0, unsupportedOS(runtime.GOOS) }

// fdp has the Linux spelling so callers still compile; every Linux-only caller is refused before it is used.
func fdp(fd int) string { return fmt.Sprintf("/proc/self/fd/%d", fd) }
