package spawn

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"factory/go/internal/gate"
)

// Event is one DB event the caller records (event(con, entity, id, action, detail)).
type Event struct{ Entity, ID, Action, Detail string }

// KV is one ordered key/value pair: Python dicts keep insertion order and the order is observable (spawn_args JSON,
// the child env).
type KV struct{ Key, Value string }

// AgentRow is the part of an agents row Plan reads.
type AgentRow struct {
	Name, Role, Status, PaneID, Org string
	SpawnArgs                       []byte // the spawn_args JSON text ("" = none)
}

// TaskFacts is the tasks row of Request.Task. Exists=false is "no such task".
type TaskFacts struct {
	Exists           bool
	Goal, Team, Role string
	Spec, Acceptance string
	SandboxRequired  bool
	JudgeHoldReason  string
	PRURL            string
	WorktreeExists   bool // tasks.worktree is set and the directory exists
}

// EvalFacts are the filesystem facts check_evaluator_spawn reads, resolved by the caller.
type EvalFacts struct {
	Root       string // resolved <skilleval_root>; RootErr when resolve_root failed (its message)
	RootErr    string
	Realpath   func(string) string
	ShimOK     func(path string) bool
	PromptOnly func(dir string) error // assert_prompt_only_tree
}

// Request is the argument list of spawn() plus Caller, the cmd_spawn caller ("" = an internal caller: tick, up, msg).
type Request struct {
	Role, Goal, Team, Task, Name, Cwd, Prompt, Model, Resume, Pane, Org, HostBackend string
	FromQueue, Relaunch, Fresh, SandboxOptin, StrictName                             bool
	Hold                                                                             string
	EnvExtra                                                                         []KV
	PluginDirs                                                                       []string
	AddHomeDir                                                                       bool // spawn()'s add_home_dir; the zero value is "false": set it true for every non-evaluator
	Caller                                                                           string
	CallerName                                                                       string // the acting agent's name, for the require_role text
	CallerWorker                                                                     string // caller_is_worker(): the worker role of the caller, "" = not a worker
	SessionID                                                                        string // the uuid4 of a fresh spawn, injected so the plan is deterministic
}

// Facts is everything Plan needs from outside, read by the caller from the DB, config and filesystem.
type Facts struct {
	Config    map[string]string // effective config values (the row, else the built-in default)
	Agent     func(name string) *AgentRow
	Task      TaskFacts // the row of Request.Task
	GoalOrg   string    // goals.org of Request.Goal ("" none/unknown)
	TaskOrg   string    // org of Request.Task's goal
	CallerOrg string    // agent_org of the acting agent
	// WorkerStubExempt is worker_stub_exempt(): a tests/stubs pane tool AND a non-live FACTORY_HOME; it lifts refuse_worker.
	WorkerStubExempt bool
	EnabledOrgs      int
	// LiveTwin returns the name of a live or starting boss/director of org ("" none).
	LiveTwin func(role, org string) string

	GoalTokenLimit, GoalTokenUsed int64
	GoalTokenTally                bool // a goal_token_tally row exists

	Staleness string // staleness_precheck result for Request.Task ("" = proceed)
	// OldPaneNotClosed is the pane id when release_stale_pane could not confirm the old pane of Request.Name closed (the caller asks herdr), else "".
	OldPaneNotClosed string
	JudgeHold        string // judge_hold(con, task) ("" = none)

	Gate       gate.Snapshot
	GateConfig gate.Config

	Home, RunDir, RolesDir string
	ResearchDir            string // research_dir(task), resolved by the caller
	RoleSettingsTemplate   string // roles/settings/<role>.json.in text (read_text)
	Tokens                 []Token
	CommonText, RoleText   string // roles/_common.md and roles/<role>.md (read_text)
	RolePluginDirExists    bool
	GoalPluginDir          string // GOAL_PLUGINS/<goal> when it is an existing directory inside GOAL_PLUGINS, else ""
	Eval                   EvalFacts
}

// QueueRow is the agents row spawn() inserts when the gate says wait.
type QueueRow struct {
	Name, Role, Goal, Team, Task, Reason string
	ArgsJSON                             string
	Org                                  string
	Message                              string // the "QUEUED ..." line printed on stdout
}

