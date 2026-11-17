// PARTIAL for P7a (T-1495): keep tests/test_digest.py until the three cases below have Go counterparts.
// not ported: test_other_value_error_in_build_digest_propagates (test_digest.py:503) - mock.patch of mod.build_digest and mod.cmd_digest in-process, no CLI form
// not ported: test_tick_digest_failure_isolated (test_digest.py:527) - mock.patch of mod.build_digest/mod.tick_lock and mod.cmd_tick in-process, no CLI form
// not ported: test_tick_digest_failed_logged_once_per_day (test_digest.py:539) - same mock.patch of build_digest in-process
package s2msgs

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	ft "factory/go/internal/factorytest"
)

// Port of tests/test_digest.py: 40 of 43 cases (Python Ran 43).

var digestOrgLine = regexp.MustCompile(`Orgs: claude (\d+) active · cursor (\d+) active`)

func digestGoalBlocksUnderOrg(out, org string) int {
	marker := "## Org " + org
	if !strings.Contains(out, marker) {
		return 0
	}
	section := strings.Split(out, marker)[1]
	for _, stop := range []string{"## Org claude", "## Org cursor", "## Closed earlier", "## Lessons", "## HQ"} {
		if stop != marker && strings.Contains(section, stop) {
			section = strings.Split(section, stop)[0]
		}
	}
	n := 0
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(line, "## G-") {
			n++
		}
	}
	return n
}

func digestOrgCounts(t *testing.T, out string) (claude, cursor int) {
	t.Helper()
	m := digestOrgLine.FindStringSubmatch(out)
	if m == nil {
		t.Fatal("Orgs line missing")
	}
	return atoi(t, m[1]), atoi(t, m[2])
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		t.Fatal(err)
	}
	return n
}

type digestBase struct {
	e *ft.Env
}

func newDigestBase(t *testing.T) *digestBase {
	t.Helper()
	slot(t)
	e := ft.New(t)
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedTeam("g-001-a", "G-001", "zz-test-lead")
	e.SeedAgent(ft.AgentRow{Name: "zz-test-director", Role: "director"})
	e.SeedAgent(ft.AgentRow{Name: "zz-test-coder", Role: "coder", Goal: "G-001", Team: "g-001-a", Task: "T-0002"})
	e.SeedAgent(ft.AgentRow{Name: "zz-test-boss", Role: "boss"})
	return &digestBase{e: e}
}

func (d *digestBase) digest(t *testing.T, args ...string) ft.Result {
	t.Helper()
	return ok(t, d.e, "", frozenEnv, append([]string{"digest"}, args...)...)
}

func (d *digestBase) digestOut(t *testing.T, args ...string) string {
	t.Helper()
	return d.digest(t, args...).Stdout
}

type digestTaskOpt struct {
	nullPR    bool
	assignee  string
	attempts  int
	dependsOn string
}

func digestSeed(t *testing.T, e *ft.Env, tid, status, title string, opt digestTaskOpt) {
	t.Helper()
	e.SeedTask(ft.Task{ID: tid, Status: status, Team: "g-001-a"})
	if opt.nullPR {
		sqlExec(t, e, "UPDATE tasks SET pr_url=NULL WHERE id=?", tid)
	}
	deps := opt.dependsOn
	if deps == "" {
		deps = "[]"
	}
	var assignee any
	if opt.assignee != "" {
		assignee = opt.assignee
	}
	sqlExec(t, e, "UPDATE tasks SET title=?, assignee=?, attempts=?, depends_on=? WHERE id=?",
		title, assignee, opt.attempts, deps, tid)
}

func (d *digestBase) block(t *testing.T, tid string, hours int, action, detail string) {
	t.Helper()
	if action == "" {
		action = "judge-blocked"
	}
	if detail == "" {
		detail = "stale sha"
	}
	sqlExec(t, d.e, "INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
		isoDelta(time.Duration(hours)*time.Hour), "judge", "task", tid, action, detail)
}

func parseH3Sections(out string) (map[string][]string, []string) {
	sec := map[string][]string{}
	var order []string
	var cur string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "### ") {
			cur = line[4:]
			if _, ok := sec[cur]; !ok {
				order = append(order, cur)
			}
			sec[cur] = nil
		} else if cur != "" {
			sec[cur] = append(sec[cur], line)
		}
	}
	return sec, order
}

