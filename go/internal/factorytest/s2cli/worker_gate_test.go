package s2cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
)

// Port of tests/test_worker_gate.py: 6 of 17 cases (the WorkerCommandTests). The Python module stays as the parity
// oracle until P7a; the cases that run tick, spawn, up, relaunch, judge or clean call NeedPython.
//
// PARTIAL (P7a, T-1495): the 11 HerdrGateTests are not ported. No production Go code sets herdr.Client.Gate (the
// T-0031 gate lives only in bin/factory_cli.py), so a Go test would only test a gate written in the test file.
// The Python module must stay until a Go gate exists.
//   not ported: test_herdr_denied_for_each_worker_role_on_each_mutating_verb - no Go herdr gate
//   not ported: test_herdr_allowed_verbs_pass_for_workers - no Go herdr gate
//   not ported: test_worker_detected_by_process_ancestry - no Go herdr gate or worker detection
//   not ported: test_flagless_lead_ancestry_not_worker - no Go herdr gate or worker detection
//   not ported: test_worker_pane_spoofing_director_denied - no Go herdr gate or worker detection
//   not ported: test_worker_temp_home_real_herdr_mutating_refused - no Go herdr gate
//   not ported: test_worker_temp_home_default_socket_is_dead - gate-driven, no Go herdr gate
//   not ported: test_worker_temp_home_configured_live_socket_is_dead - gate-driven, no Go herdr gate
//   not ported: test_worker_temp_home_explicit_socket_honoured - gate-driven, no Go herdr gate
//   not ported: test_stub_exemption_only_for_real_stub - no Go herdr gate
//   not ported: test_worker_command_exempt_only_with_stub_and_temp_home - no Go refuse_worker

func TestWorkerGateWorkerTickUpRelaunchSpawnRefusedWithoutDbChange(t *testing.T) {
	w := newWorkerGateEnv(t)
	w.NeedPython()
	before := w.snapshot()
	cmds := [][]string{{"tick"}, {"up"}, {"relaunch", "zz-test-lead"}, {"spawn", "pm", "--name", "zz-test-x"}}
	for _, args := range cmds {
		for _, who := range []string{"zz-test-coder", "zz-test-director"} {
			r := w.FailWith(ft.Opts{Agent: who, Env: map[string]string{"FACTORY_ROLE": "coder"}}, 3, args...)
			contains(t, "stderr", r.Stderr, "workers may not run")
		}
	}
	if !reflect.DeepEqual(w.snapshot(), before) {
		t.Fatalf("DB snapshot changed")
	}
	if len(w.herdrArgvs()) != 0 {
		t.Fatalf("herdr calls = %v", w.herdrArgvs())
	}
}

func TestWorkerGateLeadAndOwnerPathsUnchanged(t *testing.T) {
	w := newWorkerGateEnv(t)
	w.NeedPython()
	r := w.OKWith(ft.Opts{Agent: "zz-test-lead"}, "spawn", "pm", "--name", "zz-test-pm2")
	contains(t, "stdout", r.Stdout, "SPAWNED zz-test-pm2")
	w.OK("tick")
	w.OK("up")
	if len(w.deniedEvents()) != 0 {
		t.Fatalf("herdr-denied events = %v", w.deniedEvents())
	}
}

func TestWorkerGateWorkerMsgThenOwnerTickNudges(t *testing.T) {
	w := newWorkerGateEnv(t)
	w.NeedPython()
	sqlExec(t, w.Env, "UPDATE agents SET pane_id='px0', session_id='s-lead' WHERE name='zz-test-lead'")
	w.SetHerdr(map[string]any{"agents": []map[string]any{{
		"name": "zz-test-lead", "agent": "claude", "pane_id": "px0", "agent_status": "idle",
		"agent_session": map[string]string{"value": "s-lead"},
	}}})
	w.OKWith(w.workerOpts("coder"), "msg", "lead:g-001-a", "hello", "body")
	if len(w.herdrArgvs()) != 0 {
		t.Fatalf("herdr calls after msg = %v", w.herdrArgvs())
	}
	w.OK("tick")
	var prompts [][]string
	for _, a := range w.herdrArgvs() {
		if len(a) >= 2 && a[0] == "agent" && a[1] == "prompt" {
			prompts = append(prompts, a)
		}
	}
	if len(prompts) == 0 {
		t.Fatalf("no agent prompt in herdr calls: %v", w.herdrArgvs())
	}
}