// Start is what _spawn_start would launch.
type Start struct {
	Model, Runtime, Cwd string
	NeedWorktree        bool // coder/qa/reviewer without an explicit cwd: the caller prepares the task worktree
	Sandboxed, GhCred   bool // the caller adds the sandbox PATH / gh credential env (side effects)
	Env                 []KV
	Effort              []string
	SettingsPath        string
	SettingsText        string
	EvaluatorSettings   bool // evaluator: SettingsText is empty; the caller builds evaluator_settings() (guard copies, hook rewrite), not ported here
	PromptPath          string
	PromptText          string
	Kickoff             string
	ClaudeArgs          []string // runtime claude only
}

// SpawnPlan is the result of Plan: exactly one of Refusal, Queue, Start, Bounce, Requeue is set.
type SpawnPlan struct {
	Name, Org, Role  string
	Goal, Team, Task string
	Resume           string
	Fresh            bool
	Events           []Event
	Refusal          *Refusal
	Queue            *QueueRow
	Start            *Start
	Stats            gate.Stats
	ArgsJSON         string
	// Bounce: a claimed queued row whose head went stale; the caller cancels it (event spawn-bounced).
	// Requeue: a claimed judge row that is held; the caller sets it back to queued. Both leave Start nil.
	Bounce, Requeue string
}

// Refusal reasons the tick keys on (Python's exception attributes).
const (
	ReasonGoalLimit = "goal-token-limit"
	ReasonSettings  = "settings-render"
	ReasonEvaluator = "evaluator-invalid"
)

var goalIDRE = regexp.MustCompile(`^G-\p{Nd}+$`)

