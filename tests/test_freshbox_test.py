"""ci/freshbox_test.py: the plan, the preflight refusals, cleanup on every exit path, the pinned download and the
results file. Every machine effect goes through a fake Host; nothing here creates a user or runs the real tools."""
import ast
import datetime
import hashlib
import importlib.util
import io
import os
import signal
import stat
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SCRIPT = os.path.join(ROOT, "ci", "freshbox_test.py")
AGENTS = "NAME ROLE STATUS\nboss boss live\ndirector director live\n"
BIN = "--" + "herdr-bin"  # a real file to copy, so no download is needed


def load():
    spec = importlib.util.spec_from_file_location("freshbox_test", SCRIPT)
    m = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(m)
    return m


fb = load()


class FakeProc:
    terminated = False

    def terminate(self):
        self.terminated = True


class FakeHost(fb.Host):
    """Records every call. `rules` maps a substring of the joined argv to (rc, output), an exception or a callable."""

    def __init__(self, tmp, euid=0, tty=True, users=(), busy=False, rules=None, assets=None):
        self.tmp, self._euid, self._tty, self.users, self.busy = tmp, euid, tty, set(users), busy
        self.rules, self.assets = rules or {}, assets or {}
        self.calls, self.downloads, self.chowns, self.rmtrees, self.procs = [], [], [], [], []
        self.inv_home = os.path.join(tmp, "alice-home")
        os.makedirs(self.inv_home, exist_ok=True)

    def euid(self): return self._euid
    def isatty(self): return self._tty
    def user_exists(self, name): return name in self.users
    def pw_dir(self, name): return self.inv_home
    def ids(self, name): return 4242, 4243
    def port_busy(self, port): return self.busy
    def count_claude(self): return 3
    def lexists(self, path): return path in self.leftovers

    leftovers = ()
    def sleep(self, s): pass
    def http_status(self, url): return 200

    stdin_seen = None

    def read_token(self, path):
        if path == "-":
            return self.stdin_token
        with open(path) as f:
            return f.read()

    def run(self, argv, capture=True, timeout=None, cwd=None, input_text=None):
        j = " ".join(argv)
        self.calls.append((list(argv), capture))
        if input_text is not None:
            self.stdin_seen = (list(argv), input_text)
        for key, val in self.rules.items():
            if key in j:
                if isinstance(val, BaseException):
                    raise val
                if callable(val):
                    val()
                    break
                return val
        if argv[0] == "useradd":
            self.users.add(argv[-1])
        elif argv[0] == "userdel":
            self.users.discard(argv[-1])
        elif argv[0] == "id":
            return (0 if argv[1] in self.users else 1), ""
        elif j.endswith("factory init"):
            return 0, "initialised /ho" "me/x/factory/db/factory.db\n"
        elif "ls " in j and "run/settings" in j:
            return 0, "boss.json\ncoder.json\n"
        elif j.endswith("factory agents"):
            return 0, AGENTS
        return 0, ""

    def popen(self, argv, logpath, cwd=None):
        self.calls.append((list(argv), None))
        p = FakeProc()
        self.procs.append(p)
        return p

    def download(self, url, dest):
        self.downloads.append(url)
        with open(dest, "wb") as f:
            f.write(self.assets.get(url, b"data"))

    def open_results(self, path, uid, gid):
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        os.fchmod(fd, 0o600)
        self.chowns.append((path, uid, gid))
        return os.fdopen(fd, "w")

    def rmtree(self, d):
        self.rmtrees.append(d)
        super().rmtree(d)

    def argvs(self, name):
        return [a for a, _ in self.calls if a[0] == name]

    def joined(self):
        return [" ".join(a) for a, _ in self.calls]


class Base(unittest.TestCase):
    def setUp(self):
        self._td = tempfile.TemporaryDirectory()
        self.tmp = self._td.name
        self.addCleanup(self._td.cleanup)
        self.env = {"SUDO_USER": "alice", "TERM": "xterm"}
        self.out = os.path.join(self.tmp, "results.log")
        self.sink = io.StringIO()

    def go(self, host, *args, env=None):
        return fb.main(list(args) + ["--out", self.out], host=host, env=env or self.env, out=self.sink)

    def go_bin(self, host):
        return self.go(host, BIN, __file__)

    def results(self):
        with open(self.out) as f:
            return f.read()


