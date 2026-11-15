package spawn

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"factory/go/internal/gate"
)

func f64(v float64) *float64 { return &v }

func baseGate() (gate.Snapshot, gate.Config) {
	snap := gate.Snapshot{Now: time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC), HerdrReachable: true, MemMB: 3000,
		Usage: gate.Usage{FiveHour: f64(10), SevenDay: f64(20), Fresh: true},
		Procs: gate.ProcScan{Known: true, Argv: map[int][]string{}}, Load1: 0.5, NumCPU: 8}
	cfg := gate.Config{MaxLiveAgents: 3, MaxTotalClaude: 10, MinMemAvailableMB: 500, TmpMaxPct: 90, MaxFiveHourPct: 90,
		MaxSevenDayPct: 95, LoadRatio: 1.5, MaxLiveLeads: 4, ReservedWorkerSlots: 10, ReviewReservedSlots: 2,
		ReviewAutospawn: "off", LeadParking: "0", MinMemText: "500", TmpMaxPctText: "90", LoadRatioText: "1.5",
		FiveHourText: "90", SevenDayText: "95"}
	return snap, cfg
}

func baseFacts() Facts {
	snap, cfg := baseGate()
	tpl, err := ReadText("testdata/roles/settings/coder.json.in")
	if err != nil {
		panic(err)
	}
	return Facts{
		Config: map[string]string{"org_prefix_default": "1", "opus_effort": "medium"},
		Task:   TaskFacts{Exists: true, Goal: "G-1", Team: "g-1-a", Role: "coder", Spec: "spec", Acceptance: "acc"},
		Gate:   snap, GateConfig: cfg,
		Home: "/h", RunDir: "/h/run", RolesDir: "/h/roles",
		RoleSettingsTemplate: tpl, Tokens: SettingsTokens("/h", "/w/u", "/w/u/.config"),
		CommonText: "common", RoleText: "role", EnabledOrgs: 1,
	}
}

func coderReq() Request {
	return Request{Role: "coder", Task: "T-1", AddHomeDir: true, SessionID: "sid-1", Caller: "director", Prompt: "do it"}
}

