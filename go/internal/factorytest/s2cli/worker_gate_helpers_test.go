package s2cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	ft "factory/go/internal/factorytest"
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
