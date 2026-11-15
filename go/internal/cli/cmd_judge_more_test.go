package cli

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func (r *judgeRig) rows(table string) string { return r.scalar("SELECT count(*) FROM " + table) }

// TestJudgeParse: the argv shapes Go answers, and every shape that must reach Python's argparse.
func TestJudgeParse(t *testing.T) {
	ours := [][]string{
		{"T-1", "--merge", "--reason", "x"},
		{"--merge", "T-1", "--reason=x"},
		{"T-1", "--reject", "--reason", "-", "--cause", "bug"},
		{"T-1", "--reason", "x", "--merge"},
	}
	for _, a := range ours {
		if _, ok := parseJudge(a); !ok {
			t.Errorf("parseJudge(%v) = not ok, want ok", a)
		}
	}
	python := [][]string{
		{"--train", "T-1", "T-2"}, {"T-1", "--record-merged", "abc", "--reason", "x"}, {"T-1", "--merge", "--dry-run", "--reason", "x"},
		{"T-1", "--reject", "--no-code-change", "--reason", "x"}, {"T-1", "--mer", "--reason", "x"}, {"T-1", "--merge", "--reject"},
		{"T-1", "--reason", "x"}, {"T-1", "--merge", "--reason"}, {"T-1", "--merge", "--reason", "-x"}, {"T-1", "T-2", "--merge"},
		{"T-1", "--merge", "--reason", "a", "--reason", "b"}, {"T-1", "--merge", "--", "x"}, {"T-1", "--merge", "-h"},
		{"T-1", "--reject", "--cause", "büg", "--reason", "x"}, {"T-1", "--merge", "--reason", "\xff"},
	}
	for _, a := range python {
		if _, ok := parseJudge(a); ok {
			t.Errorf("parseJudge(%q) = ok, want it handed to Python", a)
		}
	}
}

func FuzzParseJudge(f *testing.F) {
	f.Add("T-1\x00--merge\x00--reason\x00ship")
	f.Add("--reject\x00--cause=bug\x00--reason\x00-")
	f.Add("--train\x00T-1")
	f.Fuzz(func(t *testing.T, s string) {
		if p, ok := parseJudge(strings.Split(s, "\x00")); ok {
			if c := p.(judgeCmd); strings.HasPrefix(c.task, "-") && c.task != "-" {
				t.Fatalf("a flag-looking word %q was taken as the task", c.task)
			}
		}
	})
}

// TestJudgeHandsOverBeforeWriting: what Go does not decide the way Python does goes to Python with nothing written and
// stdin untouched.
func TestJudgeHandsOverBeforeWriting(t *testing.T) {
	cases := []struct {
		name string
		prep func(r *judgeRig)
	}{
		{"head does not contain main (refresh, behind_ok)", func(r *judgeRig) {
			jgit(t, r.wt, "checkout", "-q", "main")
			os.WriteFile(filepath.Join(r.wt, "m"), []byte("m\n"), 0o644)
			jgit(t, r.wt, "add", "m")
			jgit(t, r.wt, "commit", "-q", "-m", "main moves")
			r.main = jgit(t, r.wt, "rev-parse", "HEAD")
			jgit(t, r.wt, "push", "-q", r.origin, "HEAD:refs/heads/main")
			os.WriteFile(filepath.Join(r.stub, "main"), []byte(r.main+"\n"), 0o644)
		}},
		{"local-gate project", func(r *judgeRig) { r.sql("UPDATE projects SET gate_cmd='make test'") }},
		{"public_text_repos configured", func(r *judgeRig) {
			r.sql("INSERT INTO config(key,value) VALUES('public_text_repos','o/r') ON CONFLICT(key) DO UPDATE SET value='o/r'")
		}},
		{"git config Python might refuse", func(r *judgeRig) { jgit(t, r.wt, "config", "core.sshCommand", "echo hi") }},
		{"tasks.judge_hold_reason column missing", func(r *judgeRig) { r.sql("ALTER TABLE tasks DROP COLUMN judge_hold_reason") }},
		{"a status check of a shape Go does not read", func(r *judgeRig) {
			js := `{"number":7,"state":"OPEN","headRefOid":"` + r.head + `","headRefName":"b","baseRefName":"main","mergeable":"MERGEABLE","isDraft":false,"statusCheckRollup":[{"name":["x"]}],"url":"u"}`
			os.WriteFile(filepath.Join(r.stub, "pr.json"), []byte(js), 0o644)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newJudgeRig(t)
			r.both()
			tc.prep(r)
			code, out, e, del := r.run("reason from stdin", "T-0001", "--merge", "--reason", "-")
			if !del {
				t.Fatalf("not handed to python: exit %d stdout %q stderr %q", code, out, e)
			}
			if strings.Contains(r.ghLog(), "pr merge") {
				t.Fatalf("gh pr merge ran before the hand-over:\n%s", r.ghLog())
			}
			if n := r.rows("events") + r.rows("rejection_incidents"); n != "00" {
				t.Fatalf("events/incidents written before the hand-over: %s", n)
			}
			if got := r.scalar("SELECT status FROM tasks WHERE id='T-0001'"); got != "in_review" {
				t.Fatalf("task status = %q", got)
			}
		})
	}
}

