// Command factory-forward forwards a factory CLI call to the HQ host over ssh (G-057 SC1). On a worker
// host it stands in for the factory CLI: `factory-forward <factory args...>`. On the HQ host ssh runs it as
// `factory-forward --serve`, which runs the real bin/factory. Protocol, config and install: docs/multi-machine.md.
// Nothing calls this binary until the owner installs it (G-057 R3). A panic exits 2.
package main

import (
	"fmt"
	"io"
	"os"

	"factory/go/internal/forward"
)

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Environ())) }

func run(args []string, stdin *os.File, stdout, stderr io.Writer, environ []string) (code int) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(stderr, "forward: internal error: %v\n", r)
			code = 2
		}
	}()
	if len(args) > 0 && args[0] == "--serve" {
		if len(args) != 1 {
			fmt.Fprintln(stderr, "forward: --serve takes no arguments (the call comes on stdin)")
			return 2
		}
		return forward.Serve(stdin, stdout, stderr, environ)
	}
	path, err := forward.ConfigPath(environ)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	c, err := forward.LoadConfig(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "forward: cannot read the working directory: %v\n", err)
		return 2
	}
	var in io.Reader
	if forward.ForwardStdin(stdin) {
		in = stdin
	}
	return forward.RunClient(c, args, environ, cwd, in, stdout, stderr)
}
