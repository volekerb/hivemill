package s2cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
	"factory/go/internal/herdr"
)

// Port of tests/test_worker_gate.py: all 17 cases (HerdrGateTests 11, WorkerCommandTests 6). The Python module stays
// as the parity oracle until P7a. HerdrGateTests exercise herdr.Client with an in-process herdr_gate (mirrors
// bin/factory_cli.py); WorkerCommandTests that run tick, spawn, up, relaunch, judge, or clean call NeedPython.
//
// PARTIAL (P7a): TestWorkerGateWorkerTempHomeConfiguredLiveSocketIsDead skips the two ~<username>/ spellings until
// herdr.Socket expands ~user/ like Python os.path.expanduser (go/internal/herdr expandUser handles ~ and ~/ only).

func TestWorkerGateHerdrDeniedForEachWorkerRoleOnEachMutatingVerb(t *testing.T) {
	w := newWorkerGateEnv(t)
	for _, role := range wgWorkerRoles {
		for _, argv := range wgMutating {
			before := len(w.deniedEvents())
			code, _ := w.herdrCall(t, argv, "zz-test-"+role, map[string]string{"FACTORY_ROLE": role}, false)
			if code != 3 {
				t.Fatalf("role=%s argv=%v: exit %d, want 3", role, argv, code)
			}
			if got := len(w.deniedEvents()); got != before+1 {
				t.Fatalf("role=%s argv=%v: herdr-denied events %d, want %d", role, argv, got, before+1)
			}
		}
	}
	if got := w.herdrArgvs(); len(got) != 0 {
		t.Fatalf("herdr calls = %v, want none", got)
	}
}

func TestWorkerGateHerdrAllowedVerbsPassForWorkers(t *testing.T) {
	w := newWorkerGateEnv(t)
	for _, argv := range wgAllowed {
		code, _ := w.herdrCall(t, argv, "zz-test-coder", map[string]string{"FACTORY_ROLE": "coder"}, true)
		if code != 0 {
			t.Fatalf("argv=%v: exit %d, want 0", argv, code)
		}
		argvs := w.herdrArgvs()
		if len(argvs) == 0 || !reflect.DeepEqual(argvs[len(argvs)-1], argv) {
			t.Fatalf("last herdr argv = %v, want %v", argvs, argv)
		}
	}
	if len(w.deniedEvents()) != 0 {
		t.Fatalf("herdr-denied events = %v", w.deniedEvents())
	}
}

func TestWorkerGateWorkerDetectedByProcessAncestry(t *testing.T) {
	w := newWorkerGateEnv(t)
	me := os.Getpid()
	pids := ancestorPIDs(me, 2, 2001)
	bash, cl := pids[0], pids[1]
	w.FakeProc(me, "x", bash, "python3")
	w.FakeProc(bash, "x", cl, "bash")
	w.FakeProc(cl, "cl", 1, "claude", "--settings", "/srv/x/factory/roles/settings/coder.json", "--resume", "s")
	code, _ := w.herdrCall(t, []string{"pane", "close", "p1"}, "zz-test-director", nil, false)
	if code != 3 {
		t.Fatalf("exit %d, want 3", code)
	}
	if len(w.herdrArgvs()) != 0 {
		t.Fatalf("herdr calls = %v", w.herdrArgvs())
	}
	if len(w.deniedEvents()) != 1 {
		t.Fatalf("herdr-denied events = %d, want 1", len(w.deniedEvents()))
	}
}

func TestWorkerGateFlaglessLeadAncestryNotWorker(t *testing.T) {
	w := newWorkerGateEnv(t)
	me := os.Getpid()
	pids := ancestorPIDs(me, 2, 2001)
	bash, cl := pids[0], pids[1]
	w.FakeProc(me, "x", bash, "python3")
	w.FakeProc(bash, "x", cl, "bash")
	w.FakeProc(cl, "cl", 1, "claude", "--resume", "sess-lead")
	code, _ := w.herdrCall(t, []string{"agent", "start", "x"}, "zz-test-lead", nil, true)
	if code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	want := [][]string{{"agent", "start", "x"}}
	if !reflect.DeepEqual(w.herdrArgvs(), want) {
		t.Fatalf("herdr calls = %v, want %v", w.herdrArgvs(), want)
	}
}

func TestWorkerGateWorkerPaneSpoofingDirectorDenied(t *testing.T) {
	w := newWorkerGateEnv(t)
	sqlExec(t, w.Env, "UPDATE agents SET pane_id='p9' WHERE name='zz-test-coder'")
	code, _ := w.herdrCall(t, []string{"pane", "close", "p1"}, "zz-test-director", map[string]string{"HERDR_PANE_ID": "p9"}, false)
	if code != 3 {
		t.Fatalf("exit %d, want 3", code)
	}
	if len(w.herdrArgvs()) != 0 {
		t.Fatalf("herdr calls = %v", w.herdrArgvs())
	}
	if len(w.deniedEvents()) != 1 {
		t.Fatalf("herdr-denied events = %d, want 1", len(w.deniedEvents()))
	}
	if n := w.Count("SELECT count(*) FROM events WHERE action='identity-spoof'"); n != 1 {
		t.Fatalf("identity-spoof events = %d, want 1", n)
	}
}

