package guard

import (
	"strings"
	"testing"
)

// T-0789 / T-0885 (tests/test_worker_guard_nopush_interp_inline.py): the DENIED / ALLOWED tables are the Python
// ones verbatim, plus case-sensitivity and gh-regex edge cases.

const (
	inlPyPush = `import subprocess; subprocess.run(["git","push"])`
	inlPySys  = `import os; os.system("git push origin main")`
	inlPyPost = `import os; os.system("gh api -X POST repos/x/y/merges")`
	inlNodePs = `require("child_process").execSync("git push")`
)

var inlineDenied = []string{
	"python3 -c '" + inlPyPush + "'",
	"python3 -c '" + inlPySys + "'",
	"python -c '" + inlPyPush + "'",
	"python3.11 -c '" + inlPyPush + "'",
	"python3 -Ic '" + inlPyPush + "'",
	"python3 -I -c '" + inlPyPush + "'",
	"python3 -W ignore -c '" + inlPyPush + "'",
	`perl -e 'system("git","push")'`,
	`perl -E 'system("git push")'`,
	"node -e '" + inlNodePs + "'",
	"node --eval '" + inlNodePs + "'",
	"node -p '" + inlNodePs + "'",
	"nodejs -e '" + inlNodePs + "'",
	`ruby -e 'system("git push")'`,
	`python3 -c 'import os; os.system("gh pr merge 1")'`,
	`python3 -c 'import os; os.system("gh pr create --fill")'`,
	"python3 -c '" + inlPyPost + "'",
	`python3 -c 'import os; os.system("gh api --method=DELETE repos/x/y")'`,
	`python3 -c 'import subprocess; subprocess.run(["gh","api","-X","PUT","repos/x/y"])'`,
	`python3 -c 'import os; os.system("gh repo delete x")'`,
	`python3 -c 'import os; os.system("gh repo view x")'`,
	`bash -c "python3 -c 'import os; os.system(\"git push\")'"`,
	`sh -c "perl -e 'system(q(git push))'"`,
	"cd /tmp && python3 -c '" + inlPyPush + "'",
	"env python3 -c '" + inlPyPush + "'",
	`perl -e'system(q(git push))'`,
	`ruby -e"system(q(git push))"`,
	"node --eval='" + inlNodePs + "'",
	"node --print='" + inlNodePs + "'",
	`python3 -c'import os; os.system("git push")'`,
	`python3 -c 'import os; os.system("gh -R a/b pr merge 1")'`,
	`python3 -c 'import os; os.system("gh --repo a/b pr create --fill")'`,
	`python3 -c 'import os; os.system("gh api x -X GET; gh api y -X POST")'`,
	`python3 -c "print('git push')"`,
	`python3 -c 'import os; os.system("gh api repos/x -X post")'`, // method is case-insensitive
	`python3 -c 'import os; os.system("gh api repos/x --method patch")'`,
	`python3 -c 'import os; os.system("gh -R a/b -q .x api repos/x -X PUT")'`, // flag with a value, then api
}

var inlineAllowed = []string{
	`python3 -c "import json"`,
	`python3 -c 'import subprocess; subprocess.run(["git","log","-1"])'`,
	`python3 -c "print('push')"`,
	`python3 -c "print('git status')"`,
	`node -e 'console.log(1)'`,
	`perl -e 'print 1'`,
	`ruby -e 'puts 1'`,
	`python3 -c 'import os; os.system("gh pr view 1")'`,
	`python3 -c 'import os; os.system("gh api repos/x/y/pulls")'`,
	`python3 -c 'import os; os.system("gh api -X GET repos/x/y/pulls")'`,
	`python3 -c 'print("-X POST")'`,
	`perl -e'print 1'`,
	`python3 -c 'import os; os.system("gh -R a/b pr view 1")'`,
	`python3 script.py`,
	`python3 script.py --arg 'git push'`,
	`bash -c "python3 -c 'import json'"`,
	`python3 -c 'print("Git Push")'`, // word tokens are case-sensitive
	`python3 -c 'print("GIT PUSH")'`,
	`python3 -c 'print("gitpush", "pushgit", "git_push")'`, // not word tokens
	`python3 -c 'print("ghx pr merge 1")'`,                 // not the gh word
	`python3 -c 'print("gh pr view; MERGE")'`,
	`python3 -c 'print("gh api x -fX POST")'`,  // (?<!\w) before -X
	`python3 -c 'print("gh api x -X POSTAL")'`, // \b after the method
}