class DryRun(Base):
    def test_cli_exits_0_without_tty_or_root_and_prints_ordered_plan(self):
        p = subprocess.run([sys.executable, SCRIPT, "--dry-run"], stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                           stderr=subprocess.PIPE, universal_newlines=True)
        self.assertEqual(p.returncode, 0, p.stderr)
        names = [ln.split()[2] for ln in p.stdout.splitlines() if ln.startswith("[PLAN]")]
        self.assertEqual(names, ["preflight", "create-user", "install-herdr", "install-claude", "prerequisites", "logins",
                                 "follow-setup", "herdr-server", "factory-up", "web-ui"])
        out = p.stdout
        self.assertLess(out.index("useradd -m -d /ho" "me/fbtest"), out.index("herdr server"))
        self.assertLess(out.index("gh auth setup-git"), out.index("git clone"))
        self.assertLess(out.index("factory up"), out.index("factory serve --bind 127.0.0.1 --port 8799"))
        self.assertIn("runuser -u fbtest -- env -i HOME=/ho" "me/fbtest", out)
        self.assertIn("XDG_RUNTIME_DIR=/ho" "me/fbtest/.xdg-runtime", out)
        self.assertIn("--- cleanup", out)
        self.assertIn("userdel -r fbtest", out)
        self.assertNotIn("settings/applications", out)
        self.assertNotIn("revoke", out.lower())
        self.assertLess(out.index("gh auth logout -h github.com"), out.index("userdel -r fbtest"))
        self.assertTrue(out.rstrip().endswith("failed=none"))

    def test_dry_run_touches_nothing(self):
        class Boom(fb.Host):
            def count_claude(self): return 0
            def pw_dir(self, name): return "/ho" "me/someone"

        for name in ("euid", "isatty", "user_exists", "port_busy", "run", "popen", "download", "http_status", "mkdtemp",
                     "rmtree", "open_results", "sleep", "ids"):
            setattr(Boom, name, lambda *a, _n=name, **k: self.fail("dry run called host." + _n))
        rc = fb.main(["--dry-run", BIN, "/opt/bin/tool"], host=Boom(), env={"SUDO_USER": "alice"}, out=self.sink)
        self.assertEqual(rc, 0)
        self.assertIn("install -m 755 -o fbtest -g fbtest /opt/bin/tool /ho" "me/fbtest/.local/bin/", self.sink.getvalue())

    def test_default_max_total_claude_is_running_plus_four_right_before_factory_up(self):
        fb.main(["--dry-run"], host=FakeHost(self.tmp), env=self.env, out=self.sink)
        out = self.sink.getvalue()
        self.assertIn("factory config max_total_claude 7", out)
        self.assertLess(out.index("factory config max_total_claude"), out.index("factory up"))
        self.assertNotIn("max_total_claude 9", out)


