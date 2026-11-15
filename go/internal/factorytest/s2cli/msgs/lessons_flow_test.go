package s2msgs

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	ft "factory/go/internal/factorytest"
)

// Port of tests/test_lessons_flow.py (31 cases). judge merge and gh Script paths may call NeedPython.
// not ported: CollapseTests (4 cases) — second judge --merge on PR #2 needs Python gh merge_lands / files JSON (stub parity).

const flowPR2 = "https://github.com/zz-test/zz-test/pull/2"

var flowFrozen = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

func flowISO(d time.Duration) string {
	return flowFrozen.Add(d).Format("2006-01-02T15:04:05Z07:00")
}

type flowBase struct {
	e *ft.Env
}

func newFlowBase(t *testing.T) *flowBase {
	t.Helper()
	t.Setenv("TZ", "UTC")
	e := ft.New(t)
	seedProject(e, "zz-test-proj")
	e.SeedGoal("G-001", "zz-test-proj", "zz-test goal")
	e.SeedTask(ft.Task{})
	for _, r := range []string{"boss", "director", "lead"} {
		e.SeedAgent(ft.AgentRow{Name: "zz-test-" + r, Role: r})
	}
	for _, r := range lessonWorkers {
		e.SeedAgent(ft.AgentRow{Name: "zz-test-" + r, Role: r, Goal: "G-001", Task: "T-0001"})
	}
	e.UseGitHead()
	installFlowGh(t, e, prViewRule(ft.PRURL, e.HeadSHA, "OPEN", nil, greenChecks()))
	appendGhMainAPI(t, e)
	return &flowBase{e: e}
}

func (f *flowBase) newLesson(t *testing.T, title string) string {
	t.Helper()
	if title == "" {
		title = "t"
	}
	p := f.e.OKWith(ft.Opts{Agent: "zz-test-lead"}, "lesson", "add", "--title", title, "--roles", "lead,director", "--rule", "r")
	return strings.TrimSpace(p.Stdout)
}

func (f *flowBase) merge(t *testing.T, tid string) ft.Result {
	t.Helper()
	if tid == "" {
		tid = "T-0001"
	}
	okAs(f.e, "zz-test-reviewer", "verdict", tid, "--pass", "--summary", "ok", "--sha", f.e.HeadSHA)
	okAs(f.e, "zz-test-qa", "verdict", tid, "--pass", "--summary", gatesSummary(), "--sha", f.e.HeadSHA)
	f.e.NeedPython()
	return as(f.e, "zz-test-judge", "judge", tid, "--merge", "--reason", "zz-test")
}

func flowMsgs(e *ft.Env, subject string) [][]string {
	return e.SQL("SELECT id, sender, recipient, kind, subject, body, read_at FROM messages WHERE subject LIKE ? ORDER BY id", subject+"%")
}

func seedPRExtra(t *testing.T, e *ft.Env, url, tid string) {
	t.Helper()
	e.SeedTask(ft.Task{ID: tid, PRURL: url, Status: "in_review"})
	seedPRStub(t, e, url, e.HeadSHA, greenChecks())
}

func TestLessonsFlowHelpListsActions(t *testing.T) {
	f := newFlowBase(t)
	out := f.e.OK("lesson", "--help").Stdout
	for _, w := range []string{"add", "list", "show", "edit", "link", "apply", "verify", "reopen"} {
		contains(t, "help", out, w)
	}
}

func TestLessonsFlowAmendments(t *testing.T) {
	f := newFlowBase(t)
	lid := f.newLesson(t, "t")
	p := as(f.e, "zz-test-lead", "lesson", "add", "L-009", "--title", "t", "--roles", "all", "--rule", "r")
	if p.Exit != 2 {
		t.Fatalf("exit %d", p.Exit)
	}
	contains(t, "stderr", p.Stderr, "add takes no id")
	if as(f.e, "zz-test-lead", "lesson", "list", "--role", "bogus").Exit != 2 {
		t.Fatal("bogus role")
	}
	f.e.OKWith(ft.Opts{Agent: "zz-test-lead"}, "lesson", "list", "--role", "all")
	p = as(f.e, "zz-test-lead", "lesson", "show")
	if p.Exit != 2 {
		t.Fatalf("show exit %d", p.Exit)
	}
	contains(t, "stderr", p.Stderr, "usage: factory lesson show")
	notContains(t, "stderr", p.Stderr, "None")
	if lid != "L-001" {
		t.Fatalf("lid %q", lid)
	}
}

