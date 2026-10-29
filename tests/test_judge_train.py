"""T-0497: `factory judge --train T-a T-b ... --dry-run` plans a batch merge train (readiness + prefix trees) and
changes nothing: no push, no merge, no ref in the origin."""
import json
import os
import stat
import subprocess
import tempfile
from pathlib import Path

from harness import FactoryTestCase
from harness import git as harness_git

GREEN = [{"name": "unittest", "status": "COMPLETED", "conclusion": "SUCCESS"}]
RED = [{"name": "unittest", "status": "COMPLETED", "conclusion": "FAILURE"}]
URL = "https://github.com/zz/zz/pull/%d"

# Fake gh: state in $ZZ_GH_STATE; every call is appended to its "calls" list so a test can assert nothing was written.
GH_STUB = r'''#!/usr/bin/env python3
import json, os, subprocess, sys
argv = sys.argv[1:]
path = os.environ["ZZ_GH_STATE"]
st = json.load(open(path))
st["calls"].append(argv)
json.dump(st, open(path, "w"))
if argv[:2] == ["pr", "view"]:
    print(json.dumps(st["prs"][argv[2]]))
elif argv[:2] == ["repo", "view"]:
    print(json.dumps({"url": st["repo_url"]}))
elif argv[:1] == ["api"] and argv[1].endswith("/commits/main"):
    print(subprocess.run(["git", "-C", st["repo_url"], "rev-parse", "refs/heads/main"], capture_output=True, text=True).stdout.strip())
else:
    print("gh: unsupported by the test stub: " + " ".join(argv), file=sys.stderr)
    sys.exit(1)
'''


def git(cwd, *args):
    return harness_git(*args, cwd=cwd)


class JudgeTrainTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_project()
        e.seed_goal(project="zz-test-proj")
        e.seed_agent("zz-test-judge", "judge", goal="G-001")
        self.zz = Path(tempfile.mkdtemp(prefix="zz-test-jt-", dir=e.home))
        gh = self.zz / "bin" / "gh"
        gh.parent.mkdir()
        gh.write_text(GH_STUB)
        gh.chmod(gh.stat().st_mode | stat.S_IXUSR)
        self.state = self.zz / "gh.json"
        self.remote = e.home / "origin.git"
        self.wt = e.home / "wt"
        git(e.home, "init", "-q", "--bare", "-b", "main", str(self.remote))
        hook = self.remote / "hooks" / "pre-receive"  # any push at all is logged
        hook.write_text(f'#!/bin/sh\ncat >> "{self.zz}/pushes.log"\n')
        hook.chmod(0o755)
        git(e.home, "clone", "-q", str(self.remote), str(self.wt))
        self.write(self.wt, "base.txt", "line1\nline2\nline3\n")
        git(self.wt, "add", "-A")
        git(self.wt, "commit", "-q", "-m", "base")
        git(self.wt, "push", "-q", "origin", "HEAD:main")
        (self.zz / "pushes.log").write_text("")
        self.base = git(self.wt, "rev-parse", "HEAD")
        self.prs, self.heads = {}, {}

    def write(self, cwd, name, text):
        (Path(cwd) / name).write_text(text)

    def make_pr(self, n, files, checks=GREEN, verdicts=True):
        """PR n branches from base and sets `files` ({name: text}); heads are fetched through refs/pull/n/head."""
        tid = f"T-{n:04d}"
        git(self.wt, "checkout", "-q", "-B", f"b{n}", self.base)
        for name, text in files.items():
            self.write(self.wt, name, text)
        git(self.wt, "add", "-A")
        git(self.wt, "commit", "-q", "-m", f"pr {n}")
        head = git(self.wt, "rev-parse", "HEAD")
        git(self.wt, "update-ref", f"refs/pull/{n}/head", head)
        git(self.wt, "push", "-q", "--no-verify", "origin", f"{head}:refs/pull/{n}/head")
        self.heads[n] = head
        self.prs[URL % n] = {"number": n, "state": "OPEN", "headRefOid": head, "headRefName": f"b{n}", "baseRefName": "main",
                             "mergeable": "MERGEABLE", "isDraft": False, "url": URL % n, "statusCheckRollup": checks}
        self.env.seed_task(tid, pr_url=URL % n)
        self.env.sql("UPDATE tasks SET worktree=?, branch=? WHERE id=?", (str(self.wt), f"b{n}", tid))
        if verdicts:
            for role in ("reviewer", "qa"):
                self.verdict(tid, role, head)
        return tid

    def verdict(self, tid, role, sha, verdict="pass"):
        self.env.sql("INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) VALUES(?,?,?,?,?,?,?,?)",
                     (tid, f"x", sha, role, verdict, "ok", "zz", "t"))

    def finish_setup(self):
        git(self.wt, "checkout", "-q", "main")
        (self.zz / "pushes.log").write_text("")
        self.state.write_text(json.dumps({"prs": self.prs, "repo_url": str(self.remote), "calls": []}))

    def train(self, *tasks, extra=("--dry-run",)):
        self.finish_setup() if not self.state.exists() else None
        path = f"{self.zz / 'bin'}{os.pathsep}{self.env.env()['PATH']}"
        return self.env.run("judge", "--train", *tasks, *extra, agent="zz-test-judge",
                            extra_env={"PATH": path, "ZZ_GH_STATE": str(self.state)})

    def assert_nothing_written(self):
        self.assertEqual((self.zz / "pushes.log").read_text(), "", "a push reached origin")
        calls = json.loads(self.state.read_text())["calls"]
        self.assertFalse([c for c in calls if c[:2] in (["pr", "merge"], ["pr", "close"], ["pr", "edit"], ["pr", "comment"])
                          or c[:1] == ["api"] and "-X" in c], calls)
        refs = git(self.remote, "for-each-ref", "--format=%(refname)")
        self.assertNotIn("factory-train", refs)
        self.assertEqual(git(self.remote, "rev-parse", "refs/heads/main"), self.base)

    def test_all_ready(self):
        a = self.make_pr(1, {"a.txt": "a\n"})
        b = self.make_pr(2, {"b.txt": "b\n"})
        c = self.make_pr(3, {"c.txt": "c\n"})
        p = self.train(a, b, c)
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        lines = p.stdout.splitlines()
        self.assertIn(self.base[:8], lines[0])
        self.assertNotIn("DROPPED", p.stdout)
        steps = [l for l in lines if l[:2] in ("1.", "2.", "3.")]
        self.assertEqual(len(steps), 3, p.stdout)
        for k, (tid, n) in enumerate(((a, 1), (b, 2), (c, 3)), 1):
            self.assertIn(f"{k}. {tid} head={self.heads[n]} ", steps[k - 1])
            self.assertIn(f"ref=factory-train/{a}-{b}-{c}-{k}", steps[k - 1])
        # T_k really is the tree of main + PR1..PRk: the last one holds all three files and the base
        t3 = steps[2].split(" T_3=")[1].split()[0]
        self.assertEqual(sorted(git(self.wt, "ls-tree", "--name-only", t3).split()), ["a.txt", "b.txt", "base.txt", "c.txt"])
        t1 = steps[0].split(" T_1=")[1].split()[0]
        self.assertEqual(sorted(git(self.wt, "ls-tree", "--name-only", t1).split()), ["a.txt", "base.txt"])
        self.assert_nothing_written()

    def test_one_not_ready_is_dropped_and_rest_continue(self):
        a = self.make_pr(1, {"a.txt": "a\n"}, checks=RED)
        b = self.make_pr(2, {"b.txt": "b\n"})
        c = self.make_pr(3, {"c.txt": "c\n"}, verdicts=False)
        d = self.make_pr(4, {"d.txt": "d\n"})
        p = self.train(a, b, c, d)
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn(f"DROPPED {a}: checks not green: unittest=FAILURE", p.stdout)
        self.assertIn(f"DROPPED {c}: no reviewer PASS; no qa PASS", p.stdout)
        self.assertIn(f"1. {b} head={self.heads[2]}", p.stdout)
        self.assertIn(f"2. {d} head={self.heads[4]}", p.stdout)
        self.assertNotIn(f"{a} head=", p.stdout)
        self.assert_nothing_written()

    def test_stale_verdict_without_carry_is_dropped(self):
        a = self.make_pr(1, {"a.txt": "a\n"})
        b = self.make_pr(2, {"b.txt": "b\n"})
        self.env.sql("UPDATE verdicts SET head_sha=? WHERE task_id=? AND role='qa'", ("f" * 40, a))
        self.finish_setup()
        p = self.train(a, b)
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertRegex(p.stdout, rf"DROPPED {a}: qa PASS is for ffffffff, head is {self.heads[1][:8]}")
        self.assertIn(f"1. {b} head=", p.stdout)

    def test_conflict_drops_the_later_pr_and_rest_continue(self):
        a = self.make_pr(1, {"base.txt": "line1\nA\nline3\n"})
        b = self.make_pr(2, {"base.txt": "line1\nB\nline3\n"})  # conflicts with PR 1
        c = self.make_pr(3, {"c.txt": "c\n"})
        p = self.train(a, b, c)
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn(f"DROPPED {b}: conflicts with the train so far: base.txt", p.stdout)
        self.assertIn(f"1. {a} head={self.heads[1]}", p.stdout)
        self.assertIn(f"2. {c} head={self.heads[3]}", p.stdout)
        self.assertIn(f"ref=factory-train/{a}-{c}-2", p.stdout)
        self.assert_nothing_written()

    def test_more_than_four_refused(self):
        ids = [self.make_pr(n, {f"f{n}.txt": "x\n"}) for n in range(1, 6)]
        p = self.train(*ids)
        self.assertEqual(p.returncode, 2, p.stdout + p.stderr)
        self.assertIn("--train takes 2..4", p.stderr)
        self.assertEqual(json.loads(self.state.read_text())["calls"], [])  # refused before any gh call

    def test_dry_run_on_merge_is_refused_and_nothing_merges(self):
        a = self.make_pr(1, {"a.txt": "a\n"})
        self.finish_setup()
        path = f"{self.zz / 'bin'}{os.pathsep}{self.env.env()['PATH']}"
        p = self.env.run("judge", a, "--merge", "--reason", "x", "--dry-run", agent="zz-test-judge",
                         extra_env={"PATH": path, "ZZ_GH_STATE": str(self.state)})
        self.assertEqual(p.returncode, 2, p.stdout + p.stderr)
        self.assertIn("--dry-run is only for --train", p.stderr)
        self.assert_nothing_written()

    def test_duplicate_ids_refused(self):
        a = self.make_pr(1, {"a.txt": "a\n"})
        b = self.make_pr(2, {"b.txt": "b\n"})
        p = self.train(a, a, b)
        self.assertEqual(p.returncode, 2, p.stdout + p.stderr)
        self.assertIn("distinct", p.stderr)

    def test_train_with_positional_task_refused(self):
        a = self.make_pr(1, {"a.txt": "a\n"})
        b = self.make_pr(2, {"b.txt": "b\n"})
        p = self.train(a, b, extra=("--dry-run", "--reason", "x"))
        self.assertEqual(p.returncode, 2, p.stdout + p.stderr)
        self.assertIn("no task argument or --reason", p.stderr)

    def test_auto_merge_zero_project_is_dropped(self):
        a = self.make_pr(1, {"a.txt": "a\n"})
        b = self.make_pr(2, {"b.txt": "b\n"})
        self.env.sql("UPDATE projects SET auto_merge=0")
        p = self.train(a, b)
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn("auto_merge=0", p.stdout)

    def test_single_task_refused(self):
        a = self.make_pr(1, {"a.txt": "a\n"})
        p = self.train(a)
        self.assertEqual(p.returncode, 2, p.stdout + p.stderr)
        self.assertIn("--train takes 2..4", p.stderr)

    def test_train_without_dry_run_refused(self):
        a = self.make_pr(1, {"a.txt": "a\n"})
        b = self.make_pr(2, {"b.txt": "b\n"})
        p = self.train(a, b, extra=())
        self.assertEqual(p.returncode, 2, p.stdout + p.stderr)
        self.assertIn("--dry-run", p.stderr)
        self.assert_nothing_written()

    def test_no_ready_task_exits_nonzero(self):
        a = self.make_pr(1, {"a.txt": "a\n"}, checks=RED)
        b = self.make_pr(2, {"b.txt": "b\n"}, checks=RED)
        p = self.train(a, b)
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn("nothing to plan", p.stderr)

    def test_only_judge_may_plan(self):
        a = self.make_pr(1, {"a.txt": "a\n"})
        b = self.make_pr(2, {"b.txt": "b\n"})
        self.finish_setup()
        self.env.seed_agent("zz-test-coder", "coder", goal="G-001")
        path = f"{self.zz / 'bin'}{os.pathsep}{self.env.env()['PATH']}"
        p = self.env.run("judge", "--train", a, b, "--dry-run", agent="zz-test-coder",
                         extra_env={"PATH": path, "ZZ_GH_STATE": str(self.state)})
        self.assertNotEqual(p.returncode, 0)
        self.assertNotIn("TRAIN", p.stdout)