func TestWorkerGateWorkerTempHomeRealHerdrMutatingRefused(t *testing.T) {
	w := newWorkerGateEnv(t)
	repo, err := ft.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(w.Home, "realbin")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(filepath.Join(repo, "tests", "stubs", "herdr"), filepath.Join(real, "herdr")); err != nil {
		t.Fatal(err)
	}
	for _, argv := range wgMutating[:3] {
		path := real + string(os.PathListSeparator) + os.Getenv("PATH")
		code, _ := w.herdrCall(t, argv, "zz-test-coder", map[string]string{"FACTORY_ROLE": "coder", "PATH": path}, true)
		if code != 3 {
			t.Fatalf("argv=%v: exit %d, want 3", argv, code)
		}
	}
	if len(w.herdrArgvs()) != 0 {
		t.Fatalf("herdr calls = %v", w.herdrArgvs())
	}
	if len(w.deniedEvents()) != 3 {
		t.Fatalf("herdr-denied events = %d, want 3", len(w.deniedEvents()))
	}
}

func TestWorkerGateWorkerTempHomeDefaultSocketIsDead(t *testing.T) {
	w := newWorkerGateEnv(t)
	wgApplyProcessEnv(t, w.Env, "zz-test-coder", map[string]string{"FACTORY_ROLE": "coder"})
	cl := w.newHerdrClient("zz-test-coder")
	if got := cl.Socket(); got != herdr.DeadSocket {
		t.Fatalf("worker socket = %q, want %q", got, herdr.DeadSocket)
	}
	defaultSock := filepath.Join(w.UserHome, ".config", "herdr", "herdr.sock")
	w.workerOnce = false
	wgApplyProcessEnv(t, w.Env, "", map[string]string{"FACTORY_AGENT": "", "FACTORY_ROLE": ""})
	cl = w.newHerdrClient("")
	if got := cl.Socket(); got != defaultSock {
		t.Fatalf("non-worker socket = %q, want %q", got, defaultSock)
	}
}

func TestWorkerGateWorkerTempHomeConfiguredLiveSocketIsDead(t *testing.T) {
	w := newWorkerGateEnv(t)
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	real := u.HomeDir
	username := u.Username
	linkParent := t.TempDir()
	link := filepath.Join(linkParent, "cfg-link")
	if err := os.Symlink(filepath.Join(real, ".config"), link); err != nil {
		t.Fatal(err)
	}
	worker := map[string]string{"FACTORY_ROLE": "coder", "FACTORY_AGENT": "zz-test-coder"}
	spellings := []struct {
		val  string
		skip string // non-empty: not ported in Go herdr.Socket yet
	}{
		{"~/.config/herdr/herdr.sock", ""},
		{real + "/.config/herdr/herdr.sock", ""},
		{real + "/.config/herdr/../herdr/herdr.sock", ""},
		{real + "//.config/./herdr/herdr.sock", ""},
		{"~" + username + "/.config/herdr/herdr.sock", "~<user>/ expanduser parity"},
		{"~" + username + "//.config/herdr/../herdr/herdr.sock", "~<user>/ expanduser parity"},
		{real + "/.config/herdr/../../.config/herdr/herdr.sock", ""},
		{link + "/herdr/herdr.sock", ""},
		{link + "/herdr//herdr.sock/", ""},
	}
	for _, sp := range spellings {
		sp := sp
		t.Run(sp.val, func(t *testing.T) {
			if sp.skip != "" {
				t.Skip(sp.skip)
			}
			w.OK("config", "herdr_socket", sp.val)
			w.workerOnce = false
			wgApplyProcessEnv(t, w.Env, "zz-test-coder", worker)
			cl := w.newHerdrClient("zz-test-coder")
			if got := cl.Socket(); got != herdr.DeadSocket {
				t.Fatalf("socket = %q, want dead", got)
			}
		})
	}
	w.OK("config", "herdr_socket", real+"/.config/herdr/other.sock")
	wgApplyProcessEnv(t, w.Env, "zz-test-coder", worker)
	cl := w.newHerdrClient("zz-test-coder")
	wantOther := real + "/.config/herdr/other.sock"
	if got := cl.Socket(); got != wantOther {
		t.Fatalf("explicit other socket = %q, want %q", got, wantOther)
	}
	w.OK("config", "herdr_socket", real+"/.config/herdr/herdr.sock")
	w.workerOnce = false
	wgApplyProcessEnv(t, w.Env, "", map[string]string{"FACTORY_AGENT": "", "FACTORY_ROLE": ""})
	cl = w.newHerdrClient("")
	wantLive := real + "/.config/herdr/herdr.sock"
	if got := cl.Socket(); got != wantLive {
		t.Fatalf("non-worker live socket = %q, want %q", got, wantLive)
	}
}

