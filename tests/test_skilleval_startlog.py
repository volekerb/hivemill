"""T-0495: evaluator start diagnostics. The claude shim and the evaluator spawn write <trial>/log/start.log
(pre-exec failures with a traceback, stage-1 rc and stderr), capped at 64 KiB, never holding the OAuth token;
`factory skilleval results|list` show the last rc and <= 20 tail lines for error / lost / running-without-tokens trials.

Group A (shim, CLI) needs no namespaces. The stage-1 test (StageOneTests) runs the recorded evaluator spawn command
with a fake claude inside real user namespaces and skips with the reason where they are restricted.
No real claude, no real token: fake scripts and a dummy token string in temp dirs.
"""
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import test_skilleval_sandbox as sb  # noqa: E402  (module import: its test classes are not collected twice)
from harness import FactoryTestCase, REPO  # noqa: E402

sys.path.insert(0, str(REPO))
from scout import egress, nsbox, skilleval  # noqa: E402

TOKEN = "sk-ant-" + "oat01-STARTLOGDUMMYTOKEN-9f3a1c"  # split so the repo secret scan does not flag the dummy
UNSHARE = os.environ.get("NSBOX_TEST_UNSHARE", "unshare")
USERNS_OK, USERNS_WHY = nsbox.userns_available(UNSHARE)
SKIP_MSG = f"user namespaces unavailable: {USERNS_WHY}"
FAKE_CLAUDE = "#!/bin/sh\nexit 0\n"
CAP = 64 * 1024


class ShimBase(unittest.TestCase):
    """A temp world: <base>/trials/tr0001 (the trial), a token file, a fake claude, and empty cwd/home/tmp dirs."""

    def setUp(self):
        self.base = Path(tempfile.mkdtemp(prefix="zz-test-startlog-")).resolve()
        self.addCleanup(shutil.rmtree, self.base, True)
        self.trial = self.base / "trials" / "tr0001"
        self.trial.mkdir(parents=True)
        self.home = self.base / "home"
        self.cwd = self.base / "cwd"
        self.tmp = self.base / "tmp"
        for d in (self.home, self.cwd, self.tmp):
            d.mkdir()
        self.token_file = self.base / "token"
        self.token_file.write_text(TOKEN + "\n")
        self.token_file.chmod(0o600)
        self.claude = self.base / "bin" / "claude"
        self.claude.parent.mkdir()
        self.claude.write_text(FAKE_CLAUDE)
        self.claude.chmod(0o755)
        self.fake_unshare = self.base / "bin" / "unshare"
        self.fake_unshare.write_text("#!/bin/sh\nexit 0\n")
        self.fake_unshare.chmod(0o755)
        self.log = self.trial / "log" / "start.log"

    def snapshot(self):
        """Every path under base except the trial dir: nothing may be created, changed or removed there."""
        out = {}
        for p in sorted(self.base.rglob("*")):
            if p == self.trial or self.trial in p.parents:
                continue
            out[str(p)] = (p.stat().st_size, p.stat().st_mtime_ns) if p.is_file() else "dir"
        return out

    def make_shim(self, **kw):
        kw.setdefault("token_file", str(self.token_file))
        kw.setdefault("unshare", str(self.fake_unshare))
        kw.setdefault("proxy_max_life", 5)
        return nsbox.write_shim(self.trial, self.claude, home=self.home, trials_root=self.trial.parent, **kw)

    def run_shim(self, shim, *args):
        env = {"PATH": os.environ["PATH"], "HOME": str(self.home), "TMPDIR": str(self.tmp)}
        p = subprocess.run([str(shim), *args], env=env, cwd=self.cwd, capture_output=True, text=True, timeout=60)
        self.addCleanup(nsbox.stop_proxy, str(self.trial))
        return p

    def log_text(self):
        self.assertTrue(self.log.is_file(), f"no {self.log}")
        return self.log.read_text(errors="replace")

    def shim_cfg(self, **over):
        ns = nsbox.make_cfg(self.trial, str(self.claude), str(self.home), str(self.trial.parent),
                            unshare=str(self.fake_unshare), forward=True)
        return {"ns": ns, "token_file": str(self.token_file), "proxy_allow": egress.ALLOW, "proxy_max_life": 5,
                "upstream": {}, **over}


