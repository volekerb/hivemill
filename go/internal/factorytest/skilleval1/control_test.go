package skilleval1

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	ft "factory/go/internal/factorytest"
	"factory/go/internal/gate"
	"factory/go/internal/sandbox"
	"factory/go/internal/scout"
	"factory/go/internal/skilleval"
	"factory/go/internal/spawn"
)

// Port of tests/test_skilleval_control.py (19 cases): the skilleval positive control (skilleval/controls/marker,
// (testdata/ctl/marker); the CLI cases run the harness home with a copy of it, and use bin/factory (NeedPython) only where the Go CLI delegates.
// (testdata/ctl/marker); the CLI cases run bin/factory (NeedPython) against the repo's own control dir.
//
// Two cases (spawn args carry the plugin dir; a planted script refuses the start) drive the Python spawn() with a built
// sandbox and a pane-tool stub. Go has no such pair yet (SandboxBuilder needs `factory init` and an unwired shim
// writer), so they are ported at the level Go has: spawn.Plan, the evaluator plan, on the vendored control copy.

const ctlKey = scout.ControlKey

var ctlWorkers = []string{"coder", "qa", "reviewer", "judge", "cleaner", "pm", "scout", "evaluator"}

func ctlStamp() string { return "2026-10-25T10:00:00+00:00" }

func ctlDir() string { return "testdata/ctl/marker" }

// ---- ControlPluginTests ----------------------------------------------------------------------------------------

func TestSkillevalControl_ControlDirIsPromptOnly(t *testing.T) {
	if err := scout.AssertPromptOnlyTree(ctlDir()); err != nil { // raises VendorError otherwise
		t.Fatal(err)
	}
	var files []string
	err := filepath.WalkDir(ctlDir(), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, p)
		}
		return err
	})
	if err != nil || len(files) == 0 {
		t.Fatalf("files %v, err %v", files, err)
	}
	for _, f := range files {
		if ext := filepath.Ext(f); ext != ".md" && ext != ".json" {
			t.Errorf("%s: only .md and .json files belong in the control", f)
		}
	}
	b, err := os.ReadFile(filepath.Join(ctlDir(), "skills", "eval-marker", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if n := strings.Count(text, "eval-marker-loaded"); n != 1 {
		t.Fatalf("marker text appears %d times, want once", n)
	}
	if !strings.Contains(text, "every task") {
		t.Fatal("SKILL.md must say it applies to every task")
	}
}

func TestSkillevalControl_VendorLocalCopiesWith0644AndIsReusable(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	sha, err := scout.ContentSha(ctlDir())
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(sha) {
		t.Fatalf("sha %q", sha)
	}
	cand := scout.VCand{ID: "C-0009", HeadSha: sha, InstallRisk: "prompt-only", Status: "triaged"}
	res, err := scout.VendorLocal(cand, root, ctlDir(), skilleval.ControlPlugin)
	if err != nil {
		t.Fatal(err)
	}
	if res.Reused {
		t.Fatal("first vendor must not be a reuse")
	}
	if len(res.Meta.PrescreenHits) != 0 {
		t.Fatalf("prescreen_hits %v", res.Meta.PrescreenHits)
	}
	plugin := filepath.Join(res.Dir, res.Plugin)
	n := 0
	err = filepath.WalkDir(plugin, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		n++
		st, serr := os.Stat(p)
		if serr != nil {
			return serr
		}
		if st.Mode().Perm() != 0o644 {
			t.Errorf("%s: mode %o, want 644", p, st.Mode().Perm())
		}
		return nil
	})
	if err != nil || n == 0 {
		t.Fatalf("walk: %v (%d files)", err, n)
	}
	if _, ok := scout.Vendored(root, "C-0009", sha); !ok {
		t.Fatal("Vendored: the copy is not recognised")
	}
	if !scout.IsEvalAllowed(cand, root) {
		t.Fatal("IsEvalAllowed: the vendored control must be allowed")
	}
	again, err := scout.VendorLocal(cand, root, ctlDir(), skilleval.ControlPlugin)
	if err != nil || !again.Reused {
		t.Fatalf("second vendor: %+v %v, want a reuse", again, err)
	}
}

