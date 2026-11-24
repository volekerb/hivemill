package skilleval1

// Port of tests/test_skilleval_sandbox.py (45 cases): the evaluator trial sandbox, the spawn contract of the evaluator
// and `factory skilleval try`. The Go product has no CLI path for try/spawn/tick yet (those stay in Python until P4-L), so
// the cases are driven against the Go packages that carry the behaviour:
//
//   - skilleval.SandboxBuilder / skilleval.Env (Build, Launch, Try, Void) for the trial layout and the try refusals,
//   - spawn.Plan for the evaluator spawn contract (check_evaluator_spawn, the claude argv and env), fed by the exact
//     arguments Env.Launch hands the spawner,
//   - sandbox.EvalNsCfg / PreflightArgv for the namespaces.
//
// Test doubles (named in each case): the trial DB seed runs a copy of an empty schema instead of `factory init`, and the
// claude shim writer (not wired in Go) is a plain script. Cases with no Go counterpart are top-level tests that skip with
// "not ported: <reason>" so the case count stays complete; the reasons are in the notes of those tests.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"context"

	"factory/go/internal/factorydb/dbtest"
	ft "factory/go/internal/factorytest"
	"factory/go/internal/gate"
	"factory/go/internal/sandbox"
	"factory/go/internal/scout"
	"factory/go/internal/skilleval"
	"factory/go/internal/spawn"

	_ "modernc.org/sqlite"
)

const sbxFakeClaude = "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 9.9.9; exit 0; fi\nenv\npwd\n"

// sbxToken is split so a repo secret scan does not flag the dummy.
const sbxToken = "sk-ant-" + "oat01-DUMMYTRIALTOKEN"

var sbxSHA = strings.Repeat("c", 40)

// sbxAPI is skilleval.API for the Env: fixed clock, config from a map, events written to the events table.
type sbxAPI struct {
	clock time.Time
	cfg   map[string]string
	home  string
	live  string
}

func (a *sbxAPI) Now() string                             { return scout.Stamp(a.clock) }
func (a *sbxAPI) Clock() time.Time                        { return a.clock }
func (a *sbxAPI) Cfg(_ *sql.DB, k string) (string, error) { return a.cfg[k], nil }
func (a *sbxAPI) Event(db *sql.DB, entity, id, action, detail string) error {
	_, err := db.Exec("INSERT INTO events(ts, entity, entity_id, action, detail) VALUES(?,?,?,?,?)", a.Now(), entity, id, action, detail)
	if err != nil {
		return fmt.Errorf("sbxAPI event: %w", err)
	}
	return nil
}
func (a *sbxAPI) ForceStop(*sql.DB, string, string, sql.NullString) (string, string, error) {
	return "", "", errors.New("sbxAPI: ForceStop not used here")
}
func (a *sbxAPI) Launch(*sql.DB, skilleval.Trial, string, string, string) error {
	return errors.New("sbxAPI: Launch not used here")
}
func (a *sbxAPI) Home() string    { return a.home }
func (a *sbxAPI) HomeDir() string { return a.live }
func (a *sbxAPI) Unshare() string { return "unshare" }

// sbxSpawnArgs is one call of the Spawner callback.
type sbxSpawnArgs struct {
	name, cwd, prompt string
	env               [][2]string
	plugins           []string
}

// sbxBuilder wraps the real SandboxBuilder: it counts builds, can skip the namespace preflight (the layout and spawn
// cases need none, like Python group A) or answer it with a canned refusal.
type sbxBuilder struct {
	real          skilleval.SandboxBuilder
	skipPreflight bool
	preflightErr  error
}

func (b *sbxBuilder) Build(db *sql.DB, t skilleval.Trial, root, home, unshare string) (skilleval.Info, error) {
	return b.real.Build(db, t, root, home, unshare)
}

func (b *sbxBuilder) Preflight(info skilleval.Info) error {
	if b.preflightErr != nil {
		return b.preflightErr
	}
	if b.skipPreflight {
		return nil
	}
	return b.real.Preflight(info)
}

// sbxFix is SandboxBase.setUp: a temp user home with the dummy token and the fake claude, an eval root, one
// prompt-only candidate C-0001 with a vendored plugin, and a DB with the scout tables.
type sbxFix struct {
	t         *testing.T
	repo      string
	live      string // the live factory home (never touched)
	home      string // the user home holding the token and claude
	root      string // skilleval_root
	token     string
	claude    string
	pluginSrc string
	db        *sql.DB
	api       *sbxAPI
	builder   *sbxBuilder
	env       *skilleval.Env
	out       *strings.Builder
	spawned   []sbxSpawnArgs
	spawnErr  error
	nextTrial int
}

func sbxMust(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func sbxWrite(t testing.TB, path, text string, mode os.FileMode) {
	t.Helper()
	sbxMust(t, os.MkdirAll(filepath.Dir(path), 0o755))
	sbxMust(t, os.WriteFile(path, []byte(text), mode))
	sbxMust(t, os.Chmod(path, mode))
}

func sbxExec(t testing.TB, db *sql.DB, q string, a ...any) {
	t.Helper()
	if _, err := db.Exec(q, a...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func sbxRealTemp(t testing.TB) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	sbxMust(t, err)
	return d
}

// sbxWriteShim is the test double of the (unported) shim writer: bin/claude is a script naming the real claude. The
// real shim, its factory CLI snapshot and the token read are Python-only, see TestSkillevalSandbox_ShimEnvArrivesInsideAndTheRestIsDropped.
func sbxWriteShim(trialDir, realClaude, _, _, _ string, _ *sandbox.Cfg, _ map[string]string) (string, error) {
	if err := os.MkdirAll(filepath.Join(trialDir, "scout"), 0o755); err != nil { // the snapshot dir the allow view binds read-only
		return "", fmt.Errorf("create scout snapshot dir: %w", err)
	}
	shim := filepath.Join(trialDir, "bin", "claude")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\nexec "+realClaude+" \"$@\"\n"), 0o755); err != nil {
		return "", fmt.Errorf("write shim: %w", err)
	}
	if err := os.Chmod(shim, 0o755); err != nil {
		return "", fmt.Errorf("chmod shim: %w", err)
	}
	return shim, nil
}

// sbxNewDB creates a database file from the schema dump copy and returns an open handle.
func sbxNewDB(t testing.TB, path string) *sql.DB {
	t.Helper()
	sbxMust(t, os.MkdirAll(filepath.Dir(path), 0o755))
	d, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(30000)")
	sbxMust(t, err)
	d.SetMaxOpenConns(1)
	t.Cleanup(func() { d.Close() })
	ddl, err := os.ReadFile("testdata/sbx/init.sql")
	sbxMust(t, err)
	if _, err := d.Exec(string(ddl)); err != nil {
		t.Fatalf("testdata/sbx/init.sql: %v", err)
	}
	return d
}

