package s2msgs

import (
	"testing"

	ft "factory/go/internal/factorytest"
)

// Port of tests/test_report_default.py (9 cases).

var reportWorkers = []string{"pm", "coder", "qa", "reviewer", "judge", "cleaner"}

func newReportDefaultEnv(t *testing.T) *ft.Env {
	t.Helper()
	t.Parallel()
	e := ft.New(t)
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedAgent(ft.AgentRow{Name: "zz-test-director", Role: "director"})
	e.SeedAgent(ft.AgentRow{Name: "zz-test-boss", Role: "boss"})
	e.SeedAgent(ft.AgentRow{Name: "lead-g-001-a", Role: "lead", Goal: "G-001", Team: "g-001-a"})
	e.SeedTeam("g-001-a", "G-001", "lead-g-001-a")
	return e
}

func TestReportDefaultWorkerWithTeamDefaultsToLead(t *testing.T) {
	for _, role := range reportWorkers {
		t.Run(role, func(t *testing.T) {
			e := newReportDefaultEnv(t)
			name := "zz-test-" + role
			e.SeedAgent(ft.AgentRow{Name: name, Role: role, Goal: "G-001", Team: "g-001-a"})
			p, rcpt := report(e, name)
			if p.Exit != 0 {
				t.Fatalf("exit %d: %s", p.Exit, p.Stderr)
			}
			contains(t, "stdout", p.Stdout, "report → lead-g-001-a")
			if rcpt != "lead-g-001-a" {
				t.Fatalf("recipient = %q, want lead-g-001-a", rcpt)
			}
		})
	}
}

func TestReportDefaultHQAndLeadDefaultUnchanged(t *testing.T) {
	for _, name := range []string{"zz-test-boss", "zz-test-director", "lead-g-001-a"} {
		t.Run(name, func(t *testing.T) {
			e := newReportDefaultEnv(t)
			p, rcpt := report(e, name)
			if p.Exit != 0 {
				t.Fatalf("exit %d: %s", p.Exit, p.Stderr)
			}
			if rcpt != "zz-test-director" {
				t.Fatalf("recipient = %q, want zz-test-director", rcpt)
			}
		})
	}
}

func TestReportDefaultOwnerDefaultUnchanged(t *testing.T) {
	e := newReportDefaultEnv(t)
	p, rcpt := report(e, "owner")
	if p.Exit != 0 {
		t.Fatalf("exit %d: %s", p.Exit, p.Stderr)
	}
	if rcpt != "zz-test-director" {
		t.Fatalf("recipient = %q, want zz-test-director", rcpt)
	}
}

func TestReportDefaultExplicitToHonoured(t *testing.T) {
	e := newReportDefaultEnv(t)
	e.SeedAgent(ft.AgentRow{Name: "zz-test-coder", Role: "coder", Goal: "G-001", Team: "g-001-a"})
	if _, rcpt := report(e, "zz-test-coder", "--to", "director"); rcpt != "zz-test-director" {
		t.Fatalf("to director: %q", rcpt)
	}
	if _, rcpt := report(e, "zz-test-coder", "--to", "boss"); rcpt != "zz-test-boss" {
		t.Fatalf("to boss: %q", rcpt)
	}
	if _, rcpt := report(e, "zz-test-director", "--to", "lead:g-001-a"); rcpt != "lead-g-001-a" {
		t.Fatalf("to lead: %q", rcpt)
	}
}

func TestReportDefaultWorkerWithoutTeamDefaultsToDirector(t *testing.T) {
	e := newReportDefaultEnv(t)
	e.SeedAgent(ft.AgentRow{Name: "zz-test-coder", Role: "coder", Goal: "G-001"})
	p, rcpt := report(e, "zz-test-coder")
	if p.Exit != 0 {
		t.Fatalf("exit %d: %s", p.Exit, p.Stderr)
	}
	if rcpt != "zz-test-director" {
		t.Fatalf("recipient = %q, want zz-test-director", rcpt)
	}
}

func TestReportDefaultWorkerTeamWithoutLeadFallsBack(t *testing.T) {
	e := newReportDefaultEnv(t)
	e.SeedTeam("g-001-b", "G-001", "")
	e.SeedAgent(ft.AgentRow{Name: "zz-test-coder", Role: "coder", Goal: "G-001", Team: "g-001-b"})
	p, rcpt := report(e, "zz-test-coder")
	if p.Exit != 0 {
		t.Fatalf("exit %d: %s", p.Exit, p.Stderr)
	}
	if rcpt != "zz-test-director" {
		t.Fatalf("recipient = %q, want zz-test-director", rcpt)
	}
	contains(t, "stderr", p.Stderr, "has no lead")
}

func TestReportDefaultStaffEngineerDefaultsToDirector(t *testing.T) {
	e := newReportDefaultEnv(t)
	e.SeedAgent(ft.AgentRow{Name: "staff-engineer", Role: "staff-engineer"})
	p, rcpt := report(e, "staff-engineer")
	if p.Exit != 0 {
		t.Fatalf("exit %d: %s", p.Exit, p.Stderr)
	}
	if rcpt != "zz-test-director" {
		t.Fatalf("recipient = %q, want zz-test-director", rcpt)
	}
}

func TestReportDefaultWorkerReportToStaffEngineerRefusedLikeMsg(t *testing.T) {
	e := newReportDefaultEnv(t)
	e.NeedPython()
	e.SeedAgent(ft.AgentRow{Name: "zz-test-coder", Role: "coder", Goal: "G-001", Team: "g-001-a"})
	r := as(e, "zz-test-coder", "report", "x", "y", "--to", "staff-engineer")
	m := as(e, "zz-test-coder", "msg", "staff-engineer", "x", "y")
	if r.Exit != 3 {
		t.Fatalf("report exit %d, want 3\n%s", r.Exit, r.Stderr)
	}
	if r.Exit != m.Exit || r.Stderr != m.Stderr {
		t.Fatalf("report (%d, %q) != msg (%d, %q)", r.Exit, r.Stderr, m.Exit, m.Stderr)
	}
	contains(t, "stderr", r.Stderr, "may not message staff-engineer")
	if len(e.SQL("SELECT 1 FROM messages WHERE kind='report'")) != 0 {
		t.Fatal("report message was stored")
	}
}

func TestReportDefaultLeadAndDirectorReportToStaffEngineerAllowed(t *testing.T) {
	for _, name := range []string{"lead-g-001-a", "zz-test-director"} {
		t.Run(name, func(t *testing.T) {
			e := newReportDefaultEnv(t)
			e.NeedPython()
			p, rcpt := report(e, name, "--to", "staff-engineer")
			if p.Exit != 0 {
				t.Fatalf("exit %d: %s", p.Exit, p.Stderr)
			}
			if rcpt != "staff-engineer" {
				t.Fatalf("recipient = %q, want staff-engineer", rcpt)
			}
		})
	}
}
