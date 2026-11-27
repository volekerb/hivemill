package hm

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Defaults are the runner's own config keys (table hm_config). They are independent of the old pipeline's caps
// (max_live_agents, max_total_claude, dispatch_max_per_tick are never read).
var Defaults = map[string]string{
	"max_concurrent": "2",                 // working runs at one time (working, judging)
	"max_waiting":    "20",                // waiting runs (CI, merge, held for the owner): reached = no new run
	"projects":       "",                  // project ids a plain `hivemill run` may pick ready tasks from; empty = explicit ids only
	"model":          "claude-sonnet-5-5", // task session and judge
	"risk_model":     "claude-opus-5-5",   // when a declared-scope or changed file is a sensitive path, or the task is T3
	"effort":         "medium",            // --effort of every session
	// dynamic workflows (workflow.go): which task sessions get the owner's request to use the Workflow tool
	"task_workflows":       "large", // off | large (4+ criteria, 6+ scope paths or T3) | all
	"task_workflow_agents": "6",     // at most this many agents per workflow, as the request says
	// /goal (goal.go): a task session keeps working until its acceptance criteria pass
	"task_goal":        "1",    // 1: the first prompt of a claude-code task session starts with /goal; 0: no prefix
	"task_goal_turns":  "60",   // the goal stops after this many turns
	"gate_max_blocks":  "3",    // Stop-hook blocks per round, then the stop is allowed and the task is FAIL for the owner
	"gate_wait_s":      "900",  // wait for a test slot before leaving the affected tests to CI
	"judge_rounds_max": "2",    // judge REJECT rounds before FAIL
	"ci_rounds_max":    "3",    // red-CI rounds sent back to the session before FAIL
	"local_ci_jobs":    "2",    // ci_gate none: gate_cmd background jobs running at once per machine; the rest queue
	"ci_poll_s":        "60",   // a gated run reads its PR at most this often (and once per pass)
	"ci_timeout_s":     "2700", // checks still pending after this: one owner alert, the run stays
	"auto_revert":      "off",  // on: a runner merge that turns main red is reverted through a PR
	"t3_auto_merge":    "on",   // a T3 PR with a judge MERGE merges unless a hard limit holds it (merguard.go); off = held
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
	"run_max_working_min":      "180",  // minutes in working per round; then one resume, FAIL "timed out" 30 later; 0 or off = no limit
	"health_max_age_s":         "300",  // `hivemill health` fails when the last good pass is older
	"alert_every_s":            "3600", // at most one dead-man alert per this many seconds
	"backup_interval":          "off",  // daemon snapshot schedule as a Go duration (6h, 30m); off = only `hivemill backup`
	"backup_keep":              "7",    // snapshots kept (in backup_dir and in a file:// target)
	"backup_dir":               "",     // empty = state/hivemill/backups
	"backup_target":            "",     // file:///path (copy) | s3://bucket/path | sftp://host/path (litestream when installed)
	"standby_from":             "",     // on the standby: HOST or HOST:DIR of the primary's snapshots (docs/standby.md); empty = no check
	"standby_check_days":       "30",   // daemon `standby check` of standby_from every this many days; 0 = off
	"alert_cmd":                "",     // owner alerts: run as sh -c "$alert_cmd" hivemill-alert TITLE TEXT; empty = log, plus a notification on macOS
	// planner (planner.go): one long-lived session that keeps the ready queues full and turns failures into a next step
	"planner_queue_min":  "3",               // lint-passing ready tasks wanted per project with an active goal
	"planner_model":      "claude-opus-5-5", // the planner session's model (never Fable)
	"planner_effort":     "medium",          // its --effort
	"planner_wake_gap_s": "600",             // at most one wake per this many seconds; reasons in between are coalesced
	"planner_ack_s":      "900",             // a delivered work item still open after this many seconds is delivered again
	"idle_alert_min":     "20",              // `health --alert`: no dispatch for this many minutes while an active goal exists
	"idle_alert_h":       "2",               // used in hours instead when idle_alert_min is 0
	"ask_alert_h":        "1",               // `health --alert`: open owner questions older than this go out in one digest
	// session placement (placement.go, docs/hivemill-machines.md): task sessions on other hc machines
	"session_machines":     "",     // hc machines for task sessions in order, e.g. worker1,local; empty = local core only
	"machine_mem_floor_mb": "2500", // a listed machine needs this much MemAvailable to take a new task session
	// `hivemill obs` bind host (obs_http.go): an IP address or localhost; beyond loopback that network can read the page
	"obs_listen": "127.0.0.1",
	// disk cleanup (clean.go): `hivemill clean` and the daemon's step
	"clean_every_min":   "60", // the daemon cleans this often; 0 or off = only `hivemill clean`
	"worktree_keep_h":   "24", // a done or cancelled task keeps its worktree this long
	"disk_trim_pct":     "80", // above this disk use of the home, Go build caches idle for 7 days are emptied
	"transcript_keep_d": "30", // transcripts of runs ended longer ago are gzipped
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

// CheckModel refuses a session model that is not a claude-* id, and every Fable model. `hivemill config` applies it to
// model and risk_model, so a refused model never reaches the config (dispatch checks again in ModelFor).
func CheckModel(m string) error {
	if !strings.HasPrefix(m, "claude-") || strings.Contains(strings.ToLower(m), "fable") {
		return fmt.Errorf("model %q refused: use a claude-sonnet-* or claude-opus-* id (never Fable)", m)
	}
	return nil
}
