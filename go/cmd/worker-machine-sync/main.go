// Command worker-machine-sync copies an owner-supplied allow-list of secret files to a worker machine (chmod 600,
// never printed). It is a manual owner tool: nothing in the factory calls it. See docs/machine-sync.md.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"factory/go/internal/machinesync"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("worker-machine-sync", flag.ContinueOnError)
	fs.SetOutput(stderr)
	machine := fs.String("machine", "", "worker machine name (ssh destination)")
	list := fs.String("list", "", "JSON file: array of {src,dst} absolute paths")
	dry := fs.Bool("dry-run", false, "print what would be copied; copy nothing")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *machine == "" || *list == "" || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: worker-machine-sync --machine <name> --list <file> [--dry-run]")
		return 2
	}
	if err := machinesync.ValidateMachine(*machine); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(stderr, "refused: cannot determine home directory")
		return 2
	}
	entries, err := machinesync.LoadList(*list)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	err = machinesync.Run(machinesync.Options{Machine: *machine, Entries: entries, DryRun: *dry, LocalHome: home, Stdout: stdout, Stderr: stderr})
	var ref *machinesync.RefusedError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ref):
		fmt.Fprintln(stderr, ref)
		return 2
	default:
		return 1
	}
}
