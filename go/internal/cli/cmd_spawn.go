package cli

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"factory/go/internal/herdr"
	"factory/go/internal/identity"
	"factory/go/internal/spawn"
)

// spawn (G-048 T-1460, P1h): cmd_spawn in bin/factory_cli.py (11248). Plan refusals come from spawn.Plan unchanged and
// spawn.Exec runs a plan that passed the gate. Every shape Go does not run yet (new worktree, sandbox, gh credential,
// cursor, the evaluator, a new herdr pane, coder and reviewer kickoff extras, PR staleness pre-check, judge holds, goal
// token limits, researcher) is handed to Python BEFORE any write. Unreachable until P1-L2 turns the command on.

func init() {
	register("spawn", "", handler{parse: parseSpawn, run: runSpawn, ident: func(any) identity.Cmd { return identity.Cmd{Name: "spawn"} }})
}

type spawnCmd struct {
	role                                                         string
	goal, team, task, name, cwd, prompt, model, org, hostBackend string
	sandbox                                                      bool
}

// parseSpawn accepts `spawn ROLE [--goal G] [--team T] [--task T] [--name N] [--cwd P] [--prompt X] [--model M]
// [--org O] [--host-backend herdr|hc] [--sandbox]` with the role first, each option at most once. Anything argparse
// could read differently (abbreviated options, options before the role, a dash value) delegates.
func parseSpawn(rest []string) (any, bool) {
	var plain []string
	sandbox := false
	for _, a := range rest {
		if a == "--sandbox" {
			if sandbox {
				return nil, false
			}
			sandbox = true
			continue
		}
		plain = append(plain, a)
	}
	pos, vals, ok := msgArgs(plain, []string{"--goal", "--team", "--task", "--name", "--cwd", "--prompt", "--model", "--org", "--host-backend"}, 1, 1)
	if !ok || !inList(spawnRoles, pos[0]) {
		return nil, false
	}
	if hb, has := vals["--host-backend"]; has && hb != "herdr" && hb != "hc" {
		return nil, false
	}
	v := func(k string) string { return vals[k] }
	for _, k := range []string{"--goal", "--team", "--task", "--name", "--cwd", "--prompt", "--model", "--org"} {
		if x, has := vals[k]; has && x == "" {
			return nil, false // an empty value is falsy in Python: let it decide
		}
	}
	return spawnCmd{role: pos[0], goal: v("--goal"), team: v("--team"), task: v("--task"), name: v("--name"), cwd: v("--cwd"),
		prompt: v("--prompt"), model: v("--model"), org: v("--org"), hostBackend: v("--host-backend"), sandbox: sandbox}, true
}

// spawnRoles is ROLES (the argparse choices).
var spawnRoles = spawn.Roles

