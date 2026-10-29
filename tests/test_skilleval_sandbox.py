"""T-0111: evaluator spawn path through the trial sandbox, and `factory skilleval try`.

Group A needs no namespaces (layout, spawn argv/env through the multiplexer stub, fail-closed refusals, the token
rule) and runs everywhere, CI included. Group B (real namespaces) skips with the reason where they are restricted.
No real claude, no real token: a fake claude script and a dummy token file in a temp home.
"""
import ast
import builtins
import json
import os
import re
import shutil
import stat
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
from harness import FactoryTestCase, FACTORY, FACTORY_SRC, REPO  # noqa: E402

sys.path.insert(0, str(REPO))
from scout import nsbox, sandbox  # noqa: E402
from web import dbpath  # noqa: E402

TOKEN = "sk-ant-" + "oat01-DUMMYTRIALTOKEN"  # split so the repo secret scan does not flag the dummy
SHA = "c" * 40
UNSHARE = os.environ.get("NSBOX_TEST_UNSHARE", "unshare")
USERNS_OK, USERNS_WHY = nsbox.userns_available(UNSHARE)
SKIP_MSG = f"user namespaces unavailable: {USERNS_WHY}"
FAKE_CLAUDE = "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 9.9.9; exit 0; fi\nenv\npwd\n"


