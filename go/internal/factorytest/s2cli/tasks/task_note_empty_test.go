package s2tasks

import (
	"testing"

	ft "factory/go/internal/factorytest"
)

// Port of tests/test_task_note_empty.py (5 of 5 cases). Complete.

func tneEnv(t *testing.T) *ft.Env {
	e := ft.New(t)
	e.SeedGoal("G-001", "", "zz-test goal")
	seedTask(e, ft.Task{Status: "todo"}, true)
	return e
}

func tneNotes(e *ft.Env) []string {
	return col(e, "SELECT detail FROM events WHERE entity='task' AND entity_id='T-0001' AND action='note' ORDER BY id")
}

func tneRefuse(t *testing.T, e *ft.Env, text, stdin string) {
	t.Helper()
	before := len(tneNotes(e))
	p := runO(e, ft.Opts{Stdin: stdin}, "task", "note", "T-0001", text)
	if p.Exit == 0 {
		t.Fatalf("task note %q succeeded: %s", text, p.Stdout)
	}
	contains(t, "stderr", p.Stderr, "empty")
	eq(t, "notes", len(tneNotes(e)), before)
}

func TestTaskNoteEmptyArgumentRefused(t *testing.T)           { tneRefuse(t, tneEnv(t), "", "") }
func TestTaskNoteEmptyWhitespaceArgumentRefused(t *testing.T) { tneRefuse(t, tneEnv(t), "  \n ", "") }
func TestTaskNoteEmptyStdinRefused(t *testing.T)              { tneRefuse(t, tneEnv(t), "-", "") }
func TestTaskNoteEmptyWhitespaceStdinRefused(t *testing.T)    { tneRefuse(t, tneEnv(t), "-", "\n  \n") }

func TestTaskNoteEmptyRealNoteStillWorks(t *testing.T) {
	e := tneEnv(t)
	ok(e, "", "task", "note", "T-0001", "inline note")
	okS(e, "", "stdin note\n", "task", "note", "T-0001", "-")
	n := tneNotes(e)
	eqS(t, "last two notes", n[len(n)-2:], []string{"inline note", "stdin note"})
}
