"""G-010 T-0162: the sandbox_roles flag, the fail-closed spawn preflight, and the rollback golden test.

Capture of tests/fixtures/spawn_golden_main.json (done once, on origin/main, before the change):
    git archive origin/main | tar -x -C <empty dir>; cp tests/test_sandbox_spawn.py <dir>/tests/
    cd <dir> && python3 tests/test_sandbox_spawn.py --capture > tests/fixtures/spawn_golden_main.json
"""
import ast
import json
import os
import re
import shutil
import stat
import subprocess
import sys
import unittest
from pathlib import Path

from harness import FACTORY, FIXTURES, REPO, FactoryEnv, FactoryTestCase

sys.path.insert(0, str(REPO))
from scout import nsbox, workerbox  # noqa: E402

GOLDEN = FIXTURES / "spawn_golden_main.json"
MARKER = "zz-test-token-marker-5c1e9d"
ROLE_NAMES = {"lead": "zz-lead", "reviewer": "zz-reviewer", "qa": "zz-qa", "coder": "zz-coder"}
UUID = re.compile(r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}")


def userns_here():
    return nsbox.userns_available()[0]


def prep(env, fake_unshare=True):
    """Stubs and files a sandboxed spawn needs: a fake claude, a fake unshare (userns ok), the three token files
    (dummy content: MARKER), and one cwd per worker role (qa/coder get a .git directory = a per-task clone)."""
    claude = env.stub_bin / "claude"
    claude.write_text("#!/bin/sh\nexit 0\n")
    claude.chmod(0o755)
    if fake_unshare:
        unshare = env.stub_bin / "unshare"
        unshare.write_text("#!/bin/sh\nexit 0\n")
        unshare.chmod(0o755)
    tdir = env.user_home / ".config" / "factory"
    tdir.mkdir(parents=True)
    for name in ("worker-oauth-token", "gh-worker-ro-token", "gh-worker-rw-token"):
        (tdir / name).write_text(MARKER + "\n")
        (tdir / name).chmod(0o600)
    env.seed_goal()
    env.seed_team(lead="zz-lead")
    for role in ("reviewer", "qa", "coder"):
        wt = env.home_dir / "worktrees" / f"w-{role}"
        wt.mkdir(parents=True)
        if role != "reviewer":
            (wt / ".git").mkdir()


def cwd_of(env, role):
    return str(env.home_dir / "worktrees" / f"w-{role}")


def spawn_args(env, role, name=None, cwd=True):
    a = ["spawn", role, "--name", name or ROLE_NAMES[role], "--goal", "G-001", "--team", "g-001-a"]
    if cwd and role != "lead":
        a += ["--cwd", cwd_of(env, role)]
    return a


def normalise(env, argv):
    s = json.dumps(argv)
    s = s.replace(str(env.home_dir), "<HOME>").replace(str(env.home), "<TMP>")
    return json.loads(UUID.sub("<SID>", s))


def record(configure=None):
    """{role: [normalised multiplexer argv, ...]} for a lead, reviewer, qa and coder spawn in a fresh temp home."""
    env = FactoryEnv()
    try:
        prep(env)
        if configure:
            configure(env)
        out = {}
        for role in ("lead", "reviewer", "qa", "coder"):
            before = len(env.calls("herdr"))
            p = env.run(*spawn_args(env, role))
            assert p.returncode == 0, f"{role}: {p.stdout}{p.stderr}"
            out[role] = [normalise(env, c["argv"]) for c in env.calls("herdr")[before:]]
        return out
    finally:
        env.cleanup()


def strip_path_env(argv):
    """argv without its `--env PATH=...` pair, and the removed values."""
    out, gone, i = [], [], 0
    while i < len(argv):
        if argv[i] == "--env" and argv[i + 1].startswith("PATH="):
            gone.append(argv[i + 1])
            i += 2
        else:
            out.append(argv[i])
            i += 1
    return out, gone


class SandboxCase(FactoryTestCase):
    def setUp(self):
        super().setUp()
        prep(self.env)

    def agents(self, name):
        return self.env.sql("SELECT * FROM agents WHERE name=?", (name,))

    def events(self, action="sandbox-refused"):
        return self.env.sql("SELECT entity_id, detail FROM events WHERE action=?", (action,))

    def mux(self, *words):
        return [c["argv"] for c in self.env.calls("herdr") if c["argv"][:len(words)] == list(words)]

    def set_roles(self, value):
        self.ok("config", "sandbox_roles", value)

    def spawn(self, role, name=None, cwd=True):
        return self.env.run(*spawn_args(self.env, role, name, cwd))

    def assert_refused(self, role, reason, name=None, cwd=True):
        name = name or ROLE_NAMES[role]
        calls_before = len(self.env.calls("herdr"))
        p = self.spawn(role, name, cwd)
        self.assertNotEqual(p.returncode, 0, p.stdout)
        self.assertIn("sandbox refused", p.stderr)
        new = [c["argv"][:2] for c in self.env.calls("herdr")[calls_before:]]
        for bad in (["pane", "split"], ["tab", "create"], ["agent", "start"], ["pane", "run"], ["workspace", "create"]):
            self.assertNotIn(bad, new)
        self.assertEqual([r for r in self.agents(name) if r["status"] == "live"], [])
        ev = [json.loads(r["detail"]) for r in self.events() if r["entity_id"] == name]
        self.assertEqual([e["reason"] for e in ev][-1:], [reason], ev)
        self.assertEqual(ev[-1]["role"], role)
        return p


