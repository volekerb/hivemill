package s2cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
	"factory/go/internal/herdr"
	"factory/go/internal/identity"
)

// Helpers for tests/test_worker_gate.py (shared constants and herdr/refuse_worker wiring).

var wgWorkerRoles = []string{"pm", "coder", "qa", "reviewer", "judge", "cleaner", "staff-engineer"}

var wgMutating = [][]string{
	{"agent", "start", "x"},
	{"pane", "close", "p1"},
	{"pane", "split", "p1"},
	{"pane", "run", "p1", "ls"},
	{"tab", "create"},
	{"tab", "rename", "t1", "x"},
	{"workspace", "create"},
	{"server", "stop"},
	{"bogus", "verb"},
}

var wgAllowed = [][]string{
	{"agent", "list"},
	{"agent", "read", "x"},
	{"pane", "list"},
	{"pane", "process-info", "--pane", "p1"},
	{"tab", "list", "--workspace", "w1"},
	{"workspace", "list"},
	{"pane", "report-metadata", "p1"},
	{"workspace", "report-metadata", "w1"},
	{"agent", "prompt", "x", "hi"},
}

var herdrWorkerAllow = map[[2]string]bool{
	{"agent", "list"}:                true,
	{"agent", "read"}:                true,
	{"pane", "list"}:                 true,
	{"pane", "process-info"}:         true,
	{"tab", "list"}:                  true,
	{"workspace", "list"}:            true,
	{"pane", "report-metadata"}:      true,
	{"workspace", "report-metadata"}: true,
	{"agent", "prompt"}:              true,
}

var workerRolesSet = map[string]bool{
	"pm": true, "coder": true, "qa": true, "reviewer": true, "judge": true, "cleaner": true,
	"staff-engineer": true,
}

var wgSettingsRoleRE = regexp.MustCompile(`/settings/([\w-]+)\.json\n?\z`)

// validPRBody matches tests/harness.py VALID_PR_BODY (pr_body()).
const validPRBody = "## Summary\nWhat changed. \n\n## Why\nT-0001 motivation.\n\n## Changes\n- one change\n\n" +
	"## Test evidence\n`python3 -m unittest` -> OK\n\n## Risk / rollback\nRevert this PR.\n\n" +
	"<details><summary>Acceptance walk</summary>\n\n## Acceptance walk\n1. met - cmd -> out\n</details>\n"

func wgGatesSummary(text string) string {
	if text == "" {
		text = "ok"
	}
	return text + "\n\n## Gates\n- unittest exit=0 cmd=python3 -m unittest discover -s tests -v\n"
}

type wgEnv struct {
	*ft.Env
	workerOnce bool
	workerRole string
}

// newWorkerGateEnv is WorkerGateBase.setUp.
func newWorkerGateEnv(t *testing.T) *wgEnv {
	t.Helper()
	e := ft.New(t)
	e.HerdrStateful()
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedTeam("g-001-a", "G-001", "zz-test-lead")
	e.SeedTask(ft.Task{ID: "T-0001", Role: "coder", Status: "in_progress", Team: "g-001-a"})
	e.SeedAgent(ft.AgentRow{Name: "zz-test-lead", Role: "lead", Goal: "G-001", Team: "g-001-a"})
	e.SeedAgent(ft.AgentRow{Name: "zz-test-director", Role: "director", Goal: "G-001", Team: "g-001-a"})
	e.SeedAgent(ft.AgentRow{Name: "zz-test-boss", Role: "boss", Goal: "G-001", Team: "g-001-a"})
	for _, r := range wgWorkerRoles {
		e.SeedAgent(ft.AgentRow{Name: "zz-test-" + r, Role: r, Goal: "G-001", Team: "g-001-a", Task: "T-0001"})
	}
	sqlExec(t, e, "UPDATE config SET value='herdr' WHERE key='terminal_backend'")
	return &wgEnv{Env: e}
}

func (w *wgEnv) workerOpts(role string) ft.Opts {
	return ft.Opts{Agent: "zz-test-" + role, Env: map[string]string{"FACTORY_ROLE": role}}
}

func (w *wgEnv) herdrArgvs() [][]string {
	var out [][]string
	for _, c := range w.Calls("herdr") {
		out = append(out, c.Argv)
	}
	return out
}

func (w *wgEnv) deniedEvents() [][]string {
	return w.SQL("SELECT * FROM events WHERE action='herdr-denied'")
}

func ancestorPIDs(me, n, start int) []int {
	var out []int
	for x := start; len(out) < n; x++ {
		if x != me {
			out = append(out, x)
		}
	}
	return out
}

func (w *wgEnv) snapshot() [][][]string {
	tables := []string{"agents", "events", "messages", "tasks"}
	out := make([][][]string, len(tables))
	for i, tb := range tables {
		out[i] = w.SQL("SELECT * FROM " + tb)
	}
	return out
}

