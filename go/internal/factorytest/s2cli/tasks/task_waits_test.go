package s2tasks

import (
	"regexp"
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
)

// Port of tests/test_task_waits.py (8 of 9 cases; PARTIAL for P7a, T-1495).
// not ported: MigrateWaitsTests.test_old_db_gets_waits_column_default_internal - it asserts the Python db() migrate()
// adds the tasks.waits column to an old database; the Go CLI opens the database read-only and leaves migration to
// Python (go/internal/cli/cmd_res.go:1008), so there is no Go counterpart to drive.
// T-0370: tasks record who a blocked task waits on (owner | internal); task show/list, res and digest show it.
// The Python FROZEN clock is 2026-10-27 12:00 local; here it is 12:00 UTC with TZ=UTC so the digest date matches.

func twEnv(t *testing.T) *ft.Env {
	e := ft.New(t)
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedTeam("g-001-a", "G-001", "zz-test-lead")
	for _, id := range []string{"T-0001", "T-0002", "T-0003"} {
		seedTask(e, ft.Task{ID: id, Status: "in_progress", Team: "g-001-a"}, true)
	}
	return e
}

func twWaits(e *ft.Env, id string) string { return val(e, "SELECT waits FROM tasks WHERE id=?", id) }

func TestTaskWaitsDefaultIsInternalAndExplicitOwnerSets(t *testing.T) {
	e := twEnv(t)
	ok(e, "", "task", "update", "T-0001", "--status", "blocked", "--reason", "queue full")
	ok(e, "", "task", "update", "T-0002", "--status", "blocked", "--reason", "needs owner key", "--waits", "owner")
	eq(t, "T-0001 waits", twWaits(e, "T-0001"), "internal")
	eq(t, "T-0002 waits", twWaits(e, "T-0002"), "owner")
	ev := val(e, "SELECT detail FROM events WHERE entity_id='T-0002' AND action='update' ORDER BY id")
	contains(t, "event", ev, "waits=owner")
	contains(t, "event", ev, "needs owner key")
}

func TestTaskWaitsShowAndListPrintWaitsForBlockedOnly(t *testing.T) {
	e := twEnv(t)
	ok(e, "", "task", "update", "T-0002", "--status", "blocked", "--waits", "owner")
	contains(t, "show T-0002", ok(e, "", "task", "show", "T-0002").Stdout, "waits: owner")
	notContains(t, "show T-0001", ok(e, "", "task", "show", "T-0001").Stdout, "waits:")
	lines := strings.Split(strings.TrimRight(ok(e, "", "task", "list").Stdout, "\n"), "\n")
	rows := map[string]string{}
	for _, l := range lines[1:] {
		if f := strings.Fields(l); len(f) > 0 {
			rows[f[0]] = l
		}
	}
	if !regexp.MustCompile(`blocked\s+owner`).MatchString(rows["T-0002"]) {
		t.Fatalf("T-0002 row %q lacks blocked + owner", rows["T-0002"])
	}
	notContains(t, "T-0001 row", rows["T-0001"], "owner")
	contains(t, "list header", lines[0], "WAITS")
}

func TestTaskWaitsClearingWaitsByLeavingBlockedOrFlipping(t *testing.T) {
	e := twEnv(t)
	ok(e, "", "task", "update", "T-0002", "--status", "blocked", "--waits", "owner")
	ok(e, "", "task", "update", "T-0002", "--waits", "internal") // explicit flip while blocked
	eq(t, "after flip", twWaits(e, "T-0002"), "internal")
	ok(e, "", "task", "update", "T-0002", "--waits", "owner")
	ok(e, "", "task", "update", "T-0002", "--status", "in_progress") // unblock resets
	eq(t, "after unblock", twWaits(e, "T-0002"), "internal")
	ok(e, "", "task", "update", "T-0002", "--status", "blocked") // re-block without --waits: internal
	eq(t, "after re-block", twWaits(e, "T-0002"), "internal")
}

func TestTaskWaitsReblockWithoutWaitsKeepsExistingValue(t *testing.T) {
	e := twEnv(t)
	ok(e, "", "task", "update", "T-0002", "--status", "blocked", "--waits", "owner")
	ok(e, "", "task", "update", "T-0002", "--status", "blocked", "--reason", "still waiting")
	eq(t, "waits", twWaits(e, "T-0002"), "owner")
}

func TestTaskWaitsRefusedWhenNotBlockedOrBadValue(t *testing.T) {
	e := twEnv(t)
	e.NeedPython() // this form (bad --waits value / res) is handed to Python
	p := refused(e, 2, "", "task", "update", "T-0001", "--waits", "owner")
	contains(t, "stderr", p.Stderr, "--status blocked")
	eq(t, "waits", twWaits(e, "T-0001"), "internal")
	refused(e, 2, "", "task", "update", "T-0001", "--status", "blocked", "--waits", "nobody")
	eq(t, "status", val(e, "SELECT status FROM tasks WHERE id='T-0001'"), "in_progress")
}

func TestTaskWaitsResCountsOwnerVsInternal(t *testing.T) {
	e := twEnv(t)
	e.NeedPython() // this form (bad --waits value / res) is handed to Python
	ok(e, "", "task", "update", "T-0001", "--status", "blocked")
	ok(e, "", "task", "update", "T-0002", "--status", "blocked", "--waits", "owner")
	ok(e, "", "task", "update", "T-0003", "--status", "blocked")
	contains(t, "res", ok(e, "", "res").Stdout, "blocked tasks: 1 owner-blocked · 2 internal-blocked")
}

func TestTaskWaitsResBlockedLineAfterWorkersLine(t *testing.T) {
	e := twEnv(t)
	e.NeedPython() // this form (bad --waits value / res) is handed to Python
	lines := strings.Split(ok(e, "", "res").Stdout, "\n")
	i := -1
	for n, l := range lines {
		if strings.HasPrefix(l, "workers live ") {
			i = n
			break
		}
	}
	if i < 0 {
		t.Fatalf("no 'workers live ' line in %q", lines)
	}
	for _, l := range lines[i+1:] {
		if strings.HasPrefix(l, "blocked tasks: ") {
			return
		}
	}
	t.Fatalf("no 'blocked tasks: ' line after the workers line in %q", lines)
}

func TestTaskWaitsDigestCountsAndTags(t *testing.T) {
	e := twEnv(t)
	ok(e, "", "task", "update", "T-0001", "--status", "blocked", "--reason", "queue")
	ok(e, "", "task", "update", "T-0002", "--status", "blocked", "--reason", "need key", "--waits", "owner")
	out := e.OKWith(ft.Opts{Env: map[string]string{"FACTORY_NOW": "2026-10-27T12:00:00+00:00", "TZ": "UTC"}},
		"digest", "--date", "2026-10-27", "--hours", "24").Stdout
	contains(t, "digest", out, "Blocked tasks: 1 owner-blocked, 1 internal-blocked")
	contains(t, "digest", out, "T-0002 zz-test task — blocked (owner): need key")
	contains(t, "digest", out, "T-0001 zz-test task — blocked: queue")
}
