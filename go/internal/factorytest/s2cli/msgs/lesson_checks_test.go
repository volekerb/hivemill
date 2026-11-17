package s2msgs

import (
	"fmt"
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
)

// Port of tests/test_lesson_checks.py (14 cases).

func lessonChecksEnv(t *testing.T) *ft.Env {
	t.Helper()
	e := ft.New(t)
	e.HerdrStateful()
	sqlExec(t, e, "UPDATE config SET value='herdr' WHERE key='terminal_backend'")
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedTeam("g-001-a", "G-001", "zz-test-lead")
	e.SeedTask(ft.Task{ID: "T-0001", Status: "in_progress", Team: "g-001-a"})
	sqlExec(t, e, "UPDATE tasks SET spec='old spec', acceptance='old acc' WHERE id='T-0001'")
	sqlExec(t, e, "UPDATE tasks SET pr_url=NULL WHERE id='T-0001'")
	e.SeedAgent(ft.AgentRow{Name: "zz-test-lead", Role: "lead", Goal: "G-001", Team: "g-001-a"})
	e.SeedAgent(ft.AgentRow{Name: "zz-test-lead2", Role: "lead", Goal: "G-001", Team: "g-001-b"})
	e.SeedAgent(ft.AgentRow{Name: "zz-test-boss", Role: "boss"})
	e.SeedAgent(ft.AgentRow{Name: "zz-test-director", Role: "director"})
	return e
}

func taskCol(e *ft.Env, col string) string {
	rows := e.SQL("SELECT " + col + " FROM tasks WHERE id='T-0001'")
	return rows[0][0]
}

func TestLessonChecksGoalNewBossAndOwnerOK(t *testing.T) {
	e := lessonChecksEnv(t)
	e.OKWith(ft.Opts{Agent: "zz-test-boss"}, "goal", "new", "--title", "zz-test b")
	e.OK("goal", "new", "--title", "zz-test o")
	if e.Count("SELECT count(*) FROM goals") != 3 {
		t.Fatalf("goals = %d want 3", e.Count("SELECT count(*) FROM goals"))
	}
}

func TestLessonChecksGoalNewRefusedForEveryoneElse(t *testing.T) {
	e := lessonChecksEnv(t)
	roles := append([]string{"director", "lead"}, workers...)
	for i, role := range roles {
		name := fmt.Sprintf("zz-test-x%d", i)
		e.SeedAgent(ft.AgentRow{Name: name, Role: role})
		p := e.FailWith(ft.Opts{Agent: name}, 3, "goal", "new", "--title", "zz-test")
		if !strings.Contains(p.Stderr, "only the boss creates goals") {
			t.Fatalf("%s: %q", role, p.Stderr)
		}
	}
	if e.Count("SELECT count(*) FROM goals") != 1 {
		t.Fatal("extra goals")
	}
}

func TestLessonChecksSpecEditBlockedUnderLiveWorker(t *testing.T) {
	e := lessonChecksEnv(t)
	e.SeedAgent(ft.AgentRow{Name: "zz-test-coder", Role: "coder", Goal: "G-001", Task: "T-0001"})
	for _, pair := range [][2]string{{"--spec", "spec"}, {"--acceptance", "acceptance"}} {
		flag, col := pair[0], pair[1]
		p := e.FailWith(ft.Opts{Agent: "zz-test-lead"}, 2, "task", "update", "T-0001", flag, "new")
		if !strings.Contains(p.Stderr, "live worker zz-test-coder") || !strings.Contains(p.Stderr, "factory stop zz-test-coder") {
			t.Fatalf("stderr %q", p.Stderr)
		}
		e.Fail(2, "task", "update", "T-0001", flag, "new")
		want := "old spec"
		if col == "acceptance" {
			want = "old acc"
		}
		if got := taskCol(e, col); got != want {
			t.Fatalf("%s = %q want %q", col, got, want)
		}
	}
}

func TestLessonChecksOtherTaskFieldsOKUnderLiveWorker(t *testing.T) {
	e := lessonChecksEnv(t)
	e.SeedAgent(ft.AgentRow{Name: "zz-test-coder", Role: "coder", Goal: "G-001", Task: "T-0001"})
	e.OKWith(ft.Opts{Agent: "zz-test-lead"}, "task", "update", "T-0001", "--title", "zz-test renamed", "--note", "n")
}

func TestLessonChecksSpecEditOKAfterStopRequested(t *testing.T) {
	e := lessonChecksEnv(t)
	e.SeedAgent(ft.AgentRow{Name: "zz-test-coder", Role: "coder", Goal: "G-001", Task: "T-0001"})
	sqlExec(t, e, "UPDATE agents SET stop_requested_at='t' WHERE name='zz-test-coder'")
	e.OKWith(ft.Opts{Agent: "zz-test-lead"}, "task", "update", "T-0001", "--spec", "new")
	if taskCol(e, "spec") != "new" {
		t.Fatal("spec not updated")
	}
}

func TestLessonChecksSpecEditOKForQueuedDoneExitedAgent(t *testing.T) {
	e := lessonChecksEnv(t)
	for _, st := range []string{"queued", "done", "exited"} {
		e.SeedAgent(ft.AgentRow{Name: "zz-test-" + st, Role: "coder", Goal: "G-001", Task: "T-0001", Status: st})
	}
	e.OKWith(ft.Opts{Agent: "zz-test-lead"}, "task", "update", "T-0001", "--spec", "new", "--acceptance", "new acc")
	if taskCol(e, "acceptance") != "new acc" {
		t.Fatal("acceptance")
	}
}