class ConfigGuardTests(FactoryTestCase):
    def value(self):
        out = self.ok("config", "sandbox_roles").stdout
        return re.search(r"^sandbox_roles = (.*)$", out, re.M).group(1)

    def test_blocked_roles_exit_2_value_unchanged(self):
        self.ok("config", "sandbox_roles", "reviewer")
        for bad in ("judge", "lead", "boss", "cleaner", "evaluator", "foo", "pm", "director", "staff-engineer",
                    "scout", "reviewer,judge", "reviewer,,qa"):
            p = self.refused(2, "config", "sandbox_roles", bad)
            self.assertIn("cannot be sandboxed", p.stderr, bad)
            self.assertEqual(self.value(), "reviewer", bad)

    def test_allowed_roles_stored_and_printed(self):
        self.ok("config", "sandbox_roles", "reviewer,qa")
        self.assertEqual(self.value(), "reviewer,qa")
        self.ok("config", "sandbox_roles", " coder , reviewer ")
        self.assertEqual(self.value(), "coder,reviewer")
        self.ok("config", "sandbox_roles", "")
        self.assertEqual(self.value(), "")

    def test_default_is_off(self):
        self.assertEqual(self.value(), "")

    def test_a_worker_may_not_set_it(self):
        self.env.seed_agent("zz-coder", "coder")
        self.refused(3, "config", "sandbox_roles", "reviewer", agent="zz-coder")


class GoldenTests(unittest.TestCase):
    """SC3, the rollback: the fixture is what origin/main did (see the module doc for how it was captured)."""

    @classmethod
    def setUpClass(cls):
        cls.golden = json.loads(GOLDEN.read_text())

    def test_golden_flag_unset_is_byte_identical_to_main(self):
        self.assertEqual(json.dumps(record(), sort_keys=True), json.dumps(self.golden, sort_keys=True))

    def test_golden_flag_empty_is_byte_identical_to_main(self):
        got = record(lambda env: env.run("config", "sandbox_roles", ""))
        self.assertEqual(json.dumps(got, sort_keys=True), json.dumps(self.golden, sort_keys=True))

    def test_golden_flag_on_then_off_is_byte_identical_to_main(self):
        def toggle(env):
            for v in ("reviewer", ""):
                self.assertEqual(env.run("config", "sandbox_roles", v).returncode, 0)
        self.assertEqual(json.dumps(record(toggle), sort_keys=True), json.dumps(self.golden, sort_keys=True))

    def test_golden_reviewer_on_differs_only_by_the_reviewer_pane_path(self):
        got = record(lambda env: env.run("config", "sandbox_roles", "reviewer"))
        for role in ("lead", "qa", "coder"):
            self.assertEqual(got[role], self.golden[role], role)
        stripped = [strip_path_env(a) for a in got["reviewer"]]
        self.assertEqual([a for a, _ in stripped], self.golden["reviewer"])
        gone = [g for _, gs in stripped for g in gs]
        self.assertEqual(len(gone), 1, gone)
        self.assertTrue(gone[0].startswith("PATH=<TMP>/userhome/.local/state/factory/workers/zz-reviewer/bin:"), gone)
        self.assertEqual([c for c in self.golden["reviewer"] if strip_path_env(c)[1]], [])


