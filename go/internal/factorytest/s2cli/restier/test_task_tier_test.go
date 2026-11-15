package s2restier

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
)

// Port of tests/test_task_tier.py (13 cases; see ported_s2cli_restier.txt).

func TestTaskTierShowPrintsTier(t *testing.T) {
	e := tierBaseEnv(t)
	as(e, "zz-lead", "task", "tier", "T-0001", "up", "T2")
	show := as(e, "zz-coder", "task", "show", "T-0001").Stdout
	if !strings.Contains(show, "tier: T2") {
		t.Fatalf("show = %q", show)
	}
}

func TestTaskTierUnsetTierReadsAsDash(t *testing.T) {
	e := tierBaseEnv(t)
	out := as(e, "zz-coder", "task", "tier", "T-0001").Stdout
	if strings.TrimSpace(out) != "-" {
		t.Fatalf("out = %q", out)
	}
}

func TestTaskTierUpRaisesAndNeverLowers(t *testing.T) {
	e := tierBaseEnv(t)
	as(e, "zz-rev", "task", "tier", "T-0001", "T1")
	as(e, "zz-lead", "task", "tier", "T-0001", "up", "T3")
	if tierOf(e) != "T3" {
		t.Fatalf("tier = %q", tierOf(e))
	}
	r := e.FailWith(ft.Opts{Agent: "zz-lead"}, 2, "task", "tier", "T-0001", "T1")
	if !strings.Contains(r.Stderr, "never lowered") {
		t.Fatalf("stderr = %q", r.Stderr)
	}
	if tierOf(e) != "T3" {
		t.Fatalf("tier after refuse = %q", tierOf(e))
	}
}

func TestTaskTierBadTierRefused(t *testing.T) {
	e := tierBaseEnv(t)
	e.FailWith(ft.Opts{Agent: "zz-lead"}, 2, "task", "tier", "T-0001", "up", "T9")
	if tierOf(e) != "" {
		t.Fatalf("tier = %q", tierOf(e))
	}
}

func TestTaskTierCoderCannotRaise(t *testing.T) {
	e := tierBaseEnv(t)
	e.FailWith(ft.Opts{Agent: "zz-coder"}, 3, "task", "tier", "T-0001", "up", "T3")
	if tierOf(e) != "" {
		t.Fatalf("tier = %q", tierOf(e))
	}
}

func TestTaskTierClassifyRefusedWhileFlagOff(t *testing.T) {
	e := tierBaseEnv(t)
	e.FailWith(ft.Opts{Agent: "zz-lead"}, 2, "task", "tier", "T-0001", "--classify", "--head", "main")
}

func TestTaskTierEveryRaiserRoleMayRaise(t *testing.T) {
	e := tierBaseEnv(t)
	for i, role := range []string{"qa", "judge", "director", "boss"} {
		e.SeedAgent(ft.AgentRow{Name: fmt.Sprintf("zz-%s", role), Role: role, Goal: "G-001"})
		as(e, fmt.Sprintf("zz-%s", role), "task", "tier", "T-0001", "up", fmt.Sprintf("T%d", i))
	}
	if tierOf(e) != "T3" {
		t.Fatalf("tier = %q", tierOf(e))
	}
}

func TestTaskTierTierAndClassifyTogetherRefused(t *testing.T) {
	e := tierBaseEnv(t)
	e.FailWith(ft.Opts{Agent: "zz-lead"}, 2, "task", "tier", "T-0001", "T2", "--classify")
}

func TestTaskTierClassifyWithoutBranchOrHeadRefused(t *testing.T) {
	e := tierBaseEnv(t)
	riskTiersOn(t, e)
	r := e.FailWith(ft.Opts{Agent: "zz-lead"}, 2, "task", "tier", "T-0001", "--classify")
	if !strings.Contains(r.Stderr, "--head") {
		t.Fatalf("stderr = %q", r.Stderr)
	}
}

func TestTaskTierClassifyDashRefRefused(t *testing.T) {
	e := tierBaseEnv(t)
	riskTiersOn(t, e)
	e.FailWith(ft.Opts{Agent: "zz-lead"}, 2, "task", "tier", "T-0001", "--classify", "--base", "--output=zz-never", "--head", "main")
}

func TestTaskTierRulesComeFromMainCheckoutNotThePR(t *testing.T) {
	e := tierBaseEnv(t)
	homeRepo(t, e)
	riskTiersOn(t, e)
	out := as(e, "zz-lead", "task", "tier", "T-0001", "--classify", "--base", "main", "--head", "pr").Stdout
	if !strings.Contains(out, "tier T3") {
		t.Fatalf("out = %q", out)
	}
	if tierOf(e) != "T3" {
		t.Fatalf("tier = %q", tierOf(e))
	}
	rows := e.SQL("SELECT detail FROM events WHERE action='tier'")
	if len(rows) == 0 || !strings.Contains(rows[0][0], "sensitive path ci/guard_x.py") {
		t.Fatalf("event detail = %v", rows)
	}
}

func TestTaskTierNewCommitSetsMaxOfOldAndNew(t *testing.T) {
	e := tierBaseEnv(t)
	homeRepo(t, e)
	riskTiersOn(t, e)
	gitHome(t, e.Home, "checkout", "-q", "-b", "small", "main")
	if err := os.WriteFile(filepath.Join(e.Home, "web.py"), []byte("x = 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitHome(t, e.Home, "commit", "-q", "-m", "small change", "web.py")
	gitHome(t, e.Home, "checkout", "-q", "main")
	as(e, "zz-lead", "task", "tier", "T-0001", "--classify", "--base", "main", "--head", "small")
	if tierOf(e) != "T1" {
		t.Fatalf("after small: tier = %q", tierOf(e))
	}
	as(e, "zz-lead", "task", "tier", "T-0001", "--classify", "--base", "main", "--head", "pr")
	if tierOf(e) != "T3" {
		t.Fatalf("after pr: tier = %q", tierOf(e))
	}
	out := as(e, "zz-lead", "task", "tier", "T-0001", "--classify", "--base", "main", "--head", "small").Stdout
	if tierOf(e) != "T3" {
		t.Fatalf("after small again: tier = %q", tierOf(e))
	}
	if !strings.Contains(out, "kept the higher") {
		t.Fatalf("out = %q", out)
	}
}

func TestTaskTierDoesNotChangeMergeOrSpawnState(t *testing.T) {
	e := tierBaseEnv(t)
	before := e.SQL("SELECT status, assignee, pr_url FROM tasks WHERE id='T-0001'")
	as(e, "zz-lead", "task", "tier", "T-0001", "up", "T3")
	after := e.SQL("SELECT status, assignee, pr_url FROM tasks WHERE id='T-0001'")
	if len(before) != 1 || len(after) != 1 {
		t.Fatalf("rows before=%v after=%v", before, after)
	}
	for i := range before[0] {
		if before[0][i] != after[0][i] {
			t.Fatalf("col %d changed: %q -> %q", i, before[0][i], after[0][i])
		}
	}
}