func wgApplyProcessEnv(t *testing.T, e *ft.Env, agent string, extra map[string]string) {
	t.Helper()
	for _, k := range []string{"FACTORY_ROLE", "HERDR_PANE_ID", "FACTORY_AGENT"} {
		os.Unsetenv(k)
	}
	merged := e.Environ(agent, extra)
	for _, kv := range merged {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
}

type wgGateErr struct{ msg string }

func (e *wgGateErr) Error() string { return e.msg }

type wgHerdrStore struct {
	e     *ft.Env
	actor string
}

func (s *wgHerdrStore) Config(key string) (string, bool) {
	rows := s.e.SQL("SELECT value FROM config WHERE key=?", key)
	if len(rows) != 1 {
		return "", false
	}
	return rows[0][0], true
}

func (s *wgHerdrStore) AgentRow(name string) *herdr.Row {
	rows := s.e.SQL("SELECT name, spawn_args, pane_id FROM agents WHERE name=?", name)
	if len(rows) != 1 {
		return nil
	}
	return &herdr.Row{Name: rows[0][0], SpawnArgs: rows[0][1], PaneID: rows[0][2]}
}

func (s *wgHerdrStore) Event(entity, id, action, detail string) {
	sqlExec(s.e.T, s.e, "INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES('t',?,?,?,?,?)",
		s.actor, entity, id, action, detail)
}

func wgProcStatPPid(root string, pid int) (int, bool) {
	b, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, false
	}
	s := string(b)
	i := strings.LastIndex(s, ")")
	if i < 0 {
		return 0, false
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 2 {
		return 0, false
	}
	pp, err := strconv.Atoi(f[1])
	if err != nil {
		return 0, false
	}
	return pp, true
}

func wgAncestryWorker(root string, startPid, startPPid int) string {
	pid := startPid
	if _, ok := wgProcStatPPid(root, pid); !ok {
		pid = startPPid
	}
	for i := 0; i < 64; i++ {
		var argv []string
		if raw, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "cmdline")); err == nil {
			for _, x := range strings.Split(string(raw), "\x00") {
				if x != "" {
					argv = append(argv, x)
				}
			}
		}
		head := argv
		if len(head) > 3 {
			head = head[:3]
		}
		for _, x := range head {
			if x[strings.LastIndex(x, "/")+1:] != "claude" {
				continue
			}
			var vals []string
			for j := 0; j < len(argv)-1; j++ {
				if argv[j] == "--settings" {
					vals = append(vals, argv[j+1])
				}
			}
			for _, x := range argv {
				if strings.HasPrefix(x, "--settings=") {
					vals = append(vals, strings.SplitN(x, "=", 2)[1])
				}
			}
			for _, v := range vals {
				if m := wgSettingsRoleRE.FindStringSubmatch(v); m != nil && workerRolesSet[m[1]] {
					return m[1]
				}
			}
			return ""
		}
		pp, ok := wgProcStatPPid(root, pid)
		if !ok || pp <= 1 {
			return ""
		}
		pid = pp
	}
	return ""
}

func (w *wgEnv) detectWorker() string {
	if w.workerOnce {
		return w.workerRole
	}
	root := os.Getenv("FACTORY_PROC_DIR")
	if root == "" {
		root = "/proc"
	}
	if r := wgAncestryWorker(root, os.Getpid(), os.Getppid()); r != "" {
		w.workerOnce, w.workerRole = true, r
		return r
	}
	if r := os.Getenv("FACTORY_ROLE"); workerRolesSet[r] {
		w.workerOnce, w.workerRole = true, r
		return r
	}
	agent := os.Getenv("FACTORY_AGENT")
	if agent != "" {
		rows := w.SQL("SELECT role FROM agents WHERE name=?", agent)
		if len(rows) == 1 {
			if workerRolesSet[rows[0][0]] {
				w.workerOnce, w.workerRole = true, rows[0][0]
				return rows[0][0]
			}
		}
	}
	if pane := os.Getenv("HERDR_PANE_ID"); pane != "" {
		rows := w.SQL("SELECT name,role FROM agents WHERE pane_id=? AND status='live'", pane)
		for _, r := range rows {
			role := r[1]
			if !workerRolesSet[role] {
				continue
			}
			fa := os.Getenv("FACTORY_AGENT")
			var fr string
			if fa != "" {
				ar := w.SQL("SELECT role FROM agents WHERE name=?", fa)
				if len(ar) == 1 {
					fr = ar[0][0]
				} else {
					fr = "unknown"
				}
			}
			if fa != "" && !workerRolesSet[fr] {
				detail := fmt.Sprintf("pane %s (%s %s) claims FACTORY_AGENT=%s", pane, role, r[0], fa)
				sqlExec(w.T, w.Env, "INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES('t',?,?,?,?,?)",
					"pane:"+pane, "agent", r[0], "identity-spoof", detail)
			}
			w.workerOnce, w.workerRole = true, role
			return role
		}
	}
	w.workerOnce, w.workerRole = true, ""
	return ""
}

func (w *wgEnv) workerStubExempt() bool {
	return herdr.IsTestStub() && !w.homeLive()
}

