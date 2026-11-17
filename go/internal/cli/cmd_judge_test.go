package cli

import (
	"bytes"
	"database/sql"
	"factory/go/internal/factorydb"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"factory/go/internal/identity"
)

// The judge gate through the real entry point (Run): a temp FACTORY_HOME with a DB, real git repos (a bare "origin",
// the PR head, a clone as the task worktree) and a gh stub on PATH that answers the calls the gate makes from files
// and logs every call. Texts and exit codes are the ones bin/factory_cli.py prints.

type judgeRig struct {
	t                 *testing.T
	home, stub, wt    string
	origin            string
	main, head, other string // main sha, PR head sha, an unrelated older head
	db                *sql.DB
}

func jgit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t",
		"-c", "commit.gpgsign=false"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// judgeStubScript is the gh stub: it logs its argv to the STUB dir and answers from files in it.
const judgeStubScript = `#!/bin/sh
echo "$@" >> "$STUB/log"
case "$1 $2" in
"pr view")
  if [ -f "$STUB/hang" ]; then sleep 20; fi
  case "$*" in
    *"--json files"*) echo '{"files":[{"path":"go/x.go"}]}' ;;
    *) cat "$STUB/pr.json" ;;
  esac ;;
"repo view") printf '{"url":"%s"}' "$(cat "$STUB/origin")" ;;
"api repos/o/r/commits/main") cat "$STUB/main" ;;
"pr merge") if [ -f "$STUB/merge_once" ]; then cat "$STUB/merge_once" >&2; rm "$STUB/merge_once"; exit 1; fi
  if [ -f "$STUB/merge_fail" ]; then cat "$STUB/merge_fail" >&2; exit 1; fi; exit 0 ;;
*) echo "stub gh: unexpected $*" >&2; exit 9 ;;
esac
`