// TestPlanRefusals: every refusal carries Python's exact text and exit code; the allowed cases plan a start.
func TestPlanRefusals(t *testing.T) {
	live := &AgentRow{Name: "claude-coder-t-1", Role: "coder", Status: "live", PaneID: "p-7"}
	cases := []struct {
		name   string
		req    func(*Request)
		facts  func(*Facts)
		code   int
		text   string // refusal text (die message), "" when the case must not refuse
		queued string // gate/hold reason when the plan queues
		events []string
	}{
		{name: "allowed: a normal coder spawn"},
		{name: "allowed: a lead spawn", req: func(r *Request) {
			*r = Request{Role: "lead", Goal: "G-1", Team: "g-1-a", AddHomeDir: true, Caller: "director"}
		}},
		{name: "unknown role", req: func(r *Request) { r.Role = "wizard" }, code: 1,
			text: "role must be one of ['boss', 'director', 'lead', 'pm', 'coder', 'qa', 'reviewer', 'judge', 'cleaner', 'staff-engineer', 'researcher', 'scout', 'retro', 'evaluator']"},
		{name: "live duplicate agent", facts: func(f *Facts) { f.Agent = func(n string) *AgentRow { return live } }, code: 1,
			text: "claude-coder-t-1 is already live in p-7"},
		{name: "starting duplicate", facts: func(f *Facts) {
			f.Agent = func(n string) *AgentRow { return &AgentRow{Name: n, Status: "starting"} }
		}, code: 2, text: "claude-coder-t-1 is already starting (claimed by the tick); wait for it to go live"},
		{name: "no such task", facts: func(f *Facts) { f.Task.Exists = false }, code: 2, text: "no task T-1"},
		{name: "task without spec", facts: func(f *Facts) { f.Task.Spec = "   \v" }, code: 2,
			text: "T-1 has no spec in the DB; write it with factory task update T-1 --spec - before spawning (scratch files are lost on reboot)"},
		{name: "coder task without acceptance", facts: func(f *Facts) { f.Task.Acceptance = "" }, code: 2,
			text: "T-1 has no acceptance in the DB; write it with factory task update T-1 --acceptance - before spawning (scratch files are lost on reboot)"},
		{name: "qa without PR or worktree", req: func(r *Request) { r.Role = "qa" }, facts: func(f *Facts) { f.Task.Role = "qa" }, code: 2,
			text: "T-1 has no PR and no worktree, so a qa agent would start in the shared checkout: create a worktree for it, or pass one: factory spawn qa --task T-1 --cwd <worktree>"},
		{name: "stale head bounced", facts: func(f *Facts) {
			f.Staleness = "CONFLICT-ONLY: a.go; spawn a merge-only coder (fix-loop, not an attempt)"
		}, code: 2,
			text: "CONFLICT-ONLY: a.go; spawn a merge-only coder (fix-loop, not an attempt)", events: []string{"task/T-1/spawn-bounced/coder: CONFLICT-ONLY: a.go; spawn a merge-only coder (fix-loop, not an attempt)"}},
		{name: "sandbox_required without --sandbox", facts: func(f *Facts) { f.Task.SandboxRequired = true }, code: 3,
			text: "T-1 is sandbox_required: rerun with --sandbox (factory spawn coder ... --task T-1 --sandbox)", events: []string{"task/T-1/sandbox-required-refused/coder coder"}},
		{name: "sandbox_required with --sandbox is allowed", req: func(r *Request) { r.SandboxOptin = true }, facts: func(f *Facts) { f.Task.SandboxRequired = true }},
		{name: "--sandbox on a role that cannot", req: func(r *Request) { r.Role, r.Task, r.SandboxOptin = "pm", "", true }, code: 2,
			text: "--sandbox: pm cannot be sandboxed; allowed: coder, qa, researcher, reviewer"},
		{name: "researcher without task", req: func(r *Request) { r.Role, r.Task = "researcher", "" }, code: 2,
			text: "researcher needs a task: factory spawn researcher --task T-NNNN (its research dir is per task)"},
		{name: "researcher on the cursor org", req: func(r *Request) { r.Role, r.Org = "researcher", "cursor" }, facts: func(f *Facts) { f.Task.Role = "researcher" }, code: 3,
			text: "the researcher role is not supported on the Cursor runtime (docs/research-team.md); use the claude org"},
		{name: "scout by a lead", req: func(r *Request) { r.Role, r.Task, r.Caller = "scout", "", "lead" }, code: 3,
			text: "only the director (or the owner) spawns a scout; the tick does it on schedule"},
		{name: "retro by a lead", req: func(r *Request) { r.Role, r.Task, r.Caller = "retro", "", "lead" }, code: 3,
			text: "only the director (or the owner) spawns a retro; the tick does it on schedule"},
		{name: "boss spawns a coder", req: func(r *Request) { r.Caller = "boss" }, code: 3, text: "the boss only (re)starts the director; ask the director for teams"},
		{name: "lead spawns a lead", req: func(r *Request) { r.Role, r.Task, r.Caller = "lead", "", "lead" }, code: 3,
			text: "leads spawn workers only (pm, coder, qa, reviewer, judge, cleaner)"},
		{name: "evaluator through the CLI", req: func(r *Request) { r.Role = "evaluator" }, code: 3,
			text: "the evaluator is spawned only by the skilleval harness; run `factory skilleval try` instead"},
		{name: "coder worker caller", req: func(r *Request) { r.Caller, r.CallerWorker, r.CallerName = "coder", "coder", "claude-coder-t-1" }, code: 3,
			text: "role 'coder' (claude-coder-t-1) may not do this: workers may not run `factory spawn`; ask your lead"},
		{name: "worker caller, stub exempt, still needs require_role", req: func(r *Request) { r.Caller, r.CallerWorker, r.CallerName = "coder", "coder", "claude-coder-t-1" },
			facts: func(f *Facts) { f.WorkerStubExempt = true }, code: 3, text: "role 'coder' (claude-coder-t-1) may not do this; allowed: director, lead, boss"},
		{name: "pm caller refused by require_role", req: func(r *Request) { r.Caller, r.CallerName = "pm", "claude-pm-x" }, code: 3,
			text: "role 'pm' (claude-pm-x) may not do this; allowed: director, lead, boss"},
		{name: "qa caller refused by require_role", req: func(r *Request) { r.Caller, r.CallerName = "qa", "claude-qa-x" }, code: 3,
			text: "role 'qa' (claude-qa-x) may not do this; allowed: director, lead, boss"},
		{name: "reviewer caller refused by require_role", req: func(r *Request) { r.Caller, r.CallerName = "reviewer", "claude-reviewer-x" }, code: 3,
			text: "role 'reviewer' (claude-reviewer-x) may not do this; allowed: director, lead, boss"},
		{name: "judge caller refused by require_role", req: func(r *Request) { r.Caller, r.CallerName = "judge", "claude-judge-x" }, code: 3,
			text: "role 'judge' (claude-judge-x) may not do this; allowed: director, lead, boss"},
		{name: "cleaner caller refused by require_role", req: func(r *Request) { r.Caller, r.CallerName = "cleaner", "claude-cleaner-x" }, code: 3,
			text: "role 'cleaner' (claude-cleaner-x) may not do this; allowed: director, lead, boss"},
		{name: "staff-engineer caller refused by require_role", req: func(r *Request) { r.Caller, r.CallerName = "staff-engineer", "claude-staff-engineer-x" }, code: 3,
			text: "role 'staff-engineer' (claude-staff-engineer-x) may not do this; allowed: director, lead, boss"},
		{name: "unknown caller refused", req: func(r *Request) { r.Caller, r.CallerName = "unknown", "x" }, code: 3,
			text: "role 'unknown' (x) may not do this; allowed: director, lead, boss"},
		{name: "owner caller allowed", req: func(r *Request) { r.Caller = "owner" }},
		{name: "unknown org", req: func(r *Request) { r.Org = "mars" }, code: 2, text: "unknown org 'mars'; known: claude, cursor"},
		{name: "goal of another org", req: func(r *Request) { r.Org, r.Goal = "cursor", "G-1" }, facts: func(f *Facts) { f.GoalOrg = "claude" }, code: 2,
			text: "G-1 belongs to the claude org; move it first: factory goal org G-1 cursor"},
		{name: "strict --name changes", req: func(r *Request) { r.Name, r.StrictName = "coder-x", true }, code: 2,
			text: "--name coder-x is refused: agents of the claude org must be named claude-coder-x (org naming rule: every name starts with the org prefix). Use --name claude-coder-x, or omit --name for the default."},
		{name: "goal at its token limit", facts: func(f *Facts) { f.GoalTokenLimit, f.GoalTokenUsed, f.GoalTokenTally = 100, 100, true }, code: 3,
			text:   "G-1 is at its token limit: tally 100 tokens, limit 100. A coder is new work and is not started; judge, reviewer, qa, lead and cleaner still are. To go on, the director or owner raises it: factory goal edit G-1 --token-limit N",
			events: []string{"task/T-1/goal-token-limit-refused/coder: G-1 is at its token limit: tally 100 tokens, limit 100. A coder is new work and is not started; judge, reviewer, qa, lead and cleaner still are. To go on, the director or owner raises it: factory goal edit G-1 --token-limit N"}},
		{name: "token limit does not stop a judge", req: func(r *Request) { r.Role = "judge" }, facts: func(f *Facts) {
			f.GoalTokenLimit, f.GoalTokenUsed, f.GoalTokenTally = 100, 500, true
			f.Task.Role = "judge"
		}},
		{name: "second live director of the org", req: func(r *Request) { r.Role, r.Task, r.Goal, r.Team = "director", "", "", "" }, facts: func(f *Facts) {
			f.LiveTwin = func(role, org string) string { return "claude-director" }
		}, code: 3, text: "claude-director is already live as the claude director; not starting claude-director. To restart it use `factory relaunch claude-director` (add --fresh for a new session)."},
		{name: "relaunch over a live row is allowed", req: func(r *Request) { r.Relaunch = true }, facts: func(f *Facts) { f.Agent = func(n string) *AgentRow { return live } }},
		{name: "over the resource cap is queued", facts: func(f *Facts) {
			for _, n := range []string{"a0", "a1", "a2"} {
				f.Gate.LiveRows = append(f.Gate.LiveRows, gate.AgentRow{Name: n, PaneID: "p-" + n, Started: "2026-11-15T01:00:00+00:00", SessionID: "s", Runtime: "claude"})
				f.Gate.HerdrAgents = append(f.Gate.HerdrAgents, gate.HerdrAgent{Name: n, PaneID: "p-" + n, Agent: "claude"})
			}
		}, queued: "3/3 agent panes live", events: []string{"agent/claude-coder-t-1/queued/3/3 agent panes live"}},
		{name: "herdr unreachable is queued", facts: func(f *Facts) { f.Gate.HerdrReachable = false }, queued: "herdr unreachable",
			events: []string{"agent/claude-coder-t-1/queued/herdr unreachable"}},
		{name: "caller hold queues", req: func(r *Request) { r.Hold = "lead waits" }, queued: "lead waits", events: []string{"agent/claude-coder-t-1/queued/lead waits"}},
		{name: "judge held by the task flag", req: func(r *Request) { r.Role = "judge" }, facts: func(f *Facts) { f.Task.Role = "judge"; f.Task.JudgeHoldReason = " lead holds it " },
			queued: "lead holds it", events: []string{"agent/claude-judge-t-1/queued/lead holds it"}},
		{name: "judge held by the cap or a not-ready PR", req: func(r *Request) { r.Role = "judge" }, facts: func(f *Facts) { f.Task.Role = "judge"; f.JudgeHold = "3 judges live or starting (max_live_judges=3)" },
			queued: "3 judges live or starting (max_live_judges=3)", events: []string{"agent/claude-judge-t-1/queued/3 judges live or starting (max_live_judges=3)"}},
		{name: "a queued judge from the queue skips the hold", req: func(r *Request) { r.Role, r.FromQueue = "judge", true }, facts: func(f *Facts) { f.Task.Role = "judge"; f.Task.JudgeHoldReason = "x" }},
		{name: "done task is not refused by Python's spawn()", facts: func(f *Facts) {}},
		{name: "bad opus_effort", req: func(r *Request) { r.Role = "pm"; r.Task = "" }, facts: func(f *Facts) { f.Config["opus_effort"] = "bogus" }, code: 1,
			text: "config opus_effort must be one of low, medium, high, xhigh, max or empty, got 'bogus': fix it with `factory config opus_effort medium`"},
		{name: "settings template typo stops the spawn", facts: func(f *Facts) { f.RoleSettingsTemplate = `{"a": "@FACTORY_NOPE@"}` }, code: 3,
			text: "settings render failed for coder: settings template /h/roles/settings/coder.json.in has an unknown @FACTORY_ token; known: @FACTORY_LIVE_HOME@, @FACTORY_HOME_DIR@, @FACTORY_USER_CONFIG@; nothing was started"},
		{name: "researcher cwd outside its research dir", req: func(r *Request) { r.Role, r.Cwd = "researcher", "/elsewhere" }, facts: func(f *Facts) { f.Task.Role = "researcher"; f.ResearchDir = "/rs/T-1" }, code: 4,
			text: "researcher cwd must be its research dir /rs/T-1, not /elsewhere; drop --cwd (or fix the recorded cwd)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, f := coderReq(), baseFacts()
			if c.req != nil {
				c.req(&req)
			}
			if c.facts != nil {
				c.facts(&f)
			}
			p := Plan(req, f)
			var events []string
			for _, e := range p.Events {
				events = append(events, fmt.Sprintf("%s/%s/%s/%s", e.Entity, e.ID, e.Action, e.Detail))
			}
			switch {
			case c.text != "":
				if p.Refusal == nil || p.Refusal.Msg != c.text || p.Refusal.Code != c.code {
					t.Fatalf("want refusal (%d) %q, got %+v (queue %v)", c.code, c.text, p.Refusal, p.Queue)
				}
			case c.queued != "":
				if p.Refusal != nil || p.Queue == nil || p.Queue.Reason != c.queued {
					t.Fatalf("want queued %q, got refusal %+v queue %+v", c.queued, p.Refusal, p.Queue)
				}
				if want := fmt.Sprintf("QUEUED %s: %s. The tick starts it when capacity frees up.", p.Name, c.queued); p.Queue.Message != want {
					t.Errorf("queue message %q, want %q", p.Queue.Message, want)
				}
				if p.Start != nil {
					t.Errorf("a queued plan must not start")
				}
			default:
				if p.Refusal != nil || p.Queue != nil || p.Start == nil {
					t.Fatalf("want a start, got refusal %+v queue %+v", p.Refusal, p.Queue)
				}
			}
			if c.events != nil && fmt.Sprint(events) != fmt.Sprint(c.events) {
				t.Errorf("events %q, want %q", events, c.events)
			}
		})
	}
}