class PreflightBlockedTests(SandboxCase):
    def setUp(self):
        super().setUp()
        self.set_roles("reviewer,qa,coder")

    def test_userns_unavailable(self):
        (self.env.stub_bin / "unshare").write_text("#!/bin/sh\necho denied >&2\nexit 1\n")
        self.assert_refused("reviewer", "userns")

    def test_legacy_db_layout(self):
        legacy = self.env.home_dir / "factory.db"
        self.env.db_path.rename(legacy)
        self.env.db_path = legacy  # the test helpers read the DB where it is now
        self.assert_refused("reviewer", "db-layout")

    def test_token_file_missing(self):
        (self.env.user_home / ".config" / "factory" / "worker-oauth-token").unlink()
        self.assert_refused("reviewer", "token")

    def test_gh_token_missing_for_the_role(self):
        (self.env.user_home / ".config" / "factory" / "gh-worker-rw-token").unlink()
        self.assert_refused("coder", "token")
        self.assertEqual(self.spawn("reviewer").returncode, 0)  # the ro token is all a reviewer needs

    def test_token_file_mode_0644(self):
        (self.env.user_home / ".config" / "factory" / "gh-worker-ro-token").chmod(0o644)
        self.assert_refused("reviewer", "token")

    def test_token_file_is_a_symlink(self):
        t = self.env.user_home / ".config" / "factory" / "worker-oauth-token"
        real = self.env.home / "real-token"
        real.write_text(MARKER)
        real.chmod(0o600)
        t.unlink()
        t.symlink_to(real)
        self.assert_refused("reviewer", "token")

    def test_token_file_empty(self):
        (self.env.user_home / ".config" / "factory" / "worker-oauth-token").write_text("")
        self.assert_refused("reviewer", "token")

    def test_no_claude_on_path(self):
        (self.env.stub_bin / "claude").unlink()
        path = f"{self.env.stub_bin}{os.pathsep}/usr/bin{os.pathsep}/bin"
        if shutil.which("claude", path=path):
            self.skipTest("a claude is installed in /usr/bin")
        p = self.env.run(*spawn_args(self.env, "reviewer"), extra_env={"PATH": path})
        self.assertEqual(p.returncode, 3, p.stderr)
        self.assertEqual([json.loads(r["detail"])["reason"] for r in self.events()], ["claude"])
        self.assertEqual(self.mux("agent", "start"), [])

    def test_qa_and_coder_with_a_legacy_worktree(self):
        for role in ("qa", "coder"):
            wt = Path(cwd_of(self.env, role))
            shutil.rmtree(wt / ".git")
            (wt / ".git").write_text("gitdir: /nowhere\n")
            p = self.assert_refused(role, "clone")
            self.assertIn("per-task clone (G-010 T-G)", p.stderr)

    def test_reviewer_cwd_is_the_factory_home(self):
        self.assert_refused("reviewer", "cwd", cwd=False)  # no task worktree: cwd falls back to the factory home

    def test_live_factory_refuses_code_run_from_outside_its_checkout(self):
        with self.env.load_factory() as mod:
            self.assertIsNone(mod.sandbox_preflight("reviewer", cwd_of(self.env, "reviewer"), "zz-reviewer"))  # a temp home: no code-root rule
            mod.LIVE_HOMES = {str(self.env.home_dir.resolve())}  # pretend this temp home is the live one: workerbox is in REPO, not in it
            self.assertEqual(mod.sandbox_preflight("reviewer", cwd_of(self.env, "reviewer"), "zz-reviewer")[0], "code-root")

    def test_refusal_message_names_no_token_path(self):
        (self.env.user_home / ".config" / "factory" / "worker-oauth-token").unlink()
        p = self.assert_refused("reviewer", "token")
        self.assertNotIn("worker-oauth-token", p.stdout + p.stderr)
        for r in self.env.sql("SELECT subject, body FROM messages WHERE subject LIKE 'sandbox refused%'"):
            self.assertNotIn("oauth-token", r["subject"] + r["body"])

    def test_other_roles_and_the_flag_off_are_not_affected(self):
        (self.env.stub_bin / "unshare").write_text("#!/bin/sh\nexit 1\n")
        self.assertEqual(self.spawn("lead").returncode, 0)  # not listed (and not sandboxable)
        self.set_roles("")
        self.assertEqual(self.spawn("reviewer").returncode, 0)


class PreflightThrottleTests(SandboxCase):
    def msgs(self):
        return self.env.sql("SELECT recipient, subject FROM messages WHERE subject LIKE 'sandbox refused%'")

    def test_same_reason_within_an_hour_sends_one_director_message(self):
        self.set_roles("reviewer")
        (self.env.stub_bin / "unshare").write_text("#!/bin/sh\nexit 1\n")
        self.assert_refused("reviewer", "userns", "zz-rev-1")
        self.assert_refused("reviewer", "userns", "zz-rev-2")
        self.assertEqual(len(self.events()), 2)
        self.assertEqual([m["recipient"] for m in self.msgs()], ["director"])

    def test_another_reason_sends_its_own_message_and_an_old_one_is_resent(self):
        self.set_roles("reviewer")
        (self.env.stub_bin / "unshare").write_text("#!/bin/sh\nexit 1\n")
        self.assert_refused("reviewer", "userns", "zz-rev-1")
        (self.env.user_home / ".config" / "factory" / "worker-oauth-token").unlink()
        (self.env.stub_bin / "unshare").write_text("#!/bin/sh\nexit 0\n")
        self.assert_refused("reviewer", "token", "zz-rev-2")
        self.assertEqual(len(self.msgs()), 2)
        self.env.sql("UPDATE throttle SET ts=? WHERE key='sandbox_refused:token'", (str(0),))
        self.assert_refused("reviewer", "token", "zz-rev-3")
        self.assertEqual(len(self.msgs()), 3)