class FactoryUp(Base):
    """Step 9: the cap is computed when `factory up` runs, and a busy host is INCONCLUSIVE, not FAIL."""

    QUEUED = "QUEUED boss: 7/7 claude processes on host (incl. non-factory)\n"
    NOT_LIVE = "NAME ROLE STATUS\nboss boss queued\n"

    def host(self, **kw):
        class H(FakeHost):
            def count_claude(self):  # the live factory has started more claude by the time the server is up
                return 9 if any("server" in j for j in self.joined()) else 3
        return H(self.tmp, **kw)

    def config_values(self, h):
        return [a[-1] for a in h.argvs("runuser") if "max_total_claude" in a]

    def test_cap_is_computed_at_step_9_from_the_count_at_that_moment(self):
        h = self.host()
        self.assertEqual(self.go_bin(h), 0)
        self.assertEqual(self.config_values(h), ["13"])
        j = h.joined()
        self.assertLess(next(i for i, x in enumerate(j) if "max_total_claude" in x), next(i for i, x in enumerate(j) if x.endswith("factory up")))
        self.assertIn("max_total_claude=13", self.results())

    def test_explicit_max_total_claude_is_kept(self):
        h = self.host()
        self.assertEqual(self.go(h, BIN, __file__, "--max-total-claude", "20"), 0)
        self.assertEqual(self.config_values(h), ["20"])

    def test_queued_on_the_host_cap_is_inconclusive_with_the_reason_quoted(self):
        h = self.host(rules={"factory up": (0, self.QUEUED), "factory agents": (0, self.NOT_LIVE)})
        rc = self.go_bin(h)
        res = self.results()
        self.assertEqual(rc, 3)
        self.assertIn("[INCONCLUSIVE] 9 factory-up", res)
        self.assertIn("7/7 claude processes on host", res)
        self.assertNotIn("[FAIL]", res)
        last = res.strip().splitlines()[-1]
        self.assertTrue(last.startswith("RESULT INCONCLUSIVE"), last)
        self.assertIn("failed=none", last)
        self.assertIn("inconclusive=factory-up", last)

    def test_queued_on_load_is_inconclusive(self):
        q = "QUEUED boss: load1 9.5 > 1.5 x 8 CPUs\n"
        rc = self.go_bin(self.host(rules={"factory up": (0, q), "factory agents": (0, self.NOT_LIVE)}))
        self.assertEqual(rc, 3)
        self.assertTrue(self.results().strip().splitlines()[-1].startswith("RESULT INCONCLUSIVE"))

    def test_queued_for_any_other_reason_is_fail(self):
        for q in ("QUEUED boss: herdr unreachable\n", "QUEUED boss: /tmp 900/1000 MB used (90%) >= 80%\n",
                  "QUEUED boss: 5h usage 95% >= 90%\n"):
            self.setUp()
            rc = self.go_bin(self.host(rules={"factory up": (0, q), "factory agents": (0, self.NOT_LIVE)}))
            res = self.results()
            self.assertEqual(rc, 1, q)
            self.assertIn("[FAIL] 9 factory-up", res)
            self.assertNotIn("INCONCLUSIVE", res)
            self.assertIn("RESULT FAIL", res.strip().splitlines()[-1])

    def test_inconclusive_then_cleanup_failure_is_fail_with_rc_1(self):
        h = self.host(rules={"factory up": (0, self.QUEUED), "factory agents": (0, self.NOT_LIVE), "userdel": (1, "busy")})
        rc = self.go_bin(h)
        last = self.results().strip().splitlines()[-1]
        self.assertTrue(last.startswith("RESULT FAIL"), last)
        self.assertEqual(rc, 1)

    def test_real_liveness_failure_without_a_gate_is_still_fail(self):
        h = self.host(rules={"factory agents": (0, self.NOT_LIVE)})
        rc = self.go_bin(h)
        res = self.results()
        self.assertEqual(rc, 1)
        self.assertIn("[FAIL] 9 factory-up", res)
        self.assertNotIn("INCONCLUSIVE", res)
        self.assertIn("RESULT FAIL", res.strip().splitlines()[-1])
        self.assertIn("failed=factory-up", res.strip().splitlines()[-1])

    def test_hint_names_the_flag(self):
        for rules in ({"factory up": (0, self.QUEUED), "factory agents": (0, self.NOT_LIVE)}, {"factory agents": (0, self.NOT_LIVE)}):
            self.setUp()
            self.go_bin(self.host(rules=rules))
            self.assertIn("--max-total-claude N", self.results())

    def test_help_text_says_plus_four(self):
        with self.assertRaises(SystemExit), mock.patch("sys.stdout", new_callable=io.StringIO) as o:
            fb.parse(["-h"], FakeHost(self.tmp), {})
        self.assertIn("+ 4", " ".join(o.getvalue().split()))
        self.assertNotIn("+ 6", " ".join(o.getvalue().split()))


