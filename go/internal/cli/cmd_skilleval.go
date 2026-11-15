package cli

import (
	"database/sql"
	"errors"
	"path/filepath"
	"time"

	sbx "factory/go/internal/sandbox"
	"factory/go/internal/scout"
	"factory/go/internal/skilleval"
)

// skilleval vendor | queue | control | void | list | results (Python cmd_skilleval). try, collect and cancel stop and
// start agents (force_stop, spawn), which the Go CLI does not have before P4-L: their argv shapes are not parsed here,
// so Python answers them. Registered for P4-L: unreachable until "skilleval" is added to cli_go_cmds.

func init() {
	for _, a := range []string{"vendor", "queue", "control", "void", "list", "results"} {
		a := a
		register("skilleval", a, handler{parse: func(rest []string) (any, bool) { return parseSkilleval(a, rest) }, run: runSkilleval})
	}
}

type skillevalCmd struct {
	action, id, candidate, reason, why, tasks string
	rescreen                                  bool
}

func parseSkilleval(action string, rest []string) (any, bool) {
	c := skillevalCmd{action: action}
	var pos []string
	seen := map[string]bool{}
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch a {
		case "--rescreen":
			if seen[a] || action != "vendor" {
				return nil, false
			}
			seen[a], c.rescreen = true, true
			continue
		case "--candidate", "--reason", "--tasks", "--why":
			if seen[a] || i+1 >= len(rest) || rest[i+1] == "" || rest[i+1][0] == '-' {
				return nil, false
			}
			seen[a] = true
			i++
			switch a {
			case "--candidate":
				c.candidate = rest[i]
			case "--reason":
				c.reason = rest[i]
			case "--tasks":
				c.tasks = rest[i]
			case "--why":
				c.why = rest[i]
			}
			continue
		}
		if a == "" || a[0] == '-' {
			return nil, false
		}
		pos = append(pos, a)
	}
	if len(pos) > 1 {
		return nil, false
	}
	if len(pos) == 1 {
		c.id = pos[0]
	}
	if c.reason != "" && c.reason != "infra" && c.reason != "timeout" && c.reason != "over-budget" && c.reason != "cancelled" {
		return nil, false // argparse's choices error
	}
	return c, true
}

// cliAPI is skilleval.API for the commands that never stop or start an agent.
type cliAPI struct {
	c    *ctx
	home string
}

func (a cliAPI) Now() string { return eventTS(a.c) }
func (a cliAPI) Clock() time.Time {
	return a.c.env.Now()
}
func (a cliAPI) Cfg(_ *sql.DB, key string) (string, error) { return cfgText(a.c, key) }
func (a cliAPI) Event(_ *sql.DB, entity, id, action, detail string) error {
	return event(a.c, entity, id, action, strp(detail))
}
func (a cliAPI) ForceStop(*sql.DB, string, string, sql.NullString) (string, string, error) {
	return "", "", errors.New("force_stop is not available in the Go CLI before P4-L")
}
func (a cliAPI) Launch(*sql.DB, skilleval.Trial, string, string, string) error {
	return errors.New("trial launch is not available in the Go CLI before P4-L")
}
func (a cliAPI) Home() string    { return a.home }
func (a cliAPI) HomeDir() string { return a.c.env.Home() }
func (a cliAPI) Unshare() string { return "unshare" }

// factoryRepoRoot is the checkout the bin/factory path belongs to (<repo>/bin/factory); "" when it is not known.
func factoryRepoRoot(c *ctx) string {
	a0 := c.deps.Argv0
	if a0 == "" {
		return ""
	}
	if r, err := filepath.EvalSymlinks(a0); err == nil {
		a0 = r
	}
	return filepath.Dir(filepath.Dir(a0))
}

func skillevalExit(err error) (int, error) {
	var ee *skilleval.ExitError
	if errors.As(err, &ee) {
		return 0, dieErr(ee.Code, ee.Msg)
	}
	return 0, err
}

func runSkilleval(c *ctx, p any) (int, error) {
	a := p.(skillevalCmd)
	if w, del := callerIsWorker(c); del || w != "" {
		return 0, errDelegate // workers are refused by Python (with its stub exemption)
	}
	if a.action == "queue" || a.action == "control" {
		if _, err := requireRole(c, "director", "lead", "boss"); err != nil {
			return 0, err
		}
	} else if _, err := requireRole(c, "director", "lead"); err != nil {
		return 0, err
	}
	repo := factoryRepoRoot(c)
	if repo == "" {
		return 0, errDelegate
	}
	env := &skilleval.Env{
		Collector:  skilleval.Collector{TasksDir: filepath.Join(repo, "skilleval", "tasks")},
		ControlDir: filepath.Join(repo, "skilleval", "controls", "marker"),
		Actor:      c.who,
		Home:       c.env.Home(),
		Out:        c.out,
		Api:        cliAPI{c: c, home: passwdHome(c)},
		NewGh:      scout.NewGh,
	}
	if a.action == "vendor" {
		if a.id == "" {
			return 0, dieErr(2, "usage: factory skilleval vendor C-xxxx [--rescreen]")
		}
		if a.rescreen {
			return skillevalExit(env.Rescreen(c.db, a.id))
		}
		_, _, err := env.VendorCandidate(c.db, a.id)
		return skillevalExit(err)
	}
	switch a.action {
	case "queue":
		return skillevalExit(env.QueueCandidate(c.db, a.id, a.tasks))
	case "control":
		return skillevalExit(env.Control(c.db, a.tasks))
	case "void":
		return skillevalExit(env.VoidTarget(c.db, a.id, a.reason, a.why))
	default: // list, results
		return skillevalExit(env.ListOrResults(c.db, a.candidate))
	}
}

// passwdHome is the passwd home of the user (nsbox.real_home, the live eval_test_seams value), never $HOME: the eval
// token path must not be redirectable by the environment.
func passwdHome(_ *ctx) string { return sbx.RealHome() }