// newSessionID is uuid.uuid4(); tests replace it.
var newSessionID = func() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func runSpawn(c *ctx, parsed any) (int, error) {

	p := parsed.(spawnCmd)
	if p.prompt == "-" || strings.HasPrefix(p.prompt, "@") || p.role == "coder" || ((p.role == "qa" || p.role == "reviewer") && p.task != "") || p.role == "researcher" || p.role == "evaluator" || p.sandbox {
		return 0, errDelegate
	}
	worker, del := callerIsWorker(c)
	if del {
		return 0, errDelegate
	}
	role, err := actorRole(c)
	if err != nil {
		return 0, err
	}
	home := c.env.Home()
	req := spawn.Request{Role: p.role, Goal: p.goal, Team: p.team, Task: p.task, Name: p.name, Cwd: p.cwd, Prompt: p.prompt,
		Model: p.model, Org: p.org, HostBackend: p.hostBackend, SandboxOptin: p.sandbox, StrictName: p.name != "",
		AddHomeDir: true, Caller: role, CallerName: c.who, CallerWorker: worker, SessionID: newSessionID()}
	if role == "lead" {
		var g, t sql.NullString
		if err := c.db.QueryRow("SELECT goal_id, team_id FROM agents WHERE name=?", c.who).Scan(&g, &t); err != nil {
			return 0, errDelegate // Python crashes on a missing row
		}
		if req.Goal == "" {
			req.Goal = g.String
		}
		if req.Team == "" {
			req.Team = t.String
		}
	}
	f, host, err := spawnFacts(c, req, home)

	if err != nil {
		return 0, err
	}
	// Plan refusals all precede the gate (a failed gate queues): a probe that stops at the gate finds them without a
	// host call. Then the shape hand-over, then the gate snapshot (it lists the host's agents): a hand-over to Python
	// never follows a host call of ours (M36655).
	probe := f
	probe.StopAtGate = true
	plan := spawn.Plan(req, probe)
	if plan.AtGate {
		if shapeDelegates(req, f, host.exe) {
			return 0, needPython("shape not ported")
		}
		snap, gcfg, err := resSnapshot(c, c.env.Now())
		if err != nil {
			return 0, err
		}
		f.Gate, f.GateConfig = snap, gcfg
		plan = spawn.Plan(req, f)
	}
	if plan.Start != nil {
		if plan.Start.NeedWorktree || plan.Start.Sandboxed || plan.Start.GhCred || plan.Start.EvaluatorSettings || plan.Start.Runtime != "claude" || host.exe != "hc" {
			return 0, errDelegate
		}
	}
	if plan.Bounce != "" || plan.Requeue != "" {
		return 0, errDelegate
	}
	kd, kerr := kickoffDelay(c)
	if kerr != nil {
		return 0, kerr
	}
	d := spawn.ExecDeps{DB: ctxExecer{c}, Host: host.client, Now: c.env.Now, Epoch: func() float64 { return float64(c.env.Now().UnixNano()) / 1e9 },
		Sleep: time.Sleep, HostExe: host.exe, Home: home, Actor: c.who, Stdout: c.out, Live: plan.Stats.Live, KickoffDelay: kd, SettleDelay: time.Second,
		AsyncStart: plan.Start != nil && f.Config["tick_async_start"] == "1"}
	d.Send = func(to, subject, body, kind, goal, task string) error {
		rcpt, err := msgResolveRecipient(c, to, plan.Org)
		if err != nil {
			return err
		}
		var g, t any
		if goal != "" {
			g = goal
		}
		if task != "" {
			t = task
		}
		_, err = c.exec("INSERT INTO messages(sender,recipient,kind,subject,body,goal_id,task_id,created) VALUES(?,?,?,?,?,?,?,?)",
			"factory", rcpt, kind, subject, body, g, t, eventTS(c))
		return err
	}
	var scriptErr error
	d.TaskGoal = f.Task.Goal
	if d.Existing, err = spawnExisting(c, plan.Name); err != nil {
		return 0, err
	}
	d.CheckStart = func() error { return scriptErr }
	if host.client != nil && host.exe == "hc" {
		host.client.SpawnHostExe = "hc"
		d.SetRun = hcSetRun(c, host.client, f.RunDir, &scriptErr)
	}
	if err := spawn.EmitEvents(plan, d); err != nil {
		return 0, err
	}
	switch {
	case plan.Refusal != nil:
		return 0, dieErr(plan.Refusal.Code, plan.Refusal.Msg)
	case plan.Queue != nil:
		if _, err := spawn.ApplyQueue(plan, false, d); err != nil {
			return 0, err
		}
		return 0, nil
	}
	_, err = spawn.Exec(plan, d)
	if err != nil {
		var ref *spawn.WorktreeRefusal
		switch {
		case errors.Is(err, spawn.ErrNotPorted):
			return 0, errDelegate
		case errors.As(err, &ref):
			return 0, dieErr(ref.Code, ref.Msg)
		}
		return 0, dieErr(1, err.Error())
	}
	return 0, nil
}