class GhLoginAndLogout(Base):
    TOKEN = "SENTINEL-tok-9f3a1c77"

    def logout_then_kill(self, h):
        j = h.joined()
        i = next(i for i, x in enumerate(j) if x.endswith("gh auth logout -h github.com"))
        self.assertIn("runuser -u fbtest --", j[i])
        self.assertLess(i, next(i for i, x in enumerate(j) if x.startswith("pkill")))
        self.assertLess(i, next(i for i, x in enumerate(j) if x.startswith("userdel")))

    def test_no_revoke_string_in_any_output(self):
        h = FakeHost(self.tmp)
        self.assertEqual(self.go_bin(h), 0)
        self.assertNotIn("revoke", (self.results() + self.sink.getvalue()).lower())
        with open(SCRIPT) as f:
            self.assertNotIn("revoke", f.read().lower())

    def test_cleanup_logs_the_test_user_out_before_pkill_and_userdel(self):
        h = FakeHost(self.tmp)
        self.assertEqual(self.go_bin(h), 0)
        self.logout_then_kill(h)

    def test_logout_failure_does_not_stop_cleanup(self):
        h = FakeHost(self.tmp, rules={"gh auth logout": (1, "not logged in")})
        self.assertEqual(self.go_bin(h), 0)
        self.assertNotIn("fbtest", h.users)

    def test_logout_skipped_when_the_user_was_not_created(self):
        h = FakeHost(self.tmp, rules={"useradd": (1, "boom")})
        self.assertEqual(self.go_bin(h), 1)
        self.assertEqual([j for j in h.joined() if "gh auth logout" in j], [])

    def test_token_file_is_fed_on_stdin_only(self):
        tf = os.path.join(self.tmp, "tok")
        with open(tf, "w") as f:
            f.write(self.TOKEN + "\n")
        h = FakeHost(self.tmp)
        self.assertEqual(self.go(h, BIN, __file__, "--gh-token-file", tf), 0)
        argv, text = h.stdin_seen
        self.assertEqual(argv[-4:], ["gh", "auth", "login", "--with-token"])
        self.assertEqual(text, self.TOKEN + "\n")
        self.assertTrue(any(j.endswith("gh auth setup-git") for j in h.joined()))
        self.assertFalse([j for j in h.joined() if "gh auth login" in j and "--with-token" not in j])
        self.assertNotIn(self.TOKEN, "\n".join(h.joined()))
        self.assertNotIn(self.TOKEN, self.results() + self.sink.getvalue())

    def test_token_is_redacted_even_if_a_tool_echoes_it(self):
        tf = os.path.join(self.tmp, "tok")
        with open(tf, "w") as f:
            f.write(self.TOKEN)
        h = FakeHost(self.tmp, rules={"--with-token": (0, "got " + self.TOKEN)})
        self.assertEqual(self.go(h, BIN, __file__, "--gh-token-file", tf), 0)
        self.assertNotIn(self.TOKEN, self.results() + self.sink.getvalue())

    def test_dash_reads_stdin(self):
        h = FakeHost(self.tmp)
        h.stdin_token = self.TOKEN + "\n"
        self.assertEqual(self.go(h, BIN, __file__, "--gh-token-file", "-"), 0)
        self.assertEqual(h.stdin_seen[1], self.TOKEN + "\n")
        self.assertNotIn(self.TOKEN, self.results() + self.sink.getvalue() + "\n".join(h.joined()))

    def test_real_host_reads_dash_with_a_hidden_prompt_on_stderr(self):
        with mock.patch("getpass.getpass", return_value="abc\n") as gp:
            self.assertEqual(fb.Host().read_token("-"), "abc\n")
        self.assertIn("GitHub token", gp.call_args[0][0])
        self.assertIn("hidden", gp.call_args[0][0])
        self.assertIs(gp.call_args[1]["stream"], sys.stderr)

    def test_short_token_is_refused_before_any_step(self):
        h = FakeHost(self.tmp)
        h.stdin_token = "short-token\n"
        with mock.patch("sys.stderr", new_callable=io.StringIO) as e:
            self.assertEqual(self.go(h, BIN, __file__, "--gh-token-file", "-"), 2)
        self.assertIn("shorter than 20", e.getvalue())
        self.assertEqual(h.argvs("useradd"), [])
        self.assertFalse(os.path.exists(self.out))

    def test_empty_or_missing_token_file_is_refused_before_any_step(self):
        tf = os.path.join(self.tmp, "empty")
        open(tf, "w").close()
        for path in (tf, os.path.join(self.tmp, "nope")):
            h = FakeHost(self.tmp)
            with mock.patch("sys.stderr", new_callable=io.StringIO):
                self.assertEqual(self.go(h, BIN, __file__, "--gh-token-file", path), 2)
            self.assertEqual(h.argvs("useradd"), [])
            self.assertFalse(os.path.exists(self.out))

    def test_without_the_option_login_stays_interactive(self):
        h = FakeHost(self.tmp)
        self.assertEqual(self.go_bin(h), 0)
        self.assertIsNone(h.stdin_seen)
        self.assertIn((h.argvs("runuser")[[a[-3:] for a in h.argvs("runuser")].index(["gh", "auth", "login"])], False), h.calls)

    def test_dry_run_shows_logout_and_token_login(self):
        fb.main(["--dry-run", "--gh-token-file", "-"], host=FakeHost(self.tmp), env=self.env, out=self.sink)
        out = self.sink.getvalue()
        self.assertIn("gh auth login --with-token", out)
        self.assertIn("token on stdin only", out)
        self.assertLess(out.index("gh auth logout -h github.com"), out.index("userdel -r fbtest"))


class Preflight(Base):
    def refused(self, host, *args, env=None):
        rc = self.go(host, *args, env=env)
        self.assertEqual(rc, 2)
        self.assertEqual([a for a, _ in host.calls], [], "refusal must have no side effects")
        self.assertFalse(os.path.exists(self.out))

    def test_not_root(self):
        self.refused(FakeHost(self.tmp, euid=1000))

    def test_no_tty(self):
        self.refused(FakeHost(self.tmp, tty=False))

    def test_existing_user_is_never_touched(self):
        h = FakeHost(self.tmp, users={"fbtest"})
        self.refused(h)
        self.assertEqual(h.argvs("userdel"), [])
        self.assertIn("fbtest", h.users)

    def test_user_equals_sudo_user(self):
        self.refused(FakeHost(self.tmp), "--user", "alice")

    def test_user_root(self):
        with mock.patch("sys.stderr", io.StringIO()), self.assertRaises(SystemExit) as cm:
            self.go(FakeHost(self.tmp), "--user", "root")
        self.assertEqual(cm.exception.code, 2)

    def test_no_sudo_user(self):
        self.refused(FakeHost(self.tmp), env={"TERM": "xterm"})

    def test_port_busy(self):
        self.refused(FakeHost(self.tmp, busy=True))

    def test_leftover_home_without_a_user_is_refused_so_userdel_cannot_eat_it(self):
        for path in ("/ho" "me/fbtest", "/var/mail/fbtest"):
            h = FakeHost(self.tmp)
            h.leftovers = (path,)
            self.refused(h)

    def test_results_file_inside_the_test_home_is_refused_without_creating_it(self):
        h = FakeHost(self.tmp)
        rc = fb.main(["--out", "/ho" "me/fbtest/r.log"], host=h, env=self.env, out=self.sink)
        self.assertEqual(rc, 2)
        self.assertEqual(h.chowns, [])
        self.assertEqual(h.calls, [])