class SandboxBase(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        self.root = e.home / "evalroot"
        self.ok("config", "skilleval_root", str(self.root), agent="owner")
        self.home = e.user_home
        self.token = self.home / ".config" / "factory" / "eval-oauth-token"
        self.token.parent.mkdir(parents=True)
        self.token.write_text(TOKEN + "\n")
        self.token.chmod(0o600)
        self.claude = self.home / ".local" / "bin" / "claude"
        self.claude.parent.mkdir(parents=True, exist_ok=True)
        self.claude.write_text(FAKE_CLAUDE)
        self.claude.chmod(0o755)
        e.sql("INSERT INTO scout_candidates(id,key,repo,head_sha,install_risk,status,sources,first_seen,last_seen) "
              "VALUES('C-0001','gh:o/r','o/r',?,'prompt-only','new','[]','t','t')", (SHA,))
        self.plugin_src = self.root / "vendor" / f"C-0001@{SHA}"
        (self.plugin_src / "plugin" / "skills" / "s").mkdir(parents=True)
        (self.plugin_src / "plugin" / "skills" / "s" / "SKILL.md").write_text("# s\nJust prose.\n")
        self.write_meta()

    def write_meta(self):
        files = sorted(p.relative_to(self.plugin_src / "plugin").as_posix()
                       for p in (self.plugin_src / "plugin").rglob("*") if p.is_file())
        (self.plugin_src / "VENDOR.json").write_text(json.dumps(
            {"candidate": "C-0001", "sha": SHA, "plugin": "plugin", "copied": files, "skipped": [], "prescreen_hits": []}))

    def trial_row(self, con, cond="with", cid="C-0001", sha=SHA, task="fix-bug"):
        tid = con.execute("INSERT INTO skill_trials(candidate_id,vendor_sha,task,condition,status,queued) "
                          "VALUES(?,?,?,?,'pending','t')", (cid, sha, task, cond)).lastrowid
        return con.execute("SELECT * FROM skill_trials WHERE id=?", (tid,)).fetchone()

    def build(self, con, cond="with", **kw):
        return sandbox.build_sandbox(con, self.trial_row(con, cond), self.root, home=str(self.home),
                                     real_claude=str(self.claude), token_file=str(self.token), **kw)

    def spawn_kwargs(self, info, with_plugin=True):
        d = Path(info["dir"])
        return dict(name=info["agent"], cwd=info["work"], prompt="Run `factory task show T-0001`.",
                    plugin_dirs=[info["plugin"]] if with_plugin and info["plugin"] else [], add_home_dir=False,
                    env_extra={"FACTORY_HOME": str(d / "fh"), "HOME": str(d / "home"), "CLAUDE_CONFIG_DIR": str(d / "cfg"),
                               "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1", "DISABLE_AUTOUPDATER": "1",
                               "PATH": os.pathsep.join([str(d / "bin"), str(REPO / "bin"), str(self.home / ".local/bin"),
                                                        "/usr/local/bin", "/usr/bin", "/bin"])})

    def herdr_calls(self):
        return [c["argv"] for c in self.env.calls("herdr")]

    def env_of(self, argv):
        """The --env KEY=VALUE pairs of a pane-creating call."""
        return dict(argv[i + 1].split("=", 1) for i, x in enumerate(argv) if x == "--env")

    def start_calls(self):
        return [a for a in self.herdr_calls() if a[:2] == ["agent", "start"]]

    def pane_call(self):
        calls = [a for a in self.herdr_calls() if a[:2] in (["tab", "create"], ["pane", "split"]) and "--env" in a]
        self.assertEqual(len(calls), 1, calls)
        return calls[0]

    def check_spawn(self, info, with_plugin):
        trial = Path(info["dir"]).resolve()
        (start,) = self.start_calls()
        plugin_idx = [i for i, x in enumerate(start) if x == "--plugin-dir"]
        if with_plugin:
            self.assertEqual(len(plugin_idx), 1, start)
            self.assertTrue(Path(os.path.realpath(start[plugin_idx[0] + 1])).is_relative_to(self.root.resolve() / "vendor"))
        else:
            self.assertEqual(plugin_idx, [], start)
        self.assertNotIn("--add-dir", start)
        live = str(self.env.home_dir)
        pane = self.pane_call()
        for argv in (start, pane):
            self.assertFalse([x for x in argv if live in x], f"live factory home in {argv}")
            self.assertFalse([x for x in argv if "roles/plugins" in x], argv)
        env = self.env_of(pane)
        self.assertEqual(env["FACTORY_HOME"], str(trial / "fh"))
        self.assertEqual(env["HOME"], str(trial / "home"))
        self.assertEqual(env["CLAUDE_CONFIG_DIR"], str(trial / "cfg"))
        self.assertEqual(env["FACTORY_ROLE"], "evaluator")
        self.assertEqual(env["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"], "1")
        self.assertEqual(env["DISABLE_AUTOUPDATER"], "1")
        path = env["PATH"].split(os.pathsep)
        self.assertEqual(path[0], str(trial / "bin"))
        self.assertIn(str(REPO / "bin"), path)
        self.assertIn(str(self.home / ".local/bin"), path)
        self.assertEqual(pane[pane.index("--cwd") + 1], str(trial / "work"))
        self.assertNotIn(TOKEN, json.dumps(self.env.calls()))


class LayoutTests(SandboxBase):
    def test_layout_and_seed(self):
        with self.env.load_factory() as f:
            info = self.build(f.db())
        d = Path(info["dir"])
        work = d / "work"
        self.assertEqual(subprocess.run(["git", "-C", str(work), "rev-list", "--count", "HEAD"], capture_output=True,
                                        text=True).stdout.strip(), "1")
        self.assertEqual(subprocess.run(["git", "-C", str(work), "remote"], capture_output=True, text=True).stdout.strip(), "")
        self.assertTrue((work / "taskdeps.py").is_file())
        self.assertFalse((work / "solution").exists() or (work / "hidden").exists())
        self.assertEqual([x.name for x in (d / "home").iterdir()], [sandbox.HOME_RC[0]])
        seed = json.loads((d / "cfg" / ".claude.json").read_text())
        self.assertEqual(seed, {"hasCompletedOnboarding": True, "theme": "dark",
                                "projects": {os.path.realpath(work): {"hasTrustDialogAccepted": True,
                                                                      "hasCompletedProjectOnboarding": True}}})
        shim = d / "bin" / "claude"
        self.assertEqual(stat.S_IMODE(shim.stat().st_mode), 0o755)
        self.assertIn(str(self.claude), shim.read_text())  # basename claude, not the resolved install
        self.assertNotIn(TOKEN, shim.read_text())
        env = {"PATH": os.environ["PATH"], "FACTORY_HOME": str(d / "fh"), "HOME": str(d / "home"), "LC_ALL": "C.UTF-8"}
        p = subprocess.run([sys.executable, str(d / "bin" / "factory"), "task", "show", "T-0001"], env=env,
                           capture_output=True, text=True, cwd=work)
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn((REPO / "skilleval/tasks/fix-bug/task.md").read_text().splitlines()[0], p.stdout)
        cfgs = subprocess.run([sys.executable, str(d / "bin" / "factory"), "config", "herdr_socket"], env=env,
                              capture_output=True, text=True, cwd=work).stdout
        self.assertIn(sandbox.DEAD_SOCKET, cfgs)
        self.assertIn("git_push = 0", subprocess.run([sys.executable, str(d / "bin" / "factory"), "config"], env=env,
                                                     capture_output=True, text=True, cwd=work).stdout)
        con = __import__("sqlite3").connect(dbpath.resolve(d / "fh"))
        self.assertEqual(con.execute("SELECT count(*) FROM messages WHERE recipient=? AND read_at IS NULL",
                                     (info["agent"],)).fetchone()[0], 1)
        self.assertEqual(con.execute("SELECT role,status FROM agents WHERE name=?", (info["agent"],)).fetchone(),
                         ("evaluator", "live"))
        con.close()

    def test_without_condition_has_no_plugin(self):
        with self.env.load_factory() as f:
            info = self.build(f.db(), cond="without")
        self.assertIsNone(info["plugin"])

    def test_missing_or_loose_token_builds_nothing(self):
        for fix in (lambda: self.token.unlink(), lambda: self.token.chmod(0o644)):
            self.token.write_text(TOKEN)
            self.token.chmod(0o600)
            self.env.sql("DELETE FROM skill_trials")
            fix()
            with self.env.load_factory() as f, self.assertRaises(sandbox.SandboxRefused) as cm:
                self.build(f.db())
            self.assertIn("owner decision D3 pending: no eval token", str(cm.exception))
            self.assertFalse((self.root / "trials").exists())

    def test_symlinked_token_refused(self):
        real = self.home / "real-token"
        real.write_text(TOKEN)
        real.chmod(0o600)
        self.token.unlink()
        self.token.symlink_to(real)
        with self.assertRaises(sandbox.SandboxRefused):
            sandbox.check_token(str(self.token))


class SpawnTests(SandboxBase):
    def spawn(self, mod, info, with_plugin=True, **over):
        con = mod.db()
        kw = {**self.spawn_kwargs(info, with_plugin), **over}
        return mod.spawn(con, "evaluator", **kw)

    def test_with_condition_argv_and_env(self):
        with self.env.load_factory() as f:
            info = self.build(f.db())
            self.assertEqual(self.spawn(f, info), info["agent"])
        self.check_spawn(info, True)

    def test_without_condition_has_no_plugin_dir(self):
        with self.env.load_factory() as f:
            info = self.build(f.db(), cond="without")
            self.spawn(f, info, with_plugin=False)
        self.check_spawn(info, False)

    def test_role_files_are_read_only_copies_inside_the_trial(self):
        with self.env.load_factory() as f:
            info = self.build(f.db())
            self.spawn(f, info)
        (start,) = self.start_calls()
        trial = Path(info["dir"]).resolve()
        self.assertEqual(start[start.index("--settings") + 1], str(trial / "role" / "settings.json"))
        self.assertEqual(start[start.index("--append-system-prompt-file") + 1], str(trial / "role" / "prompt.md"))
        self.assertIn("evaluator", (trial / "role" / "prompt.md").read_text())

    def test_plugin_outside_vendor_root_dies_before_any_call(self):
        outside = self.env.home / "elsewhere"
        (outside / "skills").mkdir(parents=True)
        (outside / "skills" / "x.md").write_text("prose")
        with self.env.load_factory() as f:
            info = self.build(f.db())
            with self.assertRaises(SystemExit) as cm:
                self.spawn(f, info, plugin_dirs=[str(outside)])
        self.assertEqual(cm.exception.code, 3)
        self.assertEqual(self.herdr_calls(), [])
        self.assertEqual(self.env.sql("SELECT * FROM agents WHERE name=?", (info["agent"],)), [])

    def test_vendored_dir_with_planted_cjs_dies(self):
        with self.env.load_factory() as f:
            info = self.build(f.db())
            (self.plugin_src / "plugin" / "evil.cjs").write_text("process.exit(1)")
            with self.assertRaises(SystemExit) as cm:
                self.spawn(f, info)
        self.assertEqual(cm.exception.code, 3)
        self.assertEqual(self.herdr_calls(), [])

    def test_symlink_out_of_vendor_root_dies(self):
        link = self.root / "vendor" / "link"
        link.symlink_to(self.env.home)
        with self.env.load_factory() as f:
            info = self.build(f.db())
            with self.assertRaises(SystemExit):
                self.spawn(f, info, plugin_dirs=[str(link)])
        self.assertEqual(self.herdr_calls(), [])

    def test_non_evaluator_may_not_pass_sandbox_params(self):
        with self.env.load_factory() as f:
            con = f.db()
            for kw in ({"env_extra": {"X": "1"}}, {"plugin_dirs": ["/tmp/x"]}, {"add_home_dir": False}):
                with self.assertRaises(SystemExit) as cm:
                    f.spawn(con, "coder", name="zz-c", **kw)
                self.assertEqual(cm.exception.code, 3)
        self.assertEqual(self.herdr_calls(), [])

    def test_non_evaluator_without_params_still_spawns(self):
        with self.env.load_factory() as f:
            f.spawn(f.db(), "pm", name="zz-pm", cwd=str(self.env.home))
        self.assertTrue(self.start_calls())
        self.assertIn("--add-dir", self.start_calls()[0])

    def test_evaluator_without_a_sandbox_dies(self):
        with self.env.load_factory() as f:
            con = f.db()
            for kw in ({}, {"env_extra": {"FACTORY_HOME": str(self.env.home_dir)}, "add_home_dir": False}):
                with self.assertRaises(SystemExit) as cm:
                    f.spawn(con, "evaluator", name="eval-x", cwd=str(self.env.home), **kw)
                self.assertEqual(cm.exception.code, 3)
        self.assertEqual(self.herdr_calls(), [])

    def test_bad_env_cwd_or_path_die(self):
        with self.env.load_factory() as f:
            info = self.build(f.db())
            good = self.spawn_kwargs(info)
            d = Path(info["dir"])
            bads = [dict(cwd=str(self.env.home_dir)), dict(cwd=str(d)),
                    dict(env_extra={**good["env_extra"], "FACTORY_HOME": str(self.env.home_dir)}),
                    dict(env_extra={**good["env_extra"], "HOME": str(self.home)}),
                    dict(env_extra={**good["env_extra"], "CLAUDE_CONFIG_DIR": str(self.home / ".claude")}),
                    dict(env_extra={**good["env_extra"], "PATH": "/usr/bin:" + good["env_extra"]["PATH"]}),
                    dict(env_extra={**good["env_extra"], "CLAUDE_CODE_OAUTH_TOKEN": TOKEN}),
                    dict(env_extra={**good["env_extra"], "LD_PRELOAD": "/tmp/x.so"})]
            for over in bads:
                with self.assertRaises(SystemExit, msg=str(over)) as cm:
                    self.spawn(f, info, **over)
                self.assertEqual(cm.exception.code, 3)
        self.assertEqual(self.herdr_calls(), [])

    def test_queued_evaluator_keeps_env_and_plugin_after_dequeue(self):
        with self.env.load_factory() as f:
            info = self.build(f.db())
        self.env.set_herdr(fail=True)  # gate closed
        with self.env.load_factory() as f:
            self.assertIsNone(self.spawn(f, info))
        row = self.env.sql("SELECT status, spawn_args FROM agents WHERE name=?", (info["agent"],))[0]
        self.assertEqual(row["status"], "queued")
        self.assertEqual(json.loads(row["spawn_args"])["plugin_dirs"], [info["plugin"]])
        self.env.set_herdr(fail=False)
        self.env.calls_file.write_text("")
        self.ok("tick")
        self.assertEqual(self.env.sql("SELECT status FROM agents WHERE name=?", (info["agent"],))[0]["status"], "live")
        self.check_spawn(info, True)

    def test_queued_evaluator_that_no_longer_validates_is_cancelled_not_a_tick_crash(self):
        with self.env.load_factory() as f:
            info = self.build(f.db())
        self.env.set_herdr(fail=True)
        with self.env.load_factory() as f:
            self.spawn(f, info)
        self.env.set_herdr(fail=False)
        shutil.rmtree(info["dir"])
        self.env.sql("UPDATE skill_trials SET status='cancelled'")  # a pending row would make the skilleval tick rebuild the sandbox
        self.ok("tick")
        self.assertEqual(self.env.sql("SELECT status FROM agents WHERE name=?", (info["agent"],))[0]["status"], "cancelled")
        self.assertEqual(self.start_calls(), [])

    def queue_running_trial(self, info):
        """A gate-queued evaluator whose trial row is 'running', as skilleval_launch leaves it."""
        self.env.set_herdr(fail=True)
        with self.env.load_factory() as f:
            self.assertIsNone(self.spawn(f, info))
        self.env.sql("UPDATE skill_trials SET status='running', agent=?, started='t'", (info["agent"],))
        self.env.set_herdr(fail=False)
        self.env.calls_file.write_text("")

    def trial_state(self):
        return self.env.sql("SELECT status, ended, passed, check_output FROM skill_trials")[0]

    def test_tick_cancel_of_invalid_queued_evaluator_ends_the_trial_with_a_reason(self):
        with self.env.load_factory() as f:
            info = self.build(f.db())
        self.queue_running_trial(info)
        shutil.rmtree(info["dir"])
        self.ok("tick")
        self.assertEqual(self.env.sql("SELECT status FROM agents WHERE name=?", (info["agent"],))[0]["status"], "cancelled")
        t = self.trial_state()
        self.assertEqual((t["status"], t["passed"]), ("cancelled", 0))
        self.assertTrue(t["ended"])
        self.assertIn("no longer validates", t["check_output"])
        self.assertEqual(self.start_calls(), [])

    def test_tick_retries_a_queued_evaluator_after_a_transient_spawn_failure(self):
        with self.env.load_factory() as f:
            info = self.build(f.db())
        self.queue_running_trial(info)
        with self.env.load_factory() as f:
            with mock.patch.object(f, "_spawn_start", side_effect=SystemExit(1)):  # a transient failure past the validation
                f.cmd_tick(mock.Mock(), locked=True)
        self.assertEqual(self.env.sql("SELECT status FROM agents WHERE name=?", (info["agent"],))[0]["status"], "queued")
        self.assertEqual(self.trial_state()["status"], "running")
        self.assertEqual(len(self.env.sql("SELECT 1 FROM events WHERE entity_id=? AND action='evaluator-spawn-retry'",
                                          (info["agent"],))), 1)
        self.ok("tick")
        self.assertEqual(self.env.sql("SELECT status FROM agents WHERE name=?", (info["agent"],))[0]["status"], "live")
        self.assertEqual(self.trial_state()["status"], "running")

    def test_shim_must_be_executable(self):
        with self.env.load_factory() as f:
            info = self.build(f.db())
            Path(info["shim"]).chmod(0o644)
            with self.assertRaises(SystemExit) as cm:
                self.spawn(f, info)
            self.assertEqual(cm.exception.code, 3)
            Path(info["shim"]).chmod(0o755)
            self.assertEqual(self.spawn(f, info), info["agent"])

    def test_shim_must_be_owned_by_the_user(self):
        with self.env.load_factory() as f:
            info = self.build(f.db())
            with mock.patch.object(f.os, "getuid", return_value=os.getuid() + 1):  # as if another user owned the shim
                with self.assertRaises(SystemExit) as cm:
                    self.spawn(f, info)
            self.assertEqual(cm.exception.code, 3)
            self.assertEqual(self.spawn(f, info), info["agent"])

    def test_shim_must_not_be_a_symlink(self):
        with self.env.load_factory() as f:
            info = self.build(f.db())
            shim = Path(info["shim"])
            real = shim.with_name("claude.real")
            shim.rename(real)
            shim.symlink_to(real)
            with self.assertRaises(SystemExit) as cm:
                self.spawn(f, info)
            self.assertEqual(cm.exception.code, 3)

    def test_skilleval_try_resolves_the_root_once(self):
        src = FACTORY_SRC.read_text()
        fn = next(n for n in ast.parse(src).body if isinstance(n, ast.FunctionDef) and n.name == "skilleval_try")
        self.assertEqual(ast.get_source_segment(src, fn).count("resolve_root("), 1)

    def test_no_unread_live_message_and_tick_does_not_nudge(self):
        with self.env.load_factory() as f:
            info = self.build(f.db())
            self.spawn(f, info)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM messages WHERE recipient=? AND read_at IS NULL",
                                      (info["agent"],))[0]["c"], 0)
        prompts = [a for a in self.herdr_calls() if a[:2] == ["agent", "prompt"]]
        self.assertEqual(len(prompts), 1)
        self.assertIn("factory task show T-0001", prompts[0][3])
        self.env.sql("UPDATE agents SET last_nudge=NULL WHERE name=?", (info["agent"],))
        self.env.calls_file.write_text("")
        self.ok("tick")
        self.assertEqual([a for a in self.herdr_calls() if a[:2] == ["agent", "prompt"]], [])

    def test_token_never_reaches_a_call_or_the_live_db_and_spawn_never_opens_it(self):
        token = str(self.token)
        real_open, real_os_open = builtins.open, os.open

        def guard(orig):
            def inner(path, *a, **k):
                if isinstance(path, (str, bytes, os.PathLike)) and os.fsdecode(path) == token:
                    raise AssertionError("spawn opened the token file")
                return orig(path, *a, **k)
            return inner
        with self.env.load_factory() as f:
            info = self.build(f.db())
            with mock.patch.object(builtins, "open", guard(real_open)), mock.patch.object(os, "open", guard(real_os_open)):
                self.spawn(f, info)
        self.assertNotIn(TOKEN, json.dumps(self.env.calls()))
        dump = subprocess.run(["sqlite3", str(self.env.db_path), ".dump"], capture_output=True,
                              text=True).stdout if shutil.which("sqlite3") else None
        if dump is None:
            import sqlite3
            con = sqlite3.connect(self.env.db_path)
            dump = "\n".join(con.iterdump())
            con.close()
        self.assertNotIn(TOKEN, dump)
        self.assertIn(info["agent"], dump)  # the dump does contain the agent: the grep is meaningful
        tree = ast.parse(FACTORY_SRC.read_text())
        spawn_src = [ast.get_source_segment(FACTORY_SRC.read_text(), n) for n in tree.body
                     if isinstance(n, ast.FunctionDef) and n.name in ("spawn", "_spawn_start", "check_evaluator_spawn")]
        self.assertEqual(len(spawn_src), 3)
        for src in spawn_src:
            self.assertNotIn("oauth-token", src)
            self.assertNotIn("read_token", src)


