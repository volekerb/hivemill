package s2cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
)

// Port of tests/test_resume.py (25 cases; see ported_s2cli.txt). Stateful herdr (Env.HerdrStateful, SetHerdr),
// fake /proc via Env.FakeProc (FACTORY_PROC_DIR), harness time via Env.Now. up, relaunch and spawn still run in
// Python: those tests call NeedPython. Owner tick is driven by the Go CLI with Python units; non-owner tick delegates
// entirely to Python (none of these cases use that path).

var resumeSlots = make(chan struct{}, 3)

type resumeEnv struct {
	*ft.Env
}

func newResumeEnv(t *testing.T) *resumeEnv {
	t.Helper()
	t.Parallel()
	resumeSlots <- struct{}{}
	t.Cleanup(func() { <-resumeSlots })
	e := ft.New(t)
	e.HerdrStateful()
	sqlExec(t, e, "UPDATE config SET value='herdr' WHERE key='terminal_backend'")
	copyRolesTree(t, e)
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedTeam("g-001-a", "G-001", "zz-test-lead")
	for _, row := range []struct {
		name, role, sess, team, goal string
	}{
		{"zz-test-director", "director", "sess-dir", "", ""},
		{"zz-test-boss", "boss", "sess-boss", "", ""},
		{"zz-test-lead", "lead", "sess-lead", "g-001-a", "G-001"},
	} {
		a := ft.AgentRow{Name: row.name, Role: row.role, Status: "lost"}
		if row.goal != "" {
			a.Goal = row.goal
		}
		if row.team != "" {
			a.Team = row.team
		}
		e.SeedAgent(a)
		sqlExec(t, e, "UPDATE agents SET session_id=? WHERE name=?", row.sess, row.name)
	}
	return &resumeEnv{e}
}

func newResumeStillLiveEnv(t *testing.T) *resumeEnv {
	t.Helper()
	r := newResumeEnv(t)
	sqlExec(t, r.Env, "UPDATE agents SET status='live', pane_id='px0', started='2026-01-01T00:00:00+00:00' WHERE name='zz-test-director'")
	r.resumeSetAgents(t, [][2]string{{"zz-test-director", "sess-dir"}})
	r.resumeFakeProcBare(t, 101, "sess-dir")
	return r
}

func newResumeRelaunchEnv(t *testing.T) *resumeEnv {
	t.Helper()
	r := newResumeEnv(t)
	sqlExec(t, r.Env, "UPDATE agents SET status='live', pane_id='px2' WHERE name='zz-test-lead'")
	r.resumeSetAgents(t, [][2]string{
		{"zz-test-director", "sess-dir"},
		{"zz-test-boss", "sess-boss"},
		{"zz-test-lead", "sess-lead"},
	})
	r.SetHerdr(map[string]any{
		"workspaces": []map[string]string{{"workspace_id": "w1", "label": "x"}},
		"tabs":       []map[string]any{{"tab_id": "t1", "workspace_id": "w1", "label": "hq", "pane_count": 1}},
		"panes":      []map[string]string{{"pane_id": "px2", "tab_id": "t1", "workspace_id": "w1"}},
	})
	return r
}

func (r *resumeEnv) resumeSettings(role string) string {
	return filepath.Join(r.Home, "run", "settings", role+".json")
}

func (r *resumeEnv) resumeFakeProcBare(t *testing.T, pid int, sess string, extra ...string) {
	t.Helper()
	argv := append([]string{"claude", "--resume", sess}, extra...)
	r.FakeProc(pid, "claude", 1, argv...)
}

func (r *resumeEnv) resumeFakeProcRole(t *testing.T, pid int, sess, role string, extra ...string) {
	t.Helper()
	argv := []string{"claude", "--resume", sess}
	argv = append(argv, extra...)
	argv = append(argv, "--model", "m", "--permission-mode", "auto", "--settings", r.resumeSettings(role))
	r.FakeProc(pid, "claude", 1, argv...)
}

func (r *resumeEnv) resumeSetAgents(t *testing.T, pairs [][2]string) {
	t.Helper()
	agents := make([]map[string]any, len(pairs))
	for i, p := range pairs {
		agents[i] = map[string]any{
			"name": p[0], "agent": "claude", "pane_id": "px" + strconv.Itoa(i),
			"agent_status": "idle", "agent_session": map[string]string{"value": p[1]},
		}
	}
	r.SetHerdr(map[string]any{"agents": agents})
}

