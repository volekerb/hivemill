package lanecheck

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"factory/go/internal/gitx"
)

// isolate keeps the developer's git config out of the test.
func isolate(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "init.defaultBranch=main"}, args...)
	r, err := gitx.Run(dir, gitx.Options{}, full...)
	if err != nil || r.ExitCode != 0 {
		t.Fatalf("git %v: err=%v exit=%d stderr=%s", args, err, r.ExitCode, r.Stderr)
	}
	return strings.TrimSpace(r.Stdout)
}

func lines100(edits map[int]string) string {
	var b strings.Builder
	for i := 1; i <= 100; i++ {
		if s, ok := edits[i]; ok {
			b.WriteString(s + "\n")
		} else {
			fmt.Fprintf(&b, "line %d\n", i)
		}
	}
	return b.String()
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// stubOnPath writes an executable sh script called name and puts its directory first on PATH.
func stubOnPath(t *testing.T, name, script string) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// branch creates name from main with one commit that writes the files.
func branch(t *testing.T, dir, name string, files map[string]string) string {
	t.Helper()
	git(t, dir, "checkout", "-q", "-b", name, "main")
	for f, body := range files {
		write(t, dir, f, body)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", name)
	oid := git(t, dir, "rev-parse", "HEAD")
	git(t, dir, "checkout", "-q", "main")
	return oid
}

// baseRepo is a repo whose main has f.txt (100 lines) and other.txt, with origin/main pointing at it.
func baseRepo(t *testing.T) string {
	t.Helper()
	isolate(t)
	dir := filepath.Join(t.TempDir(), "remote")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-q")
	write(t, dir, "f.txt", lines100(nil))
	write(t, dir, "other.txt", "other\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "base")
	git(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
	return dir
}

func run(t *testing.T, dir string, args ...string) (int, string, string) {
	t.Helper()
	var so, se bytes.Buffer
	code := Main(append(args, "--dir", dir), &so, &se)
	return code, so.String(), se.String()
}

func outLines(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func TestPairClean(t *testing.T) {
	dir := baseRepo(t)
	branch(t, dir, "A", map[string]string{"f.txt": lines100(map[int]string{10: "A ten"})})
	branch(t, dir, "B", map[string]string{"f.txt": lines100(map[int]string{90: "B ninety"})})
	code, out, errs := run(t, dir, "pair", "A", "B")
	if code != 0 {
		t.Fatalf("exit %d, stderr %s", code, errs)
	}
	if got := outLines(out); len(got) != 2 || got[0] != "CLEAN" || got[1] != "both touch: f.txt" {
		t.Fatalf("output %q", out)
	}
}

func TestPairConflict(t *testing.T) {
	dir := baseRepo(t)
	branch(t, dir, "A", map[string]string{"f.txt": lines100(map[int]string{10: "A ten"})})
	branch(t, dir, "B", map[string]string{"f.txt": lines100(map[int]string{10: "B ten"})})
	code, out, errs := run(t, dir, "pair", "A", "B")
	if code != 3 {
		t.Fatalf("exit %d, want 3; stderr %s", code, errs)
	}
	if got := outLines(out); len(got) != 2 || got[0] != "CONFLICT: f.txt" || got[1] != "both touch: f.txt" {
		t.Fatalf("output %q", out)
	}
	// flags may follow the refs; an explicit base gives the same answer
	if code, out, _ := run(t, dir, "pair", "A", "B", "--base", "main"); code != 3 || !strings.HasPrefix(out, "CONFLICT: f.txt\n") {
		t.Fatalf("with --base main: exit %d output %q", code, out)
	}
}

// Several conflicted files come on one line, sorted; a path with a space is quoted so it stays one token.
func TestPairConflictSortedAndQuoted(t *testing.T) {
	dir := baseRepo(t)
	branch(t, dir, "A", map[string]string{"f.txt": lines100(map[int]string{10: "A ten"}), "a b.txt": "x\n"})
	branch(t, dir, "B", map[string]string{"f.txt": lines100(map[int]string{10: "B ten"}), "a b.txt": "y\n"})
	code, out, errs := run(t, dir, "pair", "A", "B")
	want := "CONFLICT: \"a b.txt\" f.txt\nboth touch: \"a b.txt\" f.txt\n"
	if code != 3 || out != want {
		t.Fatalf("exit %d stdout %q stderr %q, want %q", code, out, errs, want)
	}
}

// Paths are relative to the repository root also when the command runs in a subdirectory.
func TestPairFromSubdirectory(t *testing.T) {
	dir := baseRepo(t)
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "sub/s.txt", "base\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "sub")
	git(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
	branch(t, dir, "A", map[string]string{"f.txt": lines100(map[int]string{10: "A ten"}), "sub/s.txt": "A\n"})
	branch(t, dir, "B", map[string]string{"f.txt": lines100(map[int]string{10: "B ten"}), "sub/s.txt": "B\n"})
	code, out, errs := run(t, filepath.Join(dir, "sub"), "pair", "A", "B")
	want := "CONFLICT: f.txt sub/s.txt\nboth touch: f.txt sub/s.txt\n"
	if code != 3 || out != want {
		t.Fatalf("exit %d stdout %q stderr %q, want %q", code, out, errs, want)
	}
}

func TestPairNoSharedFile(t *testing.T) {
	dir := baseRepo(t)
	branch(t, dir, "A", map[string]string{"f.txt": lines100(map[int]string{10: "A ten"})})
	branch(t, dir, "B", map[string]string{"other.txt": "B\n"})
	code, out, _ := run(t, dir, "pair", "A", "B")
	if code != 0 || out != "CLEAN\nboth touch: (none)\n" {
		t.Fatalf("exit %d output %q", code, out)
	}
}

func TestPairErrors(t *testing.T) {
	dir := baseRepo(t)
	branch(t, dir, "A", map[string]string{"other.txt": "A\n"})
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"bad ref", []string{"pair", "A", "nosuchref"}, `bad ref "nosuchref"`},
		{"bad first ref", []string{"pair", "nosuchref", "A"}, `bad ref "nosuchref"`},
		{"bad base", []string{"pair", "A", "main", "--base", "nobase"}, `--base: bad ref "nobase"`},
		{"one ref", []string{"pair", "A"}, "pair needs exactly two refs"},
		{"unknown command", []string{"frobnicate"}, `unknown command "frobnicate"`},
		{"unknown flag", []string{"pair", "A", "main", "--nope"}, "flag provided but not defined"},
		{"plan without mode", []string{"plan", "A"}, "plan needs --against-open"},
		{"open with argument", []string{"open", "A"}, "open takes no positional argument"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errs := run(t, dir, tc.args...)
			if code != 2 || !strings.Contains(errs, tc.want) || out != "" {
				t.Fatalf("exit %d stdout %q stderr %q, want exit 2 and %q", code, out, errs, tc.want)
			}
		})
	}
	// "--" ends the flags, so a ref that looks like an option reaches Resolve and is refused there
	var so, se bytes.Buffer
	if code := Main([]string{"pair", "--dir", dir, "--", "--upload-pack=x", "A"}, &so, &se); code != 2 || !strings.Contains(se.String(), "may not be empty or start with '-'") {
		t.Fatalf("option-like ref: exit %d stderr %q", code, se.String())
	}
	if code, _, errs := run(t, t.TempDir(), "pair", "A", "B"); code != 2 || !strings.Contains(errs, `bad ref "A"`) {
		t.Fatalf("outside a repository: exit %d stderr %q", code, errs)
	}
}

func TestNoArgsAndHelp(t *testing.T) {
	var so, se bytes.Buffer
	if code := Main(nil, &so, &se); code != 2 || !strings.Contains(se.String(), "usage:") {
		t.Fatalf("no args: exit %d stderr %q", code, se.String())
	}
	so.Reset()
	if code := Main([]string{"--help"}, &so, &se); code != 0 || !strings.Contains(so.String(), "lane-check pair") {
		t.Fatalf("help: exit %d stdout %q", code, so.String())
	}
}

// An old git has no `merge-tree --write-tree`: the answer is exit 2 with the version hint, never CLEAN.
func TestOldGit(t *testing.T) {
	dir := baseRepo(t)
	branch(t, dir, "A", map[string]string{"other.txt": "A\n"})
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	stubOnPath(t, "git", `for a in "$@"; do if [ "$a" = merge-tree ]; then echo 'usage: git merge-tree <base-tree> <branch1> <branch2>' >&2; exit 129; fi; done
exec '`+real+`' "$@"
`)
	code, out, errs := run(t, dir, "pair", "A", "main")
	if code != 2 || out != "" || !strings.Contains(errs, "git >= 2.38") {
		t.Fatalf("exit %d stdout %q stderr %q", code, out, errs)
	}
}

// openFixture: a remote with PR heads under refs/pull/<n>/head, a clone of it, and a stub gh on PATH that lists them.
type openFixture struct {
	remote, work, ghLog, ghJSON string
}

func newOpenFixture(t *testing.T, heads map[int]map[string]string) (*openFixture, map[int]string) {
	t.Helper()
	remote := baseRepo(t)
	oids := map[int]string{}
	var rows []string
	for n := 1; n <= len(heads); n++ {
		oid := branch(t, remote, fmt.Sprintf("pr%d", n), heads[n])
		git(t, remote, "update-ref", fmt.Sprintf("refs/pull/%d/head", n), oid)
		oids[n] = oid
		// listed in reverse order: the output must still be sorted by number
		rows = append([]string{fmt.Sprintf(`{"number":%d,"headRefName":"pr%d","headRefOid":"%s"}`, n, n, oid)}, rows...)
	}
	root := t.TempDir()
	git(t, root, "clone", "-q", remote, "work")
	fx := &openFixture{remote: remote, work: filepath.Join(root, "work"), ghLog: filepath.Join(root, "gh.log"), ghJSON: filepath.Join(root, "gh.json")}
	fx.list(t, "["+strings.Join(rows, ",")+"]")
	stubOnPath(t, "gh", `echo "$*" >> '`+fx.ghLog+`'
cat '`+fx.ghJSON+`'
`)
	return fx, oids
}

func (fx *openFixture) list(t *testing.T, json string) {
	t.Helper()
	if err := os.WriteFile(fx.ghJSON, []byte(json), 0o644); err != nil {
		t.Fatal(err)
	}
}

// state is everything lane-check must leave alone in the work checkout.
func (fx *openFixture) state(t *testing.T) string {
	t.Helper()
	return "HEAD=" + git(t, fx.work, "rev-parse", "HEAD") + "\nrefs:\n" + git(t, fx.work, "for-each-ref") +
		"\nstatus:\n" + git(t, fx.work, "status", "--porcelain")
}

func threeHeads() map[int]map[string]string {
	return map[int]map[string]string{
		1: {"f.txt": lines100(map[int]string{10: "one ten"})},
		2: {"f.txt": lines100(map[int]string{10: "two ten"})},
		3: {"f.txt": lines100(map[int]string{90: "three ninety"})},
	}
}

func TestOpenNoSideEffects(t *testing.T) {
	fx, _ := newOpenFixture(t, map[int]map[string]string{
		1: {"f.txt": lines100(map[int]string{10: "one ten"})},
		2: {"f.txt": lines100(map[int]string{10: "two ten"})},
	})
	before := fx.state(t)
	code, out, errs := run(t, fx.work, "open")
	if code != 3 || out != "1 x 2 CONFLICT f.txt\n" {
		t.Fatalf("exit %d stdout %q stderr %q", code, out, errs)
	}
	if got := git(t, fx.work, "for-each-ref", "refs/lane-check"); got != "" {
		t.Fatalf("private refs left behind: %q", got)
	}
	if got := git(t, fx.work, "status", "--porcelain"); got != "" {
		t.Fatalf("working tree changed: %q", got)
	}
	if after := fx.state(t); after != before {
		t.Fatalf("checkout changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	log, err := os.ReadFile(fx.ghLog)
	if err != nil {
		t.Fatal(err)
	}
	if want := "pr list --state open --json number,headRefName,headRefOid --limit 1000\n"; string(log) != want {
		t.Fatalf("gh calls %q, want exactly one: %q", log, want)
	}
}

func TestOpenPairsAndExitCode(t *testing.T) {
	fx, _ := newOpenFixture(t, threeHeads())
	code, out, errs := run(t, fx.work, "open", "--repo", "volekerb/hivemill")
	if code != 3 {
		t.Fatalf("exit %d, want 3; stderr %s", code, errs)
	}
	want := []string{"1 x 2 CONFLICT f.txt", "1 x 3 CLEAN", "2 x 3 CLEAN"}
	if got := outLines(out); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("output %q, want %q", got, want)
	}
	if !strings.Contains(errs, "3 open PRs, 3 pairs checked, 1 conflict") {
		t.Fatalf("summary missing: %q", errs)
	}
	log, err := os.ReadFile(fx.ghLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(strings.TrimSpace(string(log)), "-R volekerb/hivemill") {
		t.Fatalf("--repo not passed to gh: %q", log)
	}

	code, out, errs = run(t, fx.work, "open", "--file", "f.txt")
	if code != 3 || len(outLines(out)) != 3 {
		t.Fatalf("--file f.txt: exit %d stdout %q stderr %q", code, out, errs)
	}
	code, out, errs = run(t, fx.work, "open", "--file", "other.txt")
	if code != 0 || out != "" {
		t.Fatalf("--file other.txt: exit %d stdout %q stderr %q", code, out, errs)
	}
	if got := git(t, fx.work, "for-each-ref", "refs/lane-check"); got != "" {
		t.Fatalf("private refs left behind: %q", got)
	}
}

func TestOpenAllClean(t *testing.T) {
	fx, _ := newOpenFixture(t, map[int]map[string]string{
		1: {"f.txt": lines100(map[int]string{10: "one ten"})},
		2: {"f.txt": lines100(map[int]string{90: "two ninety"})},
		3: {"other.txt": "three\n"},
	})
	code, out, errs := run(t, fx.work, "open")
	if code != 0 || out != "1 x 2 CLEAN\n" {
		t.Fatalf("exit %d stdout %q stderr %q", code, out, errs)
	}
}

// Every failure on the open path is exit 2 with no verdict line, and still leaves no private ref.
func TestOpenErrors(t *testing.T) {
	fx, oids := newOpenFixture(t, threeHeads())
	row := func(n int, oid string) string {
		return fmt.Sprintf(`{"number":%d,"headRefName":"x","headRefOid":"%s"}`, n, oid)
	}
	for _, tc := range []struct {
		name, json string
		args       []string
		want       string
	}{
		{"moved head", "[" + row(1, oids[2]) + "]", []string{"open"}, "PR 1 head is " + oids[1]},
		{"unknown PR", "[" + row(1, oids[1]) + "," + row(9, oids[2]) + "]", []string{"open"}, "git fetch origin (PR heads)"},
		{"not JSON", "oops", []string{"open"}, "gh pr list: bad JSON"},
		{"short oid", "[" + row(1, "abc") + "]", []string{"open"}, "PR 1 has no valid headRefOid"},
		{"duplicate", "[" + row(1, oids[1]) + "," + row(1, oids[1]) + "]", []string{"open"}, "PR 1 listed twice"},
		{"bad repo", "[]", []string{"open", "--repo", "-x"}, `bad --repo "-x"`},
		{"bad remote", "[" + row(1, oids[1]) + "]", []string{"open", "--remote", "nosuchremote"}, "git fetch nosuchremote"},
		{"bad base", "[]", []string{"open", "--base", "nobase"}, `--base: bad ref "nobase"`},
		{"plan bad ref", "[]", []string{"plan", "nosuchref", "--against-open"}, `bad ref "nosuchref"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx.list(t, tc.json)
			code, out, errs := run(t, fx.work, tc.args...)
			if code != 2 || out != "" || !strings.Contains(errs, tc.want) {
				t.Fatalf("exit %d stdout %q stderr %q, want exit 2 and %q", code, out, errs, tc.want)
			}
			if got := git(t, fx.work, "for-each-ref", "refs/lane-check"); got != "" {
				t.Fatalf("private refs left behind: %q", got)
			}
		})
	}
}

func TestOpenGhFails(t *testing.T) {
	fx, _ := newOpenFixture(t, threeHeads())
	stubOnPath(t, "gh", "echo 'HTTP 502: bad gateway' >&2\nexit 1\n")
	code, out, errs := run(t, fx.work, "open")
	if code != 2 || out != "" || !strings.Contains(errs, "gh pr list: exit 1: HTTP 502: bad gateway") {
		t.Fatalf("exit %d stdout %q stderr %q", code, out, errs)
	}
}

func TestPlanAgainstOpen(t *testing.T) {
	fx, oids := newOpenFixture(t, threeHeads())
	// the task branch edits line 10 too: it conflicts with PR 1 and 2, not with 3
	git(t, fx.work, "checkout", "-q", "-b", "task")
	write(t, fx.work, "f.txt", lines100(map[int]string{10: "task ten"}))
	git(t, fx.work, "commit", "-q", "-am", "task")
	before := fx.state(t)
	code, out, errs := run(t, fx.work, "plan", "HEAD", "--against-open")
	if code != 3 {
		t.Fatalf("exit %d, want 3; stderr %s", code, errs)
	}
	want := "HEAD x 1 CONFLICT f.txt\nHEAD x 2 CONFLICT f.txt\nHEAD x 3 CLEAN\n"
	if out != want {
		t.Fatalf("output %q, want %q", out, want)
	}
	if after := fx.state(t); after != before {
		t.Fatalf("checkout changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if code, out, _ := run(t, fx.work, "plan", "--against-open", "--file", "other.txt", "HEAD"); code != 0 || out != "" {
		t.Fatalf("--file other.txt: exit %d stdout %q", code, out)
	}
	// a PR whose head is the ref itself (the task's own PR) is not a pair
	if code, out, _ := run(t, fx.work, "plan", oids[3], "--against-open"); code != 0 || out != oids[3]+" x 1 CLEAN\n"+oids[3]+" x 2 CLEAN\n" {
		t.Fatalf("own PR: exit %d stdout %q", code, out)
	}
}

// A head that already conflicts with the base on its own is named on stderr; the verdict and exit code stay.
func TestStaleHeadNote(t *testing.T) {
	fx, _ := newOpenFixture(t, map[int]map[string]string{
		1: {"f.txt": lines100(map[int]string{10: "one ten"})},
		2: {"f.txt": lines100(map[int]string{90: "two ninety"})},
	})
	// main moves on line 10 after PR 1 and 2 were cut; the task starts from the new main
	write(t, fx.work, "f.txt", lines100(map[int]string{10: "main ten"}))
	git(t, fx.work, "commit", "-q", "-am", "main moves")
	git(t, fx.work, "update-ref", "refs/remotes/origin/main", "HEAD")
	git(t, fx.work, "checkout", "-q", "-b", "task")
	write(t, fx.work, "f.txt", lines100(map[int]string{10: "main ten", 50: "task fifty"}))
	git(t, fx.work, "commit", "-q", "-am", "task")
	code, out, errs := run(t, fx.work, "plan", "task", "--against-open")
	if code != 3 || out != "task x 1 CONFLICT f.txt\ntask x 2 CLEAN\n" {
		t.Fatalf("exit %d stdout %q stderr %q", code, out, errs)
	}
	if !strings.Contains(errs, "note: 1 already conflicts with origin/main on its own (f.txt)") {
		t.Fatalf("stale note for PR 1 missing: %q", errs)
	}
	if strings.Contains(errs, "note: task already") || strings.Contains(errs, "note: 2 already") {
		t.Fatalf("note on a head that merges with the base: %q", errs)
	}
	code, out, errs = run(t, fx.work, "open")
	if code != 0 || out != "1 x 2 CLEAN\n" || strings.Contains(errs, "note:") {
		t.Fatalf("open: exit %d stdout %q stderr %q", code, out, errs)
	}
}

func TestParsePRList(t *testing.T) {
	oid := strings.Repeat("a", 40)
	for _, tc := range []struct {
		name, in string
		n        int
		bad      bool
	}{
		{"empty list", "[]", 0, false},
		{"two", `[{"number":7,"headRefName":"b","headRefOid":"` + oid + `"},{"number":2,"headRefName":"a","headRefOid":"` + oid + `"}]`, 2, false},
		{"sha256", `[{"number":1,"headRefOid":"` + strings.Repeat("b", 64) + `"}]`, 1, false},
		{"zero number", `[{"number":0,"headRefOid":"` + oid + `"}]`, 0, true},
		{"upper hex", `[{"number":1,"headRefOid":"` + strings.Repeat("A", 40) + `"}]`, 0, true},
		{"object", `{}`, 0, true},
		{"empty", ``, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prs, err := ParsePRList([]byte(tc.in))
			if (err != nil) != tc.bad || len(prs) != tc.n {
				t.Fatalf("got %v, %v", prs, err)
			}
			for i := 1; i < len(prs); i++ {
				if prs[i-1].Number >= prs[i].Number {
					t.Fatalf("not sorted: %v", prs)
				}
			}
		})
	}
}

func FuzzParsePRList(f *testing.F) {
	f.Add([]byte(`[{"number":1,"headRefName":"a","headRefOid":"` + strings.Repeat("a", 40) + `"}]`))
	f.Add([]byte(`[{"number":-1}]`))
	f.Add([]byte(`null`))
	f.Fuzz(func(t *testing.T, data []byte) {
		prs, err := ParsePRList(data)
		if err != nil {
			return
		}
		seen := map[int]bool{}
		for _, p := range prs {
			if p.Number <= 0 || !oidRe.MatchString(p.OID) || seen[p.Number] {
				t.Fatalf("accepted a bad row: %+v", p)
			}
			seen[p.Number] = true
		}
	})
}