// TestPlanStart: the planned start of a coder carries the names, org, env, argv and spawn_args of Python's spawn().
func TestPlanStart(t *testing.T) {
	f := baseFacts()
	f.Config["scrub_terms_file"] = " /tmp/terms "
	f.Config["sandbox_roles"] = "reviewer"
	f.Config["worker_gh_roles"] = "coder"
	f.RolePluginDirExists = true
	f.GoalPluginDir = "/h/roles/plugins/goals/G-1"
	f.EnabledOrgs = 2
	p := Plan(coderReq(), f)
	if p.Refusal != nil || p.Start == nil {
		t.Fatalf("plan: %+v", p.Refusal)
	}
	s := p.Start
	if p.Name != "claude-coder-t-1" || p.Org != "claude" || s.Model != "claude-sonnet-5-5" || s.Runtime != "claude" {
		t.Errorf("identity: %s %s %s %s", p.Name, p.Org, s.Model, s.Runtime)
	}
	if !s.NeedWorktree || s.Cwd != "" || s.Sandboxed || !s.GhCred {
		t.Errorf("worktree/sandbox flags: %+v", s)
	}
	wantEnv := "[{FACTORY_AGENT claude-coder-t-1} {FACTORY_ROLE coder} {FACTORY_HOME /h} {SCRUB_TERMS_FILE /tmp/terms}"
	if got := fmt.Sprint(s.Env); !strings.HasPrefix(got, wantEnv) {
		t.Errorf("env %s, want prefix %s", got, wantEnv)
	}
	wantArgs := "--model claude-sonnet-5-5 --permission-mode auto --settings /h/run/settings/coder.json " +
		"--append-system-prompt-file /h/run/claude-coder-t-1.prompt.md --add-dir /h -n claude-coder-t-1 " +
		"--strict-mcp-config --mcp-config {\"mcpServers\":{}} --plugin-dir /h/roles/plugins/coder --plugin-dir /h/roles/plugins/goals/G-1 --session-id sid-1"
	if got := strings.Join(s.ClaudeArgs, " "); got != wantArgs {
		t.Errorf("claude args:\n got %s\nwant %s", got, wantArgs)
	}
	wantJSON := `{"role": "coder", "goal": "G-1", "team": "g-1-a", "task": "T-1", "name": "claude-coder-t-1", "cwd": null, "prompt": "do it", "model": null, "resume": null, "fresh": false, "org": "claude"}`
	if p.ArgsJSON != wantJSON {
		t.Errorf("spawn_args:\n got %s\nwant %s", p.ArgsJSON, wantJSON)
	}
	if want := "You are `claude-coder-t-1`, the factory coder. Run `factory whoami` and `factory inbox` first. Your task is T-1: read it with `factory task show T-1`. Goal context: `factory goal show G-1`. Your team is g-1-a. do it"; s.Kickoff != want {
		t.Errorf("kickoff %q", s.Kickoff)
	}
	if !strings.HasSuffix(s.PromptText, "Never use Fable.\nOther orgs on this box: Cursor (`cursor-*`). They share the box capacity (one agent cap; P1 goals first across orgs) and the repos. Only the boss and the director talk across orgs (e.g. `factory msg cursor-director ...` for capacity, file conflicts or red main); leads and workers never do.\n") {
		t.Errorf("multi-org prompt lacks the org block: %q", s.PromptText)
	}

	// resume of a session recorded on another runtime is dropped and becomes a fresh one
	f2 := baseFacts()
	f2.Agent = func(n string) *AgentRow {
		return &AgentRow{Name: n, Status: "done", SpawnArgs: []byte(`{"runtime": "cursor"}`)}
	}
	r2 := coderReq()
	r2.Resume = "old-session"
	p2 := Plan(r2, f2)
	if p2.Refusal != nil || p2.Resume != "" || !p2.Fresh || len(p2.Events) != 1 || p2.Events[0].Action != "resume-dropped" ||
		p2.Events[0].Detail != "runtime cursor -> claude" {
		t.Fatalf("resume drop: %+v %+v", p2.Refusal, p2.Events)
	}
	if got := strings.Join(p2.Start.ClaudeArgs, " "); !strings.HasSuffix(got, "--session-id sid-1 --system-prompt-snapshot off") {
		t.Errorf("fresh relaunch flags: %s", got)
	}
	// a kept resume
	f3, r3 := baseFacts(), coderReq()
	r3.Resume = "keep-me"
	f3.Agent = func(n string) *AgentRow { return &AgentRow{Name: n, Status: "done"} }
	if got := strings.Join(Plan(r3, f3).Start.ClaudeArgs, " "); !strings.HasSuffix(got, "--resume keep-me --system-prompt-snapshot off") {
		t.Errorf("resume flags: %s", got)
	}
}