func (d *digestBase) checkDigestSections(t *testing.T, now time.Time, args []string, extra map[string]string) {
	t.Helper()
	e := d.e
	digestSeed(t, e, "T-0001", "done", "zz-test done task", digestTaskOpt{})
	sqlExec(t, e, "INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
		isoAt(now, -2*time.Hour), "judge", "task", "T-0001", "merged", "@"+shaA[:8])
	sqlExec(t, e, "INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,created) VALUES(?,?,?,?,?,?)",
		"T-0001", ft.PRURL, shaA, "judge", "merge", isoAt(now, -2*time.Hour))
	digestSeed(t, e, "T-0002", "in_progress", "zz-test wip task", digestTaskOpt{assignee: "zz-test-coder", attempts: 2})
	digestSeed(t, e, "T-0003", "in_review", "zz-test review task", digestTaskOpt{assignee: "zz-test-coder", attempts: 1})
	digestSeed(t, e, "T-0004", "blocked", "zz-test blocked task", digestTaskOpt{})
	digestSeed(t, e, "T-0005", "todo", "zz-test ready task", digestTaskOpt{})
	digestSeed(t, e, "T-0006", "todo", "zz-test waiting task", digestTaskOpt{dependsOn: `["T-0004"]`})
	sqlExec(t, e, "INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
		isoAt(now, -1*time.Hour), "judge", "task", "T-0003", "judge-blocked", "checks not green")
	sqlExec(t, e, "INSERT INTO agents(name,role,goal_id,status) VALUES('zz-test-gone','coder','G-001','lost')")
	sqlExec(t, e, "INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
		isoAt(now, -1*time.Hour), "tick", "agent", "zz-test-gone", "lost", "p1")
	sqlExec(t, e, "INSERT INTO evaluations(team_id,score,author,created) VALUES('g-001-a',4,'x','t')")
	env := map[string]string{"FACTORY_NOW": now.Format(time.RFC3339)}
	for k, v := range extra {
		env[k] = v
	}
	out := ok(t, e, "", env, append([]string{"digest"}, args...)...).Stdout
	sec, secOrder := parseH3Sections(out)
	wantHdr := fmt.Sprintf("# Factory digest %s\n", now.Format("2006-01-02"))
	if !strings.HasPrefix(out, wantHdr) {
		snip := out
		if len(snip) > 60 {
			snip = snip[:60]
		}
		t.Fatalf("header = %q, want prefix %q", snip, wantHdr)
	}
	contains(t, "digest", out, "## G-001 — zz-test goal (active)")
	wantSecs := []string{"Done", "In progress", "Blocked", "Up next", "Team scores"}
	if fmt.Sprint(secOrder) != fmt.Sprint(wantSecs) {
		t.Fatalf("sections = %v, want %v", secOrder, wantSecs)
	}
	done := strings.Join(sec["Done"], "\n")
	for _, sub := range []string{"T-0001", ft.PRURL, shaA[:8]} {
		contains(t, "done", done, sub)
	}
	prog := strings.Join(sec["In progress"], "\n")
	for _, sub := range []string{"T-0002", "zz-test-coder", "attempts 2", "T-0003"} {
		contains(t, "prog", prog, sub)
	}
	blocked := strings.Join(sec["Blocked"], "\n")
	for _, sub := range []string{"T-0004", "judge-blocked: checks not green", "zz-test-gone"} {
		contains(t, "blocked", blocked, sub)
	}
	contains(t, "up next", strings.Join(sec["Up next"], "\n"), "1 ready todo")
	contains(t, "teams", strings.Join(sec["Team scores"], "\n"), "| g-001-a | 6 | 1 | 1 | 0 | 1 | 4.0 |")
}

func isoAt(base time.Time, delta time.Duration) string {
	return digestISO(delta, base)
}

func (d *digestBase) fixedBerlin(t *testing.T, hh, mm int) {
	t.Helper()
	fixed := fmt.Sprintf("2026-10-24T%02d:%02d:00+02:00", hh, mm)
	now, err := time.Parse(time.RFC3339, fixed)
	if err != nil {
		t.Fatal(err)
	}
	d.checkDigestSections(t, now, nil, map[string]string{"TZ": "Europe/Berlin", "FACTORY_NOW": fixed})
}

func (d *digestBase) closeGoal(t *testing.T, closedH int, updatedH *int, event bool, gid, detail string) {
	t.Helper()
	if gid == "" {
		gid = "G-001"
	}
	if detail == "" {
		detail = "done"
	}
	uh := closedH
	if updatedH != nil {
		uh = *updatedH
	}
	sqlExec(t, d.e, "UPDATE goals SET status='done', updated=? WHERE id=?", isoDelta(-time.Duration(uh)*time.Hour), gid)
	if event {
		sqlExec(t, d.e, "INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
			isoDelta(-time.Duration(closedH)*time.Hour), "x", "goal", gid, "status", detail)
	}
}

func (d *digestBase) lost(t *testing.T, taskID *string, taskStatus string) {
	t.Helper()
	var tid any
	if taskID != nil {
		tid = *taskID
	}
	sqlExec(t, d.e, "INSERT INTO agents(name,role,goal_id,task_id,status) VALUES('zz-test-gone','coder','G-001',?,'lost')", tid)
	if taskStatus != "" {
		id := ""
		if taskID != nil {
			id = *taskID
		}
		digestSeed(t, d.e, id, taskStatus, "zz-test lost work", digestTaskOpt{})
	}
	sqlExec(t, d.e, "INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
		isoDelta(-3*time.Hour), "tick", "agent", "zz-test-gone", "lost", "p1")
}