func TestLessonChecksMsgSpecWarnsAndDelivers(t *testing.T) {
	e := lessonChecksEnv(t)
	e.SeedAgent(ft.AgentRow{Name: "zz-test-coder", Role: "coder", Goal: "G-001", Task: "T-0001"})
	p := e.OKWith(ft.Opts{Agent: "zz-test-lead"}, "msg", "zz-test-coder", "update", "new acceptance item: add X")
	contains(t, "stderr", p.Stderr, "zz-test-coder is running T-0001")
	contains(t, "stdout", p.Stdout, "M")
	ev := e.SQL("SELECT entity, entity_id, detail FROM events WHERE action='spec-by-msg'")
	if len(ev) != 1 || ev[0][0] != "task" || ev[0][1] != "T-0001" {
		t.Fatalf("events %v", ev)
	}
	contains(t, "detail", ev[0][2], "zz-test-lead->zz-test-coder")
	if e.Count("SELECT count(*) FROM messages WHERE recipient='zz-test-coder'") != 1 {
		t.Fatal("message")
	}
}

func TestLessonChecksMsgWithoutSpecWordsNoWarning(t *testing.T) {
	e := lessonChecksEnv(t)
	e.SeedAgent(ft.AgentRow{Name: "zz-test-coder", Role: "coder", Goal: "G-001", Task: "T-0001"})
	p := e.OKWith(ft.Opts{Agent: "zz-test-lead"}, "msg", "zz-test-coder", "status?", "how is it going")
	notContains(t, "stderr", p.Stderr, "warning")
	if len(e.SQL("SELECT * FROM events WHERE action='spec-by-msg'")) != 0 {
		t.Fatal("event")
	}
}

func TestLessonChecksMsgDecisionAndLeadToLeadNoWarning(t *testing.T) {
	e := lessonChecksEnv(t)
	e.SeedAgent(ft.AgentRow{Name: "zz-test-coder", Role: "coder", Goal: "G-001", Task: "T-0001"})
	p := e.OKWith(ft.Opts{Agent: "zz-test-lead"}, "msg", "zz-test-coder", "scope", "acceptance changed", "--kind", "decision")
	notContains(t, "stderr", p.Stderr, "warning")
	p = e.OKWith(ft.Opts{Agent: "zz-test-lead"}, "msg", "zz-test-lead2", "scope", "acceptance changed")
	notContains(t, "stderr", p.Stderr, "warning")
	if len(e.SQL("SELECT * FROM events WHERE action='spec-by-msg'")) != 0 {
		t.Fatal("events")
	}
}

func TestLessonChecksMsgToIdleWorkerWithoutTaskNoWarning(t *testing.T) {
	e := lessonChecksEnv(t)
	e.SeedAgent(ft.AgentRow{Name: "zz-test-queued", Role: "coder", Goal: "G-001", Task: "T-0001", Status: "queued"})
	p := e.OKWith(ft.Opts{Agent: "zz-test-lead"}, "msg", "zz-test-queued", "spec", "new spec")
	notContains(t, "stderr", p.Stderr, "warning")
}

func lcSpawn(e *ft.Env, role, task, name string) ft.Result {
	e.NeedPython()
	return as(e, "zz-test-director", "spawn", role, "--goal", "G-001", "--task", task, "--name", name)
}

func TestLessonChecksSpawnEmptySpecRefused(t *testing.T) {
	e := lessonChecksEnv(t)
	e.HerdrStateful()
	sqlExec(t, e, "UPDATE tasks SET spec='' WHERE id='T-0001'")
	p := lcSpawn(e, "coder", "T-0001", "zz-test-s")
	if p.Exit != 2 {
		t.Fatalf("exit %d stderr %q", p.Exit, p.Stderr)
	}
	contains(t, "stderr", p.Stderr, "has no spec in the DB")
	if len(e.HerdrStarts()) != 0 {
		t.Fatal("started agent")
	}
	if e.Count("SELECT count(*) FROM agents WHERE name='zz-test-s'") != 0 {
		t.Fatal("agent row")
	}
}

func TestLessonChecksSpawnEmptyAcceptanceRefusedForCoderAndQA(t *testing.T) {
	e := lessonChecksEnv(t)
	e.HerdrStateful()
	sqlExec(t, e, "UPDATE tasks SET acceptance=NULL WHERE id='T-0001'")
	for _, role := range []string{"coder", "qa"} {
		p := lcSpawn(e, role, "T-0001", "zz-test-s")
		if p.Exit != 2 {
			t.Fatalf("%s exit %d", role, p.Exit)
		}
		contains(t, "stderr", p.Stderr, "no acceptance")
	}
	if len(e.HerdrStarts()) != 0 {
		t.Fatal("starts")
	}
}

func TestLessonChecksSpawnPMWithoutAcceptanceOK(t *testing.T) {
	e := lessonChecksEnv(t)
	e.HerdrStateful()
	sqlExec(t, e, `INSERT INTO tasks(id,goal_id,team_id,title,role,status,pr_url,spec,acceptance,created,updated)
		VALUES('T-0002','G-001',NULL,'zz-test task','pm','todo',NULL,'zz-test spec',NULL,'t','t')`)
	p := lcSpawn(e, "pm", "T-0002", "zz-test-s")
	if p.Exit != 0 {
		t.Fatalf("exit %d stderr %q", p.Exit, p.Stderr)
	}
	if len(e.HerdrStarts()) != 1 {
		t.Fatalf("starts = %d", len(e.HerdrStarts()))
	}
}

func TestLessonChecksSpawnUnknownTaskRefused(t *testing.T) {
	e := lessonChecksEnv(t)
	e.HerdrStateful()
	p := lcSpawn(e, "coder", "T-9999", "zz-test-s")
	if p.Exit != 2 {
		t.Fatalf("exit %d", p.Exit)
	}
	if len(e.HerdrStarts()) != 0 {
		t.Fatal("started")
	}
}