class PreflightAllowedTests(SandboxCase):
    def test_reviewer_goes_live_with_the_agent_bin_first_on_the_pane_path(self):
        self.set_roles("reviewer")
        p = self.spawn("reviewer")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(self.agents("zz-reviewer")[0]["status"], "live")
        agent_dir = self.env.user_home / ".local" / "state" / "factory" / "workers" / "zz-reviewer"
        paths = [a[a.index("--env") + 1:][0:1] for a in self.mux("tab", "create") + self.mux("pane", "split")
                 if any(x.startswith("PATH=") for x in a)]
        pane_env = [x for a in self.mux("tab", "create") + self.mux("pane", "split") for x in a if x.startswith("PATH=")]
        self.assertEqual(len(pane_env), 1, paths)
        self.assertTrue(pane_env[0].startswith(f"PATH={agent_dir}/bin:{self.env.stub_bin}"), pane_env[0])
        shim = agent_dir / "bin" / "claude"
        self.assertEqual(stat.S_IMODE(shim.stat().st_mode), 0o755)
        args = json.loads(self.agents("zz-reviewer")[0]["spawn_args"])
        self.assertTrue(args["sandbox"])
        ev = [json.loads(r["detail"]) for r in self.env.sql("SELECT detail FROM events WHERE action='spawn' AND entity_id='zz-reviewer'")]
        self.assertTrue(ev[0]["sandbox"])

    def shim_cfg(self, name):
        """The cfg literal of the spawned agent's shim (what workerbox.shim_main reads)."""
        shim = (self.env.user_home / ".local/state/factory/workers" / name / "bin/claude").read_text()
        return json.loads(ast.literal_eval(re.search(r"json\.loads\((.*)\), sys\.argv", shim).group(1)))

    def test_default_config_keeps_todays_token_paths(self):
        self.set_roles("reviewer,coder")
        tokens = self.env.user_home / ".config" / "factory"
        for role, token in (("reviewer", "gh-worker-ro-token"), ("coder", "gh-worker-rw-token")):
            self.assertEqual(self.spawn(role).returncode, 0)
            self.assertEqual(self.shim_cfg(ROLE_NAMES[role])["gh_token"], str(tokens / token))

    def test_gh_token_rw_config_names_the_token_the_shim_reads(self):
        self.set_roles("coder")
        other = self.env.user_home / ".config" / "factory" / "gh-other-token"
        other.write_text(MARKER + "\n")
        other.chmod(0o600)
        self.ok("config", "gh_token_rw", str(other))
        self.assertEqual(self.spawn("coder").returncode, 0)
        self.assertEqual(self.shim_cfg("zz-coder")["gh_token"], str(other))

    def test_gh_token_ro_config_names_the_token_for_a_reviewer(self):
        self.set_roles("reviewer")
        other = self.env.user_home / ".config" / "factory" / "gh-other-ro"
        other.write_text(MARKER + "\n")
        other.chmod(0o600)
        self.ok("config", "gh_token_ro", str(other))
        self.assertEqual(self.spawn("reviewer").returncode, 0)
        self.assertEqual(self.shim_cfg("zz-reviewer")["gh_token"], str(other))

    def test_gh_rw_roles_picks_the_rw_token_for_a_reviewer(self):
        self.set_roles("reviewer")
        self.ok("config", "gh_rw_roles", "reviewer")
        self.assertEqual(self.spawn("reviewer").returncode, 0)
        self.assertTrue(self.shim_cfg("zz-reviewer")["gh_token"].endswith("gh-worker-rw-token"))

    def test_gh_token_config_outside_the_config_dir_is_refused(self):
        self.set_roles("coder")
        elsewhere = self.env.home / "elsewhere-token"
        elsewhere.write_text(MARKER + "\n")
        elsewhere.chmod(0o600)
        self.ok("config", "gh_token_rw", str(elsewhere))
        p = self.assert_refused("coder", "token")
        self.assertIn("gh_token_rw", p.stderr)
        self.assertNotIn(str(elsewhere), p.stderr)

    def test_the_gitconfig_helper_is_scoped_to_the_worker_repos(self):
        self.set_roles("reviewer")
        self.ok("config", "worker_gh_repos", "own/one,own/two")
        self.assertEqual(self.spawn("reviewer").returncode, 0)
        gitconfig = self.env.user_home / ".local/state/factory/workers/zz-reviewer/gitconfig"
        got = subprocess.run(["git", "config", "-f", str(gitconfig), "--get", "credential.https://github.com.helper"],
                             capture_output=True, text=True, check=True).stdout.strip()
        self.assertTrue(got.endswith("scout/ghcred.py git-credential own/one,own/two"), got)

    def test_the_shim_imports_from_the_factory_home(self):
        self.set_roles("reviewer")
        self.assertEqual(self.spawn("reviewer").returncode, 0)
        shim = (self.env.user_home / ".local/state/factory/workers/zz-reviewer/bin/claude").read_text()
        self.assertIn(f"sys.path.insert(0, {str(self.env.home_dir)!r})", shim)
        self.assertEqual(shim.splitlines()[2], f"sys.path.insert(0, {str(self.env.home_dir)!r})")

    def test_a_staff_engineer_is_never_sandboxed(self):
        self.set_roles("reviewer,qa,coder")
        self.env.seed_agent("zz-director", "director")
        self.ok("msg", "staff-engineer", "q", "ctx", "--kind", "request", agent="zz-director")
        self.assertEqual(self.mux("agent", "start")[0][2], "staff-engineer")
        self.assertEqual([x for a in self.env.calls("herdr") for x in a["argv"] if str(x).startswith("PATH=")], [])
        self.assertFalse((self.env.user_home / ".local/state/factory/workers/staff-engineer").exists())


