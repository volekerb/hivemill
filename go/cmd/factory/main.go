// Command factory is the Go side of the factory CLI (G-041 S1-b): whoami, flags, agents and the read forms of
// config. Everything else runs in Python via execv; see package cli.
package main

import (
	"os"
	"time"

	"factory/go/internal/cli"
	"factory/go/internal/identity"
)

func main() {
	env := identity.Env{
		Getenv:    os.Getenv,
		LookupEnv: os.LookupEnv,
		Uid:       os.Getuid(),
		Now:       time.Now,
	}
	argv0 := ""
	if len(os.Args) > 0 {
		argv0 = os.Args[0]
	}
	var args []string
	if len(os.Args) > 1 {
		args = os.Args[1:]
	}
	os.Exit(cli.Run(cli.Deps{
		Args:    args,
		Argv0:   argv0,
		Environ: os.Environ(),
		Env:     env,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
	}))
}
