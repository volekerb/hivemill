package main

// Hook forwarding for session placement (docs/hivemill-machines.md). On a worker machine the factory DB is reachable
// only over a shared filesystem, where SQLite is unsafe, so every hivemill call there (the session's own and its
// Stop/SessionStart hooks') runs on the coordinator instead: ssh with the same args, cwd, FACTORY_* env and stdin,
// and the remote exit code. A worker is configured by HIVEMILL_COORDINATOR or ~/.config/hivemill/coordinator (one
// line: the coordinator's ssh name); that directory must not be shared with the coordinator.
// `hivemill local ...` never forwards: it is what the coordinator asks of the worker itself; nor do the DB-free
// subcommands of runsHere.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"factory/go/internal/hm"
	"factory/go/internal/shlex"
)

var (
	coordRe   = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._@-]*$`)
	fwdEnvRe  = regexp.MustCompile(`^FACTORY_[A-Z0-9_]+$`)
	forwarded = "HIVEMILL_FORWARDED" // set on the coordinator side of a forward: never forward again (no loop)
)

// runsHere are the subcommands that open no DB and act on this machine: the guard hook (every tool call: an ssh hop
// each would be slow), the clone's pre-push check and the affected-test runner (the session's CPU, not the
// coordinator's). They run on the worker even when it forwards. `guard export` reads the DB, so it forwards.
var runsHere = map[string]bool{"guard": true, "check": true, "affected": true}

// coordinator is the ssh name this machine forwards to ("" = run here).
func coordinator(getenv func(string) string, userHome string) string {
	if getenv(forwarded) != "" {
		return ""
	}
	c := strings.TrimSpace(getenv("HIVEMILL_COORDINATOR"))
	if c == "" && userHome != "" {
		b, _ := os.ReadFile(filepath.Join(userHome, ".config", "hivemill", "coordinator"))
		c, _, _ = strings.Cut(strings.TrimSpace(string(b)), "\n")
		c = strings.TrimSpace(c)
	}
	if c == "off" {
		return ""
	}
	return c
}

// forwardArgv is the ssh argv that runs `hivemill args` on coord in cwd with the FACTORY_* variables of environ.
func forwardArgv(coord, cwd string, environ, args []string) ([]string, error) {
	if !coordRe.MatchString(coord) {
		return nil, fmt.Errorf("coordinator %q is not an ssh host name", coord)
	}
	var pairs []string
	for _, kv := range environ {
		if k, _, ok := strings.Cut(kv, "="); ok && fwdEnvRe.MatchString(k) {
			pairs = append(pairs, shlex.Quote(kv))
		}
	}
	sort.Strings(pairs)
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = shlex.Quote(a)
	}
	remote := `PATH="$HOME/.local/bin:$PATH"; export PATH; cd ` + shlex.Quote(cwd) + ` || exit 1; exec env ` + forwarded + `=1 ` +
		strings.Join(append(pairs, "hivemill"), " ")
	if len(q) > 0 {
		remote += " " + strings.Join(q, " ")
	}
	return []string{"ssh", "-o", "BatchMode=yes", "--", coord, remote}, nil
}

// runForward runs argv with stdin and the outputs passed through and returns its exit code (255 when it cannot run or
// dies on a signal, like ssh itself on a connection failure).
func runForward(argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee) && ee.ExitCode() >= 0:
		return ee.ExitCode()
	}
	fmt.Fprintf(stderr, "hivemill: forward to the coordinator: %v\n", err)
	return 255
}

// preMain handles `hivemill local ...` and forwarding; done=false means the call runs here (Main).
func preMain(args []string, stdin io.Reader, stdout, stderr io.Writer) (code int, done bool) {
	uh, _ := os.UserHomeDir()
	if len(args) > 0 && args[0] == "local" {
		return cmdLocal(args[1:], uh, stdout, stderr), true
	}
	coord := coordinator(os.Getenv, uh)
	if coord == "" || (len(args) > 0 && runsHere[args[0]] && !isGuardExport(args)) {
		return 0, false
	}
	cwd, err := os.Getwd()
	if err != nil {
		cwd = uh
	}
	argv, err := forwardArgv(coord, cwd, os.Environ(), args)
	if err != nil {
		fmt.Fprintln(stderr, "hivemill:", err)
		return 2, true
	}
	return runForward(argv, stdin, stdout, stderr), true
}

// cmdLocal: `local coordinator` prints where this machine forwards (exit 1 when it does not); `local trust <dir>` adds
// workspace trust for a task clone to this machine's ~/.claude.json (the runner calls it on a worker before a session).
func cmdLocal(args []string, userHome string, stdout, stderr io.Writer) int {
	switch {
	case len(args) == 1 && args[0] == "coordinator":
		c := coordinator(os.Getenv, userHome)
		if c == "" {
			fmt.Fprintln(stderr, "hivemill: this machine runs hivemill itself (no coordinator)")
			return 1
		}
		fmt.Fprintln(stdout, c)
		return 0
	case len(args) == 2 && args[0] == "trust":
		if err := hm.TrustClone(hm.LoadEnv(), filepath.Clean(args[1])); err != nil {
			fmt.Fprintln(stderr, "hivemill:", err)
			return 1
		}
		return 0
	}
	fmt.Fprintln(stderr, "usage: hivemill local coordinator | local trust <clone dir>")
	return 2
}
