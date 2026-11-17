// Package gowrap is the logic behind cmd/gowrap (T-1790): a wrapper that runs go or gofmt with a pinned
// environment, after an argv/path check, under a Landlock write allow-list.
//
// The argv check only catches mistakes (go test runs worker-written code); the containment is Landlock, which
// the go process and everything it runs inherits. There is no switch that disables it: if the kernel has no
// Landlock the wrapper refuses (exit 126).
package gowrap

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"syscall"
)

// Exit codes.
const (
	ExitRefused   = 2   // argv or path refused
	ExitNoLandlk  = 126 // Landlock unavailable or could not be applied
	ExitNoToolchn = 127 // the go toolchain binary is missing
)

// homeOverride is set only by the tests, with -ldflags "-X factory/go/internal/gowrap.homeOverride=<dir>", so
// the e2e test can run the real binary against a fake home. A production binary is built without it and takes
// HOME from the passwd entry; there is no runtime knob.
var homeOverride string

// Deps are the process boundaries. The zero value is the real process.
type Deps struct {
	Home    string                                      // default: passwd home (os/user), not $HOME
	Cwd     string                                      // default: os.Getwd
	Environ []string                                    // default: os.Environ
	Stderr  io.Writer                                   // default: os.Stderr
	ABI     func() (int, error)                         // Landlock ABI probe; <1 = unavailable
	Apply   func(abi int, dirs, files []string) error   // restrict this thread
	Exec    func(path string, argv, env []string) error // replace the process
}

// refusal is a check failure: exit 2, "gowrap: refused: <reason>".
type refusal struct{ reason string }

func (r *refusal) Error() string { return "refused: " + r.reason }

func refuse(format string, a ...any) error { return &refusal{fmt.Sprintf(format, a...)} }

// errToolchainMissing: the pinned go or gofmt binary does not exist.
var errToolchainMissing = errors.New("go toolchain missing")

func (d *Deps) fill() error {
	if d.Home == "" {
		d.Home = homeOverride
	}
	if d.Home == "" {
		u, err := user.Current()
		if err != nil || u.HomeDir == "" {
			return fmt.Errorf("cannot read the passwd home: %v", err)
		}
		d.Home = u.HomeDir
	}
	if d.Cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("cannot read cwd: %w", err)
		}
		d.Cwd = wd
	}
	if d.Environ == nil {
		d.Environ = os.Environ()
	}
	if d.Stderr == nil {
		d.Stderr = os.Stderr
	}
	if d.ABI == nil {
		d.ABI = landlockABI
	}
	if d.Apply == nil {
		d.Apply = landlockApply
	}
	if d.Exec == nil {
		d.Exec = syscall.Exec
	}
	return nil
}

// Plan is a checked command: what to exec, with which env, and where it may write.
type Plan struct {
	Path     string   // absolute toolchain binary
	Argv     []string // argv[0] is Path
	Env      []string
	WriteDir []string // Landlock write roots (directories)
}

// Run checks args (tool, subcommand, flags...), applies Landlock and execs. It returns only on failure, with the
// exit code. The caller must have called runtime.LockOSThread (cmd/gowrap does).
func Run(args []string, d Deps) int {
	runtime.LockOSThread()
	if err := d.fill(); err != nil {
		fmt.Fprintf(stderrOr(d.Stderr), "gowrap: %v\n", err)
		return ExitNoLandlk
	}
	plan, err := Prepare(args, &d)
	var ref *refusal
	switch {
	case errors.As(err, &ref):
		fmt.Fprintf(d.Stderr, "gowrap: refused: %s\n", ref.reason)
		return ExitRefused
	case errors.Is(err, errToolchainMissing):
		fmt.Fprintln(d.Stderr, "gowrap: go toolchain missing")
		return ExitNoToolchn
	case err != nil:
		fmt.Fprintf(d.Stderr, "gowrap: %v\n", err)
		return ExitNoLandlk
	}
	abi, aerr := d.ABI()
	if aerr != nil || abi < 1 {
		fmt.Fprintln(d.Stderr, "gowrap: landlock unavailable; refusing")
		return ExitNoLandlk
	}
	if err := d.Apply(abi, plan.WriteDir, []string{"/dev/null"}); err != nil {
		fmt.Fprintf(d.Stderr, "gowrap: landlock: %v; refusing\n", err)
		return ExitNoLandlk
	}
	err = d.Exec(plan.Path, plan.Argv, plan.Env)
	fmt.Fprintf(d.Stderr, "gowrap: exec %s: %v\n", plan.Path, err)
	return ExitNoLandlk
}

func stderrOr(w io.Writer) io.Writer {
	if w == nil {
		return os.Stderr
	}
	return w
}

var goSubcommands = map[string]bool{
	"build": true, "test": true, "vet": true, "run": true, "list": true, "fmt": true, "env": true, "version": true,
}

// Prepare runs every check and builds the plan. It creates the worker cache dirs. Deps must be filled.
func Prepare(args []string, d *Deps) (*Plan, error) {
	if len(args) == 0 {
		return nil, refuse("usage: gowrap go <subcmd> [args] | gowrap gofmt [args]")
	}
	tool, rest := args[0], args[1:]
	sub := ""
	switch tool {
	case "go":
		if len(rest) == 0 || rest[0] == "" {
			return nil, refuse("empty go subcommand")
		}
		sub = rest[0]
		if !goSubcommands[sub] {
			return nil, refuse("go subcommand %q is not allowed (allowed: build test vet run list fmt env version)", sub)
		}
	case "gofmt":
	default:
		return nil, refuse("tool %q is not go or gofmt", tool)
	}
	if err := scanStatic(tool, sub, rest); err != nil {
		return nil, err
	}

	// Worker cache dirs, then roots.
	gocache := filepath.Join(d.Home, ".cache", "go", "worker-build")
	gotmp := filepath.Join(d.Home, ".cache", "go", "tmp")
	for _, p := range []string{gocache, gotmp} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			return nil, fmt.Errorf("cannot create %s: %w (create it, then retry)", p, err)
		}
	}
	roots, err := computeRoots(d.Home, d.Cwd, gocache, gotmp)
	if err != nil {
		return nil, err
	}
	if tool == "go" && sub != "version" && sub != "env" || tool == "gofmt" {
		if !roots.cwdOK(roots.Cwd) {
			return nil, refuse("cwd %s is not inside the task worktree or /tmp", roots.Cwd)
		}
	}
	if err := scanPaths(tool, sub, rest, roots); err != nil {
		return nil, err
	}

	bin := filepath.Join(d.Home, ".local", "go", "bin", tool)
	if st, err := os.Stat(bin); err != nil || st.IsDir() {
		return nil, errToolchainMissing
	}
	env, ignoredGoflags := buildEnv(d.Environ, d.Home)
	if ignoredGoflags {
		fmt.Fprintln(d.Stderr, "gowrap: ignoring caller GOFLAGS")
	}
	return &Plan{Path: bin, Argv: append([]string{bin}, rest...), Env: env, WriteDir: roots.writable()}, nil
}
