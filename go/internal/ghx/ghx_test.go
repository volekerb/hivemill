package ghx

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"factory/go/internal/gitx"

	_ "modernc.org/sqlite"
)

func stubGh(t *testing.T, body string) (log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(t.TempDir(), "log")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\" >> '" + log + "'; done\nprintf -- '--\\n' >> '" + log + "'\n" + body
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return log
}

func newDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec("CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	return db
}

// TestArgvGolden: testdata/gh_argv.golden is the argv a stub gh logged when Python gh_json ran the same two calls.
func TestArgvGolden(t *testing.T) {
	log := stubGh(t, "echo '{}'\n")
	if _, err := JSON(0, "pr", "view", "https://github.com/o/r/pull/7", "--json", "state,mergeCommit,baseRefName,headRefOid,number"); err != nil {
		t.Fatal(err)
	}
	if _, err := JSON(0, "run", "list", "-R", "o/r", "--workflow", "tests.yml", "--branch", "main", "--event", "push", "--limit", "1",
		"--json", "databaseId,status,conclusion,headSha,url"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(log)
	want, err := os.ReadFile("testdata/gh_argv.golden")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("argv differs from Python gh_json\n got: %q\nwant: %q", got, want)
	}
}

func TestJSONErrors(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"exit", "echo 'HTTP 404: Not Found' >&2; echo second >&2; exit 1\n", "gh pr view https://x: HTTP 404: Not Found"},
		{"empty stderr", "exit 2\n", "gh pr view https://x: "},
		{"bad json", "echo nope\n", "bad JSON"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stubGh(t, c.body)
			_, err := JSON(0, "pr", "view", "https://x", "--json", "a")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v want %q", err, c.want)
			}
		})
	}
}

func TestJSONTimeout(t *testing.T) {
	stubGh(t, "exec sleep 30\n")
	_, err := JSON(200*time.Millisecond, "pr", "view")
	if _, ok := err.(*gitx.TimeoutError); !ok {
		t.Fatalf("want TimeoutError, got %v", err)
	}
}

func TestRedMainWorkflowMissing(t *testing.T) {
	cases := map[string]bool{
		"HTTP 404: workflow tests.yml not found on the default branch": true,
		"Workflow Not Found":  true,
		"HTTP 404: Not Found": false,
		"workflow failed":     false,
		"":                    false,
	}
	for in, want := range cases {
		if got := RedMainWorkflowMissing(in); got != want {
			t.Errorf("%q: got %v want %v", in, got, want)
		}
	}
}

func TestGitEnv(t *testing.T) {
	env := GitEnv(gitx.Options{})
	m := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	if m["GIT_CONFIG_COUNT"] != "7" || m["GIT_CONFIG_KEY_0"] != "core.hooksPath" || m["GIT_CONFIG_VALUE_0"] != "/dev/null" ||
		m["GIT_CONFIG_KEY_6"] != "tag.gpgSign" || m["GIT_TERMINAL_PROMPT"] != "0" || m["GIT_CONFIG_NOSYSTEM"] != "1" {
		t.Fatalf("env = %v", m)
	}
}

func pyLoads(t *testing.T, row string) map[string]any {
	t.Helper()
	cmd := exec.Command("python3", "-I", "-c", "import json,sys; d=json.loads(sys.argv[1]); print(json.dumps([d['at'], d['ready'], d['why']]))", row)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("python3 json.loads(%q): %v", row, err)
	}
	return map[string]any{"out": strings.TrimSpace(string(out))}
}