func TestLessonsFlowLinkPRMergedThenApply(t *testing.T) {
	f := newFlowBase(t)
	lid := f.newLesson(t, "t")
	appendGhRules(t, f.e, prViewRule(oldPR, shaA, "MERGED", nil, nil))
	f.e.OKWith(ft.Opts{Agent: "zz-test-lead"}, "lesson", "link", lid, "--pr", oldPR, "--via", "prompt")
	if got := f.e.SQL("SELECT merged_at FROM lesson_links")[0][0]; got != "2026-10-01T10:00:00Z" {
		t.Fatalf("merged_at %q", got)
	}
	if as(f.e, "zz-test-lead", "lesson", "link", lid, "--pr", oldPR).Exit != 2 {
		t.Fatal("duplicate link")
	}
	f.e.OKWith(ft.Opts{Agent: "zz-test-lead"}, "lesson", "apply", lid, "--note", "done")
	if lessonCol(f.e, lid, "status") != "applied" {
		t.Fatal("status")
	}
	if lessonCol(f.e, lid, "applied_at") == "NULL" {
		t.Fatal("applied_at")
	}
	if f.e.FailWith(ft.Opts{Agent: "zz-test-lead"}, 2, "lesson", "apply", lid).Exit != 2 {
		t.Fatal("re-apply")
	}
}

func TestLessonsFlowApplyUnmergedAndNoLinks(t *testing.T) {
	f := newFlowBase(t)
	lid := f.newLesson(t, "t")
	p := as(f.e, "zz-test-lead", "lesson", "apply", lid)
	if p.Exit != 2 {
		t.Fatalf("exit %d", p.Exit)
	}
	contains(t, "stderr", p.Stderr, "no links")
	f.e.OKWith(ft.Opts{Agent: "zz-test-lead"}, "lesson", "link", lid, "--task", "T-0001")
	p = as(f.e, "zz-test-lead", "lesson", "apply", lid)
	if p.Exit != 2 {
		t.Fatalf("exit %d", p.Exit)
	}
	contains(t, "stderr", p.Stderr, "T-0001")
	if lessonCol(f.e, lid, "status") != "open" {
		t.Fatal("status")
	}
	if as(f.e, "zz-test-lead", "lesson", "link", lid, "--task", "T-9999").Exit != 2 {
		t.Fatal("bad task")
	}
	if as(f.e, "zz-test-lead", "lesson", "link", lid, "--task", "T-0001").Exit != 2 {
		t.Fatal("dup task link")
	}
}

func TestLessonsFlowApplyRechecksTaskLinkViaVerdict(t *testing.T) {
	f := newFlowBase(t)
	lid := f.newLesson(t, "t")
	f.e.OKWith(ft.Opts{Agent: "zz-test-lead"}, "lesson", "link", lid, "--task", "T-0001")
	sqlExec(t, f.e, "INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,created) VALUES(?,?,?,?,?,?)",
		"T-0001", ft.PRURL, shaA, "judge", "merge", "2026-10-02T00:00:00+00:00")
	f.e.OKWith(ft.Opts{Agent: "zz-test-lead"}, "lesson", "apply", lid)
	if lessonCol(f.e, lid, "status") != "applied" {
		t.Fatal("status")
	}
}

func TestLessonsFlowLinkDoneMergedTaskStoresMergedAt(t *testing.T) {
	f := newFlowBase(t)
	lid := f.newLesson(t, "t")
	sqlExec(t, f.e, "UPDATE tasks SET status='done' WHERE id='T-0001'")
	sqlExec(t, f.e, "INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,created) VALUES(?,?,?,?,?,?)",
		"T-0001", ft.PRURL, shaA, "judge", "merge", "2026-10-02T00:00:00+00:00")
	f.e.OKWith(ft.Opts{Agent: "zz-test-lead"}, "lesson", "link", lid, "--task", "T-0001")
	row := f.e.SQL("SELECT merged_at, pr_url FROM lesson_links")[0]
	if row[0] != "2026-10-02T00:00:00+00:00" || row[1] != ft.PRURL {
		t.Fatalf("row %v", row)
	}
}

