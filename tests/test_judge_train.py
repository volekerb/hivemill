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


# ── T-0522 acceptance (test-first, qa T-0756): `factory judge --train T-a T-b ...` WITHOUT --dry-run ─────────────────────
# Fake gh with a real `pr merge`: it squash-merges the PR head into the bare origin's main (tree = merge-tree of main and
# head, one parent) and records each merge with the tree main had right afterwards. State keys the tests set:
#   merge_fail: gh pr merge exits 1;  inject_after: {"<pr number>": {"name": f, "text": t}} lands a foreign commit on main
#   right after that PR's merge (another PR merging at the same moment).
GH_MERGE_STUB = r'''#!/usr/bin/env python3
import json, os, subprocess, sys
argv = sys.argv[1:]
path = os.environ["ZZ_GH_STATE"]
st = json.load(open(path))
st["calls"].append(argv)


def save():
    json.dump(st, open(path, "w"))


def git(*a, inp=None):
    env = dict(os.environ, GIT_AUTHOR_NAME="gh", GIT_AUTHOR_EMAIL="gh@example.invalid",
               GIT_COMMITTER_NAME="gh", GIT_COMMITTER_EMAIL="gh@example.invalid")
    p = subprocess.run(["git", "-C", st["repo_url"], *a], capture_output=True, text=True, env=env, input=inp)
    return p.returncode, p.stdout.strip(), p.stderr.strip()


def fail(msg):
    save()
    print(msg, file=sys.stderr)
    sys.exit(1)


if argv[:2] == ["pr", "view"]:
    save()
    print(json.dumps(st["prs"][argv[2]]))
elif argv[:2] == ["repo", "view"]:
    save()
    print(json.dumps({"url": st["repo_url"]}))
elif argv[:1] == ["api"] and argv[1].endswith("/commits/main"):
    save()
    print(git("rev-parse", "refs/heads/main")[1])
elif argv[:2] in (["pr", "close"], ["pr", "comment"]):
    save()
elif argv[:2] == ["pr", "merge"]:
    pr = st["prs"][argv[2]]
    if st.get("merge_fail"):
        fail("gh: pr merge failed (test stub)")
    head = argv[argv.index("--match-head-commit") + 1] if "--match-head-commit" in argv else None
    if head != pr["headRefOid"]:
        fail("gh: head branch was modified (test stub: --match-head-commit mismatch)")
    main = git("rev-parse", "refs/heads/main")[1]
    rc, out, err = git("merge-tree", "--write-tree", main, head)
    if rc != 0:
        fail("gh: not mergeable: " + err)
    tree = out.splitlines()[0]
    commit = git("commit-tree", tree, "-p", main, "-m", "squash (#%d)" % pr["number"])[1]
    git("update-ref", "refs/heads/main", commit)
    pr["state"] = "MERGED"
    st.setdefault("merges", []).append({"url": argv[2], "number": pr["number"], "head": head, "argv": argv, "tree": tree})
    extra = (st.get("inject_after") or {}).get(str(pr["number"]))
    if extra:
        blob = git("hash-object", "-w", "--stdin", inp=extra["text"])[1]
        entries = git("ls-tree", commit)[1]
        tree2 = git("mktree", inp=entries + "\n100644 blob %s\t%s\n" % (blob, extra["name"]))[1]
        c2 = git("commit-tree", tree2, "-p", commit, "-m", "foreign commit")[1]
        git("update-ref", "refs/heads/main", c2)
    save()
else:
    fail("gh: unsupported by the test stub: " + " ".join(argv))
'''

# gate_cmd: log "<HEAD sha> <files in the tree>" per real execution, fail iff bad.txt is in the tree under test
GATE_LOGGING = r'''echo "$(git rev-parse HEAD) $(ls | tr '\n' ' ')" >> "$GATE_LOG"; test ! -e bad.txt'''
AGENT = "zz-test-judge"