// TestJudgeRefusals: refusals that are not in the item-4 table, each with Python's text.
func TestJudgeRefusals(t *testing.T) {
	t.Run("no judge role", func(t *testing.T) {
		r := newJudgeRig(t)
		r.sql("UPDATE agents SET role='qa' WHERE name='claude-judge'")
		code, _, e, _ := r.run("", "T-0001", "--merge", "--reason", "x")
		if code != 3 || !strings.Contains(e, "may not do this; allowed: judge") {
			t.Fatalf("exit %d stderr %q", code, e)
		}
	})
	t.Run("unknown task, no PR", func(t *testing.T) {
		r := newJudgeRig(t)
		if code, _, e, _ := r.run("", "T-9", "--merge", "--reason", "x"); code != 1 || e != "factory: no task T-9\n" {
			t.Fatalf("exit %d stderr %q", code, e)
		}
		r.sql("UPDATE tasks SET pr_url=NULL")
		if code, _, e, _ := r.run("", "T-0001", "--merge", "--reason", "x"); code != 1 || e != "factory: T-0001 has no PR\n" {
			t.Fatalf("exit %d stderr %q", code, e)
		}
	})
	t.Run("empty reason", func(t *testing.T) {
		r := newJudgeRig(t)
		if code, _, e, _ := r.run("", "T-0001", "--merge", "--reason", ""); code != 2 ||
			e != "factory: judge needs a task and --reason (or --train T-a T-b ... [--dry-run])\n" {
			t.Fatalf("exit %d stderr %q", code, e)
		}
	})
	t.Run("draft, conflicts and the closed PR are all named", func(t *testing.T) {
		r := newJudgeRig(t)
		r.both()
		js := `{"number":7,"state":"CLOSED","headRefOid":"` + r.head + `","headRefName":"b","baseRefName":"main","mergeable":"CONFLICTING","isDraft":true,"statusCheckRollup":[],"url":"u"}`
		os.WriteFile(filepath.Join(r.stub, "pr.json"), []byte(js), 0o644)
		code, out, e, _ := r.run("", "T-0001", "--merge", "--reason", "x")
		r.refused(code, out, e, "merge refused: PR is CLOSED; PR is a draft; PR has conflicts")
	})
	t.Run("project auto_merge=0", func(t *testing.T) {
		r := newJudgeRig(t)
		r.both()
		r.sql("UPDATE projects SET auto_merge=0")
		code, out, e, _ := r.run("", "T-0001", "--merge", "--reason", "x")
		r.refused(code, out, e, "merge refused: project has auto_merge=0 — boss must get the owner's OK")
	})
	t.Run("frozen repo", func(t *testing.T) {
		r := newJudgeRig(t)
		r.both()
		r.sql("INSERT INTO config(key,value) VALUES('frozen_repos','o/r') ON CONFLICT(key) DO UPDATE SET value='o/r'")
		code, out, e, _ := r.run("", "T-0001", "--merge", "--reason", "x")
		r.refused(code, out, e, "merge refused: repo o/r is frozen (G-021); nothing merged")
	})
	t.Run("judge agent stopped after the gate started", func(t *testing.T) {
		r := newJudgeRig(t)
		r.both()
		r.sql("UPDATE agents SET stop_requested_at='2026-11-12T00:00:00+00:00' WHERE name='claude-judge'")
		code, out, e, _ := r.run("", "T-0001", "--merge", "--reason", "x")
		r.refused(code, out, e, "merge refused: judge agent claude-judge has stop requested; nothing merged")
	})
	t.Run("task no longer in_review", func(t *testing.T) {
		r := newJudgeRig(t)
		r.both()
		r.sql("UPDATE tasks SET status='done'")
		code, _, e, _ := r.run("", "T-0001", "--merge", "--reason", "x")
		if code != 4 || e != "factory: merge refused: T-0001 is no longer in_review (merged or moved by another run); nothing merged\n" {
			t.Fatalf("exit %d stderr %q", code, e)
		}
	})
	t.Run("gh pr merge fails", func(t *testing.T) {
		r := newJudgeRig(t)
		r.both()
		os.WriteFile(filepath.Join(r.stub, "merge_fail"), []byte("boom\n"), 0o644)
		code, _, e, _ := r.run("", "T-0001", "--merge", "--reason", "x")
		if code != 5 || e != "factory: gh pr merge failed: boom\n" {
			t.Fatalf("exit %d stderr %q", code, e)
		}
		if got := r.scalar("SELECT detail FROM events WHERE action='merge-failed'"); got != "boom" {
			t.Fatalf("merge-failed event = %q", got)
		}
		if got := r.scalar("SELECT status FROM tasks WHERE id='T-0001'"); got != "in_review" {
			t.Fatalf("task status = %q after a failed merge", got)
		}
	})
}