class QueueTests(SandboxCase):
    def queue_reviewer(self):
        self.env.set_herdr(fail=True)
        self.assertIn("QUEUED", self.ok(*spawn_args(self.env, "reviewer")).stdout)
        self.env.set_herdr(fail=False)
        self.set_roles("reviewer")

    def test_a_refused_queued_spawn_is_cancelled_once_and_the_lead_told(self):
        self.queue_reviewer()
        probes = self.env.stub_bin.parent / "unshare-probes"  # the userns probe runs once per preflight
        (self.env.stub_bin / "unshare").write_text(f"#!/bin/sh\necho probe >> {probes}\nexit 0\n")
        (self.env.user_home / ".config" / "factory" / "worker-oauth-token").unlink()
        starts_before = len(self.mux("agent", "start"))
        for _ in range(3):
            self.ok("tick")
        self.assertEqual(len(probes.read_text().split()), 1)  # no retry loop: preflight ran on the first pass only
        self.assertEqual(self.agents("zz-reviewer")[0]["status"], "cancelled")
        self.assertEqual(len(self.mux("agent", "start")), starts_before)  # fail closed: no pane for the refused row
        self.assertEqual([r for r in self.agents("zz-reviewer") if r["status"] == "live"], [])
        msgs = self.env.sql("SELECT recipient, subject, body FROM messages WHERE subject LIKE 'sandbox refused queued spawn%'")
        self.assertEqual([(m["recipient"], m["subject"]) for m in msgs],
                         [("zz-lead", "sandbox refused queued spawn zz-reviewer")])
        self.assertIn("token", msgs[0]["body"])
        self.assertIn("spawn again", msgs[0]["body"])
        for bad in ("worker-oauth-token", "gh-worker"):
            self.assertNotIn(bad, msgs[0]["body"])
        ev = [json.loads(r["detail"]) for r in self.events("dequeued") if r["entity_id"] == "zz-reviewer"]
        self.assertEqual([e["by"] for e in ev], ["sandbox-preflight"])
        self.assertEqual([json.loads(r["detail"])["reason"] for r in self.events()], ["token"])

    def test_a_cancelled_refused_row_is_not_started_after_the_fix(self):
        self.queue_reviewer()
        (self.env.stub_bin / "unshare").write_text("#!/bin/sh\nexit 1\n")
        self.ok("tick")
        (self.env.stub_bin / "unshare").write_text("#!/bin/sh\nexit 0\n")  # fixed: too late, the row is gone
        self.ok("tick")
        self.assertEqual(self.agents("zz-reviewer")[0]["status"], "cancelled")
        self.assertEqual(self.mux("agent", "start"), [])
        self.ok(*spawn_args(self.env, "reviewer"))  # the lead spawns again: it starts
        self.assertEqual(self.agents("zz-reviewer")[0]["status"], "live")

    def test_a_direct_sandboxed_spawn_that_is_refused_exits_3_and_queues_nothing(self):
        (self.env.stub_bin / "unshare").write_text("#!/bin/sh\nexit 1\n")
        p = self.env.run(*spawn_args(self.env, "reviewer"), "--sandbox")
        self.assertEqual(p.returncode, 3, p.stderr)
        self.assertEqual(self.agents("zz-reviewer"), [])
        self.assertEqual(self.mux("agent", "start"), [])