class TryTests(SandboxBase):
    def test_cli_try_refuses_missing_token_without_a_sandbox(self):
        self.token.unlink()
        p = self.refused(3, "skilleval", "try", "fix-bug", "--candidate", "C-0001", agent="owner")
        self.assertIn("owner decision D3 pending: no eval token", p.stderr)
        self.assertFalse((self.root / "trials").exists())
        self.assertEqual(self.herdr_calls(), [])

    def test_cli_try_refuses_loose_token_mode(self):
        self.token.chmod(0o644)
        p = self.refused(3, "skilleval", "try", "fix-bug", "--candidate", "C-0001", agent="owner")
        self.assertIn("owner decision D3 pending", p.stderr)
        self.assertFalse((self.root / "trials").exists())

    def test_no_user_namespaces_means_no_fallback(self):
        p = self.refused(3, "skilleval", "try", "fix-bug", "--candidate", "C-0001", agent="owner",
                         extra_env={"NSBOX_TEST_UNSHARE": "/nonexistent/unshare"})
        self.assertIn("no unsandboxed fallback", p.stderr)
        self.assertFalse((self.root / "trials").exists())
        self.assertEqual(self.env.sql("SELECT * FROM agents WHERE role='evaluator'"), [])
        self.assertEqual(self.env.sql("SELECT * FROM skill_trials"), [])
        self.assertEqual(self.herdr_calls(), [])

    def test_hide_failure_is_refused_and_cleaned_up(self):
        with self.env.load_factory() as f:
            con = f.db()
            a = mock.Mock(id="fix-bug", candidate="C-0001", condition=None)
            with mock.patch.object(f.scoutsandbox.nsbox, "userns_available", return_value=(True, "ok")), \
                    mock.patch.object(f.scoutsandbox, "preflight",
                                      side_effect=sandbox.SandboxRefused("sandbox preflight failed: mount")), \
                    self.assertRaises(SystemExit) as cm:
                f.skilleval_try(con, a)
            self.assertEqual(cm.exception.code, 3)
            self.assertEqual(con.execute("SELECT status FROM skill_trials").fetchone()[0], "error")
        self.assertEqual(self.herdr_calls(), [])
        self.assertEqual(self.env.sql("SELECT * FROM agents WHERE role='evaluator'"), [])
        self.assertEqual(list((self.root / "trials").iterdir()), [])

    def test_non_prompt_only_and_needs_owner_candidates_are_refused(self):
        self.env.sql("INSERT INTO scout_candidates(id,key,head_sha,install_risk,status,sources,first_seen,last_seen) "
                     "VALUES('C-0002','gh:o/s',?,'scripts','new','[]','t','t')", (SHA,))
        self.env.sql("INSERT INTO scout_candidates(id,key,head_sha,install_risk,status,sources,first_seen,last_seen) "
                     "VALUES('C-0003','gh:o/t',?,'prompt-only','needs-owner','[]','t','t')", (SHA,))
        for cid in ("C-0002", "C-0003"):
            p = self.refused(2, "skilleval", "try", "fix-bug", "--candidate", cid, agent="owner")
            self.assertIn("not eligible", p.stderr)
        self.assertEqual(self.env.sql("SELECT * FROM skill_trials"), [])
        self.assertFalse((self.root / "trials").exists())

    def test_roles(self):
        for r in ("coder", "qa", "reviewer", "pm"):
            self.env.seed_agent(f"zz-{r}", r)
        for r in ("coder", "qa", "reviewer", "pm"):
            self.refused(3, "skilleval", "try", "fix-bug", "--candidate", "none", agent=f"zz-{r}")

    def test_unknown_task_and_bad_condition(self):
        self.refused(2, "skilleval", "try", "../x", "--candidate", "none", agent="owner")
        self.refused(2, "skilleval", "try", "nope", "--candidate", "none", agent="owner")
        self.refused(2, "skilleval", "try", "fix-bug", "--candidate", "none", "--condition", "with", agent="owner")


