// Go tests for tests/test_inbox.py (31 cases): InboxTests and InboxTerseTests.
package s2msgs

import (
	"fmt"
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
)

func inboxBase(t *testing.T) *ft.Env {
	t.Helper()
	slot(t)
	e := ft.New(t)
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedTeam("g-001-a", "G-001", "zz-test-lead")
	e.SeedAgent(ft.AgentRow{Name: "zz-test-coder", Role: "coder", Goal: "G-001", Team: "g-001-a"})
	e.SeedAgent(ft.AgentRow{Name: "zz-test-lead", Role: "lead", Goal: "G-001", Team: "g-001-a"})
	e.SeedAgent(ft.AgentRow{Name: "zz-test-director", Role: "director"})
	return e
}

func inboxTerseBase(t *testing.T) *ft.Env {
	t.Helper()
	slot(t)
	e := ft.New(t)
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedTeam("g-001-a", "G-001", "zz-test-lead")
	e.SeedAgent(ft.AgentRow{Name: "zz-test-coder", Role: "coder", Goal: "G-001", Team: "g-001-a"})
	e.SeedAgent(ft.AgentRow{Name: "zz-test-coder2", Role: "coder", Goal: "G-001", Team: "g-001-a"})
	e.SeedAgent(ft.AgentRow{Name: "zz-test-lead", Role: "lead", Goal: "G-001", Team: "g-001-a"})
	e.SeedAgent(ft.AgentRow{Name: "zz-test-director", Role: "director"})
	e.SeedAgent(ft.AgentRow{Name: "boss", Role: "boss"})
	return e
}

func recipient(t *testing.T, e *ft.Env, to string) string {
	t.Helper()
	p := ok(t, e, "zz-test-coder", nil, "msg", to, "subj", "body")
	if !msgIDRe.MatchString(p.Stdout) {
		t.Fatalf("msg stdout %q, want ^M\\d+ →", p.Stdout)
	}
	return sql1(t, e, "SELECT recipient FROM messages ORDER BY id DESC LIMIT 1")
}

func tereteSend(t *testing.T, e *ft.Env, to, kind, subject, body, sender string) string {
	t.Helper()
	ok(t, e, sender, nil, "msg", to, subject, body, "--kind", kind)
	return sql1(t, e, "SELECT id FROM messages ORDER BY id DESC LIMIT 1")
}

func TestInboxRecipientAgentName(t *testing.T) {
	e := inboxBase(t)
	if got := recipient(t, e, "zz-test-lead"); got != "zz-test-lead" {
		t.Fatalf("recipient = %q", got)
	}
}

func TestInboxRecipientRoleName(t *testing.T) {
	e := inboxBase(t)
	if got := recipient(t, e, "director"); got != "zz-test-director" {
		t.Fatalf("recipient = %q", got)
	}
}

func TestInboxRecipientRolePicksNewestLive(t *testing.T) {
	e := inboxBase(t)
	e.SeedAgent(ft.AgentRow{Name: "zz-test-lead2", Role: "lead", Status: "live", Started: "2026-02-01T00:00:00+00:00"})
	e.SeedAgent(ft.AgentRow{Name: "zz-test-lead3", Role: "lead", Status: "exited", Started: "2026-03-01T00:00:00+00:00"})
	if got := recipient(t, e, "lead"); got != "zz-test-lead2" {
		t.Fatalf("recipient = %q", got)
	}
}

func TestInboxRecipientLeadTeam(t *testing.T) {
	e := inboxBase(t)
	if got := recipient(t, e, "lead:g-001-a"); got != "zz-test-lead" {
		t.Fatalf("recipient = %q", got)
	}
}

func TestInboxRecipientLeadTeamWithoutLeadFails(t *testing.T) {
	e := inboxBase(t)
	e.SeedTeam("g-001-b", "G-001", "")
	p := refused(t, e, "zz-test-coder", 1, nil, "msg", "lead:g-001-b", "s", "b")
	contains(t, "stderr", p.Stderr, "no lead")
}