func TestWorkerGateWorkerDoneInboxVerdictReportTaskNoteStillWork(t *testing.T) {
	w := newWorkerGateEnv(t)
	w.NeedPython()
	w.seedProject("zz-test-proj")
	sqlExec(t, w.Env, "UPDATE goals SET project_id='zz-test-proj' WHERE id='G-001'")
	w.UseGitHead()
	checks := []map[string]string{{"name": "unittest", "status": "COMPLETED", "conclusion": "SUCCESS"}}
	w.seedPR(checks, validPRBody)
	bodyLine, _ := json.Marshal(validPRBody)
	prView, _ := json.Marshal(map[string]any{
		"number": 1, "body": validPRBody, "state": "OPEN", "headRefOid": w.HeadSHA, "headRefName": "factory/t-0001",
		"baseRefName": "main", "mergeable": "MERGEABLE", "isDraft": false, "url": ft.PRURL,
		"statusCheckRollup": checks,
	})
	mainSHA, err := exec.Command("git", "-C", w.RepoURL, "rev-parse", "refs/heads/main").Output()
	if err != nil {
		t.Fatal(err)
	}
	w.addGhRules(
		ft.StubRule{Match: []string{"pr", "view", ft.PRURL, "--json", "body"}, Out: `{"body":` + string(bodyLine) + "}\n"},
		ft.StubRule{Match: []string{"pr", "view", ft.PRURL}, Out: string(prView) + "\n"},
		ft.StubRule{Match: []string{"repo", "view"}, Out: `{"url": "` + w.RepoURL + `"}` + "\n"},
		ft.StubRule{Match: []string{"api", "commits/main"}, Out: strings.TrimSpace(string(mainSHA)) + "\n"},
		ft.StubRule{Match: []string{"pr", "merge"}, Out: "", Exit: 0},
	)
	for _, role := range []string{"coder", "qa", "reviewer", "judge"} {
		w.OKWith(w.workerOpts(role), "inbox")
		w.OKWith(w.workerOpts(role), "agents")
		w.OKWith(w.workerOpts(role), "res")
		w.OKWith(w.workerOpts(role), "task", "show", "T-0001")
		w.OKWith(w.workerOpts(role), "task", "list")
		w.OKWith(w.workerOpts(role), "digest")
	}
	w.OKWith(w.workerOpts("coder"), "task", "note", "T-0001", "a note")
	w.OKWith(w.workerOpts("coder"), "task", "update", "T-0001", "--status", "in_review", "--pr", ft.PRURL)
	w.OKWith(w.workerOpts("qa"), "verdict", "T-0001", "--pass", "--sha", w.HeadSHA, "--summary", wgGatesSummary("ok"))
	w.OKWith(w.workerOpts("reviewer"), "verdict", "T-0001", "--pass", "--sha", w.HeadSHA, "--summary", "ok")
	w.OKWith(w.workerOpts("judge"), "report", "headline", "body")
	w.OKWith(w.workerOpts("judge"), "judge", "T-0001", "--merge", "--reason", "green")
	for _, role := range []string{"coder", "qa", "reviewer", "judge"} {
		w.OKWith(w.workerOpts(role), "done", "finished")
	}
	if len(w.deniedEvents()) != 0 {
		t.Fatalf("herdr-denied events = %v", w.deniedEvents())
	}
	for _, a := range w.herdrArgvs() {
		if len(a) < 2 || a[0] != "agent" || a[1] != "list" {
			t.Fatalf("unexpected herdr call %v, want only agent list", w.herdrArgvs())
		}
	}
}

func TestWorkerGateFlaglessLiveShapeLeadSpawnAndDirectorRelaunch(t *testing.T) {
	w := newWorkerGateEnv(t)
	w.NeedPython()
	sqlExec(t, w.Env, "UPDATE agents SET pane_id='pl', session_id='s-lead' WHERE name='zz-test-lead'")
	sqlExec(t, w.Env, "UPDATE agents SET pane_id='pd', session_id='s-dir' WHERE name='zz-test-director'")
	r := w.OKWith(ft.Opts{Agent: "zz-test-lead", Env: map[string]string{"HERDR_PANE_ID": "pl", "CLAUDECODE": "1"}},
		"spawn", "pm", "--name", "zz-test-pm2")
	contains(t, "stdout", r.Stdout, "SPAWNED zz-test-pm2")
	w.OKWith(ft.Opts{Agent: "zz-test-director", Env: map[string]string{"HERDR_PANE_ID": "pd", "CLAUDECODE": "1"}},
		"relaunch", "zz-test-lead", "--dry-run")
	if len(w.deniedEvents()) != 0 {
		t.Fatalf("herdr-denied events = %v", w.deniedEvents())
	}
}

func TestWorkerGateCleanerMayCleanOtherWorkersMayNot(t *testing.T) {
	w := newWorkerGateEnv(t)
	w.NeedPython()
	for _, role := range wgWorkerRoles {
		if role == "cleaner" {
			continue
		}
		r := w.FailWith(w.workerOpts(role), 3, "clean", "--dry-run")
		contains(t, "stderr", r.Stderr, "only the cleaner")
	}
	r := w.OKWith(w.workerOpts("cleaner"), "clean", "--dry-run")
	notContains(t, "stderr", r.Stderr, "may not")
}

func copyFile(src, dst string) error {
	in, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, in, 0o755)
}
