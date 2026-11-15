// Command macos-facts records read-only facts about this Mac as one Markdown
// report (ADR 0003 stage M0). It exits 0 even when probes fail.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"factory/go/internal/macfacts"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs_ := flag.NewFlagSet("macos-facts", flag.ContinueOnError)
	fs_.SetOutput(stderr)
	herdr := fs_.String("herdr", "", "path of the herdr binary to codesign/xattr-inspect (optional)")
	out := fs_.String("out", "", "write the report to this file (default stdout)")
	if err := fs_.Parse(args); err != nil {
		return 2
	}
	herdrPath := *herdr
	if strings.HasPrefix(herdrPath, "-") {
		fmt.Fprintln(stderr, "macos-facts: --herdr value looks like an option:", herdrPath, "- pass a path, e.g. ./-name")
		return 2
	}
	if herdrPath != "" {
		// Children run in the temp dir, so resolve against the caller's cwd now.
		abs, err := filepath.Abs(herdrPath)
		if err != nil {
			fmt.Fprintln(stderr, "macos-facts: cannot resolve --herdr:", err)
			return 2
		}
		herdrPath = abs
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(stderr, "macos-facts: cannot find the home directory:", err)
		home = ""
	}
	tmp, err := os.MkdirTemp("", "macos-facts-")
	if err != nil {
		fmt.Fprintln(stderr, "macos-facts: cannot create its temp dir:", err)
		return 1
	}
	defer os.RemoveAll(tmp)

	env := &macfacts.Env{
		Runner: macfacts.ExecRunner{Dir: tmp}, Home: home, TempDir: tmp, HerdrPath: herdrPath,
		Uid: os.Getuid(), Now: time.Now, LookPath: exec.LookPath,
		Stat: statOnly, Fstype: macfacts.StatfsType,
	}
	report := macfacts.Render(env, macfacts.Collect(context.Background(), env, macfacts.Probes()))
	if *out == "" {
		fmt.Fprint(stdout, report)
		return 0
	}
	if err := os.WriteFile(*out, []byte(report), 0o600); err != nil {
		fmt.Fprintln(stderr, "macos-facts: cannot write --out file:", err, "- pick a writable path")
		return 1
	}
	return 0
}

// statOnly reports existence, mode and size; it never opens the file.
func statOnly(path string) (bool, string) {
	fi, err := os.Lstat(path)
	if err != nil {
		return false, err.Error()
	}
	return true, fmt.Sprintf("mode=%s size=%d", fs.FileMode.String(fi.Mode()), fi.Size())
}
