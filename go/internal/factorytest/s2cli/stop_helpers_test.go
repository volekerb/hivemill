package s2cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	ft "factory/go/internal/factorytest"
)

// Port of tests/test_stop.py (62 of 63 cases; see ported_s2cli.txt). Stateful herdr (Env.HerdrStateful, SetHerdr),
// fake /proc via Env.FakeProc/FakeClaude, harness time via Env.Now (FACTORY_TEST_NOW). stop, tick, spawn, dequeue
// and reap-agent still run in Python through the Go CLI: those tests call NeedPython at the start of the test.

const (
	stopSID  = "sess-zz-coder"
	stopOLD  = "2026-01-01T00:00:00+00:00"
	stopRev  = "sess-zz-rev"
	stopFast = "0.2"
)

var (
	stopSlots = make(chan struct{}, 3)
	stopPyHdr = `import contextlib, importlib.util, io, os, sys, json, argparse, sqlite3
from pathlib import Path
from unittest import mock
repo = Path(os.environ["FACTORY_REPO_ROOT"])
spec = importlib.util.spec_from_file_location("factory_under_test", repo / "bin" / "factory_cli.py")
mod = importlib.util.module_from_spec(spec)
with contextlib.redirect_stdout(io.StringIO()):
    spec.loader.exec_module(mod)
`
)

type stopEnv struct {
	*ft.Env
}

func newStopEnv(t *testing.T) *stopEnv {
	t.Helper()
	t.Parallel()
	stopSlots <- struct{}{}
	t.Cleanup(func() { <-stopSlots })
	e := ft.New(t)
	e.HerdrStateful()
	sqlExec(t, e, "UPDATE config SET value='herdr' WHERE key='terminal_backend'")
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedTeam("g-001-a", "G-001", "zz-lead")
	e.SeedTeam("g-001-b", "G-001", "zz-lead-b")
	e.SeedTask(ft.Task{ID: "T-0001", Goal: "G-001", Role: "coder", Status: "in_progress", Team: "g-001-a"})
	e.SeedAgent(ft.AgentRow{Name: "zz-director", Role: "director", Goal: "G-001"})
	e.SeedAgent(ft.AgentRow{Name: "zz-boss", Role: "boss"})
	e.SeedAgent(ft.AgentRow{Name: "zz-lead", Role: "lead", Goal: "G-001", Team: "g-001-a"})
	e.SeedAgent(ft.AgentRow{Name: "zz-lead-b", Role: "lead", Goal: "G-001", Team: "g-001-b"})
	started := stopAgoWall(3600)
	e.SeedAgent(ft.AgentRow{Name: "zz-coder", Role: "coder", Goal: "G-001", Team: "g-001-a", Task: "T-0001", PaneID: "p1", Started: started})
	e.SeedAgent(ft.AgentRow{Name: "zz-coder-b", Role: "coder", Goal: "G-001", Team: "g-001-b", PaneID: "p2", Started: started})
	sqlExec(t, e, "UPDATE agents SET session_id=? WHERE name='zz-coder'", stopSID)
	s := &stopEnv{e}
	s.stopHerdrAgents(t, [][2]string{{"zz-coder", "p1"}, {"zz-coder-b", "p2"}})
	return s
}

func newStopEscalationEnv(t *testing.T) *stopEnv {
	t.Helper()
	s := newStopEnv(t)
	s.NeedPython()
	s.stop(t, []string{"zz-coder"}, "zz-lead", 0, nil)
	return s
}

func newReapAgentEnv(t *testing.T) *stopEnv {
	t.Helper()
	s := newStopEnv(t)
	s.SeedAgent(ft.AgentRow{Name: "zz-rev", Role: "reviewer", Goal: "G-001", Team: "g-001-a", PaneID: "p3", Started: stopAgoWall(3600)})
	sqlExec(t, s.Env, "UPDATE agents SET session_id=? WHERE name='zz-rev'", stopRev)
	s.stopHerdrAgents(t, [][2]string{{"zz-lead", "p3"}, {"zz-coder", "p1"}})
	s.FakeClaude(4300, 1, "claude", "--resume", stopRev)
	s.stopSetEnviron(t, 4300, "zz-lead")
	return s
}

