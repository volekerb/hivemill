package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitIn runs git in dir for test setup; a failure fails the test.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=1+a@users.noreply.github.com",
		"GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=1+a@users.noreply.github.com",
		"GIT_AUTHOR_DATE=2026-11-12T00:00:00+0000", "GIT_COMMITTER_DATE=2026-11-12T00:00:00+0000")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

const goodBody = `## Summary
Adds x.

## Why
Because.

## Changes
- x

## Test evidence
` + "`go test ./...`" + ` -> ok

## Risk / rollback
none

<details>
<summary>Acceptance walk</summary>

## Acceptance walk
1. ok -> PASS

</details>
`

// prRepo is an origin with main, and a task worktree on factory/t-0001 one commit ahead. A fake gh (on PATH) answers
// `pr view --json body` from body.json and any other `pr view` from state.json, both in the stub directory.
type prRepo struct {
	e      *cliEnv
	origin string
	wt     string
	stub   string
}

func newPrRepo(t *testing.T, fast bool) *prRepo {
	t.Helper()
	e := newCliEnvLive(t, false)
	root := t.TempDir()
	r := &prRepo{e: e, origin: filepath.Join(root, "origin.git"), wt: filepath.Join(root, "wt"), stub: filepath.Join(root, "stub")}
	gitIn(t, root, "init", "-q", "--bare", "-b", "main", r.origin)
	gitIn(t, root, "clone", "-q", r.origin, r.wt)
	gitIn(t, r.wt, "checkout", "-q", "-b", "main")
	os.MkdirAll(filepath.Join(r.wt, "tests"), 0o755)
	os.MkdirAll(filepath.Join(r.wt, "ci"), 0o755)
	os.WriteFile(filepath.Join(r.wt, "tests", "test_a.py"), []byte("class A:\n    def test_one(self):\n        pass\n"), 0o644)
	os.WriteFile(filepath.Join(r.wt, "f.txt"), []byte("base\n"), 0o644)
	if fast {
		os.WriteFile(filepath.Join(r.wt, "ci", "affected.py"), []byte("# stub\n"), 0o644)
	}
	gitIn(t, r.wt, "add", "-A")
	gitIn(t, r.wt, "commit", "-q", "-m", "base")
	gitIn(t, r.wt, "push", "-q", "origin", "main")
	gitIn(t, r.wt, "checkout", "-q", "-b", "factory/t-0001")
	os.WriteFile(filepath.Join(r.wt, "g.txt"), []byte("work\n"), 0o644)
	gitIn(t, r.wt, "add", "-A")
	gitIn(t, r.wt, "commit", "-q", "-m", "work")
	os.MkdirAll(r.stub, 0o755)
	script := "#!/bin/sh\nlast=\"\"; for a in \"$@\"; do last=\"$a\"; done\n" +
		"if [ \"$1 $2\" != \"pr view\" ]; then echo \"unexpected gh $*\" >&2; exit 9; fi\n" +
		"if [ \"$last\" = body ]; then cat \"" + r.stub + "/body.json\"; else cat \"" + r.stub + "/state.json\"; fi\n"
	os.WriteFile(filepath.Join(r.stub, "gh"), []byte(script), 0o755)
	t.Setenv("PATH", r.stub+":"+os.Getenv("PATH"))
	r.setBody(goodBody)
	r.setState("OPEN", gitIn(t, r.wt, "rev-parse", "HEAD"), `[{"__typename":"CheckRun","name":"tests","conclusion":"SUCCESS","status":"COMPLETED"}]`)
	e.sql("UPDATE tasks SET worktree=?, pr_url='https://github.com/acme/app/pull/7', branch='factory/t-0001' WHERE id='T-0001'", r.wt)
	e.sql("UPDATE agents SET cwd=? WHERE name='claude-coder'", r.wt)
	e.sql("INSERT OR REPLACE INTO config(key,value) VALUES('gate_fast_projects','P-1')")
	return r
}

func (r *prRepo) setBody(b string) {
	j, _ := json.Marshal(map[string]string{"body": b})
	os.WriteFile(filepath.Join(r.stub, "body.json"), j, 0o644)
}

func (r *prRepo) setState(state, head, rollup string) {
	os.WriteFile(filepath.Join(r.stub, "state.json"), []byte(`{"number":7,"state":"`+state+`","headRefOid":"`+head+
		`","headRefName":"factory/t-0001","baseRefName":"main","mergeable":"MERGEABLE","isDraft":false,"statusCheckRollup":`+rollup+`,"url":"u"}`), 0o644)
}

func (r *prRepo) prcheck(t *testing.T) (string, string, int) {
	t.Helper()
	return r.e.run("claude-coder", "", "prcheck")
}