func TestSkillevalControl_VendorLocalRefusesATaintedSourceAndHeldCandidates(t *testing.T) {
	td := t.TempDir()
	src := filepath.Join(td, "src")
	collectWrite(t, filepath.Join(src, "skills", "x", "SKILL.md"), "prose only\n", 0o644)
	collectWrite(t, filepath.Join(src, "run.sh"), "echo hi\n", 0o644)
	cand := scout.VCand{ID: "C-0009", HeadSha: strings.Repeat("d", 40), InstallRisk: "prompt-only", Status: "triaged"}
	root := filepath.Join(td, "root")
	isVendorErr := func(err error) bool { var ve *scout.VendorError; return errors.As(err, &ve) }
	if _, err := scout.VendorLocal(cand, root, src, "p"); !isVendorErr(err) {
		t.Fatalf("tainted source: %v, want a VendorError", err)
	}
	if collectExists(root) {
		t.Fatal("a refused vendor must leave no root behind")
	}
	held := cand
	held.Status = "needs-owner"
	if _, err := scout.VendorLocal(held, root, ctlDir(), "p"); !isVendorErr(err) {
		t.Fatalf("needs-owner: %v, want a VendorError", err)
	}
	scripts := cand
	scripts.InstallRisk = "scripts"
	if _, err := scout.VendorLocal(scripts, root, ctlDir(), "p"); !isVendorErr(err) {
		t.Fatalf("install_risk scripts: %v, want a VendorError", err)
	}
}

// ---- ControlCliBase / ControlRoleTests (bin/factory: NeedPython) --------------------------------------------------

type ctlCLI struct {
	e    *ft.Env
	root string
	db   *sql.DB
}

// ctlNewCLI is ControlCliBase.setUp. The Go CLI answers skilleval control|queue|list|results itself (workers and
// collect go to bin/factory): it finds the control plugin and the tasks next to its binary, so the harness home gets
// a skilleval/ tree (a copy of the control plugin; one meta.json per task the cases name). needPython is set by the
// cases that drive a command the Go CLI hands to Python.
func ctlNewCLI(t *testing.T, needPython bool) *ctlCLI {
	t.Helper()
	e := ft.New(t)
	if needPython {
		e.NeedPython()
	}
	collectCopyTree(t, ctlDir(), filepath.Join(e.Root, "skilleval", "controls", "marker"))
	for _, task := range []string{"fix-bug", "add-feature", "review-diff"} {
		collectWrite(t, filepath.Join(e.Root, "skilleval", "tasks", task, "meta.json"), `{"id": "`+task+`", "role": "coder"}`, 0o644)
	}
	c := &ctlCLI{e: e, root: filepath.Join(e.Root, "evalroot")}
	c.db = e.RawDB()
	t.Cleanup(func() { c.db.Close() })
	// `factory config` itself is handed to Python; the row it writes is all the cases need, so write it directly.
	collectExec(t, c.db, "INSERT INTO config(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", "skilleval_root", c.root)
	return c
}

func (c *ctlCLI) as(agent string) ft.Opts { return ft.Opts{Agent: agent} }