class JudgeTrainRunTests(FactoryTestCase):
    """The non-dry-run train: one gate run on the tip, then merges in train order, bisecting on a failure."""

    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_project()
        e.seed_goal(project="zz-test-proj")
        e.seed_agent(AGENT, "judge", goal="G-001")
        e.seed_agent("zz-test-lead", "lead", goal="G-001")
        e.seed_team("g-001-a", goal="G-001", lead="zz-test-lead")
        self.zz = Path(tempfile.mkdtemp(prefix="zz-test-jt-", dir=e.home))
        gh = self.zz / "bin" / "gh"
        gh.parent.mkdir()
        gh.write_text(GH_MERGE_STUB)
        gh.chmod(gh.stat().st_mode | stat.S_IXUSR)
        self.state = self.zz / "gh.json"
        self.gate_log = self.zz / "gate.log"
        self.remote = e.home / "origin.git"
        self.wt = e.home / "wt"
        git(e.home, "init", "-q", "--bare", "-b", "main", str(self.remote))
        hook = self.remote / "hooks" / "pre-receive"  # any push at all is logged
        hook.write_text(f'#!/bin/sh\ncat >> "{self.zz}/pushes.log"\n')
        hook.chmod(0o755)
        git(e.home, "clone", "-q", str(self.remote), str(self.wt))
        (self.wt / "base.txt").write_text("line1\nline2\nline3\n")
        git(self.wt, "add", "-A")
        git(self.wt, "commit", "-q", "-m", "base")
        git(self.wt, "push", "-q", "origin", "HEAD:main")
        self.base = git(self.wt, "rev-parse", "HEAD")
        self.prs, self.heads, self.ids = {}, {}, {}
        e.sql("UPDATE projects SET gh_repo='zz/zz', repo_path=?, gate_cmd=?, gate_timeout_s=60", (str(self.wt), GATE_LOGGING))

    make_pr = JudgeTrainTests.make_pr
    verdict = JudgeTrainTests.verdict
    write = JudgeTrainTests.write

    def pr(self, n, files=None, **kw):
        tid = self.make_pr(n, files or {f"f{n}.txt": f"{n}\n"}, **kw)
        self.env.sql("UPDATE tasks SET team_id='g-001-a' WHERE id=?", (tid,))
        self.ids[n] = tid
        return tid

    def prepare(self):
        git(self.wt, "checkout", "-q", "main")
        (self.zz / "pushes.log").write_text("")
        self.state.write_text(json.dumps({"prs": self.prs, "repo_url": str(self.remote), "calls": [], "merges": []}))

    def gh_state(self):
        return json.loads(self.state.read_text())

    def edit_state(self, **kw):
        st = self.gh_state()
        st.update(kw)
        self.state.write_text(json.dumps(st))

    def set_gate(self, cmd, timeout=60):
        self.env.sql("UPDATE projects SET gate_cmd=?, gate_timeout_s=?", (cmd, timeout))

    def train(self, *tasks):
        if not self.state.exists():
            self.prepare()
        path = f"{self.zz / 'bin'}{os.pathsep}{self.env.env()['PATH']}"
        return self.env.run("judge", "--train", *tasks, agent=AGENT, extra_env={
            "PATH": path, "ZZ_GH_STATE": str(self.state), "GATE_LOG": str(self.gate_log),
            "FACTORY_GATE_SLOT_WAIT_S": "0", "FACTORY_GATE_POLL_S": "0"})

    # -- observations
    def merged_numbers(self):
        return [m["number"] for m in self.gh_state()["merges"]]

    def gate_executions(self):
        return [l.split() for l in self.gate_log.read_text().splitlines()] if self.gate_log.exists() else []

    def train_runs(self):
        return self.env.sql("SELECT * FROM gate_runs WHERE kind='train' ORDER BY id")

    def status(self, n):
        return self.env.sql("SELECT status FROM tasks WHERE id=?", (self.ids[n],))[0]["status"]

    def judge_verdicts(self, n, verdict=None):
        q = "SELECT * FROM verdicts WHERE task_id=? AND role='judge'" + (" AND verdict=?" if verdict else "")
        return self.env.sql(q, (self.ids[n], verdict) if verdict else (self.ids[n],))

    def main_tree(self):
        return git(self.remote, "rev-parse", "refs/heads/main^{tree}")

    def expected_trees(self, nums):
        """Trees of origin/main + PR nums[0..k], built independently with merge-tree (the PRs touch disjoint files)."""
        tip, out = self.base, []
        for n in nums:
            tree = git(self.wt, "merge-tree", "--write-tree", tip, self.heads[n])
            tip = git(self.wt, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit-tree", tree,
                      "-p", tip, "-p", self.heads[n], "-m", f"step {n}")
            out.append(tree)
        return out

    def train_ref(self, *ids):
        return "refs/factory-train/" + "-".join(ids)

    def assert_train_ref_not_pushed(self):
        self.assertEqual((self.zz / "pushes.log").read_text(), "", "a push reached origin (the train ref must stay local)")
        self.assertNotIn("factory-train", git(self.remote, "for-each-ref", "--format=%(refname)"))
        calls = self.gh_state()["calls"]
        self.assertFalse([c for c in calls if c[:1] == ["api"] and "-X" in c], calls)

    def assert_no_merge_happened(self, p):
        self.assertEqual(self.gh_state()["merges"], [], p.stdout + p.stderr)
        self.assertEqual(self.main_tree(), git(self.wt, "rev-parse", f"{self.base}^{{tree}}"))
        for n in self.ids:
            self.assertEqual(self.status(n), "in_review")
            self.assertEqual(self.judge_verdicts(n, "merge"), [])

    # -- 1/2: all pass
    def test_all_pass_merges_every_pr_in_order_with_tree_asserts(self):
        a, b, c = self.pr(1), self.pr(2), self.pr(3)
        t1, t2, t3 = self.expected_trees([1, 2, 3])
        p = self.train(a, b, c)
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        merges = self.gh_state()["merges"]
        self.assertEqual([m["number"] for m in merges], [1, 2, 3], p.stdout + p.stderr)
        for m, n in zip(merges, (1, 2, 3)):  # the existing merge path: squash, pinned to the exact head
            self.assertEqual(m["head"], self.heads[n])
            self.assertIn("--match-head-commit", m["argv"])
            self.assertEqual(m["argv"][m["argv"].index("--match-head-commit") + 1], self.heads[n])
        self.assertEqual([m["tree"] for m in merges], [t1, t2, t3])  # origin/main tree == T_k after each merge
        self.assertEqual(self.main_tree(), t3)
        self.assertEqual(sorted(git(self.remote, "ls-tree", "--name-only", "refs/heads/main").split()),
                         ["base.txt", "f1.txt", "f2.txt", "f3.txt"])
        for n in (1, 2, 3):
            self.assertEqual(self.status(n), "done")
            self.assertEqual(len(self.judge_verdicts(n, "merge")), 1)

    def test_all_pass_runs_the_gate_once_on_the_tip_and_records_the_train_run(self):
        a, b, c = self.pr(1), self.pr(2), self.pr(3)
        t3 = self.expected_trees([1, 2, 3])[2]
        p = self.train(a, b, c)
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        runs = self.train_runs()
        self.assertEqual(len(runs), 1, [dict(r) for r in runs])
        r = runs[0]
        self.assertEqual((r["status"], r["kind"], r["project_id"]), ("pass", "train", "zz-test-proj"))
        for tid in (a, b, c):
            self.assertIn(tid, r["task_ids"])
        self.assertEqual(r["main_sha"], self.base)
        self.assertEqual(r["tree"], t3)
        tip = git(self.wt, "rev-parse", self.train_ref(a, b, c))  # the tip commit is a local ref refs/factory-train/<ids>
        self.assertEqual(r["tip_sha"], tip)
        self.assertEqual(git(self.wt, "rev-parse", f"{tip}^{{tree}}"), t3)
        runs_of_cmd = self.gate_executions()
        self.assertEqual(len(runs_of_cmd), 1, runs_of_cmd)  # ONE gate execution for three PRs
        self.assertEqual(runs_of_cmd[0][0], tip)  # run at the tip sha ...
        self.assertEqual(sorted(runs_of_cmd[0][1:]), ["base.txt", "f1.txt", "f2.txt", "f3.txt"])  # ... on the tip tree
        for n in (1, 2, 3):  # the merge verdict of every task cites the gate run id
            summary = self.judge_verdicts(n, "merge")[0]["summary"]
            self.assertRegex(summary, rf"(?i)gate run #?{r['id']}\b", summary)

    def test_train_ref_is_never_pushed(self):
        a, b = self.pr(1), self.pr(2)
        p = self.train(a, b)
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assert_train_ref_not_pushed()

    # -- 3: tip fails, bisect
    def test_tip_fail_bisects_to_the_bad_pr_and_merges_the_good_prefix(self):
        a, b, c, d = self.pr(1), self.pr(2, {"bad.txt": "bad\n"}), self.pr(3), self.pr(4)
        t1 = self.expected_trees([1])[0]
        p = self.train(a, b, c, d)
        self.assertEqual(self.merged_numbers(), [1], p.stdout + p.stderr)  # only the good prefix T_1
        self.assertEqual(self.main_tree(), t1)
        self.assertEqual(self.status(1), "done")
        for n in (2, 3, 4):
            self.assertEqual(self.status(n), "in_review")
            self.assertEqual(self.judge_verdicts(n, "merge"), [])
        # N=4: tip T_4 fails, T_2 fails, T_1 passes = the tip run plus exactly 2 bisect runs
        ran = self.gate_executions()
        self.assertEqual(len(ran), 3, ran)
        self.assertEqual([sorted(x[1:]) for x in ran], [["bad.txt", "base.txt", "f1.txt", "f3.txt", "f4.txt"],
                                                       ["bad.txt", "base.txt", "f1.txt"], ["base.txt", "f1.txt"]])
        runs = self.train_runs()
        self.assertEqual([r["status"] for r in runs], ["fail", "fail", "pass"], [dict(r) for r in runs])
        self.assertEqual(self.judge_verdicts(2, "reject"), [])  # the train never records a REJECT itself
        self.assert_train_ref_not_pushed()

    def test_first_failing_pr_gets_a_reject_candidate_message_to_its_lead_with_the_log_path(self):
        a, b = self.pr(1), self.pr(2, {"bad.txt": "bad\n"})
        p = self.train(a, b)
        self.assertEqual(self.merged_numbers(), [1], p.stdout + p.stderr)
        failed = [r for r in self.train_runs() if r["status"] == "fail"]
        self.assertTrue(failed, p.stdout + p.stderr)
        msgs = self.env.sql("SELECT * FROM messages WHERE task_id=? AND recipient IN ('zz-test-lead','lead:g-001-a')", (self.ids[2],))
        self.assertEqual(len(msgs), 1, p.stdout + p.stderr)
        self.assertIn("REJECT", (msgs[0]["subject"] + msgs[0]["body"]).upper())
        self.assertTrue(any(r["log_path"] and r["log_path"] in msgs[0]["body"] for r in failed), msgs[0]["body"])
        self.assertEqual(self.env.sql("SELECT * FROM messages WHERE task_id=?", (self.ids[1],)), [])  # the good PR gets none
        self.assertEqual(self.judge_verdicts(2, "reject"), [])
        self.assertEqual(self.status(2), "in_review")

    def test_first_pr_bad_merges_nothing(self):
        a, b = self.pr(1, {"bad.txt": "bad\n"}), self.pr(2)
        p = self.train(a, b)
        self.assert_no_merge_happened(p)
        self.assertEqual(len(self.gate_executions()), 2, self.gate_executions())  # T_2 fails, T_1 fails, no third run
        self.assertEqual([r["status"] for r in self.train_runs()], ["fail", "fail"])
        msgs = self.env.sql("SELECT * FROM messages WHERE task_id=? AND recipient IN ('zz-test-lead','lead:g-001-a')", (self.ids[1],))
        self.assertEqual(len(msgs), 1, p.stdout + p.stderr)

    # -- 2: main moved
    def test_main_moved_mid_train_stops_and_the_rest_go_serial(self):
        a, b, c = self.pr(1), self.pr(2), self.pr(3)
        self.prepare()
        self.edit_state(inject_after={"1": {"name": "foreign.txt", "text": "another PR landed\n"}})
        p = self.train(a, b, c)
        merged = self.merged_numbers()
        self.assertEqual(merged[:1], [1], p.stdout + p.stderr)  # PR 1 merged: its tree check ran
        self.assertNotIn(3, merged, p.stdout + p.stderr)  # the mismatch after the first merge stops the train
        self.assertEqual(self.status(1), "done")
        self.assertEqual(self.status(3), "in_review")
        self.assertEqual(self.judge_verdicts(3, "merge"), [])
        self.assertIn("serial", (p.stdout + p.stderr).lower())  # the rest are handed back to the serial judge
        self.assertIn("foreign.txt", git(self.remote, "ls-tree", "--name-only", "refs/heads/main"))
        self.assertEqual(len(self.gate_executions()), 1)  # no second gate run for the rest

    # -- cache
    def test_cache_hit_on_rerun_skips_the_gate(self):
        a, b = self.pr(1), self.pr(2)
        self.prepare()
        self.edit_state(merge_fail=True)  # run 1: the gate passes, then the merge fails: main and the PRs are unchanged
        self.train(a, b)
        self.assertEqual(len(self.gate_executions()), 1)
        self.assertEqual(self.gh_state()["merges"], [])
        self.edit_state(merge_fail=False)
        p = self.train(a, b)  # run 2: same project, same tip tree, same main_sha
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.merged_numbers(), [1, 2], p.stdout + p.stderr)
        self.assertEqual(len(self.gate_executions()), 1, "the second train re-ran the gate command instead of using the cache")
        runs = self.train_runs()
        self.assertEqual([r["status"] for r in runs], ["pass", "pass"], [dict(r) for r in runs])
        self.assertEqual(runs[0]["tree"], runs[1]["tree"])
        self.assertEqual(runs[0]["main_sha"], runs[1]["main_sha"])
        self.assertIn("cached", (runs[1]["note"] or ""))

    # -- 4: ERROR / timeout / holds
    def test_gate_timeout_is_error_and_merges_nothing(self):
        a, b = self.pr(1), self.pr(2)
        self.set_gate("sleep 30", timeout=1)
        p = self.train(a, b)
        runs = self.train_runs()
        self.assertEqual([r["status"] for r in runs], ["error"], p.stdout + p.stderr)
        self.assertIn("timed out", runs[0]["note"])
        self.assert_no_merge_happened(p)
        self.assertIn("serial", (p.stdout + p.stderr).lower())  # the train is released, the PRs fall back to serial
        self.assertEqual(len(self.train_runs()), 1)  # an error is not bisected
        self.assert_train_ref_not_pushed()

    def test_gate_error_no_capacity_merges_nothing(self):
        a, b = self.pr(1), self.pr(2)
        self.prepare()
        self.env.sql("INSERT INTO gate_runs(project_id,kind,status,created,pid) VALUES('zz-test-proj','main','running','t',?)",
                     (os.getpid(),))  # gate_max_parallel=1 is taken by a live run
        p = self.train(a, b)
        self.assertEqual(self.gate_executions(), [], p.stdout + p.stderr)  # the train run waits for capacity like any gate run
        self.assertEqual([r["status"] for r in self.train_runs()], ["error"], p.stdout + p.stderr)
        self.assert_no_merge_happened(p)

    def test_main_red_hold_applies_to_the_train(self):
        a, b = self.pr(1), self.pr(2)
        self.env.sql("INSERT INTO config(key,value) VALUES('red_main_freeze','1') ON CONFLICT(key) DO UPDATE SET value='1'")
        self.env.sql("INSERT INTO config(key,value) VALUES('red_main:zz/zz',?)", (json.dumps({"run": "9", "sha": "x", "url": "u", "since": "t"}),))
        p = self.train(a, b)
        self.assert_no_merge_happened(p)
        self.assertIn("red main", p.stdout + p.stderr)
        self.assertNotEqual(p.returncode, 0)