// TestPrcheckRefusals: the PR-readiness gate in both directions, through the real entry point (Run) with real git and a
// stub gh. The texts are the ones bin/factory_cli.py prints (prcheck_fresh, prcheck_identity, prcheck_body).
func TestPrcheckRefusals(t *testing.T) {
	t.Run("clean branch passes", func(t *testing.T) {
		r := newPrRepo(t, true)
		out, errs, code := r.prcheck(t)
		full := gitIn(t, r.wt, "rev-parse", "HEAD")
		want := "PASS fresh: HEAD contains origin/main\n" +
			"PASS identity: 1 commit(s), author and committer noreply\n" +
			"PASS body: PR body follows .github/pull_request_template.md\n" +
			"PASS public-text: n/a: the repo is not in public_text_repos\n" +
			"PASS affected: not run locally (gate_fast_projects, M16816); CI is green on the PR head (all checks SUCCESS)\n" +
			"PASS tier: tier: unavailable (no risk classifier in this build)\n" +
			"prcheck passed on " + full[:9] + "\n"
		if code != 0 || out != want || errs != "" {
			t.Fatalf("exit %d\nout %q\nwant %q\nerr %q", code, out, want, errs)
		}
		if d := r.e.str("SELECT detail FROM events WHERE action='prcheck'"); d != `{"head": "`+full+`", "ok": true, "failed": []}` {
			t.Errorf("event detail %q", d)
		}
	})
	t.Run("non-noreply author is refused", func(t *testing.T) {
		r := newPrRepo(t, true)
		cmd := exec.Command("git", "-c", "user.email=me@example.com", "-c", "user.name=me", "commit", "-q", "--allow-empty", "-m", "bad2")
		cmd.Dir = r.wt
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=me", "GIT_AUTHOR_EMAIL=me@example.com",
			"GIT_COMMITTER_NAME=me", "GIT_COMMITTER_EMAIL=me@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("commit: %v %s", err, out)
		}
		head := gitIn(t, r.wt, "rev-parse", "HEAD")
		r.setState("OPEN", head, `[{"name":"tests","conclusion":"SUCCESS"}]`)
		out, _, code := r.prcheck(t)
		if code != 1 || !strings.Contains(out, "FAIL identity: non-noreply identity in "+head[:9]+" author+committer: set the repo-local user.email to your "+
			"@users.noreply.github.com address and rewrite those commits (git rebase -r --exec 'git commit --amend --reset-author --no-edit')\n") ||
			!strings.HasSuffix(out, "prcheck FAILED on "+head[:9]+": fix the FAIL lines, commit, rerun\n") {
			t.Fatalf("exit %d out %q", code, out)
		}
	})
	t.Run("branch behind main with a conflict is refused", func(t *testing.T) {
		r := newPrRepo(t, true)
		gitIn(t, r.wt, "checkout", "-q", "main")
		os.WriteFile(filepath.Join(r.wt, "g.txt"), []byte("main side\n"), 0o644)
		gitIn(t, r.wt, "add", "-A")
		gitIn(t, r.wt, "commit", "-q", "-m", "main moves")
		gitIn(t, r.wt, "push", "-q", "origin", "main")
		gitIn(t, r.wt, "checkout", "-q", "factory/t-0001")
		out, _, code := r.prcheck(t)
		if code != 1 || !strings.Contains(out, "FAIL fresh: merging origin/main conflicts (g.txt): run git merge origin/main, resolve, rerun the tests, push\n") {
			t.Fatalf("exit %d out %q", code, out)
		}
	})
	t.Run("branch behind main but clean passes with a note", func(t *testing.T) {
		r := newPrRepo(t, true)
		gitIn(t, r.wt, "checkout", "-q", "main")
		os.WriteFile(filepath.Join(r.wt, "h.txt"), []byte("other\n"), 0o644)
		gitIn(t, r.wt, "add", "-A")
		gitIn(t, r.wt, "commit", "-q", "-m", "main moves")
		gitIn(t, r.wt, "push", "-q", "origin", "main")
		gitIn(t, r.wt, "checkout", "-q", "factory/t-0001")
		out, _, code := r.prcheck(t)
		if code != 0 || !strings.Contains(out, "PASS fresh: behind origin/main by 1; a merge is clean (git fetch origin && git merge origin/main keeps it fresh)\n") {
			t.Fatalf("exit %d out %q", code, out)
		}
	})
	t.Run("PR body without the acceptance walk is refused", func(t *testing.T) {
		r := newPrRepo(t, true)
		r.setBody(strings.Replace(goodBody, "## Acceptance walk", "## Something else", 1))
		out, _, code := r.prcheck(t)
		want := "FAIL body: PR body of https://github.com/acme/app/pull/7:\n      - no <details> block holding a `## Acceptance walk` line (the walk goes inside <details>)\n"
		if code != 1 || !strings.Contains(out, want) {
			t.Fatalf("exit %d out %q", code, out)
		}
	})
	t.Run("a dropped test count needs a Tests removed line", func(t *testing.T) {
		r := newPrRepo(t, true)
		gitIn(t, r.wt, "rm", "-q", "tests/test_a.py")
		gitIn(t, r.wt, "commit", "-q", "-m", "drop")
		r.setState("OPEN", gitIn(t, r.wt, "rev-parse", "HEAD"), `[{"name":"tests","conclusion":"SUCCESS"}]`)
		out, _, code := r.prcheck(t)
		if code != 1 || !strings.Contains(out, "      - the test count drops (1 at the merge base with origin/main, 0 on HEAD): add a line `Tests removed: <which and why>` outside <details>\n") {
			t.Fatalf("exit %d out %q", code, out)
		}
		r.setBody(strings.Replace(goodBody, "## Risk / rollback\nnone", "## Risk / rollback\nnone\nTests removed: test_one, obsolete", 1))
		if out, _, code := r.prcheck(t); code != 0 {
			t.Fatalf("with the line: exit %d out %q", code, out)
		}
	})
	t.Run("CI not green on the head", func(t *testing.T) {
		r := newPrRepo(t, true)
		r.setState("OPEN", gitIn(t, r.wt, "rev-parse", "HEAD"), `[{"name":"tests","conclusion":"FAILURE"},{"name":"lint","status":"IN_PROGRESS"}]`)
		out, errs, code := r.prcheck(t)
		if code != 1 || !strings.Contains(out, "FAIL affected: not run locally (gate_fast_projects, M16816); CI is not green on the head yet (CI not all green: tests=FAILURE, lint=IN_PROGRESS): push, wait for CI, rerun prcheck\n") || errs != "" {
			t.Fatalf("exit %d out %q err %q", code, out, errs)
		}
		r.setState("CLOSED", gitIn(t, r.wt, "rev-parse", "HEAD"), `[]`)
		out, _, _ = r.prcheck(t)
		if !strings.Contains(out, "(PR is CLOSED, not OPEN)") {
			t.Errorf("closed: %q", out)
		}
		r.setState("OPEN", "abcdef0123456789", `[]`)
		out, _, _ = r.prcheck(t)
		if !strings.Contains(out, "(PR head abcdef012 != local HEAD ") {
			t.Errorf("moved head: %q", out)
		}
	})
	t.Run("missing ci/affected.py", func(t *testing.T) {
		r := newPrRepo(t, false)
		out, _, code := r.prcheck(t)
		if code != 1 || !strings.Contains(out, "FAIL affected: ci/affected.py is missing in the worktree: git fetch origin && git merge origin/main\n") {
			t.Fatalf("exit %d out %q", code, out)
		}
	})
}

