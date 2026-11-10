"""T-1220: per-task clones get their Claude workspace-trust entry pre-registered in the owner's ~/.claude.json
(temp HOME only), strictly under FACTORY_HOME/worktrees/, and `factory clean` drops it again."""
import json
import os
import unittest
import unittest.mock

from harness import FactoryTestCase, git
from test_clean_clones import CloneCleanBase

SEED = {"hasTrustDialogAccepted": True, "hasCompletedProjectOnboarding": True}
OTHER = {"theme": "dark", "numStartups": 7, "projects": {"/elsewhere/proj": {"allowedTools": ["x"]}}}


class TrustBase:
    def cj(self):
        return self.env.user_home / ".claude.json"

    def write_cj(self, data=None, mode=0o600):
        p = self.cj()
        p.write_text(json.dumps(OTHER if data is None else data))
        p.chmod(mode)
        return p

    def load(self):
        return json.loads(self.cj().read_text())

    def refusals(self):
        return [json.loads(r["detail"]) for r in self.env.sql("SELECT detail FROM events WHERE action='clone-trust-refused'")]


class CloneTrustSpawnTests(TrustBase, FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_project()
        e.seed_goal(project="zz-test-proj", title="zz-test clone goal")
        self.h = e.home_dir
        self.bare = e.home / "origin.git"
        git("init", "-q", "--bare", "-b", "main", str(self.bare), cwd=e.home)
        git("remote", "add", "origin", str(self.bare), cwd=self.h)
        git("commit", "-q", "--allow-empty", "-m", "base", cwd=self.h)
        git("push", "-q", "-u", "origin", "main", cwd=self.h)
        e.sql("UPDATE projects SET repo_path=? WHERE id='zz-test-proj'", (str(self.h),))
        e.seed_task(goal="G-001", status="in_progress")
        self.wt = self.h / "worktrees" / "G-001-T-0001"

    def prepare(self):
        with self.env.load_factory() as mod:
            con = mod.db()
            try:
                out = mod.prepare_worktree(con, "T-0001", "coder")
                con.commit()
                return 0, out
            except SystemExit as ex:
                return ex.code, None

    def make_clone(self):
        self.ok("config", "task_clones", "1")
        self.assertEqual(self.prepare()[0], 0)
        return self.wt

    def test_clone_adds_exactly_one_entry_and_nothing_else(self):
        self.write_cj()
        wt = self.make_clone()
        want = json.loads(json.dumps(OTHER))
        want["projects"][os.path.realpath(wt)] = SEED
        self.assertEqual(self.load(), want)
        self.assertEqual(self.cj().stat().st_mode & 0o777, 0o600)
        self.assertEqual(self.refusals(), [])

    def test_existing_entry_leaves_the_file_untouched(self):
        wt_key = os.path.realpath(self.wt)
        p = self.write_cj({"projects": {wt_key: {**SEED, "extra": 1}}})
        os.utime(p, ns=(1_000_000_000, 1_000_000_000))
        before = (p.read_bytes(), p.stat().st_mtime_ns)
        self.make_clone()
        self.assertEqual((p.read_bytes(), p.stat().st_mtime_ns), before)

    def test_unusable_claude_json_writes_nothing_logs_and_the_spawn_goes_on(self):
        big = self.cj()
        cases = {
            "missing": lambda: None,
            "invalid": lambda: big.write_text("{not json"),
            "symlink": lambda: big.symlink_to(self.env.home / "real.json"),
            "toolarge": lambda: big.write_text(json.dumps({"projects": {}, "pad": "x" * 64})),
        }
        (self.env.home / "real.json").write_text(json.dumps(OTHER))
        for name, setup in cases.items():
            with self.subTest(name):
                if big.is_symlink() or big.exists():
                    big.unlink()
                setup()
                self.env.sql("DELETE FROM events WHERE action='clone-trust-refused'")
                import shutil
                shutil.rmtree(self.wt, ignore_errors=True)
                self.env.sql("UPDATE tasks SET worktree=NULL WHERE id='T-0001'")
                self.ok("config", "task_clones", "1")
                if name == "toolarge":
                    with self.env.load_factory() as mod:
                        con = mod.db()
                        mod.make_task_clone(con, self.h, self.wt, "factory/t-0001", "main")
                        with unittest.mock.patch.object(mod.workerbox, "CLAUDE_JSON_MAX", 10):
                            mod.trust_task_clone(con, "T-0001", self.wt)
                        con.commit()
                else:
                    self.assertEqual(self.prepare()[0], 0)
                self.assertTrue((self.wt / ".git").is_dir())  # the clone exists: the spawn went on
                self.assertEqual(len(self.refusals()), 1)
                if name == "invalid":
                    self.assertEqual(big.read_text(), "{not json")
                if name == "missing":
                    self.assertFalse(big.exists())
                if name == "symlink":
                    self.assertEqual(json.loads((self.env.home / "real.json").read_text()), OTHER)

    def test_scope_refusals_write_nothing_and_log_one_event_each(self):
        self.write_cj()
        before = self.cj().read_bytes()
        wts = self.env.home_dir / "worktrees"
        wts.mkdir(exist_ok=True)
        outside = self.env.home / "outside"
        outside.mkdir()
        (wts / "link").symlink_to(outside)
        (outside / "sub").mkdir()
        with self.env.load_factory() as mod:
            con = mod.db()
            for path in (outside, wts, wts / "link" / "sub", wts / ".." / "outside"):
                with self.subTest(path=str(path)):
                    con.execute("DELETE FROM events WHERE action='clone-trust-refused'")
                    self.assertEqual(mod.trust_task_clone(con, "T-0001", path), "refused")
                    con.commit()
                    self.assertEqual(len(self.refusals()), 1)
                    self.assertEqual(self.cj().read_bytes(), before)


class CloneTrustCleanTests(TrustBase, CloneCleanBase):
    def test_clean_removes_only_the_cleaned_clones_entry(self):
        wt, _ = self.clone_task()
        key = os.path.realpath(wt)
        self.write_cj({"theme": "dark", "projects": {key: SEED, "/elsewhere/proj": SEED}})
        self.clean()
        self.assertFalse(wt.exists())
        self.assertEqual(self.load(), {"theme": "dark", "projects": {"/elsewhere/proj": SEED}})
        self.assertEqual(self.cj().stat().st_mode & 0o777, 0o600)

    def test_clean_without_an_entry_does_not_rewrite_the_file(self):
        wt, _ = self.clone_task()
        p = self.write_cj()
        os.utime(p, ns=(1_000_000_000, 1_000_000_000))
        self.clean()
        self.assertFalse(wt.exists())
        self.assertEqual(p.stat().st_mtime_ns, 1_000_000_000)
        self.assertEqual(self.load(), OTHER)


if __name__ == "__main__":
    unittest.main()