// Plan is spawn() and the refusals of cmd_spawn() without any side effect. The caller prepares: Request.Prompt is the
// prompt after gated_reviewer_prompt, coder_judge_reject_kickoff and coder_ci_handoff_kickoff; a lead caller has its own
// goal and team filled into Request.Goal and Request.Team (cmd_spawn does that before spawn()); the sandbox PATH and
// the gh credential env are applied over Start.Env (they replace, not setdefault); a claimed queued row passes
// Facts.Staleness and Facts.JudgeHold for the recheck.
// Known gaps for P1h: evaluator_settings (see Start.EvaluatorSettings) and release_stale_pane (Facts.OldPaneNotClosed).
func Plan(req Request, f Facts) SpawnPlan {
	p := SpawnPlan{Role: req.Role}
	fail := func(code int, format string, a ...any) SpawnPlan {
		p.Refusal = refuse(code, format, a...)
		return p
	}
	// refuse_worker("spawn"): cmd_spawn's first line and spawn()'s belt, so it holds for internal callers too.
	if req.CallerWorker != "" && !f.WorkerStubExempt {
		return fail(3, "role '%s' (%s) may not do this: workers may not run `factory spawn`; ask your lead", req.CallerWorker, req.CallerName)
	}
	if req.Caller != "" {
		if r := checkCommand(&req, f, &p); r != nil {
			p.Refusal = r
			return p
		}
	}
	role := req.Role
	if !inList(Roles, role) {
		return fail(1, "role must be one of %s", pyList(Roles))
	}
	if req.SandboxOptin && !inList(Sandboxable, role) {
		s := append([]string(nil), Sandboxable...)
		sort.Strings(s)
		return fail(2, "--sandbox: %s cannot be sandboxed; allowed: %s", role, strings.Join(s, ", "))
	}
	if role == "researcher" && req.Task == "" {
		return fail(2, "researcher needs a task: factory spawn researcher --task T-NNNN (its research dir is per task)")
	}
	org := req.Org
	if role == "retro" {
		org = "cursor" // the daily retro job is always cursor runtime
	}
	goal, team, task := req.Goal, req.Team, req.Task
	if task != "" && !(goal != "" && team != "") && f.Task.Exists {
		if goal == "" {
			goal = f.Task.Goal
		}
		if team == "" {
			team = f.Task.Team
		}
	}
	bareHQ := (role == "boss" || role == "director") && task == "" && team == "" && goal == ""
	name := req.Name
	if name == "" {
		if bareHQ {
			name = role
		} else {
			base := firstNonEmpty(task, team, goal, role)
			name = role + "-" + base
		}
	}
	name = Slug(name)
	prior := f.agent(name)
	gorg := f.GoalOrg // goal_org of the resolved goal: the task's goal when none was named
	if req.Goal == "" {
		gorg = f.TaskOrg
	}
	org = spawnOrg(org, prior, name, goal, task, gorg, f)
	resume, fresh := req.Resume, req.Fresh
	if resume != "" && prior != nil {
		priorRT := priorSpawnArg(prior, "runtime")
		if priorRT == "" {
			priorRT = "claude"
		}
		nowRT, err := OrgRuntimeFor(role, org, f.Config["agent_runtimes"], f.Config["cursor_pilot_roles"])
		if err != nil {
			return fail(2, "%s", err.Error())
		}
		if priorRT != nowRT {
			p.Events = append(p.Events, Event{"agent", name, "resume-dropped", fmt.Sprintf("runtime %s -> %s", priorRT, nowRT)})
			resume, fresh = "", true
		}
	}
	if role != "evaluator" {
		asked := name
		name = oname(org, name, f.Config)
		if req.StrictName && name != asked {
			return fail(2, "--name %s is refused: agents of the %s org must be named %s (org naming rule: every name "+
				"starts with the org prefix). Use --name %s, or omit --name for the default.", asked, org, name, name)
		}
	}
	p.Name, p.Org, p.Goal, p.Team, p.Task, p.Resume, p.Fresh = name, org, goal, team, task, resume, fresh
	if r := checkEvaluatorSpawn(role, req, f); r != nil {
		if role == "evaluator" {
			r.Reason = ReasonEvaluator // EvaluatorInvalid: the tick cancels the row on this one only
		}
		p.Refusal = r
		return p
	}
	if !req.Relaunch {
		if why := goalTokenRefusal(role, goal, gorg, f); why != "" {
			if !req.FromQueue && task != "" {
				p.Events = append(p.Events, Event{"task", task, "goal-token-limit-refused", role + ": " + why})
			}
			p.Refusal = &Refusal{Msg: why, Code: 3, Reason: ReasonGoalLimit}
			return p
		}
	}
	existing := f.agent(name)
	if (role == "boss" || role == "director") && !req.Relaunch && !req.FromQueue && f.LiveTwin != nil {
		if twin := f.LiveTwin(role, org); twin != "" {
			return fail(3, "%s is already live as the %s %s; not starting %s. To restart it use `factory relaunch %s` "+
				"(add --fresh for a new session).", twin, org, role, name, twin)
		}
	}
	if existing != nil && existing.Status == "live" && !req.Relaunch {
		return fail(1, "%s is already live in %s", name, existing.PaneID)
	}
	if existing != nil && f.OldPaneNotClosed != "" {
		return fail(1, "%s: its old pane %s could not be confirmed closed; not reusing the name. The row keeps the pane id (the exited-pane reaper retries it); retry the spawn later.", name, f.OldPaneNotClosed)
	}
	sandboxOptin := req.SandboxOptin
	if inList(Sandboxable, role) && task != "" && f.Task.SandboxRequired && f.Task.Role == role {
		sandboxOptin = true // a sandbox_required task never starts unsandboxed
	}
	args := &orderedArgs{}
	args.set("role", role)
	args.set("goal", nilIfEmpty(goal))
	args.set("team", nilIfEmpty(team))
	args.set("task", nilIfEmpty(task))
	args.set("name", name)
	args.set("cwd", nilIfEmpty(req.Cwd))
	args.set("prompt", nilIfEmpty(req.Prompt))
	args.set("model", nilIfEmpty(req.Model))
	args.set("resume", nilIfEmpty(resume))
	args.set("fresh", fresh)
	args.set("org", org)
	prev := map[string]json.RawMessage{}
	if req.FromQueue && existing != nil {
		prev = parseArgsObject(existing.SpawnArgs)
	}
	switch {
	case req.HostBackend != "":
		args.set("host_backend", req.HostBackend)
		if !req.FromQueue || string(prev["host_backend_pinned"]) == "true" {
			args.set("host_backend_pinned", true)
		}
	case rawIn(prev["host_backend"], "hc", "herdr"):
		args.setRaw("host_backend", prev["host_backend"])
		if string(prev["host_backend_pinned"]) == "true" {
			args.set("host_backend_pinned", true)
		}
	}
	if sandboxOptin {
		args.set("sandbox_optin", true)
	}
	if role == "evaluator" {
		args.set("env_extra", req.EnvExtra)
		args.set("plugin_dirs", append([]string{}, req.PluginDirs...))
		args.set("add_home_dir", req.AddHomeDir)
	}
	if existing != nil && existing.Status == "starting" && !req.FromQueue {
		return fail(2, "%s is already starting (claimed by the tick); wait for it to go live", name)
	}
	rt, err := OrgRuntimeFor(role, org, f.Config["agent_runtimes"], f.Config["cursor_pilot_roles"])
	if err != nil {
		return fail(2, "%s", err.Error())
	}
	if role == "researcher" && rt == "cursor" {
		return fail(3, "the researcher role is not supported on the Cursor runtime (docs/research-team.md); use the claude org")
	}
	liveExisting := existing != nil && existing.Status == "live"
	d := gate.Decide(f.Gate, f.GateConfig, gate.Options{
		LoadExempt: (req.Relaunch && (role == "boss" || role == "director")) || role == "judge" || role == "lead",
		OwnLead:    req.Relaunch && liveExisting,
		Role:       role, Runtime: rt,
	})
	p.Stats = d.Stats
	ok, why := d.OK, d.Reason
	if ok && req.Hold != "" {
		ok, why = false, req.Hold
	}
	if ok && role == "judge" && !req.FromQueue && !req.Relaunch && resume == "" {
		if f.Task.Exists && pyStrip(f.Task.JudgeHoldReason) != "" && task != "" {
			ok, why = false, pyStrip(f.Task.JudgeHoldReason)
		} else if f.JudgeHold != "" {
			ok, why = false, f.JudgeHold
		}
	}
	if !ok {
		if existing != nil && (existing.Status == "queued" || existing.Status == "starting") {
			old := parseArgsObject(existing.SpawnArgs)
			for _, k := range []string{"spawn_failures", "spawn_failed_at", "host_backend", "host_backend_pinned"} {
				if v, found := old[k]; found {
					args.setRaw(k, v)
				}
			}
		}
		aj, err := args.json()
		if err != nil {
			return fail(1, "spawn_args: %v", err)
		}
		p.ArgsJSON = aj
		p.Events = append(p.Events, Event{"agent", name, "queued", why})
		p.Queue = &QueueRow{Name: name, Role: role, Goal: goal, Team: team, Task: task, Reason: why, ArgsJSON: aj, Org: org,
			Message: fmt.Sprintf("QUEUED %s: %s. The tick starts it when capacity frees up.", name, why)}
		return p
	}
	aj, err := args.json()
	if err != nil {
		return fail(1, "spawn_args: %v", err)
	}
	if req.FromQueue && existing != nil && existing.Status == "queued" && task != "" && resume == "" && !req.Relaunch {
		// the claimed-row recheck of spawn(): main moved while the row waited (Facts.Staleness), or a judge is held
		// (Facts.JudgeHold computed with cap=False, or the task flag)
		if f.Staleness != "" {
			p.Bounce = f.Staleness
			p.Events = append(p.Events, Event{"task", task, "spawn-bounced", role + ": " + f.Staleness})
			return p
		}
		if role == "judge" {
			held := f.JudgeHold
			if held == "" {
				held = pyStrip(f.Task.JudgeHoldReason)
			}
			if held != "" {
				p.Requeue = held
				p.Events = append(p.Events, Event{"agent", name, "queued", held})
				return p
			}
		}
	}
	p.ArgsJSON = aj
	st, r := planStart(req, f, p, rt, sandboxOptin, resume, fresh)
	if r != nil {
		p.Refusal = r
		return p
	}
	p.Start = st
	return p
}