class PerSpawnOptInTests(SandboxCase):
    """T-0389: `factory spawn <role> --sandbox` sandboxes one agent without the role-wide sandbox_roles flag."""

    def optin(self, role, name, *extra):
        return self.env.run(*spawn_args(self.env, role, name), "--sandbox", *extra)

    def test_one_spawn_is_sandboxed_and_another_of_the_same_role_is_not(self):
        p = self.optin("reviewer", "zz-spike")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual([r["value"] for r in self.env.sql("SELECT value FROM config WHERE key='sandbox_roles'")
                          if r["value"]], [])
        args = json.loads(self.agents("zz-spike")[0]["spawn_args"])
        self.assertTrue(args["sandbox"] and args["sandbox_optin"])
        self.assertTrue((self.env.user_home / ".local/state/factory/workers/zz-spike").is_dir())
        self.assertEqual(self.spawn("reviewer", "zz-other").returncode, 0)
        other = json.loads(self.agents("zz-other")[0]["spawn_args"])
        self.assertNotIn("sandbox", other)
        self.assertNotIn("sandbox_optin", other)
        self.assertFalse((self.env.user_home / ".local/state/factory/workers/zz-other").exists())

    def test_a_queued_optin_is_kept_when_the_tick_starts_it(self):
        self.env.set_herdr(fail=True)
        self.assertIn("QUEUED", self.optin("reviewer", "zz-spike").stdout)
        self.assertTrue(json.loads(self.agents("zz-spike")[0]["spawn_args"])["sandbox_optin"])
        self.env.set_herdr(fail=False)
        self.ok("tick")
        row = self.agents("zz-spike")[0]
        self.assertEqual(row["status"], "live")
        self.assertTrue(json.loads(row["spawn_args"])["sandbox"])

    def test_a_queued_plain_spawn_stays_unsandboxed(self):
        self.env.set_herdr(fail=True)
        self.assertIn("QUEUED", self.spawn("reviewer", "zz-other").stdout)
        self.env.set_herdr(fail=False)
        self.ok("tick")
        row = self.agents("zz-other")[0]
        self.assertEqual(row["status"], "live")
        self.assertNotIn("sandbox", json.loads(row["spawn_args"]))

    def test_relaunch_keeps_the_optin(self):
        self.assertEqual(self.optin("reviewer", "zz-spike").returncode, 0)
        self.ok("relaunch", "zz-spike", "--force")
        self.assertTrue(json.loads(self.agents("zz-spike")[0]["spawn_args"])["sandbox_optin"])
        paths = [x for a in self.mux("tab", "create") + self.mux("pane", "split") for x in a if x.startswith("PATH=")]
        self.assertEqual(len(paths), 2)
        self.assertTrue(all("/workers/zz-spike/bin:" in x for x in paths))

    def test_optin_still_runs_the_preflight(self):
        (self.env.stub_bin / "unshare").write_text("#!/bin/sh\nexit 1\n")
        p = self.optin("reviewer", "zz-spike")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("sandbox refused", p.stderr)
        self.assertEqual(self.mux("agent", "start"), [])

    def test_roles_that_cannot_be_sandboxed_exit_2(self):
        for role in ("lead", "pm", "judge", "cleaner", "staff-engineer"):
            before = len(self.env.calls("herdr"))
            p = self.env.run("spawn", role, "--name", f"zz-x-{role}", "--goal", "G-001", "--team", "g-001-a", "--sandbox")
            self.assertEqual(p.returncode, 2, (role, p.stderr))
            self.assertIn("cannot be sandboxed", p.stderr)
            self.assertEqual(len(self.env.calls("herdr")), before)
            self.assertEqual(self.agents(f"zz-x-{role}"), [])

    def test_a_worker_cannot_use_it(self):
        self.env.seed_agent("zz-test-coder", "coder", goal="G-001", team="g-001-a")
        p = self.env.run(*spawn_args(self.env, "reviewer", "zz-spike"), "--sandbox", agent="zz-test-coder")
        self.assertEqual(p.returncode, 3, p.stderr)
        self.assertIn("workers may not run", p.stderr)
        self.assertEqual(self.agents("zz-spike"), [])