// TestJudgeMergeRetryStdinReason: "Base branch was modified" retries the merge once; the --reason - text is read once
// (stdin cannot be read twice) and the retried merge still carries it.
func TestJudgeMergeRetryStdinReason(t *testing.T) {
	r := newJudgeRig(t)
	r.both()
	os.WriteFile(filepath.Join(r.stub, "merge_once"), []byte("GraphQL: Base branch was modified. Review and try the merge again.\n"), 0o644)
	code, out, e, del := r.run("ship it from stdin\n", "T-0001", "--merge", "--reason", "-")
	if del || code != 0 || e != "" {
		t.Fatalf("delegated=%v exit %d stderr %q stdout %q", del, code, e, out)
	}
	r.merged(out)
	if n := strings.Count(r.ghLog(), "pr merge"); n != 2 {
		t.Fatalf("gh pr merge ran %d times, want 2 (one retry):\n%s", n, r.ghLog())
	}
	if got := r.scalar("SELECT summary FROM verdicts WHERE role='judge'"); got != "ship it from stdin" {
		t.Fatalf("judge verdict summary = %q, want the stdin reason", got)
	}
}

// TestJudgeReject: --reject writes the verdict, incident, status, event and the coder notice like Python.
func TestJudgeReject(t *testing.T) {
	r := newJudgeRig(t)
	code, out, e, del := r.run("", "T-0001", "--reject", "--reason", "tests missing", "--cause", "missing-test")
	if del || code != 0 || e != "" || out != "T-0001 REJECTED @ "+short8(r.head)+"\n" {
		t.Fatalf("delegated=%v exit %d stdout %q stderr %q", del, code, out, e)
	}
	if got := r.scalar("SELECT status FROM tasks WHERE id='T-0001'"); got != "in_progress" {
		t.Fatalf("status = %q", got)
	}
	if got := r.scalar("SELECT verdict||'|'||summary||'|'||head_sha FROM verdicts WHERE role='judge'"); got != "reject|tests missing|"+r.head {
		t.Fatalf("verdict = %q", got)
	}
	if got := r.scalar("SELECT cause||'|'||kind FROM rejection_incidents"); got != "missing_test|judge" {
		t.Fatalf("incident = %q", got)
	}
	if got := r.scalar("SELECT subject FROM messages WHERE recipient='claude-coder-t-1'"); got != "judge reject: T-0001 in_progress" {
		t.Fatalf("notice subject = %q", got)
	}
	if got := r.scalar("SELECT body FROM messages WHERE recipient='claude-coder-t-1'"); got !=
		"Task T-0001 status is in_progress after judge reject.\n\ntests missing\n\nAfter the fix is pushed: factory task update T-0001 --status in_review (existing PR checks apply)." {
		t.Fatalf("notice body = %q", got)
	}
	r2 := newJudgeRig(t)
	if code, _, e, _ := r2.run("", "T-0001", "--reject", "--reason", "x"); code != 2 || !strings.Contains(e, "rejection refused, nothing recorded: pass --cause") {
		t.Fatalf("no --cause: exit %d stderr %q", code, e)
	}
	if r2.rows("verdicts") != "0" {
		t.Fatal("a refused reject wrote a verdict")
	}
}