func (f Facts) agent(name string) *AgentRow {
	if f.Agent == nil {
		return nil
	}
	return f.Agent(name)
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// oname is oname(): <org>-<base>; the default org's prefix is behind config org_prefix_default; an already prefixed
// name is returned unchanged.
func oname(org, base string, cfg map[string]string) string {
	if !ValidOrg(org) {
		org = DefaultOrg
	}
	if PrefixOf(base) != "" {
		return base
	}
	v := pyStrip(cfg["org_prefix_default"])
	if v == "" {
		v = "0"
	}
	if org == DefaultOrg && v != "1" {
		return base
	}
	return OrgName(org, base)
}

// spawnOrg is spawn_org()/org_for(): the explicit org, else the prior row's, else the name prefix, else (when the
// spawn names no goal or task) the caller's org; then the goal's, the task's goal's, the default org.
func spawnOrg(org string, prior *AgentRow, name, goal, task, gorg string, f Facts) string {
	cand := org
	if cand == "" {
		cand = priorSpawnArg(prior, "org")
	}
	if cand == "" {
		cand = PrefixOf(name)
	}
	if cand == "" && goal == "" && task == "" {
		cand = f.CallerOrg
	}
	if ValidOrg(cand) {
		return cand
	}
	if gorg != "" && goal != "" {
		return gorg
	}
	if f.TaskOrg != "" && task != "" {
		return f.TaskOrg
	}
	return DefaultOrg
}

// priorSpawnArg is _prior_spawn_arg: one string key of the row's spawn_args ("" for none, unreadable or non-string).
func priorSpawnArg(row *AgentRow, key string) string {
	if row == nil {
		return ""
	}
	var s string
	if raw, ok := parseArgsObject(row.SpawnArgs)[key]; ok && json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}

// parseArgsObject reads a spawn_args JSON object, keeping raw values; anything else (empty, invalid, not an object)
// is an empty map, like Python's `except ValueError: {}` plus the isinstance guard.
func parseArgsObject(b []byte) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(b)) == 0 {
		return out
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return out
	}
	return m
}