// kickoffDelay is float(os.environ.get("FACTORY_KICKOFF_DELAY_S") or 2); a value Go cannot read hands over.
func kickoffDelay(c *ctx) (time.Duration, error) {
	v := envOf(c, "FACTORY_KICKOFF_DELAY_S")
	if v == "" {
		return 2 * time.Second, nil
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || !(f >= 0 && f <= 3600) {
		return 0, needPython("line 188")
	}
	return time.Duration(f * float64(time.Second)), nil
}

// hcSetRun installs the hc run context of one `agent start` (Python's _HC_RUN_CWD/_HC_RUN_ENV/_HC_RUN_NAME): the
// agent env goes into a wrapper script under run/, never onto the hc argv. A script that cannot be written is kept in
// *scriptErr and the start is pointed at a path that cannot run, so no agent starts without its env.
func hcSetRun(c *ctx, cl *herdr.Client, runDir string, scriptErr *error) spawn.RunSettings {
	getenv := func(k string) (string, bool) { return lookupEnv(c, k) }
	live := liveSocketFn(c)
	return func(cwd string, env []spawn.KV, name string) {
		cl.RunCwd = cwd
		cl.RunTail = nil
		if len(env) == 0 {
			return
		}
		cl.RunTail = func(program string, post []string) []string {
			tail, err := spawn.HCRunArgvTail(envOf(c, "FACTORY_HC_SPAWN_SCRIPT_ROOT"), runDir, name, env, program, post, getenv, live)
			if err != nil {
				*scriptErr = fmt.Errorf("hc spawn script: %w", err)
				return []string{"sh", "/nonexistent/factory-hc-spawn-script-failed"}
			}
			return tail
		}
	}
}

// liveSocketFn is _is_live_socket: val names the live host socket by any spelling.
func liveSocketFn(c *ctx) func(string) bool {
	return func(val string) bool {
		if val == "" {
			return false
		}
		if val == herdr.DefaultSocket {
			return true
		}
		live := identity.Realpath(c.env.UserHome() + herdr.DefaultSocket[1:])
		cands := []string{identity.Realpath(expandHome(c, val))}
		if val == "~" || strings.HasPrefix(val, "~/") {
			cands = append(cands, identity.Realpath(c.env.UserHome()+val[1:]))
		}
		for _, x := range cands {
			if x == live {
				return true
			}
		}
		return false
	}
}

func expandHome(c *ctx, p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		return c.env.UserHome() + p[1:]
	}
	return p
}

func envOf(c *ctx, k string) string {
	for _, kv := range c.deps.Environ {
		if strings.HasPrefix(kv, k+"=") {
			return kv[len(k)+1:]
		}
	}
	return ""
}

// ctxExecer writes through ctx.exec so the statement trace sees every spawn write.
type ctxExecer struct{ c *ctx }

func (e ctxExecer) Exec(q string, args ...any) (sql.Result, error) { return e.c.exec(q, args...) }

// spawnHost is the host the agent starts on and the client that talks to it.
type spawnHost struct {
	exe    string
	client *herdr.Client
}