// TestPlanOrgAndNames: org derivation and the org prefix naming rule.
func TestPlanOrgAndNames(t *testing.T) {
	cases := []struct {
		name, org, wantName string
		req                 Request
		facts               func(*Facts)
	}{
		{"default org prefix on", "claude", "claude-coder-t-1", coderReq(), nil},
		{"default org prefix off", "claude", "coder-t-1", coderReq(), func(f *Facts) { f.Config["org_prefix_default"] = "0" }},
		{"goal org decides", "cursor", "cursor-coder-t-1", coderReq(), func(f *Facts) { f.TaskOrg = "cursor" }},
		{"retro is always cursor", "cursor", "cursor-retro-retro", Request{Role: "retro", AddHomeDir: true}, nil},
		{"bare director", "claude", "claude-director", Request{Role: "director", AddHomeDir: true}, nil},
		{"name prefix picks the org", "cursor", "cursor-boss", Request{Role: "boss", Name: "cursor-boss", AddHomeDir: true}, nil},
		{"caller org for a goal-less spawn", "cursor", "cursor-pm-pm", Request{Role: "pm", AddHomeDir: true}, func(f *Facts) { f.CallerOrg = "cursor" }},
		{"slug of the task", "claude", "claude-coder-t-1459-x", Request{Role: "coder", Name: "Coder T-1459 X!", AddHomeDir: true}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := baseFacts()
			if c.facts != nil {
				c.facts(&f)
			}
			p := Plan(c.req, f)
			if p.Refusal != nil || p.Name != c.wantName || p.Org != c.org {
				t.Fatalf("got %q/%q (%+v), want %q/%q", p.Name, p.Org, p.Refusal, c.wantName, c.org)
			}
		})
	}
}