func rawIn(raw json.RawMessage, vals ...string) bool {
	var s string
	if raw == nil || json.Unmarshal(raw, &s) != nil {
		return false
	}
	return inList(vals, s)
}

// goalTokenRefusal is goal_token_refusal().
func goalTokenRefusal(role, goal, gorg string, f Facts) string {
	if !(role == "coder" || role == "pm") || goal == "" {
		return ""
	}
	if gorg != "" && gorg != DefaultOrg {
		return ""
	}
	if f.GoalTokenLimit <= 0 || !f.GoalTokenTally || f.GoalTokenUsed < f.GoalTokenLimit {
		return ""
	}
	return fmt.Sprintf("%s is at its token limit: tally %d tokens, limit %d. A %s is new work and is not started; "+
		"judge, reviewer, qa, lead and cleaner still are. To go on, the director or owner raises it: "+
		"factory goal edit %s --token-limit N", goal, f.GoalTokenUsed, f.GoalTokenLimit, role, goal)
}

// checkCommand is the refusal part of cmd_spawn() that precedes spawn().
func checkCommand(req *Request, f Facts, p *SpawnPlan) *Refusal {
	role, caller := req.Role, req.Caller
	if role == "evaluator" {
		return refuse(3, "the evaluator is spawned only by the skilleval harness; run `factory skilleval try` instead")
	}
	// require_role(con, "director", "lead", "boss"): the owner or one of these; anything else is refused.
	if caller != "owner" && caller != "director" && caller != "lead" && caller != "boss" {
		return refuse(3, "role '%s' (%s) may not do this; allowed: director, lead, boss", caller, req.CallerName)
	}
	if role == "scout" && caller != "director" && caller != "owner" {
		return refuse(3, "only the director (or the owner) spawns a scout; the tick does it on schedule")
	}
	if role == "retro" && caller != "director" && caller != "owner" {
		return refuse(3, "only the director (or the owner) spawns a retro; the tick does it on schedule")
	}
	if caller == "boss" && role != "director" {
		return refuse(3, "the boss only (re)starts the director; ask the director for teams")
	}
	if caller == "lead" && (role == "boss" || role == "director" || role == "lead") {
		return refuse(3, "leads spawn workers only (pm, coder, qa, reviewer, judge, cleaner)")
	}
	if task := req.Task; task != "" {
		if !f.Task.Exists {
			return refuse(2, "no task %s", task)
		}
		if pyStrip(f.Task.Spec) == "" {
			return refuse(2, "%s has no spec in the DB; write it with factory task update %s --spec - before spawning "+
				"(scratch files are lost on reboot)", task, task)
		}
		if (role == "coder" || role == "qa") && pyStrip(f.Task.Acceptance) == "" {
			return refuse(2, "%s has no acceptance in the DB; write it with factory task update %s --acceptance - "+
				"before spawning (scratch files are lost on reboot)", task, task)
		}
		if role == "qa" && req.Cwd == "" && pyStrip(f.Task.PRURL) == "" && !f.Task.WorktreeExists {
			return refuse(2, "%s has no PR and no worktree, so a qa agent would start in the shared checkout: create a worktree "+
				"for it, or pass one: factory spawn qa --task %s --cwd <worktree>", task, task)
		}
		if f.Staleness != "" {
			p.Events = append(p.Events, Event{"task", task, "spawn-bounced", role + ": " + f.Staleness})
			return refuse(2, "%s", f.Staleness)
		}
		if inList(Sandboxable, role) && !req.SandboxOptin && f.Task.SandboxRequired && f.Task.Role == role {
			p.Events = append(p.Events, Event{"task", task, "sandbox-required-refused", role + " " + firstNonEmpty(req.Name, role)})
			return refuse(3, "%s is sandbox_required: rerun with --sandbox (factory spawn %s ... --task %s --sandbox)", task, role, task)
		}
	}
	if req.Org != "" {
		if _, err := CheckOrg(req.Org); err != nil {
			return refuse(2, "%s", err.Error())
		}
		if req.Goal != "" && f.GoalOrg != "" && f.GoalOrg != req.Org {
			return refuse(2, "%s belongs to the %s org; move it first: factory goal org %s %s", req.Goal, f.GoalOrg, req.Goal, req.Org)
		}
	}
	return nil
}