// spawnFacts reads everything Plan needs. It returns needPython("line 266") for any fact Go does not reproduce.
func spawnFacts(c *ctx, req spawn.Request, home string) (spawn.Facts, spawnHost, error) {
	var f spawn.Facts
	f.Home, f.RunDir = home, filepath.Join(home, "run")
	cfg := map[string]string{}
	for _, k := range []string{"agent_runtimes", "cursor_pilot_roles", "org_prefix_default", "sandbox_roles", "worker_gh_roles",
		"scrub_terms_file", "opus_effort", "tick_async_start", "orgs_enabled"} {
		v, err := cfgText(c, k)
		if err != nil {
			return f, spawnHost{}, err
		}
		cfg[k] = v
	}
	f.Config = cfg
	orgsOn, err := enabledOrgs(c)
	if err != nil {
		return f, spawnHost{}, err
	}
	f.EnabledOrgs = len(orgsOn)
	if f.CallerOrg, err = agentOrg(c, c.who); err != nil {
		return f, spawnHost{}, err
	}
	f.GoalOrg, err = goalOrg(c, req.Goal)
	if err != nil {
		return f, spawnHost{}, err
	}
	if req.Task != "" {
		var goal, team, trole, spec, acc, wt, pr, hold sql.NullString
		var sandbox sql.NullInt64
		err := c.db.QueryRow("SELECT goal_id, team_id, role, spec, acceptance, worktree, pr_url, judge_hold_reason, sandbox_required FROM tasks WHERE id=?",
			req.Task).Scan(&goal, &team, &trole, &spec, &acc, &wt, &pr, &hold, &sandbox)
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil && strings.Contains(err.Error(), "no such column"):
			return f, spawnHost{}, needPython("tasks table predates the spawn columns")
		case err != nil:
			return f, spawnHost{}, err
		default:
			f.Task = spawn.TaskFacts{Exists: true, Goal: goal.String, Team: team.String, Role: trole.String, Spec: spec.String,
				Acceptance: acc.String, SandboxRequired: sandbox.Int64 != 0, JudgeHoldReason: hold.String, PRURL: pr.String}
			if wt.String != "" {
				if _, serr := os.Stat(wt.String); serr == nil {
					f.Task.WorktreeExists = true
				}
			}
			if pr.String != "" && (req.Role == "reviewer" || req.Role == "qa" || req.Role == "judge") {
				return f, spawnHost{}, needPython("line 310") // the staleness pre-check and the gated-PR line need git and gh
			}
		}
		if f.TaskOrg, err = taskOrg(c, req.Task); err != nil {
			return f, spawnHost{}, err
		}
	}
	if req.Role == "judge" {
		return f, spawnHost{}, needPython("line 318") // judge_hold
	}
	if req.Goal != "" {
		var lim sql.NullInt64
		if err := c.db.QueryRow("SELECT token_limit FROM goals WHERE id=?", req.Goal).Scan(&lim); err == nil && lim.Int64 > 0 {
			return f, spawnHost{}, needPython("line 323") // the goal token tally
		}
	}
	f.Agent = func(name string) *spawn.AgentRow {
		var r spawn.AgentRow
		var pane, sa, org sql.NullString
		if err := c.db.QueryRow("SELECT name, role, status, pane_id, spawn_args, org FROM agents WHERE name=?", name).Scan(&r.Name, &r.Role, &r.Status, &pane, &sa, &org); err != nil {
			return nil
		}
		r.PaneID, r.Org, r.SpawnArgs = pane.String, org.String, []byte(sa.String)
		return &r
	}
	// a done/exited row that still holds a pane needs release_stale_pane (a host call and writes): Python's
	if err := spawnStaleCheck(c, req); err != nil {
		return f, spawnHost{}, err
	}
	f.LiveTwin = func(role, org string) string {
		rows, err := c.db.Query("SELECT name, spawn_args FROM agents WHERE role=? AND status IN ('live','starting')", role)
		if err != nil {
			return ""
		}
		var names []string
		for rows.Next() {
			var n string
			var sa sql.NullString
			if rows.Scan(&n, &sa) != nil {
				continue
			}
			names = append(names, n)
		}
		if rows.Close() != nil || rows.Err() != nil {
			return ""
		}
		// agentOrg queries too: the cursor is closed first (one connection, a nested query would wait for it forever)
		for _, n := range names {
			if o, _ := agentOrg(c, n); o == org {
				return n
			}
		}
		return ""
	}
	live, lerr := c.env.HomeIsLive()
	if lerr != nil {
		return f, spawnHost{}, needPython("line 359")
	}
	f.WorkerStubExempt = herdr.IsTestStub() && !live
	f.RolesDir = filepath.Join(home, "roles") // ROLES_DIR = HOME / "roles"
	if req.Role != "evaluator" {
		if f.RoleSettingsTemplate, err = spawn.ReadText(filepath.Join(f.RolesDir, "settings", req.Role+".json.in")); err != nil {
			return f, spawnHost{}, needPython("line 370") // Python's own message
		}
	}
	if f.CommonText, err = spawn.ReadText(filepath.Join(f.RolesDir, "_common.md")); err != nil {
		return f, spawnHost{}, needPython("line 374")
	}
	if f.RoleText, err = spawn.ReadText(filepath.Join(f.RolesDir, req.Role+".md")); err != nil {
		return f, spawnHost{}, needPython("line 377")
	}
	if fi, err := os.Stat(filepath.Join(f.RolesDir, "plugins", req.Role)); err == nil && fi.IsDir() {
		f.RolePluginDirExists = true
	}
	primary, homes, derr := c.env.DeriveLiveHomes()
	if derr != nil {
		return f, spawnHost{}, needPython("line 384")
	}
	rr := identity.Realpath(runRootOf(c))
	if r := spawn.CheckLivePin(homes[identity.Realpath(home)], primary, rr, identity.Realpath(home)); r != nil {
		return f, spawnHost{}, needPython("line 388")
	}
	pw := identity.Realpath(c.env.UserHome())
	f.Tokens = spawn.SettingsTokens(primary, pw, filepath.Join(pw, ".config"))
	cl := &herdr.Client{Store: &resStore{db: c.db}}
	exe := ""
	switch req.HostBackend {
	case "hc", "herdr":
		exe = req.HostBackend
	default:
		_, exe = cl.TerminalBackendEffective()
	}
	return f, spawnHost{exe: exe, client: cl}, nil
}