func (d *digestBase) blockedSection(t *testing.T) string {
	t.Helper()
	return section(d.digestOut(t), "Blocked")
}

func (d *digestBase) reasonCase(t *testing.T, rows [][3]any) string {
	t.Helper()
	digestSeed(t, d.e, "T-0001", "blocked", "zz-test reason", digestTaskOpt{})
	const ev = "INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)"
	for _, row := range rows {
		hours := row[0].(int)
		sqlExec(t, d.e, ev, isoDelta(time.Duration(hours)*time.Hour), "lead", "task", "T-0001", row[1], row[2])
	}
	return section(d.digestOut(t, "--hours", "48"), "Blocked")
}

func newTickDigestBase(t *testing.T) *digestBase {
	t.Helper()
	d := newDigestBase(t)
	d.e.NeedPython()
	ok(t, d.e, "", nil, "config", "digest_hour", "0")
	return d
}

var realClock = ft.Opts{Env: map[string]string{"FACTORY_TEST_NOW": ""}}

func tickDigestDates(t *testing.T, e *ft.Env) map[string]struct{} {
	t.Helper()
	// Mirrors Python: the tick uses the real local date; accept before/after in case midnight passes mid-call.
	before := time.Now().Format("2006-01-02")
	e.OKWith(realClock, "tick")
	return map[string]struct{}{before: {}, time.Now().Format("2006-01-02"): {}}
}

func dailyDigests(t *testing.T, e *ft.Env, day string) [][]string {
	t.Helper()
	if day != "" {
		return e.SQL("SELECT sender, recipient, kind, subject, body FROM messages WHERE subject=?", "daily digest "+day)
	}
	return e.SQL("SELECT sender, recipient, kind, subject, body FROM messages WHERE subject LIKE 'daily digest %' ORDER BY id")
}

func configVal(t *testing.T, e *ft.Env, key string) string {
	t.Helper()
	return sql1(t, e, "SELECT value FROM config WHERE key=?", key)
}

func TestDigestHarnessPassesTzToCli(t *testing.T) {
	e := ft.New(t)
	now := map[string]string{"FACTORY_NOW": "2026-10-24T23:30:00+00:00"}
	for _, tc := range []struct {
		zone, day string
	}{
		{"Etc/GMT+2", "2026-10-24"},
		{"Etc/GMT-2", "2026-10-25"},
	} {
		t.Run(tc.zone, func(t *testing.T) {
			t.Setenv("TZ", tc.zone)
			out := ok(t, e, "", now, "digest").Stdout
			want := fmt.Sprintf("# Factory digest %s\n", tc.day)
			if !strings.HasPrefix(out, want) {
				snip := out
				if len(snip) > 40 {
					snip = snip[:40]
				}
				t.Fatalf("header %q, want %q", snip, want)
			}
		})
	}
}

func TestDigestSections(t *testing.T) {
	d := newDigestBase(t)
	d.checkDigestSections(t, digestFrozen, []string{"--date", digestFrozen.Format("2006-01-02")}, nil)
}

func TestDigestSectionsAt0030Local(t *testing.T) {
	d := newDigestBase(t)
	d.fixedBerlin(t, 0, 30)
}

func TestDigestSectionsAt1200Local(t *testing.T) {
	d := newDigestBase(t)
	d.fixedBerlin(t, 12, 0)
}

func TestDigestListsHqFlaglessOnce(t *testing.T) {
	d := newDigestBase(t)
	e := d.e
	e.SeedGoal("G-002", "", "zz-test second goal")
	sqlExec(t, e, "UPDATE agents SET flagless=1, flagless_since='2026-10-23T10:00:00+00:00' WHERE name IN ('zz-test-director','zz-test-boss')")
	sqlExec(t, e, "UPDATE agents SET flagless=1, flagless_since='2026-10-23T11:00:00+00:00' WHERE name='zz-test-coder'")
	out := d.digestOut(t)
	for _, name := range []string{"zz-test-director", "zz-test-boss"} {
		line := "agent " + name + " running without role flags"
		if strings.Count(out, line) != 1 {
			t.Fatalf("%q count = %d, want 1", line, strings.Count(out, line))
		}
	}
	hq := strings.Split(out, "## HQ")[1]
	contains(t, "hq", hq, "zz-test-director")
	contains(t, "hq", hq, "zz-test-boss")
	notContains(t, "hq", hq, "zz-test-coder")
	if strings.Index(out, "## HQ") <= strings.Index(out, "## G-002") {
		t.Fatal("HQ should follow G-002")
	}
	g1 := strings.Split(strings.Split(out, "## G-001")[1], "## G-002")[0]
	contains(t, "g1 blocked", section(g1, "Blocked"), "agent zz-test-coder running without role flags")
	notContains(t, "g2 body", strings.Split(strings.Split(out, "## G-002")[1], "## HQ")[0], "zz-test-boss")
}