func TestInboxRecipientOwnerGoesToBoss(t *testing.T) {
	e := inboxBase(t)
	if got := recipient(t, e, "owner"); got != "boss" {
		t.Fatalf("recipient = %q", got)
	}
}

func TestInboxRecipientBossWithoutAgentRow(t *testing.T) {
	e := inboxBase(t)
	if got := recipient(t, e, "boss"); got != "boss" {
		t.Fatalf("recipient = %q", got)
	}
}

func TestInboxRecipientUnknownFails(t *testing.T) {
	e := inboxBase(t)
	p := refused(t, e, "zz-test-coder", 1, nil, "msg", "zz-test-nobody", "s", "b")
	contains(t, "stderr", p.Stderr, "unknown recipient")
}

func TestInboxMarksRead(t *testing.T) {
	e := inboxBase(t)
	ok(t, e, "zz-test-coder", nil, "msg", "zz-test-lead", "first", "body one")
	p := ok(t, e, "zz-test-lead", nil, "inbox")
	contains(t, "stdout", p.Stdout, "first")
	contains(t, "stdout", p.Stdout, "from zz-test-coder")
	contains(t, "stdout", p.Stdout, "body one")
	contains(t, "stdout", ok(t, e, "zz-test-lead", nil, "inbox").Stdout, "inbox empty")
	if c := sql1(t, e, "SELECT count(*) FROM messages WHERE read_at IS NULL"); c != "0" {
		t.Fatalf("unread count = %s", c)
	}
}

func TestInboxPeekDoesNotMarkRead(t *testing.T) {
	e := inboxBase(t)
	ok(t, e, "zz-test-coder", nil, "msg", "zz-test-lead", "first", "b")
	contains(t, "stdout", ok(t, e, "zz-test-lead", nil, "inbox", "--peek").Stdout, "first")
	contains(t, "stdout", ok(t, e, "zz-test-lead", nil, "inbox").Stdout, "first")
}

func TestInboxAllShowsRead(t *testing.T) {
	e := inboxBase(t)
	ok(t, e, "zz-test-coder", nil, "msg", "zz-test-lead", "first", "b")
	ok(t, e, "zz-test-lead", nil, "inbox")
	contains(t, "stdout", ok(t, e, "zz-test-lead", nil, "inbox").Stdout, "inbox empty")
	contains(t, "stdout", ok(t, e, "zz-test-lead", nil, "inbox", "--all").Stdout, "first")
}

func TestInboxAgentFlagDoesNotMarkRead(t *testing.T) {
	e := inboxBase(t)
	ok(t, e, "zz-test-coder", nil, "msg", "zz-test-lead", "first", "b")
	p := ok(t, e, "zz-test-director", nil, "inbox", "--agent", "zz-test-lead")
	contains(t, "stdout", p.Stdout, "first")
	if c := sql1(t, e, "SELECT count(*) FROM messages WHERE read_at IS NULL"); c != "1" {
		t.Fatalf("unread count = %s", c)
	}
	contains(t, "stdout", ok(t, e, "zz-test-lead", nil, "inbox").Stdout, "first")
}

func TestInboxOnlyOwnMessages(t *testing.T) {
	e := inboxBase(t)
	ok(t, e, "zz-test-coder", nil, "msg", "zz-test-lead", "for lead", "b")
	contains(t, "stdout", ok(t, e, "zz-test-director", nil, "inbox").Stdout, "inbox empty")
}

func TestInboxTwoDashArgsExit2(t *testing.T) {
	e := inboxBase(t)
	r := e.RunWith(ft.Opts{Stdin: "text\n"}, "goal", "new", "--title", "zz-test", "--intent", "-", "--success", "-")
	if r.Exit != 2 {
		t.Fatalf("exit %d, want 2\nstderr: %s", r.Exit, r.Stderr)
	}
	contains(t, "stderr", r.Stderr, "only one argument per call can be '-'")
	if c := sql1(t, e, "SELECT count(*) FROM goals"); c != "1" {
		t.Fatalf("goal count = %s", c)
	}
}