@unittest.skipUnless(USERNS_OK, SKIP_MSG)
class NamespaceTests(SandboxBase):
    def extra(self):
        return {"NSBOX_TEST_UNSHARE": UNSHARE}

    def test_try_baseline_end_to_end(self):
        p = self.env.run("skilleval", "try", "fix-bug", "--candidate", "none", extra_env=self.extra())
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        row = self.env.sql("SELECT * FROM skill_trials")[0]
        self.assertEqual((row["candidate_id"], row["condition"], row["status"]), ("C-0000", "without", "running"))
        self.assertEqual(self.env.sql("SELECT id FROM scout_candidates WHERE id='C-0000'")[0]["id"], "C-0000")
        (start,) = self.start_calls()
        self.assertNotIn("--plugin-dir", start)
        self.assertEqual(row["agent"], start[2])
        self.assertTrue(row["agent"].startswith("eval-"))

    def test_try_with_candidate_vendors_nothing_new_and_passes_one_plugin_dir(self):
        p = self.env.run("skilleval", "try", "fix-bug", "--candidate", "C-0001", extra_env=self.extra())
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        (start,) = self.start_calls()
        self.assertEqual(start.count("--plugin-dir"), 1)
        row = self.env.sql("SELECT * FROM skill_trials")[0]
        self.assertEqual(row["condition"], "with")
        self.check_spawn({"dir": row["sandbox"]}, True)
        again = self.env.run("skilleval", "try", "fix-bug", "--candidate", "C-0001", extra_env=self.extra())
        self.assertEqual(again.returncode, 2, again.stderr)

    def test_preflight_fails_when_a_bind_source_is_missing(self):
        with self.env.load_factory() as f:
            info = self.build(f.db())
        sandbox.preflight(info)  # the intact view passes
        shutil.rmtree(Path(info["dir"]) / "role")
        with self.assertRaises(sandbox.SandboxRefused) as cm:
            sandbox.preflight(info)
        self.assertIn("preflight failed", str(cm.exception))

    def test_shim_env_arrives_inside_and_the_rest_is_dropped(self):
        with self.env.load_factory() as f:
            info = self.build(f.db())
        d = Path(info["dir"])
        env = {"PATH": os.environ["PATH"], "FACTORY_AGENT": info["agent"], "FACTORY_HOME": str(self.env.home_dir),
               "FACTORY_ROLE": "coder", "HOME": str(self.home), "CLAUDE_CONFIG_DIR": str(self.home / ".claude"),
               "SECRET_STUFF": "leak", "FACTORY_DB": "/x", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
               "DISABLE_AUTOUPDATER": "1"}
        p = subprocess.run([str(d / "bin" / "claude")], env=env, cwd=info["work"], capture_output=True, text=True, timeout=60)
        self.assertEqual(p.returncode, 0, p.stderr)
        got = dict(l.split("=", 1) for l in p.stdout.splitlines() if "=" in l)
        self.assertEqual(got["FACTORY_HOME"], str(d / "fh"))
        self.assertEqual(got["FACTORY_ROLE"], "evaluator")
        self.assertEqual(got["FACTORY_AGENT"], info["agent"])
        self.assertEqual(got["HOME"], str(d / "home"))
        self.assertEqual(got["CLAUDE_CONFIG_DIR"], str(d / "cfg"))
        for gone in ("SECRET_STUFF", "FACTORY_DB"):
            self.assertNotIn(gone, got)
        self.assertEqual(got["CLAUDE_CODE_OAUTH_TOKEN"], TOKEN)  # the shim read the file, inside the pane
        self.assertEqual(p.stdout.splitlines()[-1], os.path.realpath(info["work"]))