func newSbxFix(t *testing.T) *sbxFix {
	t.Helper()
	repo, err := ft.RepoRoot()
	sbxMust(t, err)
	tmp := sbxRealTemp(t)
	f := &sbxFix{t: t, repo: repo, live: filepath.Join(tmp, "factory"), home: filepath.Join(tmp, "userhome"), root: filepath.Join(tmp, "evalroot")}
	sbxMust(t, os.MkdirAll(f.live, 0o755))
	f.token = filepath.Join(f.home, ".config", "factory", "eval-oauth-token")
	sbxWrite(t, f.token, sbxToken+"\n", 0o600)
	f.claude = filepath.Join(f.home, ".local", "bin", "claude")
	sbxWrite(t, f.claude, sbxFakeClaude, 0o755)

	// the live-style DB: a copy of the harness schema dump (testdata/sbx/init.sql, scout tables included)
	f.db = sbxNewDB(t, filepath.Join(tmp, "db-home", "factory.db"))
	sbxExec(t, f.db, "INSERT INTO scout_candidates(id,key,repo,head_sha,install_risk,status,sources,first_seen,last_seen) "+
		"VALUES('C-0001','gh:o/r','o/r',?,'prompt-only','new','[]','t','t')", sbxSHA)
	f.pluginSrc = filepath.Join(f.root, "vendor", "C-0001@"+sbxSHA)
	sbxWrite(t, filepath.Join(f.pluginSrc, "plugin", "skills", "s", "SKILL.md"), "# s\nJust prose.\n", 0o644)
	f.writeMeta()

	// the trial DB seed: an empty schema copy stands in for `factory init`; the Go seed rows go on top
	tplDB := sbxNewDB(t, filepath.Join(tmp, "fh-template", "factory.db"))
	sbxMust(t, tplDB.Close()) // the template is only copied
	tpl := filepath.Join(tmp, "fh-template", "factory.db")
	f.builder = &sbxBuilder{skipPreflight: true, real: skilleval.SandboxBuilder{
		TasksDir:    filepath.Join(repo, "skilleval", "tasks"),
		FactoryInit: []string{"sh", "-c", `mkdir -p "$FACTORY_HOME/db" && cp "$1" "$FACTORY_HOME/db/factory.db"`, "sh", tpl},
		WriteShim:   sbxWriteShim,
	}}
	f.api = &sbxAPI{clock: time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC), home: f.home, live: f.live,
		cfg: map[string]string{"skilleval_root": f.root}}
	f.out = &strings.Builder{}
	f.env = &skilleval.Env{
		Collector:  skilleval.Collector{TasksDir: filepath.Join(repo, "skilleval", "tasks")},
		BinDir:     filepath.Join(repo, "bin"),
		Actor:      "owner",
		Home:       f.live,
		PasswdHome: f.home,
		UnshareBin: "unshare",
		Out:        f.out,
		Api:        f.api,
		NewGh:      scout.NewGh,
		Builder:    f.builder,
		Spawn: func(_ *sql.DB, name, cwd, prompt string, env [][2]string, plugins []string) error {
			f.spawned = append(f.spawned, sbxSpawnArgs{name, cwd, prompt, env, plugins})
			return f.spawnErr
		},
	}
	return f
}

func (f *sbxFix) writeMeta() {
	f.t.Helper()
	plugin := filepath.Join(f.pluginSrc, "plugin")
	var files []string
	sbxMust(f.t, filepath.WalkDir(plugin, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, rerr := filepath.Rel(plugin, p)
			if rerr != nil {
				return rerr
			}
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	}))
	sort.Strings(files)
	meta, err := json.Marshal(map[string]any{"candidate": "C-0001", "sha": sbxSHA, "plugin": "plugin", "copied": files,
		"skipped": []any{}, "prescreen_hits": []any{}})
	sbxMust(f.t, err)
	sbxWrite(f.t, filepath.Join(f.pluginSrc, "VENDOR.json"), string(meta), 0o644)
}

// trialRow inserts a skill_trials row with status and returns it.
func (f *sbxFix) trialRow(cond, status string) skilleval.Trial {
	f.t.Helper()
	res, err := f.db.Exec("INSERT INTO skill_trials(candidate_id,vendor_sha,task,condition,status,queued) VALUES(?,?,?,?,?,'t')",
		"C-0001", sbxSHA, "fix-bug", cond, status)
	sbxMust(f.t, err)
	id, err := res.LastInsertId()
	sbxMust(f.t, err)
	tr, ok, err := skilleval.GetTrial(f.db, id)
	if err != nil || !ok {
		f.t.Fatalf("trial %d: %v %v", id, ok, err)
	}
	return tr
}

func (f *sbxFix) build(cond string) skilleval.Info {
	f.t.Helper()
	info, err := f.builder.Build(f.db, f.trialRow(cond, "pending"), f.root, f.home, "unshare")
	sbxMust(f.t, err)
	return info
}

// launch is skilleval_launch through Env.Launch: the claimed row is 'starting'; the spawner is recorded.
func (f *sbxFix) launch(cond string) (skilleval.Info, sbxSpawnArgs) {
	f.t.Helper()
	before := len(f.spawned)
	info, err := f.env.Launch(f.db, f.trialRow(cond, "starting"), f.root, f.home, "unshare")
	sbxMust(f.t, err)
	if len(f.spawned) != before+1 {
		f.t.Fatalf("spawner calls %d, want %d", len(f.spawned), before+1)
	}
	return info, f.spawned[len(f.spawned)-1]
}

func (f *sbxFix) trialsRoot() string { return filepath.Join(f.root, "trials") }

func sbxExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func (f *sbxFix) count(q string, a ...any) int {
	f.t.Helper()
	var n int
	sbxMust(f.t, f.db.QueryRow(q, a...).Scan(&n))
	return n
}

func sbxNeedUserns(t *testing.T) {
	t.Helper()
	if ok, why := sandbox.UsernsAvailable("unshare"); !ok {
		t.Skipf("user namespaces unavailable: %s", why)
	}
}

// ---- spawn.Plan side (the spawn() contract) ----

func sbxGate(open bool) (gate.Snapshot, gate.Config) {
	f64 := func(v float64) *float64 { return &v }
	snap := gate.Snapshot{Now: time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC), HerdrReachable: open, MemMB: 3000,
		Usage: gate.Usage{FiveHour: f64(10), SevenDay: f64(20), Fresh: true},
		Procs: gate.ProcScan{Known: true, Argv: map[int][]string{}}, Load1: 0.5, NumCPU: 8}
	cfg := gate.Config{MaxLiveAgents: 3, MaxTotalClaude: 10, MinMemAvailableMB: 500, TmpMaxPct: 90, MaxFiveHourPct: 90,
		MaxSevenDayPct: 95, LoadRatio: 1.5, MaxLiveLeads: 4, ReservedWorkerSlots: 10, ReviewReservedSlots: 2,
		ReviewAutospawn: "off", LeadParking: "0", MinMemText: "500", TmpMaxPctText: "90", LoadRatioText: "1.5",
		FiveHourText: "90", SevenDayText: "95"}
	return snap, cfg
}

func (f *sbxFix) readRepo(rel string) string {
	f.t.Helper()
	s, err := spawn.ReadText(filepath.Join(f.repo, rel))
	sbxMust(f.t, err)
	return s
}