func TestNoPushInterpInlineDenied(t *testing.T) {
	clearTermsEnv(t)
	for _, role := range []string{"qa", "reviewer", "pm", "staff-engineer", "scout", "evaluator"} {
		for _, cmd := range inlineDenied {
			if exitOf(Check(cmd, t.TempDir(), role, false)) != 2 {
				t.Errorf("%s: %q not refused", role, cmd)
			}
		}
	}
}

func TestNoPushInterpInlineAllowed(t *testing.T) {
	clearTermsEnv(t)
	for _, role := range []string{"qa", "reviewer", "pm", "staff-engineer", "scout", "evaluator"} {
		for _, cmd := range inlineAllowed {
			if err := Check(cmd, t.TempDir(), role, false); err != nil {
				t.Errorf("%s: %q refused: %v", role, cmd, err)
			}
		}
	}
}

func TestNoPushInterpInlineMessages(t *testing.T) {
	clearTermsEnv(t)
	err := Check("python3 -c '"+inlPyPush+"'", t.TempDir(), "qa", false)
	if err == nil || !strings.Contains(err.Error(), "python3 inline code that runs `git push` (no-push roles never push; ask your lead)") {
		t.Errorf("git push message: %v", err)
	}
	err = Check("python3 -c '"+inlPyPost+"'", t.TempDir(), "qa", false)
	want := "python3 inline code that runs a gh write (pr merge/create, repo, api with a write method or body, release write); no-push roles never write to GitHub; ask your lead"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("gh write message: %v", err)
	}
}

func TestNoPushInterpInlineOtherRolesUnchanged(t *testing.T) {
	clearTermsEnv(t)
	for _, role := range []string{"coder", "cleaner", "judge"} {
		for _, cmd := range []string{"python3 -c '" + inlPySys + "'", `perl -e 'system("gh pr view 1")'`, `python3 -c 'import json'`} {
			if err := Check(cmd, t.TempDir(), role, false); err != nil {
				t.Errorf("%s: %q: %v", role, cmd, err)
			}
		}
	}
	for _, role := range []string{"boss", "director"} {
		for _, cmd := range []string{"python3 -c '" + inlPyPush + "'", `ruby -e 'system("git push")'`, "python3 -c '" + inlPyPost + "'"} {
			if err := Check(cmd, t.TempDir(), role, true); err != nil {
				t.Errorf("hq %s: %q: %v", role, cmd, err)
			}
		}
	}
}

func TestMatchInlineGhWrite(t *testing.T) {
	for s, want := range map[string]bool{
		"gh pr merge 1":                  true,
		"gh pr new":                      true,
		"gh issue create":                true,
		"gh issue new":                   true,
		"gh workflow run x.yml":          true,
		"gh label create bug":            true,
		"gh secret set X":                true,
		"gh variable set X":              true,
		"gh pr  create":                  true,
		"gh -R a/b pr merge":             true,
		"gh --repo=a/b pr create":        true,
		"gh repo view":                   true,
		"gh api x -X POST":               true,
		"gh api x --method=delete":       true,
		"gh api x -X Put":                true,
		"gh pr view 1":                   false,
		"gh api x":                       false,
		"gh api x -X GET":                false,
		"gh api x -fX POST":              false,
		"gh api x -X POSTS":              false,
		"ghx pr merge":                   false,
		"xgh pr merge":                   false,
		"gh pr mergeable":                false,
		"gh pr merge-all":                true, // \b between e and -
		"gh":                             false,
		"gh -R":                          false,
		"gh -R a/b -q .x api y -X PATCH": true,
		"git push":                       false,
	} {
		if got := matchInlineGhWrite(s); got != want {
			t.Errorf("matchInlineGhWrite(%q) = %v, want %v", s, got, want)
		}
	}
}