class NsboxFixTests(unittest.TestCase):
    """T-0111 nsbox fixes: symlinked trials_root siblings, make_cfg called once."""

    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp(prefix="zz-test-nsfix-")).resolve()
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.real = self.tmp / "real-trials"
        self.real.mkdir()
        self.link = self.tmp / "trials-link"
        self.link.symlink_to(self.real)
        for n in ("own", "other"):
            (self.real / n).mkdir()

    def cfg(self, trial):
        return {"trial": str(trial), "trials_root": str(self.link), "hide_dirs": []}

    def test_own_trial_is_not_a_sibling_whatever_the_spelling(self):
        for own in (self.link / "own", self.real / "own"):
            sibs = nsbox._sibling_trials(self.cfg(own))
            self.assertEqual(sibs, [str(self.real / "other")], own)

    def test_cwd_inside_a_sibling_through_the_symlink_is_refused_and_own_is_accepted(self):
        cfg = self.cfg(self.real / "own")
        with self.assertRaises(ValueError):
            nsbox._refuse_hidden_cwd(str(self.link / "other" / "x"), cfg)
        nsbox._refuse_hidden_cwd(str(self.link / "own"), cfg)
        nsbox._refuse_hidden_cwd(str(self.real / "own"), self.cfg(self.link / "own"))

    def test_run_contained_builds_its_cfg_once(self):
        with mock.patch.object(nsbox, "make_cfg", wraps=nsbox.make_cfg) as mk, \
                mock.patch.object(nsbox.subprocess, "Popen", side_effect=RuntimeError("stop")):
            with self.assertRaises(RuntimeError):
                nsbox.run_contained(self.real / "own", ["true"], cwd=str(self.real / "own"), home=str(self.tmp),
                                    trials_root=str(self.link))
        self.assertEqual(mk.call_count, 1)

    def test_run_contained_still_refuses_a_hidden_cwd(self):
        with self.assertRaises(ValueError):
            nsbox.run_contained(self.real / "own", ["true"], cwd=str(self.link / "other"), home=str(self.tmp),
                                trials_root=str(self.link))


