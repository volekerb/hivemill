package hm

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Defaults are the runner's own config keys (table hm_config). They are independent of the old pipeline's caps
// (max_live_agents, max_total_claude, dispatch_max_per_tick are never read).
var Defaults = map[string]string{
	"max_concurrent":   "2",                 // active runs at one time
	"projects":         "",                  // project ids a plain `hivemill run` may pick ready tasks from; empty = explicit ids only
	"model":            "claude-sonnet-5-5", // task session and judge
	"risk_model":       "claude-opus-5-5",   // when the task names a risk-tier path or is T3
	"effort":           "medium",            // --effort of every session
	"gate_max_blocks":  "3",                 // Stop-hook blocks per round, then the stop is allowed and the task is FAIL for the owner
	"gate_wait_s":      "900",               // wait for a test slot before leaving the affected tests to CI
	"judge_rounds_max": "2",                 // judge REJECT rounds before FAIL
	"ci_rounds_max":    "3",                 // red-CI rounds sent back to the session before FAIL
	"auto_revert":      "off",               // on: a runner merge that turns main red is reverted through a PR
}

// DefaultKeys returns the config keys, sorted.
func DefaultKeys() []string {
	out := make([]string, 0, len(Defaults))
	for k := range Defaults {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Env is where the runner lives: the factory home, the user's home and the tools it calls.
type Env struct {
	Home     string // factory home (FACTORY_HOME, default ~/factory)
	UserHome string
	Actor    string // FACTORY_AGENT of the caller, "" for the owner
}

// LoadEnv reads the environment.
func LoadEnv() Env {
	uh, _ := os.UserHomeDir()
	home := os.Getenv("FACTORY_HOME")
	if home == "" {
		home = filepath.Join(uh, "factory")
	}
	return Env{Home: home, UserHome: uh, Actor: os.Getenv("FACTORY_AGENT")}
}

// DBPath is the factory DB (db/factory.db, else the legacy file).
func (e Env) DBPath() string {
	p := filepath.Join(e.Home, "db", "factory.db")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return filepath.Join(e.Home, "factory.db")
}

// StateDir holds the event log and the per-run logs.
func (e Env) StateDir() string { return filepath.Join(e.Home, "state", "hivemill") }

func cfgInt(s Store, key string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s.Cfg(key)))
	if err != nil {
		n, _ = strconv.Atoi(Defaults[key])
	}
	return n
}

func csv(v string) []string {
	var out []string
	for _, x := range strings.Split(v, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}
