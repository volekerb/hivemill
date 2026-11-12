// Package identity is the Go port of the factory CLI's identity layer (bin/factory_cli.py _identity, actor,
// require_known, evaluator_home_guard, derive_live_homes). It fails closed: inside an agent context a missing
// FACTORY_AGENT never means owner.
package identity

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"factory/go/internal/factorydb"
)

// Unknown is the identity of an agent pane whose FACTORY_AGENT is lost and matches no single live agent.
const Unknown = "unknown"

// ExitError ends the command with Code after printing "factory: <Msg>" to stderr (Python's die()).
type ExitError struct {
	Code int
	Msg  string
}

func (e *ExitError) Error() string { return e.Msg }

// ErrDelegate means this binary cannot answer faithfully and the command must run in Python.
var ErrDelegate = errors.New("identity: delegate to python")

// Env is everything the identity layer reads from the process; tests inject it.
type Env struct {
	Getenv func(string) string
	// LookupEnv distinguishes unset from empty (FACTORY_HOME=""); nil means Getenv != "" is "set".
	LookupEnv func(string) (string, bool)
	Uid       int
	// PwDir returns the passwd home directory of a uid; nil uses the system passwd database.
	PwDir func(uid int) (string, error)
	// Now is the clock for events rows.
	Now func() time.Time
	// Stderr receives the pane-recovery hint.
	Stderr io.Writer
}

func (e Env) pwdir(uid int) (string, error) {
	if e.PwDir != nil {
		return e.PwDir(uid)
	}
	return pwDir(uid)
}

// Home is HOME of this run: $FACTORY_HOME, else <home dir>/factory. Like Python's Path(os.environ.get(...)) an empty
// FACTORY_HOME is the current directory.
func (e Env) Home() string {
	if v, ok := lookup(e, "FACTORY_HOME"); ok {
		if v == "" {
			return "."
		}
		return v
	}
	return filepath.Join(e.userHome(), "factory")
}

// userHome is Python's Path.home(): $HOME, else the passwd entry.
func (e Env) userHome() string {
	if h := e.Getenv("HOME"); h != "" {
		return h
	}
	h, _ := e.pwdir(e.Uid)
	return h
}

// UserHome exposes Path.home().
func (e Env) UserHome() string { return e.userHome() }

func lookup(e Env, k string) (string, bool) {
	if e.LookupEnv != nil {
		return e.LookupEnv(k)
	}
	v := e.Getenv(k)
	return v, v != ""
}