func TestDigestDoneTaskWithEarlierJudgeBlockNotBlocked(t *testing.T) {
	d := newDigestBase(t)
	digestSeed(t, d.e, "T-0001", "done", "zz-test refused then merged", digestTaskOpt{})
	sqlExec(t, d.e, "UPDATE tasks SET updated=? WHERE id='T-0001'", isoDelta(-1*time.Hour))
	d.block(t, "T-0001", -3, "", "")
	sqlExec(t, d.e, "INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
		isoDelta(-1*time.Hour), "lead", "task", "T-0001", "update", "status=done")
	out := d.digestOut(t, "--hours", "24")
	contains(t, "done", section(out, "Done"), "T-0001")
	notContains(t, "blocked", section(out, "Blocked"), "T-0001")
}

func TestDigestOpenTaskJudgeBlockedListed(t *testing.T) {
	d := newDigestBase(t)
	digestSeed(t, d.e, "T-0001", "in_review", "zz-test refused open", digestTaskOpt{assignee: "zz-test-coder"})
	d.block(t, "T-0001", -2, "", "")
	d.block(t, "T-0001", -1, "", "checks red")
	blocked := section(d.digestOut(t, "--hours", "24"), "Blocked")
	if strings.Count(blocked, "T-0001") != 1 {
		t.Fatalf("T-0001 count = %d", strings.Count(blocked, "T-0001"))
	}
	contains(t, "blocked", blocked, "judge-blocked: checks red")
}

func TestDigestStatusBlockedListed(t *testing.T) {
	d := newDigestBase(t)
	digestSeed(t, d.e, "T-0001", "blocked", "zz-test status blocked", digestTaskOpt{})
	digestSeed(t, d.e, "T-0002", "blocked", "zz-test blocked with event", digestTaskOpt{})
	d.block(t, "T-0002", -2, "merge-failed", "gh down")
	blocked := section(d.digestOut(t, "--hours", "24"), "Blocked")
	contains(t, "blocked", blocked, "T-0001 zz-test status blocked (blocked)")
	if strings.Count(blocked, "T-0002") != 1 {
		t.Fatalf("T-0002 count")
	}
	contains(t, "blocked", blocked, "merge-failed: gh down")
}

func TestDigestStatusBlockedShowsReason(t *testing.T) {
	d := newDigestBase(t)
	const ev = "INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)"
	digestSeed(t, d.e, "T-0001", "blocked", "zz-test noted", digestTaskOpt{})
	sqlExec(t, d.e, ev, isoDelta(-3*time.Hour), "lead", "task", "T-0001", "note", "waiting on owner decision")
	sqlExec(t, d.e, ev, isoDelta(-2*time.Hour), "lead", "task", "T-0001", "update", "status=blocked")
	digestSeed(t, d.e, "T-0002", "blocked", "zz-test event only", digestTaskOpt{})
	sqlExec(t, d.e, ev, isoDelta(-2*time.Hour), "lead", "task", "T-0002", "update", "status=blocked — disk full")
	digestSeed(t, d.e, "T-0003", "blocked", "zz-test bare", digestTaskOpt{})
	blocked := section(d.digestOut(t, "--hours", "24"), "Blocked")
	contains(t, "blocked", blocked, "T-0001 zz-test noted — blocked: waiting on owner decision")
	contains(t, "blocked", blocked, "T-0002 zz-test event only — blocked: disk full")
	contains(t, "blocked", blocked, "T-0003 zz-test bare (blocked)")
}

func TestDigestBlockedReasonFreshUpdateNoteBeatsOlderNote(t *testing.T) {
	d := newDigestBase(t)
	blocked := d.reasonCase(t, [][3]any{{-30, "note", "Attempt 2: fix item 2"}, {-1, "update", "status=blocked — waiting on owner decision"}})
	contains(t, "blocked", blocked, "blocked: waiting on owner decision")
	notContains(t, "blocked", blocked, "Attempt 2")
}

func TestDigestBlockedReasonNewerNoteBeatsUpdateNote(t *testing.T) {
	d := newDigestBase(t)
	blocked := d.reasonCase(t, [][3]any{{-3, "update", "status=blocked — waiting on owner decision"}, {-1, "note", "owner replied, retrying"}})
	contains(t, "blocked", blocked, "blocked: owner replied, retrying")
	notContains(t, "blocked", blocked, "waiting on owner")
}

