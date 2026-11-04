"""G-016 T-0352: tests/guard_impl.py picks the hook implementation and, in diff mode, fails on a divergence."""
import json
import os
import pathlib
import stat
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import guard_impl  # noqa: E402
from guard_impl import hook_run  # noqa: E402

BLOCK = json.dumps({"tool_name": "Bash", "tool_input": {"command": "pkill x"}})
ALLOW = json.dumps({"tool_name": "Bash", "tool_input": {"command": "echo hi"}})


def stub(td, body):
    path = os.path.join(td, "worker-guard")
    with open(path, "w") as f:
        f.write("#!/bin/sh\n" + body + "\n")
    os.chmod(path, os.stat(path).st_mode | stat.S_IXUSR)
    return path


class GuardImplTests(unittest.TestCase):
    def env(self, mode, go=None):
        e = {"GUARD_IMPL": mode}
        if go:
            e["GUARD_GO_BIN"] = go
        return mock.patch.dict(os.environ, e)

    def test_default_is_the_python_hook(self):
        with mock.patch.dict(os.environ, clear=False):
            os.environ.pop("GUARD_IMPL", None)
            self.assertEqual(guard_impl.impl(), "python")
            self.assertEqual(hook_run(BLOCK, ["--role", "coder"]).returncode, 2)

    def test_unknown_mode_is_an_error(self):
        with self.env("rust"), self.assertRaises(ValueError):
            hook_run(ALLOW, ["--role", "coder"])

    def test_diff_catches_a_go_binary_that_always_allows(self):
        with tempfile.TemporaryDirectory() as td, self.env("diff", stub(td, "cat >/dev/null; exit 0")):
            with self.assertRaises(AssertionError) as cm:
                hook_run(BLOCK, ["--role", "coder"])
            self.assertIn("pkill x", str(cm.exception))
            self.assertIn("python=2 go=0", str(cm.exception))

    def test_diff_passes_when_both_agree_and_returns_python_result(self):
        with tempfile.TemporaryDirectory() as td, self.env("diff", stub(td, "cat >/dev/null; exit 0")):
            self.assertEqual(hook_run(ALLOW, ["--role", "coder"]).returncode, 0)

    def test_diff_fails_when_only_stderr_differs(self):  # T-0352 B2: same exit code, different message
        with tempfile.TemporaryDirectory() as td, self.env("diff", stub(td, "cat >/dev/null; echo other >&2; exit 2")):
            with self.assertRaises(AssertionError) as cm:
                hook_run(BLOCK, ["--role", "coder"])
            self.assertIn("stderr differs", str(cm.exception))
            self.assertIn("pkill x", str(cm.exception))

    def test_diff_normalizes_only_the_go_prefix(self):
        self.assertEqual(guard_impl.normalize_go_stderr("worker-guard(go): hook error\n"), "worker-guard: hook error\n")
        self.assertEqual(guard_impl.normalize_go_stderr("worker-guard: blocked (go)\n"), "worker-guard: blocked (go)\n")
        self.assertEqual(guard_impl.normalize_go_stderr("note: worker-guard(go) is mid-line\n"), "note: worker-guard(go) is mid-line\n")
        with tempfile.TemporaryDirectory() as td, self.env("diff", stub(td, "cat >/dev/null; echo 'worker-guard(go): x' >&2; exit 0")):
            with self.assertRaises(AssertionError):  # prefix normalized, but python's stderr is empty on ALLOW
                hook_run(ALLOW, ["--role", "coder"])

    def test_go_mode_runs_the_go_binary(self):
        with tempfile.TemporaryDirectory() as td, self.env("go", stub(td, "cat >/dev/null; exit 7")):
            self.assertEqual(hook_run(BLOCK, ["--role", "coder"]).returncode, 7)

    def test_go_mode_with_a_missing_binary_errors_instead_of_skipping(self):
        with self.env("go", "/nonexistent/worker-guard"), self.assertRaises(RuntimeError):
            hook_run(BLOCK, ["--role", "coder"])


CLI = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "bin", "factory_cli.py")
SCHEMA = ("CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT NOT NULL);"
          "CREATE TABLE agents (name TEXT PRIMARY KEY, goal_id TEXT);"
          "CREATE TABLE tasks (id TEXT PRIMARY KEY, worktree TEXT);")


shutil_which = __import__("shutil").which


def go_unavailable(err):
    """True only for "no go binary found" and "go.mod requires go >=" (toolchain older than go.mod, CI shard 8)."""
    msg = str(err)
    return msg.startswith("go not found on PATH") or "go.mod requires go >=" in msg


def real_cli():
    """bin/factory_cli.py loaded from source under a throwaway FACTORY_HOME (it reads the env at import)."""
    import importlib.util
    with tempfile.TemporaryDirectory() as td, mock.patch.dict(os.environ, {"FACTORY_HOME": td}):
        spec = importlib.util.spec_from_file_location("cli_t0977", CLI)
        mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(mod)
    return mod