// stopAgoWall matches tests/test_stop.py ago(): wall clock, because the tick stop fallback uses datetime.now().
func stopAgoWall(seconds int) string {
	return time.Now().Add(-time.Duration(seconds) * time.Second).Format("2006-01-02T15:04:05-07:00")
}

func stopUnsetTestNow(extra map[string]string) map[string]string {
	if extra == nil {
		extra = map[string]string{}
	}
	extra["FACTORY_TEST_NOW"] = ""
	return extra
}

func (s *stopEnv) stopHerdrAgents(t *testing.T, specs [][2]string) {
	t.Helper()
	panes := make([]map[string]string, 0, len(specs))
	agents := make([]map[string]any, 0, len(specs))
	for _, sp := range specs {
		n, p := sp[0], sp[1]
		panes = append(panes, map[string]string{"pane_id": p, "tab_id": "t1", "workspace_id": "w1"})
		agents = append(agents, map[string]any{
			"name": n, "agent": "claude", "pane_id": p, "agent_status": "working",
		})
	}
	s.SetHerdr(map[string]any{
		"workspaces": []map[string]string{{"workspace_id": "w1", "label": "w"}},
		"panes":      panes,
		"agents":     agents,
	})
}

func (s *stopEnv) stopRow(t *testing.T, name string) map[string]string {
	t.Helper()
	rows := s.SQL("SELECT * FROM agents WHERE name=?", name)
	if len(rows) != 1 {
		t.Fatalf("agent %s: %v", name, rows)
	}
	cols := s.SQL("PRAGMA table_info(agents)")
	m := map[string]string{}
	for i, c := range cols {
		if i < len(rows[0]) {
			m[c[1]] = rows[0][i]
		}
	}
	return m
}

func (s *stopEnv) stopField(t *testing.T, name, col string) string {
	t.Helper()
	rows := s.SQL("SELECT "+col+" FROM agents WHERE name=?", name)
	if len(rows) != 1 {
		t.Fatalf("agent %s col %s: %v", name, col, rows)
	}
	return rows[0][0]
}

func (s *stopEnv) stopHerdrVerbs() [][2]string {
	var out [][2]string
	for _, c := range s.Calls("herdr") {
		if len(c.Argv) >= 2 {
			out = append(out, [2]string{c.Argv[0], c.Argv[1]})
		}
	}
	return out
}

func (s *stopEnv) stopHerdrArgvs() [][]string {
	var out [][]string
	for _, c := range s.Calls("herdr") {
		out = append(out, c.Argv)
	}
	return out
}

func (s *stopEnv) stopEvents(action, name string) [][]string {
	return s.SQL("SELECT * FROM events WHERE action=? AND entity_id=?", action, name)
}

func (s *stopEnv) stopMsgs(rcpt, like string) [][]string {
	return s.SQL("SELECT * FROM messages WHERE recipient=? AND subject LIKE ?", rcpt, like)
}

func (s *stopEnv) stop(t *testing.T, args []string, agent string, code int, extra map[string]string) ft.Result {
	t.Helper()
	s.NeedPython()
	all := append([]string{"stop"}, args...)
	r := s.RunWith(ft.Opts{Agent: agent, Env: stopUnsetTestNow(extra)}, all...)
	if r.Exit != code {
		t.Fatalf("stop %v as %s: exit %d, want %d\nstdout: %s\nstderr: %s", args, agent, r.Exit, code, r.Stdout, r.Stderr)
	}
	return r
}

func (s *stopEnv) stopOK(t *testing.T, agent string, args ...string) ft.Result {
	t.Helper()
	s.NeedPython()
	return s.OKWith(ft.Opts{Agent: agent, Env: stopUnsetTestNow(nil)}, args...)
}

func (s *stopEnv) stopRefused(t *testing.T, code int, agent string, args ...string) ft.Result {
	t.Helper()
	s.NeedPython()
	return s.FailWith(ft.Opts{Agent: agent, Env: stopUnsetTestNow(nil)}, code, args...)
}

func (s *stopEnv) stopTick(t *testing.T, extra map[string]string) ft.Result {
	t.Helper()
	s.NeedPython()
	r := s.RunWith(ft.Opts{Agent: "zz-director", Env: stopUnsetTestNow(extra)}, "tick")
	if r.Exit != 0 {
		t.Fatalf("tick: exit %d\nstdout: %s\nstderr: %s", r.Exit, r.Stdout, r.Stderr)
	}
	return r
}