class TrialHomeZshTests(SandboxBase):
    """T-0699: an empty trial HOME made zsh-newuser-install eat the first key of the pane launch line."""

    def run_zsh(self, home):
        keys = "(sleep 2; printf 'echo MARKER\\n'; sleep 1; printf 'exit\\n') | script -qc 'zsh -i' /dev/null"
        r = subprocess.run(["sh", "-c", keys], capture_output=True, text=True, timeout=30,
                           env={"PATH": os.environ["PATH"], "HOME": str(home), "LC_ALL": "C.UTF-8", "TERM": "dumb"})
        return r.stdout

    @staticmethod
    def launch_ok(out):
        """MARKER as its own output line (not the echoed keystrokes) and no shell error."""
        return bool(re.search(r"(?m)^MARKER\r?$", out)) and "command not found" not in out

    def setUp(self):
        super().setUp()
        if not shutil.which("zsh") or not shutil.which("script"):
            self.skipTest("zsh or script (pty) not available")

    def test_trial_home_rc_is_comment_only_file(self):
        with self.env.load_factory() as f:
            info = self.build(f.db())
        rc = Path(info["dir"]) / "home" / sandbox.HOME_RC[0]
        self.assertTrue(rc.is_file() and not rc.is_symlink())
        self.assertEqual(stat.S_IMODE(rc.lstat().st_mode), 0o644)
        lines = [x for x in rc.read_text().splitlines() if x.strip()]
        self.assertTrue(lines and all(x.startswith("#") for x in lines))

    def test_launch_line_survives_in_built_home(self):
        with self.env.load_factory() as f:
            info = self.build(f.db())
        out = self.run_zsh(Path(info["dir"]) / "home")
        self.assertTrue(self.launch_ok(out), out)
        self.assertNotIn("zsh-newuser-install", out)

    def test_empty_home_is_detected(self):
        d = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, d, True)
        out = self.run_zsh(d)
        self.assertIn("zsh-newuser-install", out)
        self.assertFalse(self.launch_ok(out), out)


if __name__ == "__main__":
    unittest.main()
