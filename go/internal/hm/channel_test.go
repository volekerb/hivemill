package hm

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// addProject adds a second project "app" with one goal and the todo task T-0002, and a fresh usage reading so that
// a pass dispatches.
func addProject(t *testing.T, r *Runner, s *SQLite) {
	t.Helper()
	dir := filepath.Join(r.Env.UserHome, ".claude")
	_ = os.MkdirAll(dir, 0o700)
	reading := fmt.Sprintf(`{"five_hour_pct": "10", "seven_day_pct": "12", "updated_at": %d}`, time.Now().Unix())
	if err := os.WriteFile(filepath.Join(dir, "usage-limits.json"), []byte(reading), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO projects(id,name,repo_path,gh_repo) VALUES('app','app','/nonexistent','zz-test/app')`,
		`INSERT INTO goals(id,project_id,title) VALUES('G-002','app','zz-test goal')`,
		`INSERT INTO tasks(id,goal_id,title,spec,acceptance,status) VALUES('T-0002','G-002','zz-test task','spec','1. x - cmd:true => 0','todo')`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetCfg("projects", "www,app"); err != nil {
		t.Fatal(err)
	}
}

func TestStableChannelUsesStableBinary(t *testing.T) {
	r, s := testRunner(t, "")
	addProject(t, r, s)
	_, _ = s.db.Exec(`UPDATE tasks SET status='todo' WHERE id='T-0001'`)
	if err := s.SetCfg("project.www.channel", "beta"); err == nil {
		t.Error("an unknown channel must be refused")
	}
	if err := s.SetCfg("project.nope.channel", ChannelStable); err == nil {
		t.Error("an unknown project must be refused")
	}
	if got := r.Channel("www"); got != ChannelEdge {
		t.Errorf("default channel %q", got)
	}
	if err := s.SetCfg("project.www.channel", ChannelStable); err != nil {
		t.Fatal(err)
	}
	if got := r.Delegate("www"); got != "" {
		t.Errorf("nothing promoted yet, edge runs the project: %q", got)
	}
	bin := StableBinary(r.Env.UserHome)
	_ = os.MkdirAll(filepath.Dir(bin), 0o755)
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var calls []string
	r.Tools.Sh = func(_ string, _ time.Duration, _ []string, argv ...string) (int, string) {
		if argv[0] == bin {
			calls = append(calls, strings.Join(argv[1:], " "))
			return 1, "broken" // a failing stable pass stops nothing else
		}
		return 0, ""
	}
	var out bytes.Buffer
	r.Out = &out
	if err := r.Pass(true); err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, "|") != "run --project www" {
		t.Errorf("stable binary calls: %q", calls)
	}
	// edge dispatches its own project (the start fails here: no git) and never the stable one
	if got := out.String(); !strings.Contains(got, "T-0002: ") || strings.Contains(got, "T-0001: ") {
		t.Errorf("edge pass output: %s", got)
	}
	// the third failed stable pass in a row is told to the owner
	for range stableFailAlert - 1 {
		if err := r.Pass(false); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.Meta("stable_fail:www"); got != "3" || !strings.Contains(out.String(), "stable pass failed") {
		t.Errorf("failed stable passes: %q, %s", got, out.String())
	}
	// merge and gate steps of the project's task are handed to the stable binary, the other project's are not
	if r.DelegateTask("T-0001") != bin || r.DelegateTask("T-0002") != "" {
		t.Errorf("delegate: %q, %q", r.DelegateTask("T-0001"), r.DelegateTask("T-0002"))
	}
	run := &Run{Task: "T-0001", Project: "www", Agent: "claude-task-t-0001", State: Working}
	putRuns(t, s, run)
	if r.DelegateAgent(run.Agent) != bin {
		t.Error("the session hooks of the project go to the stable binary")
	}
	// the session settings call the stable binary
	_ = os.MkdirAll(filepath.Join(r.Env.Home, "roles", "hivemill"), 0o755)
	_ = os.WriteFile(filepath.Join(r.Env.Home, "roles", "hivemill", "task.json.in"),
		[]byte(`{"command": "@FACTORY_HOME_DIR@/.local/bin/hivemill gate hook"}`), 0o644)
	path, err := renderSettings(r.Env, "task", r.stableFor("www"))
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), `"`+bin+` gate hook"`) {
		t.Errorf("settings %s", b)
	}
	// the stable binary itself runs the project: no loop
	old := SelfBinary
	SelfBinary = func() (string, error) { return filepath.EvalSymlinks(bin) }
	t.Cleanup(func() { SelfBinary = old })
	calls = nil
	out.Reset()
	if err := r.PassProject("www", false); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 || !strings.Contains(out.String(), "T-0001: ") {
		t.Errorf("project pass of the stable binary: calls %q, output %s", calls, out.String())
	}
	if err := r.PassProject("nope", false); !errors.Is(err, ErrRefused) {
		t.Errorf("unknown project: %v", err)
	}
}

func TestPromoteNeeds24hClean(t *testing.T) {
	r, s := testRunner(t, "")
	edge := filepath.Join(t.TempDir(), "hivemill")
	build := func(content string) string {
		if err := os.WriteFile(edge, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
		sum, _, err := fileSum(edge)
		if err != nil {
			t.Fatal(err)
		}
		return sum
	}
	sum := build("build one")
	step := time.Duration(cfgInt(s, "health_max_age_s")) * time.Second / 2
	now := time.Unix(1795548325, 0)
	// run passes the build for d; the last one fails when asked to
	run := func(d time.Duration, failLast bool) {
		for end := now.Add(d); !now.After(end); now = now.Add(step) {
			r.edgePassStart(edge, sum, now)
			r.edgePassEnd(failLast && now.Add(step).After(end), now)
		}
		now = now.Add(-step) // the time of the last pass
	}
	refused := func(why string) {
		t.Helper()
		if _, err := r.Promote(now); !errors.Is(err, ErrRefused) {
			t.Errorf("%s: promote must be refused, got %v", why, err)
		}
		if _, err := os.Stat(StableBinary(r.Env.UserHome)); err == nil {
			t.Errorf("%s: a stable binary was written", why)
		}
	}
	refused("no daemon yet")
	run(23*time.Hour, false)
	refused("23 h")
	run(2*time.Hour, true)
	refused("a failed pass")
	run(23*time.Hour, false)
	refused("23 h after a failed pass")
	run(2*time.Hour, false)
	r.edgeDirty()
	refused("an auto-revert")
	run(23*time.Hour, false)
	now = now.Add(3 * time.Hour) // the daemon was down
	run(2*time.Hour, false)
	refused("a gap in the passes")
	sum = build("build two")
	run(2*time.Hour, false)
	refused("a new build")
	run(25*time.Hour, false)
	build("build three") // installed, the daemon still runs build two
	refused("the file changed under the daemon")
	build("build two")
	// a pass that did not run earns nothing; one that never ended (no daemon holds its lock) blocks the promote
	before := r.readEdge().LastEnd
	r.edgePassStart(edge, sum, now)
	r.edgePassSkipped()
	if b := r.readEdge(); b.LastEnd != before || b.Running {
		t.Errorf("skipped pass: %+v", b)
	}
	r.edgePassStart(edge, sum, now)
	refused("the daemon died in a pass")
	r.edgePassEnd(false, now)
	dst, err := r.Promote(now)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dst)
	if fi, err := os.Stat(dst); dst != StableBinary(r.Env.UserHome) || err != nil || fi.Mode().Perm()&0o111 == 0 || string(got) != "build two" {
		t.Errorf("promoted %s: %q, %v", dst, got, err)
	}
}

func TestDrainProjectOnly(t *testing.T) {
	r, s := testRunner(t, "")
	addProject(t, r, s)
	_, _ = s.db.Exec(`UPDATE tasks SET status='todo' WHERE id='T-0001'`)
	if err := r.SetProjectDrain("nope", true); !errors.Is(err, ErrRefused) {
		t.Errorf("unknown project: %v", err)
	}
	if err := r.SetProjectDrain("www", true); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.DrainedProjects(), ","); got != "www" || r.Draining() {
		t.Errorf("drained %q, global %v", got, r.Draining())
	}
	if err := r.Start("T-0001"); err == nil || !strings.Contains(err.Error(), "drain") {
		t.Errorf("a drained project must refuse a start: %v", err)
	}
	if err := r.Start("T-0002"); err != nil && strings.Contains(err.Error(), "drain") {
		t.Errorf("the other project is not drained: %v", err)
	}
	var out bytes.Buffer
	r.Out = &out
	if err := r.Pass(true); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "T-0002: ") || strings.Contains(got, "T-0001: ") {
		t.Errorf("dispatch: %s", got)
	}
	// an active run of the drained project still advances
	putRuns(t, s, &Run{Task: "T-0001", Project: "www", Agent: "claude-task-t-0001", State: Working})
	out.Reset()
	if err := r.Pass(true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "T-0001: ") {
		t.Errorf("active run: %s", out.String())
	}
	if err := r.SetProjectDrain("www", false); err != nil {
		t.Fatal(err)
	}
	if r.ProjectDraining("www") || len(r.DrainedProjects()) != 0 {
		t.Error("resume lifts the project drain")
	}
}