class Cleanup(Base):
    def assert_cleaned(self, h):
        self.assertEqual(h.argvs("userdel"), [["userdel", "-r", "fbtest"]])
        self.assertIn("id fbtest", h.joined())
        self.assertNotIn("fbtest", h.users)
        self.assertEqual(len(h.rmtrees), 1)
        self.assertIn("[PASS] cleanup verify-user-gone", self.results())
        self.assertNotIn("settings/applications", self.results())
        self.assertNotIn("revoke", self.results().lower())
        self.assertTrue(all(p.terminated for p in h.procs))

    def test_success(self):
        h = FakeHost(self.tmp)
        self.assertEqual(self.go_bin(h), 0)
        self.assert_cleaned(h)
        self.assertIn("RESULT PASS steps=10 failed=none", self.results())

    def test_fail_stops_and_cleans_up(self):
        h = FakeHost(self.tmp, rules={"factory up": (1, "boom")})
        self.assertEqual(self.go_bin(h), 1)
        self.assert_cleaned(h)
        self.assertIn("[FAIL] 9 factory-up", self.results())
        self.assertNotIn("serve --bind", " ".join(h.joined()))
        self.assertIn("RESULT FAIL steps=9 failed=factory-up", self.results())

    def test_exception_cleans_up(self):
        h = FakeHost(self.tmp, rules={"factory init": RuntimeError("bug")})
        self.assertEqual(self.go_bin(h), 1)
        self.assert_cleaned(h)
        self.assertIn("RESULT FAIL", self.results())

    def test_sigint_cleans_up(self):
        h = FakeHost(self.tmp, rules={"git clone": lambda: os.kill(os.getpid(), signal.SIGINT)})
        before = signal.getsignal(signal.SIGINT)
        self.assertEqual(self.go_bin(h), 130)
        self.assert_cleaned(h)
        self.assertIs(signal.getsignal(signal.SIGINT), before)

    def test_sigterm_cleans_up(self):
        h = FakeHost(self.tmp, rules={"git clone": lambda: os.kill(os.getpid(), signal.SIGTERM)})
        self.assertEqual(self.go_bin(h), 130)
        self.assert_cleaned(h)

    def test_sighup_cleans_up(self):
        h = FakeHost(self.tmp, rules={"git clone": lambda: os.kill(os.getpid(), signal.SIGHUP)})
        before = signal.getsignal(signal.SIGHUP)
        self.assertEqual(self.go_bin(h), 130)
        self.assert_cleaned(h)
        self.assertEqual(h.argvs("userdel"), [["userdel", "-r", "fbtest"]])
        self.assertIs(signal.getsignal(signal.SIGHUP), before)

    def test_useradd_rc9_user_appeared_is_not_touched(self):
        class Raced(FakeHost):
            def run(self, argv, capture=True, timeout=None, cwd=None):
                if argv[0] == "useradd":
                    self.calls.append((list(argv), capture))
                    self.users.add(argv[-1])
                    return 9, "user exists"
                return FakeHost.run(self, argv, capture, timeout, cwd)

        h = Raced(self.tmp)
        self.assertEqual(self.go(h), 1)
        self.assertEqual(h.argvs("userdel"), [])
        self.assertEqual(h.argvs("pkill"), [])
        self.assertIn("user appeared after preflight; not touching it", self.results())
        self.assertIn("fbtest", h.users)

    def test_successful_useradd_is_deleted(self):
        h = FakeHost(self.tmp)
        self.assertEqual(self.go_bin(h), 0)
        self.assertEqual(h.argvs("userdel"), [["userdel", "-r", "fbtest"]])

    def test_failed_useradd_deletes_nothing(self):
        h = FakeHost(self.tmp, rules={"useradd": (9, "cannot")})
        self.assertEqual(self.go(h), 1)
        self.assertEqual(h.argvs("userdel"), [])
        self.assertEqual(h.argvs("pkill"), [])

    def test_user_created_by_a_timed_out_useradd_is_still_deleted(self):
        class Slow(FakeHost):
            def run(self, argv, capture=True, timeout=None, cwd=None):
                if argv[0] == "useradd":
                    self.calls.append((list(argv), capture))
                    self.users.add(argv[-1])
                    return 124, "timed out"
                return FakeHost.run(self, argv, capture, timeout, cwd)

        h = Slow(self.tmp)
        self.assertEqual(self.go(h), 1)
        self.assertEqual(h.argvs("userdel"), [["userdel", "-r", "fbtest"]])
        self.assertNotIn("fbtest", h.users)

    def test_userdel_is_retried_until_the_user_is_gone(self):
        class Busy(FakeHost):
            n = 0

            def run(self, argv, capture=True, timeout=None, cwd=None):
                if argv[0] == "userdel":
                    self.n += 1
                    if self.n < 3:
                        self.calls.append((list(argv), capture))
                        return 8, "user is currently used by process"
                return FakeHost.run(self, argv, capture, timeout, cwd)

        h = Busy(self.tmp)
        self.assertEqual(self.go_bin(h), 0)
        self.assertEqual(len(h.argvs("userdel")), 3)
        self.assertIn("[PASS] cleanup verify-user-gone", self.results())

    def test_closed_stdout_does_not_abort_cleanup(self):
        class Broken(io.StringIO):
            def write(self, s):
                raise BrokenPipeError

        h = FakeHost(self.tmp)
        rc = fb.main(["--out", self.out, BIN, __file__], host=h, env=self.env, out=Broken())
        self.assertEqual(rc, 0)
        self.assertEqual(len(h.argvs("userdel")), 1)

    def test_idle_pkill_and_loginctl_are_not_reported_as_failures(self):
        h = FakeHost(self.tmp, rules={"pkill": (1, ""), "loginctl": (1, "User fbtest is not logged in.\n")})
        self.assertEqual(self.go_bin(h), 0)
        out = self.sink.getvalue()
        cleanup_out = out[out.index("--- cleanup"):]
        self.assertNotIn("-> rc=1", cleanup_out)
        self.assertEqual(cleanup_out.count("-> nothing to clean"), 3)
        self.assertEqual(len(h.argvs("pkill")), 2)
        self.assertEqual(len(h.argvs("loginctl")), 1)
        self.assert_cleaned(h)

    def test_user_still_present_after_cleanup_is_a_failure(self):
        h = FakeHost(self.tmp, rules={"userdel": (1, "busy")})
        self.assertEqual(self.go_bin(h), 1)
        self.assertIn("[FAIL] cleanup verify-user-gone", self.results())

    def test_only_the_users_own_things_are_killed_or_removed(self):
        h = FakeHost(self.tmp)
        self.go_bin(h)
        for argv in h.argvs("pkill"):
            self.assertEqual(argv[2:], ["-u", "fbtest"])
        self.assertEqual(h.argvs("rm"), [])
        self.assertEqual(len(h.rmtrees), 1)
        self.assertTrue(os.path.basename(h.rmtrees[0]).startswith("freshbox-"))