func rawRow(t *testing.T, db *sql.DB, key string) string {
	t.Helper()
	var v string
	if err := db.QueryRow("SELECT value FROM config WHERE key=?", key).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// TestCacheRoundTrip: a Go-written judge_ci row parses in Python json.loads with keys at, ready, why; a
// Python-written fixture row (testdata, json.dumps) is read by Go.
func TestCacheRoundTrip(t *testing.T) {
	db := newDB(t)
	url := "https://github.com/o/r/pull/7"
	if err := StoreJudgeCI(db, url, true, "CI green — ok", 1700000000.5); err != nil {
		t.Fatal(err)
	}
	row := rawRow(t, db, "judge_ci:"+url)
	if want := "{\"at\": 1700000000.5, \"ready\": true, \"why\": \"CI green \\u2014 ok\"}"; row != want {
		t.Fatalf("row = %s want %s", row, want)
	}
	if out := pyLoads(t, row)["out"]; out != "[1700000000.5, true, \"CI green \\u2014 ok\"]" {
		t.Fatalf("python read %s", out)
	}
	if c, ok := LoadJudgeCI(db, url, 1700000100); !ok || !c.Ready || c.Why != "CI green — ok" {
		t.Fatalf("Go read of own row: %+v %v", c, ok)
	}
	if _, ok := LoadJudgeCI(db, url, 1700000000.5+120); ok {
		t.Fatal("row at the TTL must be a miss")
	}
	if _, ok := LoadJudgeCI(db, url, 1699999999); ok {
		t.Fatal("row from the future must be a miss")
	}

	fx, err := os.ReadFile("testdata/judge_ci_python.json")
	if err != nil {
		t.Fatal(err)
	}
	db.Exec("INSERT INTO config(key,value) VALUES('judge_ci:fx',?)", string(fx))
	if c, ok := LoadJudgeCI(db, "fx", 1700000010); !ok || c.Ready || c.Why != "CI not green: test=FAILURE" || c.At != 1700000000.5 {
		t.Fatalf("python fixture: %+v %v", c, ok)
	}

	pv, _ := os.ReadFile("testdata/prview_python.json")
	db.Exec("INSERT INTO config(key,value) VALUES('prview:fx',?)", string(pv))
	head := "abc123"
	if pr, ok := LoadPRView(db, "fx", 1700000010, &head, PRViewTTLDefault); !ok || pr["state"] != "OPEN" {
		t.Fatalf("python prview fixture: %v %v", pr, ok)
	}
	other := "def"
	if _, ok := LoadPRView(db, "fx", 1700000010, &other, PRViewTTLDefault); ok {
		t.Fatal("head mismatch must miss")
	}
	if _, ok := LoadPRView(db, "fx", 1700000010, nil, PRViewTTLDefault); ok {
		t.Fatal("unknown head must miss")
	}
	if _, ok := LoadPRView(db, "fx", 1700000010, &head, 0); ok {
		t.Fatal("ttl 0 disables the cache")
	}
	if err := StorePRView(db, url, map[string]any{"headRefOid": "h1", "number": 7}, 1700000000.25, PRViewTTLDefault); err != nil {
		t.Fatal(err)
	}
	prow := rawRow(t, db, "prview:"+url)
	out, err := exec.Command("python3", "-I", "-c", "import json,sys; d=json.loads(sys.argv[1]); print(d['at'], d['head'], d['pr']['number'])", prow).Output()
	if err != nil || strings.TrimSpace(string(out)) != "1700000000.25 h1 7" {
		t.Fatalf("python read of Go prview row: %q %v", out, err)
	}
	h1 := "h1"
	if _, ok := LoadPRView(db, url, 1700000001, &h1, PRViewTTLDefault); !ok {
		t.Fatal("Go read of own prview row failed")
	}
	if err := StorePRView(db, "none", nil, 1, PRViewTTLDefault); err != nil {
		t.Fatal(err)
	}
	if _, ok := get(db, "prview:none"); ok {
		t.Fatal("nil pr must not be stored")
	}
}

// TestUnreadableRowIsAMiss: cases of tests/test_judge_ci_prune.py (unparsable value, non-finite or huge at) read as a
// miss, never an error (judge_pr_ready: "an unreadable cache entry is just a miss").
func TestUnreadableRowIsAMiss(t *testing.T) {
	db := newDB(t)
	for i, v := range []string{"not json", `{"at": NaN, "ready": true, "why": "x"}`, `{"at": Infinity, "ready": true, "why": "x"}`,
		`{"at": 1e999, "ready": true, "why": "x"}`, `{"ready": true, "why": "x"}`, `{"at": 1, "ready": "yes", "why": "x"}`, `[]`} {
		k := string(rune('a' + i))
		db.Exec("INSERT INTO config(key,value) VALUES(?,?)", "judge_ci:"+k, v)
		if _, ok := LoadJudgeCI(db, k, 2); ok {
			t.Errorf("%q read as a hit", v)
		}
	}
	if _, ok := LoadJudgeCI(db, "absent", 2); ok {
		t.Error("absent row read as a hit")
	}
}