func TestDigestBlockedReasonNone(t *testing.T) {
	d := newDigestBase(t)
	blocked := d.reasonCase(t, [][3]any{{-5, "update", "status=in_review pr=https://x/pull/1"}, {-1, "update", "status=blocked"}})
	contains(t, "blocked", blocked, "T-0001 zz-test reason (blocked)")
	notContains(t, "blocked", blocked, "in_review")
}

func TestDigestDoneIncludesWorkerTasks(t *testing.T) {
	d := newDigestBase(t)
	digestSeed(t, d.e, "T-0001", "done", "zz-test pm plan", digestTaskOpt{nullPR: true})
	sqlExec(t, d.e, "UPDATE tasks SET role='pm', updated=? WHERE id='T-0001'", isoDelta(-1*time.Hour))
	digestSeed(t, d.e, "T-0002", "done", "zz-test old pm plan", digestTaskOpt{nullPR: true})
	sqlExec(t, d.e, "UPDATE tasks SET role='pm', updated=? WHERE id='T-0002'", isoDelta(-72*time.Hour))
	done := section(d.digestOut(t, "--hours", "24"), "Done")
	contains(t, "done", done, "T-0001 zz-test pm plan")
	notContains(t, "done", done, "T-0002")
}

func TestDigestNoDoubleCount(t *testing.T) {
	d := newDigestBase(t)
	e := d.e
	digestSeed(t, e, "T-0001", "done", "zz-test merged task", digestTaskOpt{})
	sqlExec(t, e, "UPDATE tasks SET updated=? WHERE id='T-0001'", isoDelta(-1*time.Hour))
	sqlExec(t, e, "INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
		isoDelta(-1*time.Hour), "judge", "task", "T-0001", "merged", "@"+shaA[:8])
	sqlExec(t, e, "INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,created) VALUES(?,?,?,?,?,?)",
		"T-0001", ft.PRURL, shaA, "judge", "merge", isoDelta(-1*time.Hour))
	done := section(d.digestOut(t, "--hours", "24"), "Done")
	if strings.Count(done, "T-0001") != 1 {
		t.Fatalf("count = %d", strings.Count(done, "T-0001"))
	}
	contains(t, "done", done, ft.PRURL)
}

func TestDigestGoalClosed2hAgoShownInFull(t *testing.T) {
	d := newDigestBase(t)
	d.closeGoal(t, 2, nil, true, "", "")
	out := d.digestOut(t)
	contains(t, "digest", out, "## G-001 — zz-test goal (done)")
	for _, sec := range []string{"### Done", "### In progress", "### Blocked"} {
		contains(t, "digest", out, sec)
	}
	notContains(t, "digest", out, "Closed earlier")
}

func TestDigestGoalClosedLongAgoCollapsed(t *testing.T) {
	d := newDigestBase(t)
	uh := 1
	d.closeGoal(t, 30, &uh, true, "", "")
	out := d.digestOut(t)
	notContains(t, "digest", out, "G-001")
	contains(t, "digest", out, "_no active goals_")
	wide := d.digestOut(t, "--hours", "72")
	notContains(t, "wide", wide, "## G-001")
	contains(t, "wide", wide, "_no active goals_")
	contains(t, "wide", wide, "## Closed earlier")
	line := "- G-001 — zz-test goal (done, closed " + isoDelta(-30*time.Hour) + ")"
	if strings.Count(wide, line) != 1 || strings.Count(wide, "G-001") != 1 {
		t.Fatalf("closed line count")
	}
}

func TestDigestGoalClosedLongAgoExplicitGoalOneLine(t *testing.T) {
	d := newDigestBase(t)
	uh := 1
	d.closeGoal(t, 30, &uh, true, "", "")
	out := d.digestOut(t, "--goal", "G-001")
	if strings.Count(out, "G-001") != 1 {
		t.Fatalf("G-001 count")
	}
	contains(t, "digest", out, "## Closed earlier")
	contains(t, "digest", out, "(done, closed ")
	notContains(t, "digest", out, "### Done")
}

func TestDigestClosedGoalWithoutStatusEventUsesUpdated(t *testing.T) {
	d := newDigestBase(t)
	d.closeGoal(t, 2, nil, false, "", "")
	contains(t, "digest", d.digestOut(t), "## G-001 — zz-test goal (done)")
	d.closeGoal(t, 30, nil, false, "", "")
	notContains(t, "digest", d.digestOut(t), "G-001")
	wide := d.digestOut(t, "--hours", "72")
	contains(t, "wide", wide, "- G-001 — zz-test goal (done, closed "+isoDelta(-30*time.Hour)+")")
}

func TestDigestCancelledGoalWithNoteCollapsed(t *testing.T) {
	d := newDigestBase(t)
	uh := 1
	d.closeGoal(t, 30, &uh, true, "", "cancelled: dropped")
	sqlExec(t, d.e, "UPDATE goals SET status='cancelled' WHERE id='G-001'")
	contains(t, "digest", d.digestOut(t, "--hours", "72"), "(cancelled, closed")
}