func resumeStartsMap(e *resumeEnv) map[string][]string {
	m := map[string][]string{}
	for _, a := range e.HerdrStarts() {
		if len(a) >= 3 {
			m[a[2]] = a
		}
	}
	return m
}

func (r *resumeEnv) resumeAgentStatus(t *testing.T, name string) string {
	t.Helper()
	rows := r.SQL("SELECT status FROM agents WHERE name=?", name)
	if len(rows) != 1 {
		t.Fatalf("agent %s: %v", name, rows)
	}
	return rows[0][0]
}

func (r *resumeEnv) resumeFlagless(t *testing.T, name string) (flagless, since string) {
	t.Helper()
	rows := r.SQL("SELECT flagless, flagless_since FROM agents WHERE name=?", name)
	if len(rows) != 1 {
		t.Fatalf("agent %s: %v", name, rows)
	}
	return rows[0][0], rows[0][1]
}

func resumeFlaglessMsgs(e *resumeEnv) [][]string {
	return e.SQL("SELECT recipient, body FROM messages WHERE subject LIKE '%flagless%'")
}

func resumeHerdrArgv2(e *resumeEnv) [][2]string {
	var out [][2]string
	for _, c := range e.Calls("herdr") {
		if len(c.Argv) >= 2 {
			out = append(out, [2]string{c.Argv[0], c.Argv[1]})
		}
	}
	return out
}

func resumeHerdrCalls(e *resumeEnv) [][]string {
	var out [][]string
	for _, c := range e.Calls("herdr") {
		out = append(out, c.Argv)
	}
	return out
}

func resumeMutatingHerdr(e *resumeEnv) [][2]string {
	var out [][2]string
	for _, c := range e.Calls("herdr") {
		if len(c.Argv) < 2 {
			continue
		}
		pair := [2]string{c.Argv[0], c.Argv[1]}
		if pair == [2]string{"pane", "close"} || pair == [2]string{"agent", "start"} || pair == [2]string{"pane", "run"} {
			out = append(out, pair)
		}
	}
	return out
}

func resumePrompts(e *resumeEnv) [][]string {
	var out [][]string
	for _, c := range e.Calls("herdr") {
		if len(c.Argv) >= 2 && c.Argv[0] == "agent" && c.Argv[1] == "prompt" {
			out = append(out, c.Argv)
		}
	}
	return out
}

func resumeIndexAfter(argv []string, flag string) int {
	for i, a := range argv {
		if a == flag {
			return i
		}
	}
	return -1
}

func (r *resumeEnv) resumePaneEnv() map[string]string {
	return map[string]string{"HERDR_PANE_ID": "px0"}
}

func (r *resumeEnv) resumeDetect(t *testing.T) {
	t.Helper()
	r.OK("tick")
	if fl, _ := r.resumeFlagless(t, "zz-test-director"); fl != "1" {
		t.Fatalf("flagless = %s, want 1", fl)
	}
	if st := r.resumeAgentStatus(t, "zz-test-director"); st != "live" {
		t.Fatalf("status = %s, want live", st)
	}
}

func (r *resumeEnv) resumeFlaglessPM(t *testing.T) {
	t.Helper()
	r.SeedAgent(ft.AgentRow{Name: "zz-test-pm", Role: "pm", Goal: "G-001", Team: "g-001-a", Status: "lost"})
	sqlExec(t, r.Env, "UPDATE agents SET flagless=1, flagless_since='2026-10-23T10:00:00+00:00' WHERE name='zz-test-pm'")
}

func (r *resumeEnv) resumeSpawnPM(t *testing.T) ft.Result {
	t.Helper()
	r.NeedPython()
	return r.RunWith(ft.Opts{Agent: "zz-test-director"}, "spawn", "pm", "--name", "zz-test-pm", "--goal", "G-001", "--team", "g-001-a")
}

func resumeAgentCols(t *testing.T, e *ft.Env) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, row := range e.SQL("PRAGMA table_info(agents)") {
		out[row[1]] = true
	}
	return out
}

func resumeEventDetail(t *testing.T, detail string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(detail), &m); err != nil {
		t.Fatalf("event detail JSON: %v", err)
	}
	return m
}

func resumeRemoveFakeProc(t *testing.T, e *ft.Env, pid int) {
	t.Helper()
	if err := os.RemoveAll(filepath.Join(e.ProcDir, strconv.Itoa(pid))); err != nil {
		t.Fatal(err)
	}
}

func resumeJoinLines(lines []string, sep string) string {
	return strings.Join(lines, sep)
}