class PreExecFailureTests(ShimBase):
    """AC1: a failure before os.execve is logged with type, message and traceback, and the shim exits non-zero."""

    def assert_failure_record(self, p, needle=None):
        self.assertNotEqual(p.returncode, 0, p.stdout + p.stderr)
        text = self.log_text()
        self.assertIn("Traceback", text)
        self.assertRegex(text, r"\b\w*(Error|Exit|Exception)\b")
        self.assertRegex(text, r"20\d\d-\d\d-\d\d", "the record carries a timestamp")
        if needle:
            self.assertIn(needle, text)
        return text

    def test_missing_token_file_is_logged_and_exits_non_zero(self):
        missing = self.base / "no-such-token"
        p = self.run_shim(self.make_shim(token_file=str(missing)))
        self.assert_failure_record(p)

    def test_unreadable_token_file_is_logged_and_exits_non_zero(self):
        self.token_file.chmod(0o644)  # read_token insists on mode 0600
        p = self.run_shim(self.make_shim())
        self.assert_failure_record(p)

    def test_start_proxy_failure_is_logged_with_its_message(self):
        ns = self.shim_cfg()["ns"]
        ns["python"] = "/nonexistent-python-for-startlog/py"  # start_proxy's Popen raises FileNotFoundError
        shim = self.make_shim(ns=ns)
        p = self.run_shim(shim)
        text = self.assert_failure_record(p, "FileNotFoundError")
        self.assertIn("nonexistent-python-for-startlog", text)

    def test_successful_pre_exec_phase_logs_a_stage1_exec_record_without_env(self):
        p = self.run_shim(self.make_shim(), "--version")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)  # the fake unshare exits 0
        text = self.log_text()
        self.assertIn("stage1 exec", text)
        self.assertIn(str(self.fake_unshare), text)  # argv[0] only
        for secret in (TOKEN, "CLAUDE_CODE_OAUTH_TOKEN", "PATH=", "HOME="):
            self.assertNotIn(secret, text)


class StageOneTests(sb.SandboxBase):
    """AC2: the stage-1 child's exit status and stderr reach the trial's start.log."""

    @unittest.skipUnless(USERNS_OK, SKIP_MSG)
    def test_stage1_rc_and_stderr_reach_the_trial_log(self):
        self.claude.write_text("#!/bin/sh\nif [ \"$1\" = --version ]; then echo 9.9.9; exit 0; fi\n"
                               "echo boom-xyz >&2\nexit 3\n")
        self.claude.chmod(0o755)
        with self.env.load_factory() as f:
            info = self.build(f.db())
            self.assertEqual(f.spawn(f.db(), "evaluator", **self.spawn_kwargs(info)), info["agent"])
        trial = Path(info["dir"]).resolve()
        (start,) = self.start_calls()
        cmd = start[start.index("--") + 1:]
        if cmd[0].startswith("-"):  # the pane manager launches `claude` itself, found on the pane PATH: the trial shim first
            cmd = ["claude", *cmd]
        env = self.env_of(self.pane_call())
        env["HOME"] = str(trial / "home")
        env["NSBOX_TEST_UNSHARE"] = UNSHARE
        p = subprocess.run(cmd, env=env, cwd=trial / "work", stdin=subprocess.DEVNULL, capture_output=True,
                           text=True, timeout=120)
        log = trial / "log" / "start.log"
        self.assertTrue(log.is_file(), f"no {log}; rc={p.returncode} stderr={p.stderr!r}")
        text = log.read_text(errors="replace")
        self.assertRegex(text, r"\brc=3\b")
        self.assertIn("boom-xyz", text)
        self.assertNotIn(TOKEN, text)


class RedactionTests(ShimBase):
    """AC4 (shim side): the whole token never reaches start.log, not even through an exception message."""

    def test_token_in_an_exception_message_is_redacted(self):
        ns = self.shim_cfg()["ns"]
        ns["python"] = f"/nonexistent/{TOKEN}/py"  # the FileNotFoundError text contains the token
        p = self.run_shim(self.make_shim(ns=ns))
        self.assertNotEqual(p.returncode, 0)
        text = self.log_text()
        self.assertIn("FileNotFoundError", text)
        self.assertEqual(text.count(TOKEN), 0)
        grep = subprocess.run(["grep", "-rF", TOKEN, str(self.trial / "log")], capture_output=True, text=True)
        self.assertEqual(grep.stdout, "", "grep -F of the token in the trial log must give 0 hits")