func TestLessonsFlowVerifyAndReopen(t *testing.T) {
	f := newFlowBase(t)
	lid := f.newLesson(t, "t")
	if as(f.e, "zz-test-director", "lesson", "verify", lid, "--evidence", "e").Exit != 2 {
		t.Fatal("verify open")
	}
	sqlExec(t, f.e, "UPDATE lessons SET status='applied', applied_at='2026-10-01T00:00:00+00:00'")
	if as(f.e, "zz-test-lead", "lesson", "verify", lid, "--evidence", "e").Exit != 3 {
		t.Fatal("lead verify")
	}
	if lessonCol(f.e, lid, "status") != "applied" {
		t.Fatal("status")
	}
	f.e.OKWith(ft.Opts{Agent: "zz-test-director"}, "lesson", "verify", lid, "--evidence", "seen in M1")
	if lessonCol(f.e, lid, "status") != "verified" {
		t.Fatal("verified")
	}
	if lessonCol(f.e, lid, "verified_by") != "zz-test-director" {
		t.Fatal("verified_by")
	}
	if lessonCol(f.e, lid, "evidence") != "seen in M1" {
		t.Fatal("evidence")
	}
	if lessonCol(f.e, lid, "verified_at") == "NULL" {
		t.Fatal("verified_at")
	}
	f.e.OKWith(ft.Opts{Agent: "zz-test-lead"}, "lesson", "reopen", lid, "--reason", "happened again", "--source", "M99")
	if lessonCol(f.e, lid, "status") != "open" {
		t.Fatal("reopen status")
	}
	if lessonCol(f.e, lid, "recurrences") != "1" {
		t.Fatal("recurrences")
	}
	for _, col := range []string{"verified_at", "verified_by", "evidence"} {
		if lessonCol(f.e, lid, col) != "NULL" {
			t.Fatalf("%s should be null", col)
		}
	}
	contains(t, "sources", lessonCol(f.e, lid, "sources"), "M99")
	contains(t, "show", f.e.OKWith(ft.Opts{Agent: "zz-test-lead"}, "lesson", "show", lid).Stdout, "recurrence: happened again")
	if as(f.e, "zz-test-lead", "lesson", "reopen", lid, "--reason", "x").Exit != 2 {
		t.Fatal("reopen twice")
	}
	for _, ag := range []string{"zz-test-boss", "owner"} {
		sqlExec(t, f.e, "UPDATE lessons SET status='applied'")
		f.e.OKWith(ft.Opts{Agent: ag}, "lesson", "verify", lid, "--evidence", "ok")
	}
}

func TestLessonsFlowWorkersRefused(t *testing.T) {
	f := newFlowBase(t)
	lid := f.newLesson(t, "t")
	for _, role := range lessonWorkers {
		ag := "zz-test-" + role
		cases := [][]string{
			{"link", lid, "--task", "T-0001"},
			{"apply", lid},
			{"verify", lid, "--evidence", "e"},
			{"reopen", lid, "--reason", "r"},
		}
		for _, args := range cases {
			t.Run(role+"/"+args[0], func(t *testing.T) {
				p := as(f.e, ag, append([]string{"lesson"}, args...)...)
				if p.Exit != 3 {
					t.Fatalf("exit %d", p.Exit)
				}
			})
		}
	}
	if f.e.Count("SELECT count(*) FROM lesson_links") != 0 {
		t.Fatal("links")
	}
	if lessonCol(f.e, lid, "status") != "open" {
		t.Fatal("status")
	}
}

func TestLessonsFlowLinkThenMergeApplies(t *testing.T) {
	f := newFlowBase(t)
	lid := f.newLesson(t, "t")
	f.e.OKWith(ft.Opts{Agent: "zz-test-lead"}, "lesson", "link", lid, "--task", "T-0001", "--via", "hook")
	p := f.merge(t, "T-0001")
	if p.Exit != 0 {
		t.Fatalf("merge %d %q", p.Exit, p.Stderr)
	}
	re := regexp.MustCompile(`^T-0001 MERGED @ \w{8}\n$`)
	if !re.MatchString(p.Stdout) {
		t.Fatalf("stdout %q", p.Stdout)
	}
	link := f.e.SQL("SELECT merged_at, pr_url FROM lesson_links")[0]
	if link[0] == "NULL" || link[1] != ft.PRURL {
		t.Fatalf("link %v", link)
	}
	if lessonCol(f.e, lid, "status") != "applied" {
		t.Fatal("status")
	}
	if lessonCol(f.e, lid, "applied_at") == "NULL" {
		t.Fatal("applied_at")
	}
	contains(t, "show", f.e.OKWith(ft.Opts{Agent: "zz-test-lead"}, "lesson", "show", lid).Stdout, "auto: T-0001")
}