// TestPlanEvaluator: the evaluator spawn is refused unless it sits in a complete trial sandbox.
func TestPlanEvaluator(t *testing.T) {
	trial := "/root/trials/t1"
	good := func() (Request, Facts) {
		f := baseFacts()
		f.Task = TaskFacts{}
		f.Eval = EvalFacts{Root: "/root", Realpath: func(s string) string { return s }, ShimOK: func(string) bool { return true }, PromptOnly: func(string) error { return nil }}
		return Request{Role: "evaluator", Cwd: trial + "/work", PluginDirs: []string{"/root/vendor/p"}, Name: "e1", SessionID: "s",
			EnvExtra: []KV{{"FACTORY_HOME", trial + "/fh"}, {"HOME", trial + "/home"}, {"CLAUDE_CONFIG_DIR", trial + "/cfg"}, {"PATH", trial + "/bin:/usr/bin"}}}, f
	}
	if r, f := good(); true {
		p := Plan(r, f)
		if p.Refusal != nil || p.Start == nil || !strings.Contains(strings.Join(p.Start.ClaudeArgs, " "), "--settings /root/trials/t1/role/settings.json --append-system-prompt-file /root/trials/t1/role/prompt.md -n e1") ||
			strings.Contains(strings.Join(p.Start.ClaudeArgs, " "), "--add-dir") {
			t.Fatalf("good evaluator: %+v %+v", p.Refusal, p.Start)
		}
		if !strings.Contains(p.ArgsJSON, `"env_extra": {"FACTORY_HOME": "/root/trials/t1/fh"`) || !strings.HasSuffix(p.ArgsJSON, `"add_home_dir": false}`) {
			t.Errorf("evaluator spawn_args %s", p.ArgsJSON)
		}
	}
	bad := []struct {
		name string
		mut  func(*Request, *Facts)
		text string
	}{
		{"no env", func(r *Request, f *Facts) { r.EnvExtra = nil }, "the evaluator is spawned only into a trial sandbox: env_extra and add_home_dir=False are required (use `factory skilleval try`)"},
		{"home dir added", func(r *Request, f *Facts) { r.AddHomeDir = true }, "the evaluator is spawned only into a trial sandbox: env_extra and add_home_dir=False are required (use `factory skilleval try`)"},
		{"extra env key", func(r *Request, f *Facts) { r.EnvExtra = append(r.EnvExtra, KV{"LD_PRELOAD", "x"}) },
			"evaluator env_extra may only set FACTORY_HOME, HOME, CLAUDE_CONFIG_DIR, PATH, CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC, DISABLE_AUTOUPDATER; refused: LD_PRELOAD"},
		{"duplicate FACTORY_HOME", func(r *Request, f *Facts) { r.EnvExtra = append(r.EnvExtra, KV{"FACTORY_HOME", "/w/u/factory"}) },
			"evaluator env_extra repeats a key (a dict cannot): FACTORY_HOME; refused"},
		{"live FACTORY_HOME", func(r *Request, f *Facts) { r.EnvExtra[0].Value = "/w/u/factory" }, "evaluator FACTORY_HOME /w/u/factory is not <skilleval_root>/trials/<id>/fh"},
		{"wrong HOME", func(r *Request, f *Facts) { r.EnvExtra[1].Value = "/w/u" }, "evaluator HOME must be /root/trials/t1/home"},
		{"shim missing", func(r *Request, f *Facts) { f.Eval.ShimOK = func(string) bool { return false } }, "evaluator PATH must start with /root/trials/t1/bin holding the claude shim (executable, owned by you)"},
		{"wrong cwd", func(r *Request, f *Facts) { r.Cwd = "/w/u" }, "evaluator cwd must be /root/trials/t1/work"},
		{"plugin outside vendor", func(r *Request, f *Facts) { r.PluginDirs = []string{"/etc"} }, "plugin dir /etc is outside /root/vendor"},
		{"plugin not prompt-only", func(r *Request, f *Facts) { f.Eval.PromptOnly = func(string) error { return fmt.Errorf("has x.py") } }, "plugin dir /root/vendor/p is not prompt-only: has x.py"},
		{"root unresolved", func(r *Request, f *Facts) { f.Eval.RootErr = "skilleval_root is not set" }, "skilleval_root is not set"},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			r, f := good()
			c.mut(&r, &f)
			p := Plan(r, f)
			if p.Refusal == nil || p.Refusal.Code != 3 || p.Refusal.Msg != c.text {
				t.Fatalf("got %+v, want exit 3 %q", p.Refusal, c.text)
			}
		})
	}
	// a non-evaluator may not carry the sandbox parameters
	req, f := coderReq(), baseFacts()
	req.EnvExtra = []KV{{"A", "b"}}
	if p := Plan(req, f); p.Refusal == nil || p.Refusal.Code != 3 ||
		p.Refusal.Msg != "env_extra, plugin_dirs and add_home_dir=False are for the evaluator only, not coder" {
		t.Errorf("coder with env_extra: %+v", p.Refusal)
	}
	req = coderReq()
	req.AddHomeDir = false
	if p := Plan(req, f); p.Refusal == nil || p.Refusal.Code != 3 {
		t.Errorf("coder without add_home_dir: %+v", p.Refusal)
	}
}

