// Command gowrap runs the Go toolchain (go or gofmt) under a pinned environment, an argv and path check and a
// Landlock write allow-list (T-1790). Usage: gowrap go <subcmd> [args] | gowrap gofmt [args].
//
// The logic lives in internal/gowrap. There is no env var or flag that turns the sandbox off.
package main

import (
	"os"
	"runtime"

	"factory/go/internal/gowrap"
)

func main() {
	// Landlock domains are per thread and execve carries the calling thread's domain: restrict and exec
	// from this one OS thread.
	runtime.LockOSThread()
	os.Exit(gowrap.Run(os.Args[1:], gowrap.Deps{}))
}
