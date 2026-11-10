"""T-1220: per-task clones get their Claude workspace-trust entry pre-registered in the owner's ~/.claude.json
(temp HOME only), strictly under FACTORY_HOME/worktrees/, and `factory clean` drops it again."""
import json
import os
import stat
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


class CloneTrustBranchTests(TrustBase, FactoryTestCase):
    """The security branches of the ~/.claude.json write: foreign owner, not a regular file, a racing writer, and
    the clean-side (add=False) scope refusal."""

    def setUp(self):
        super().setUp()
        self.env.seed_project()
        self.env.seed_goal(project="zz-test-proj", title="zz-test clone goal")
        self.env.seed_task(goal="G-001", status="in_progress")
        self.wts = self.env.home_dir / "worktrees"
        self.wts.mkdir(exist_ok=True)
        self.clone = self.wts / "G-001-T-0001"
        self.clone.mkdir()

    def trust(self, mod, con, add=True, path=None):
        res = mod.trust_task_clone(con, "T-0001", path or self.clone, add)
        con.commit()
        return res

    def test_foreign_uid_owner_writes_nothing_and_records_a_refusal(self):
        p = self.write_cj()
        before = p.read_bytes()
        real_fstat = os.fstat

        def foreign(fd):
            st = real_fstat(fd)
            return os.stat_result((st.st_mode, st.st_ino, st.st_dev, st.st_nlink, os.getuid() + 1, *tuple(st)[5:]))

        with self.env.load_factory() as mod:
            con = mod.db()
            with unittest.mock.patch.object(mod.workerbox.os, "fstat", foreign):
                self.assertEqual(self.trust(mod, con), "refused")
        self.assertEqual(p.read_bytes(), before)
        got = self.refusals()
        self.assertEqual(len(got), 1)
        self.assertIn("not owned by uid", got[0]["reason"])

    def test_non_regular_file_writes_nothing_and_records_a_refusal(self):
        p = self.cj()
        with self.env.load_factory() as mod:
            con = mod.db()
            for name in ("directory", "fifo"):
                with self.subTest(name):
                    con.execute("DELETE FROM events WHERE action='clone-trust-refused'")
                    if p.is_dir():
                        (p / "keep").unlink()
                        p.rmdir()
                    if name == "directory":
                        p.mkdir()
                        (p / "keep").write_text("x")
                    else:
                        os.mkfifo(p)
                    self.assertEqual(self.trust(mod, con), "refused")
                    got = self.refusals()
                    self.assertEqual(len(got), 1)
                    self.assertIn("not a regular file", got[0]["reason"])
                    if name == "directory":
                        self.assertEqual([x.name for x in p.iterdir()], ["keep"])
                    else:
                        self.assertTrue(stat.S_ISFIFO(os.lstat(p).st_mode))
                        p.unlink()
                        p.mkdir()  # next subtest cleans this up again

    def racing(self, mod, rewrites):
        """Patch lstat so the first `rewrites` lstat calls on ~/.claude.json are preceded by another writer's rewrite."""
        real = os.lstat
        state = {"n": 0}
        other = self.cj()

        def lstat(path, *a, **k):
            if str(path) == str(other) and state["n"] < rewrites:
                state["n"] += 1
                data = dict(OTHER, racer=state["n"], pad="y" * state["n"])
                tmp = other.with_name("racer.tmp")
                tmp.write_text(json.dumps(data))
                tmp.chmod(0o600)
                os.replace(tmp, other)
            return real(path, *a, **k)

        return unittest.mock.patch.object(mod.workerbox.os, "lstat", lstat), state

    def test_a_rewrite_between_read_and_rename_is_retried_once_then_merged(self):
        self.write_cj()
        with self.env.load_factory() as mod:
            con = mod.db()
            ctx, state = self.racing(mod, 1)
            with ctx:
                self.assertEqual(self.trust(mod, con), "added")
        self.assertEqual(state["n"], 1)
        got = self.load()
        self.assertEqual(got["racer"], 1)  # the other writer's key survived
        self.assertEqual(got["projects"][os.path.realpath(self.clone)], SEED)
        self.assertEqual(self.refusals(), [])

    def test_a_second_rewrite_gives_up_without_dropping_the_other_writers_keys(self):
        self.write_cj()
        with self.env.load_factory() as mod:
            con = mod.db()
            ctx, state = self.racing(mod, 99)
            reads = []
            real_read = mod.workerbox._read_owner_json

            def counting(home):
                reads.append(1)
                return real_read(home)

            with ctx, unittest.mock.patch.object(mod.workerbox, "_read_owner_json", counting):
                self.assertEqual(self.trust(mod, con), "refused")
        self.assertEqual(len(reads), 2)  # one read plus exactly one retry
        self.assertEqual(state["n"], 2)
        got = self.load()
        self.assertEqual(got["racer"], 2)  # the last writer's content, untouched
        self.assertNotIn(os.path.realpath(self.clone), got["projects"])
        self.assertEqual(len(self.refusals()), 1)
        self.assertIn("changed twice", self.refusals()[0]["reason"])

    def test_clean_side_refuses_outside_and_symlinked_paths_and_removes_no_entry(self):
        outside = self.env.home / "outside"
        (outside / "sub").mkdir(parents=True)
        (self.wts / "link").symlink_to(outside)
        link_clone = self.wts / "linkclone"
        link_clone.symlink_to(self.clone)
        cases = (outside, outside / "sub", self.wts, self.wts / "link" / "sub", link_clone)
        entries = {str(c): SEED for c in cases}
        entries[os.path.realpath(self.clone)] = SEED
        p = self.write_cj({**OTHER, "projects": entries})
        before = p.read_bytes()
        with self.env.load_factory() as mod:
            con = mod.db()
            for path in cases:
                with self.subTest(path=str(path)):
                    con.execute("DELETE FROM events WHERE action='clone-trust-refused'")
                    self.assertEqual(self.trust(mod, con, add=False, path=path), "refused")
                    self.assertEqual(len(self.refusals()), 1)
                    self.assertEqual(p.read_bytes(), before)


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