func TestDigestLostAgentTaskDoneNoLine(t *testing.T) {
	d := newDigestBase(t)
	tid := "T-0009"
	d.lost(t, &tid, "done")
	notContains(t, "blocked", d.blockedSection(t), "zz-test-gone")
}

func TestDigestLostAgentTaskMergedAfterNoLine(t *testing.T) {
	d := newDigestBase(t)
	tid := "T-0009"
	d.lost(t, &tid, "in_review")
	sqlExec(t, d.e, "INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,created) VALUES(?,?,?,?,?,?)",
		"T-0009", ft.PRURL, shaA, "judge", "merge", isoDelta(-2*time.Hour))
	notContains(t, "blocked", d.blockedSection(t), "zz-test-gone")
}

func TestDigestLostAgentTaskMergedEventAfterNoLine(t *testing.T) {
	d := newDigestBase(t)
	tid := "T-0009"
	d.lost(t, &tid, "in_review")
	sqlExec(t, d.e, "INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
		isoDelta(-2*time.Hour), "judge", "task", "T-0009", "merged", "merged-ext")
	notContains(t, "blocked", d.blockedSection(t), "zz-test-gone")
}

func TestDigestLostAgentMergeBeforeLostKeepsLine(t *testing.T) {
	d := newDigestBase(t)
	tid := "T-0009"
	d.lost(t, &tid, "in_progress")
	sqlExec(t, d.e, "INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,created) VALUES(?,?,?,?,?,?)",
		"T-0009", ft.PRURL, shaA, "judge", "merge", isoDelta(-5*time.Hour))
	contains(t, "blocked", d.blockedSection(t), "agent zz-test-gone lost at")
}

func TestDigestLostAgentOpenTaskKeepsLine(t *testing.T) {
	d := newDigestBase(t)
	tid := "T-0009"
	d.lost(t, &tid, "in_progress")
	contains(t, "blocked", d.blockedSection(t), "agent zz-test-gone lost at")
}

func TestDigestLostAgentNoTaskKeepsLine(t *testing.T) {
	d := newDigestBase(t)
	d.lost(t, nil, "")
	contains(t, "blocked", d.blockedSection(t), "agent zz-test-gone lost at")
}

func TestDigestEmptySections(t *testing.T) {
	d := newDigestBase(t)
	out := d.digestOut(t, "--goal", "G-001")
	if strings.Count(out, "_nothing_") != 3 {
		t.Fatalf("_nothing_ count = %d", strings.Count(out, "_nothing_"))
	}
}

func TestDigestLeadsLineOnceWithTwoGoals(t *testing.T) {
	d := newDigestBase(t)
	d.e.SeedGoal("G-002", "", "zz-test goal two")
	out := d.digestOut(t)
	contains(t, "digest", out, "## G-001 ")
	contains(t, "digest", out, "## G-002 ")
	if strings.Count(out, "Leads: ") != 1 {
		t.Fatal("Leads line count")
	}
	if strings.Index(out, "Leads: ") >= strings.Index(out, "## G-001 ") {
		t.Fatal("Leads should precede G-001")
	}
}

func TestDigestWindow(t *testing.T) {
	d := newDigestBase(t)
	e := d.e
	digestSeed(t, e, "T-0001", "done", "zz-test old done", digestTaskOpt{})
	old := isoDelta(-72 * time.Hour)
	sqlExec(t, e, "INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
		old, "judge", "task", "T-0001", "merged", "@"+shaA[:8])
	sqlExec(t, e, "INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,created) VALUES(?,?,?,?,?,?)",
		"T-0001", ft.PRURL, shaA, "judge", "merge", old)
	done24 := section(d.digestOut(t, "--hours", "24"), "Done")
	notContains(t, "done24", done24, "T-0001")
	done96 := section(d.digestOut(t, "--hours", "96"), "Done")
	contains(t, "done96", done96, "T-0001")
}

func TestDigestSendRole(t *testing.T) {
	d := newDigestBase(t)
	digestSeed(t, d.e, "T-0002", "in_progress", "zz-test wip", digestTaskOpt{assignee: "zz-test-coder"})
	refused(t, d.e, "zz-test-coder", 3, frozenEnv, "digest", "--send")
	if sql1(t, d.e, "SELECT count(*) FROM messages") != "0" {
		t.Fatal("messages after coder refused")
	}
	ok(t, d.e, "zz-test-director", frozenEnv, "digest", "--send")
	rows := d.e.SQL("SELECT sender, recipient, kind, subject, body FROM messages")
	if len(rows) != 1 {
		t.Fatalf("messages = %d", len(rows))
	}
	if rows[0][0] != "zz-test-director" || rows[0][1] != "zz-test-boss" || rows[0][2] != "report" {
		t.Fatalf("msg meta = %v", rows[0][:3])
	}
	wantSubj := "daily digest " + digestFrozen.Format("2006-01-02")
	if rows[0][3] != wantSubj {
		t.Fatalf("subject = %q", rows[0][3])
	}
	contains(t, "body", rows[0][4], "# Factory digest")
}