// TestPlanHostBackend: a pinned backend travels with the queued row and the requeue keeps the tick's failure count.
func TestPlanHostBackend(t *testing.T) {
	f := baseFacts()
	f.Gate.HerdrReachable = false
	f.Agent = func(n string) *AgentRow {
		return &AgentRow{Name: n, Status: "queued", SpawnArgs: []byte(`{"host_backend": "hc", "host_backend_pinned": true, "spawn_failures": 2, "spawn_failed_at": 1731.5, "other": 1}`)}
	}
	req := coderReq()
	req.FromQueue = true
	p := Plan(req, f)
	if p.Queue == nil {
		t.Fatalf("not queued: %+v", p.Refusal)
	}
	want := `{"role": "coder", "goal": "G-1", "team": "g-1-a", "task": "T-1", "name": "claude-coder-t-1", "cwd": null, "prompt": "do it", "model": null, "resume": null, "fresh": false, "org": "claude", "host_backend": "hc", "host_backend_pinned": true, "spawn_failures": 2, "spawn_failed_at": 1731.5}`
	if p.Queue.ArgsJSON != want {
		t.Errorf("queued args:\n got %s\nwant %s", p.Queue.ArgsJSON, want)
	}
	req.HostBackend = "herdr"
	p = Plan(req, f)
	if !strings.Contains(p.Queue.ArgsJSON, `"host_backend": "hc", "host_backend_pinned": true`) { // Python: the requeue copies the old row's keys over the new ones
		t.Errorf("a requeue keeps the queued row's backend: %s", p.Queue.ArgsJSON)
	}
	req.FromQueue, f.Agent = false, nil
	if p := Plan(req, f); !strings.Contains(p.Queue.ArgsJSON, `"host_backend": "herdr", "host_backend_pinned": true`) {
		t.Errorf("explicit backend pins: %s", p.Queue.ArgsJSON)
	}
}