// TestPrcheckHandsOver: what Go does not decide runs in Python before any check, with nothing written.
func TestPrcheckHandsOver(t *testing.T) {
	r := newPrRepo(t, true)
	r.e.sql("INSERT OR REPLACE INTO config(key,value) VALUES('public_text_repos','acme/app')")
	out, errs, code := r.prcheck(t)
	if r.e.execs != 1 || out != "" || errs != "" || code != 0 {
		t.Errorf("public repo: execs %d exit %d out %q err %q", r.e.execs, code, out, errs)
	}
	r.e.sql("DELETE FROM config WHERE key='public_text_repos'")
	r.e.sql("UPDATE config SET value='' WHERE key='gate_fast_projects'")
	r.prcheck(t)
	if r.e.execs != 1 {
		t.Errorf("local test run: execs %d, want 1 (the slot and ci/affected.py are Python)", r.e.execs)
	}
	if n := r.e.str("SELECT count(*) FROM events WHERE action='prcheck'"); n != "0" {
		t.Errorf("a hand-over wrote %s prcheck events", n)
	}
}

func TestPrcheckPreconditions(t *testing.T) {
	r := newPrRepo(t, true)
	if _, errs, code := r.e.run("claude-pm", "", "prcheck"); code != 3 || !strings.Contains(errs, "role 'pm' (claude-pm) may not do this; allowed: coder") {
		t.Errorf("non-coder: exit %d err %q", code, errs)
	}
	if _, errs, code := r.e.run("owner", "", "prcheck"); code != 3 || errs != "factory: prcheck is for a coder with a task; run it from your task worktree\n" {
		t.Errorf("owner: exit %d err %q", code, errs)
	}
	os.WriteFile(filepath.Join(r.wt, "g.txt"), []byte("dirty\n"), 0o644)
	if _, errs, code := r.prcheck(t); code != 3 || errs != "factory: uncommitted tracked changes: commit them (a WIP commit is fine), then rerun prcheck\n" {
		t.Errorf("dirty: exit %d err %q", code, errs)
	}
	r.e.sql("UPDATE tasks SET worktree='/nonexistent-dir' WHERE id='T-0001'")
	if _, errs, code := r.prcheck(t); code != 3 || errs != "factory: T-0001 has no worktree directory; ask your lead\n" {
		t.Errorf("no worktree: exit %d err %q", code, errs)
	}
}

func FuzzParsePrcheck(f *testing.F) {
	for _, s := range []string{"--pr", "--pr=u", "-", "", "x", "\xff"} {
		f.Add(s, "u")
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		parsePrcheck([]string{a, b})
		parsePrcheck([]string{a})
	})
}
