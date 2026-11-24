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
	"judge_effort":     "low",               // --effort of the judge session
	"gate_max_blocks":  "3",                 // Stop-hook blocks per round, then the stop is allowed and the task is FAIL for the owner
	"gate_wait_s":      "900",               // wait for a test slot before leaving the affected tests to CI
	"judge_rounds_max": "2",                 // judge REJECT rounds before FAIL
	"ci_rounds_max":    "3",                 // red-CI rounds sent back to the session before FAIL
	"auto_revert":      "off",               // on: a runner merge that turns main red is reverted through a PR
	// dispatch caps: active runs may finish; only new runs wait
	"max_five_hour_pct":        "92",   // plan 5-hour usage at or over this (fresh reading) holds new runs
	"max_seven_day_pct":        "95",   // plan 7-day usage at or over this holds new runs
	"mem_floor_mb":             "1024", // MemAvailable below this holds new runs
	"usage_max_age_s":          "600",  // a usage reading older than this is unknown: dispatch is held (fail closed)
	"usage_require_5h":         "on",   // on: an empty 5-hour field is unknown and holds dispatch
	"task_budget_usd":          "5",    // a run whose sessions cost more (API prices) is stopped and marked FAIL "budget"; off = none
	"project_daily_budget_usd": "off",  // per project, sessions started today; reached = no new run in that project
	"weekly_budget_usd":        "off",  // all runs, sessions started in the last 7 days; reached = no new run
	"pass_interval_s":          "60",   // `hivemill daemon`: seconds between passes
	"resume_max":               "3",    // lost sessions relaunched with claude --resume per run, then FAIL "session lost"
	"health_max_age_s":         "300",  // `hivemill health` fails when the last good pass is older
	"alert_every_s":            "3600", // at most one dead-man alert per this many seconds
	"backup_interval":          "off",  // daemon snapshot schedule as a Go duration (6h, 30m); off = only `hivemill backup`
	"backup_keep":              "7",    // snapshots kept (in backup_dir and in a file:// target)
	"backup_dir":               "",     // empty = state/hivemill/backups
	"backup_target":            "",     // file:///path (copy) | s3://bucket/path | sftp://host/path (litestream when installed)
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