// sbxShimOK is the executable/owned/not-a-symlink predicate the caller of spawn.Plan supplies (Python: check_evaluator_spawn).
func sbxShimOK(path string) bool {
	var st syscall.Stat_t
	if err := syscall.Lstat(path, &st); err != nil {
		return false
	}
	return st.Mode&syscall.S_IFMT == syscall.S_IFREG && st.Mode&0o111 != 0 && int(st.Uid) == os.Getuid()
}

func (f *sbxFix) facts(role string, gateOpen bool, agent func(string) *spawn.AgentRow) spawn.Facts {
	snap, cfg := sbxGate(gateOpen)
	if agent == nil {
		agent = func(string) *spawn.AgentRow { return nil }
	}
	return spawn.Facts{
		Config: map[string]string{"org_prefix_default": "1", "opus_effort": "medium"},
		Agent:  agent, Gate: snap, GateConfig: cfg,
		Home: f.live, RunDir: filepath.Join(f.live, "run"), RolesDir: filepath.Join(f.repo, "roles"),
		RoleSettingsTemplate: f.readRepo("roles/settings/" + role + ".json.in"),
		Tokens:               spawn.SettingsTokens(f.live, f.home, filepath.Join(f.home, ".config")),
		CommonText:           f.readRepo("roles/_common.md"), RoleText: f.readRepo("roles/" + role + ".md"), EnabledOrgs: 1,
		Eval: spawn.EvalFacts{Root: sandbox.Realpath(f.root), Realpath: sandbox.Realpath, ShimOK: sbxShimOK, PromptOnly: scout.AssertPromptOnlyTree},
	}
}

func sbxKV(pairs [][2]string) []spawn.KV {
	var out []spawn.KV
	for _, p := range pairs {
		out = append(out, spawn.KV{Key: p[0], Value: p[1]})
	}
	return out
}

func (f *sbxFix) request(s sbxSpawnArgs) spawn.Request {
	return spawn.Request{Role: "evaluator", Name: s.name, Cwd: s.cwd, Prompt: s.prompt, EnvExtra: sbxKV(s.env),
		PluginDirs: s.plugins, SessionID: "sid-1"}
}

func (f *sbxFix) plan(r spawn.Request) spawn.SpawnPlan {
	return spawn.Plan(r, f.facts(r.Role, true, nil))
}

func sbxEnvMap(env []spawn.KV) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		m[kv.Key] = kv.Value
	}
	return m
}

// check is check_spawn: argv and env of the planned evaluator start.
func (f *sbxFix) check(info skilleval.Info, p spawn.SpawnPlan, withPlugin bool) {
	f.t.Helper()
	t := f.t
	if p.Refusal != nil || p.Start == nil {
		t.Fatalf("planned start expected, refusal %+v", p.Refusal)
	}
	trial := sandbox.Realpath(info.Dir)
	args := p.Start.ClaudeArgs
	var idx []int
	for i, x := range args {
		if x == "--plugin-dir" {
			idx = append(idx, i)
		}
	}
	if withPlugin {
		if len(idx) != 1 {
			t.Fatalf("want one --plugin-dir: %v", args)
		}
		if r := sandbox.Realpath(args[idx[0]+1]); !strings.HasPrefix(r, sandbox.Realpath(f.root)+"/vendor/") {
			t.Fatalf("plugin dir %s not under the vendor root", r)
		}
	} else if len(idx) != 0 {
		t.Fatalf("no --plugin-dir expected: %v", args)
	}
	for _, x := range args {
		if x == "--add-dir" {
			t.Fatalf("--add-dir in %v", args)
		}
	}
	all := append([]string{}, args...)
	for _, kv := range p.Start.Env {
		all = append(all, kv.Key+"="+kv.Value)
	}
	all = append(all, p.Start.Cwd)
	for _, x := range all {
		if strings.Contains(x, f.live) {
			t.Fatalf("live factory home in %q", x)
		}
		if strings.Contains(x, "roles/plugins") {
			t.Fatalf("roles/plugins in %q", x)
		}
	}
	env := sbxEnvMap(p.Start.Env)
	for k, want := range map[string]string{"FACTORY_HOME": trial + "/fh", "HOME": trial + "/home", "CLAUDE_CONFIG_DIR": trial + "/cfg",
		"FACTORY_ROLE": "evaluator", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1", "DISABLE_AUTOUPDATER": "1"} {
		if env[k] != want {
			t.Errorf("env %s = %q, want %q", k, env[k], want)
		}
	}
	path := strings.Split(env["PATH"], string(os.PathListSeparator))
	if path[0] != trial+"/bin" || !sbxHas(path, filepath.Join(f.repo, "bin")) || !sbxHas(path, filepath.Join(f.home, ".local/bin")) {
		t.Errorf("PATH %v", path)
	}
	if p.Start.Cwd != trial+"/work" {
		t.Errorf("cwd %q", p.Start.Cwd)
	}
	raw, err := json.Marshal(p)
	sbxMust(t, err)
	if strings.Contains(string(raw), sbxToken) {
		t.Errorf("the token is in the plan")
	}
}