class RelaunchTests(SandboxCase):
    def test_relaunch_reuses_the_agent_dir_and_resumes_the_session(self):
        self.set_roles("reviewer")
        self.assertEqual(self.spawn("reviewer").returncode, 0)
        sid = self.agents("zz-reviewer")[0]["session_id"]
        agent_dir = self.env.user_home / ".local/state/factory/workers/zz-reviewer"
        shim_before = (agent_dir / "bin" / "claude").read_text()
        self.ok("relaunch", "zz-reviewer", "--force")
        starts = self.mux("agent", "start")
        self.assertEqual(len(starts), 2)
        self.assertIn("--resume", starts[1])
        self.assertEqual(starts[1][starts[1].index("--resume") + 1], sid)
        paths = [x for a in self.mux("tab", "create") + self.mux("pane", "split") for x in a if x.startswith("PATH=")]
        self.assertEqual(len(paths), 2)
        self.assertEqual(paths[0], paths[1])
        self.assertTrue(paths[1].startswith(f"PATH={agent_dir}/bin:"))
        self.assertEqual(sorted(p.name for p in agent_dir.parent.iterdir()), ["zz-reviewer"])
        self.assertEqual((agent_dir / "bin" / "claude").read_text(), shim_before)

    def test_a_refused_relaunch_does_not_close_the_running_pane(self):
        self.set_roles("reviewer")
        self.assertEqual(self.spawn("reviewer").returncode, 0)
        (self.env.stub_bin / "unshare").write_text("#!/bin/sh\nexit 1\n")
        p = self.env.run("relaunch", "zz-reviewer", "--force")
        self.assertNotEqual(p.returncode, 0)
        self.assertEqual(self.mux("pane", "close"), [])
        self.assertEqual(self.agents("zz-reviewer")[0]["status"], "live")


    def _live_reviewer_in_factory_home(self):
        """A reviewer spawned while the flag is off with cwd = the factory home: legal then, refused once sandboxed."""
        p = self.env.run("spawn", "reviewer", "--name", "zz-reviewer", "--goal", "G-001", "--team", "g-001-a",
                         "--cwd", str(self.env.home_dir))
        self.assertEqual(p.returncode, 0, p.stderr)
        self.set_roles("reviewer")
        return self.agents("zz-reviewer")[0]["pane_id"]

    def test_relaunch_refused_by_the_cwd_rule_keeps_the_pane_and_the_row(self):
        pane = self._live_reviewer_in_factory_home()
        p = self.env.run("relaunch", "zz-reviewer", "--force")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("sandbox refused", p.stderr)
        self.assertEqual(self.mux("pane", "close"), [])
        row = self.agents("zz-reviewer")[0]
        self.assertEqual((row["status"], row["pane_id"]), ("live", pane))
        self.assertEqual([json.loads(r["detail"])["reason"] for r in self.events()], ["cwd"])

    def test_when_idle_relaunch_refused_by_the_cwd_rule_keeps_the_pane_and_the_row(self):
        pane = self._live_reviewer_in_factory_home()
        self.ok("relaunch", "zz-reviewer", "--when-idle")
        self.ok("tick")
        self.assertEqual(self.mux("pane", "close"), [])
        row = self.agents("zz-reviewer")[0]
        self.assertEqual((row["status"], row["pane_id"]), ("live", pane))
        self.assertEqual([json.loads(r["detail"])["reason"] for r in self.events()], ["cwd"])
        self.assertEqual(len(self.events("relaunch-failed")), 1)


    def test_relaunch_of_a_row_without_cwd_is_a_clean_cwd_refusal(self):
        pane = self._live_reviewer_in_factory_home()
        self.env.sql("UPDATE agents SET cwd=NULL WHERE name='zz-reviewer'")
        p = self.env.run("relaunch", "zz-reviewer", "--force")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("sandbox refused", p.stderr)
        self.assertNotIn("Traceback", p.stderr)
        self.assertEqual(self.mux("pane", "close"), [])
        row = self.agents("zz-reviewer")[0]
        self.assertEqual((row["status"], row["pane_id"]), ("live", pane))
        self.assertEqual([json.loads(r["detail"])["reason"] for r in self.events()], ["cwd"])

    def test_relaunch_whose_prepare_fails_after_the_pane_closed_leaves_no_live_row(self):
        self.set_roles("reviewer")
        self.assertEqual(self.spawn("reviewer").returncode, 0)
        agent_dir = self.env.user_home / ".local/state/factory/workers/zz-reviewer"
        agent_dir.chmod(0o500)  # preflight passes; prepare_agent cannot write its files: OSError
        try:
            p = self.env.run("relaunch", "zz-reviewer", "--force")
        finally:
            agent_dir.chmod(0o700)
        self.assertNotEqual(p.returncode, 0)
        self.assertEqual(len(self.mux("pane", "close")), 1)
        row = self.agents("zz-reviewer")[0]
        self.assertEqual((row["status"], row["pane_id"]), ("exited", None))
        self.assertEqual(len(self.events("relaunch-failed-after-close")), 1)