var evalEnvKeys = []string{"FACTORY_HOME", "HOME", "CLAUDE_CONFIG_DIR", "PATH",
	"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "DISABLE_AUTOUPDATER"}

func kvGet(l []KV, k string) (string, bool) {
	for _, kv := range l {
		if kv.Key == k {
			return kv.Value, true
		}
	}
	return "", false
}

// checkEvaluatorSpawn is check_evaluator_spawn(): fail closed. Non-evaluators may not pass the sandbox parameters; an
// evaluator needs a complete trial sandbox.
func checkEvaluatorSpawn(role string, req Request, f Facts) *Refusal {
	if role != "evaluator" {
		if len(req.EnvExtra) > 0 || len(req.PluginDirs) > 0 || !req.AddHomeDir {
			return refuse(3, "env_extra, plugin_dirs and add_home_dir=False are for the evaluator only, not %s", role)
		}
		return nil
	}
	if len(req.EnvExtra) == 0 || req.AddHomeDir {
		return refuse(3, "the evaluator is spawned only into a trial sandbox: env_extra and add_home_dir=False are required "+
			"(use `factory skilleval try`)")
	}
	// Python's env_extra is a dict and cannot repeat a key; start applies the last of a repeated key while kvGet reads
	// the first, so a repeat would let an unchecked value through: refuse it (fail closed).
	seen := map[string]bool{}
	var dup []string
	for _, kv := range req.EnvExtra {
		if seen[kv.Key] && !inList(dup, kv.Key) {
			dup = append(dup, kv.Key)
		}
		seen[kv.Key] = true
	}
	if len(dup) > 0 {
		sort.Strings(dup)
		return refuse(3, "evaluator env_extra repeats a key (a dict cannot): %s; refused", strings.Join(dup, ", "))
	}
	var bad []string
	for _, kv := range req.EnvExtra {
		if !inList(evalEnvKeys, kv.Key) {
			bad = append(bad, kv.Key)
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return refuse(3, "evaluator env_extra may only set %s; refused: %s", strings.Join(evalEnvKeys, ", "), strings.Join(bad, ", "))
	}
	e := f.Eval
	if e.RootErr != "" {
		return refuse(3, "%s", e.RootErr)
	}
	fh, _ := kvGet(req.EnvExtra, "FACTORY_HOME")
	if fh == "" {
		return refuse(3, "evaluator env_extra has no FACTORY_HOME")
	}
	trial := filepath.Dir(fh)
	rp := e.Realpath
	if rp == nil {
		return refuse(3, "evaluator spawn: the caller gave no path resolver; not starting it")
	}
	if filepath.Base(fh) != "fh" || filepath.Dir(rp(trial)) != filepath.Join(e.Root, "trials") || rp(trial) != trial {
		return refuse(3, "evaluator FACTORY_HOME %s is not <skilleval_root>/trials/<id>/fh", fh)
	}
	for _, kv := range []KV{{"HOME", filepath.Join(trial, "home")}, {"CLAUDE_CONFIG_DIR", filepath.Join(trial, "cfg")}} {
		if v, _ := kvGet(req.EnvExtra, kv.Key); v != kv.Value {
			return refuse(3, "evaluator %s must be %s", kv.Key, kv.Value)
		}
	}
	path, _ := kvGet(req.EnvExtra, "PATH")
	first, _, _ := strings.Cut(path, ":")
	if first != filepath.Join(trial, "bin") || e.ShimOK == nil || !e.ShimOK(filepath.Join(trial, "bin", "claude")) {
		return refuse(3, "evaluator PATH must start with %s holding the claude shim (executable, owned by you)", filepath.Join(trial, "bin"))
	}
	if req.Cwd == "" || rp(req.Cwd) != filepath.Join(trial, "work") {
		return refuse(3, "evaluator cwd must be %s", filepath.Join(trial, "work"))
	}
	vendor := filepath.Join(e.Root, "vendor")
	for _, d := range req.PluginDirs {
		r := rp(d)
		if r != vendor && !strings.HasPrefix(r, vendor+"/") {
			return refuse(3, "plugin dir %s is outside %s", d, vendor)
		}
		if e.PromptOnly == nil {
			return refuse(3, "evaluator spawn: the caller gave no prompt-only check for plugin dir %s; not starting it", d)
		}
		if err := e.PromptOnly(r); err != nil {
			return refuse(3, "plugin dir %s is not prompt-only: %v", d, err)
		}
	}
	return nil
}

// orderedArgs is the spawn_args dict: insertion-ordered, serialised like json.dumps (", " and ": ", ensure_ascii).
type orderedArgs struct {
	keys []string
	vals map[string]any
}

func (a *orderedArgs) set(k string, v any) {
	if a.vals == nil {
		a.vals = map[string]any{}
	}
	if _, ok := a.vals[k]; !ok {
		a.keys = append(a.keys, k)
	}
	a.vals[k] = v
}

func (a *orderedArgs) setRaw(k string, raw json.RawMessage) { a.set(k, raw) }

func (a *orderedArgs) json() (string, error) {
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range a.keys {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(`"` + jsonEscape(k) + `": `)
		if err := writeVal(&b, a.vals[k]); err != nil {
			return "", fmt.Errorf("spawn_args key %s: %w", k, err)
		}
	}
	b.WriteByte('}')
	return b.String(), nil
}

func writeVal(w io.Writer, v any) error {
	var s string
	switch x := v.(type) {
	case nil:
		s = "null"
	case string:
		s = `"` + jsonEscape(x) + `"`
	case bool:
		s = map[bool]string{true: "true", false: "false"}[x]
	case []string:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = `"` + jsonEscape(e) + `"`
		}
		s = "[" + strings.Join(parts, ", ") + "]"
	case []KV:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = `"` + jsonEscape(e.Key) + `": "` + jsonEscape(e.Value) + `"`
		}
		s = "{" + strings.Join(parts, ", ") + "}"
	case json.RawMessage:
		s = string(x)
	default:
		return fmt.Errorf("unsupported value type %T", v)
	}
	if _, err := io.WriteString(w, s); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}