func sbxHas(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func sbxExit(t testing.TB, err error, code int) *skilleval.ExitError {
	t.Helper()
	var ee *skilleval.ExitError
	if !errors.As(err, &ee) || ee.Code != code {
		t.Fatalf("want exit %d, got %v", code, err)
	}
	return ee
}

func sbxTry(f *sbxFix, task, cand, cond string) error {
	return f.env.Try(f.db, skilleval.TryOpts{Task: task, Candidate: cand, Condition: cond, HasCandidate: cand != ""})
}

// ---- LayoutTests ----

func TestSkillevalSandbox_LayoutAndSeed(t *testing.T) {
	f := newSbxFix(t)
	info := f.build("with")
	d, work := info.Dir, info.Work
	git := func(args ...string) string {
		c := exec.Command("git", append([]string{"-C", work}, args...)...)
		c.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
		out, err := c.Output()
		sbxMust(t, err)
		return strings.TrimSpace(string(out))
	}
	if n := git("rev-list", "--count", "HEAD"); n != "1" {
		t.Errorf("commits %q", n)
	}
	if r := git("remote"); r != "" {
		t.Errorf("remote %q", r)
	}
	if st, err := os.Stat(filepath.Join(work, "taskdeps.py")); err != nil || !st.Mode().IsRegular() {
		t.Errorf("taskdeps.py: %v", err)
	}
	if sbxExists(filepath.Join(work, "solution")) || sbxExists(filepath.Join(work, "hidden")) {
		t.Errorf("solution or hidden copied into work")
	}
	ents, err := os.ReadDir(filepath.Join(d, "home"))
	sbxMust(t, err)
	if len(ents) != 1 || ents[0].Name() != sandbox.HomeRC[0] {
		t.Errorf("home entries %v", ents)
	}
	raw, err := os.ReadFile(filepath.Join(d, "cfg", ".claude.json"))
	sbxMust(t, err)
	var seed map[string]any
	sbxMust(t, json.Unmarshal(raw, &seed))
	want := map[string]any{"hasCompletedOnboarding": true, "theme": "dark", "projects": map[string]any{
		sandbox.Realpath(work): map[string]any{"hasTrustDialogAccepted": true, "hasCompletedProjectOnboarding": true}}}
	if !reflect.DeepEqual(seed, want) {
		t.Errorf("seed %v", seed)
	}
	// the shim: a test double here (the real one is Python); mode and content assertions are about the double
	shim := filepath.Join(d, "bin", "claude")
	if st, err := os.Stat(shim); err != nil || st.Mode().Perm() != 0o755 {
		t.Errorf("shim %v %v", st, err)
	}
	body, err := os.ReadFile(shim)
	sbxMust(t, err)
	if !strings.Contains(string(body), f.claude) || strings.Contains(string(body), sbxToken) {
		t.Errorf("shim text %q", body)
	}
	// the trial DB: the task seed and the config rows `factory config herdr_socket` / `git_push` would print
	fh := dbtest.Raw(t, skilleval.FhDB(filepath.Join(d, "fh")))
	defer fh.Close()
	var n int
	sbxMust(t, fh.QueryRow("SELECT count(*) FROM messages WHERE recipient=? AND read_at IS NULL", info.Agent).Scan(&n))
	if n != 1 {
		t.Errorf("unread messages %d", n)
	}
	var role, status string
	sbxMust(t, fh.QueryRow("SELECT role,status FROM agents WHERE name=?", info.Agent).Scan(&role, &status))
	if role != "evaluator" || status != "live" {
		t.Errorf("agent row %s %s", role, status)
	}
	var sock, push string
	sbxMust(t, fh.QueryRow("SELECT value FROM config WHERE key='herdr_socket'").Scan(&sock))
	sbxMust(t, fh.QueryRow("SELECT value FROM config WHERE key='git_push'").Scan(&push))
	if sock != sandbox.DeadSocket || push != "0" {
		t.Errorf("config rows %q %q", sock, push)
	}
	var spec string
	sbxMust(t, fh.QueryRow("SELECT spec FROM tasks WHERE id=?", sandbox.SeedTask).Scan(&spec))
	first, err := os.ReadFile(filepath.Join(f.repo, "skilleval/tasks/fix-bug/task.md"))
	sbxMust(t, err)
	if spec != string(first) {
		t.Errorf("seeded spec differs from task.md")
	}
}

func TestSkillevalSandbox_WithoutConditionHasNoPlugin(t *testing.T) {
	f := newSbxFix(t)
	if info := f.build("without"); info.Plugin != "" {
		t.Fatalf("plugin %q", info.Plugin)
	}
}

func TestSkillevalSandbox_MissingOrLooseTokenBuildsNothing(t *testing.T) {
	f := newSbxFix(t)
	for name, fix := range map[string]func(){
		"missing": func() { sbxMust(t, os.Remove(f.token)) },
		"loose":   func() { sbxMust(t, os.Chmod(f.token, 0o644)) },
	} {
		t.Run(name, func(t *testing.T) {
			sbxWrite(t, f.token, sbxToken, 0o600)
			sbxExec(t, f.db, "DELETE FROM skill_trials")
			fix()
			_, err := f.builder.Build(f.db, f.trialRow("with", "pending"), f.root, f.home, "unshare")
			if !errors.Is(err, sandbox.ErrSandboxRefused) || !strings.Contains(err.Error(), "owner decision D3 pending: no eval token") {
				t.Fatalf("err %v", err)
			}
			if sbxExists(f.trialsRoot()) {
				t.Fatalf("trials dir created")
			}
		})
	}
}

func TestSkillevalSandbox_SymlinkedTokenRefused(t *testing.T) {
	f := newSbxFix(t)
	real := filepath.Join(f.home, "real-token")
	sbxWrite(t, real, sbxToken, 0o600)
	sbxMust(t, os.Remove(f.token))
	sbxMust(t, os.Symlink(real, f.token))
	if err := sandbox.CheckToken(f.token); !errors.Is(err, sandbox.ErrSandboxRefused) {
		t.Fatalf("err %v", err)
	}
}

// ---- SpawnTests ----

func TestSkillevalSandbox_WithConditionArgvAndEnv(t *testing.T) {
	f := newSbxFix(t)
	info, s := f.launch("with")
	if s.name != info.Agent {
		t.Fatalf("spawned %q want %q", s.name, info.Agent)
	}
	f.check(info, f.plan(f.request(s)), true)
}

func TestSkillevalSandbox_WithoutConditionHasNoPluginDir(t *testing.T) {
	f := newSbxFix(t)
	info, s := f.launch("without")
	if len(s.plugins) != 0 {
		t.Fatalf("plugins %v", s.plugins)
	}
	f.check(info, f.plan(f.request(s)), false)
}

func TestSkillevalSandbox_RoleFilesAreReadOnlyCopiesInsideTheTrial(t *testing.T) {
	f := newSbxFix(t)
	info, s := f.launch("with")
	p := f.plan(f.request(s))
	if p.Start == nil {
		t.Fatalf("refusal %+v", p.Refusal)
	}
	trial := sandbox.Realpath(info.Dir)
	argAfter := func(flag string) string {
		for i, x := range p.Start.ClaudeArgs {
			if x == flag && i+1 < len(p.Start.ClaudeArgs) {
				return p.Start.ClaudeArgs[i+1]
			}
		}
		return ""
	}
	if got := argAfter("--settings"); got != trial+"/role/settings.json" {
		t.Errorf("--settings %q", got)
	}
	if got := argAfter("--append-system-prompt-file"); got != trial+"/role/prompt.md" {
		t.Errorf("--append-system-prompt-file %q", got)
	}
	if !strings.Contains(p.Start.PromptText, "evaluator") {
		t.Errorf("the brief does not mention evaluator")
	}
}

func TestSkillevalSandbox_PluginOutsideVendorRootDiesBeforeAnyCall(t *testing.T) {
	f := newSbxFix(t)
	outside := filepath.Join(f.live, "elsewhere")
	sbxWrite(t, filepath.Join(outside, "skills", "x.md"), "prose", 0o644)
	_, s := f.launch("with")
	s.plugins = []string{outside}
	p := f.plan(f.request(s))
	if p.Refusal == nil || p.Refusal.Code != 3 || p.Start != nil || p.Queue != nil {
		t.Fatalf("plan %+v", p)
	}
	if n := f.count("SELECT count(*) FROM agents WHERE name=?", s.name); n != 0 {
		t.Fatalf("agent rows %d", n)
	}
}

func TestSkillevalSandbox_VendoredDirWithPlantedCjsDies(t *testing.T) {
	f := newSbxFix(t)
	_, s := f.launch("with")
	sbxWrite(t, filepath.Join(f.pluginSrc, "plugin", "evil.cjs"), "process.exit(1)", 0o644)
	p := f.plan(f.request(s))
	if p.Refusal == nil || p.Refusal.Code != 3 || p.Start != nil {
		t.Fatalf("plan %+v", p)
	}
}

func TestSkillevalSandbox_SymlinkOutOfVendorRootDies(t *testing.T) {
	f := newSbxFix(t)
	link := filepath.Join(f.root, "vendor", "link")
	sbxMust(t, os.Symlink(f.live, link))
	_, s := f.launch("with")
	s.plugins = []string{link}
	p := f.plan(f.request(s))
	if p.Refusal == nil || p.Start != nil {
		t.Fatalf("plan %+v", p)
	}
}

func TestSkillevalSandbox_NonEvaluatorMayNotPassSandboxParams(t *testing.T) {
	f := newSbxFix(t)
	for name, mod := range map[string]func(*spawn.Request){
		"env_extra":    func(r *spawn.Request) { r.EnvExtra = []spawn.KV{{Key: "X", Value: "1"}} },
		"plugin_dirs":  func(r *spawn.Request) { r.PluginDirs = []string{"/tmp/x"} },
		"add_home_dir": func(r *spawn.Request) { r.AddHomeDir = false },
	} {
		t.Run(name, func(t *testing.T) {
			r := spawn.Request{Role: "coder", Name: "zz-c", AddHomeDir: true, SessionID: "sid-1"}
			mod(&r)
			p := spawn.Plan(r, f.facts("coder", true, nil))
			if p.Refusal == nil || p.Refusal.Code != 3 || p.Start != nil || p.Queue != nil {
				t.Fatalf("plan %+v", p)
			}
		})
	}
}

func TestSkillevalSandbox_NonEvaluatorWithoutParamsStillSpawns(t *testing.T) {
	f := newSbxFix(t)
	p := spawn.Plan(spawn.Request{Role: "pm", Name: "zz-pm", Cwd: f.live, AddHomeDir: true, SessionID: "sid-1"}, f.facts("pm", true, nil))
	if p.Refusal != nil || p.Start == nil {
		t.Fatalf("plan %+v", p.Refusal)
	}
	if !sbxHas(p.Start.ClaudeArgs, "--add-dir") {
		t.Fatalf("no --add-dir in %v", p.Start.ClaudeArgs)
	}
}

func TestSkillevalSandbox_EvaluatorWithoutASandboxDies(t *testing.T) {
	f := newSbxFix(t)
	for name, mod := range map[string]func(*spawn.Request){
		"bare": func(r *spawn.Request) {},
		"live FACTORY_HOME": func(r *spawn.Request) {
			r.EnvExtra, r.AddHomeDir = []spawn.KV{{Key: "FACTORY_HOME", Value: f.live}}, false
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := spawn.Request{Role: "evaluator", Name: "eval-x", Cwd: f.live, AddHomeDir: true, SessionID: "sid-1"}
			mod(&r)
			p := f.plan(r)
			if p.Refusal == nil || p.Refusal.Code != 3 || p.Start != nil {
				t.Fatalf("plan %+v", p)
			}
		})
	}
}