func TestInboxSingleDashReadsStdin(t *testing.T) {
	e := inboxBase(t)
	r := e.RunWith(ft.Opts{Agent: "zz-test-coder", Stdin: "from stdin\n"}, "msg", "zz-test-lead", "long", "-")
	if r.Exit != 0 {
		t.Fatalf("msg exit %d: %s", r.Exit, r.Stderr)
	}
	got := sql1(t, e, "SELECT body FROM messages WHERE id=1")
	if got != "from stdin\n" && got != "from stdin" {
		t.Fatalf("body = %q", got)
	}
}

func TestInboxMessageKindAndIdsShown(t *testing.T) {
	e := inboxBase(t)
	e.SeedTask(ft.Task{ID: "T-0001", Status: "todo", PRURL: ""})
	ok(t, e, "zz-test-coder", nil, "msg", "zz-test-lead", "s", "b", "--kind", "report", "--goal", "G-001", "--task", "T-0001")
	out := ok(t, e, "zz-test-lead", nil, "inbox").Stdout
	contains(t, "stdout", out, "[report]")
	contains(t, "stdout", out, "goal=G-001 task=T-0001")
}

func TestInboxHqReportCollapsed(t *testing.T) {
	e := inboxTerseBase(t)
	mid := tereteSend(t, e, "director", "report", "subj", body10(), "zz-test-coder")
	out := ok(t, e, "zz-test-director", nil, "inbox").Stdout
	contains(t, "stdout", out, "line 1")
	contains(t, "stdout", out, fmt.Sprintf("(+9 lines: factory inbox --show M%s)", mid))
	notContains(t, "stdout", out, "line 5")
}

func TestInboxHqOtherKindsFull(t *testing.T) {
	kinds := []string{"request", "decision", "escalation", "alert", "msg"}
	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			e := inboxTerseBase(t)
			tereteSend(t, e, "director", kind, "subj", body10(), "zz-test-lead")
			out := ok(t, e, "zz-test-director", nil, "inbox").Stdout
			contains(t, "stdout", out, "line 10")
			notContains(t, "stdout", out, "--show")
		})
	}
}

func TestInboxHqShortReportFull(t *testing.T) {
	e := inboxTerseBase(t)
	tereteSend(t, e, "director", "report", "subj", "a\nb", "zz-test-coder")
	out := ok(t, e, "zz-test-director", nil, "inbox").Stdout
	contains(t, "stdout", out, "   a\n   b")
	notContains(t, "stdout", out, "--show")
}

func TestInboxHqLongSingleLineReportCollapsed(t *testing.T) {
	e := inboxTerseBase(t)
	tereteSend(t, e, "lead:g-001-a", "report", "subj", strings.Repeat("x", 500), "zz-test-coder")
	out := ok(t, e, "zz-test-lead", nil, "inbox").Stdout
	contains(t, "stdout", out, "(truncated: factory inbox --show M")
	notContains(t, "stdout", out, "+0 lines")
	notContains(t, "stdout", out, strings.Repeat("x", 201))
}

func TestInboxDailyDigestFull(t *testing.T) {
	e := inboxTerseBase(t)
	tereteSend(t, e, "boss", "report", "daily digest 2026-10-25", body10(), "zz-test-coder")
	contains(t, "stdout", ok(t, e, "boss", nil, "inbox").Stdout, "line 10")
}

func TestInboxBossReportCollapsed(t *testing.T) {
	e := inboxTerseBase(t)
	tereteSend(t, e, "boss", "report", "subj", body10(), "zz-test-coder")
	notContains(t, "stdout", ok(t, e, "boss", nil, "inbox").Stdout, "line 5")
}

func TestInboxWorkerReportFull(t *testing.T) {
	e := inboxTerseBase(t)
	ok(t, e, "zz-test-coder2", nil, "msg", "zz-test-coder", "subj", body10(), "--kind", "report")
	out := ok(t, e, "zz-test-coder", nil, "inbox").Stdout
	contains(t, "stdout", out, "line 10")
	notContains(t, "stdout", out, "--show")
}