// TestModelChoice: the model of every role in every org, the cleaner override, and never a Fable model.
func TestModelChoice(t *testing.T) {
	for _, org := range OrgNames {
		for _, role := range Roles {
			got := RoleModel(role, org, map[string]string{})
			if got == "" || strings.Contains(strings.ToLower(got), "fable") {
				t.Errorf("RoleModel(%s,%s) = %q", role, org, got)
			}
			f := baseFacts()
			f.Task = TaskFacts{}
			f.Config["org_prefix_default"] = "1"
			req := Request{Role: role, Org: org, Goal: "", AddHomeDir: true, SessionID: "s", Name: role + "-x"}
			if role == "researcher" {
				req.Task = "T-1"
				f.Task = TaskFacts{Exists: true, Spec: "s", Acceptance: "a", Role: "researcher"}
				f.ResearchDir = "/rs"
			}
			if role == "evaluator" {
				continue // needs the trial sandbox, covered by TestPlanEvaluator
			}
			p := Plan(req, f)
			got = RoleModel(role, p.Org, map[string]string{}) // retro is always the cursor org
			if p.Start != nil && strings.Contains(strings.ToLower(p.Start.Model), "fable") {
				t.Errorf("plan %s/%s chose %q", org, role, p.Start.Model)
			}
			if p.Start != nil && p.Start.Model != got {
				t.Errorf("plan %s/%s model %q, table %q", org, role, p.Start.Model, got)
			}
		}
	}
	if got := RoleModel("cleaner", "claude", map[string]string{"model_cleaner": " claude-opus-5-5 "}); got != "claude-opus-5-5" {
		t.Errorf("cleaner override: %q", got)
	}
	if got := RoleModel("coder", "claude", map[string]string{"model_coder": "x"}); got != "claude-sonnet-5-5" {
		t.Errorf("only cleaner has an override key: %q", got)
	}
	if got := RoleModel("coder", "cursor", map[string]string{}); got != "composer-2.5" {
		t.Errorf("cursor builder: %q", got)
	}
	// effort only for Opus models
	if a, _ := EffortArgs("high", "claude-opus-5-5"); strings.Join(a, " ") != "--effort high" {
		t.Errorf("opus effort: %v", a)
	}
	if a, _ := EffortArgs("high", "claude-sonnet-5-5"); a != nil {
		t.Errorf("sonnet effort: %v", a)
	}
}