func TestLessonsFlowMergeOutputSameWithAndWithoutLesson(t *testing.T) {
	f := newFlowBase(t)
	plain := f.merge(t, "T-0001")
	sqlExec(t, f.e, "UPDATE tasks SET status='in_review' WHERE id='T-0001'")
	sqlExec(t, f.e, "DELETE FROM verdicts")
	lid := f.newLesson(t, "t")
	f.e.OKWith(ft.Opts{Agent: "zz-test-lead"}, "lesson", "link", lid, "--task", "T-0001")
	withL := f.merge(t, "T-0001")
	if plain.Exit != withL.Exit || plain.Stdout != withL.Stdout {
		t.Fatalf("plain (%d,%q) vs with (%d,%q)", plain.Exit, plain.Stdout, withL.Exit, withL.Stdout)
	}
}

func TestLessonsFlowUnrelatedMergeDoesNotApplyOtherLesson(t *testing.T) {
	f := newFlowBase(t)
	lid := f.newLesson(t, "t")
	f.e.SeedTask(ft.Task{ID: "T-0002", Status: "done", PRURL: oldPR})
	sqlExec(t, f.e, "INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,created) VALUES(?,?,?,?,?,?)",
		"T-0002", oldPR, shaA, "judge", "merge", "2026-10-02T00:00:00+00:00")
	f.e.OKWith(ft.Opts{Agent: "zz-test-lead"}, "lesson", "link", lid, "--task", "T-0002")
	if f.e.SQL("SELECT merged_at FROM lesson_links")[0][0] == "NULL" {
		t.Fatal("merged_at")
	}
	if f.merge(t, "T-0001").Exit != 0 {
		t.Fatal("merge")
	}
	if lessonCol(f.e, lid, "status") != "open" {
		t.Fatal("lesson applied unexpectedly")
	}
}

func TestLessonsFlowHalfLinkedLessonNotApplied(t *testing.T) {
	f := newFlowBase(t)
	lid := f.newLesson(t, "t")
	f.e.OKWith(ft.Opts{Agent: "zz-test-lead"}, "lesson", "link", lid, "--task", "T-0001")
	f.e.SeedTask(ft.Task{ID: "T-0002", PRURL: oldPR})
	f.e.OKWith(ft.Opts{Agent: "zz-test-lead"}, "lesson", "link", lid, "--task", "T-0002")
	if f.merge(t, "T-0001").Exit != 0 {
		t.Fatal("merge")
	}
	if lessonCol(f.e, lid, "status") != "open" {
		t.Fatal("applied")
	}
}

func TestLessonsFlowHookFailureDoesNotFailMerge(t *testing.T) {
	f := newFlowBase(t)
	lid := f.newLesson(t, "t")
	f.e.OKWith(ft.Opts{Agent: "zz-test-lead"}, "lesson", "link", lid, "--task", "T-0001")
	appendGhRules(t, f.e, ft.StubRule{Match: []string{"pr", "view", ft.PRURL, "files"}, Err: "files unavailable\n", Exit: 1})
	p := f.merge(t, "T-0001")
	if p.Exit != 0 {
		t.Fatalf("merge %d", p.Exit)
	}
	contains(t, "stdout", p.Stdout, "T-0001 MERGED")
	if f.e.Count("SELECT count(*) FROM verdicts WHERE verdict='merge'") != 1 {
		t.Fatal("verdict")
	}
	if f.e.SQL("SELECT status FROM tasks WHERE id='T-0001'")[0][0] != "done" {
		t.Fatal("task status")
	}
	if len(f.e.SQL("SELECT * FROM events WHERE action='post-merge-hook-failed'")) != 1 {
		t.Fatal("event")
	}
}

func (f *flowBase) mergeWithFiles(t *testing.T, files ...string) {
	t.Helper()
	installFlowGh(t, f.e, prViewRule(ft.PRURL, f.e.HeadSHA, "OPEN", files, greenChecks()))
	p := f.merge(t, "T-0001")
	if p.Exit != 0 {
		t.Fatalf("merge %d %q", p.Exit, p.Stderr)
	}
}