class Download(Base):
    def pinned(self, content, sha=None, size=None):
        url = "https://example.test/asset"
        row = (url, len(content) if size is None else size, sha or hashlib.sha256(content).hexdigest())
        patch = mock.patch.multiple(fb, HERDR_ASSETS={"x86_64": row, "aarch64": row})
        return patch, {url: content}

    def installs_pinned(self, h):
        return [a for a in h.argvs("install") if a[-1].endswith(".local/bin/herdr")]

    def test_sha256_mismatch_aborts_before_install(self):
        patch, assets = self.pinned(b"evil", sha="0" * 64)
        h = FakeHost(self.tmp, assets=assets)
        with patch:
            self.assertEqual(self.go(h), 1)
        self.assertIn("[FAIL] 3 install-herdr", self.results())
        self.assertIn("sha256 mismatch", self.results())
        self.assertEqual(self.installs_pinned(h), [])
        self.assertEqual(len(h.argvs("userdel")), 1)

    def test_size_mismatch_aborts_before_install(self):
        patch, assets = self.pinned(b"abcd", size=99)
        h = FakeHost(self.tmp, assets=assets)
        with patch:
            self.assertEqual(self.go(h), 1)
        self.assertIn("size mismatch", self.results())
        self.assertEqual(self.installs_pinned(h), [])

    def test_verified_download_installs(self):
        patch, assets = self.pinned(b"good")
        h = FakeHost(self.tmp, assets=assets)
        with patch:
            self.assertEqual(self.go(h), 0)
        self.assertEqual(h.downloads[0], "https://example.test/asset")
        self.assertEqual(self.installs_pinned(h)[0][:6], ["install", "-m", "755", "-o", "fbtest", "-g"])

    def test_no_pinned_release_for_this_arch_fails_with_advice(self):
        with mock.patch.multiple(fb, HERDR_ASSETS={}):
            self.assertEqual(self.go(FakeHost(self.tmp)), 1)
        self.assertIn("no pinned", self.results())

    def test_pinned_constants(self):
        self.assertEqual(fb.HERDR_VERSION, "v0.9.5")
        for url, size, sha in fb.HERDR_ASSETS.values():
            self.assertTrue(url.startswith("https://github.com/herdrdev/herdr/releases/download/v0.9.5/"))
            self.assertRegex(sha, r"^[0-9a-f]{64}$")
            self.assertGreater(size, 1000000)

    def test_binary_flag_copies_instead_of_downloading(self):
        h = FakeHost(self.tmp)
        self.assertEqual(self.go_bin(h), 0)
        self.assertNotIn("https://example.test/asset", h.downloads)
        self.assertEqual(self.installs_pinned(h)[0][-2], __file__)

    def test_claude_installer_is_a_file_run_as_the_user(self):
        h = FakeHost(self.tmp)
        self.go_bin(h)
        self.assertEqual(h.downloads, [fb.CLAUDE_INSTALLER_URL])
        run = [a for a in h.argvs("runuser") if "bash" in a][0]
        self.assertEqual(run[:4], ["runuser", "-u", "fbtest", "--"])
        self.assertTrue(run[-1].endswith("claude-install.sh"))
        self.assertRegex(self.results(), r"installer sha256 [0-9a-f]{64}")

    def test_non_https_download_is_refused(self):
        with self.assertRaises(fb.StepFail):
            fb.Host().download("http://example.test/x", os.path.join(self.tmp, "x"))