func TestDigestScorecardUnchanged(t *testing.T) {
	d := newDigestBase(t)
	e := d.e
	digestSeed(t, e, "T-0001", "done", "a", digestTaskOpt{attempts: 3})
	digestSeed(t, e, "T-0002", "in_progress", "b", digestTaskOpt{attempts: 1})
	e.SeedTeam("g-001-b", "G-001", "")
	sqlExec(t, e, "INSERT INTO verdicts(task_id,role,verdict,created) VALUES('T-0001','judge','merge','t'),('T-0002','qa','fail','t')")
	sqlExec(t, e, "INSERT INTO evaluations(team_id,score,author,created) VALUES('g-001-a',3,'x','t'),('g-001-a',4,'x','t')")
	want := "TEAM     STATUS  TASKS  DONE  MERGED  REJECTIONS  RETRIES  SCORE  LEAD\n" +
		"g-001-a  active  2      1     1       1           2        3.5    -   \n" +
		"g-001-b  active  0      0     0       0           0               -   \n" +
		"\n" +
		"director (24h, daytime 08-20): no utilization samples · cap breaches 0 · boss scores -\n"
	out := ok(t, e, "", nil, "scorecard").Stdout
	if out != want {
		t.Fatalf("scorecard = %q", out)
	}
}

func TestDigestGroupsActiveGoalsByOrg(t *testing.T) {
	needPy(t)
	d := newDigestBase(t)
	e := d.e
	ok(t, e, "", nil, "config", "orgs_enabled", "claude,cursor")
	e.SeedGoal("G-002", "", "zz-test cursor goal")
	sqlExec(t, e, "UPDATE goals SET org='cursor' WHERE id='G-002'")
	e.SeedTeam("g-002-a", "G-002", "zz-test-lead")
	out := d.digestOut(t)
	contains(t, "digest", out, "Orgs: claude 1 active · cursor 1 active")
	claudeIdx := strings.Index(out, "## Org claude")
	g1Idx := strings.Index(out, "## G-001 — zz-test goal (active)")
	cursorIdx := strings.Index(out, "## Org cursor")
	g2Idx := strings.Index(out, "## G-002 — zz-test cursor goal (active)")
	if claudeIdx >= g1Idx || cursorIdx >= g2Idx {
		t.Fatal("org section order")
	}
}

func TestDigestShowsDisabledOrgWhenItHasGoals(t *testing.T) {
	needPy(t)
	d := newDigestBase(t)
	e := d.e
	ok(t, e, "", nil, "config", "orgs_enabled", "claude")
	e.SeedGoal("G-002", "", "zz-test cursor goal")
	sqlExec(t, e, "UPDATE goals SET org='cursor' WHERE id='G-002'")
	e.SeedTeam("g-002-a", "G-002", "zz-test-lead")
	out := d.digestOut(t)
	contains(t, "digest", out, "## G-002 — zz-test cursor goal (active)")
	contains(t, "digest", out, "## Org cursor")
	claudeN, cursorN := digestOrgCounts(t, out)
	if claudeN != digestGoalBlocksUnderOrg(out, "claude") || cursorN != digestGoalBlocksUnderOrg(out, "cursor") {
		t.Fatal("org goal counts")
	}
}

func TestDigestClaudeOnlyOmitsEmptyCursorSection(t *testing.T) {
	needPy(t)
	d := newDigestBase(t)
	ok(t, d.e, "", nil, "config", "orgs_enabled", "claude")
	out := d.digestOut(t)
	contains(t, "digest", out, "## Org claude")
	notContains(t, "digest", out, "## Org cursor")
}

func TestDigestTickDigestOnce(t *testing.T) {
	d := newTickDigestBase(t)
	dates := tickDigestDates(t, d.e)
	m := dailyDigests(t, d.e, "")
	if len(m) != 1 {
		t.Fatalf("digests = %d", len(m))
	}
	if m[0][0] != "director" || m[0][1] != "zz-test-boss" || m[0][2] != "report" {
		t.Fatalf("meta = %v", m[0][:3])
	}
	day := configVal(t, d.e, "last_digest_date")
	if _, ok := dates[day]; !ok {
		t.Fatalf("day %q not in %v", day, dates)
	}
	if m[0][3] != "daily digest "+day {
		t.Fatalf("subject = %q", m[0][3])
	}
	d.e.OKWith(realClock, "tick")
	if len(dailyDigests(t, d.e, day)) != 1 {
		t.Fatal("duplicate digest")
	}
}

