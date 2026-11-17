package s2tasks

import (
	"os"
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
)

// Port of tests/test_worker_escapes.py (6 of 15 cases; PARTIAL for P7a, T-1495).
// T-0036: close the worker -> live-herdr escapes found in review of PR #23 (E2, plus nits 2 and 3).
//
// not ported (9): the cases call Python functions in-process through harness load_factory() (herdr(), refuse_worker(),
// worker_stub_exempt(), herdr_socket(), caller_is_worker(), proc_root(), _serve_proc_root(), _ancestry_worker(), with
// module globals such as LIVE_HOMES and HOME patched). They cannot be driven through the CLI: the live-home
// predicate comes from the passwd entry of the user, which a test cannot move. The Go counterparts of these
// functions are go/internal/herdr and go/internal/identity, which have their own Go tests there.
//   WorkerEscapeTests.test_herdr_gate_stub_in_live_home_refused, test_stub_exempt_requires_temp_home,
//   test_herdr_socket_dead_for_worker_without_db, test_hq_full_flag_shape_not_worker;
//   ProcDirLiveHomeTests (all 5): test_temp_home_honours_fake_proc_dir, test_live_home_ignores_fake_proc_dir_and_records_one_event,
//   test_serve_proc_dir_honoured_off_live_home, test_live_home_serve_proc_dir_ignored_and_one_event,
//   test_live_home_forged_hq_ancestry_still_a_worker.
// The Python herdr stub log (herdr_argvs) is the herdr plus hc stub log here: the Go harness leaves the terminal
// backend on auto, which prefers hc.

const (
	weWorkerSettings = "/srv/x/factory/roles/settings/coder.json"
	weNoProc         = "/proc/nonexistent"
)

var weWorkerRoles = []string{"pm", "coder", "qa", "reviewer", "judge", "cleaner", "staff-engineer"}

// weEnv is WorkerGateBase.setUp.
func weEnv(t *testing.T) *ft.Env {
	e := ft.New(t)
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedTeam("g-001-a", "G-001", "zz-test-lead")
	e.SeedTask(ft.Task{ID: "T-0001", Role: "coder", Status: "in_progress", Team: "g-001-a"})
	for _, a := range [][2]string{{"zz-test-lead", "lead"}, {"zz-test-director", "director"}, {"zz-test-boss", "boss"}} {
		e.SeedAgent(ft.AgentRow{Name: a[0], Role: a[1], Goal: "G-001", Team: "g-001-a"})
	}
	for _, r := range weWorkerRoles {
		e.SeedAgent(ft.AgentRow{Name: "zz-test-" + r, Role: r, Goal: "G-001", Team: "g-001-a", Task: "T-0001"})
	}
	return e
}

// weTerminalCalls is every call of a terminal stub (herdr and hc): none may happen when the worker gate holds.
func weTerminalCalls(e *ft.Env) int { return len(e.Calls("herdr")) + len(e.Calls("hc")) }

// weWorkerAncestry makes the process that starts the CLI a descendant of a worker claude.
func weWorkerAncestry(e *ft.Env) {
	e.FakeProc(os.Getpid(), "x", 1, "claude", "--settings", weWorkerSettings, "--resume", "s")
}

func weSnapshot(e *ft.Env) string {
	var parts []string
	for _, tb := range []string{"agents", "events", "messages", "teams"} {
		for _, r := range sql(e, "SELECT * FROM "+tb) {
			parts = append(parts, tb+":"+strings.Join(r, "|"))
		}
	}
	return strings.Join(parts, "\n")
}

func TestWorkerEscapesTeamNewRefusedForWorkerSpoofingDirector(t *testing.T) {
	e := weEnv(t)
	e.NeedPython() // the worker refusal of team new is Python
	weWorkerAncestry(e)
	before := weSnapshot(e)
	p := refused(e, 3, "zz-test-director", "team", "new", "--goal", "G-001", "--scope", "x")
	contains(t, "stderr", p.Stderr, "workers may not run `factory team new`")
	refusedO(e, 3, ft.Opts{Agent: "zz-test-director", Env: map[string]string{"FACTORY_PROC_DIR": weNoProc, "FACTORY_ROLE": "coder"}},
		"team", "new", "--goal", "G-001", "--scope", "x")
	eq(t, "snapshot", weSnapshot(e), before)
	eq(t, "terminal calls", weTerminalCalls(e), 0)
}

func TestWorkerEscapesTeamCloseRefusedForWorker(t *testing.T) {
	e := weEnv(t)
	e.NeedPython()
	weWorkerAncestry(e)
	before := weSnapshot(e)
	refused(e, 3, "zz-test-director", "team", "close", "g-001-a")
	refusedO(e, 3, ft.Opts{Agent: "zz-test-coder", Env: map[string]string{"FACTORY_ROLE": "coder"}}, "team", "close", "g-001-a")
	eq(t, "snapshot", weSnapshot(e), before)
}

func TestWorkerEscapesTeamNewAndCloseStillWorkForDirector(t *testing.T) {
	e := weEnv(t)
	e.NeedPython()
	ok(e, "zz-test-director", "team", "new", "--goal", "G-001", "--scope", "x")
	if weTerminalCalls(e) == 0 {
		t.Fatal("team new made no terminal (herdr/hc) call")
	}
	ok(e, "zz-test-director", "team", "close", "g-001-b")
	e.OKWith(ft.Opts{Agent: "zz-test-coder", Env: map[string]string{"FACTORY_ROLE": "coder"}}, "team", "list")
}

func TestWorkerEscapesMsgToStaffEngineerFromWorkerNoInprocessSpawn(t *testing.T) {
	e := weEnv(t)
	e.NeedPython()
	exec(e, "DELETE FROM agents WHERE role='staff-engineer'") // none live: a --kind request would spawn it
	weWorkerAncestry(e)
	before := weSnapshot(e)
	for _, c := range []struct {
		who string
		env map[string]string
	}{
		{"zz-test-director", nil},
		{"zz-test-lead", nil},
		{"zz-test-coder", map[string]string{"FACTORY_PROC_DIR": weNoProc, "FACTORY_ROLE": "coder"}},
	} {
		p := refusedO(e, 3, ft.Opts{Agent: c.who, Env: c.env}, "msg", "staff-engineer", "q", "body")
		contains(t, c.who+" stderr", p.Stderr, "may not message staff-engineer")
	}
	eq(t, "snapshot", weSnapshot(e), before)
	eq(t, "terminal calls", weTerminalCalls(e), 0)
}

func TestWorkerEscapesMsgToStaffEngineerFromLeadStillSpawns(t *testing.T) {
	e := weEnv(t)
	e.NeedPython()
	exec(e, "DELETE FROM agents WHERE role='staff-engineer'")
	p := ok(e, "zz-test-lead", "msg", "staff-engineer", "q", "body", "--kind", "request")
	contains(t, "stdout", p.Stdout, "→ staff-engineer")
	if weTerminalCalls(e) == 0 {
		t.Fatal("the lead's request spawned nothing (no herdr/hc call)")
	}
}

func TestWorkerEscapesEveryWorkerRoleRefusedTeamNewByEnvAlone(t *testing.T) {
	e := weEnv(t)
	e.NeedPython()
	for _, role := range weWorkerRoles {
		t.Run(role, func(t *testing.T) {
			refusedO(e, 3, ft.Opts{Agent: "zz-test-director", Env: map[string]string{"FACTORY_ROLE": role}},
				"team", "new", "--goal", "G-001", "--scope", "x")
		})
	}
}