class CapTests(ShimBase):
    """AC5: start.log stays <= 64 KiB after 1 MiB of writes."""

    def test_log_is_capped_at_64_kib_after_1_mib_of_failures(self):
        ns = self.shim_cfg()["ns"]
        ns["python"] = "/nonexistent/" + "/".join(["a" * 200] * 18)  # ~3.6 KB message per failure
        cfg = self.shim_cfg(ns=ns)
        for _ in range(400):  # ~1.4 MiB of records in all
            try:
                nsbox.shim_main(cfg, [])
            except (SystemExit, Exception):  # the shim exits non-zero; a propagated error also counts: the log is what matters
                pass
        self.assertTrue(self.log.is_file())
        size = self.log.stat().st_size
        self.assertGreater(size, 0)
        self.assertLessEqual(size, CAP, f"start.log is {size} bytes")


class ContainmentTests(ShimBase):
    """AC6: the log path is <trial>/log/start.log and nothing is written outside the trial dir."""

    def test_failing_shim_writes_only_inside_the_trial_dir(self):
        shim = self.make_shim(token_file=str(self.base / "no-such-token"))
        before = self.snapshot()
        p = self.run_shim(shim)
        self.assertNotEqual(p.returncode, 0)
        self.assertTrue(self.log_text())
        self.assertEqual(self.log, self.trial / "log" / "start.log")
        self.assertEqual(self.snapshot(), before)
        for d in (self.cwd, self.home, self.tmp):
            self.assertEqual(list(d.iterdir()), [], f"{d} was written to")