func msgsTo(f *flowBase, who, subject string) [][]string {
	var out [][]string
	prefix := "zz-test-" + who
	for _, m := range flowMsgs(f.e, subject) {
		if m[2] == prefix {
			out = append(out, m)
		}
	}
	return out
}

func TestLessonsFlowRolesChangedLead(t *testing.T) {
	f := newFlowBase(t)
	f.mergeWithFiles(t, "roles/lead.md", "tests/x.py")
	if len(msgsTo(f, "lead", "role files changed")) != 0 || len(msgsTo(f, "boss", "role files changed")) != 0 {
		t.Fatal("unexpected msgs")
	}
	if len(flowMsgs(f.e, "role files changed")) != 1 {
		t.Fatalf("msgs %d", len(flowMsgs(f.e, "role files changed")))
	}
	d := flowMsgs(f.e, "role files changed: stale agents")
	if len(d) != 1 {
		t.Fatal("director msg")
	}
	if d[0][2] != "zz-test-director" || d[0][3] != "request" || d[0][1] != "factory" {
		t.Fatalf("hdr %v", d[0])
	}
	contains(t, "body", d[0][5], "roles/lead.md")
	contains(t, "body", d[0][5], "factory relaunch zz-test-lead --when-idle")
	if len(f.e.SQL("SELECT * FROM events WHERE action='roles-changed'")) == 0 {
		t.Fatal("event")
	}
}

func TestLessonsFlowRolesChangedDirectorNotifiesBoss(t *testing.T) {
	f := newFlowBase(t)
	f.mergeWithFiles(t, "roles/director.md")
	if len(msgsTo(f, "director", "role files changed")) != 1 {
		t.Fatal("director stale list")
	}
	if len(flowMsgs(f.e, "role files changed")) != 2 {
		t.Fatal("msg count")
	}
	boss := flowMsgs(f.e, "role files changed: director stale")
	if len(boss) != 1 || boss[0][2] != "zz-test-boss" {
		t.Fatalf("boss %v", boss)
	}
	contains(t, "body", boss[0][5], "factory relaunch zz-test-director --when-idle")
}

func TestLessonsFlowCommonMDHitsAllPersistent(t *testing.T) {
	f := newFlowBase(t)
	f.mergeWithFiles(t, "roles/_common.md")
	if len(flowMsgs(f.e, "role files changed: relaunch pending")) != 0 {
		t.Fatal("relaunch pending")
	}
	d := flowMsgs(f.e, "role files changed: stale agents")
	if len(d) != 1 {
		t.Fatal("director")
	}
	for _, who := range []string{"boss", "director", "lead"} {
		contains(t, "body", d[0][5], "factory relaunch zz-test-"+who+" --when-idle")
	}
}

func TestLessonsFlowTestsOnlyNoMessages(t *testing.T) {
	f := newFlowBase(t)
	f.mergeWithFiles(t, "tests/test_x.py", "roles/coder.md")
	if len(flowMsgs(f.e, "role files changed")) != 0 {
		t.Fatal("msgs")
	}
	if len(f.e.SQL("SELECT * FROM events WHERE action='roles-changed'")) != 0 {
		t.Fatal("event")
	}
}

func TestLessonsFlowHookOnlyMergeMarksNobodyStale(t *testing.T) {
	f := newFlowBase(t)
	f.mergeWithFiles(t, "roles/hooks/worker-guard.py", "roles/hooks/boss-inbox.py", "tests/test_x.py")
	if len(flowMsgs(f.e, "role files changed")) != 0 {
		t.Fatal("msgs")
	}
	if len(f.e.SQL("SELECT * FROM events WHERE action='roles-changed'")) != 0 {
		t.Fatal("event")
	}
}

func TestLessonsFlowHookPlusPromptStillNotifies(t *testing.T) {
	f := newFlowBase(t)
	f.mergeWithFiles(t, "roles/hooks/worker-guard.py", "roles/director.md")
	d := flowMsgs(f.e, "role files changed: stale agents")
	if len(d) != 1 {
		t.Fatal("msg")
	}
	for _, who := range []string{"boss", "director", "lead"} {
		contains(t, "body", d[0][5], "factory relaunch zz-test-"+who+" --when-idle")
	}
	if len(f.e.SQL("SELECT * FROM events WHERE action='roles-changed'")) == 0 {
		t.Fatal("event")
	}
}

