package cli

import (
	"time"

	"factory/go/internal/identity"
	"factory/go/internal/tick"
)

// factory tick (cmd_tick in bin/factory_cli.py), G-041 S3b: the Go driver owns the lock, the unit order and the laps;
// every unit that is not ported runs in a Python child (package tick). Only the bare `factory tick` as the owner is
// answered here. --help, -h, --phases, any other argument and every non-owner identity run in Python, before the lock.

func init() {
	register("tick", "", handler{parse: parseTick, run: runTick, ident: tickIdent})
}

func parseTick(rest []string) (any, bool) { return nil, len(rest) == 0 }

// tickIdent reports a read-only command: an unknown identity must reach runTick, which hands it to Python (whose
// refuse_worker and require_known words stay the only ones).
func tickIdent(any) identity.Cmd { return identity.Cmd{Name: "whoami"} }

func runTick(c *ctx, _ any) (int, error) {
	if c.who != "owner" {
		return 0, errDelegate
	}
	py, err := c.deps.lookPath("python3")
	if err != nil {
		return 0, errDelegate // delegate() says what is missing
	}
	base := time.Now()
	// From here on the handler never returns errDelegate: Python would redo the whole tick. Output goes straight to
	// the real writers, in call order with the children's.
	return tick.Run(tick.Config{
		Home: c.env.Home(), DB: c.db, Stdout: c.deps.Stdout, Stderr: c.deps.Stderr,
		Environ: c.deps.Environ, Python: py, Entry: c.deps.pyEntry(),
		Clock:  func() float64 { return time.Since(base).Seconds() },
		Getpid: c.deps.pid,
		TimersOn: func() bool {
			live, err := loadConfig(c.db)
			if err != nil {
				return false // a timer must never stop the tick
			}
			return cfgOf(live, defaultMap(), "tick_phase_timers").String() == "1"
		},
	}), nil
}