func TestSkillevalSandbox_BadEnvCwdOrPathDie(t *testing.T) {
	f := newSbxFix(t)
	info, s := f.launch("with")
	with := func(k, v string) func(*spawn.Request) {
		return func(r *spawn.Request) {
			r.EnvExtra = append([]spawn.KV{}, r.EnvExtra...)
			for i := range r.EnvExtra {
				if r.EnvExtra[i].Key == k {
					r.EnvExtra[i].Value = v
					return
				}
			}
			r.EnvExtra = append(r.EnvExtra, spawn.KV{Key: k, Value: v})
		}
	}
	good := f.request(s)
	if p := f.plan(good); p.Refusal != nil {
		t.Fatalf("the good request is refused: %+v", p.Refusal)
	}
	path := sbxEnvMap(good.EnvExtra)["PATH"]
	bads := map[string]func(*spawn.Request){
		"cwd live home":   func(r *spawn.Request) { r.Cwd = f.live },
		"cwd trial dir":   func(r *spawn.Request) { r.Cwd = info.Dir },
		"FACTORY_HOME":    with("FACTORY_HOME", f.live),
		"HOME":            with("HOME", f.home),
		"CLAUDE_CONFIG":   with("CLAUDE_CONFIG_DIR", filepath.Join(f.home, ".claude")),
		"PATH prefixed":   with("PATH", "/usr/bin:"+path),
		"oauth token key": with("CLAUDE_CODE_OAUTH_TOKEN", sbxToken),
		"LD_PRELOAD":      with("LD_PRELOAD", "/tmp/x.so"),
	}
	for name, mod := range bads {
		t.Run(name, func(t *testing.T) {
			r := f.request(s)
			mod(&r)
			p := f.plan(r)
			if p.Refusal == nil || p.Refusal.Code != 3 || p.Start != nil {
				t.Fatalf("plan %+v", p)
			}
		})
	}
}

// sbxQueueClosed plans the evaluator with the gate closed: the row that is queued.
func (f *sbxFix) queueClosed(s sbxSpawnArgs) spawn.SpawnPlan {
	f.t.Helper()
	p := spawn.Plan(f.request(s), f.facts("evaluator", false, nil))
	if p.Refusal != nil || p.Queue == nil {
		f.t.Fatalf("expected a queued row: %+v", p.Refusal)
	}
	return p
}

// dequeue is the tick: the queued row's spawn_args become the next request, the row is claimed ('starting').
func (f *sbxFix) dequeue(name, argsJSON string) spawn.SpawnPlan {
	f.t.Helper()
	var a struct {
		EnvExtra   map[string]string `json:"env_extra"`
		PluginDirs []string          `json:"plugin_dirs"`
		Cwd        string            `json:"cwd"`
		Prompt     string            `json:"prompt"`
		AddHomeDir bool              `json:"add_home_dir"`
	}
	sbxMust(f.t, json.Unmarshal([]byte(argsJSON), &a))
	var keys []string
	for k := range a.EnvExtra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var env []spawn.KV
	for _, k := range keys {
		env = append(env, spawn.KV{Key: k, Value: a.EnvExtra[k]})
	}
	req := spawn.Request{Role: "evaluator", Name: name, Cwd: a.Cwd, Prompt: a.Prompt, EnvExtra: env, PluginDirs: a.PluginDirs,
		AddHomeDir: a.AddHomeDir, FromQueue: true, SessionID: "sid-1"}
	row := func(string) *spawn.AgentRow {
		return &spawn.AgentRow{Name: name, Role: "evaluator", Status: "starting", SpawnArgs: []byte(argsJSON)}
	}
	return spawn.Plan(req, f.facts("evaluator", true, row))
}

func TestSkillevalSandbox_QueuedEvaluatorKeepsEnvAndPluginAfterDequeue(t *testing.T) {
	f := newSbxFix(t)
	info, s := f.launch("with")
	q := f.queueClosed(s)
	var args map[string]any
	sbxMust(t, json.Unmarshal([]byte(q.Queue.ArgsJSON), &args))
	if got := args["plugin_dirs"]; !reflect.DeepEqual(got, []any{info.Plugin}) {
		t.Fatalf("plugin_dirs %v want %v", got, info.Plugin)
	}
	p := f.dequeue(s.name, q.Queue.ArgsJSON)
	f.check(info, p, true)
}

func TestSkillevalSandbox_QueuedEvaluatorThatNoLongerValidatesIsCancelledNotATickCrash(t *testing.T) {
	f := newSbxFix(t)
	info, s := f.launch("with")
	q := f.queueClosed(s)
	sbxMust(t, os.RemoveAll(info.Dir))
	p := f.dequeue(s.name, q.Queue.ArgsJSON)
	// the planner reports the evaluator-invalid reason, on which (and only which) the tick cancels the row; no start
	if p.Refusal == nil || p.Refusal.Reason != spawn.ReasonEvaluator || p.Start != nil {
		t.Fatalf("plan %+v", p)
	}
}

