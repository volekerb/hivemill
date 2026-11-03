package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// T-0887: the rule itself (targets, text flags, body files, heredocs, cd/-C/--git-dir, push forms, gh api, frozen
// repos, HQ mode), not only the checker: every command goes through the Go binary (with a --py-hook that fails if
// started) and through the Python hook and must give the same exit code and stderr. The cases follow
// tests/test_worker_guard_public_text.py.

func TestPublicTextRuleMatchesPythonHook(t *testing.T) {
	hook := absHook(t)
	stub := failingHookLayout(t)
	repo := publicTextRepo(t, "https://github.com/o/pub.git")
	other := publicTextRepo(t, "https://github.com/o/private-proj.git")
	frozen := publicTextRepo(t, "git@"+"github.com:o/old.git")
	pushurl := publicTextRepo(t, "https://github.com/o/elsewhere.git")
	gitIn(t, pushurl, nil, "remote", "set-url", "--push", "origin", "https://github.com/o/pub.git")
	gitIn(t, repo, nil, "tag", "-a", "v-"+strings.Join([]string{"192", "168", "1", "4"}, "."), "-m", "m")
	home := seedDB(t, "CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT)",
		"INSERT INTO config VALUES ('public_text_repos', 'o/pub')", "INSERT INTO config VALUES ('frozen_repos', 'o/old')")
	env := withTerms(t, "FACTORY_HOME="+home)
	tmp := t.TempDir()
	file := func(name, text string) string {
		p := filepath.Join(tmp, name)
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	bad, ok, single := file("bad.md", "about secret"), file("ok.md", "Fixes T-0528, PR #3"), file("single.md", "Fixes T-0528")
	link, hard := filepath.Join(tmp, "lnk.md"), filepath.Join(tmp, "hard.md")
	if err := os.Symlink(ok, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(ok, hard); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(tmp, "fifo")
	if out, err := execCombined("mkfifo", fifo); err != nil {
		t.Fatalf("mkfifo: %v %s", err, out)
	}
	lan, ten := strings.Join([]string{"192", "168", "1", "4"}, "."), strings.Join([]string{"10", "0", "0", "5"}, ".")
	gh := "gh pr create --title t --body-file " + single
	type tc struct{ cmd, cwd, mode string }
	var cases []tc
	// worker mode for every command, HQ mode for every third one (and every command of the wrapper group below)
	n := 0
	add := func(cwd string, cmds ...string) {
		for _, c := range cmds {
			cases = append(cases, tc{c, cwd, "judge"})
			if n++; n%3 == 0 {
				cases = append(cases, tc{c, cwd, "hq"})
			}
		}
	}
	add(repo,
		"git commit -m 'T-0528: add rule'", "git commit -m 'about secret'", "git commit -am secret", "git commit -F "+bad, "git commit -F "+ok,
		"git commit -m 'see /ho"+"me/alice/notes'", "cd /ho"+"me/alice/x && git commit -m 'T-0528: add rule'",
		"gh pr create --title t --body 'about secret'", "gh pr create --title t --body-file "+bad, "gh pr create --title t --body-file "+ok,
		"gh pr create --title t --body-file "+tmp+"/missing.md", "gh pr create --title t --body-file "+link, "gh pr create --title t --body-file "+hard,
		"gh pr create --title t --body-file "+single, "gh pr create --title t --body-file "+single+" 2>/dev/null", "gh pr create --title t --body-file "+single+" 2>&1",
		"gh pr create --title t --body-file "+fifo, "gh pr create --title t --body-file /dev/null", "gh pr create --title t -F <(echo hi)",
		"gh pr create --title t --body \"$(cat <<'EOF'\nabout secret\nEOF\n)\"", "gh pr create --title t --body \"$(cat <<'EOF'\nabout the rule\nEOF\n)\"",
		"gh pr create --title t --body-file - <<'EOF'\nsecret\nEOF\n", "gh pr create --title t --body-file - <<'EOF'\nfine\nEOF\n",
		"gh pr create --title t --body-file /dev/stdin", "gh pr create --title t --body-file /dev/stdin <<'EOF'\nsecret\nEOF\n",
		"gh pr create --title t --body-file=/dev/fd/0 <<'EOF'\nsecret\nEOF\n", "gh pr create --title t -F /proc/self/fd/0 <<'EOF'\nsecret\nEOF\n",
		"gh pr create --title t --body-file /dev/fd/../fd/0 <<'EOF'\nsecret\nEOF\n", "gh pr create --title t --body-file //dev/stdin <<'EOF'\nsecret\nEOF\n",
		"gh release create v1 --notes-file /dev/fd/0 <<'EOF'\nsecret\nEOF\n", "git commit -F /dev/stdin <<'EOF'\nsecret\nEOF\n",
		"echo hi > "+single+" && "+gh, gh+" &", gh+" || true", "echo $("+gh+")", "sh -c '"+gh+"'", gh+"\necho done", "cd "+repo+" && "+gh,
		"cp "+bad+" "+single+"; "+gh, "perl -pi -e 's/a/b/' "+single+" && "+gh,
		"gh pr create --title t --body \"$X\"", "gh pr create --title t --body \"`date`\"", "gh pr create --title \"$(some_cmd)\" --body b",
		"gh api -X PATCH repos/o/pub/pulls/1 -f body=secret", "gh api -X PATCH repos/o/pub/pulls/1 -f body=fine", "gh api repos/o/pub/pulls/1",
		"gh api -X POST repos/o/pub/issues -F body=@"+bad, "gh api -X POST repos/o/pub/issues --input "+bad, "gh api -X POST repos/o/pub/issues --input "+ok,
		"gh api -X POST repos/o/pub/issues --field=body=secret", "gh api -X POST repos/o/pub/issues --input=/dev/stdin <<'EOF'\nsecret\nEOF\n",
		"gh api -X GET repos/o/pub/issues -f q=secret", "gh api user -X POST -f name=secret",
		"gh pr review 1 --comment --body secret", "gh pr review 1 --comment", "gh issue comment 1 -b secret", "gh issue comment 1 -bsecret",
		"gh release create v1 --notes secret", "gh release create v-"+ten+" --notes ok", "gh pr create --title t --body b --head factory/"+ten,
		"gh pr create --title t --body b --head 'o:factory/"+ten+"'", "gh pr create --title t --body b --head \"$B\"", "gh pr list", "gh pr view 1",
		"git tag -a v1 -m secret", "git tag v1", "git tag -a v-"+ten+" -m m", "git notes add -m secret", "git merge -m secret x", "git commit-tree -m secret HEAD^{tree}",
		"git -c alias.ci=commit ci --allow-empty -m secret", "git -calias.ci=commit ci -m x", "git -c core.pager=cat commit -m secret",
		"git --git-dir="+repo+"/.git commit --allow-empty -m secret", "git --git-dir "+repo+"/.git commit -m secret", "git --work-tree=. commit -m secret",
		"git push origin HEAD:factory/t-1", "git push origin HEAD:factory/"+ten, "git push origin bad:factory/t-1", "git push origin nosuchbranch:factory/x",
		"git push", "git push origin", "git push --all origin", "git push --tags origin", "git push --mirror origin", "git push origin --delete factory/"+ten, "git push origin --delete factory/t-1",
		"git push origin :factory/t-1", "git push -d origin factory/t-1", "git push origin +HEAD:factory/t-1", "git push origin HEAD:refs/heads/factory/t-1",
		"git push origin HEAD:refs/tags/v-"+lan, "git push origin factory/t-1 factory/t-9", "git push -o ci.skip origin HEAD:factory/t-1",
		"git push --repo=https://github.com/o/pub.git HEAD:factory/"+ten, "git push --repo https://github.com/o/pub.git HEAD:factory/"+ten,
		"git push https://github.com/o/pub.git HEAD:factory/"+ten, "git push git@"+"github.com:o/pub.git HEAD:factory/t-1", "git push https://github.com/o/other.git HEAD:factory/t-1",
		"git push ssh://git@"+"github.com/o/pub.git bad:x", "git push origin --force HEAD:factory/t-1", "git push origin HEAD:factory/t-1 --force-with-lease",
		"git push --porcelain origin HEAD:factory/t-1", "(git push origin HEAD:factory/t-1)", "git push origin HEAD:factory/t-1 && git status",
		"git status", "git log --oneline", "git diff", "ls -la", "echo hi")
	add(other, "gh pr comment 1 --body secret", "git push origin HEAD:factory/t-1", "gh pr comment 1 -R o/pub --body secret", "gh pr comment 1 -Ro/pub --body secret",
		"gh pr comment 1 --repo=o/pub --body secret", "gh pr comment 1 --repo o/pub --body fine", "gh pr comment 1 -R github.com/o/pub --body secret",
		"git -C "+repo+" commit --allow-empty -m secret", "git -C "+other+" commit --allow-empty -m secret", "git commit -m secret",
		"git --git-dir="+repo+"/.git commit --allow-empty -m secret", "(cd "+repo+"; true); git commit --allow-empty -m secret",
		"cd "+repo+" && git commit --allow-empty -m secret", "cd "+repo+"; git commit -m secret", "cd /nonexistent && git commit -m secret", "cd \"$X\" && git commit -m secret",
		"cd && git commit -m secret", "cd - && git commit -m secret", "cd .. && git commit -m secret", "git -C "+repo+" push origin HEAD:factory/"+ten,
		"git -C "+repo+" -C . commit -m secret", "git -C commit -m secret", "git -C \"$X\" commit -m secret", "git --git-dir=x status; git commit -m secret",
		"gh api -X PATCH repos/o/pub/pulls/1 -f body=secret", "gh api -X PATCH repos/o/private-proj/pulls/1 -f body=secret")
	add(frozen, "git push origin HEAD", "git commit -m x", "gh pr create -R o/old --title t --body b", "gh pr create --title t --body b", "gh api -X PATCH repos/o/old/pulls/1 -f body=x",
		"gh api repos/o/old/pulls/1", "git tag v1", "git tag -a v1 -m x", "git push origin --delete factory/t-1")
	add(pushurl, "git push origin HEAD:factory/t-1", "git commit -m 'host "+lan+"'", "git push origin bad:factory/t-1")
	wrappers := []string{"gh pr create -R o/old --title t --body b", "env X=1 gh pr create --title t --body secret", "nohup git commit -m secret", "timeout 5 git commit -m secret",
		"bash -c 'gh pr create --title t --body secret'", "sh -c \"git commit -m secret\"", "eval 'git commit -m secret'", "bash -c 'git status'", "bash -c 'echo $(gh pr create --title t --body secret)'",
		"bash -c 'g\"\"it commit -m secret'", "x=1 git commit -m secret", "xargs git commit -m secret", "gh pr create --title 'unbalanced --body b", "git commit -m 'unbalanced",
		"echo 'unbalanced", "git commit -m \"a\" \"b", "ls 'x"}
	for _, c := range wrappers {
		cases = append(cases, tc{c, repo, "judge"}, tc{c, repo, "hq"})
	}
	sem := make(chan struct{}, 3) // the Python hook is slow to start; three at a time keep the box usable
	var wg sync.WaitGroup
	for _, c := range cases {
		wg.Add(1)
		sem <- struct{}{}
		go func(c tc) {
			defer wg.Done()
			defer func() { <-sem }()
			checkAgainstPython(t, hook, stub, env, c.cmd, c.cwd, c.mode)
		}(c)
	}
	wg.Wait()
}

func checkAgainstPython(t *testing.T, hook, stub string, env []string, cmd, cwd, mode string) {
	t.Helper()
	{
		c := struct{ cmd, cwd, mode string }{cmd, cwd, mode}
		payload := []byte(`{"tool_name":"Bash","tool_input":{"command":` + jsonString(c.cmd) + `},"cwd":` + jsonString(c.cwd) + `}`)
		args, pargs := []string{"--role", c.mode, "--py-hook", stub}, []string{"--role", c.mode}
		if c.mode == "hq" {
			args, pargs = []string{"--hq", "--py-hook", stub}, []string{"--hq"}
		}
		code, stderr := call(t, payload, env, args...)
		pcode, pstderr := callPython(t, hook, payload, env, pargs...)
		if code != pcode || stderr != pstderr || strings.Contains(stderr, "failing closed") {
			t.Errorf("%s %q in %s: go exit %d %q, python exit %d %q", c.mode, c.cmd, filepath.Base(c.cwd), code, stderr, pcode, pstderr)
		}
	}
}