func newJudgeRig(t *testing.T) *judgeRig {
	t.Helper()
	root := t.TempDir()
	r := &judgeRig{t: t, home: filepath.Join(root, "fhome"), stub: filepath.Join(root, "stub"), wt: filepath.Join(root, "wt")}
	os.MkdirAll(r.stub, 0o755)
	bin := filepath.Join(root, "bin")
	os.MkdirAll(bin, 0o755)
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(judgeStubScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("STUB", r.stub)
	r.origin = filepath.Join(root, "origin.git")
	seed := filepath.Join(root, "seed")
	jgit(t, root, "init", "-q", "--bare", "-b", "main", r.origin)
	jgit(t, root, "init", "-q", "-b", "main", seed)
	os.WriteFile(filepath.Join(seed, "a"), []byte("1\n"), 0o644)
	jgit(t, seed, "add", "a")
	jgit(t, seed, "commit", "-q", "-m", "base")
	r.main = jgit(t, seed, "rev-parse", "HEAD")
	jgit(t, seed, "checkout", "-q", "-b", "pr")
	os.WriteFile(filepath.Join(seed, "b"), []byte("2\n"), 0o644)
	jgit(t, seed, "add", "b")
	jgit(t, seed, "commit", "-q", "-m", "pr 1")
	r.other = jgit(t, seed, "rev-parse", "HEAD")
	os.WriteFile(filepath.Join(seed, "c"), []byte("3\n"), 0o644)
	jgit(t, seed, "add", "c")
	jgit(t, seed, "commit", "-q", "-m", "pr 2")
	r.head = jgit(t, seed, "rev-parse", "HEAD")
	jgit(t, seed, "push", "-q", r.origin, "main:refs/heads/main", "pr:refs/heads/pr", "pr:refs/pull/7/head")
	jgit(t, root, "clone", "-q", r.origin, r.wt)
	os.WriteFile(filepath.Join(r.stub, "origin"), []byte(r.origin), 0o644)
	os.WriteFile(filepath.Join(r.stub, "main"), []byte(r.main+"\n"), 0o644)
	r.setPR("SUCCESS")
	if _, err := buildJudgeDB(r.home); err != nil {
		t.Fatal(err)
	}
	d, err := openRaw(filepath.Join(r.home, "db", "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	r.db = d
	r.sql("UPDATE tasks SET worktree=? WHERE id='T-0001'", r.wt)
	return r
}

// buildJudgeDB is buildGoldenDB with the judge seed (testdata/judge/seed.sql) on top of the shared base.sql.
func buildJudgeDB(home string) (string, error) {
	base, err := os.ReadFile(baseSQLPath())
	if err != nil {
		return "", err
	}
	seed, err := os.ReadFile(filepath.Join("testdata", "judge", "seed.sql"))
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(home, "db"), 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(home, "db", "factory.db")
	d, err := openRaw(path)
	if err != nil {
		return "", err
	}
	defer d.Close()
	for _, q := range []string{"PRAGMA journal_mode=WAL", string(base), string(seed), fmt.Sprintf("PRAGMA user_version = %d", factorydb.SchemaVersion)} {
		if _, err := d.Exec(q); err != nil {
			return "", fmt.Errorf("building %s: %w", path, err)
		}
	}
	return path, nil
}

func (r *judgeRig) sql(q string, args ...any) {
	r.t.Helper()
	if _, err := r.db.Exec(q, args...); err != nil {
		r.t.Fatal(err)
	}
}

func (r *judgeRig) scalar(q string, args ...any) string {
	r.t.Helper()
	var v sql.NullString
	if err := r.db.QueryRow(q, args...).Scan(&v); err != nil && err != sql.ErrNoRows {
		r.t.Fatal(err)
	}
	return v.String
}

// setPR writes the canned `gh pr view` answer: one check of the PR head with the given conclusion ("" = no check).
func (r *judgeRig) setPR(conclusion string) {
	roll := ""
	if conclusion != "" {
		roll = fmt.Sprintf(`{"__typename":"CheckRun","name":"tests","workflowName":"CI","status":"COMPLETED","conclusion":%q,"startedAt":"2026-11-12T00:00:00Z"}`, conclusion)
	}
	js := fmt.Sprintf(`{"number":7,"state":"OPEN","headRefOid":%q,"headRefName":"factory/t-0001","baseRefName":"main","mergeable":"MERGEABLE","isDraft":false,"statusCheckRollup":[%s],"url":"https://github.com/o/r/pull/7"}`, r.head, roll)
	if err := os.WriteFile(filepath.Join(r.stub, "pr.json"), []byte(js), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *judgeRig) verdict(role, v, head string) {
	r.sql("INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) VALUES('T-0001','https://github.com/o/r/pull/7',?,?,?,'ok','x','2026-11-12T00:00:00+00:00')", head, role, v)
}

func (r *judgeRig) both() { r.verdict("reviewer", "pass", r.head); r.verdict("qa", "pass", r.head) }

func (r *judgeRig) ghLog() string {
	b, _ := os.ReadFile(filepath.Join(r.stub, "log"))
	return string(b)
}

// run is `factory judge args...` as the judge agent; delegated reports whether the command was handed to Python.
func (r *judgeRig) run(stdin string, args ...string) (code int, out, errText string, delegated bool) {
	env := map[string]string{"FACTORY_HOME": r.home, "HOME": r.home, "FACTORY_AGENT": "claude-judge",
		"FACTORY_PROC_DIR": r.t.TempDir(), "FACTORY_JUDGE_POLL_S": "0"}
	var o, e bytes.Buffer
	execs := 0
	code = Run(Deps{Args: append([]string{"judge"}, args...), Stdout: &o, Stderr: &e, Stdin: strings.NewReader(stdin),
		Getpid: func() int { return 1 }, Getppid: func() int { return 1 },
		Getwd:    func() (string, error) { return r.wt, nil },
		LookPath: func(f string) (string, error) { return "/usr/bin/" + f, nil },
		Exec:     func(string, []string, []string) error { execs++; return nil },
		Env: identity.Env{Getenv: func(k string) string { return env[k] }, LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok },
			PwDir: func(int) (string, error) { return r.home, nil }, Uid: os.Getuid(),
			Now: func() time.Time { return time.Date(2026, 11, 12, 1, 2, 3, 0, time.UTC) }}})
	return code, o.String(), e.String(), execs > 0
}

func short8(s string) string { return s[:8] }

// merged asserts a completed merge of the rig's head: stdout, task, verdict, event and the exact `gh pr merge` argv.
func (r *judgeRig) merged(out string) {
	r.t.Helper()
	if want := "T-0001 MERGED @ " + short8(r.head) + "\n"; !strings.HasSuffix(out, want) {
		r.t.Fatalf("stdout = %q, want it to end with %q", out, want)
	}
	if got := r.scalar("SELECT status FROM tasks WHERE id='T-0001'"); got != "done" {
		r.t.Fatalf("task status = %q, want done", got)
	}
	if got := r.scalar("SELECT head_sha||' '||verdict FROM verdicts WHERE role='judge'"); got != r.head+" merge" {
		r.t.Fatalf("judge verdict = %q", got)
	}
	if got := r.scalar("SELECT detail FROM events WHERE action='merged'"); got != "@"+short8(r.head) {
		r.t.Fatalf("merged event = %q", got)
	}
	want := "pr merge https://github.com/o/r/pull/7 --squash --delete-branch --match-head-commit " + r.head
	if !strings.Contains(r.ghLog(), want+"\n") {
		r.t.Fatalf("gh log lacks %q:\n%s", want, r.ghLog())
	}
}

// refused asserts a refused merge: exit 4, Python's text, one judge-blocked event, no gh pr merge, task untouched.
func (r *judgeRig) refused(code int, out, errText, wantErr string) {
	r.t.Helper()
	if code != 4 || errText != "factory: "+wantErr+"\n" {
		r.t.Fatalf("exit %d stderr %q stdout %q, want exit 4 and %q", code, errText, out, "factory: "+wantErr)
	}
	if strings.Contains(r.ghLog(), "pr merge") {
		r.t.Fatalf("gh pr merge ran on a refused merge:\n%s", r.ghLog())
	}
	if got := r.scalar("SELECT status FROM tasks WHERE id='T-0001'"); got != "in_review" {
		r.t.Fatalf("task status = %q after a refusal, want in_review", got)
	}
	if got := r.scalar("SELECT count(*) FROM events WHERE action='judge-blocked'"); got != "1" {
		r.t.Fatalf("judge-blocked events = %s, want 1", got)
	}
	if got := r.scalar("SELECT count(*) FROM rejection_incidents WHERE kind='gate'"); got != "1" {
		r.t.Fatalf("gate incidents = %s, want 1", got)
	}
}

// TestJudgeGateEntry is the gate table of T-1463 item 4: blocked cases (a)-(f) with the text and exit code of Python's
// `factory judge`, allowed cases (g) and (h).
func TestJudgeGateEntry(t *testing.T) {
	noDelegate := func(t *testing.T, del bool) {
		t.Helper()
		if del {
			t.Fatal("the command was handed to python")
		}
	}
	t.Run("a: no QA verdict", func(t *testing.T) {
		r := newJudgeRig(t)
		r.verdict("reviewer", "pass", r.head)
		code, out, e, del := r.run("", "T-0001", "--merge", "--reason", "ship")
		noDelegate(t, del)
		r.refused(code, out, e, "merge refused: no qa PASS")
	})
	t.Run("b: reviewer PASS on an older head", func(t *testing.T) {
		r := newJudgeRig(t)
		r.verdict("reviewer", "pass", r.other)
		r.verdict("qa", "pass", r.head)
		code, out, e, del := r.run("", "T-0001", "--merge", "--reason", "ship")
		noDelegate(t, del)
		r.refused(code, out, e, fmt.Sprintf("merge refused: reviewer PASS is for %s, head is now %s (not a pure main merge: %s is not a two-parent merge)",
			short8(r.other), short8(r.head), short8(r.head)))
	})
	t.Run("c: CI green only on an older head", func(t *testing.T) {
		r := newJudgeRig(t)
		r.both()
		r.setPR("FAILURE") // the check of the new head failed; the older head's green run no longer counts
		code, out, e, del := r.run("", "T-0001", "--merge", "--reason", "ship")
		noDelegate(t, del)
		r.refused(code, out, e, "merge refused: checks not green: tests=FAILURE")
	})
	t.Run("c2: required check not started on the new head", func(t *testing.T) {
		r := newJudgeRig(t)
		r.both()
		r.setPR("")
		r.sql("UPDATE projects SET required_checks='tests'")
		code, out, e, del := r.run("", "T-0001", "--merge", "--reason", "ship")
		noDelegate(t, del)
		r.refused(code, out, e, "merge refused: required check tests missing")
	})
	t.Run("d: main red and the task is not the fix", func(t *testing.T) {
		r := newJudgeRig(t)
		r.both()
		r.sql("INSERT INTO config(key,value) VALUES('red_main_freeze','1') ON CONFLICT(key) DO UPDATE SET value='1'")
		r.sql(`INSERT INTO config(key,value) VALUES('red_main:o/r','{"run": 123, "task": "T-9999", "url": "https://x/run/123"}')`)
		code, out, e, del := r.run("", "T-0001", "--merge", "--reason", "ship")
		noDelegate(t, del)
		r.refused(code, out, e, "merge refused: red main: run 123 on o/r failed (https://x/run/123); merges wait for a green push run; nothing merged")
	})
	t.Run("e: judge hold set", func(t *testing.T) {
		r := newJudgeRig(t)
		r.both()
		r.sql("UPDATE tasks SET judge_hold_reason='wait for the lead'")
		code, out, e, del := r.run("", "T-0001", "--merge", "--reason", "ship")
		noDelegate(t, del)
		if out != "judge-hold T-0001: wait for the lead\n" {
			t.Fatalf("stdout = %q", out)
		}
		r.refused(code, out, e, "merge refused: judge hold: wait for the lead; nothing merged")
	})
	t.Run("f: merge freeze from a non-PR push", func(t *testing.T) {
		r := newJudgeRig(t)
		r.both()
		r.sql("INSERT INTO config(key,value) VALUES('main_freeze','abc1234')")
		code, out, e, del := r.run("", "T-0001", "--merge", "--reason", "ship")
		noDelegate(t, del)
		r.refused(code, out, e, "merge refused: non-PR push on main: abc1234; nothing merged. Only the director or boss clears it (factory main-unfreeze)")
	})
	t.Run("g: both PASS on the head and CI green", func(t *testing.T) {
		r := newJudgeRig(t)
		r.both()
		code, out, e, del := r.run("", "T-0001", "--merge", "--reason", "ship")
		noDelegate(t, del)
		if code != 0 || e != "" {
			t.Fatalf("exit %d stderr %q", code, e)
		}
		r.merged(out)
	})
	t.Run("g2: the reason comes from stdin", func(t *testing.T) {
		r := newJudgeRig(t)
		r.both()
		code, out, e, del := r.run("all gates green\n", "T-0001", "--merge", "--reason", "-")
		noDelegate(t, del)
		if code != 0 || e != "" {
			t.Fatalf("exit %d stderr %q", code, e)
		}
		r.merged(out)
		if got := r.scalar("SELECT summary FROM verdicts WHERE role='judge'"); got != "all gates green" {
			t.Fatalf("summary = %q", got)
		}
	})
	t.Run("h: main red and the task is the named fix", func(t *testing.T) {
		r := newJudgeRig(t)
		r.both()
		r.sql("INSERT INTO config(key,value) VALUES('red_main_freeze','1') ON CONFLICT(key) DO UPDATE SET value='1'")
		r.sql(`INSERT INTO config(key,value) VALUES('red_main:o/r','{"run": 123, "task": "T-0001", "url": "https://x/run/123"}')`)
		code, out, e, del := r.run("", "T-0001", "--merge", "--reason", "fix main")
		noDelegate(t, del)
		if code != 0 || e != "" {
			t.Fatalf("exit %d stderr %q", code, e)
		}
		r.merged(out)
	})
	t.Run("i: a PASS carried across a pure main merge still counts", func(t *testing.T) {
		r := newJudgeRig(t)
		// main moves on; the PR head is a merge of the new main into the verdict head, published as the PR head
		os.WriteFile(filepath.Join(r.wt, "m"), []byte("m\n"), 0o644)
		jgit(t, r.wt, "add", "m")
		jgit(t, r.wt, "commit", "-q", "-m", "main moves")
		r.main = jgit(t, r.wt, "rev-parse", "HEAD")
		jgit(t, r.wt, "push", "-q", r.origin, "HEAD:refs/heads/main")
		os.WriteFile(filepath.Join(r.stub, "main"), []byte(r.main+"\n"), 0o644)
		jgit(t, r.wt, "checkout", "-q", "-b", "carry", r.other)
		jgit(t, r.wt, "merge", "-q", "--no-ff", "-m", "merge main", r.main)
		r.head = jgit(t, r.wt, "rev-parse", "HEAD")
		jgit(t, r.wt, "push", "-q", r.origin, "carry:refs/pull/7/head", "--force")
		r.setPR("SUCCESS")
		r.verdict("reviewer", "pass", r.other)
		r.verdict("qa", "pass", r.other)
		code, out, e, del := r.run("", "T-0001", "--merge", "--reason", "ship")
		noDelegate(t, del)
		if code != 0 || e != "" {
			t.Fatalf("exit %d stderr %q stdout %q", code, e, out)
		}
		for _, role := range []string{"reviewer", "qa"} {
			if !strings.Contains(out, fmt.Sprintf("%s PASS carried %s -> %s across 1 pure origin/main merge(s)\n", role, short8(r.other), short8(r.head))) {
				t.Fatalf("stdout lacks the %s carry line: %q", role, out)
			}
		}
		if got := r.scalar("SELECT count(*) FROM events WHERE action='verdict-carried'"); got != "2" {
			t.Fatalf("verdict-carried events = %s, want 2", got)
		}
	})
}