// shapeDelegates is the plan.Start shape check of runSpawn (new worktree, sandbox, gh credential, evaluator, non-claude
// runtime, non-hc host) decided from config alone, so it runs before the gate snapshot makes a host call. It is
// conservative: any cursor runtime setting hands over. Python decides everything it hands over.
func shapeDelegates(req spawn.Request, f spawn.Facts, exe string) bool {
	switch {
	case exe != "hc", req.Role == "evaluator":
		return true
	case spawn.SandboxWanted(req.Role, req.SandboxOptin, f.Config["sandbox_roles"]):
		return true
	case inList(strings.Split(f.Config["worker_gh_roles"], ","), req.Role):
		return true
	case strings.Contains(f.Config["agent_runtimes"], "cursor") || strings.TrimSpace(f.Config["cursor_pilot_roles"]) != "":
		return true
	case req.Cwd == "" && (req.Role == "coder" || req.Role == "qa" || req.Role == "reviewer"):
		return true
	}
	return false
}

// spawnStaleCheck hands over a name whose previous row is done/exited with a pane (release_stale_pane).
func spawnStaleCheck(c *ctx, req spawn.Request) error {
	for _, name := range candidateNames(req) {
		var st string
		var pane sql.NullString
		err := c.db.QueryRow("SELECT status, pane_id FROM agents WHERE name=?", name).Scan(&st, &pane)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if (st == "done" || st == "exited") && pane.String != "" {
			return needPython("line 416")
		}
	}
	return nil
}

// candidateNames are the agent names a spawn may resolve to: the explicit name, else the default and its org-prefixed form.
func candidateNames(req spawn.Request) []string {
	base := req.Name
	if base == "" {
		b := req.Task
		if b == "" {
			b = req.Team
		}
		if b == "" {
			b = req.Goal
		}
		if b == "" {
			b = req.Role
		}
		base = req.Role + "-" + b
		if (req.Role == "boss" || req.Role == "director") && req.Task == "" && req.Team == "" && req.Goal == "" {
			base = req.Role
		}
	}
	base = spawn.Slug(base)
	out := []string{base}
	for _, o := range []string{"claude", "cursor"} {
		out = append(out, spawn.OrgName(o, base))
	}
	return out
}

// runRootOf is the checkout this bin/factory runs from (not FACTORY_HOME).
func runRootOf(c *ctx) string { return filepath.Dir(filepath.Dir(c.deps.pyEntry())) }

// needPython is errDelegate with the reason, for a log line and the tests.
func needPython(why string) error { return fmt.Errorf("%s: %w", why, errDelegate) }

func lookupEnv(c *ctx, k string) (string, bool) {
	for _, kv := range c.deps.Environ {
		if strings.HasPrefix(kv, k+"=") {
			return kv[len(k)+1:], true
		}
	}
	return "", false
}

// spawnExisting is the agents row of the name before this spawn (flagless, session_id), nil = none.
func spawnExisting(c *ctx, name string) (*spawn.ExistingRow, error) {
	var sid sql.NullString
	var flagless sql.NullInt64
	err := c.db.QueryRow("SELECT session_id, flagless FROM agents WHERE name=?", name).Scan(&sid, &flagless)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &spawn.ExistingRow{SessionID: sid.String, Flagless: flagless.Int64 != 0}, nil
}