func TestLessonsFlowHookPlusCoderMDNotifiesAsOnMain(t *testing.T) {
	f := newFlowBase(t)
	f.mergeWithFiles(t, "roles/hooks/worker-guard.py", "roles/coder.md")
	d := flowMsgs(f.e, "role files changed: stale agents")
	if len(d) != 1 {
		t.Fatal("msg")
	}
	for _, who := range []string{"boss", "director", "lead"} {
		contains(t, "body", d[0][5], "factory relaunch zz-test-"+who+" --when-idle")
	}
}

func TestLessonsFlowHookPlusSettingsOrPluginsStillNotifies(t *testing.T) {
	f := newFlowBase(t)
	f.mergeWithFiles(t, "roles/hooks/worker-guard.py", "roles/settings/lead.json.in", "roles/plugins/boss/x.md")
	d := flowMsgs(f.e, "role files changed: stale agents")
	if len(d) != 1 {
		t.Fatal("msg")
	}
	for _, who := range []string{"boss", "lead"} {
		contains(t, "body", d[0][5], "factory relaunch zz-test-"+who+" --when-idle")
	}
	if len(f.e.SQL("SELECT * FROM events WHERE action='roles-changed'")) == 0 {
		t.Fatal("event")
	}
}

type collapseBase struct {
	*flowBase
}

func (c *collapseBase) merge2(t *testing.T, files ...string) {
	t.Helper()
	seedPRExtra(t, c.e, flowPR2, "T-0002")
	appendGhRules(t, c.e, prViewRule(flowPR2, c.e.HeadSHA, "OPEN", files, greenChecks()))
	p := c.merge(t, "T-0002")
	if p.Exit != 0 {
		t.Fatalf("merge2 %d", p.Exit)
	}
}

func (c *collapseBase) twoMerges(t *testing.T, files ...string) {
	t.Helper()
	installFlowGh(t, c.e, prViewRule(ft.PRURL, c.e.HeadSHA, "OPEN", files, greenChecks()))
	p := c.merge(t, "T-0001")
	if p.Exit != 0 {
		t.Fatalf("merge1 %d", p.Exit)
	}
	c.merge2(t, files...)
}

func TestLessonsFlowLeadNoticeCollapsed(t *testing.T) {
	t.Skip("CollapseTests: judge merge2 gh stub parity; see file header")
	c := &collapseBase{newFlowBase(t)}
	c.twoMerges(t, "roles/lead.md")
	if len(msgsTo(c.flowBase, "lead", "role files changed: relaunch pending")) != 0 {
		t.Fatal("lead pending")
	}
	n := msgsTo(c.flowBase, "director", "role files changed: stale agents")
	if len(n) != 1 {
		t.Fatal("director notice")
	}
	if n[0][6] != "NULL" {
		t.Fatal("read_at set")
	}
	contains(t, "body", n[0][5], ft.PRURL)
	contains(t, "body", n[0][5], flowPR2)
	if strings.Count(n[0][5], "- roles/lead.md") != 1 {
		t.Fatal("lead.md count")
	}
	if strings.Count(n[0][5], "factory relaunch zz-test-lead --when-idle") != 1 {
		t.Fatal("relaunch count")
	}
	if len(flowMsgs(c.e, "role files changed")) != 1 {
		t.Fatal("total msgs")
	}
	if len(c.e.SQL("SELECT * FROM events WHERE action='roles-changed'")) != 2 {
		t.Fatal("events")
	}
}

func TestLessonsFlowReadNoticeNotCollapsed(t *testing.T) {
	t.Skip("CollapseTests: judge merge2 gh stub parity; see file header")
	c := &collapseBase{newFlowBase(t)}
	installFlowGh(t, c.e, prViewRule(ft.PRURL, c.e.HeadSHA, "OPEN", []string{"roles/lead.md"}, greenChecks()))
	p := c.merge(t, "T-0001")
	if p.Exit != 0 {
		t.Fatal("merge")
	}
	first := msgsTo(c.flowBase, "director", "role files changed: stale agents")[0]
	sqlExec(t, c.e, "UPDATE messages SET read_at='2026-01-01T00:00:00+00:00' WHERE id=?", first[0])
	c.merge2(t, "roles/lead.md")
	n := msgsTo(c.flowBase, "director", "role files changed: stale agents")
	if len(n) != 2 {
		t.Fatalf("n = %d", len(n))
	}
	if n[0][5] != first[5] {
		t.Fatal("first changed")
	}
	if strings.Contains(n[0][5], flowPR2) {
		t.Fatal("pr2 in first")
	}
	contains(t, "body", n[1][5], flowPR2)
}