class NoWorktreeOnRefusalTests(SandboxCase):
    def test_a_refused_spawn_without_cwd_creates_no_worktree(self):
        self.set_roles("coder")
        (self.env.user_home / ".config" / "factory" / "worker-oauth-token").unlink()
        repo = self.env.home / "proj-repo"  # a project with a repo: without the early preflight prepare_worktree adds a worktree
        subprocess.run(["git", "init", "-q", "-b", "main", str(repo)], check=True)
        subprocess.run(["git", "-C", str(repo), "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "i"],
                       check=True)
        self.env.seed_project("zz-proj")
        self.env.sql("UPDATE projects SET repo_path=?, base_branch='main' WHERE id='zz-proj'", (str(repo),))
        self.env.sql("UPDATE goals SET project_id='zz-proj' WHERE id='G-001'")
        self.env.seed_task("T-0001", "G-001", role="coder", status="in_progress", team="g-001-a")
        before = sorted(x.name for x in (self.env.home_dir / "worktrees").iterdir())
        p = self.env.run("spawn", "coder", "--name", "zz-coder", "--goal", "G-001", "--team", "g-001-a", "--task", "T-0001")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("(token)", p.stderr)
        self.assertEqual(sorted(x.name for x in (self.env.home_dir / "worktrees").iterdir()), before)
        self.assertEqual(before, ["w-coder", "w-qa", "w-reviewer"])  # only the fixtures: prepare_worktree never ran


class NoLeakTests(SandboxCase):
    def test_token_content_and_path_appear_nowhere_but_the_shim_path(self):
        self.set_roles("reviewer")
        self.assertEqual(self.spawn("reviewer").returncode, 0)
        (self.env.user_home / ".config" / "factory" / "gh-worker-rw-token").unlink()
        self.set_roles("reviewer,coder")
        self.assert_refused("coder", "token")  # the refusal path leaks nothing either
        blobs = {"multiplexer calls": self.env.calls_file.read_text()}
        for table in ("events", "messages", "agents", "throttle", "config"):
            blobs[table] = json.dumps([tuple(r) for r in self.env.sql(f"SELECT * FROM {table}")])
        for name, blob in blobs.items():
            self.assertNotIn(MARKER, blob, name)
            self.assertNotIn("oauth-token", blob, name)
            self.assertNotIn("gh-worker", blob, name)
        agent_dir = self.env.user_home / ".local/state/factory/workers/zz-reviewer"
        for f in agent_dir.rglob("*"):
            if f.is_file():
                self.assertNotIn(MARKER, f.read_text(errors="replace"), str(f))  # the shim names the files, never copies them


class DetectionTests(SandboxCase):
    def test_sandboxed_claude_argv_is_counted_once_and_not_flagged(self):
        self.set_roles("reviewer")
        self.assertEqual(self.spawn("reviewer").returncode, 0)
        row = self.agents("zz-reviewer")[0]
        claude_args = self.mux("agent", "start")[0]
        claude_args = claude_args[claude_args.index("--") + 1:]
        real = str(self.env.stub_bin / "claude")  # the shim execs the real claude by absolute path
        argv = [real, *claude_args]
        self.assertIn("--session-id", argv)
        self.assertTrue(argv[argv.index("--settings") + 1].endswith("/settings/reviewer.json"))
        self.env.fake_claude(4242, ppid=1, argv=argv)
        with self.env.load_factory() as mod:
            con = mod.db()
            self.assertEqual([p for p, _ in mod.claude_procs(row["session_id"])], [4242])
            self.assertEqual(mod.flagless_state(dict(row)), ("ok", 4242))
            self.assertEqual(mod.claude_outside_spawn(con), [])
            tot = mod.total_claude_procs()
            self.assertEqual((tot["total"], tot["pids"]), (1, [4242]))


@unittest.skipUnless(userns_here(), "user namespaces are not available on this box")
class InsideTests(FactoryTestCase):
    """The real shim chain (shim -> unshare -> nsbox -> a fake claude) in a temp FACTORY_HOME; the fake claude runs
    `factory msg` and `factory done` as the spawned agent, from inside the namespace."""

    def test_factory_msg_and_done_work_from_inside(self):
        env = self.env
        prep(env, fake_unshare=False)
        for d in ("bin", "scout", "web"):  # the shim and the CLI inside run the code under the temp factory home
            shutil.copytree(REPO / d, env.home_dir / d, ignore=shutil.ignore_patterns("__pycache__"))
        env.seed_agent("zz-lead", "lead", goal="G-001", team="g-001-a")
        claude = env.stub_bin / "claude"
        claude.write_text(f"#!{sys.executable}\n" + """
import os, subprocess, sys
cli = [sys.executable, os.path.join(os.environ["FACTORY_HOME"], "bin", "factory")]
def run(*a):
    p = subprocess.run(cli + list(a), capture_output=True, text=True)
    print("rc", p.returncode, p.stdout, p.stderr, flush=True)
    return p.returncode
sys.exit(run("inbox") or run("msg", "zz-lead", "hello from inside", "body") or run("done", "inside is done"))
""")
        self.ok("config", "sandbox_roles", "reviewer")
        p = self.ok(*spawn_args(env, "reviewer"))
        agent_dir = env.user_home / ".local/state/factory/workers/zz-reviewer"
        pane_env = {k: os.environ[k] for k in ("LANG",) if k in os.environ}
        pane_env.update({"PATH": f"{agent_dir}/bin:{env.stub_bin}:{os.environ.get('PATH', '/usr/bin:/bin')}",
                         "FACTORY_AGENT": "zz-reviewer", "FACTORY_ROLE": "reviewer", "FACTORY_HOME": str(env.home_dir),
                         "TERM": "dumb"})
        r = subprocess.run([str(agent_dir / "bin" / "claude")], env=pane_env, capture_output=True, text=True,
                           cwd=cwd_of(env, "reviewer"), timeout=120)
        self.assertEqual(r.returncode, 0, f"{p.stdout}\nrc={r.returncode}\n{r.stdout}\n{r.stderr}")
        self.assertEqual(r.stdout.count("rc 0"), 3, r.stdout)
        msgs = env.sql("SELECT sender, recipient, subject FROM messages WHERE subject='hello from inside'")
        self.assertEqual([tuple(m) for m in msgs], [("zz-reviewer", "zz-lead", "hello from inside")])
        self.assertEqual(env.sql("SELECT status FROM agents WHERE name='zz-reviewer'")[0][0], "done")


if __name__ == "__main__":
    if "--capture" in sys.argv:
        print(json.dumps(record(), indent=1, sort_keys=True))
    else:
        unittest.main()
