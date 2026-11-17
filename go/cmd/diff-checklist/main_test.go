package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func item(id, title, trigger string) string {
	return `{"id":"` + id + `","title":"` + title + `","check":"check ` + id + `","sources":["PR#1","PR#2"],"trigger":` + trigger + `}`
}

func items(mode string, its ...string) string {
	return `{"version":1,"mode":"` + mode + `","items":[` + strings.Join(its, ",") + "]}\n"
}

func sh(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false"}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newRepo makes a repo whose origin/main holds baseItems (when non-empty).
func newRepo(t *testing.T, baseItems string) string {
	t.Helper()
	dir := t.TempDir()
	sh(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "README.md", "hi\n")
	if baseItems != "" {
		write(t, dir, "ci/diff-checklist.json", baseItems)
	}
	sh(t, dir, "add", "-A")
	sh(t, dir, "commit", "-q", "-m", "base")
	sh(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
	return dir
}

func commit(t *testing.T, dir string) {
	t.Helper()
	sh(t, dir, "add", "-A")
	sh(t, dir, "commit", "-q", "-m", "work")
}

func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestRunGoldenBlock(t *testing.T) {
	dir := newRepo(t, items("observe",
		item("CL-01", "Hook rules", `{"paths":["roles/**"]}`),
		item("CL-02", "No sh", `{"paths":["go/**"],"added":["sh\", \"-c"]}`)))
	sha := sh(t, dir, "rev-parse", "--short", "origin/main")
	write(t, dir, "roles/hooks/g", "x\n")
	commit(t, dir)
	code, out, errs := runCLI(t, "run", "--repo-dir", dir)
	want := "<details><summary>Diff checklist</summary>\n\n## Diff checklist\nitems: origin/main@" + sha +
		" · mode: observe · matched 1 of 2\n- CL-01 Hook rules | check: check CL-01 | result: TODO\n</details>\n"
	if code != 0 || out != want {
		t.Fatalf("code %d stderr %q\ngot:\n%q\nwant:\n%q", code, errs, out, want)
	}
}

func TestRunNoneMatched(t *testing.T) {
	dir := newRepo(t, items("enforce", item("CL-01", "T", `{"paths":["roles/**"]}`)))
	write(t, dir, "docs/a.md", "x\n")
	commit(t, dir)
	code, out, _ := runCLI(t, "run", "--repo-dir", dir)
	if code != 0 || !strings.Contains(out, "- none matched\n") || !strings.Contains(out, "matched 0 of 1") {
		t.Fatalf("code %d out %q", code, out)
	}
}

// Items come from the ref only: a HEAD that deletes CL-01 from the file still sees it,
// and an item added only in the worktree file is not listed.
func TestRunReadsItemsFromRefOnly(t *testing.T) {
	dir := newRepo(t, items("enforce", item("CL-01", "Base item", `{"paths":["roles/**"]}`)))
	write(t, dir, "ci/diff-checklist.json", items("enforce", item("CL-09", "Sneaky", `{"paths":["roles/**"]}`)))
	write(t, dir, "roles/x.md", "x\n")
	commit(t, dir)
	code, out, errs := runCLI(t, "run", "--repo-dir", dir)
	if code != 0 || !strings.Contains(out, "- CL-01 Base item") {
		t.Fatalf("CL-01 missing: code %d out %q err %q", code, out, errs)
	}
	if strings.Contains(out, "CL-09") {
		t.Fatalf("CL-09 from the worktree file must not be listed: %q", out)
	}
	// An uncommitted worktree edit is not read either.
	write(t, dir, "ci/diff-checklist.json", items("enforce", item("CL-08", "Dirty", `{"paths":["roles/**"]}`)))
	_, out, _ = runCLI(t, "run", "--repo-dir", dir)
	if strings.Contains(out, "CL-08") || !strings.Contains(out, "CL-01") {
		t.Fatalf("worktree copy was read: %q", out)
	}
}

func TestRunItemsRefOverride(t *testing.T) {
	dir := newRepo(t, items("observe", item("CL-01", "Base", `{"paths":["roles/**"]}`)))
	write(t, dir, "ci/diff-checklist.json", items("observe", item("CL-02", "Head", `{"paths":["roles/**"]}`)))
	write(t, dir, "roles/x.md", "x\n")
	commit(t, dir)
	_, out, _ := runCLI(t, "run", "--repo-dir", dir, "--items-ref", "HEAD")
	if !strings.Contains(out, "CL-02") || strings.Contains(out, "CL-01") {
		t.Fatalf("out %q", out)
	}
}

func TestRunNoItemsFile(t *testing.T) {
	dir := newRepo(t, "")
	write(t, dir, "ci/diff-checklist.json", items("observe", item("CL-01", "T", `{"paths":["a"]}`)))
	commit(t, dir)
	code, out, errs := runCLI(t, "run", "--repo-dir", dir, "--base", "origin/main")
	want := "diff-checklist: no ci/diff-checklist.json on origin/main: nothing to run\n"
	if code != 0 || out != want {
		t.Fatalf("code %d out %q err %q", code, out, errs)
	}
}

func TestRunFailsClosed(t *testing.T) {
	t.Run("invalid items on ref", func(t *testing.T) {
		dir := newRepo(t, `{"version":1,"mode":"observe","items":[{"id":"CL-01"}]}`)
		code, _, errs := runCLI(t, "run", "--repo-dir", dir)
		if code != 2 || !strings.Contains(errs, "invalid ci/diff-checklist.json on origin/main") {
			t.Fatalf("code %d err %q", code, errs)
		}
	})
	t.Run("missing ref", func(t *testing.T) {
		dir := newRepo(t, items("observe"))
		code, _, errs := runCLI(t, "run", "--repo-dir", dir, "--base", "nope/ref")
		if code != 2 || !strings.Contains(errs, "nope/ref") {
			t.Fatalf("code %d err %q", code, errs)
		}
	})
	t.Run("not a repo", func(t *testing.T) {
		code, _, _ := runCLI(t, "run", "--repo-dir", t.TempDir())
		if code != 2 {
			t.Fatalf("code %d", code)
		}
	})
	t.Run("option-like ref", func(t *testing.T) {
		dir := newRepo(t, items("observe"))
		code, _, _ := runCLI(t, "run", "--repo-dir", dir, "--base", "--output=x")
		if code != 2 {
			t.Fatalf("code %d", code)
		}
	})
}

func TestRunDiffOverLimit(t *testing.T) {
	dir := newRepo(t, items("observe", item("CL-01", "T", `{"paths":["big/**"]}`)))
	write(t, dir, "big/f.txt", strings.Repeat("0123456789abcdef\n", 200))
	commit(t, dir)
	old := maxDiff
	maxDiff = 1000
	defer func() { maxDiff = old }()
	code, _, errs := runCLI(t, "run", "--repo-dir", dir)
	if code != 2 || !strings.Contains(errs, "over the") || !strings.Contains(errs, "limit") {
		t.Fatalf("code %d err %q", code, errs)
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"bogus"}, {"body"}, {"validate"}, {"run", "extra"}, {"run", "--nope"}} {
		if code, _, _ := runCLI(t, args...); code != 2 {
			t.Errorf("%v: code %d want 2", args, code)
		}
	}
}

// ghStub puts a fake gh on PATH serving diff and body from files.
func ghStub(t *testing.T, diff, body string) string {
	t.Helper()
	d := t.TempDir()
	write(t, d, "diff", diff)
	view, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		t.Fatal(err)
	}
	write(t, d, "view.json", string(view))
	script := "#!/bin/sh\nd=\"$GH_STUB_DIR\"\n" +
		"echo \"$@\" >> \"$d/calls\"\n" +
		"[ -f \"$d/fail\" ] && { echo 'gh: boom' >&2; exit 1; }\n" +
		"case \"$2\" in\n" +
		"diff) [ -f \"$d/big\" ] && { head -c 22000000 /dev/zero | tr '\\0' a; exit 0; }; cat \"$d/diff\";;\n" +
		"view) cat \"$d/view.json\";;\n" +
		"esac\n"
	bin := t.TempDir()
	write(t, bin, "gh", script)
	if err := os.Chmod(filepath.Join(bin, "gh"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GH_STUB_DIR", d)
	return d
}

const prDiff = "diff --git a/roles/x.md b/roles/x.md\n--- a/roles/x.md\n+++ b/roles/x.md\n@@ -0,0 +1 @@\n+x\n"

func blockWith(results ...string) string {
	return "## Summary\nx\n<details><summary>Diff checklist</summary>\n\n## Diff checklist\nitems: origin/main@abc · mode: m · matched 2 of 3\n" +
		strings.Join(results, "\n") + "\n</details>\n"
}

func bodyRepo(t *testing.T, mode string) string {
	return newRepo(t, items(mode,
		item("CL-01", "One", `{"paths":["roles/**"]}`),
		item("CL-02", "Two", `{"paths":["roles/**"]}`),
		item("CL-03", "Three", `{"paths":["docs/**"]}`)))
}

func TestBodyAllPass(t *testing.T) {
	dir := bodyRepo(t, "enforce")
	ghStub(t, prDiff, blockWith("- CL-01 One | check: c | result: PASS ran it", "- CL-02 Two | check: c | result: N/A not touched"))
	code, out, errs := runCLI(t, "body", "--pr", "7", "--repo", "o/r", "--repo-dir", dir)
	want := "OK CL-01\nOK CL-02\ndiff-checklist: matched 2 of 3; problems 0; mode enforce\n"
	if code != 0 || out != want {
		t.Fatalf("code %d err %q\ngot %q\nwant %q", code, errs, out, want)
	}
}

func TestBodyTodoEnforceAndObserve(t *testing.T) {
	body := blockWith("- CL-01 One | check: c | result: PASS ok", "- CL-02 Two | check: c | result: TODO")
	dir := bodyRepo(t, "enforce")
	ghStub(t, prDiff, body)
	code, out, _ := runCLI(t, "body", "--pr", "7", "--repo-dir", dir)
	if code != 1 || !strings.Contains(out, "TODO CL-02") || !strings.Contains(out, "problems 1; mode enforce") || strings.Contains(out, "observe:") {
		t.Fatalf("enforce: code %d out %q", code, out)
	}
	dir = bodyRepo(t, "observe")
	ghStub(t, prDiff, body)
	code, out, _ = runCLI(t, "body", "--pr", "7", "--repo-dir", dir)
	if code != 0 || !strings.Contains(out, "observe: would block: CL-02\n") || !strings.Contains(out, "mode observe") {
		t.Fatalf("observe: code %d out %q", code, out)
	}
}

func TestBodyProblemKinds(t *testing.T) {
	dir := bodyRepo(t, "enforce")
	ghStub(t, prDiff, blockWith("- CL-01 One | check: c | result: N/A"))
	code, out, _ := runCLI(t, "body", "--pr", "7", "--repo-dir", dir)
	if code != 1 || !strings.Contains(out, "TODO CL-01: N/A needs a reason") || !strings.Contains(out, "MISSING CL-02") {
		t.Fatalf("code %d out %q", code, out)
	}
	ghStub(t, prDiff, blockWith("- CL-01 One | check: c | result: FAIL broke x", "- CL-02 Two | check: c | result: PASS y"))
	code, out, _ = runCLI(t, "body", "--pr", "7", "--repo-dir", dir)
	if code != 1 || !strings.Contains(out, "FAIL CL-01: broke x") {
		t.Fatalf("code %d out %q", code, out)
	}
}

func TestBodyUsesTwoGhCallsWithRepo(t *testing.T) {
	dir := bodyRepo(t, "enforce")
	d := ghStub(t, prDiff, blockWith("- CL-01 One | check: c | result: PASS a", "- CL-02 Two | check: c | result: PASS b"))
	if code, _, errs := runCLI(t, "body", "--pr", "7", "--repo", "o/r", "--repo-dir", dir); code != 0 {
		t.Fatalf("code %d %s", code, errs)
	}
	calls, err := os.ReadFile(filepath.Join(d, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(calls), "pr diff 7 -R o/r\npr view 7 -R o/r --json body\n"; got != want {
		t.Fatalf("calls %q want %q", got, want)
	}
}

func TestBodyFailsClosed(t *testing.T) {
	dir := bodyRepo(t, "observe")
	d := ghStub(t, prDiff, "")
	write(t, d, "fail", "1")
	if code, _, errs := runCLI(t, "body", "--pr", "7", "--repo-dir", dir); code != 2 || !strings.Contains(errs, "gh") {
		t.Fatalf("gh failure: code %d err %q", code, errs)
	}
	bad := newRepo(t, `{"version":9}`)
	ghStub(t, prDiff, "")
	if code, _, _ := runCLI(t, "body", "--pr", "7", "--repo-dir", bad); code != 2 {
		t.Fatalf("invalid items: code %d", code)
	}
}

func TestBodyNoItemsFile(t *testing.T) {
	dir := newRepo(t, "")
	ghStub(t, prDiff, "")
	code, out, _ := runCLI(t, "body", "--pr", "7", "--repo-dir", dir)
	if code != 0 || out != "diff-checklist: no ci/diff-checklist.json on origin/main: nothing to run\n" {
		t.Fatalf("code %d out %q", code, out)
	}
}

func TestBodyDiffOverLimit(t *testing.T) {
	dir := bodyRepo(t, "enforce")
	d := ghStub(t, prDiff, "")
	write(t, d, "big", "1")
	code, _, errs := runCLI(t, "body", "--pr", "7", "--repo-dir", dir)
	if code != 2 || !strings.Contains(errs, "20 MiB") {
		t.Fatalf("code %d err %q", code, errs)
	}
}

func TestValidate(t *testing.T) {
	dir := t.TempDir()
	good := items("observe", item("CL-01", "T", `{"paths":["a/**"],"added":["x+"]}`))
	write(t, dir, "good.json", good)
	if code, out, _ := runCLI(t, "validate", "--file", filepath.Join(dir, "good.json")); code != 0 || !strings.Contains(out, "ok") {
		t.Fatalf("good: code %d out %q", code, out)
	}
	bad := map[string]struct{ content, want string }{
		"dup":    {items("observe", item("CL-01", "T", `{"paths":["a"]}`), item("CL-01", "U", `{"paths":["b"]}`)), "item CL-01: field id"},
		"unk":    {`{"version":1,"mode":"observe","items":[],"extra":1}`, "unknown key"},
		"regex":  {items("observe", item("CL-02", "T", `{"added":["("]}`)), "item CL-02: field trigger.added[0]"},
		"source": {`{"version":1,"mode":"observe","items":[{"id":"CL-03","title":"t","check":"c","sources":["PR#1"],"trigger":{"paths":["a"]}}]}`, "item CL-03: field sources"},
	}
	for name, b := range bad {
		write(t, dir, name+".json", b.content)
		code, _, errs := runCLI(t, "validate", "--file", filepath.Join(dir, name+".json"))
		if code != 1 || !strings.Contains(errs, b.want) {
			t.Errorf("%s: code %d err %q want %q", name, code, errs, b.want)
		}
	}
	if code, _, _ := runCLI(t, "validate", "--file", filepath.Join(dir, "missing.json")); code != 2 {
		t.Errorf("missing file: code %d want 2", code)
	}
}
