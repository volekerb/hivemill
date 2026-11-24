package skilleval2

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	ft "factory/go/internal/factorytest"
)

// startlogToken is the dummy OAuth token of tests/test_skilleval_startlog.py (split so the repo secret scan does not flag it).
const startlogToken = "sk-ant-" + "oat01-STARTLOGDUMMYTOKEN-9f3a1c"

// startlogCap is CAP of the Python module: the start.log size limit.
const startlogCap = 64 * 1024

// startlogWorld is SkillevalOutputTests.setUp: a factory home with skilleval_root set and candidate C-0001.
type startlogWorld struct {
	t    *testing.T
	e    *ft.Env
	root string
	n    int
}

func newStartlogWorld(t *testing.T) *startlogWorld {
	t.Helper()
	e := ft.New(t)
	e.SeedAgent(ft.AgentRow{Name: "owner", Role: "owner"})
	w := &startlogWorld{t: t, e: e, root: filepath.Join(e.Root, "evalroot")} // outside FACTORY_HOME: config refuses an overlap
	e.OKWith(ft.Opts{Agent: "owner"}, "config", "skilleval_root", w.root)
	tok := filepath.Join(e.UserHome, ".config", "factory", "eval-oauth-token")
	if err := os.MkdirAll(filepath.Dir(tok), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tok, []byte(startlogToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	slExec(t, e, "INSERT INTO scout_candidates(id,key,repo,head_sha,install_risk,status,sources,first_seen,last_seen) "+
		"VALUES('C-0001','gh:o/r','o/r',?,'prompt-only','new','[]','t','t')", fmt.Sprintf("%040d", 0)+"")
	return w
}

func slExec(t testing.TB, e *ft.Env, q string, args ...any) {
	t.Helper()
	d := e.RawDB()
	defer d.Close()
	if _, err := d.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// trial inserts a skill_trials row (tokens < 0 = NULL) with <root>/trials/trNNNN and, when lines != nil, its start.log
// ending in rc=<rc>. It returns the trial dir.
func (w *startlogWorld) trial(status, task string, tokens int, lines []string, rc int) string {
	w.t.Helper()
	w.n++
	d := filepath.Join(w.root, "trials", fmt.Sprintf("tr%04d", w.n))
	if err := os.MkdirAll(d, 0o755); err != nil {
		w.t.Fatal(err)
	}
	if lines != nil {
		if err := os.Mkdir(filepath.Join(d, "log"), 0o755); err != nil {
			w.t.Fatal(err)
		}
		body := ""
		for _, l := range lines {
			body += l + "\n"
		}
		body += fmt.Sprintf("rc=%d\n", rc)
		if err := os.WriteFile(filepath.Join(d, "log", "start.log"), []byte(body), 0o644); err != nil {
			w.t.Fatal(err)
		}
	}
	var tok any
	if tokens >= 0 {
		tok = tokens
	}
	slExec(w.t, w.e, "INSERT INTO skill_trials(id,candidate_id,vendor_sha,task,condition,status,agent,sandbox,queued,"+
		"tokens_in,tokens_out) VALUES(?,?,?,?,?,?,?,?,'t',?,?)",
		w.n, "C-0001", fmt.Sprintf("%040d", 0), task, "with", status, fmt.Sprintf("eval-tr%04d", w.n), d, tok, tok)
	return d
}

// outputs is the stdout of `skilleval results|list --candidate C-0001` as owner.
func (w *startlogWorld) outputs() []string {
	w.t.Helper()
	var outs []string
	for _, verb := range []string{"results", "list"} {
		outs = append(outs, w.e.OKWith(ft.Opts{Agent: "owner"}, "skilleval", verb, "--candidate", "C-0001").Stdout)
	}
	return outs
}

func nullS(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }
