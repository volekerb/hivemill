import importlib.util
import json
import os
import pwd
import re
import shlex
import subprocess
import sys
import tempfile
import unittest

from harness import rendered_settings

HOME_DIR = pwd.getpwuid(os.getuid()).pw_dir
LIVE_HOME = os.path.join(HOME_DIR, "factory")  # what the hook and bin/factory derive (T-0212)

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
HOOK = os.path.join(ROOT, "roles", "hooks", "worker-guard.py")
LIVE_HOOK = LIVE_HOME + "/roles/hooks/worker-guard.py"
WORKERS = ("pm", "coder", "qa", "reviewer", "judge", "cleaner", "staff-engineer")
NON_WORKERS = ("boss", "director", "lead")


def run_hook(command, tool="Bash", raw=None, cwd=None, role=None):
    payload = raw if raw is not None else json.dumps(
        {"tool_name": tool, "tool_input": {"command": command}, **({"cwd": cwd} if cwd else {})})
    argv = [sys.executable, HOOK] + (["--role", role] if role else [])
    return subprocess.run(argv, input=payload, capture_output=True, text=True, timeout=30)


class WorkerGuardTests(unittest.TestCase):
    def assertBlocked(self, cmd):
        p = run_hook(cmd)
        self.assertEqual(p.returncode, 2, f"should block: {cmd!r}\n{p.stderr}")
        self.assertIn("worker-guard", p.stderr)

    def assertAllowed(self, cmd):
        p = run_hook(cmd)
        self.assertEqual(p.returncode, 0, f"should allow: {cmd!r}\n{p.stderr}")

    def test_spec_blocked(self):
        for cmd in (
            "herdr server stop",
            "HOME=x herdr server stop",
            "FOO=1 BAR=2 herdr agent list",
            "env HOME=x herdr pane list",
            "env -i herdr pane list",
            "sudo herdr server stop",
            "command herdr server stop",
            "exec herdr server stop",
            "nohup herdr server stop",
            "timeout 5 herdr server stop",
            "ls | xargs herdr",
            'bash -c "herdr server stop"',
            "sh -c 'cd /tmp && herdr server stop'",
            "zsh -c \"bash -c 'herdr server stop'\"",
            "echo hi; herdr server stop",
            "true && /usr/bin/herdr x",
            "false || herdr x",
            "echo a | herdr x",
            "(herdr x)",
            "{ herdr x; }",
            "$(herdr pane list)",
            "echo $(herdr pane list)",
            'echo "$(herdr pane list)"',
            "`herdr pane list`",
            "~/.local/bin/herdr pane list",
            "./herdr x",
            "cd /tmp\nherdr server stop",
            "echo hi # c\nherdr x",
            "systemctl --user stop herdr.service",
            "X=1 systemctl --user daemon-reload",
            "systemctl --user enable factory-up.service",
            "systemctl --user restart factory-tick.timer",
            "systemctl --user start herdr-server",
            "systemctl --user disable --now factory-tick.timer",
            "sudo systemctl stop herdr",
            "systemctl --user kill 'factory-*'",
            "systemctl --user stop '*'",
            "bash -c 'systemctl --user daemon-reload'",
            'echo "unbalanced',
            "echo 'x",
        ):
            with self.subTest(cmd=cmd):
                self.assertBlocked(cmd)

    def test_spec_allowed(self):
        for cmd in (
            'factory done "x"',
            "git status",
            "python3 -m unittest discover -s tests -v",
            "grep herdr README.md",
            "cat tests/stubs/herdr",
            "echo herdr",
            "systemctl --user status factory-tick.service",
            "systemctl --user cat herdr-server.service",
            "systemctl --user list-units 'factory-*'",
            "systemctl --user is-active herdr-server",
            "systemd-analyze --user verify systemd/*.service",
        ):
            with self.subTest(cmd=cmd):
                self.assertAllowed(cmd)

    def test_factory_unit_commands_blocked_for_workers(self):
        for cmd in ("factory up", "env X=1 factory tick", "bin/factory relaunch x",
                    "python3 ~/factory/bin/factory spawn coder", 'sh -c "factory up"', "factory clean --dry-run",
                    f"FACTORY_HOME={LIVE_HOME} factory up", "FACTORY_HOME=$H factory up",
                    f"env FACTORY_HOME={LIVE_HOME}/../factory ./bin/factory tick", "echo hi; factory tick"):
            for role in (None, "coder"):
                with self.subTest(cmd=cmd, role=role):
                    p = run_hook(cmd, role=role)
                    self.assertEqual(p.returncode, 2, p.stderr)

    def test_factory_unit_commands_allowed(self):
        for cmd in ("FACTORY_HOME=/tmp/x ./bin/factory up", "env FACTORY_HOME=/tmp/x factory tick",
                    'factory done "x"', "factory msg lead hi body", "factory task note T-0001 hi",
                    'grep "factory up" README.md', "factory inbox", "factory whoami"):
            with self.subTest(cmd=cmd):
                self.assertAllowed(cmd)

    def test_cleaner_may_clean(self):
        self.assertEqual(run_hook("factory clean --dry-run", role="cleaner").returncode, 0)
        self.assertEqual(run_hook("factory clean --dry-run", role="coder").returncode, 2)
        self.assertEqual(run_hook("factory up", role="cleaner").returncode, 2)

    def test_bypass_forms_blocked(self):
        for cmd in (
            "eval herdr server stop",
            "eval 'HOME=x herdr server stop'",
            r"h\erdr server stop",
            '"herdr" server stop',
            "'herdr' server stop",
            "h''erdr server stop",
            "$'herdr' server stop",
            "exec -a x herdr server stop",
            "busybox sh -c 'herdr server stop'",
            "H=herdr; $H server stop",
            "H=herdr; ${H} server stop",
            "/usr/bin/her* server stop",
            "/usr/bin/herd? server stop",
            "{herdr,x} server stop",
            "sudo -u root env A=1 herdr server stop",
            "bash -c \"sudo env A=1 herdr server stop\"",
            "echo ok && (X=1 " + HOME_DIR + "/.local/bin/herdr pane list)",
            "find /usr -exec herdr x {} +",
            "echo 'herdr server stop' | bash",
            "echo 'herdr server stop' | sh -",
            "bash <<EOF\nherdr server stop\nEOF",
            "cat <<EOF | sh\nherdr server stop\nEOF",
            "echo '<<EOF'\nherdr x\nEOF",
            "alias h=herdr; h server stop",
            "ln -s /usr/bin/herdr /tmp/h; /tmp/h server stop",
            "cp ~/.local/bin/herdr /tmp/h",
            "ssh localhost herdr server stop",
            "ssh localhost 'herdr server stop'",
            "watch -n1 herdr agent list",
            "tmux new-session 'herdr server stop'",
            "pkill -f herdr",
            "killall herdr",
            "kill $(pgrep herdr)",
            "python3 -c 'import os; os.system(\"herdr server stop\")'",
            "python3 <<EOF\nimport os; os.system('herdr server stop')\nEOF",
            "x=$(herdr agent list)",
            "for i in 1; do herdr server stop; done",
            "if true; then herdr server stop; fi",
            "h() { herdr \"$@\"; }; h server stop",
            "echo a > /dev/null; HERDR_ENV=1 herdr x",
            "2>/dev/null herdr x",
            "cat <(herdr x)",
            "bash nonexistent-script-guard-test.sh",
        ):
            with self.subTest(cmd=cmd):
                self.assertBlocked(cmd)

    def test_more_allowed(self):
        for cmd in (
            "git commit -m \"$(cat <<'EOF'\nfix: don't call herdr directly\n\nmore herdr text\nEOF\n)\"",
            "factory plan G-001 - <<'EOF'\nthe herdr binary is stubbed in tests\nEOF",
            "gh pr create --title t --body 'herdr is stubbed in tests'",
            "git commit -m 'T-0016: block herdr'",
            "PATH=$PWD/tests/stubs:$PATH python3 -m unittest discover -s tests -v",
            "tests/stubs/herdr agent list",
            "./tests/stubs/herdr server stop",
            "command -v herdr",
            "which herdr",
            "pgrep herdr",
            "find . -name herdr",
            "echo 'a;b' && ls 2>&1 | tail -n 3",
            "for f in roles/settings/*.json; do python3 -c \"import json,sys;json.load(open(sys.argv[1]))\" $f; done",
            "[ -f README.md ] && echo yes",
            "[[ -f README.md ]] && echo yes",
            "x=$(git rev-parse HEAD) && echo $x",
            "cd \"$(git rev-parse --show-toplevel)\" && git status",
            "timeout 60 python3 -m unittest discover -s tests",
            "env FOO=1 python3 -m py_compile bin/factory",
            "echo hi # herdr is not called here",
            "sh <<< 'echo hi'",
            "cat <(echo hi)",
            "xargs -I{} grep {} f",
            "xargs -I {} grep {} f",
            "echo $(git rev-parse HEAD)",
            "sed -n '/herdr/p' README.md",
            "git commit -m 'block herdr'",
            "git -c user.name=x log",
            "systemctl --user status herdr-server",
            "systemctl --user show factory-tick.timer",
            "python3 -m unittest tests.test_herdr_thing",
            "factory msg lead 'herdr is down' 'body; with; semicolons'",
            "git diff origin/main...HEAD --stat",
            "sed -n '1,5p' README.md",
        ):
            with self.subTest(cmd=cmd):
                self.assertAllowed(cmd)

    def test_attempt2_bypasses_blocked(self):
        """Every bypass named in the reviewer (B1, N1) and QA (AC4) FAIL comments on PR #11."""
        for cmd in (
            "$(which herdr) server stop",
            "$(command -v herdr) server stop",
            "X=1 $(which herdr) server stop",
            "echo ok; $(which herdr) server stop",
            'sh -c "$(echo herdr) server stop"',
            '"$(which herdr)" server stop',
            "`which herdr` server stop",
            "$(printf 'herd%s' r) server stop",
            'sh <<< "herdr server stop"',
            "bash <<< 'herdr server stop'",
            "bash <(echo herdr server stop)",
            "source <(echo herdr x)",
            "echo herdr | xargs -I{} {} server stop",
            "echo herdr | xargs -I {} {} server stop",
            "echo herdr | xargs -i {} server stop",
            "printf 'herdr server stop' | xargs -0 sh -c",
            "awk 'BEGIN{system(\"herdr server stop\")}'",
            "gawk 'BEGIN{system(\"herdr x\")}'",
            "sed -n '1e herdr server stop' /etc/hostname",
            "sed 's/a/herdr server stop/e' f",
            "vim -c '!herdr server stop' -c q",
            "git -c core.pager='herdr server stop' log",
            "git -c alias.x='!herdr server stop' x",
            "git config alias.x '!herdr server stop'",
            "systemctl --user isolate rescue.target",
            "eval \"$CMD\"",
            "bash -c",
        ):
            with self.subTest(cmd=cmd):
                self.assertBlocked(cmd)

    def test_parser_alone(self):
        """Layer 1 catches the reviewer/QA forms by itself, with the raw-text backstop disabled."""
        spec = importlib.util.spec_from_file_location("worker_guard", HOOK)
        wg = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(wg)
        wg.BACKSTOP = re.compile(r"(?!x)x")  # never matches
        for cmd in (
            "$(which herdr) server stop",
            "$(command -v herdr) server stop",
            "X=1 $(which herdr) server stop",
            "echo ok; $(which herdr) server stop",
            'sh -c "$(echo herdr) server stop"',
            "$(printf 'herd%s' r) server stop",
            'sh <<< "herdr server stop"',
            "bash <(echo herdr server stop)",
            "echo herdr | xargs -I{} {} server stop",
            "printf 'herdr server stop' | xargs -0 sh -c",
            "awk 'BEGIN{system(\"herdr server stop\")}'",
            "sed -n '1e herdr server stop' /etc/hostname",
            "vim -c '!herdr server stop' -c q",
            "git -c core.pager='herdr server stop' log",
            "git -c alias.x='!herdr server stop' x",
            "systemctl --user isolate rescue.target",
        ):
            with self.subTest(cmd=cmd):
                with self.assertRaises(wg.Block):
                    wg.check(cmd)
        for cmd in ("x=$(git rev-parse HEAD)", "echo $(git rev-parse HEAD)", "sh <<< 'echo hi'"):
            with self.subTest(cmd=cmd):
                wg.check(cmd)

    def test_backstop_alone(self):
        """Layer 2 (R1) blocks on raw text even if the parser (layer 1) allows everything."""
        spec = importlib.util.spec_from_file_location("worker_guard", HOOK)
        wg = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(wg)
        wg.Analyzer.script = lambda self, text, depth=0: None  # parser mocked to allow
        for cmd in (
            "find / -name herdr -exec {} server stop \\;",
            "echo herdr | xargs -I{} {} pane close p1",
            "env -S 'herdr agent start a'",
            "sudo herdr tab create",
            "x=1 nohup herdr  workspace   close w1",
            "bash -c 'herdr agent send a x'",
            "echo herdr | xargs -I{} {} agent \\\n  start x",
        ):
            with self.subTest(cmd=cmd):
                with self.assertRaises(wg.Block):
                    wg.check(cmd)
        for cmd in ("grep herdr README.md", "echo herdr", "herdr_helper server stop", "tests/stubs/herdr server stop",
                    "herdr\nserver stop", "herdr is a server", "echo herdr server stop"):
            with self.subTest(cmd=cmd):
                wg.check(cmd)  # no Block

    def test_false_positives_allowed(self):
        for cmd in (
            "grep -rn 'herdr server stop' tests/", "rg herdr bin/", "cat README.md", "less README.md",
            "git commit -m 'guard: block herdr server stop'",
            "gh pr comment 11 --body 'herdr server stop is blocked'",
            "factory msg lead-g-001-a x 'herdr server stop blocked'",
            "python3 -m unittest discover -s tests -v",
            "sed -i 's/old/new/' tests/test_worker_guard.py",
            "echo 'HERDR_SOCKET_PATH is pinned' >> README.md",
        ):
            with self.subTest(cmd=cmd):
                self.assertAllowed(cmd)

    def test_r2_path_quoted_data_only(self):
        self.assertAllowed("grep -rn 'herdr.sock' README.md")
        self.assertAllowed("git commit -m 'pin ~/.config/herdr/herdr.sock'")
        self.assertBlocked("ls ~/.config/herdr")
        self.assertBlocked("cat herdr.sock")
        self.assertBlocked("ls '~/.config/herdr'")

    def test_stub_path_is_not_a_traversal_hole(self):
        self.assertBlocked("tests/stubs/../../herdr server stop")
        self.assertBlocked("/usr/bin/herdr x # tests/stubs/herdr")

    def test_script_file_is_inspected(self):
        with tempfile.TemporaryDirectory() as d:
            bad, good = os.path.join(d, "bad.sh"), os.path.join(d, "good.sh")
            with open(bad, "w") as f:
                f.write("#!/bin/sh\necho hi\nherdr server stop\n")
            with open(good, "w") as f:
                f.write("#!/bin/sh\necho hi\n")
            os.chmod(bad, 0o755)
            self.assertBlocked(f"bash {shlex.quote(bad)}")
            self.assertBlocked(f"source {shlex.quote(bad)}")
            self.assertBlocked(shlex.quote(bad))
            self.assertAllowed(f"bash {shlex.quote(good)}")
            self.assertEqual(run_hook("bash bad.sh", cwd=d).returncode, 2)
            self.assertEqual(run_hook("bash good.sh", cwd=d).returncode, 0)

    def test_protocol(self):
        self.assertEqual(run_hook("herdr x", tool="Read").returncode, 0)
        for raw in ("", "not json", "[]", json.dumps({"tool_name": "Bash"}),
                    json.dumps({"tool_name": "Bash", "tool_input": {"command": 5}})):
            with self.subTest(raw=raw):
                self.assertEqual(run_hook(None, raw=raw).returncode, 2)

    def test_deep_nesting_fails_closed(self):
        cmd = "herdr x"
        for _ in range(10):
            cmd = "bash -c " + shlex.quote(cmd)
        self.assertBlocked(cmd)
        cmd = "echo hi"
        for _ in range(10):
            cmd = "bash -c " + shlex.quote(cmd)
        self.assertBlocked(cmd)

    def test_settings_wiring(self):
        for role in WORKERS + NON_WORKERS:
            with open(rendered_settings(role)) as f:
                s = json.load(f)
            if role in NON_WORKERS:  # HQ roles run only the PRIV rule (T-0057): --hq, no --role
                cmds = [h["command"] for g in s["hooks"]["PreToolUse"] for h in g["hooks"]]
                self.assertEqual(len(cmds), 1, role)
                self.assertEqual(shlex.split(cmds[0])[-2:], [LIVE_HOOK, "--hq"], role)
                if role == "boss":  # boss also has the inbox UserPromptSubmit hook (T-0093)
                    self.assertIn("UserPromptSubmit", s["hooks"], role)
                continue
            groups = s["hooks"]["PreToolUse"]
            cmds = [h["command"] for g in groups if g.get("matcher") == "Bash" for h in g["hooks"]
                    if h.get("type") == "command"]
            self.assertEqual(len(cmds), 1, role)
            script = [t for t in shlex.split(cmds[0]) if t.endswith("worker-guard.py")]
            self.assertEqual(script, [LIVE_HOOK], role)
            self.assertEqual(shlex.split(cmds[0])[-2:], ["--role", role])
            self.assertTrue(os.path.isabs(script[0]))
            # the live path ~/factory/roles/hooks/worker-guard.py mirrors this repo file
            self.assertTrue(os.path.isfile(HOOK))
            self.assertTrue(os.access(HOOK, os.X_OK))

    def test_settings_pin_dead_herdr_socket(self):
        for role in WORKERS + NON_WORKERS:
            with open(rendered_settings(role)) as f:
                env = json.load(f).get("env", {})
            if role in NON_WORKERS:
                self.assertNotIn("HERDR_SOCKET_PATH", env, role)
            else:
                sock = env.get("HERDR_SOCKET_PATH", "")
                self.assertTrue(sock.startswith("/proc/nonexistent/"), role)  # nothing can be created there

    def _pm_settings(self):
        with open(rendered_settings("pm")) as f:
            return json.load(f)

    def test_pm_settings_still_deny_edit_write_commit_push_merge(self):
        perms = self._pm_settings()["permissions"]
        for rule in ("Edit", "Write", "NotebookEdit", "Bash(git commit:*)", "Bash(git push:*)",
                     "Bash(git merge:*)", "Bash(git rebase:*)", "Bash(git reset:*)", "Bash(gh pr merge:*)"):
            self.assertIn(rule, perms["deny"], rule)
        for rule in perms["allow"]:  # only read-only git/gh beyond factory
            self.assertNotIn(rule.split(":")[0], ("Bash(git commit", "Bash(git push", "Bash(gh pr merge"), rule)
        for rule in ("Bash(git show:*)", "Bash(gh pr list:*)"):
            self.assertIn(rule, perms["allow"], rule)

    def test_pm_settings_keep_worker_guard_and_dead_socket(self):
        s = self._pm_settings()
        cmds = [h["command"] for g in s["hooks"]["PreToolUse"] if g.get("matcher") == "Bash"
                for h in g["hooks"] if h.get("type") == "command"]
        self.assertEqual(len(cmds), 1)
        self.assertEqual(shlex.split(cmds[0])[-2:], ["--role", "pm"])
        self.assertIn(LIVE_HOOK, shlex.split(cmds[0]))
        self.assertTrue(s["env"]["HERDR_SOCKET_PATH"].startswith("/proc/nonexistent/"))
        self.assertEqual(s["env"]["FACTORY_ROLE"], "pm")

    def test_reexport_and_flag_bypasses_blocked(self):
        """M224: re-exporting the live socket or passing a herdr launch flag must not unblock a mutating verb."""
        self.assertBlocked("HERDR_SOCKET_PATH=~/.config/herdr/herdr.sock herdr server stop")
        self.assertBlocked("export HERDR_SOCKET_PATH=" + HOME_DIR + "/.config/herdr/herdr.sock; herdr server stop")
        # herdr has no --socket flag (its launch flags are --session and --machine); block all forms anyway
        self.assertBlocked("herdr --socket /x server stop")
        self.assertBlocked("herdr --session foo server stop")
        self.assertBlocked("herdr --machine m server stop")

    def test_worker_statusline_pins_live_socket(self):
        want = {"type": "command", "command": "HERDR_SOCKET_PATH=" + HOME_DIR + "/.config/herdr/herdr.sock "
                                              + HOME_DIR + "/.claude/statusline-usage.sh"}
        for role in WORKERS + NON_WORKERS:
            with open(rendered_settings(role)) as f:
                sl = json.load(f).get("statusLine")
            if role in NON_WORKERS:
                self.assertIsNone(sl, role)
            else:
                self.assertEqual(sl, want, role)

    def test_worker_commands_stay_allowed(self):
        for cmd in ('factory task update T-0016 --status in_review --pr https://x/pull/1',
                    'factory msg lead:g-001-a "s" "b"', 'factory verdict T-1 pass "ok"',
                    'git push -u origin HEAD', 'gh pr create --fill', 'gh pr view 1 --json state',
                    'python3 -m py_compile bin/factory'):
            with self.subTest(cmd=cmd):
                self.assertAllowed(cmd)

    def test_attempt3_r1_r2_r3_blocked(self):
        sock = "HERDR_SOCKET_PATH=" + HOME_DIR + "/.config/herdr/herdr.sock"
        for cmd in (
            # QA FAIL list, verbatim
            f"find ~/.local/bin -name herdr -exec env {sock} {{}} server stop \\;",
            f"find ~/.local/bin -name herdr -execdir env {sock} {{}} server stop \\;",
            f"echo ~/.local/bin/herdr | xargs -I{{}} env {sock} {{}} server stop",
            "which herdr | xargs -I{} timeout 5 {} server stop",
            f"echo ~/.local/bin/herdr | xargs -I{{}} sudo env {sock} {{}} server stop",
            f"echo ~/.local/bin/herdr | xargs -I{{}} nohup env {sock} {{}} server stop",
            f"echo ~/.local/bin/herdr | xargs -I{{}} command env {sock} {{}} server stop",
            # R1: any tokens between herdr and the mutating sequence, also across a \-continuation
            "find / -name herdr -exec {} pane close 1 \\;",
            "echo herdr | xargs -I{} {} agent \\\n  start x",
            # R2: socket re-target, session, config paths, unsetting alongside herdr
            "export HERDR_SOCKET_PATH=/x", "HERDR_SESSION=default true", "declare -x HERDR_SOCKET_PATH",
            "ls ~/.config/herdr", "ls $HOME/.config/herdr", "cat herdr.sock",
            "env -u HERDR_SOCKET_PATH herdr pane list", "unset HERDR_SOCKET_PATH; herdr pane list",
            "env -i herdr pane list",
            # R3: binary copy / read
            "cat ~/.local/bin/herdr > /tmp/x", "dd if=" + HOME_DIR + "/.local/bin/herdr of=/tmp/x",
            "cp ~/.local/bin/herdr /tmp/x", "install ~/.local/bin/herdr /tmp/x", "rsync ~/.local/bin/herdr /tmp/",
            "tee /tmp/x < ~/.local/bin/herdr", "ln -s ~/.local/bin/herdr /tmp/x", "mv ~/.local/bin/herdr /tmp/x",
            "base64 ~/.local/bin/herdr", "xxd ~/.local/bin/herdr", "head -c 100 ~/.local/bin/herdr",
            "tail -c 100 ~/.local/bin/herdr", "cat $HERDR_BIN_PATH", "dd if=$HERDR_BIN_PATH of=/tmp/x",
        ):
            with self.subTest(cmd=cmd):
                self.assertBlocked(cmd)

    def test_attempt3_allowed(self):
        for cmd in (
            "env -i PATH=tests/stubs:/usr/bin python3 -m unittest discover -s tests",
            "factory whoami", "cat tests/stubs/herdr", "tests/stubs/herdr pane list",
            "head -5 tests/stubs/herdr", "cp tests/stubs/herdr /tmp/stub",
        ):
            with self.subTest(cmd=cmd):
                self.assertAllowed(cmd)

    def test_qa_normal_commands_allowed(self):
        for cmd in (
            "git status", "git diff origin/main...HEAD --stat", "git log --oneline -5", "git fetch origin",
            "git commit -m 'Fix guard\n\nsecond line'", "gh pr view 11 --json state", "gh pr diff 11 | grep herdr",
            "gh pr comment 11 --body-file - <<'EOF'\nlooks fine\nEOF", "python3 -m unittest discover -s tests",
            "python3 -m py_compile roles/hooks/worker-guard.py", "factory done \"x\"",
            "factory msg lead:g-001-a s - <<'EOF'\nbody\nEOF", "factory whoami", "factory verdict T-1 pass ok",
            "grep -rn herdr README.md", "systemctl --user status factory-tick", "systemd-analyze --user verify a.service",
            "x=$(git rev-parse HEAD)", "for f in a b; do echo $f; done", "echo herdr", "cat README.md",
            "tests/stubs/herdr pane list", "git diff | grep herdr", "ls", "grep herdr README.md",
        ):
            with self.subTest(cmd=cmd):
                self.assertAllowed(cmd)

    def run_as(self, cmd, own="coder-t-0036"):
        env = {**os.environ, "HOME": os.path.dirname(LIVE_HOME)}  # ~/factory must mean the live home, also on CI
        env.pop("FACTORY_AGENT", None)
        if own:
            env["FACTORY_AGENT"] = own
        return subprocess.run([sys.executable, HOOK, "--role", "coder"], capture_output=True, text=True, env=env,
                              input=json.dumps({"tool_name": "Bash", "tool_input": {"command": cmd}}), timeout=30)

    def test_identity_belt_blocked(self):
        for cmd in (
            "setsid -f factory team new G-001 -", "nohup setsid factory up", "echo hi; setsid sleep 1",
            "factory up & disown", "env -u FACTORY_ROLE factory up", "env -uHERDR_PANE_ID factory up",
            "env --unset=FACTORY_ROLE factory up", "unset HERDR_PANE_ID; factory spawn pm", "unset FACTORY_ROLE",
            "env -i factory up", "env -i PATH=/usr/bin ./bin/factory tick",
            "FACTORY_AGENT=director factory team new G-001 x", "FACTORY_AGENT=owner factory msg x y",
            "env FACTORY_AGENT=director factory team new", "export FACTORY_AGENT=director", "FACTORY_AGENT=director",
            "sh -c 'FACTORY_AGENT=director factory team new'", "FACTORY_ROLE=lead factory spawn pm",
        ):
            with self.subTest(cmd=cmd):
                p = self.run_as(cmd)
                self.assertEqual(p.returncode, 2, f"should block: {cmd!r}\n{p.stderr}")

    def test_identity_belt_allowed(self):
        for cmd in (
            "FACTORY_AGENT=coder-t-0036 factory done x", "env FACTORY_AGENT=coder-t-0036 factory done x",
            "FACTORY_HOME=/tmp/x ./bin/factory up", "grep setsid README.md", "echo setsid",
            "env -i PATH=tests/stubs:/usr/bin python3 -m unittest discover -s tests", "factory inbox",
            "factory msg lead:g-001-a s b", "factory task show T-0036", "factory verdict T-1 pass ok",
            "factory report G-001 h -", "factory done 'unset FACTORY_ROLE setsid'",
        ):
            with self.subTest(cmd=cmd):
                p = self.run_as(cmd)
                self.assertEqual(p.returncode, 0, f"should allow: {cmd!r}\n{p.stderr}")

    def test_identity_without_own_name_blocks_every_assignment(self):
        self.assertEqual(self.run_as("FACTORY_AGENT=coder-t-0036 factory done x", own=None).returncode, 2)
        self.assertEqual(self.run_as("factory done x", own=None).returncode, 0)

    def test_own_name_owner_still_blocked(self):
        self.assertEqual(self.run_as("FACTORY_AGENT=owner factory msg x y", own="owner").returncode, 2)

    def test_hook_allows_foreign_agent_with_temp_home(self):
        for cmd in (
            "FACTORY_HOME=$(mktemp -d) FACTORY_AGENT=zz-test-coder ./bin/factory task list",
            "FACTORY_AGENT=zz-test-coder FACTORY_HOME=$(mktemp -d) ./bin/factory task list",
            "FACTORY_HOME=$(mktemp -d -p /tmp) FACTORY_AGENT=zz-test-coder ./bin/factory task list",
            "FACTORY_HOME=$(mktemp -p /tmp/q -d) FACTORY_AGENT=zz-test-coder ./bin/factory task list",
            "FACTORY_HOME=/tmp/qa1 FACTORY_AGENT=zz-test-coder ./bin/factory task list",
            "FACTORY_HOME=/tmp/qa1 FACTORY_AGENT=owner ./bin/factory task list",
            "env FACTORY_HOME=/tmp/qa1 FACTORY_AGENT=director ./bin/factory task list",
        ):
            with self.subTest(cmd=cmd):
                p = self.run_as(cmd)
                self.assertEqual(p.returncode, 0, f"should allow: {cmd!r}\n{p.stderr}")

    def test_hook_blocks_foreign_agent_without_temp_home(self):
        live = LIVE_HOME
        for cmd in (
            "FACTORY_AGENT=zz-test-coder ./bin/factory task list",
            "FACTORY_HOME=~/factory FACTORY_AGENT=zz-test-coder ./bin/factory task list",
            f"FACTORY_HOME={live} FACTORY_AGENT=zz-test-coder ./bin/factory task list",
            f"FACTORY_HOME={live}/worktrees/x FACTORY_AGENT=zz-test-coder ./bin/factory task list",
            f"FACTORY_HOME={live}/../factory FACTORY_AGENT=zz-test-coder ./bin/factory task list",
            "FACTORY_HOME=$HOME/factory FACTORY_AGENT=zz-test-coder ./bin/factory task list",
            "FACTORY_HOME=$(echo ~/factory) FACTORY_AGENT=zz-test-coder ./bin/factory task list",
            "FACTORY_HOME=$(mktemp -d /x) FACTORY_AGENT=zz-test-coder ./bin/factory task list",
            f"FACTORY_HOME=$(mktemp -d -p {live}) FACTORY_AGENT=zz-test-coder ./bin/factory task list",
            f"FACTORY_HOME=$(mktemp -d -p {live}/worktrees) FACTORY_AGENT=zz-test-coder ./bin/factory task list",
            f"TMPDIR={live} FACTORY_HOME=$(mktemp -d) FACTORY_AGENT=zz-test-coder ./bin/factory task list",
            "FACTORY_HOME=`mktemp -d` FACTORY_AGENT=zz-test-coder ./bin/factory task list",
            "FACTORY_HOME=$(mktemp -d; id) FACTORY_AGENT=zz-test-coder ./bin/factory task list",
        ):
            with self.subTest(cmd=cmd):
                p = self.run_as(cmd)
                self.assertEqual(p.returncode, 2, f"should block: {cmd!r}\n{p.stderr}")


class ClaudeGuardTests(unittest.TestCase):
    """G-006 B2: only factory spawn starts Claude (lesson M347)."""

    def test_claude_blocked(self):
        for cmd in (
            "claude -p hi",
            "FOO=1 claude -p x",
            "env A=1 claude",
            "/usr/bin/claude --print x",
            "~/.local/bin/claude",
            "./claude",
            "bash -c 'claude -p x'",
            "timeout 5 claude -p x",
            "nohup claude -p x &",
            "echo x | xargs claude -p",
            "$(which claude) -p x",
            "echo $(claude -p x)",
            "eval 'claude -p x'",
            "sudo -u x env A=1 claude",
            "npx @anthropic-ai/claude-code -p x",
            "pnpm dlx @anthropic-ai/claude-code -p x",
            "node /usr/lib/node_modules/@anthropic-ai/claude-code/cli.js -p x",
            "cd /tmp && claude",
            "bash <<'EOF'\nclaude -p x\nEOF",
            "alias c=claude",
            "alias c=/usr/bin/claude",
        ):
            with self.subTest(cmd=cmd):
                p = run_hook(cmd)
                self.assertEqual(p.returncode, 2, f"should block: {cmd!r}\n{p.stderr}")
                self.assertIn("only factory spawn starts Claude", p.stderr)

    def test_claude_alias_script_from_disk_blocked(self):
        with tempfile.TemporaryDirectory() as d:
            with open(os.path.join(d, "go.zsh"), "w") as f:
                f.write("alias c=claude\nc -p x\n")
            p = run_hook("zsh go.zsh", cwd=d)
            self.assertEqual(p.returncode, 2, p.stderr)
            self.assertIn("only factory spawn starts Claude", p.stderr)

    def test_dynamic_command_word_message_names_both_rules(self):
        for cmd in ("$X -p x", "$(which tool) -p x"):
            with self.subTest(cmd=cmd):
                p = run_hook(cmd)
                self.assertEqual(p.returncode, 2, p.stderr)
                self.assertIn("only factory spawn starts Claude", p.stderr)
                self.assertIn("herdr", p.stderr)

    def test_claude_script_from_disk_blocked(self):
        with tempfile.TemporaryDirectory() as d:
            with open(os.path.join(d, "go.sh"), "w") as f:
                f.write("#!/bin/sh\nclaude -p x\n")
            p = run_hook("bash go.sh", cwd=d)
            self.assertEqual(p.returncode, 2, p.stderr)
            self.assertIn("only factory spawn starts Claude", p.stderr)

    ALIAS_CLAUDE = (
        'alias c="claude -p"', "alias c='claude -p x'", 'alias h="/usr/bin/claude --print"', "alias c=\\\\claude",
        "alias c='\\claude'", 'alias -g c="claude -p"', 'alias c="FOO=1 claude"', 'alias c="env claude"',
        'alias c="sudo claude -p"', 'alias c="timeout 5 claude"', 'alias c="cd /tmp && claude"',
        'alias c="ls; claude -p x"', "alias c=\"bash -c 'claude -p x'\"", 'alias c="~/.local/bin/claude -p"',
        'alias c="npx @anthropic-ai/claude-code -p"', "c(){ claude; }", "function c { claude -p x; }; c",
        'alias c="ls;claude"', "alias c='claude;'", 'alias c="ls|claude"', "alias c='\"claude\"'",
        "alias c=\"'claude'\"", "alias c='c\\laude'", "alias c='\"/usr/bin/claude\"'", "alias c=\"cl''aude\"",
        "alias c=$'claude\\x20-p'",
    )
    ALIAS_HERDR = ('alias h="herdr pane list"', 'alias h="env herdr agent start"', 'alias h="/usr/bin/herdr tab list"',
                   "function h { herdr pane list; }", 'alias h="ls;herdr"', "alias h='herdr;'", "alias h='\"herdr\"'",
                   "alias h=\"'herdr'\"", "alias h='her\\dr'")

    def test_alias_value_with_arguments_blocked(self):
        for cmd in self.ALIAS_CLAUDE:
            with self.subTest(cmd=cmd):
                p = run_hook(cmd)
                self.assertEqual(p.returncode, 2, f"should block: {cmd!r}\n{p.stderr}")
                self.assertIn("only factory spawn starts Claude", p.stderr)
        for cmd in self.ALIAS_HERDR:
            with self.subTest(cmd=cmd):
                self.assertEqual(run_hook(cmd).returncode, 2, cmd)

    def test_alias_value_with_arguments_in_script_file_blocked(self):
        for body in ('alias c="claude -p"\nc x\n', "alias c='claude -p x'\nc\n", "alias c=\\\\claude\nc\n",
                     'alias h="herdr pane list"\nh\n', 'alias c="ls;claude"\nc\n', "alias c='\"claude\"'\nc -p\n",
                     'alias h="ls;herdr"\nh\n'):
            for interp in ("zsh", "bash"):
                with self.subTest(body=body, interp=interp), tempfile.TemporaryDirectory() as d:
                    with open(os.path.join(d, "f.sh"), "w") as f:
                        f.write(body)
                    p = run_hook(f"{interp} f.sh", cwd=d)
                    self.assertEqual(p.returncode, 2, p.stderr)

    def test_alias_with_claude_only_as_argument_allowed(self):
        for cmd in ("echo alias c=claude", 'git commit -m "alias c=claude"', 'grep "alias c=claude" README.md',
                    "MODEL=claude-opus ls", "X=/usr/bin/claude ls", "cat ~/.claude/x", 'alias ll="ls -la"',
                    'alias gc="grep claude README.md"', 'alias x="echo claude"', "function ok { ls; }"):
            with self.subTest(cmd=cmd):
                p = run_hook(cmd)
                self.assertEqual(p.returncode, 0, f"should allow: {cmd!r}\n{p.stderr}")

    def test_claude_as_argument_allowed(self):
        for cmd in (
            "grep -rn claude README.md",
            "cat ~/.claude/settings.json",
            "ls ~/.claude",
            'git commit -m "mention claude -p"',
            "gh pr comment --body 'claude'",
            "echo claude",
        ):
            with self.subTest(cmd=cmd):
                p = run_hook(cmd)
                self.assertEqual(p.returncode, 0, f"should allow: {cmd!r}\n{p.stderr}")

    def test_persistent_roles_deny_claude(self):
        for role in NON_WORKERS:
            with self.subTest(role=role):
                with open(rendered_settings(role)) as f:
                    deny = json.load(f)["permissions"]["deny"]
                self.assertIn("Bash(claude:*)", deny)
                rel = f"roles/settings/{role}.json.in"  # origin/main's template; absent before the T-B2 merge
                base = subprocess.run(["git", "show", f"origin/main:{rel}"], cwd=ROOT,
                                      capture_output=True, text=True)
                if base.returncode != 0:
                    for kept in ("Bash(gh pr merge:*)", "Bash(sqlite3:*)"):
                        self.assertIn(kept, deny)
                    continue
                old = json.loads(base.stdout)["permissions"]["deny"]
                with open(os.path.join(ROOT, rel)) as f:
                    tdeny = json.load(f)["permissions"]["deny"]
                # only these may differ from main: PRIV denies (T-0057), db/** denies (T-0159), advisor (T-0250)
                added = {"Advisor", "Bash(claude:*)", "Bash(sudo:*)", "Bash(su:*)", "Bash(doas:*)", "Bash(pkexec:*)",
                         "Bash(docker:*)", "Edit(/@FACTORY_LIVE_HOME@/db/**)", "Write(/@FACTORY_LIVE_HOME@/db/**)",  # T-A
                         "Edit(/@FACTORY_LIVE_HOME@/run/settings/**)", "Write(/@FACTORY_LIVE_HOME@/run/settings/**)"}  # T-B2
                self.assertEqual([x for x in tdeny if x not in added], [x for x in old if x not in added])

if __name__ == "__main__":
    unittest.main()
