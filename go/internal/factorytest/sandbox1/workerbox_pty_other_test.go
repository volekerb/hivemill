//go:build !linux

package sandbox1

import "testing"

func wbGetsid(pid int) int   { return -1 }
func wbTcgetpgrp(fd int) int { return -1 }

func wbPty(t testing.TB, argv []string, cwd string, env []string) (string, int) {
	t.Helper()
	t.Skip("the pty test needs Linux")
	return "", 0
}