// TestJudgeLock: a merge that another judge holds the per-task lock for is refused, nothing merged.
func TestJudgeLock(t *testing.T) {
	r := newJudgeRig(t)
	r.both()
	j := &judgeEnv{home: r.home}
	f, err := judgeLock(j, "T-0001")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	code, _, e, _ := r.run("", "T-0001", "--merge", "--reason", "x")
	want := "factory: merge refused: another judge or mechanical merger is merging T-0001 right now (run/merge-T-0001.lock); nothing merged\n"
	if code != 4 || e != want {
		t.Fatalf("exit %d stderr %q, want %q", code, e, want)
	}
	if strings.Contains(r.ghLog(), "pr merge") {
		t.Fatal("merged while the lock was held")
	}
}

// TestJudgeUnicode: Python's str.strip, \d and \b are Unicode; so is what the port reads. NBSP, \v and U+0663 are
// whitespace / a digit there. Python: "HTTP 5٠٠ x" matches r"\bHTTP 5\d\d\b" -> True;
// " \v ab  ".strip() == "ab"; int("٣") == 3 but the port hands such a value to Python (not ok here).
func TestJudgeUnicode(t *testing.T) {
	if !judgeUnknownMerge("HTTP 5٠٠ bad") {
		t.Error("an Arabic-Indic digit after HTTP 5 must match \\d like Python")
	}
	if judgeUnknownMerge("éHTTP 500") {
		t.Error("\\b: a letter before HTTP is a word character in Python")
	}
	if !judgeUnknownMerge("HTTP 502 Bad Gateway") || !judgeUnknownMerge("operation Timed Out") || judgeUnknownMerge("HTTP 404") {
		t.Error("the ASCII forms of UNKNOWN_MERGE_ERROR")
	}
	if got := pyStrip(" \v ab  "); got != "ab" {
		t.Errorf("pyStrip = %q", got)
	}
	if _, ok := judgeInt("٣"); ok {
		t.Error("judgeInt took a non-ASCII digit; Python's int() reads it as 3, so the port must hand over")
	}
	if n, ok := judgeInt("2400"); !ok || n != 2400 {
		t.Errorf("judgeInt(2400) = %d %v", n, ok)
	}
	for _, bad := range []string{"", " 5", "5 ", "+5", "1_0", "-", "5 "} {
		if _, ok := judgeInt(bad); ok {
			t.Errorf("judgeInt(%q) ok", bad)
		}
	}
}

// TestRegistryJudge: judge is registered, and only the exact argv shapes of parseJudge reach its handler.
func TestRegistryJudge(t *testing.T) {
	if _, ok := registry[key{"judge", ""}]; !ok {
		t.Fatal("judge is not registered")
	}
	if _, _, ok := lookup([]string{"judge", "T-1", "--merge", "--reason", "x"}); !ok {
		t.Error("the plain merge form must be Go's")
	}
	for _, a := range [][]string{{"judge", "--train", "T-1", "T-2"}, {"judge", "T-1", "--record-merged", "abc"}, {"judge"}} {
		if _, _, ok := lookup(a); ok {
			t.Errorf("%v must be handed to Python", a)
		}
	}
}