class RealDownload(Base):
    """Host.download as shipped, with only urlopen mocked (the network boundary)."""

    class Fetching(FakeHost):
        download = fb.Host.download

    def test_every_download_sends_the_explicit_user_agent(self):
        seen = []

        def fake_urlopen(req, timeout=None):
            seen.append((req.full_url, req.get_header("User-agent")))
            return io.BytesIO(b"data")

        h = self.Fetching(self.tmp)
        with mock.patch.object(fb.urllib.request, "urlopen", fake_urlopen):
            self.assertEqual(self.go(h), 1)  # the pinned release asset never matches b"data"
            fb.Host().download(fb.CLAUDE_INSTALLER_URL, os.path.join(self.tmp, "inst.sh"))
        self.assertGreaterEqual(len(seen), 2)
        self.assertEqual({ua for _, ua in seen}, {"factory-freshbox/1.0"})
        self.assertIn(fb.CLAUDE_INSTALLER_URL, [u for u, _ in seen])
        self.assertTrue(any(u.startswith("https://github.com/herdrdev/") for u, _ in seen))

    def test_http_error_names_step_and_url(self):
        def fake_urlopen(req, timeout=None):
            raise fb.urllib.error.HTTPError(req.full_url, 403, "Forbidden", {}, None)

        h = self.Fetching(self.tmp)
        with mock.patch.object(fb.urllib.request, "urlopen", fake_urlopen):
            self.assertEqual(self.go_bin(h), 1)
        r = self.results()
        self.assertIn("[FAIL] 4 install-claude: HTTP 403 for https://claude.ai/install.sh", r)
        self.assertNotIn("unexpected", r)
        self.assertEqual(len(h.argvs("userdel")), 1)

    def test_url_error_names_step_and_url(self):
        def fake_urlopen(req, timeout=None):
            raise fb.urllib.error.URLError("name resolution failed")

        h = self.Fetching(self.tmp)
        with mock.patch.object(fb.urllib.request, "urlopen", fake_urlopen):
            self.assertEqual(self.go_bin(h), 1)
        self.assertIn("[FAIL] 4 install-claude: cannot fetch https://claude.ai/install.sh: name resolution failed", self.results())


class CleanupSkipped(Base):
    def test_server_stop_skipped_when_server_never_started(self):
        h = FakeHost(self.tmp, rules={"claude-install": (1, "boom")})
        self.assertEqual(self.go_bin(h), 1)
        self.assertIn("server stop -> skipped (not started)", self.results())
        self.assertFalse([a for a in h.joined() if "server stop" in a])
        self.assertEqual(h.argvs("userdel"), [["userdel", "-r", "fbtest"]])

    def test_server_stop_runs_when_server_started(self):
        h = FakeHost(self.tmp)
        self.assertEqual(self.go_bin(h), 0)
        self.assertTrue([a for a in h.joined() if "server stop" in a])
        self.assertNotIn("skipped (not started)", self.results())


