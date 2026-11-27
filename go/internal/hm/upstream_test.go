package hm

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// upstreamRunner has a fake ssh to w1 whose hivemill records what it got, then runs body.
func upstreamRunner(t *testing.T, body string) (*Runner, *fakeRemote) {
	t.Helper()
	r, s := testRunner(t, "")
	m := newFakeRemote(t, r)
	m.stub("hivemill", `printf '%s\n' "$FACTORY_HOME" "$@" >"$HOME/args"; cat >"$HOME/stdin"`+"\n"+body)
	for k, v := range map[string]string{"upstream": "w1:/srv/zz factory", "upstream_goal": "G-007", "machine_name": "zz-box"} {
		if err := s.SetCfg(k, v); err != nil {
			t.Fatal(err)
		}
	}
	return r, m
}

func (m *fakeRemote) got(name string) string {
	b, _ := os.ReadFile(filepath.Join(m.home, name))
	return string(b)
}

func TestUpstreamAddLintsAndFiles(t *testing.T) {
	r, m := upstreamRunner(t, `echo "T-0042: spec gate PASS (1 criteria)"; echo "T-0042 todo in G-007"`)
	task := UpstreamTask{Title: "zz it's $(touch pwned)", Spec: "line one\nline two\n", Acceptance: "1. works - cmd:true => exit 0", Priority: -1}

	bad := task
	bad.Acceptance = "1. it works"
	if _, err := r.UpstreamAdd(bad); !errors.Is(err, ErrRefused) || len(m.log) != 0 {
		t.Fatalf("criteria that fail the lint: err %v, ssh calls %d", err, len(m.log))
	}

	id, err := r.UpstreamAdd(task)
	if err != nil || id != "T-0042" {
		t.Fatalf("id %q, err %v", id, err)
	}
	want := strings.Join([]string{"/srv/zz factory", "task", "add", "--goal=G-007", "--title=" + task.Title, "--spec-file=-",
		"--acceptance=" + task.Acceptance, "--upstream-from=zz-box", ""}, "\n")
	if got := m.got("args"); got != want {
		t.Errorf("remote call:\n%q\nwant\n%q", got, want)
	}
	if got := m.got("stdin"); got != task.Spec {
		t.Errorf("remote stdin %q", got)
	}
	if _, err := os.Stat(filepath.Join(m.home, "pwned")); err == nil {
		t.Error("the title ran in the remote shell")
	}
	ev, _ := r.Log.Read()
	if len(ev) != 1 || ev[0].Event != "upstream.filed" || ev[0].TaskID != "" || ev[0].Role != "" ||
		ev[0].Data["by"] != "owner" || ev[0].Data["upstream_task"] != "T-0042" {
		t.Errorf("events %+v", ev)
	}

	task.Priority = 0
	if _, err := r.UpstreamAdd(task); err != nil || !strings.Contains(m.got("args"), "\n--priority=0\n") {
		t.Errorf("priority 0: err %v, call %q", err, m.got("args"))
	}

	m.stub("hivemill", "echo 'hivemill: refused: no goal G-007' >&2; exit 3")
	if _, err := r.UpstreamAdd(task); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "no goal G-007") ||
		strings.Contains(err.Error(), "may have been filed") {
		t.Errorf("remote refusal: %v", err)
	}
	for _, rc := range []string{"2", "127"} { // a usage error or no hivemill there: nothing ran
		m.stub("hivemill", "exit "+rc)
		if _, err := r.UpstreamAdd(task); !errors.Is(err, ErrRefused) || strings.Contains(err.Error(), "may have been filed") {
			t.Errorf("remote exit %s: %v", rc, err)
		}
	}
	m.stub("hivemill", "exit 1")
	if _, err := r.UpstreamAdd(task); err == nil || !strings.Contains(err.Error(), "may have been filed") {
		t.Errorf("remote failure: %v", err)
	}
	var warn strings.Builder
	r.Warn = &warn
	m.stub("hivemill", "echo 'T-0043 todo in G-007'; echo zz-late >&2; exit 1")
	if id, err := r.UpstreamAdd(task); err != nil || id != "T-0043" {
		t.Errorf("filed, then a failure: id %q, err %v", id, err)
	}
	if !strings.Contains(warn.String(), "zz-late") {
		t.Errorf("the failure after filing was dropped: %q", warn.String())
	}
}

func TestUpstreamAddRefusesWithoutConfig(t *testing.T) {
	task := UpstreamTask{Title: "zz", Spec: "spec", Acceptance: "1. works - cmd:true => exit 0", Priority: -1}
	for _, c := range []struct{ upstream, goal, want string }{
		{"", "G-007", "upstream is not set"},
		{"w1:/srv/factory", "", "config upstream_goal"},
		{"w1:/srv/factory", "7", "config upstream_goal"},
		{"w1", "G-007", "want an ssh target"},
		{"w1:relative/home", "G-007", "want an ssh target"},
		{"-oProxyCommand=x:/srv/factory", "G-007", "want an ssh target"},
	} {
		r, m := upstreamRunner(t, "echo 'T-0042 todo in G-007'")
		for k, v := range map[string]string{"upstream": c.upstream, "upstream_goal": c.goal} {
			if err := r.Store.SetCfg(k, v); err != nil {
				t.Fatal(err)
			}
		}
		_, err := r.UpstreamAdd(task)
		if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), c.want) || len(m.log) != 0 {
			t.Errorf("%q goal %q: err %v, ssh calls %d", c.upstream, c.goal, err, len(m.log))
		}
	}
}

func TestUpstreamStatus(t *testing.T) {
	show := `printf 'T-0042 in_review (goal G-007, project factory, priority 1, depends [])\nzz title\nPR https://example.test/1 tier T1\n` +
		`\nSpec:\nrun: not this\n\nAcceptance:\n1. x\n\nT-0042: spec gate PASS (1 criteria)\n'`
	r, m := upstreamRunner(t, show+"\n"+`printf 'run: judging, round 1, https://example.test/1\n'`)
	out, err := r.UpstreamStatus("T-0042")
	want := "T-0042 in_review (goal G-007, project factory, priority 1, depends [])\nzz title\nPR https://example.test/1 tier T1\n" +
		"run: judging, round 1, https://example.test/1\n"
	if err != nil || out != want {
		t.Fatalf("err %v, out %q", err, out)
	}
	if got := m.got("args"); got != "/srv/zz factory\ntask\nshow\nT-0042\n" {
		t.Errorf("remote call %q", got)
	}

	m.stub("hivemill", show) // no run yet
	if out, err := r.UpstreamStatus("T-0042"); err != nil || strings.Contains(out, "run:") || strings.Contains(out, "Spec") {
		t.Errorf("no run: err %v, out %q", err, out)
	}
	if _, err := r.UpstreamStatus("T-0043"); err == nil {
		t.Error("another task's answer was taken")
	}
	m.stub("hivemill", "echo 'hivemill: no task T-0042' >&2; exit 1")
	if _, err := r.UpstreamStatus("T-0042"); err == nil || !strings.Contains(err.Error(), "no task T-0042") {
		t.Errorf("unknown task: %v", err)
	}
}