class SkillevalOutputTests(FactoryTestCase):
    """AC3 + AC4 (CLI side): `factory skilleval results|list` show the last rc and <= 20 log lines, redacted."""

    def setUp(self):
        super().setUp()
        self.root = self.env.home / "evalroot"
        self.ok("config", "skilleval_root", str(self.root), agent="owner")
        token = self.env.user_home / ".config" / "factory" / "eval-oauth-token"
        token.parent.mkdir(parents=True)
        token.write_text(TOKEN + "\n")
        token.chmod(0o600)
        self.env.sql("INSERT INTO scout_candidates(id,key,repo,head_sha,install_risk,status,sources,first_seen,last_seen) "
                     "VALUES('C-0001','gh:o/r','o/r',?,'prompt-only','new','[]','t','t')", ("c" * 40,))
        self.n = 0

    def trial(self, status, task, tokens=None, lines=None, rc=3):
        self.n += 1
        d = self.root / "trials" / f"tr{self.n:04d}"
        d.mkdir(parents=True)
        if lines is not None:
            (d / "log").mkdir()
            (d / "log" / "start.log").write_text("\n".join(lines + [f"rc={rc}"]) + "\n")
        self.env.sql("INSERT INTO skill_trials(id,candidate_id,vendor_sha,task,condition,status,agent,sandbox,queued,"
                     "tokens_in,tokens_out) VALUES(?,?,?,?,?,?,?,?,'t',?,?)",
                     (self.n, "C-0001", "c" * 40, task, "with", status, f"eval-tr{self.n:04d}", str(d), tokens, tokens))
        return d

    def outputs(self):
        return [self.ok("skilleval", verb, "--candidate", "C-0001", agent="owner").stdout for verb in ("results", "list")]

    def test_error_and_lost_trials_show_last_rc_and_at_most_20_tail_lines(self):
        body = [f"start-line-{i:02d}" for i in range(1, 41)]
        self.trial("error", "fix-bug", lines=body, rc=3)
        for out in self.outputs():
            self.assertIn("rc=3", out)
            self.assertIn("start-line-40", out)
            self.assertNotIn("start-line-01", out)
            self.assertNotIn("start-line-10", out)
            shown = re.findall(r"start-line-\d\d", out)
            self.assertTrue(1 <= len(shown) <= 20, f"{len(shown)} tail lines in {out!r}")

    def test_token_shape_joined_by_a_stripped_control_is_redacted_in_results_and_list(self):
        rest = "A" * 30
        # \x00 and \x9b are stripped; \x1c and \x0b are line breaks for str.splitlines, so they must not split the token
        self.trial("error", "fix-bug", lines=["boom sk-ant-\x00" + rest, "x sk-ant-\x1c" + rest, "y sk-ant-\x0b" + rest,
                                              "z sk-ant-\x9b" + rest], rc=3)
        for out in self.outputs():
            self.assertNotIn("sk-ant-", out)
            self.assertNotIn(rest, out)
            self.assertIn("[redacted-token]", out)

    def test_replacement_and_format_chars_inside_a_token_are_stripped_in_results_and_list(self):
        rest = "A" * 30
        d = self.trial("error", "fix-bug", lines=[], rc=3)
        (d / "log" / "start.log").write_bytes(
            b"boom sk-ant-\xff" + rest.encode() + b"\nx sk-ant-\xe2\x80\x8b" + rest.encode() + b"\ny sk-ant-\xef\xbb\xbf"
            + rest.encode() + b"\nz sk-ant-\xe2\x81\xa0" + rest.encode() + b"\ntab\there\nrc=3\n")
        for out in self.outputs():
            self.assertNotIn("sk-ant-", out)
            self.assertNotIn(rest, out)
            self.assertNotIn("\ufffd", out)
            self.assertNotIn("\u200b", out)
            self.assertIn("[redacted-token]", out)
            self.assertIn("tab\there", out)

    def test_c1_controls_are_not_displayed(self):
        self.trial("error", "fix-bug", lines=["a\x9bb\x85c\x80d\x9fe"], rc=3)
        for out in self.outputs():
            self.assertIn("abcde", out)
            for ch in "\x9b\x85\x80\x9f":
                self.assertNotIn(ch, out)

    def test_redaction_runs_only_on_the_displayed_lines(self):
        calls = []
        real = skilleval.nsbox._TOKEN_SHAPE
        class Spy:
            def sub(self, repl, text):
                calls.append(text)
                return real.sub(repl, text)
        with tempfile.TemporaryDirectory(prefix="zz-test-startlog-spy-") as d:
            nsbox.start_log_append(Path(d), "\n".join(f"line-{i:03d}" for i in range(100)) + "\n")
            row = {"status": "error", "tokens_in": None, "tokens_out": None, "sandbox": d}
            skilleval.nsbox._TOKEN_SHAPE = Spy()
            try:
                skilleval.start_log_lines(row)
            finally:
                skilleval.nsbox._TOKEN_SHAPE = real
        self.assertEqual(len(calls), 20)

    def test_running_without_tokens_shows_the_log_but_running_with_tokens_does_not(self):
        self.trial("running", "fix-bug", lines=["stuck-without-tokens-mark"], rc=137)
        self.trial("running", "add-feature", tokens=500, lines=["healthy-running-mark"], rc=0)
        for out in self.outputs():
            self.assertIn("stuck-without-tokens-mark", out)
            self.assertIn("rc=137", out)
            self.assertNotIn("healthy-running-mark", out)

    def test_passed_trial_shows_no_extra_lines_and_an_error_trial_without_a_log_still_lists(self):
        self.trial("passed", "fix-bug", tokens=100, lines=["passed-log-mark"], rc=0)
        self.trial("error", "add-feature", lines=["error-log-mark"], rc=3)
        self.trial("error", "no-log-task")  # no start.log at all: listed as before, no crash
        for out in self.outputs():
            self.assertIn("trial 1 status=passed", out)
            self.assertIn("trial 3 status=error", out)
            self.assertNotIn("passed-log-mark", out)
            self.assertIn("error-log-mark", out)  # the error trial's log is shown ...
            self.assertEqual(out.count("rc="), 1, out)  # ... and only that one: no rc for passed or log-less trials

    def test_the_token_never_appears_in_results_or_list(self):
        self.trial("error", "fix-bug", lines=["before", f"claude failed: bad token {TOKEN} rejected", "after"], rc=1)
        for out in self.outputs():
            self.assertIn("rc=1", out)
            self.assertIn("after", out)  # the log is shown ...
            self.assertEqual(out.count(TOKEN), 0)  # ... but not the token
            self.assertNotIn("oat01-STARTLOGDUMMYTOKEN", out)

    def test_showing_results_writes_nothing_into_the_trial_dir(self):
        d = self.trial("error", "fix-bug", lines=["x"], rc=3)
        before = {str(p): p.stat().st_mtime_ns for p in d.rglob("*")}
        for out in self.outputs():
            self.assertIn("rc=3", out)
        self.assertEqual({str(p): p.stat().st_mtime_ns for p in d.rglob("*")}, before)


if __name__ == "__main__":
    unittest.main()
