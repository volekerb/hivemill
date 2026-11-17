package forward

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// Serve is `factory-forward --serve` on the HQ machine: write Ack, load the config, read one header from stdin,
// run factory_bin with the header's argv, the allow-listed env and the mapped cwd, and return its exit code. The
// rest of stdin is the CLI's stdin; pass the process's own *os.File so the CLI reads it directly. Every refusal
// exits 2 and runs nothing.
func Serve(stdin io.Reader, stdout, stderr io.Writer, environ []string) int {
	if _, err := io.WriteString(stdout, Ack); err != nil {
		fmt.Fprintf(stderr, "forward: write ack: %v\n", err)
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintln(stderr, err)
		return 2
	}
	path, err := ConfigPath(environ)
	if err != nil {
		return fail(err)
	}
	c, err := LoadConfig(path)
	if err != nil {
		return fail(err)
	}
	if err := c.ValidateServer(); err != nil {
		return fail(err)
	}
	h, err := ReadHeader(stdin)
	if err != nil {
		return fail(err)
	}
	home := lookup(environ, "HOME")
	dir := MapCwd(h.Cwd, c.PathMap, home)
	if dir == "" {
		return fail(refuse("cwd %s is outside path_map and HOME is not set on this machine", h.Cwd))
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return fail(refuse("cwd %s (from %s) is not a directory on this machine; check path_map", dir, h.Cwd))
	}
	cmd := exec.Command(c.FactoryBin, h.Argv...)
	cmd.Dir = dir
	cmd.Env = ChildEnv(h, environ)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err = cmd.Run()
	code := exitCode(err)
	if code < 0 {
		var pe *os.PathError
		if errors.As(err, &pe) || errors.Is(err, exec.ErrNotFound) {
			return fail(refuse("run %s: %v; check factory_bin", c.FactoryBin, err))
		}
		return fail(refuse("run %s: %v", c.FactoryBin, err))
	}
	return code
}