func TestLessonsFlowDirectorAndBossCollapsed(t *testing.T) {
	t.Skip("CollapseTests: judge merge2 gh stub parity; see file header")
	c := &collapseBase{newFlowBase(t)}
	c.twoMerges(t, "roles/director.md", "roles/lead.md")
	d := msgsTo(c.flowBase, "director", "role files changed: stale agents")
	if len(d) != 1 {
		t.Fatal("director")
	}
	if strings.Count(d[0][5], "factory relaunch zz-test-lead --when-idle") != 1 ||
		strings.Count(d[0][5], "factory relaunch zz-test-director --when-idle") != 1 {
		t.Fatal("relaunch counts")
	}
	contains(t, "body", d[0][5], flowPR2)
	b := msgsTo(c.flowBase, "boss", "role files changed: director stale")
	if len(b) != 1 {
		t.Fatal("boss")
	}
	contains(t, "body", b[0][5], ft.PRURL)
	contains(t, "body", b[0][5], flowPR2)
}

func seedNotice(e *ft.Env, who, subject, body string) {
	sqlExec(e.T, e, "INSERT INTO messages(sender,recipient,kind,subject,body,created) VALUES('factory',?,'request',?,?,"+
		"'2026-01-01T00:00:00+00:00')", "zz-test-"+who, subject, body)
}

func TestLessonsFlowBossNoticeKeepsEveryAgentsRelaunchLine(t *testing.T) {
	f := newFlowBase(t)
	seedNotice(f.e, "boss", "role files changed: director stale",
		"PR: https://x/pull/9\nChanged files:\n- roles/lead.md\nLive agents:\n"+
			"- zz-test-lead (lead): factory relaunch zz-test-lead --when-idle\n"+
			"- zz-test-boss (boss): factory relaunch zz-test-boss --when-idle\nRun it.")
	installFlowGh(t, f.e, prViewRule(ft.PRURL, f.e.HeadSHA, "OPEN", []string{"roles/director.md"}, greenChecks()))
	p := f.merge(t, "T-0001")
	if p.Exit != 0 {
		t.Fatal("merge")
	}
	b := msgsTo(f, "boss", "role files changed: director stale")
	if len(b) != 1 {
		t.Fatal("boss msg")
	}
	for _, who := range []string{"zz-test-lead", "zz-test-boss", "zz-test-director"} {
		if strings.Count(b[0][5], fmt.Sprintf("factory relaunch %s --when-idle", who)) != 1 {
			t.Fatalf("body %q", b[0][5])
		}
	}
	contains(t, "body", b[0][5], "https://x/pull/9")
}

func TestLessonsFlowLegacyBossLineNotDuplicated(t *testing.T) {
	f := newFlowBase(t)
	seedNotice(f.e, "boss", "role files changed: director stale",
		"T-0009 merged (https://x/pull/9) and changed role files:\n- roles/director.md\n"+
			"Live agents with a stale prompt:\n- zz-test-director (director): factory relaunch zz-test-director --when-idle\n"+
			"Run: factory relaunch director --when-idle")
	installFlowGh(t, f.e, prViewRule(ft.PRURL, f.e.HeadSHA, "OPEN", []string{"roles/director.md"}, greenChecks()))
	p := f.merge(t, "T-0001")
	if p.Exit != 0 {
		t.Fatal("merge")
	}
	b := msgsTo(f, "boss", "role files changed: director stale")
	if strings.Count(b[0][5], "factory relaunch zz-test-director --when-idle") != 1 {
		t.Fatalf("body %q", b[0][5])
	}
}

