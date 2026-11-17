// Command ci-freshbox-darwin is the macOS fresh-install test (ADR 0003 stage M8a). Run it as root on a Mac:
// it creates a throwaway user, installs Hivemill as that user, checks `factory up` and the tick, then removes
// the user again on every exit path. Running it on a Mac is the owner-gated M8b task.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"syscall"

	"factory/go/internal/freshdarwin"
)

type execRunner struct{}

func (execRunner) Run(c freshdarwin.Cmd) (string, int, error) {
	cmd := exec.Command(c.Argv[0], c.Argv[1:]...)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		return buf.String(), ee.ExitCode(), nil
	}
	return buf.String(), 0, err
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, runtime.GOOS, os.Geteuid())) }

func run(args []string, stdout, stderr io.Writer, goos string, euid int) int {
	fs := flag.NewFlagSet("ci-freshbox-darwin", flag.ContinueOnError)
	fs.SetOutput(stderr)
	keep := fs.Bool("keep", false, "do not delete the throwaway user (debugging)")
	shim := fs.String("shim", "plugins/hivemill/scripts/install", "path of the release installer shim")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := freshdarwin.Check(goos, euid); err != nil {
		fmt.Fprintln(stderr, err)
		return err.(*freshdarwin.Refusal).Exit
	}
	// The shim runs as the throwaway user, whose cwd is not ours: it must be absolute (and readable by that user).
	shimPath, err := filepath.Abs(*shim)
	if err != nil {
		fmt.Fprintln(stderr, "ci-freshbox-darwin:", err)
		return 2
	}
	cfg := freshdarwin.Config{GOOS: goos, EUID: euid, Runner: execRunner{}, Log: stdout, Keep: *keep, Shim: shimPath}
	// A signal must not skip cleanup: it marks the run stopped (no further step starts) and Run still deletes
	// the user before returning.
	var stopped atomic.Bool
	cfg.Stop = stopped.Load
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		for s := range sig {
			stopped.Store(true)
			fmt.Fprintf(stderr, "ci-freshbox-darwin: got %v; finishing the current step, then cleaning up\n", s)
		}
	}()
	code, err := freshdarwin.Run(cfg)
	if err != nil {
		fmt.Fprintln(stderr, err)
	}
	return code
}