func (w *wgEnv) homeLive() bool {
	env := identity.Env{Getenv: os.Getenv, LookupEnv: os.LookupEnv, Uid: os.Getuid()}
	// Python home_is_live() uses the module HOME (FACTORY_HOME at import; tests may set f.HOME).
	check := os.Getenv("FACTORY_HOME")
	if check == "" {
		check = filepath.Join(env.UserHome(), "factory")
	}
	_, homes, err := env.DeriveLiveHomes()
	if err != nil {
		return false
	}
	real, err := filepath.EvalSymlinks(check)
	if err != nil {
		real = filepath.Clean(check)
	}
	return homes[real]
}

func (w *wgEnv) herdrGate(args []string) error {
	role := w.detectWorker()
	if role == "" || herdrWorkerAllowed(args) || w.workerStubExempt() {
		return nil
	}
	verb := strings.Join(args[:min(3, len(args))], " ")
	actor := os.Getenv("FACTORY_AGENT")
	if actor == "" {
		actor = "unknown"
	}
	detail, _ := json.Marshal(map[string]string{"actor": actor, "role": role, "verb": verb})
	store := &wgHerdrStore{e: w.Env, actor: actor}
	store.Event("herdr", "-", "herdr-denied", string(detail))
	return &wgGateErr{fmt.Sprintf("workers may not run `herdr %s` (role %s)", verb, role)}
}

func herdrWorkerAllowed(args []string) bool {
	if len(args) < 2 {
		return false
	}
	return herdrWorkerAllow[[2]string{args[0], args[1]}]
}

func (w *wgEnv) newHerdrClient(agent string) *herdr.Client {
	store := &wgHerdrStore{e: w.Env, actor: agent}
	cl := &herdr.Client{Store: store}
	cl.Gate = w.herdrGate
	cl.IsWorker = func() bool { return w.detectWorker() != "" }
	cl.HomeLive = func() bool { return w.homeLive() }
	return cl
}

// herdrCall runs one herdr() in-process as agent; returns exit code 3 on gate refusal (check controls stub failures).
// refuseWorker is refuse_worker(what): exit 3 when a worker is blocked and stub-exempt does not apply.
func (w *wgEnv) refuseWorker(t *testing.T, agent string, extra map[string]string) int {
	t.Helper()
	w.workerOnce = false
	wgApplyProcessEnv(t, w.Env, agent, extra)
	if role := w.detectWorker(); role != "" && !w.workerStubExempt() {
		return 3
	}
	return 0
}

func (w *wgEnv) herdrCall(t *testing.T, argv []string, agent string, extra map[string]string, check bool) (int, any) {
	t.Helper()
	w.workerOnce = false
	wgApplyProcessEnv(t, w.Env, agent, extra)
	cl := w.newHerdrClient(agent)
	res, err := cl.Herdr(check, argv...)
	if err != nil {
		if _, ok := err.(*wgGateErr); ok {
			return 3, nil
		}
		if !check {
			return 0, nil
		}
		t.Fatalf("herdr %v: %v", argv, err)
	}
	return 0, res
}

func (w *wgEnv) seedProject(pid string) {
	sqlExec(w.T, w.Env, "INSERT INTO projects(id,name,auto_merge,created) VALUES(?,?,1,'t')", pid, pid)
}

func (w *wgEnv) addGhRules(rules ...ft.StubRule) {
	all := map[string][]ft.StubRule{}
	if b, err := os.ReadFile(w.ScriptFile); err == nil {
		if err := json.Unmarshal(b, &all); err != nil {
			w.T.Fatalf("addGhRules: decode %s: %v", w.ScriptFile, err)
		}
	}
	merged := append(append([]ft.StubRule{}, rules...), all["gh"]...)
	w.Script("gh", merged...)
}

func (w *wgEnv) seedPR(checks []map[string]string, body string) {
	st := map[string]any{"prs": map[string]any{}}
	if b, err := os.ReadFile(filepath.Join(w.Root, "gh.json")); err == nil {
		if err := json.Unmarshal(b, &st); err != nil {
			w.T.Fatalf("seedPR: decode gh.json: %v", err)
		}
	}
	if w.RepoURL != "" {
		st["repo_url"] = w.RepoURL
	}
	prs, _ := st["prs"].(map[string]any)
	if prs == nil {
		prs = map[string]any{}
		st["prs"] = prs
	}
	sha := w.HeadSHA
	if sha == "" {
		sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	}
	entry := map[string]any{
		"number": 1, "state": "OPEN", "headRefOid": sha, "headRefName": "factory/t-0001",
		"baseRefName": "main", "mergeable": "MERGEABLE", "isDraft": false, "url": ft.PRURL,
		"statusCheckRollup": checks,
	}
	if body != "" {
		entry["body"] = body
	}
	prs[ft.PRURL] = entry
	b, err := json.Marshal(st)
	if err != nil {
		w.T.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.Root, "gh.json"), b, 0o644); err != nil {
		w.T.Fatal(err)
	}
}

func wgStubsDir(t *testing.T) string {
	t.Helper()
	repo, err := ft.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(repo, "tests", "stubs")
}

func wgLiveHome(t *testing.T) string {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(u.HomeDir, "factory")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