// DeriveLiveHomes is derive_live_homes: the live factory home(s) from the passwd entry and nothing the caller
// controls. <pw_dir>/factory is always live; <pw_dir>/.config/factory/live_home can only add one more.
func (e Env) DeriveLiveHomes() (primary string, homes map[string]bool, err error) {
	base, err := e.pwdir(e.Uid)
	if err != nil {
		return "", nil, fmt.Errorf("passwd entry of uid %d: %w", e.Uid, err)
	}
	def := Realpath(filepath.Join(base, "factory"))
	homes = map[string]bool{def: true}
	primary = def
	cfgdir := filepath.Join(base, ".config", "factory")
	path := filepath.Join(cfgdir, "live_home")
	st, err1 := os.Lstat(path)
	pst, err2 := os.Lstat(cfgdir)
	if err1 != nil || err2 != nil {
		return primary, homes, nil
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || !pst.IsDir() || int(sys.Uid) != e.Uid || st.Mode().Perm()&0o022 != 0 {
		return primary, homes, nil
	}
	raw, rerr := os.ReadFile(path)
	if rerr != nil || !utf8.Valid(raw) {
		return primary, homes, nil
	}
	lines := splitLines(string(raw))
	if len(lines) == 1 && strings.HasPrefix(lines[0], "/") && !strings.Contains(lines[0], "\x00") {
		primary = Realpath(lines[0])
		homes[primary] = true
	}
	return primary, homes, nil
}

// HomeIsLive reports whether this run's FACTORY_HOME resolves to a live factory home.
func (e Env) HomeIsLive() (bool, error) {
	_, homes, err := e.DeriveLiveHomes()
	if err != nil {
		return false, err
	}
	return homes[Realpath(e.Home())], nil
}

// EvaluatorHomeGuard refuses (exit 3) an evaluator whose FACTORY_HOME is unset, empty or live, before any DB is opened.
func (e Env) EvaluatorHomeGuard() error {
	if e.Getenv("FACTORY_ROLE") != "evaluator" {
		return nil
	}
	live, err := e.HomeIsLive()
	if err != nil {
		return ErrDelegate
	}
	if strings.TrimSpace(e.Getenv("FACTORY_HOME")) == "" || live {
		return &ExitError{3, "the evaluator may not use the live factory home: set FACTORY_HOME to its temp home " +
			"(FACTORY_HOME=<temp dir> factory <cmd>)"}
	}
	return nil
}

// Resolve is _identity(): an agent name, "owner" or Unknown. db opens the factory database lazily (only a pane id
// needs it); a failure to open returns ErrDelegate. FACTORY_AGENT=owner on a registered agent's pane records an
// identity-spoof events row and returns an *ExitError with code 3.
func (e Env) Resolve(db func() (*factorydb.DB, error)) (string, error) {
	fa := e.Getenv("FACTORY_AGENT")
	pane := e.Getenv("HERDR_PANE_ID")
	if fa != "" && fa != "owner" {
		return fa, nil
	}
	if fa == "" && e.Getenv("CLAUDECODE") == "" && pane == "" {
		return "owner", nil
	}
	type row struct{ name, status string }
	var rows []row
	if pane != "" && dbExists(e) {
		d, err := db()
		if err != nil {
			return "", ErrDelegate
		}
		rs, err := d.Query("SELECT name,status FROM agents WHERE pane_id=? AND status IN ('live','done')", pane)
		if err != nil {
			return "", ErrDelegate
		}
		for rs.Next() {
			var r row
			if err := rs.Scan(&r.name, &r.status); err != nil {
				rs.Close()
				return "", ErrDelegate
			}
			rows = append(rows, r)
		}
		rs.Close()
		if rs.Err() != nil {
			return "", ErrDelegate
		}
		if fa == "owner" {
			if len(rows) == 0 {
				return "owner", nil
			}
			var names []string
			for _, r := range rows {
				names = append(names, r.name)
			}
			joined := strings.Join(names, ",")
			if err := e.EventAs(d.DB, "pane:"+pane, "agent", joined, "identity-spoof",
				fmt.Sprintf("pane %s claims FACTORY_AGENT=owner", pane)); err != nil {
				return "", ErrDelegate
			}
			return "", &ExitError{3, fmt.Sprintf("pane %s belongs to agent %s; it may not claim FACTORY_AGENT=owner", pane, joined)}
		}
	} else if fa == "owner" {
		return "owner", nil
	}
	var live []string
	for _, r := range rows {
		if r.status == "live" {
			live = append(live, r.name)
		}
	}
	if len(live) == 1 {
		fmt.Fprintf(e.Stderr, "factory: FACTORY_AGENT unset; recovered identity %s from pane %s. export FACTORY_AGENT=%s\n",
			live[0], pane, live[0])
		return live[0], nil
	}
	return Unknown, nil
}

func dbExists(e Env) bool {
	_, err := os.Stat(factorydb.ResolvePath(e.Home()))
	return err == nil
}

// EventAs inserts one events row (event_as).
func (e Env) EventAs(db *sql.DB, who, entity, entityID, action, detail string) error {
	_, err := db.Exec("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
		e.Now().Format("2006-01-02T15:04:05-07:00"), who, entity, entityID, action, detail)
	return err
}

// Cmd is the parsed command require_known looks at (the argparse namespace fields it reads).
type Cmd struct {
	Name   string
	Action string
	Value  *string // config: the value argument, nil for a read
	Send   bool    // digest --send
}

// RequireKnown is require_known: with an Unknown identity read-only commands run and every write is refused (exit 3).
func RequireKnown(who string, c Cmd) error {
	if who != Unknown {
		return nil
	}
	if c.Name == "flags" {
		return nil
	}
	in := func(s string, set ...string) bool {
		for _, x := range set {
			if s == x {
				return true
			}
		}
		return false
	}
	ro := in(c.Name, "whoami", "inbox", "agents", "res", "doctor", "scorecard", "serve") ||
		(c.Name == "config" && c.Value == nil) ||
		(c.Name == "project" && c.Action == "list") ||
		(c.Name == "gate" && in(c.Action, "show", "list")) ||
		(c.Name == "goal" && in(c.Action, "list", "show", "tokens")) ||
		(c.Name == "team" && c.Action == "list") ||
		(c.Name == "lesson" && in(c.Action, "list", "show")) ||
		(c.Name == "task" && in(c.Action, "list", "ready", "show")) ||
		(c.Name == "digest" && !c.Send)
	if !ro {
		return &ExitError{3, "identity unknown: this looks like an agent pane but FACTORY_AGENT is unset and the pane matches " +
			"no single live agent. export FACTORY_AGENT=<your agent name>, or FACTORY_AGENT=owner if you are the owner"}
	}
	return nil
}