// The tick that cancels the row is Python-only (the Go tick has no evaluator queue pass): the Python `factory tick` runs on the
// harness home, fed with the queued row and the trial row the Go builder and planner produced.
func TestSkillevalSandbox_TickCancelOfInvalidQueuedEvaluatorEndsTheTrialWithAReason(t *testing.T) {
	env := ft.New(t)
	env.NeedPython()
	env.HerdrStateful()
	f := newSbxFix(t)
	info, s := f.launch("with")
	q := f.queueClosed(s)
	sbxMust(t, os.RemoveAll(info.Dir))
	d := env.RawDB()
	defer d.Close()
	// both pane stubs are on PATH: pin the TUI as the Python harness does
	sbxExec(t, d, "UPDATE config SET value='herdr' WHERE key='terminal_backend'")
	sbxExec(t, d, "UPDATE config SET value=? WHERE key='skilleval_root'", f.root)
	sbxExec(t, d, "INSERT INTO agents(name,role,status,spawn_args,queued_at) VALUES(?,?,'queued',?,'t')", s.name, "evaluator", q.Queue.ArgsJSON)
	sbxExec(t, d, "INSERT INTO skill_trials(candidate_id,vendor_sha,task,condition,status,queued,agent,started) VALUES('C-0001',?,'fix-bug','with','running','t',?,'t')",
		sbxSHA, s.name)
	env.OK("tick")
	if got := env.SQL("SELECT status FROM agents WHERE name=?", s.name); len(got) != 1 || got[0][0] != "cancelled" {
		t.Fatalf("agent rows %v", got)
	}
	rows := env.SQL("SELECT status, passed, ended IS NOT NULL, check_output FROM skill_trials")
	if len(rows) != 1 || rows[0][0] != "cancelled" || rows[0][1] != "0" || rows[0][2] != "1" || !strings.Contains(rows[0][3], "no longer validates") {
		t.Fatalf("trial row %v", rows)
	}
	for _, c := range env.Calls("") {
		if len(c.Argv) >= 2 && c.Argv[0] == "agent" && c.Argv[1] == "start" {
			t.Fatalf("an evaluator was started: %v", c.Argv)
		}
	}
}

// The next three drive the retry/cap logic of the Python tick, which the Python test reaches by mock.patch of _spawn_start so
// that it raises SystemExit. The pane stub's start_fail knob is not equivalent (the start is asynchronous there: the agent goes
// live with a pending start and the tick later stops it), so no honest stand-in exists.
func TestNotPortedSandbox_TickRetriesAQueuedEvaluatorAfterATransientSpawnFailure(t *testing.T) {
	t.Skip("not ported: mock.patch of the Python _spawn_start/cmd_tick (evaluator-spawn-retry event); the stub's start_fail knob is asynchronous and does not make spawn die; no Go counterpart")
}

func TestNotPortedSandbox_PersistentSpawnFailureEndsTheTrialAfterTheCap(t *testing.T) {
	t.Skip("not ported: mock.patch of the Python _spawn_start and EVALUATOR_SPAWN_RETRY_MAX of the Python tick; no Go counterpart")
}

func TestNotPortedSandbox_TransientFailuresBelowTheCapThenSuccessKeepsTheTrialRunning(t *testing.T) {
	t.Skip("not ported: mock.patch of the Python _spawn_start and the Python tick retry cap; no Go counterpart")
}

func TestNotPortedSandbox_ShimMustBeExecutable(t *testing.T) {
	t.Skip("not ported: the shim predicate (executable, owned, not a symlink) is a callback of spawn.EvalFacts supplied by the not yet written Go spawn caller; Python tested its own implementation")
}

func TestNotPortedSandbox_ShimMustBeOwnedByTheUser(t *testing.T) {
	t.Skip("not ported: mock.patch of os.getuid in Python check_evaluator_spawn; the Go shim predicate is supplied by the caller of spawn.Plan")
}

func TestNotPortedSandbox_ShimMustNotBeASymlink(t *testing.T) {
	t.Skip("not ported: the shim predicate is a callback of spawn.EvalFacts supplied by the not yet written Go spawn caller")
}

func TestNotPortedSandbox_SkillevalTryResolvesTheRootOnce(t *testing.T) {
	t.Skip("not ported: ast count of resolve_root( calls in the Python skilleval_try source; Env.Try is Go code with its own structure")
}

func TestNotPortedSandbox_NoUnreadLiveMessageAndTickDoesNotNudge(t *testing.T) {
	t.Skip("not ported: spawn marking the kickoff message read, the herdr agent prompt call and the tick nudge are Python-only runtime behaviour")
}

func sbxTableDump(t testing.TB, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table'")
	sbxMust(t, err)
	var tables []string
	for rows.Next() {
		var n string
		sbxMust(t, rows.Scan(&n))
		tables = append(tables, n)
	}
	sbxMust(t, rows.Err())
	sbxMust(t, rows.Close())
	var b strings.Builder
	for _, tb := range tables {
		r, err := db.Query("SELECT * FROM " + tb)
		sbxMust(t, err)
		cols, err := r.Columns()
		sbxMust(t, err)
		for r.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			sbxMust(t, r.Scan(ptrs...))
			fmt.Fprintf(&b, "%s: %v\n", tb, vals)
		}
		sbxMust(t, r.Err())
		sbxMust(t, r.Close())
	}
	return b.String()
}

func TestSkillevalSandbox_TokenNeverReachesACallOrTheLiveDbAndSpawnNeverOpensIt(t *testing.T) {
	f := newSbxFix(t)
	info, s := f.launch("with")
	if raw, err := json.Marshal(s); err != nil || strings.Contains(string(raw), sbxToken) {
		t.Fatalf("the token is in the spawn arguments (%v)", err)
	}
	live := sbxTableDump(t, f.db)
	if strings.Contains(live, sbxToken) {
		t.Fatal("the token is in the live DB")
	}
	if !strings.Contains(live, info.Agent) && !strings.Contains(live, "skilleval") {
		t.Fatal("the dump does not hold the trial: the grep would be meaningless")
	}
	fh := dbtest.Raw(t, skilleval.FhDB(filepath.Join(info.Dir, "fh")))
	defer fh.Close()
	trialDump := sbxTableDump(t, fh)
	if strings.Contains(trialDump, sbxToken) {
		t.Fatal("the token is in the trial DB")
	}
	if !strings.Contains(trialDump, info.Agent) {
		t.Fatal("the trial dump does not hold the agent: the grep would be meaningless")
	}
	// the sources of the spawn path never read the token (Python grepped spawn, _spawn_start and check_evaluator_spawn)
	mod, err := ft.ModuleRoot()
	sbxMust(t, err)
	srcs, err := filepath.Glob(filepath.Join(mod, "internal", "spawn", "*.go"))
	sbxMust(t, err)
	n := 0
	for _, src := range srcs {
		if strings.HasSuffix(src, "_test.go") {
			continue
		}
		body, err := os.ReadFile(src)
		sbxMust(t, err)
		n++
		for _, bad := range []string{"oauth-token", "ReadToken", "readTokenFile"} {
			if strings.Contains(string(body), bad) {
				t.Errorf("%s mentions %s", src, bad)
			}
		}
	}
	if n < 2 {
		t.Fatalf("only %d sources checked", n)
	}
}