func (s *stopEnv) stopAgeStop(t *testing.T, seconds int, name string) {
	t.Helper()
	sqlExec(t, s.Env, "UPDATE agents SET stop_requested_at=? WHERE name=?", stopAgoWall(seconds), name)
}

func (s *stopEnv) stopFastEnv() map[string]string {
	return map[string]string{"FACTORY_STOP_CONFIRM_S": stopFast}
}

func (s *stopEnv) stopRemoveProc(t *testing.T, pid int) {
	t.Helper()
	if err := os.RemoveAll(filepath.Join(s.ProcDir, strconv.Itoa(pid))); err != nil {
		t.Fatal(err)
	}
}

func (s *stopEnv) stopSetEnviron(t *testing.T, pid int, agent string) {
	t.Helper()
	d := filepath.Join(s.ProcDir, strconv.Itoa(pid))
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("PATH=/usr/bin\000FACTORY_AGENT=" + agent + "\000HOME=/x\000")
	if err := os.WriteFile(filepath.Join(d, "environ"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func stopEventDetail(t *testing.T, detail string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(detail), &m); err != nil {
		t.Fatalf("event detail JSON: %v", err)
	}
	return m
}

func stopHasVerb(verbs [][2]string, want [2]string) bool {
	for _, v := range verbs {
		if v == want {
			return true
		}
	}
	return false
}

func stopIndexArgvs(argvs [][]string, prefix ...string) int {
	for i, c := range argvs {
		if len(c) < len(prefix) {
			continue
		}
		ok := true
		for j, p := range prefix {
			if c[j] != p {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

func stopWaitFlags(c []string) []string {
	var flags []string
	for _, a := range c {
		if strings.HasPrefix(a, "--") {
			flags = append(flags, a)
		}
	}
	return flags
}

func stopColBool(t *testing.T, e *ft.Env, cols ...string) bool {
	t.Helper()
	names := map[string]bool{}
	for _, row := range e.SQL("PRAGMA table_info(agents)") {
		names[row[1]] = true
	}
	for _, c := range cols {
		if !names[c] {
			return false
		}
	}
	return true
}

var stopForceRe = regexp.MustCompile(`force-stopped after \d+s`)

func (s *stopEnv) stopPython(t *testing.T, body string, agentRole map[string]string) (stdout string, exit int) {
	t.Helper()
	s.NeedPython()
	repo, err := ft.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(s.Root, "stop_inproc.py")
	code := stopPyHdr + body
	if err := os.WriteFile(script, []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"FACTORY_REPO_ROOT": repo}
	for k, v := range agentRole {
		env[k] = v
	}
	cmd := exec.Command("python3", script)
	cmd.Dir = s.Home
	cmd.Env = s.Environ("", stopUnsetTestNow(env))
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	err = cmd.Run()
	exit = 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			t.Fatalf("stop python: %v\n%s", err, se.String())
		}
	}
	if exit != 0 {
		t.Logf("stop python stdout: %s", so.String())
		t.Logf("stop python stderr: %s", se.String())
	}
	return so.String() + se.String(), exit
}

func stopAttemptSeenPy(t *testing.T, s *stopEnv) string {
	t.Helper()
	seen := s.stopField(t, "zz-coder", "stop_attempt_at")
	if seen == "" || seen == "NULL" {
		return "None"
	}
	return strconv.Quote(seen)
}

func (s *stopEnv) stopHookDone(t *testing.T) {
	t.Helper()
	hook := fmt.Sprintf("python3 -c \"import sqlite3; c=sqlite3.connect('%s'); c.execute(\\\"UPDATE agents SET status='done' WHERE name='zz-coder'\\\"); c.commit()\"", s.DBPath)
	s.HerdrHook(hook, "")
}

func stopDueAttempt(attemptAt string) string {
	tm, err := time.Parse("2006-01-02T15:04:05-07:00", attemptAt)
	if err != nil {
		tm, err = time.Parse(time.RFC3339, strings.Replace(attemptAt, "+00:00", "Z", 1))
		if err != nil {
			panic(err)
		}
	}
	return tm.Add(300 * time.Second).Format("2006-01-02T15:04:05-07:00")
}
