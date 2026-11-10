package guard

import (
	"errors"
	"strings"
	"testing"
)

// T-1018: Go mirror of tests/test_worker_guard_uv_wrapper.py (T-0880). The cases and reasons are those of the Python
// hook; ci/guard_corpus.json and GUARD_IMPL=diff pin byte-identical stderr against it.

const uvPyPush = `import subprocess; subprocess.run(["git","push"])`

func uvBlockMsg(t *testing.T, cmd, role string) (string, bool) {
	t.Helper()
	err := Check(cmd, "", role, false)
	var b *Block
	if errors.As(err, &b) {
		return b.Msg, true
	}
	if err != nil {
		t.Fatalf("%s %q: unexpected non-block error %v", role, cmd, err)
	}
	return "", false
}

func TestUvWrapperDeniedForNoPushRoles(t *testing.T) {
	cmds := []string{
		"uv run git push origin main", "uv run -- git push", "uv run --with requests python3 -c '" + uvPyPush + "'",
		"uvx --from gh gh pr merge 1", "uv run --python 3.12 --directory /x git push", "uvx git push",
		"uv tool run gh pr create --fill", "uv --directory /x run git push", "uv run --env-file .env -p 3.12 gh pr merge 1",
		"uv run --with=x git push", "uv run --index-url http://x git push", "uv run --color never git push",
		"uv --color never run git push", "uvx --index-url http://x gh pr merge 1", "uv run --foo bar git push",
		"uv run --foo bar /usr/bin/git push", "uv run --foo bar git -C /tmp push", "uv run --foo bar gh pr merge 1",
		"cd /x && uv run git push origin main", "bash -c 'uv run git push origin main'",
		"uv run git config user.name x", "uv run herdr list", "uv run kill 1", "uv run git commit -m x",
		"uv run python3 x.py git push", "uv run pytest -k git push",
	}
	for _, role := range []string{"qa", "reviewer", "pm", "staff-engineer", "scout", "evaluator", "researcher"} {
		for _, c := range cmds {
			if _, blocked := uvBlockMsg(t, c, role); !blocked {
				t.Errorf("%s %q: allowed, want blocked", role, c)
			}
		}
	}
}

func TestUvWrapperReasonText(t *testing.T) {
	msg, _ := uvBlockMsg(t, "uv run --foo bar git push", "qa")
	if want := "uv running git push behind an unknown uv option; push only your own task branch with a plain `git push origin HEAD:factory/<task>`"; msg != want {
		t.Errorf("git push reason = %q", msg)
	}
	msg, _ = uvBlockMsg(t, "uvx --from gh gh pr merge 1", "qa")
	if want := "uvx running a gh write (pr merge/create, repo, api with a write method); no-push roles never write to GitHub; ask your lead"; msg != want {
		t.Errorf("gh write reason = %q", msg)
	}
}

func TestUvWrapperAllowed(t *testing.T) {
	cmds := []string{
		"uv run pytest tests/test_harness.py -q", "uv run python3 -m unittest x", "uvx ruff check .", "uv pip list",
		"uv run --with requests python3 script.py", "uv run git status", "uv run gh pr view 1", "uv sync",
		"cd /x && uv run python3 -c 'import json'", "uv run python3 ci/affected.py --base origin/main --quick --run",
		"uvx ruff check roles/hooks/worker-guard.py", "uv run mypy bin/factory", "uv run pytest -k git -q",
		"uv run pytest -k git tests/test_harness.py", "uv run --with git pytest tests/test_harness.py -q",
		"uv run pytest tests/git tests/test_harness.py", "uv run --color never git commit -m x",
		"python3 -m uv run git push", "uv run python3 x.py herdr", "uv run python3 script.py kill 1",
	}
	for _, role := range []string{"qa", "reviewer", "coder", "cleaner", "judge"} {
		for _, c := range cmds {
			if msg, blocked := uvBlockMsg(t, c, role); blocked {
				t.Errorf("%s %q: blocked (%s), want allowed", role, c, msg)
			}
		}
	}
}

func TestUvWrapperCoderPush(t *testing.T) {
	for _, c := range []string{"uv run git push origin main", "uv run -- git push", "uvx git push", "uv run --foo bar git push",
		"uv run kill 1", "uv run herdr list"} {
		if _, blocked := uvBlockMsg(t, c, "coder"); !blocked {
			t.Errorf("coder %q: allowed, want blocked", c)
		}
	}
	for _, c := range []string{"uv run pytest -q", "uv run git status", "uv pip list"} {
		msg, blocked := uvBlockMsg(t, c, "coder")
		if c == "uv run pytest -q" {
			if !blocked || !strings.Contains(msg, "full suite") {
				t.Errorf("coder %q: want the T-0435 full-suite refusal, got %q blocked=%v", c, msg, blocked)
			}
		} else if blocked {
			t.Errorf("coder %q: blocked (%s), want allowed", c, msg)
		}
	}
}

func TestWholePushAcceptsUvPrefix(t *testing.T) {
	for text, want := range map[string]bool{
		"git push origin HEAD:factory/t-1":                   true,
		"uv run git push origin HEAD:factory/t-1":            true,
		"uv run -- git push origin HEAD:factory/t-1":         true,
		"uv run -- -- git push origin HEAD:factory/t-1":      false,
		"uvx git push origin HEAD:factory/t-1":               false,
		"uv run git -C /w push origin HEAD:factory/t-1":      true,
		"uv  run git push origin HEAD:factory/t-1":           false,
		"uv run git push origin HEAD:factory/t-1 && echo hi": false,
	} {
		if _, _, ok := wholePush(text, "t-1", ""); ok != want {
			t.Errorf("wholePush(%q) = %v, want %v", text, ok, want)
		}
	}
}

func TestUvRunWrapperHeredocNotForwarded(t *testing.T) {
	body := "import subprocess; subprocess.run([\"git\",\"push\"])"
	c := "uv run python3 <<'EOF'\n" + body + "\nEOF"
	if msg, blocked := uvBlockMsg(t, c, "qa"); blocked {
		t.Errorf("qa heredoc %q: blocked (%s), want allowed (Python uv_check does not forward heredoc stdin)", c, msg)
	}
}

func TestUvRunWrapperStdinScan(t *testing.T) {
	echoPipe := "echo 'import subprocess; subprocess.run([\"git\",\"push\"])'"
	prog := "'import subprocess; subprocess.run([\"git\",\"push\"])'"
	for _, c := range []string{
		echoPipe + " | uv run timeout 5 python3",
		echoPipe + " | uv run env python3",
		"uv run timeout 5 python3 <<< " + prog,
		"uv run env python3 <<< " + prog,
	} {
		if _, blocked := uvBlockMsg(t, c, "qa"); !blocked {
			t.Errorf("qa %q: allowed, want blocked", c)
		}
	}
}

func TestUvT0435FullSuiteThroughUv(t *testing.T) {
	for _, role := range []string{"coder", "qa", "reviewer"} {
		for _, c := range []string{"uv run pytest", "uvx pytest", "uvx --from x pytest", "uv tool run pytest",
			"uv --quiet tool run py.test", "uv run python -m pytest"} {
			msg, blocked := uvBlockMsg(t, c, role)
			if !blocked || !strings.Contains(msg, "pytest with no test file or -k runs the full suite") {
				t.Errorf("%s %q: want the full-suite refusal, got %q blocked=%v", role, c, msg, blocked)
			}
		}
		if _, blocked := uvBlockMsg(t, "uv run pytest tests/test_x.py", role); blocked && role == "reviewer" {
			t.Errorf("reviewer uv run pytest <file> blocked")
		}
	}
}
