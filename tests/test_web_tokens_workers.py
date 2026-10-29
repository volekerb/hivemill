import os
import shutil
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from tests.test_web_tokens import line, usage_row
import web.tokens
from web.tokens import TokenIndex

TS = "2026-10-20T10:00:00Z"


class _Base(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.root = self.tmp / "home" / ".claude" / "projects"
        self.workers = self.tmp / "home" / ".local" / "state" / "factory" / "workers"
        self.wproj = self.workers / "coder-t-1" / "claude" / "projects"

    def put(self, base, rel, text):
        p = base / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(text)
        return p

    def index(self, **kw):
        i = TokenIndex(self.root, workers_root=self.workers, **kw)
        i.refresh()
        return i

    def total(self, snap):
        return sum(r["input"] for r in snap["rows"]), sum(r["requests"] for r in snap["rows"])


class WorkerRootTests(_Base):
    def test_both_roots_counted(self):
        self.put(self.root, "p/a.jsonl", line(usage_row("a", "m1", "r1", TS, inp=5)))
        self.put(self.wproj, "q/b.jsonl", line(usage_row("b", "m2", "r2", TS, inp=7)))
        snap = self.index().snapshot()
        self.assertEqual(self.total(snap), (12, 2))
        self.assertEqual(set(snap["sessions"]), {"a", "b"})

    def test_same_basename_two_entries(self):
        self.put(self.root, "p/s.jsonl", line(usage_row("s", "m1", "r1", TS, inp=5)))
        self.put(self.wproj, "p/s.jsonl", line(usage_row("s", "m2", "r2", TS, inp=7)))
        i = self.index()
        self.assertEqual(len(i._files), 2)
        self.assertEqual(i.snapshot()["files"], 2)
        self.assertEqual(self.total(i.snapshot()), (12, 2))

    def test_second_run_reads_nothing(self):
        self.put(self.root, "p/a.jsonl", line(usage_row("a", "m1", "r1", TS)))
        self.put(self.wproj, "q/b.jsonl", line(usage_row("b", "m2", "r2", TS)))
        i = self.index()
        self.assertGreater(i.snapshot()["bytes_read"], 0)
        i.refresh()
        self.assertEqual(i.snapshot()["bytes_read"], 0)

    def test_no_workers_root_same_as_before(self):
        self.put(self.root, "p/a.jsonl", line(usage_row("a", "m1", "r1", TS, inp=5)))
        with_w = self.index().snapshot()
        plain = TokenIndex(self.root)
        plain.refresh()
        self.assertIsNone(plain.workers_root)
        for k in ("rows", "sessions", "files"):
            self.assertEqual(with_w[k], plain.snapshot()[k])
        self.assertEqual(self.total(plain.snapshot()), (5, 1))

    def test_symlink_out_of_root_skipped(self):
        outside = self.put(self.tmp, "outside/x.jsonl", line(usage_row("x", "m9", "r9", TS, inp=99)))
        self.wproj.joinpath("q").mkdir(parents=True)
        os.symlink(outside, self.wproj / "q" / "x.jsonl")
        self.put(self.wproj, "q/ok.jsonl", line(usage_row("ok", "m1", "r1", TS, inp=3)))
        snap = self.index().snapshot()
        self.assertEqual(set(snap["sessions"]), {"ok"})
        self.assertEqual(self.total(snap), (3, 1))

    def test_symlinked_project_dir_out_of_root_skipped(self):
        self.put(self.tmp, "outside/d/x.jsonl", line(usage_row("x", "m9", "r9", TS, inp=99)))
        self.wproj.mkdir(parents=True)
        os.symlink(self.tmp / "outside" / "d", self.wproj / "evil")
        self.assertEqual(self.index().snapshot()["sessions"], {})

    def test_symlink_to_other_agents_file_skipped(self):
        other = self.put(self.workers / "coder-t-2" / "claude" / "projects", "q/o.jsonl",
                         line(usage_row("o", "m5", "r5", TS, inp=4)))
        self.wproj.joinpath("q").mkdir(parents=True)
        os.symlink(other, self.wproj / "q" / "link.jsonl")
        snap = self.index().snapshot()
        self.assertEqual(set(snap["sessions"]), {"o"})

    def test_agent_dir_symlink_out_skipped(self):
        self.put(self.tmp, "elsewhere/claude/projects/q/x.jsonl", line(usage_row("x", "m9", "r9", TS, inp=99)))
        self.workers.mkdir(parents=True)
        os.symlink(self.tmp / "elsewhere", self.workers / "evil-agent")
        self.assertEqual(self.index().snapshot()["sessions"], {})

    def test_only_session_limits_worker_root_files(self):
        self.put(self.root, "p/a.jsonl", line(usage_row("a", "m1", "r1", TS, inp=5)))
        self.put(self.wproj, "q/b.jsonl", line(usage_row("b", "m2", "r2", TS, inp=7)))
        self.put(self.wproj, "q/c.jsonl", line(usage_row("c", "m3", "r3", TS, inp=11)))
        self.put(self.wproj, "q/b/subagents/s1.jsonl", line(usage_row("b", "m4", "r4", TS, inp=2)))
        snap = self.index(only_session="b").snapshot()
        self.assertEqual(set(snap["sessions"]), {"b"})
        self.assertEqual(snap["files"], 2)
        self.assertEqual(self.total(snap), (9, 2))

    def test_only_session_still_skips_symlink_out(self):
        outside = self.put(self.tmp, "outside/b.jsonl", line(usage_row("b", "m9", "r9", TS, inp=99)))
        self.wproj.joinpath("q").mkdir(parents=True)
        os.symlink(outside, self.wproj / "q" / "b.jsonl")
        self.assertEqual(self.index(only_session="b").snapshot()["sessions"], {})
        self.put(self.wproj, "q/c.jsonl", line(usage_row("c", "m1", "r1", TS, inp=3)))
        self.assertEqual(set(self.index().snapshot()["sessions"]), {"c"})

    def test_extra_roots(self):
        extra = self.tmp / "extra"
        self.put(extra, "p/e.jsonl", line(usage_row("e", "m1", "r1", TS, inp=2)))
        self.put(self.root, "p/a.jsonl", line(usage_row("a", "m2", "r2", TS, inp=5)))
        self.assertEqual(self.total(self.index(extra_roots=[extra]).snapshot()), (7, 2))

    def test_default_scans_workers_root_constant(self):
        import web.tokens as t
        old = t.WORKERS_ROOT
        t.WORKERS_ROOT = str(self.workers)
        self.addCleanup(setattr, t, "WORKERS_ROOT", old)
        self.assertEqual(TokenIndex().workers_root, self.workers)
        self.assertIsNone(TokenIndex(self.root).workers_root)

class SwapAfterDiscoverTests(_Base):
    """The file passes discover, then is swapped before it is opened."""

    def discovered(self):
        i = TokenIndex(self.root, workers_root=self.workers)
        return i, i._discover()

    def refresh_with(self, i, found):
        with mock.patch.object(TokenIndex, "_discover", return_value=found):
            i.refresh()

    def test_file_swapped_to_outside_symlink_not_read(self):
        f = self.put(self.wproj, "q/b.jsonl", line(usage_row("b", "m2", "r2", TS, inp=7)))
        outside = self.put(self.tmp, "outside/secret.jsonl", line(usage_row("b", "m9", "r9", TS, inp=99)))
        i, found = self.discovered()
        self.assertEqual(len(found), 1)
        f.unlink()
        os.symlink(outside, f)
        self.refresh_with(i, found)
        self.assertEqual(self.total(i.snapshot()), (0, 0))
        self.assertEqual(i.snapshot()["bytes_read"], 0)

    def test_parent_dir_swapped_to_outside_symlink_not_read(self):
        self.put(self.wproj, "q/b.jsonl", line(usage_row("b", "m2", "r2", TS, inp=7)))
        self.put(self.tmp, "outside/q/b.jsonl", line(usage_row("b", "m9", "r9", TS, inp=99)))
        i, found = self.discovered()
        shutil.rmtree(self.wproj / "q")
        os.symlink(self.tmp / "outside" / "q", self.wproj / "q")
        self.refresh_with(i, found)
        self.assertEqual(self.total(i.snapshot()), (0, 0))

    def test_unswapped_file_still_read_through_same_path(self):
        self.put(self.wproj, "q/b.jsonl", line(usage_row("b", "m2", "r2", TS, inp=7)))
        i, found = self.discovered()
        self.refresh_with(i, found)
        self.assertEqual(self.total(i.snapshot()), (7, 1))


class HardlinkTests(_Base):
    def test_hardlink_to_outside_file_not_read(self):
        outside = self.put(self.tmp, "outside/x.jsonl", line(usage_row("x", "m9", "r9", TS, inp=99)))
        (self.wproj / "q").mkdir(parents=True)
        os.link(outside, self.wproj / "q" / "x.jsonl")
        self.put(self.wproj, "q/ok.jsonl", line(usage_row("ok", "m1", "r1", TS, inp=3)))
        snap = self.index().snapshot()
        self.assertEqual(set(snap["sessions"]), {"ok"})
        self.assertEqual(self.total(snap), (3, 1))

    def test_hardlink_made_after_discover_not_read(self):
        outside = self.put(self.tmp, "outside/x.jsonl", line(usage_row("x", "m9", "r9", TS, inp=99)))
        mine = self.put(self.wproj, "q/x.jsonl", line(usage_row("x", "m1", "r1", TS, inp=1)))
        mine.unlink()
        os.link(outside, mine)
        with mock.patch.object(TokenIndex, "_inside", staticmethod(lambda p, c: True)):
            snap = self.index().snapshot()
        self.assertEqual(self.total(snap), (0, 0))


class ReadCapTests(_Base):
    def rows(self, n):
        return "".join(line(usage_row("b", "m", f"r{k}", TS, inp=1)) for k in range(n))

    def test_tail_over_cap_read_in_bounded_chunks(self):
        text = self.rows(10)
        self.put(self.root, "p/b.jsonl", text)
        cap = len(text) // 3
        with mock.patch.object(web.tokens, "MAX_READ_BYTES", cap):
            i = TokenIndex(self.root)
            i.refresh()
            first = i.snapshot()["bytes_read"]
            self.assertGreater(first, 0)
            self.assertLessEqual(first, cap)
            self.assertLess(self.total(i.snapshot())[1], 10)
            for _ in range(10):
                i.refresh()
        self.assertEqual(self.total(i.snapshot()), (10, 10))

    def test_worker_file_over_cap_not_read_whole(self):
        text = self.rows(10)
        self.put(self.wproj, "q/b.jsonl", text)
        with mock.patch.object(web.tokens, "MAX_READ_BYTES", len(text) // 3):
            i = self.index()
        self.assertLessEqual(i.snapshot()["bytes_read"], len(text) // 3)
        self.assertLess(self.total(i.snapshot())[1], 10)

    def test_single_line_over_cap_skipped_and_logged(self):
        good = line(usage_row("b", "m", "r1", TS, inp=3))
        cap = len(good) + 10
        self.put(self.root, "p/b.jsonl", "x" * (cap * 3) + "\n" + good)
        with mock.patch.object(web.tokens, "MAX_READ_BYTES", cap), mock.patch("sys.stderr") as err:
            i = TokenIndex(self.root)
            for _ in range(20):
                i.refresh()
        self.assertTrue(any("skipped" in str(c) for c in err.write.call_args_list))
        self.assertEqual(self.total(i.snapshot())[0], 3)

    def test_cap_is_64_mib(self):
        self.assertEqual(web.tokens.MAX_READ_BYTES, 64 << 20)

    def test_tail_of_skipped_line_gives_no_parse_errors(self):
        good = line(usage_row("b", "m", "r1", TS, inp=3))
        cap = len(good) + 10
        junk = '{"usage": ' * (cap // 5)      # parses badly in any fragment, spans several chunks
        self.put(self.root, "p/b.jsonl", junk + "\n" + good)
        with mock.patch.object(web.tokens, "MAX_READ_BYTES", cap), mock.patch("sys.stderr"):
            i = TokenIndex(self.root)
            for _ in range(30):
                i.refresh()
        self.assertEqual(self.total(i.snapshot())[0], 3)
        self.assertEqual(i.snapshot()["errors"], 1)


if __name__ == "__main__":
    unittest.main()
