// Command factory is the Go side of the factory CLI (G-041 S1-b): whoami, flags, agents and the read forms of
// config. Everything else runs in Python via execv; see package cli.
package main

import (
	"fmt"
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
	now, err := testClock(env)
	if err != nil {
		fmt.Fprintf(os.Stderr, "factory(go): %v\n", err)
		os.Exit(1)
	}
	env.Now = now
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

// testClock is the clock of this run: FACTORY_TEST_NOW (an ISO timestamp with its offset) when it is set and
// FACTORY_HOME is not a live home, else the real clock. A live home always gets the real clock, so a stray variable
// can never stamp real rows; a value without an offset is refused (Python S2h does the same); if liveness cannot be decided the real clock is used. Python's now() follows the same rule.
func testClock(env identity.Env) (func() time.Time, error) {
	raw := env.Getenv("FACTORY_TEST_NOW")
	if raw == "" {
		return time.Now, nil
	}
	if live, err := env.HomeIsLive(); err != nil || live {
		return time.Now, nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04Z07:00"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return func() time.Time { return t }, nil
		}
	}
	return nil, fmt.Errorf("FACTORY_TEST_NOW must be an ISO timestamp")
}