// TestPurity: the package starts no process and imports no pane code (acceptance item 2, also asserted here).
func TestPurity(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"os/" + "exec", "exec" + ".Command", "internal/" + "herdr"} {
			if strings.Contains(string(b), bad) {
				t.Errorf("%s uses %s", e.Name(), bad)
			}
		}
	}
}

// TestPlanReviewFindings: the claimed-row recheck, the evaluator fail-closed facts and marker, the stale-pane refusal,
// the evaluator settings hand-off and the Fable guard.
func TestPlanReviewFindings(t *testing.T) {
	queued := &AgentRow{Name: "claude-coder-t-1", Status: "queued"}
	f := baseFacts()
	f.Agent = func(string) *AgentRow { return queued }
	f.Staleness = "CONFLICT-ONLY: a.go; spawn a merge-only coder (fix-loop, not an attempt)"
	req := coderReq()
	req.Caller, req.FromQueue = "", true
	if p := Plan(req, f); p.Bounce != f.Staleness || p.Start != nil || p.Queue != nil || len(p.Events) != 1 || p.Events[0].Action != "spawn-bounced" {
		t.Errorf("stale queued row: %+v", p)
	}
	f.Staleness = ""
	jr := Request{Role: "judge", Task: "T-1", AddHomeDir: true, FromQueue: true}
	f.Task.Role = "judge"
	f.JudgeHold = "T-1 PR is not ready"
	if p := Plan(jr, f); p.Requeue != "T-1 PR is not ready" || p.Start != nil {
		t.Errorf("held queued judge: %+v", p)
	}
	f.JudgeHold = ""
	if p := Plan(jr, f); p.Start == nil {
		t.Errorf("unheld queued judge must start: %+v", p.Refusal)
	}
	// stale pane
	f2 := baseFacts()
	f2.Agent = func(n string) *AgentRow { return &AgentRow{Name: n, Status: "done", PaneID: "p-9"} }
	f2.OldPaneNotClosed = "p-9"
	if p := Plan(coderReq(), f2); p.Refusal == nil || p.Refusal.Code != 1 || p.Refusal.Msg !=
		"claude-coder-t-1: its old pane p-9 could not be confirmed closed; not reusing the name. The row keeps the pane id (the exited-pane reaper retries it); retry the spawn later." {
		t.Errorf("stale pane: %+v", p.Refusal)
	}
	// Fable model
	f3 := baseFacts()
	r3 := coderReq()
	r3.Model = "fable-5-2"
	if p := Plan(r3, f3); p.Refusal == nil || p.Refusal.Code != 3 {
		t.Errorf("fable model accepted: %+v", p.Refusal)
	}
	f3.Config["model_cleaner"] = "claude-fable-5-2"
	if p := Plan(Request{Role: "cleaner", AddHomeDir: true, Name: "c"}, f3); p.Refusal == nil {
		t.Errorf("fable cleaner override accepted")
	}
	// evaluator: nil facts fail closed, refusal carries the marker, settings are the caller's
	trial := "/root/trials/t1"
	ev := func() (Request, Facts) {
		fe := baseFacts()
		fe.Eval = EvalFacts{Root: "/root", Realpath: func(s string) string { return s }, ShimOK: func(string) bool { return true }, PromptOnly: func(string) error { return nil }}
		return Request{Role: "evaluator", Cwd: trial + "/work", PluginDirs: []string{"/root/vendor/p"}, Name: "e1", SessionID: "s",
			EnvExtra: []KV{{"FACTORY_HOME", trial + "/fh"}, {"HOME", trial + "/home"}, {"CLAUDE_CONFIG_DIR", trial + "/cfg"}, {"PATH", trial + "/bin:/usr/bin"}}}, fe
	}
	r, fe := ev()
	if p := Plan(r, fe); p.Start == nil || !p.Start.EvaluatorSettings || p.Start.SettingsText != "" {
		t.Errorf("evaluator settings hand-off: %+v", p.Start)
	}
	r, fe = ev()
	fe.Eval.PromptOnly = nil
	if p := Plan(r, fe); p.Refusal == nil || p.Refusal.Reason != ReasonEvaluator {
		t.Errorf("nil PromptOnly: %+v", p.Refusal)
	}
	r, fe = ev()
	fe.Eval.Realpath = nil
	if p := Plan(r, fe); p.Refusal == nil || p.Refusal.Reason != ReasonEvaluator {
		t.Errorf("nil Realpath: %+v", p.Refusal)
	}
	r, fe = ev()
	r.Cwd = "/x"
	if p := Plan(r, fe); p.Refusal == nil || p.Refusal.Reason != ReasonEvaluator {
		t.Errorf("evaluator refusal lacks the marker: %+v", p.Refusal)
	}
}
