package guard

import (
	"strings"
	"testing"
)

// T-0881 / T-0993 (tests/test_worker_guard_nopush_gh_api_release.py): the Python DENIED / ALLOWED / CODER_CMDS lists
// verbatim, plus boundary cases of the hand-coded (?<![\w-]) lookbehinds.

var (
	ghNoPushRoles = []string{"qa", "reviewer", "pm", "staff-engineer", "scout", "evaluator"}
	ghDenied      = []string{
		"gh api repos/o/r/issues -f title=x",
		"gh api repos/o/r/pulls/1/merge -F merge_method=squash",
		"gh api x --raw-field a=b",
		"gh api x --field a=b",
		"gh api x --input body.json",
		"gh release create v1",
		"gh release edit v1",
		"gh release delete v1",
		"gh release upload v1 f",
		"gh release delete-asset v1 f",
		"gh release create -R acme/new v1 --notes x",
		`python3 -c 'import subprocess; subprocess.run(["gh","release","create","v1"])'`,
		`python3 -c 'import os; os.system("gh api repos/o/r/issues -f title=x")'`,
		`python3 -c 'import os; os.system("gh api x --input body.json")'`,
		"bash -c 'gh release create v1'",
		"bash -c 'gh api x -F a=b'",
		`python3 -c 'import os; os.system("gh api search -X GET; gh api repos/o/r/issues -f title=x")'`,
		// beyond the Python list: gh global flags, a later gh word, a -X GET that only looks like one
		"gh -R a/b api x -f a=b",
		"gh api x -X POST -f a=b",
		"gh api -XGET x -X POST -f a=b", // the last method wins
		`python3 -c 'import os; os.system("gh -R a/b api x -f a=b")'`,
		`python3 -c 'import os; os.system("gh api x -f a=b -XGETX")'`,          // \b after GET
		`python3 -c 'import os; os.system("gh api x -f a=b --no-method GET")'`, // (?<![\w-]) before --method
		`python3 -c 'print("gh release  edit v1")'`,
		`perl -e 'system("gh api x -F a=b; gh api y -X GET")'`,
		`python3 -c 'import os; os.system("gh -R gh api x -f a=b")'`, // a gh word that is a flag value
		`python3 -c 'print("gh api x -f")'`,                          // -f at the end of the text
		`python3 -c 'print("  gh release delete-asset v1 f")'`,
	}
	ghAllowed = []string{
		"gh api repos/o/r/pulls/1",
		"gh api -X GET search/issues -f q=x",
		"gh api --method GET x -F per_page=5",
		"gh release list",
		"gh release view v1",
		"gh pr view 1",
		`python3 -c 'import os; os.system("gh release list")'`,
		`python3 -c 'import os; os.system("gh api -X GET search/issues -f q=x")'`,
		"bash -c 'gh release view v1'",
		"gh api --method=GET x -F a=b",
		"gh api -XGET x -f a=b",
		`python3 -c 'import os; os.system("gh api x --method GET -F a=b")'`,
		// beyond the Python list
		"gh api x --method get -f a=b",
		`python3 -c 'print("xgh api x -f a=b")'`, // not the gh word
		`python3 -c 'print("gh_ api x -f a=b")'`,
		`python3 -c 'print("gh api x é-f a=b")'`,      // é is \w: no lookbehind match
		`python3 -c 'print("gh api x -fa=b")'`,        // -f needs a (?![\w-]) after
		`python3 -c 'print("gh api x --force")'`,      // not a body flag
		`python3 -c 'print("gh api x --not-f")'`,      // (?<![\w-]) before -f
		`python3 -c 'print("gh release createx v1")'`, // \b after create
	}
	ghCoderCmds = []string{
		"gh api repos/o/r/issues -f title=x", "gh release create v1",
		"gh release create -R acme/new v1 --notes x", "bash -c 'gh release create v1'",
		`python3 -c 'import os; os.system("gh release create v1")'`,
	}
)

func TestNoPushGhAPIReleaseDenied(t *testing.T) {
	clearTermsEnv(t)
	for _, role := range ghNoPushRoles {
		for _, cmd := range ghDenied {
			err := Check(cmd, t.TempDir(), role, false)
			if exitOf(err) != 2 {
				t.Errorf("%s: %q not refused", role, cmd)
			}
		}
	}
}

func TestNoPushGhAPIReleaseAllowed(t *testing.T) {
	clearTermsEnv(t)
	for _, role := range ghNoPushRoles {
		for _, cmd := range ghAllowed {
			if err := Check(cmd, t.TempDir(), role, false); err != nil {
				t.Errorf("%s: %q refused: %v", role, cmd, err)
			}
		}
	}
}

func TestNoPushGhAPIReleaseMessages(t *testing.T) {
	clearTermsEnv(t)
	err := Check(ghDenied[0], t.TempDir(), "qa", false)
	if err == nil || !strings.Contains(err.Error(), "gh api with -f") {
		t.Errorf("api message: %v", err)
	}
	err = Check("gh release create v1", t.TempDir(), "qa", false)
	if err == nil || !strings.Contains(err.Error(), "gh release create") {
		t.Errorf("release message: %v", err)
	}
}

func TestNoPushGhAPIReleaseOtherRolesUnchanged(t *testing.T) {
	clearTermsEnv(t)
	for _, role := range []string{"coder", "cleaner", "judge"} {
		for _, cmd := range ghCoderCmds {
			if err := Check(cmd, t.TempDir(), role, false); err != nil {
				t.Errorf("%s: %q: %v", role, cmd, err)
			}
		}
	}
	for _, role := range []string{"boss", "director"} {
		for _, cmd := range ghCoderCmds {
			if err := Check(cmd, t.TempDir(), role, true); err != nil {
				t.Errorf("hq %s: %q: %v", role, cmd, err)
			}
		}
	}
}
