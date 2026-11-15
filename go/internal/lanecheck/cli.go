package lanecheck

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

const usage = `usage:
  lane-check pair <refA> <refB> [--base origin/main]
  lane-check open [--repo owner/name] [--file <path>] [--base origin/main] [--remote origin]
  lane-check plan <ref> --against-open [--repo owner/name] [--file <path>] [--base origin/main] [--remote origin]
common: --dir <repository directory, default .>
exit: 0 no conflict, 3 conflict, 2 error (bad ref, git or gh failure, usage)
`

// onInterrupt runs fn and exits 2 on SIGINT/SIGTERM; the returned func removes the handler.
func onInterrupt(fn func()) (stop func()) {
	ch := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case <-ch:
			fn()
			os.Exit(ExitError)
		case <-done:
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}

// parse accepts flags before, between and after the positional arguments.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// Main is the lane-check command: it returns the exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return ExitError
	}
	cmd := args[0]
	if cmd == "-h" || cmd == "--help" || cmd == "help" {
		fmt.Fprint(stdout, usage)
		return ExitClean
	}
	fs := flag.NewFlagSet("lane-check "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	c := &Checker{Stderr: stderr}
	fs.StringVar(&c.Dir, "dir", ".", "repository directory")
	fs.StringVar(&c.Base, "base", "origin/main", "ref the touched files are measured against")
	var file string
	var againstOpen bool
	if cmd == "open" || cmd == "plan" {
		fs.StringVar(&c.Repo, "repo", "", "GitHub repository owner/name (default: the one gh finds for --dir)")
		fs.StringVar(&c.Remote, "remote", "origin", "git remote of that repository, the PR heads are fetched from it")
		fs.StringVar(&file, "file", "", "only pairs that both change this path")
	}
	if cmd == "plan" {
		fs.BoolVar(&againstOpen, "against-open", false, "check the ref against every open PR head")
	}
	pos, err := parse(fs, args[1:])
	if err != nil {
		return ExitError
	}
	fail := func(msg string) int {
		fmt.Fprintln(stderr, "lane-check: "+msg)
		fmt.Fprint(stderr, usage)
		return ExitError
	}
	if err := c.enterRoot(); err != nil {
		fmt.Fprintln(stderr, "lane-check:", err)
		return ExitError
	}
	var code int
	switch cmd {
	case "pair":
		if len(pos) != 2 {
			return fail("pair needs exactly two refs")
		}
		code, err = c.Pair(stdout, pos[0], pos[1])
	case "open":
		if len(pos) != 0 {
			return fail("open takes no positional argument")
		}
		code, err = c.Open(stdout, file)
	case "plan":
		if len(pos) != 1 {
			return fail("plan needs exactly one ref")
		}
		if !againstOpen {
			return fail("plan needs --against-open (the only mode)")
		}
		code, err = c.Plan(stdout, pos[0], file)
	default:
		return fail(fmt.Sprintf("unknown command %q", cmd))
	}
	if err != nil {
		fmt.Fprintln(stderr, "lane-check:", err)
		return ExitError
	}
	return code
}