func TestInboxFullFlag(t *testing.T) {
	e := inboxTerseBase(t)
	tereteSend(t, e, "director", "report", "subj", body10(), "zz-test-coder")
	contains(t, "stdout", ok(t, e, "zz-test-director", nil, "inbox", "--full").Stdout, "line 10")
}

func TestInboxShowRecipientKeepsReadAt(t *testing.T) {
	e := inboxTerseBase(t)
	ok(t, e, "zz-test-coder2", nil, "msg", "zz-test-coder", "subj", body10())
	mid := sql1(t, e, "SELECT id FROM messages")
	for _, arg := range []string{"M" + mid, mid} {
		out := ok(t, e, "zz-test-coder", nil, "inbox", "--show", arg).Stdout
		contains(t, "stdout", out, "line 10")
	}
	nullTime(t, e, "SELECT read_at FROM messages")
}

func TestInboxShowOtherWorkerRefused(t *testing.T) {
	e := inboxTerseBase(t)
	mid := tereteSend(t, e, "director", "report", "subj", body10(), "zz-test-coder")
	p := refused(t, e, "zz-test-coder2", 3, nil, "inbox", "--show", "M"+mid)
	contains(t, "stderr", p.Stderr, "not your message")
	notContains(t, "stdout", p.Stdout, "line 1")
}

func TestInboxShowSenderReadsOwnMessageKeepsReadAt(t *testing.T) {
	e := inboxTerseBase(t)
	ok(t, e, "zz-test-coder2", nil, "msg", "zz-test-coder", "subj", body10())
	mid := sql1(t, e, "SELECT id FROM messages")
	out := ok(t, e, "zz-test-coder2", nil, "inbox", "--show", "M"+mid).Stdout
	contains(t, "stdout", out, "line 10")
	nullTime(t, e, "SELECT read_at FROM messages")
}

func TestInboxShowDirectorAllowed(t *testing.T) {
	e := inboxTerseBase(t)
	mid := tereteSend(t, e, "lead:g-001-a", "report", "subj", body10(), "zz-test-coder")
	contains(t, "stdout", ok(t, e, "zz-test-director", nil, "inbox", "--show", "M"+mid).Stdout, "line 10")
}

func TestInboxShowUnknownID(t *testing.T) {
	e := inboxTerseBase(t)
	refused(t, e, "zz-test-director", 1, nil, "inbox", "--show", "M999")
}

func TestInboxLimitMarksOnlyPrinted(t *testing.T) {
	e := inboxTerseBase(t)
	for i := 0; i < 60; i++ {
		tereteSend(t, e, "zz-test-lead", "msg", fmt.Sprintf("s%d", i), "b", "zz-test-coder")
	}
	out := ok(t, e, "zz-test-lead", nil, "inbox").Stdout
	if !strings.HasPrefix(out, "(10 older unread not shown") {
		t.Fatalf("stdout prefix = %q", out[:min(80, len(out))])
	}
	if c := sql1(t, e, "SELECT count(*) FROM messages WHERE read_at IS NULL"); c != "10" {
		t.Fatalf("unread = %s", c)
	}
	out2 := ok(t, e, "zz-test-lead", nil, "inbox").Stdout
	notContains(t, "stdout", out2, "older unread")
	for i := 0; i < 10; i++ {
		contains(t, "stdout", out2, fmt.Sprintf("s%d\n", i))
	}
	if c := sql1(t, e, "SELECT count(*) FROM messages WHERE read_at IS NULL"); c != "0" {
		t.Fatalf("unread = %s", c)
	}
}

func TestInboxLimitZeroRefused(t *testing.T) {
	e := inboxTerseBase(t)
	tereteSend(t, e, "zz-test-lead", "msg", "subj", "b", "zz-test-coder")
	refused(t, e, "zz-test-lead", 1, nil, "inbox", "--limit", "0")
	if c := sql1(t, e, "SELECT count(*) FROM messages WHERE read_at IS NULL"); c != "1" {
		t.Fatalf("unread = %s", c)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