func TestDigestTickDigestBothOrgsEnabled(t *testing.T) {
	d := newTickDigestBase(t)
	e := d.e
	ok(t, e, "", nil, "config", "orgs_enabled", "claude,cursor")
	ok(t, e, "", nil, "config", "org_prefix_default", "1")
	e.SeedGoal("G-002", "", "zz-test cursor goal")
	sqlExec(t, e, "UPDATE goals SET org='cursor' WHERE id='G-002'")
	for _, row := range [][3]string{
		{"claude-boss", "boss", "claude"}, {"cursor-boss", "boss", "cursor"},
		{"claude-director", "director", "claude"}, {"cursor-director", "director", "cursor"},
	} {
		e.SeedAgent(ft.AgentRow{Name: row[0], Role: row[1]})
		sqlExec(t, e, "UPDATE agents SET org=? WHERE name=?", row[2], row[0])
	}
	dates := tickDigestDates(t, e)
	m := dailyDigests(t, e, "")
	if len(m) != 2 {
		t.Fatalf("digests = %d", len(m))
	}
	byRcpt := map[string][]string{}
	for _, row := range m {
		byRcpt[row[1]] = row
	}
	if len(byRcpt) != 2 || byRcpt["claude-boss"] == nil || byRcpt["cursor-boss"] == nil {
		t.Fatalf("recipients %v", byRcpt)
	}
	if byRcpt["claude-boss"][0] != "claude-director" || byRcpt["cursor-boss"][0] != "cursor-director" {
		t.Fatal("senders")
	}
	day := configVal(t, e, "last_digest_date")
	if _, ok := dates[day]; !ok {
		t.Fatal("day")
	}
	for _, row := range m {
		contains(t, "body", row[4], "## Org claude")
		contains(t, "body", row[4], "## Org cursor")
	}
}

func TestDigestInvalidFactoryNowDoesNotAbortTick(t *testing.T) {
	d := newTickDigestBase(t)
	e := d.e
	bad := map[string]string{"FACTORY_NOW": "not-a-time"}
	p := e.RunWith(ft.Opts{Env: bad}, "tick")
	if p.Exit != 0 {
		t.Fatalf("tick exit %d\n%s\n%s", p.Exit, p.Stdout, p.Stderr)
	}
	if len(dailyDigests(t, e, "")) != 0 {
		t.Fatal("digests after bad FACTORY_NOW tick")
	}
	if len(e.SQL("SELECT * FROM events WHERE action='digest-failed'")) != 1 {
		t.Fatal("digest-failed event")
	}
	q := e.RunWith(ft.Opts{Env: bad}, "digest")
	if q.Exit != 1 {
		t.Fatalf("digest exit %d", q.Exit)
	}
	contains(t, "stderr", q.Stderr, "FACTORY_NOW must be an ISO timestamp, got not-a-time")
	// not ported: in-process mod.digest_clock() ValueError (see tests/test_digest.py)
}

func TestDigestFactoryNowErrorIsAValueError(t *testing.T) {
	needPy(t)
	// not ported: issubclass(mod.FactoryNowError, ValueError) and in-process digest_clock (tests/test_digest.py)
	d := newDigestBase(t)
	q := d.e.RunWith(ft.Opts{Env: map[string]string{"FACTORY_NOW": "garbage"}}, "digest")
	if q.Exit != 1 {
		t.Fatalf("digest exit %d", q.Exit)
	}
	contains(t, "stderr", q.Stderr, "FACTORY_NOW must be an ISO timestamp, got garbage")
}

func TestDigestTickDigestNotDue(t *testing.T) {
	d := newTickDigestBase(t)
	day := digestFrozen.Format("2006-01-02")
	sqlExec(t, d.e, "INSERT INTO config(key,value) VALUES('last_digest_date',?)", day)
	d.e.OKWith(realClock, "tick")
	if len(dailyDigests(t, d.e, day)) != 0 {
		t.Fatal("digest sent when already done")
	}
}

func TestDigestTickDigestBeforeHour(t *testing.T) {
	d := newTickDigestBase(t)
	ok(t, d.e, "", nil, "config", "digest_hour", "24")
	d.e.OKWith(realClock, "tick")
	if len(dailyDigests(t, d.e, "")) != 0 {
		t.Fatal("digest before hour")
	}
}

func TestDigestTickDigestHerdrUnreachable(t *testing.T) {
	d := newTickDigestBase(t)
	d.e.HerdrStateful()
	d.e.HerdrFail(true)
	p := d.e.OKWith(realClock, "tick")
	contains(t, "stdout", p.Stdout, "exporting only")
	if len(dailyDigests(t, d.e, "")) != 1 {
		t.Fatal("digest count")
	}
}