func TestWorkerGateWorkerTempHomeExplicitSocketHonoured(t *testing.T) {
	w := newWorkerGateEnv(t)
	custom := "/run/user/1000/custom.sock"
	w.OK("config", "herdr_socket", custom)
	wgApplyProcessEnv(t, w.Env, "zz-test-coder", map[string]string{"FACTORY_ROLE": "coder"})
	cl := w.newHerdrClient("zz-test-coder")
	if got := cl.Socket(); got != custom {
		t.Fatalf("socket = %q, want %q", got, custom)
	}
}

func TestWorkerGateStubExemptionOnlyForRealStub(t *testing.T) {
	w := newWorkerGateEnv(t)
	repo, err := ft.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	stubs := filepath.Join(repo, "tests", "stubs")
	path := stubs + string(os.PathListSeparator) + os.Getenv("PATH")
	code, _ := w.herdrCall(t, []string{"pane", "close", "p1"}, "zz-test-coder", map[string]string{"FACTORY_ROLE": "coder", "PATH": path}, true)
	if code != 0 {
		t.Fatalf("real stub path: exit %d, want 0", code)
	}
	want := [][]string{{"pane", "close", "p1"}}
	if !reflect.DeepEqual(w.herdrArgvs(), want) {
		t.Fatalf("herdr calls = %v, want %v", w.herdrArgvs(), want)
	}
	real := filepath.Join(w.Home, "realbin")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(filepath.Join(stubs, "herdr"), filepath.Join(real, "herdr")); err != nil {
		t.Fatal(err)
	}
	fakeStubs := filepath.Join(w.Home, "x", "tests", "stubs")
	if err := os.MkdirAll(fakeStubs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(real, "herdr"), filepath.Join(fakeStubs, "herdr")); err != nil {
		t.Fatal(err)
	}
	path = fakeStubs + string(os.PathListSeparator) + os.Getenv("PATH")
	code, _ = w.herdrCall(t, []string{"pane", "close", "p2"}, "zz-test-coder", map[string]string{"FACTORY_ROLE": "coder", "PATH": path}, false)
	if code != 3 {
		t.Fatalf("fake stub symlink: exit %d, want 3", code)
	}
	if len(w.herdrArgvs()) != 1 {
		t.Fatalf("herdr calls = %d, want 1", len(w.herdrArgvs()))
	}
	linkStubs := filepath.Join(w.Home, "y", "tests", "stubs")
	if err := os.MkdirAll(linkStubs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(real, "herdr"), filepath.Join(linkStubs, "herdr")); err != nil {
		t.Fatal(err)
	}
	path = strings.Join([]string{linkStubs, real, os.Getenv("PATH")}, string(os.PathListSeparator))
	code, _ = w.herdrCall(t, []string{"pane", "close", "p3"}, "zz-test-coder", map[string]string{"FACTORY_ROLE": "coder", "PATH": path}, false)
	if code != 3 {
		t.Fatalf("hardlink stub: exit %d, want 3", code)
	}
	if len(w.herdrArgvs()) != 1 {
		t.Fatalf("herdr calls = %d, want 1", len(w.herdrArgvs()))
	}
}

func TestWorkerGateWorkerCommandExemptOnlyWithStubAndTempHome(t *testing.T) {
	w := newWorkerGateEnv(t)
	repo, err := ft.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	stubs := filepath.Join(repo, "tests", "stubs") + string(os.PathListSeparator) + os.Getenv("PATH")
	real := filepath.Join(w.Home, "realbin")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(filepath.Join(repo, "tests", "stubs", "herdr"), filepath.Join(real, "herdr")); err != nil {
		t.Fatal(err)
	}
	realPath := real + string(os.PathListSeparator) + os.Getenv("PATH")
	refuse := func(path, home string) int {
		env := map[string]string{"PATH": path, "FACTORY_ROLE": "coder"}
		if home != "" {
			env["FACTORY_HOME"] = home
		}
		return w.refuseWorker(t, "zz-test-coder", env)
	}
	if code := refuse(stubs, ""); code != 0 {
		t.Fatalf("stub+temp home: exit %d, want 0", code)
	}
	if code := refuse(stubs, wgLiveHome(t)); code != 3 {
		t.Fatalf("stub+live home: exit %d, want 3", code)
	}
	if code := refuse(realPath, ""); code != 3 {
		t.Fatalf("real herdr+temp home: exit %d, want 3", code)
	}
}

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