// ---- TryTests ----

func TestSkillevalSandbox_CliTryRefusesMissingTokenWithoutASandbox(t *testing.T) {
	f := newSbxFix(t)
	sbxMust(t, os.Remove(f.token))
	err := sbxTry(f, "fix-bug", "C-0001", "")
	ee := sbxExit(t, err, 3)
	if !strings.Contains(ee.Msg, "owner decision D3 pending: no eval token") {
		t.Errorf("msg %q", ee.Msg)
	}
	if sbxExists(f.trialsRoot()) || len(f.spawned) != 0 {
		t.Errorf("trials dir or spawn call exists")
	}
}

func TestSkillevalSandbox_CliTryRefusesLooseTokenMode(t *testing.T) {
	f := newSbxFix(t)
	sbxMust(t, os.Chmod(f.token, 0o644))
	ee := sbxExit(t, sbxTry(f, "fix-bug", "C-0001", ""), 3)
	if !strings.Contains(ee.Msg, "owner decision D3 pending") {
		t.Errorf("msg %q", ee.Msg)
	}
	if sbxExists(f.trialsRoot()) {
		t.Errorf("trials dir exists")
	}
}

func TestSkillevalSandbox_NoUserNamespacesMeansNoFallback(t *testing.T) {
	f := newSbxFix(t)
	f.env.UnshareBin = "/nonexistent/unshare"
	ee := sbxExit(t, sbxTry(f, "fix-bug", "C-0001", ""), 3)
	if !strings.Contains(ee.Msg, "no unsandboxed fallback") {
		t.Errorf("msg %q", ee.Msg)
	}
	if sbxExists(f.trialsRoot()) || len(f.spawned) != 0 {
		t.Errorf("trials dir or spawn call exists")
	}
	if n := f.count("SELECT count(*) FROM agents WHERE role='evaluator'"); n != 0 {
		t.Errorf("evaluator agents %d", n)
	}
	if n := f.count("SELECT count(*) FROM skill_trials"); n != 0 {
		t.Errorf("trial rows %d", n)
	}
}

func TestSkillevalSandbox_HideFailureIsRefusedAndCleanedUp(t *testing.T) {
	f := newSbxFix(t)
	// an unshare that always succeeds stands in for mock.patch(userns_available -> ok); the preflight is the canned refusal
	fake := filepath.Join(f.home, "fake-unshare")
	sbxWrite(t, fake, "#!/bin/sh\nexit 0\n", 0o755)
	f.env.UnshareBin = fake
	f.builder.preflightErr = fmt.Errorf("%w: sandbox preflight failed: mount", sandbox.ErrSandboxRefused)
	ee := sbxExit(t, sbxTry(f, "fix-bug", "C-0001", ""), 3)
	if !strings.Contains(ee.Msg, "sandbox preflight failed: mount") {
		t.Errorf("msg %q", ee.Msg)
	}
	var status string
	sbxMust(t, f.db.QueryRow("SELECT status FROM skill_trials").Scan(&status))
	if status != "error" {
		t.Errorf("trial status %q", status)
	}
	if len(f.spawned) != 0 {
		t.Errorf("spawned %v", f.spawned)
	}
	if n := f.count("SELECT count(*) FROM agents WHERE role='evaluator'"); n != 0 {
		t.Errorf("evaluator agents %d", n)
	}
	ents, err := os.ReadDir(f.trialsRoot())
	sbxMust(t, err)
	if len(ents) != 0 {
		t.Errorf("trial dirs left: %v", ents)
	}
}

func TestSkillevalSandbox_NonPromptOnlyAndNeedsOwnerCandidatesAreRefused(t *testing.T) {
	f := newSbxFix(t)
	sbxExec(t, f.db, "INSERT INTO scout_candidates(id,key,head_sha,install_risk,status,sources,first_seen,last_seen) "+
		"VALUES('C-0002','gh:o/s',?,'scripts','new','[]','t','t')", sbxSHA)
	sbxExec(t, f.db, "INSERT INTO scout_candidates(id,key,head_sha,install_risk,status,sources,first_seen,last_seen) "+
		"VALUES('C-0003','gh:o/t',?,'prompt-only','needs-owner','[]','t','t')", sbxSHA)
	for _, cid := range []string{"C-0002", "C-0003"} {
		ee := sbxExit(t, sbxTry(f, "fix-bug", cid, ""), 2)
		if !strings.Contains(ee.Msg, "not eligible") {
			t.Errorf("%s: msg %q", cid, ee.Msg)
		}
	}
	if n := f.count("SELECT count(*) FROM skill_trials"); n != 0 {
		t.Errorf("trial rows %d", n)
	}
	if sbxExists(f.trialsRoot()) {
		t.Errorf("trials dir exists")
	}
}

// TestSkillevalSandbox_Roles drives the Python CLI: `skilleval try` stays Python-only, the worker refusal is its.
func TestSkillevalSandbox_Roles(t *testing.T) {
	env := ft.New(t)
	env.NeedPython()
	for _, r := range []string{"coder", "qa", "reviewer", "pm"} {
		env.SeedAgent(ft.AgentRow{Name: "zz-" + r, Role: r, Status: "live"})
	}
	for _, r := range []string{"coder", "qa", "reviewer", "pm"} {
		env.FailWith(ft.Opts{Agent: "zz-" + r}, 3, "skilleval", "try", "fix-bug", "--candidate", "none")
	}
}

func TestSkillevalSandbox_UnknownTaskAndBadCondition(t *testing.T) {
	f := newSbxFix(t)
	sbxExit(t, sbxTry(f, "../x", "none", ""), 2)
	sbxExit(t, sbxTry(f, "nope", "none", ""), 2)
	sbxExit(t, sbxTry(f, "fix-bug", "none", "with"), 2)
}

// ---- NamespaceTests (real user namespaces) ----

func sbxNsFix(t *testing.T) *sbxFix {
	t.Helper()
	sbxNeedUserns(t)
	f := newSbxFix(t)
	f.builder.skipPreflight = false
	return f
}

func TestSkillevalSandbox_TryBaselineEndToEnd(t *testing.T) {
	f := sbxNsFix(t)
	sbxMust(t, sbxTry(f, "fix-bug", "none", ""))
	var cand, cond, status, agent string
	sbxMust(t, f.db.QueryRow("SELECT candidate_id, condition, status, agent FROM skill_trials").Scan(&cand, &cond, &status, &agent))
	if cand != "C-0000" || cond != "without" || status != "running" {
		t.Errorf("row %s %s %s", cand, cond, status)
	}
	if n := f.count("SELECT count(*) FROM scout_candidates WHERE id='C-0000'"); n != 1 {
		t.Errorf("baseline candidate rows %d", n)
	}
	if len(f.spawned) != 1 || len(f.spawned[0].plugins) != 0 {
		t.Fatalf("spawned %+v", f.spawned)
	}
	if agent != f.spawned[0].name || !strings.HasPrefix(agent, "eval-") {
		t.Errorf("agent %q spawned %q", agent, f.spawned[0].name)
	}
}