// trials is ControlCliBase.trials: (candidate, task, condition) of every trial row by id.
func (c *ctlCLI) trials(t testing.TB) [][3]string {
	t.Helper()
	rows, err := c.db.Query("SELECT candidate_id, task, condition FROM skill_trials ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out [][3]string
	for rows.Next() {
		var r [3]string
		if err := rows.Scan(&r[0], &r[1], &r[2]); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func (c *ctlCLI) count(t testing.TB, table string) int {
	t.Helper()
	var n int
	if err := c.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSkillevalControl_WorkersAreRefusedAndNothingIsQueued(t *testing.T) {
	c := ctlNewCLI(t, true)
	for _, role := range ctlWorkers {
		who := "zz-" + role
		c.e.SeedAgent(ft.AgentRow{Name: who, Role: role})
		t.Run(role, func(t *testing.T) {
			p := c.e.RunWith(c.as(who), "skilleval", "control")
			if p.Exit == 0 {
				t.Fatalf("exit 0, want a refusal\n%s%s", p.Stdout, p.Stderr)
			}
			if n := c.count(t, "scout_candidates"); n != 0 {
				t.Fatalf("%d candidate rows", n)
			}
			if tr := c.trials(t); len(tr) != 0 {
				t.Fatalf("trials %v", tr)
			}
		})
	}
	if collectExists(c.root) {
		t.Fatal("a refused control must not vendor anything")
	}
}

func TestSkillevalControl_DirectorLeadOwnerQueueWithAndWithoutTrials(t *testing.T) {
	c := ctlNewCLI(t, false)
	c.e.SeedAgent(ft.AgentRow{Name: "zz-director", Role: "director"})
	c.e.SeedAgent(ft.AgentRow{Name: "zz-lead", Role: "lead"})
	for _, w := range [][2]string{{"zz-director", "fix-bug"}, {"zz-lead", "add-feature"}, {"owner", "review-diff"}} {
		t.Run(w[0], func(t *testing.T) {
			p := c.e.OKWith(c.as(w[0]), "skilleval", "control", "--tasks", w[1])
			if !strings.Contains(p.Stdout, "queued 2 trial(s)") {
				t.Fatalf("stdout %q", p.Stdout)
			}
		})
	}
	var key, risk, sha, cid string
	if n := c.count(t, "scout_candidates"); n != 1 { // one reserved row, reused
		t.Fatalf("%d candidate rows, want 1", n)
	}
	if err := c.db.QueryRow("SELECT key, install_risk, head_sha, id FROM scout_candidates").Scan(&key, &risk, &sha, &cid); err != nil {
		t.Fatal(err)
	}
	collectWant(t, "key/install_risk", [2]string{key, risk}, [2]string{ctlKey, "prompt-only"})
	want, err := scout.ContentSha(ctlDir())
	if err != nil {
		t.Fatal(err)
	}
	collectWant(t, "head_sha", sha, want)
	var wantTrials []string
	for _, task := range []string{"fix-bug", "add-feature", "review-diff"} {
		for _, cond := range []string{"with", "without"} {
			wantTrials = append(wantTrials, cid+"|"+task+"|"+cond)
		}
	}
	var got []string
	for _, r := range c.trials(t) {
		got = append(got, strings.Join(r[:], "|"))
	}
	sort.Strings(got)
	sort.Strings(wantTrials)
	collectWant(t, "trials", got, wantTrials)
}

func TestSkillevalControl_DefaultTaskIsFixBugAndUnknownTaskIsRefused(t *testing.T) {
	c := ctlNewCLI(t, false)
	c.e.OKWith(c.as("owner"), "skilleval", "control")
	tasks := map[string]bool{}
	for _, r := range c.trials(t) {
		tasks[r[1]] = true
	}
	collectWant(t, "tasks", tasks, map[string]bool{"fix-bug": true})
	p := c.e.FailWith(c.as("owner"), 2, "skilleval", "control", "--tasks", "nope")
	if !strings.Contains(p.Stderr+p.Stdout, "unknown eval task") {
		t.Fatalf("output %q", p.Stderr+p.Stdout)
	}
}

// ---- ControlTrialPathTests -------------------------------------------------------------------------------------

// ctlVendored registers the control candidate in a fresh DB and vendors it under a fresh eval root.
type ctlVendored struct {
	db       *sql.DB
	root     string
	cand     scout.Candidate
	vendored string // <root>/vendor/<cid>@<sha>/<plugin>
}

func ctlVendor(t *testing.T) *ctlVendored {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	v := &ctlVendored{db: collectOpenDB(t), root: filepath.Join(base, "evalroot")}
	v.cand, err = skilleval.RegisterControl(v.db, ctlStamp(), ctlDir())
	if err != nil {
		t.Fatal(err)
	}
	res, err := scout.VendorLocal(scout.VCand{ID: v.cand.ID, HeadSha: v.cand.HeadSha.String, InstallRisk: v.cand.InstallRisk.String, Status: v.cand.Status},
		v.root, ctlDir(), skilleval.ControlPlugin)
	if err != nil {
		t.Fatal(err)
	}
	v.vendored = filepath.Join(res.Dir, res.Plugin)
	return v
}

// ctlPlan is the evaluator spawn plan for a trial of the control's "with" condition: the facts Python's spawn() reads
// from the filesystem (path resolver, shim check, prompt-only check) resolved the way the harness resolves them.
func (v *ctlVendored) plan(t *testing.T) (spawn.SpawnPlan, string) {
	t.Helper()
	trial := filepath.Join(v.root, "trials", sandbox.TrialName(1))
	for _, sub := range []string{"work", "fh", "home", "cfg", "bin"} {
		if err := os.MkdirAll(filepath.Join(trial, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	collectWrite(t, filepath.Join(trial, "bin", "claude"), "#!/bin/sh\nexit 0\n", 0o755)
	five, seven := 10.0, 20.0
	snap := gate.Snapshot{Now: time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC), HerdrReachable: true, MemMB: 3000,
		Usage: gate.Usage{FiveHour: &five, SevenDay: &seven, Fresh: true},
		Procs: gate.ProcScan{Known: true, Argv: map[int][]string{}}, Load1: 0.5, NumCPU: 8}
	gcfg := gate.Config{MaxLiveAgents: 3, MaxTotalClaude: 10, MinMemAvailableMB: 500, TmpMaxPct: 90, MaxFiveHourPct: 90,
		MaxSevenDayPct: 95, LoadRatio: 1.5, MaxLiveLeads: 4, ReservedWorkerSlots: 10, ReviewReservedSlots: 2,
		ReviewAutospawn: "off", LeadParking: "0", MinMemText: "500", TmpMaxPctText: "90", LoadRatioText: "1.5",
		FiveHourText: "90", SevenDayText: "95"}
	f := spawn.Facts{Config: map[string]string{}, Gate: snap, GateConfig: gcfg, CommonText: "common", RoleText: "role", RoleSettingsTemplate: "{}", Home: "/nonexistent-factory-home", RunDir: "/nonexistent-factory-home/run",
		RolesDir: "/nonexistent-factory-home/roles", EnabledOrgs: 1,
		Eval: spawn.EvalFacts{Root: v.root, Realpath: sandbox.Realpath, ShimOK: func(p string) bool { return collectExists(p) },
			PromptOnly: scout.AssertPromptOnlyTree}}
	req := spawn.Request{Role: "evaluator", Name: "eval-" + sandbox.TrialName(1), Cwd: filepath.Join(trial, "work"), SessionID: "sid-1",
		PluginDirs: []string{v.vendored},
		EnvExtra: []spawn.KV{{Key: "FACTORY_HOME", Value: filepath.Join(trial, "fh")}, {Key: "HOME", Value: filepath.Join(trial, "home")},
			{Key: "CLAUDE_CONFIG_DIR", Value: filepath.Join(trial, "cfg")}, {Key: "PATH", Value: filepath.Join(trial, "bin") + ":/usr/bin:/bin"}}}
	return spawn.Plan(req, f), trial
}

func TestSkillevalControl_SpawnArgsCarryPluginDirUnderTheVendorRoot(t *testing.T) {
	v := ctlVendor(t)
	p, trial := v.plan(t)
	if p.Refusal != nil || p.Start == nil {
		t.Fatalf("plan: refusal %+v start %+v", p.Refusal, p.Start)
	}
	args := p.Start.ClaudeArgs
	var plugins []string
	for i, a := range args {
		if a == "--plugin-dir" && i+1 < len(args) {
			plugins = append(plugins, args[i+1])
		}
	}
	if len(plugins) != 1 { // exactly one --plugin-dir, under <root>/vendor
		t.Fatalf("plugin dirs %v in %v", plugins, args)
	}
	collectWant(t, "plugin dir", sandbox.Realpath(plugins[0]), sandbox.Realpath(v.vendored))
	if !strings.HasPrefix(plugins[0], filepath.Join(v.root, "vendor")+"/") {
		t.Fatalf("plugin dir %s is not under <root>/vendor", plugins[0])
	}
	joined := strings.Join(args, " ") + " " + p.ArgsJSON
	for _, bad := range []string{"/.claude", "/factory/", "token", "TOKEN"} { // no live paths, no token
		if strings.Contains(joined, bad) {
			t.Errorf("spawn args contain %q: %s", bad, joined)
		}
	}
	if !strings.Contains(p.ArgsJSON, filepath.Join(trial, "fh")) {
		t.Errorf("spawn_args %s", p.ArgsJSON)
	}
}

func TestSkillevalControl_PlantedScriptInTheVendoredCopyRefusesTheStart(t *testing.T) {
	v := ctlVendor(t)
	collectWrite(t, filepath.Join(v.vendored, "skills", "eval-marker", "run.sh"), "echo pwned\n", 0o644) // after the build re-check
	p, _ := v.plan(t)
	if p.Refusal == nil || p.Refusal.Code != 3 || p.Start != nil || p.Queue != nil {
		t.Fatalf("plan: refusal %+v start %+v queue %+v, want exit 3 and no start", p.Refusal, p.Start, p.Queue)
	}
	if !strings.Contains(p.Refusal.Msg, "is not prompt-only") {
		t.Fatalf("refusal %q", p.Refusal.Msg)
	}
}

func TestSkillevalControl_PlantedScriptBeforeTheBuildRefusesTheSandbox(t *testing.T) {
	v := ctlVendor(t)
	collectWrite(t, filepath.Join(v.vendored, "skills", "eval-marker", "run.sh"), "echo pwned\n", 0o644)
	home := t.TempDir()
	tokenDir := filepath.Join(home, ".config", "factory")
	collectWrite(t, filepath.Join(tokenDir, "eval-oauth-token"), "tok", 0o600)
	collectExec(t, v.db, "INSERT INTO skill_trials(candidate_id,vendor_sha,task,condition,status,queued) VALUES(?,?,'fix-bug','with','starting','t')",
		v.cand.ID, v.cand.HeadSha.String)
	rows, err := skilleval.QueryTrials(v.db, "ORDER BY id")
	if err != nil || len(rows) != 1 {
		t.Fatalf("trials %v %v", rows, err)
	}
	b := skilleval.SandboxBuilder{TasksDir: filepath.Join(t.TempDir(), "tasks")}
	_, err = b.Build(v.db, rows[0], v.root, home, "unshare")
	if !errors.Is(err, sandbox.ErrSandboxRefused) {
		t.Fatalf("Build error %v, want sandbox.ErrSandboxRefused", err)
	}
	if collectExists(filepath.Join(v.root, "trials", sandbox.TrialName(int(rows[0].ID)))) {
		t.Fatal("a refused build must leave no trial dir")
	}
}

// ---- ControlCapTests -------------------------------------------------------------------------------------------

func TestSkillevalControl_ControlTrialsCountTowardTheDailyCap(t *testing.T) {
	if ok, why := sandbox.UsernsAvailable("unshare"); !ok {
		t.Skipf("user namespaces unavailable: %s", why)
	}
	v := ctlVendor(t)
	api := collectNewAPI(t)
	api.cfg = map[string]string{"skilleval_root": v.root, "skilleval_trial_minutes": "30", "skilleval_trial_max_tokens": "1000",
		"skilleval_max_concurrent": "1", "skilleval_max_trials_per_day": "1", "skilleval_keep_failed_h": "24"}
	collectWrite(t, filepath.Join(api.home, ".config", "factory", "eval-oauth-token"), "tok", 0o600)
	added, _, err := skilleval.QueueTrials(v.db, v.cand.ID, v.cand.HeadSha.String, []string{"fix-bug", "add-feature"}, "zz", "t")
	if err != nil || len(added) != 4 {
		t.Fatalf("queued %v %v", added, err)
	}
	c := skilleval.Collector{TasksDir: t.TempDir()}
	for minute := 0; minute < 10; minute++ {
		api.at = time.Date(2026, 10, 25, 10, minute, 0, 0, time.UTC)
		if err := c.Tick(v.db, api); err != nil {
			t.Fatal(err)
		}
		collectExec(t, v.db, "UPDATE skill_trials SET status='passed', ended=? WHERE status='running'", api.Now())
	}
	if len(api.launched) != 1 {
		t.Fatalf("started %v, want exactly 1 under a cap of 1", api.launched)
	}
	var pending int
	if err := v.db.QueryRow("SELECT count(*) FROM skill_trials WHERE status='pending'").Scan(&pending); err != nil || pending != 3 {
		t.Fatalf("pending %d %v, want 3", pending, err)
	}
}

// ---- MarkerTests -----------------------------------------------------------------------------------------------

// ctlMarkerEnv is MarkerTests.setUp: CollectBase plus a control candidate C-0002 (key local:skilleval-control).
func ctlMarkerEnv(t *testing.T) *collectEnv {
	t.Helper()
	e := collectSetup(t)
	e.needChecker()
	collectExec(t, e.db, "INSERT INTO scout_candidates(id,key,repo,head_sha,install_risk,status,sources,first_seen,last_seen) "+
		"VALUES('C-0002',?,NULL,?,'prompt-only','triaged','[]','t','t')", ctlKey, collectSHA)
	return e
}

// markerOf is marker_of: collect a trial whose work/ holds the given SKILL_MARKER.txt (content nil = none, link =
// a symlink to a file holding the marker) and return its check_output.
func ctlMarkerOf(e *collectEnv, content *string, cid string, link bool, cond string) string {
	e.t.Helper()
	o := collectDefaults()
	o.cid, o.cond = cid, cond
	id, d, _ := e.trial(o)
	work := filepath.Join(d, "work")
	switch {
	case link:
		collectWrite(e.t, filepath.Join(work, "elsewhere.txt"), "eval-marker-loaded", 0o644)
		if err := os.Symlink(filepath.Join(work, "elsewhere.txt"), filepath.Join(work, "SKILL_MARKER.txt")); err != nil {
			e.t.Fatal(err)
		}
	case content != nil:
		collectWrite(e.t, filepath.Join(work, "SKILL_MARKER.txt"), *content, 0o644)
	}
	return e.mustCollect(id, "").CheckOutput.String
}

func ctlStr(s string) *string { return &s }

func TestSkillevalControl_MarkerRecordedInCheckOutput(t *testing.T) {
	e := ctlMarkerEnv(t)
	for _, c := range []struct {
		name    string
		content *string
		link    bool
		want    string
	}{
		{"exact", ctlStr("eval-marker-loaded"), false, "control_marker=1"},
		{"trailing newline", ctlStr("eval-marker-loaded\n"), false, "control_marker=1"},
		{"missing", nil, false, "control_marker=0"},
		{"other text", ctlStr("something else"), false, "control_marker=0"},
		{"marker and more", ctlStr("eval-marker-loaded and more"), false, "control_marker=0"},
		{"symlink", nil, true, "control_marker=0"}, // untrusted work/: no symlink is followed
	} {
		if got := ctlMarkerOf(e, c.content, "C-0002", c.link, "with"); !strings.Contains(got, c.want) {
			t.Errorf("%s: check_output %q lacks %q", c.name, got, c.want)
		}
	}
}

func TestSkillevalControl_MarkerIsRecordedForTheWithoutTrialToo(t *testing.T) {
	e := ctlMarkerEnv(t)
	if got := ctlMarkerOf(e, nil, "C-0002", false, "without"); !strings.Contains(got, "control_marker=0") {
		t.Errorf("no marker: %q", got)
	}
	if got := ctlMarkerOf(e, ctlStr("eval-marker-loaded"), "C-0002", false, "without"); !strings.Contains(got, "control_marker=1") {
		t.Errorf("marker: %q", got)
	}
}

func TestSkillevalControl_MarkerLineSurvivesALongCheckerOutput(t *testing.T) {
	e := ctlMarkerEnv(t)
	e.check("head -c 5000 /dev/zero | tr '\\0' x\n")
	o := collectDefaults()
	o.cid = "C-0002"
	id, d, _ := e.trial(o)
	collectWrite(t, filepath.Join(d, "work", "SKILL_MARKER.txt"), "eval-marker-loaded", 0o644)
	row := e.mustCollect(id, "")
	out := row.CheckOutput.String
	if !strings.HasSuffix(out, "control_marker=1") {
		t.Fatalf("tail %q", out[max(0, len(out)-60):])
	}
	if len(out) > skilleval.OutputMax+40 {
		t.Fatalf("check_output is %d bytes", len(out))
	}
}

func TestSkillevalControl_RealCandidatesGetNoMarkerLine(t *testing.T) {
	e := ctlMarkerEnv(t)
	if got := ctlMarkerOf(e, ctlStr("eval-marker-loaded"), "C-0001", false, "with"); strings.Contains(got, "control_marker") {
		t.Fatalf("check_output %q", got)
	}
}

// ---- LoaderCheckTests ------------------------------------------------------------------------------------------

// ctlInvoked is LoaderCheckTests.invoked: one real-shape transcript line, parsed by SkillWasInvoked.
func ctlInvoked(t *testing.T, line any) int {
	t.Helper()
	td := t.TempDir()
	b, err := json.Marshal(line)
	if err != nil {
		t.Fatal(err)
	}
	collectWrite(t, filepath.Join(td, "s.jsonl"), string(b)+"\n", 0o644)
	n, err := skilleval.SkillWasInvoked(td, map[string]bool{"eval-marker": true, "eval-control-marker": true},
		"/x/vendor/C-0002@abc/eval-control-marker")
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSkillevalControl_NamespacedSkillToolUseCounts(t *testing.T) {
	// Claude Code writes plugin skills as `<plugin>:<skill>`.
	line := map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "model": "claude-sonnet-5-5", "content": []any{
		map[string]any{"type": "tool_use", "id": "toolu_01A", "name": "Skill", "input": map[string]any{"skill": "eval-control-marker:eval-marker"}}}}}
	if got := ctlInvoked(t, line); got != 1 {
		t.Fatalf("invoked = %d, want 1", got)
	}
}

func TestSkillevalControl_OtherSkillDoesNotCount(t *testing.T) {
	line := map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []any{
		map[string]any{"type": "tool_use", "id": "toolu_01B", "name": "Skill", "input": map[string]any{"skill": "other-plugin:other"}}}}}
	if got := ctlInvoked(t, line); got != 0 {
		t.Fatalf("invoked = %d, want 0", got)
	}
}

// ---- ControlNotAFindTests (bin/factory: NeedPython) --------------------------------------------------------------

// ctlNotAFind is ControlNotAFindTests.setUp: the control is registered and one finished with-trial recorded.
func ctlNotAFind(t *testing.T, needPython bool) (*ctlCLI, string) {
	t.Helper()
	c := ctlNewCLI(t, needPython)
	c.e.OKWith(c.as("owner"), "skilleval", "control")
	var cid string
	if err := c.db.QueryRow("SELECT id FROM scout_candidates").Scan(&cid); err != nil {
		t.Fatal(err)
	}
	collectExec(t, c.db, "INSERT INTO skill_trials(candidate_id,vendor_sha,task,condition,status,queued,ended,passed,check_output) VALUES(?,?,?,?,?,?,?,?,?)",
		cid, strings.Repeat("e", 40), "fix-bug", "with", "passed", "t", "2026-10-25T09:40:00+00:00", 1, "control_marker=1")
	return c, cid
}

func TestSkillevalControl_NotInPendingOrListNewEvenWhenStatusIsNew(t *testing.T) {
	c, cid := ctlNotAFind(t, false)
	collectExec(t, c.db, "UPDATE scout_candidates SET status='new' WHERE id=?", cid) // worst case: untriaged and new
	for _, args := range [][]string{{"scout", "pending"}, {"scout", "list", "--new"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if args[1] == "pending" {
				if _, err := exec.LookPath("python3"); err != nil { // scout pending is still answered by bin/factory
					t.Skip("python3 is not on PATH: scout pending is still handed to Python")
				}
			}
			p := c.e.OKWith(c.as("owner"), args...)
			if strings.Contains(p.Stdout+p.Stderr, ctlKey) || strings.Contains(p.Stdout, cid) {
				t.Fatalf("the control shows up:\n%s%s", p.Stdout, p.Stderr)
			}
		})
	}
}

func TestSkillevalControl_NotInTheReportFindsOrEvalResults(t *testing.T) {
	c, cid := ctlNotAFind(t, true)
	collectExec(t, c.db, "INSERT INTO scout_triage(candidate_id,purpose,target_roles,overlap,recommendation,rationale,created) VALUES(?,?,?,?,?,?,?)",
		cid, "p", "coder", "none", "adopt", "r", "2026-10-25T09:30:00+00:00")
	p := c.e.OKWith(c.as("owner"), "scout", "report", "--since", "2026-10-25T00:00:00+00:00")
	if strings.Contains(p.Stdout, ctlKey) || strings.Contains(p.Stdout, cid) {
		t.Fatalf("the control is in the report:\n%s", p.Stdout)
	}
}

func TestSkillevalControl_ResultsLabelItControl(t *testing.T) {
	c, cid := ctlNotAFind(t, false)
	out := c.e.OKWith(c.as("owner"), "skilleval", "results").Stdout
	for _, want := range []string{cid + " control: positive control (" + ctlKey + ")", "skill_invoked=- control_marker=1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("results lacks %q:\n%s", want, out)
		}
	}
}