class SeedGuardExportTests(unittest.TestCase):
    """T-0977: under go/diff hook_run seeds <FACTORY_HOME>/run/guard-export.json from the fixture DB."""

    def setUp(self):
        self.td = tempfile.TemporaryDirectory()
        self.addCleanup(self.td.cleanup)
        self.home = os.path.realpath(self.td.name)
        os.makedirs(os.path.join(self.home, "db"))
        self.db = os.path.join(self.home, "db", "factory.db")
        self.export = os.path.join(self.home, "run", "guard-export.json")
        self.sql(SCHEMA + "INSERT INTO agents VALUES ('a-1','G-001'),('a-2',NULL);"
                 "INSERT INTO tasks VALUES ('T-1','/wt/1'),('T-2','');"
                 "INSERT INTO config VALUES ('test_sandbox','1'),('other','x');")
        p = mock.patch.dict(os.environ, {"GUARD_IMPL": "diff", "FACTORY_HOME": self.home})
        p.start()
        self.addCleanup(p.stop)

    def sql(self, script):
        import sqlite3
        con = sqlite3.connect(self.db)
        try:
            con.executescript(script)
            con.commit()
        finally:
            con.close()

    def load(self):
        with open(self.export) as f:
            return json.load(f)

    def test_python_and_unset_modes_write_nothing(self):
        for mode in ("python", None):
            with mock.patch.dict(os.environ):
                if mode:
                    os.environ["GUARD_IMPL"] = mode
                else:
                    os.environ.pop("GUARD_IMPL")
                self.assertEqual(hook_run(ALLOW, ["--role", "coder"]).returncode, 0)
            self.assertFalse(os.path.exists(self.export))

    def test_layout_matches_the_real_writer_constants(self):
        cli = real_cli()
        self.assertEqual(guard_impl.EXPORT_VERSION, cli.GUARD_EXPORT_VERSION)
        self.assertEqual(set(guard_impl.EXPORT_DEFAULTS), set(cli.GUARD_EXPORT_KEYS))
        for k in cli.GUARD_EXPORT_KEYS:
            self.assertEqual(guard_impl.EXPORT_DEFAULTS[k], cli.DEFAULT_CONFIG[k], k)

    def test_seeded_doc_content_mode_and_timestamp(self):
        guard_impl.seed_guard_export()
        doc = self.load()
        self.assertEqual(set(doc), {"version", "generated_at", "config", "agent_goals", "task_worktrees"})
        self.assertEqual(doc["version"], 1)
        self.assertEqual(doc["config"], {**guard_impl.EXPORT_DEFAULTS, "test_sandbox": "1"})
        self.assertEqual(doc["agent_goals"], {"a-1": "G-001"})
        self.assertEqual(doc["task_worktrees"], {"T-1": "/wt/1"})
        import datetime
        self.assertIsNotNone(datetime.datetime.fromisoformat(doc["generated_at"]).tzinfo)
        self.assertEqual(stat.S_IMODE(os.stat(self.export).st_mode), 0o600)

    def test_partial_schema_gives_defaults_and_empty_maps(self):
        os.unlink(self.db)
        self.sql("CREATE TABLE agents (name TEXT PRIMARY KEY);")
        guard_impl.seed_guard_export()
        doc = self.load()
        self.assertEqual(doc["config"], guard_impl.EXPORT_DEFAULTS)
        self.assertEqual((doc["agent_goals"], doc["task_worktrees"]), ({}, {}))

    def test_real_writer_and_seam_documents_have_the_same_top_level_keys(self):
        import sqlite3
        cli = real_cli()
        con = sqlite3.connect(self.db)
        con.row_factory = sqlite3.Row
        try:
            real_path = pathlib.Path(self.home) / "real" / "guard-export.json"
            cli.write_guard_export(con, real_path)
        finally:
            con.close()
        guard_impl.seed_guard_export()
        real, seam = json.loads(real_path.read_text()), self.load()
        self.assertEqual(set(seam), set(real))
        self.assertEqual(seam["config"], real["config"])
        self.assertEqual(seam["agent_goals"], real["agent_goals"])
        self.assertEqual(seam["task_worktrees"], real["task_worktrees"])

    def test_go_binary_accepts_the_seeded_export(self):
        with mock.patch.dict(os.environ, {"GUARD_IMPL": "go"}):
            try:
                guard_impl.go_bin()
            except RuntimeError as e:
                if go_unavailable(e):
                    self.skipTest(str(e))
                raise
            r = hook_run(ALLOW, ["--role", "coder"], env=dict(os.environ, FACTORY_AGENT="a-1"))
        self.assertTrue(os.path.exists(self.export))
        self.assertNotIn("guard export", r.stderr)
        self.assertEqual(r.returncode, 0)

    def go_bin_error(self, **patches):
        with mock.patch.object(guard_impl, "_go_bin", []), mock.patch.dict(os.environ):
            os.environ.pop("GUARD_GO_BIN", None)
            os.environ.update(patches.pop("env", {}))
            which = patches.pop("shutil", {}).get("which", shutil_which)
            with mock.patch.object(guard_impl.shutil, "which", which), \
                 mock.patch.object(guard_impl.subprocess, "run", **patches.pop("run", {})):
                with self.assertRaises(RuntimeError) as cm:
                    guard_impl.go_bin()
        return cm.exception

    def test_only_a_missing_go_or_an_old_toolchain_skips(self):
        no_go = self.go_bin_error(shutil={"which": mock.Mock(return_value=None)}, env={"HOME": self.home})
        self.assertTrue(go_unavailable(no_go), str(no_go))
        build = mock.Mock(return_value=mock.Mock(returncode=1, stderr="go: go.mod requires go >= 1.27 (running go 1.26.10)"))
        old = self.go_bin_error(shutil={"which": mock.Mock(return_value="/bin/true")}, run={"new": build})
        self.assertTrue(go_unavailable(old), str(old))

    def test_a_build_failure_or_a_bad_guard_go_bin_does_not_skip(self):
        broken = mock.Mock(return_value=mock.Mock(returncode=1, stderr="cmd/worker-guard/main.go:3: syntax error"))
        build = self.go_bin_error(shutil={"which": mock.Mock(return_value="/bin/true")}, run={"new": broken})
        self.assertFalse(go_unavailable(build), str(build))
        bad = self.go_bin_error(env={"GUARD_GO_BIN": os.path.join(self.home, "missing")})
        self.assertFalse(go_unavailable(bad), str(bad))

    def test_no_write_without_factory_home(self):
        with mock.patch.dict(os.environ):
            os.environ.pop("FACTORY_HOME")
            guard_impl.seed_guard_export()
        self.assertFalse(os.path.exists(self.export))

    def test_no_write_outside_the_temp_dir(self):
        with mock.patch.object(guard_impl.tempfile, "gettempdir", return_value=os.path.join(self.home, "elsewhere")):
            guard_impl.seed_guard_export()
        self.assertFalse(os.path.exists(self.export))

    def test_no_write_through_a_run_symlink_leaving_the_temp_dir(self):
        with mock.patch.object(guard_impl.tempfile, "gettempdir", return_value=os.path.join(self.home, "only")):
            os.makedirs(os.path.join(self.home, "only", "h", "db"))
            os.makedirs(os.path.join(self.home, "outside"))
            h = os.path.join(self.home, "only", "h")
            os.rename(self.db, os.path.join(h, "db", "factory.db"))
            os.symlink(os.path.join(self.home, "outside"), os.path.join(h, "run"))
            guard_impl.seed_guard_export({"FACTORY_HOME": h})
        self.assertEqual(os.listdir(os.path.join(self.home, "outside")), [])

    def test_no_write_when_the_db_is_absent(self):
        os.unlink(self.db)
        guard_impl.seed_guard_export()
        self.assertFalse(os.path.exists(self.export))

    def test_env_argument_wins_over_os_environ(self):
        other = os.path.join(self.home, "other")
        os.makedirs(os.path.join(other, "db"))
        with open(os.path.join(other, "db", "factory.db"), "wb"):
            pass
        guard_impl.seed_guard_export({"FACTORY_HOME": other})
        self.assertTrue(os.path.exists(os.path.join(other, "run", "guard-export.json")))
        self.assertFalse(os.path.exists(self.export))

    def test_foreign_export_is_never_overwritten(self):
        os.makedirs(os.path.dirname(self.export))
        with open(self.export, "w") as f:
            f.write('{"mine": true}')
        guard_impl.seed_guard_export()
        guard_impl.seed_guard_export()
        with open(self.export) as f:
            self.assertEqual(f.read(), '{"mine": true}')

    def test_seeded_export_is_regenerated_after_the_db_changes(self):
        guard_impl.seed_guard_export()
        self.assertEqual(self.load()["agent_goals"], {"a-1": "G-001"})
        self.sql("INSERT INTO agents VALUES ('a-3','G-009');")
        guard_impl.seed_guard_export()
        self.assertEqual(self.load()["agent_goals"], {"a-1": "G-001", "a-3": "G-009"})

    def test_db_is_opened_read_only_and_left_unchanged(self):
        with open(self.db, "rb") as f:
            before = f.read()
        guard_impl.seed_guard_export()
        with open(self.db, "rb") as f:
            self.assertEqual(f.read(), before)
        import sqlite3
        real = sqlite3.connect
        seen = []
        with mock.patch.object(guard_impl.sqlite3, "connect", side_effect=lambda *a, **k: seen.append(a[0]) or real(*a, **k)):
            guard_impl.seed_guard_export()
        self.assertTrue(seen and all(u.endswith("?mode=ro") for u in seen), seen)

    def test_unreadable_db_seeds_nothing_and_drops_a_stale_seeded_file(self):
        with open(self.db, "wb") as f:
            f.write(b"this is not a sqlite database" * 100)
        guard_impl.seed_guard_export()
        self.assertFalse(os.path.exists(self.export))
        os.unlink(self.db)
        self.sql(SCHEMA)
        guard_impl.seed_guard_export()
        self.assertTrue(os.path.exists(self.export))
        with open(self.db, "wb") as f:
            f.write(b"garbage" * 1000)
        guard_impl.seed_guard_export()
        self.assertFalse(os.path.exists(self.export))


if __name__ == "__main__":
    unittest.main()