func TestSkillevalSandbox_TryIsRefusedForAFinishedTrialUntilItIsVoidedThenRerunsIt(t *testing.T) {
	f := sbxNsFix(t)
	sbxMust(t, sbxTry(f, "fix-bug", "none", ""))
	var tid int64
	sbxMust(t, f.db.QueryRow("SELECT id FROM skill_trials").Scan(&tid))
	sbxExec(t, f.db, "UPDATE skill_trials SET status='timeout', passed=0, wall_s=900 WHERE id=?", tid)
	ee := sbxExit(t, sbxTry(f, "fix-bug", "none", ""), 2)
	if !strings.Contains(ee.Msg, "already exists (status timeout)") {
		t.Errorf("msg %q", ee.Msg)
	}
	sbxMust(t, f.env.VoidTarget(f.db, fmt.Sprint(tid), "infra", "hook bug"))
	sbxExec(t, f.db, "UPDATE agents SET status='done' WHERE name LIKE 'eval-%'") // the first evaluator has exited
	sbxMust(t, sbxTry(f, "fix-bug", "none", ""))
	var id int64
	var status string
	var void sql.NullString
	sbxMust(t, f.db.QueryRow("SELECT id, status, void_reason FROM skill_trials").Scan(&id, &status, &void))
	if id != tid || status != "running" || void.Valid {
		t.Errorf("row %d %s %v", id, status, void)
	}
}

func TestSkillevalSandbox_TryWithCandidateVendorsNothingNewAndPassesOnePluginDir(t *testing.T) {
	f := sbxNsFix(t)
	sbxMust(t, sbxTry(f, "fix-bug", "C-0001", ""))
	if len(f.spawned) != 1 || len(f.spawned[0].plugins) != 1 {
		t.Fatalf("spawned %+v", f.spawned)
	}
	var cond, dir string
	sbxMust(t, f.db.QueryRow("SELECT condition, sandbox FROM skill_trials").Scan(&cond, &dir))
	if cond != "with" {
		t.Errorf("condition %q", cond)
	}
	p := f.plan(f.request(f.spawned[0]))
	f.check(skilleval.Info{Dir: dir}, p, true)
	sbxExit(t, sbxTry(f, "fix-bug", "C-0001", ""), 2)
}

func TestSkillevalSandbox_PreflightFailsWhenABindSourceIsMissing(t *testing.T) {
	f := sbxNsFix(t)
	info := f.build("with")
	if err := f.builder.real.Preflight(info); err != nil { // the intact view passes
		t.Fatalf("intact preflight: %v", err)
	}
	sbxMust(t, os.RemoveAll(filepath.Join(info.Dir, "role")))
	err := f.builder.real.Preflight(info)
	if !errors.Is(err, sandbox.ErrSandboxRefused) || !strings.Contains(err.Error(), "preflight failed") {
		t.Fatalf("err %v", err)
	}
}

func TestNotPortedSandbox_ShimEnvArrivesInsideAndTheRestIsDropped(t *testing.T) {
	t.Skip("not ported: runs the Python claude shim (nsbox.write_shim: env drop, token read inside the pane) and its factory CLI snapshot; the Go shim writer is not wired (P4-L)")
}

// ---- NsboxFixTests ----

func TestNotPortedSandbox_OwnTrialIsNotASiblingWhateverTheSpelling(t *testing.T) {
	t.Skip("not ported: nsbox._sibling_trials is internal; the Go siblingTrials is unexported (package sandbox) and has no public counterpart")
}

func TestNotPortedSandbox_CwdInsideASiblingThroughTheSymlinkIsRefusedAndOwnIsAccepted(t *testing.T) {
	t.Skip("not ported: nsbox._refuse_hidden_cwd has no Go counterpart (the Go sandbox does not refuse a hidden cwd)")
}

func TestNotPortedSandbox_RunContainedBuildsItsCfgOnce(t *testing.T) {
	t.Skip("not ported: mock.patch of make_cfg and Popen inside Python nsbox.run_contained; Go RunContained takes a ready Cfg")
}

func TestNotPortedSandbox_RunContainedStillRefusesAHiddenCwd(t *testing.T) {
	t.Skip("not ported: Python nsbox.run_contained refuses a cwd inside a hidden dir; Go RunContained has no such refusal")
}

// ---- TrialHomeZshTests ----

var sbxMarker = regexp.MustCompile(`(?m)^MARKER\r?$`)

func sbxNeedZsh(t *testing.T) {
	t.Helper()
	for _, b := range []string{"zsh", "script", "sh"} {
		if _, err := exec.LookPath(b); err != nil {
			t.Skipf("zsh or script (pty) not available: %v", err)
		}
	}
}

func sbxRunZsh(t *testing.T, home string) string {
	t.Helper()
	keys := `(sleep 2; printf 'echo MARKER\n'; sleep 1; printf 'exit\n') | script -qc 'zsh -i' /dev/null`
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", keys)
	// skip_global_compinit: Debian/Ubuntu's global zshrc runs compinit, which prompts on insecure dirs (CI runner) and eats keys.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "LC_ALL=C.UTF-8", "TERM=dumb", "skip_global_compinit=1"}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("zsh run: %v", err)
	}
	return string(out)
}

func sbxLaunchOK(out string) bool {
	return sbxMarker.MatchString(out) && !strings.Contains(out, "command not found")
}

func TestSkillevalSandbox_TrialHomeRcIsCommentOnlyFile(t *testing.T) {
	sbxNeedZsh(t)
	f := newSbxFix(t)
	info := f.build("with")
	rc := filepath.Join(info.Dir, "home", sandbox.HomeRC[0])
	st, err := os.Lstat(rc)
	sbxMust(t, err)
	if !st.Mode().IsRegular() || st.Mode().Perm() != 0o644 {
		t.Errorf("rc mode %v", st.Mode())
	}
	body, err := os.ReadFile(rc)
	sbxMust(t, err)
	n := 0
	for _, l := range strings.Split(string(body), "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n++
		if !strings.HasPrefix(l, "#") {
			t.Errorf("non-comment line %q", l)
		}
	}
	if n == 0 {
		t.Errorf("no comment line")
	}
}

func TestSkillevalSandbox_LaunchLineSurvivesInBuiltHome(t *testing.T) {
	sbxNeedZsh(t)
	f := newSbxFix(t)
	info := f.build("with")
	out := sbxRunZsh(t, filepath.Join(info.Dir, "home"))
	if !sbxLaunchOK(out) {
		t.Errorf("launch line eaten: %q", out)
	}
	if strings.Contains(out, "zsh-newuser-install") {
		t.Errorf("zsh-newuser-install ran: %q", out)
	}
}

func TestSkillevalSandbox_EmptyHomeIsDetected(t *testing.T) {
	sbxNeedZsh(t)
	out := sbxRunZsh(t, t.TempDir())
	if !strings.Contains(out, "zsh-newuser-install") {
		t.Errorf("no zsh-newuser-install in %q", out)
	}
	if sbxLaunchOK(out) {
		t.Errorf("launch ok in an empty home: %q", out)
	}
}