// TestJudgeRoleFilesNotice: a merge of a PR that changes roles/ tells the director which live agents are stale, once
// (a second merge folds into the unread notice), like after_merge in Python.
func TestJudgeRoleFilesNotice(t *testing.T) {
	r := newJudgeRig(t)
	r.both()
	script := strings.Replace(judgeStubScript, `echo '{"files":[{"path":"go/x.go"}]}'`, `echo '{"files":[{"path":"roles/_common.md"},{"path":"go/x.go"}]}'`, 1)
	if err := os.WriteFile(filepath.Join(filepath.Dir(r.stub), "bin", "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	r.sql("INSERT INTO agents(name,role,status) VALUES('claude-director','director','live')")
	code, out, e, del := r.run("", "T-0001", "--merge", "--reason", "ship")
	if del || code != 0 || e != "" {
		t.Fatalf("delegated=%v exit %d stderr %q", del, code, e)
	}
	r.merged(out)
	if got := r.scalar("SELECT subject FROM messages WHERE recipient='claude-director'"); got != "role files changed: stale agents" {
		t.Fatalf("director notice subject = %q", got)
	}
	want := "PR: https://github.com/o/r/pull/7\nChanged files:\n- roles/_common.md\nLive agents:\n- claude-director (director): factory relaunch claude-director --when-idle\nLive agents above have a stale prompt."
	if got := r.scalar("SELECT body FROM messages WHERE recipient='claude-director'"); got != want {
		t.Fatalf("director notice body = %q\nwant %q", got, want)
	}
	if got := r.scalar("SELECT subject FROM messages WHERE recipient='boss'"); got != "role files changed: director stale" {
		t.Fatalf("boss notice subject = %q (the director itself is stale)", got)
	}
	if got := r.scalar("SELECT detail FROM events WHERE action='roles-changed'"); got != "claude-director" {
		t.Fatalf("roles-changed event = %q", got)
	}
}

// TestJudgeGHTimeout: a gh that hangs ends in a hand-over to Python after the timeout, not in a hang.
func TestJudgeGHTimeout(t *testing.T) {
	old := judgeGHTimeout
	judgeGHTimeout = 300 * time.Millisecond
	defer func() { judgeGHTimeout = old }()
	r := newJudgeRig(t)
	r.both()
	os.WriteFile(filepath.Join(r.stub, "hang"), []byte("x"), 0o644)
	start := time.Now()
	_, _, _, del := r.run("", "T-0001", "--merge", "--reason", "x")
	if !del {
		t.Fatal("a hanging gh pr view was not handed to python")
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("took %s: the gh timeout did not fire", d)
	}
	if got := r.rows("events"); got != "0" {
		t.Fatalf("events written before the hand-over: %s", got)
	}
}

// TestJudgeSQLiteText: the driver's error text is reduced to Python's sqlite3 str(e).
func TestJudgeSQLiteText(t *testing.T) {
	for in, want := range map[string]string{
		"constraint failed: zz-fail (1811)":                       "zz-fail",
		"constraint failed: UNIQUE constraint failed: t.c (2067)": "UNIQUE constraint failed: t.c",
		"SQL logic error: no such table: x (1)":                   "no such table: x",
		"plain error (5)":                                         "plain error (5)",
	} {
		if got := judgeSQLiteText(in); got != want {
			t.Errorf("judgeSQLiteText(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestReplayStdin: a child process that replays text onto its fd 0 reads exactly that text from os.Stdin; an
// oversized text is refused before any fd is touched. (The dup onto fd 0 is done in a child, never in the test process.)
func TestReplayStdin(t *testing.T) {
	if os.Getenv("REPLAY_STDIN_CHILD") == "1" {
		if err := replayStdin([]byte("why I merge\n")); err != nil {
			os.Stderr.WriteString(err.Error())
			os.Exit(3)
		}
		b, _ := io.ReadAll(os.Stdin)
		os.Stdout.Write(b)
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestReplayStdin$")
	cmd.Env = append(os.Environ(), "REPLAY_STDIN_CHILD=1")
	cmd.Stdin = strings.NewReader("ignored original stdin")
	out, err := cmd.Output()
	if err != nil || string(out) != "why I merge\n" {
		t.Fatalf("child output %q err %v, want the replayed text", out, err)
	}
	if err := replayStdin(make([]byte, replayStdinMax+1)); err == nil {
		t.Fatal("an oversized text was accepted")
	}
}

// TestJudgeReadReasonKeepsRawBytes: --reason - keeps the raw stdin bytes for the hand-over to Python, even when they are
// not UTF-8 (Go hands over, Python decides what to record) and even when Go strips them for its own use.
func TestJudgeReadReasonKeepsRawBytes(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		wantErr  bool
		want     string
	}{
		{"invalid utf-8", "bad \xff\xfe reason\n", true, ""},
		{"valid, stripped for Go only", "  why\r\n\n", false, "why"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var replay []byte
			c := &ctx{deps: &Deps{Stdin: strings.NewReader(tc.in)}}
			got, err := judgeReadReason(c, judgeCmd{reason: "-", replay: &replay})
			if tc.wantErr != errors.Is(err, errDelegate) {
				t.Fatalf("err = %v, want delegate=%v", err, tc.wantErr)
			}
			if !tc.wantErr && *got != tc.want {
				t.Fatalf("reason = %q, want %q", *got, tc.want)
			}
			if string(replay) != tc.in {
				t.Fatalf("replay = %q, want the raw stdin %q", replay, tc.in)
			}
		})
	}
}