class Redaction(unittest.TestCase):
    def test_tokens_and_auth_headers_are_redacted(self):
        secrets = ["gho_" + "a1B2c3D4e5F6g7H8i9J0k1L2", "ghp_" + "A" * 36, "ghs_" + "b" * 30, "ghu_" + "c" * 30, "ghr_" + "d" * 30,
                   "github_pat_" + "11ABCDEFG0123456789_abcdef", "sk-ant-" + "api03-abcdefghijklmnop"]
        for s in secrets:
            out = fb.redact("token=%s end" % s)
            self.assertNotIn(s, out)
            self.assertIn("[REDACTED]", out)
        out = fb.redact("curl -H 'Authorization: Bearer abc.def' x")
        self.assertNotIn("abc.def", out)
        self.assertNotIn("TOPSECRET", fb.redact("git clone https://bob:TOPSECRET" + "@" + "example.test/x.git"))
        self.assertEqual(fb.redact("plain text, sk-learn, ghost"), "plain text, sk-learn, ghost")


class ResultsFile(Base):
    def test_mode_600_chown_to_sudo_user_and_content(self):
        secret = "gho_" + "z" * 30
        h = FakeHost(self.tmp, rules={"factory init": (0, "initialised x\ntoken %s\n" % secret)})
        self.assertEqual(self.go_bin(h), 0)
        self.assertEqual(stat.S_IMODE(os.stat(self.out).st_mode), 0o600)
        self.assertEqual(h.chowns, [(self.out, 4242, 4243)])
        text = self.results()
        self.assertIn("[PASS] 1 preflight", text)
        self.assertIn("[PASS] 10 web-ui", text)
        self.assertIn("RESULT PASS steps=10 failed=none", text)
        self.assertNotIn(secret, text)
        self.assertNotIn(secret, self.sink.getvalue())

    def test_default_path_is_under_the_invoker_home(self):
        h = FakeHost(self.tmp)
        now = datetime.datetime(2026, 1, 2, 3, 4, 5)
        rc = fb.main([BIN, __file__], host=h, env=self.env, now=now, out=self.sink)
        self.assertEqual(rc, 0)
        self.assertEqual(h.chowns[0][0], os.path.join(h.inv_home, "factory-freshbox-20260102T030405Z.log"))

    def test_login_output_is_never_captured(self):
        h = FakeHost(self.tmp)
        self.go_bin(h)
        want = (["claude"], ["gh", "auth", "login"], ["gh", "auth", "setup-git"])
        logins = [(a, cap) for a, cap in h.calls if a[0] == "runuser" and any(a[-len(w):] == w for w in want)]
        self.assertEqual(len(logins), 3)
        self.assertTrue(all(cap is False for _, cap in logins), "logins must pass the TTY through, not capture")

    def test_no_write_under_the_invoker_home_except_results(self):
        h = FakeHost(self.tmp)
        self.go_bin(h)
        for j in h.joined():
            self.assertNotIn(h.inv_home, j)
        self.assertEqual([c[0] for c in h.chowns], [self.out])

    def test_refuses_to_run_if_results_file_exists(self):
        open(self.out, "w").close()
        h = FakeHost(self.tmp)
        self.assertEqual(self.go(h), 2)
        self.assertEqual(h.calls, [])

    def test_every_user_command_is_pinned_to_the_test_home(self):
        h = FakeHost(self.tmp)
        self.go_bin(h)
        seen = 0
        for argv, _ in h.calls:
            if argv[0] == "runuser":
                seen += 1
                self.assertEqual(argv[1:4], ["-u", "fbtest", "--"])
                self.assertIn("HOME=/ho" "me/fbtest", argv)
                self.assertIn("XDG_RUNTIME_DIR=/ho" "me/fbtest/.xdg-runtime", argv)
                self.assertNotIn("FACTORY_HOME", " ".join(argv))
        self.assertGreater(seen, 15)


class Source(unittest.TestCase):
    def setUp(self):
        with open(SCRIPT) as f:
            self.src = f.read()

    def test_no_shell_true(self):
        self.assertNotIn("shell=True", self.src)
        self.assertNotIn("os.system", self.src)

    def test_no_curl_pipe_to_shell(self):
        self.assertNotRegex(self.src, r"\|\s*(ba|z)?sh\b")

    def test_python_38_syntax(self):
        ast.parse(self.src, feature_version=(3, 8))

    def test_default_repo_is_the_placeholder(self):
        self.assertEqual(fb.DEFAULT_REPO, "https://github.com/example-org/factory.git")
        self.assertNotIn("/ho" "me/", self.src.replace('"/home"', ""))


if __name__ == "__main__":
    unittest.main()