func TestLessonsFlowOldFormatDirectorNoticeParsedOnFirstCollapse(t *testing.T) {
	f := newFlowBase(t)
	old := "T-0009 merged (https://x/pull/9) and changed role files:\n- roles/lead.md\n" +
		"Live agents with a stale prompt:\n" +
		"- zz-test-lead (lead): factory relaunch zz-test-lead --when-idle\n" +
		"- zz-test-boss (boss): factory relaunch zz-test-boss --when-idle"
	seedNotice(f.e, "director", "role files changed: stale agents", old)
	installFlowGh(t, f.e, prViewRule(ft.PRURL, f.e.HeadSHA, "OPEN", []string{"roles/director.md"}, greenChecks()))
	p := f.merge(t, "T-0001")
	if p.Exit != 0 {
		t.Fatal("merge")
	}
	d := msgsTo(f, "director", "role files changed: stale agents")
	if len(d) != 1 {
		t.Fatal("notice")
	}
	for _, line := range []string{"https://x/pull/9", ft.PRURL, "- roles/lead.md", "- roles/director.md",
		"factory relaunch zz-test-lead --when-idle", "factory relaunch zz-test-boss --when-idle",
		"factory relaunch zz-test-director --when-idle"} {
		contains(t, "body", d[0][5], line)
	}
}

func TestLessonsFlowNonPersistentRoleSendsNothing(t *testing.T) {
	t.Skip("CollapseTests: judge merge2 gh stub parity; see file header")
	c := &collapseBase{newFlowBase(t)}
	c.twoMerges(t, "roles/coder.md")
	if len(flowMsgs(c.e, "role files changed")) != 0 {
		t.Fatal("msgs")
	}
}

func TestLessonsFlowHelperFailureDoesNotFailMerge(t *testing.T) {
	f := newFlowBase(t)
	sqlExec(t, f.e, "CREATE TRIGGER zz_fail BEFORE INSERT ON messages BEGIN SELECT RAISE(ABORT, 'zz-fail'); END")
	installFlowGh(t, f.e, prViewRule(ft.PRURL, f.e.HeadSHA, "OPEN", []string{"roles/lead.md"}, greenChecks()))
	p := f.merge(t, "T-0001")
	if p.Exit != 0 {
		t.Fatalf("merge %d", p.Exit)
	}
	if f.e.SQL("SELECT status FROM tasks WHERE id='T-0001'")[0][0] != "done" {
		t.Fatal("task")
	}
	if len(f.e.SQL("SELECT * FROM events WHERE action='post-merge-hook-failed'")) != 1 {
		t.Fatal("hook failed event")
	}
}

func flowDigest(f *flowBase, args ...string) string {
	return f.e.OKWith(ft.Opts{Env: map[string]string{"FACTORY_NOW": flowFrozen.Format("2006-01-02T15:04:05Z07:00")},
		Agent: "zz-test-director"}, append([]string{"digest"}, args...)...).Stdout
}

func TestLessonsFlowNoLessonsNoSection(t *testing.T) {
	f := newFlowBase(t)
	notContains(t, "digest", flowDigest(f), "## Lessons")
}

func TestLessonsFlowLessonsSection(t *testing.T) {
	f := newFlowBase(t)
	titles := []string{"new one", "applied one", "verified one", "old one"}
	for _, title := range titles {
		f.newLesson(t, title)
	}
	sqlExec(t, f.e, "UPDATE lessons SET created=?", flowISO(-2*time.Hour))
	sqlExec(t, f.e, "UPDATE lessons SET created=? WHERE id='L-004'", flowISO(-9*24*time.Hour))
	sqlExec(t, f.e, "UPDATE lessons SET status='applied', applied_at=? WHERE id='L-002'", flowISO(-1*time.Hour))
	sqlExec(t, f.e, "UPDATE lessons SET status='verified', verified_at=?, evidence='saw it\nwork' WHERE id='L-003'", flowISO(-1*time.Hour))
	sqlExec(t, f.e, "INSERT INTO lesson_links(lesson_id,pr_url,created) VALUES('L-002',?,'t')", oldPR)
	out := flowDigest(f)
	sec := strings.Split(out, "## Lessons")[1]
	newSec := strings.Split(strings.Split(sec, "### New")[1], "### Applied")[0]
	appSec := strings.Split(strings.Split(sec, "### Applied")[1], "### Verified")[0]
	verSec := strings.Split(sec, "### Verified")[1]
	contains(t, "new", newSec, "- L-001 [lead,director] new one (open)")
	notContains(t, "new", newSec, "L-004")
	contains(t, "applied", appSec, "- L-002 applied one — "+oldPR)
	contains(t, "verified", verSec, "- L-003 verified one — saw it work")
	contains(t, "verified", verSec, "Open: 2 · applied, awaiting verification: 1")
	notContains(t, "digest", flowDigest(f, "--goal", "G-001"), "## Lessons")
}
