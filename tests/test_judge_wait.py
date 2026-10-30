"""`factory judge --merge`: waits for pending CI, retries a "Base branch was modified" merge once, and refuses a head
that does not contain origin/main. Everything runs through the CLI as a subprocess against a temp FACTORY_HOME, with a
gh stub written here (tests/stubs/gh is not used) and a real bare git remote for the stale-base check."""
import json
import os
import stat
import textwrap
import unittest

from harness import PR_URL, FactoryTestCase, _check_remotes, gates_summary
from harness import git as harness_git

GH_STUB = textwrap.dedent('''\
    #!/usr/bin/env python3
    """gh stub for test_judge_wait. State file $JW_STATE: {"views": [pr json, ...], "n": 0, "merges": [{"exit", "stderr"}],
    "m": 0, "repo_url": ..., "main_repo": ...}. The n-th `pr view` answers views[min(n, last)], the m-th `pr merge` answers
    merges[min(m, last)]. Every call is appended to $JW_CALLS."""
    import json, os, subprocess, sys
    argv = sys.argv[1:]
    with open(os.environ["JW_CALLS"], "a") as f:
        f.write(json.dumps(argv) + "\\n")
    with open(os.environ["JW_STATE"]) as f:
        st = json.load(f)
    def pick(key, idx):
        items = st[key]
        r = items[min(st[idx], len(items) - 1)]
        st[idx] += 1
        with open(os.environ["JW_STATE"], "w") as f:
            json.dump(st, f)
        return r
    if argv[:2] == ["pr", "view"]:
        print(json.dumps(pick("views", "n")))
    elif argv[:2] == ["pr", "merge"]:
        r = pick("merges", "m")
        if r.get("run"):
            subprocess.run(r["run"], check=True, capture_output=True)  # side effect: main moves during the merge
        if r.get("stderr"):
            print(r["stderr"], file=sys.stderr)
        sys.exit(r.get("exit", 0))
    elif argv[:2] == ["repo", "view"]:
        print(json.dumps({"url": st["repo_url"]}))
    elif argv[:1] == ["api"] and argv[1].endswith("/commits/main"):
        print(subprocess.run(["git", "-C", st["main_repo"], "rev-parse", "refs/heads/main"],
                             capture_output=True, text=True, check=True).stdout.strip())
    else:
        print("gh: unsupported by the stub: " + " ".join(argv), file=sys.stderr)
        sys.exit(1)
''')

BASE_MODIFIED = "GraphQL: Base branch was modified. Review and try the merge again."


def check(state, conclusion=None):
    c = {"name": "unittest", "status": "COMPLETED" if conclusion else state, "workflowName": "ci"}
    if conclusion:
        c["conclusion"] = conclusion
    return [c]


PENDING = check("IN_PROGRESS")
GREEN = check("COMPLETED", "SUCCESS")
FAILED = check("COMPLETED", "FAILURE")


def git(cwd, *args):
    # identity via -c, not repo config: the judge refuses repo config outside the allow-list (docs/git-hardening.md)
    return harness_git("-c", "user.email=zz-test@example.invalid", "-c", "user.name=zz-test", *args, cwd=cwd)


class JudgeWaitTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_project()
        e.seed_goal(project="zz-test-proj")
        e.seed_task()
        for name, role in (("zz-test-reviewer", "reviewer"), ("zz-test-qa", "qa"), ("zz-test-judge", "judge")):
            e.seed_agent(name, role, goal="G-001", task="T-0001")
        self.gh_dir = e.home / "ghstub"
        self.gh_dir.mkdir()
        gh = self.gh_dir / "gh"
        gh.write_text(GH_STUB)
        gh.chmod(gh.stat().st_mode | stat.S_IXUSR)
        self.jw_state, self.jw_calls = e.home / "jw-state.json", e.home / "jw-calls.jsonl"
        self.jw_calls.touch()
        self.remote, self.wt = e.home / "origin.git", e.home / "pr-wt"
        git(e.home, "init", "-q", "--bare", "-b", "main", str(self.remote))
        git(e.home, "clone", "-q", str(self.remote), str(self.wt))
        self.commit("base.txt", "base\n", "base")
        git(self.wt, "push", "-q", "origin", "HEAD:main")
        git(self.wt, "checkout", "-q", "-b", "feature")
        self.commit("feature.txt", "f\n", "feature work")
        e.sql("UPDATE tasks SET worktree=? WHERE id='T-0001'", (str(self.wt),))
        self.x = self.publish()
        self.stub(views=[self.pr(self.x, GREEN)])
        for agent, summary in (("zz-test-reviewer", "ok"), ("zz-test-qa", gates_summary())):
            p = self.cli("verdict", "T-0001", "--pass", "--summary", summary, agent=agent)
            self.assertEqual(p.returncode, 0, p.stdout + p.stderr)

    # -- helpers ---------------------------------------------------------
    def commit(self, name, text, msg):
        (self.wt / name).write_text(text)
        git(self.wt, "add", name)
        git(self.wt, "commit", "-q", "-m", msg)

    def publish(self):
        git(self.wt, "push", "-q", "-f", "origin", "HEAD:refs/pull/1/head")
        return git(self.wt, "rev-parse", "HEAD")

    def pr(self, sha, checks, state="OPEN", mergeable="MERGEABLE"):
        return {"number": 1, "state": state, "headRefOid": sha, "headRefName": "factory/t-0001", "baseRefName": "main",
                "mergeable": mergeable, "isDraft": False, "url": PR_URL, "statusCheckRollup": checks}

    def stub(self, views, merges=None, repo_url=None):
        self.jw_state.write_text(json.dumps({
            "views": views, "n": 0, "merges": merges or [{"exit": 0}], "m": 0,
            "repo_url": repo_url or str(self.remote), "main_repo": str(self.remote)}))
        self.jw_calls.write_text("")

    def cli(self, *args, agent, poll="0.05"):
        env = {"PATH": f"{self.gh_dir}{os.pathsep}{self.env.env()['PATH']}", "JW_STATE": str(self.jw_state),
               "JW_CALLS": str(self.jw_calls), "FACTORY_JUDGE_POLL_S": poll}
        return self.env.run(*args, agent=agent, extra_env=env)

    def judge(self, poll="0.05"):
        return self.cli("judge", "T-0001", "--merge", "--reason", "zz-test", agent="zz-test-judge", poll=poll)

    def gh_calls(self, sub):
        rows = [json.loads(l) for l in self.jw_calls.read_text().splitlines() if l.strip()]
        return [r for r in rows if r[:2] == sub]

    def events(self, action):
        return [r["detail"] for r in self.env.sql("SELECT detail FROM events WHERE action=?", (action,))]

    def status(self):
        return self.env.sql("SELECT status FROM tasks WHERE id='T-0001'")[0]["status"]

    def assert_merged(self, p, merge_calls=1):
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("MERGED", p.stdout)
        self.assertEqual(self.status(), "done")
        self.assertEqual(self.events("judge-blocked"), [])
        self.assertEqual(self.events("merge-failed"), [])
        self.assertEqual(self.events("merged"), [f"@{self.x[:8]}"])
        calls = self.gh_calls(["pr", "merge"])
        self.assertEqual(len(calls), merge_calls)
        for c in calls:
            self.assertEqual(c[c.index("--match-head-commit") + 1], self.x)

    def assert_blocked(self, p, text, merge_calls=0):
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn(text, p.stderr)
        blocked = self.events("judge-blocked")
        self.assertEqual(len(blocked), 1)
        self.assertIn(text, blocked[0])
        self.assertEqual(len(self.gh_calls(["pr", "merge"])), merge_calls)
        self.assertNotEqual(self.status(), "done")

    # -- CI wait (a)-(e) -------------------------------------------------
    def test_a_pending_then_green_merges(self):
        self.stub([self.pr(self.x, PENDING), self.pr(self.x, PENDING), self.pr(self.x, GREEN)])
        p = self.judge()
        self.assert_merged(p)
        self.assertEqual(p.stdout.count("waiting for CI"), 2)

    def test_b_pending_then_failure_refused(self):
        self.stub([self.pr(self.x, PENDING), self.pr(self.x, FAILED)])
        p = self.judge()
        self.assert_blocked(p, "checks not green: unittest=FAILURE")
        self.assertEqual(p.stdout.count("waiting for CI"), 1)
        self.assertNotIn("waited", p.stderr)

    def test_c_pending_past_the_wait_refused(self):
        self.ok("config", "judge_ci_wait_s", "1")
        self.stub([self.pr(self.x, PENDING)])
        p = self.judge(poll="0.3")
        self.assert_blocked(p, "checks not green: unittest=IN_PROGRESS (waited ")
        self.assertIn("waiting for CI", p.stdout)

    def test_d_failing_at_once_does_not_wait(self):
        self.stub([self.pr(self.x, FAILED)])
        p = self.judge()
        self.assert_blocked(p, "checks not green: unittest=FAILURE")
        self.assertNotIn("waiting for CI", p.stdout)
        self.assertNotIn("waited", p.stderr)
        self.assertEqual(len(self.gh_calls(["pr", "view"])), 1)

    def test_d2_failing_next_to_pending_does_not_wait(self):
        self.stub([self.pr(self.x, FAILED + [{"name": "lint", "status": "IN_PROGRESS"}])])
        p = self.judge()
        self.assert_blocked(p, "unittest=FAILURE")
        self.assertNotIn("waiting for CI", p.stdout)

    def test_e_head_changes_while_waiting_stale_verdict(self):
        self.commit("more.txt", "real code\n", "code after the verdicts")
        y = self.publish()
        self.stub([self.pr(self.x, PENDING), self.pr(y, GREEN)])
        p = self.judge()
        self.assert_blocked(p, f"reviewer PASS is for {self.x[:8]}, head is now {y[:8]}")
        self.assertIn(f"qa PASS is for {self.x[:8]}", p.stderr)

    # -- merge race (f)-(h) ----------------------------------------------
    def test_f_base_modified_once_then_success(self):
        self.stub([self.pr(self.x, GREEN)], merges=[{"exit": 1, "stderr": BASE_MODIFIED}, {"exit": 0}])
        self.assert_merged(self.judge(), merge_calls=2)

    def test_g_base_modified_twice_records_merge_failed(self):
        self.stub([self.pr(self.x, GREEN)], merges=[{"exit": 1, "stderr": BASE_MODIFIED}] * 3)
        p = self.judge()
        self.assertEqual(p.returncode, 5, p.stdout + p.stderr)
        self.assertEqual(len(self.gh_calls(["pr", "merge"])), 2)
        self.assertEqual(len(self.events("merge-failed")), 1)
        self.assertIn("Base branch was modified", self.events("merge-failed")[0])
        self.assertEqual(self.events("merged"), [])
        self.assertNotEqual(self.status(), "done")

    def test_base_modified_while_main_moved_refused_stale_only(self):
        other = self.env.home / "other"
        git(self.env.home, "clone", "-q", str(self.remote), str(other))
        (other / "main.txt").write_text("m\n")
        git(other, "add", "main.txt")
        git(other, "commit", "-q", "-m", "main moves")
        _check_remotes(["push", "origin", "HEAD:main"], other)  # the stub runs this argv later; vet its remote now
        push = ["git", "-C", str(other), "push", "-q", "origin", "HEAD:main"]
        self.stub([self.pr(self.x, GREEN)], merges=[{"exit": 1, "stderr": BASE_MODIFIED, "run": push}, {"exit": 0}])
        p = self.judge()
        self.assert_blocked(p, "STALE-ONLY: head does not contain origin/main", merge_calls=1)
        self.assertEqual(self.events("merge-failed"), [])
        self.assertEqual(self.events("merged"), [])

    def test_h_other_merge_error_not_retried(self):
        self.stub([self.pr(self.x, GREEN)], merges=[{"exit": 1, "stderr": "boom"}, {"exit": 0}])
        p = self.judge()
        self.assertEqual(p.returncode, 5, p.stdout + p.stderr)
        self.assertEqual(len(self.gh_calls(["pr", "merge"])), 1)
        self.assertEqual(self.events("merge-failed"), ["boom"])
        self.assertEqual(self.events("merged"), [])

    def test_base_modified_but_head_changed_not_retried(self):
        self.stub([self.pr(self.x, GREEN), self.pr("c" * 40, GREEN)],
                  merges=[{"exit": 1, "stderr": BASE_MODIFIED}, {"exit": 0}])
        p = self.judge()
        self.assertEqual(p.returncode, 5, p.stdout + p.stderr)
        self.assertEqual(len(self.gh_calls(["pr", "merge"])), 1)
        self.assertEqual(len(self.events("merge-failed")), 1)

    def test_base_modified_but_now_conflicting_not_retried(self):
        self.stub([self.pr(self.x, GREEN), self.pr(self.x, GREEN, mergeable="CONFLICTING")],
                  merges=[{"exit": 1, "stderr": BASE_MODIFIED}, {"exit": 0}])
        self.assert_blocked(self.judge(), "PR has conflicts", merge_calls=1)  # the full gate re-runs before any retry

    # -- stale base (i)-(iii) --------------------------------------------
    def move_main(self):
        other = self.env.home / "other"
        git(self.env.home, "clone", "-q", str(self.remote), str(other))
        (other / "main.txt").write_text("m\n")
        git(other, "add", "main.txt")
        git(other, "commit", "-q", "-m", "main moves")
        git(other, "push", "-q", "origin", "HEAD:main")

    def test_i_stale_head_refused_despite_pass_green_mergeable(self):
        self.move_main()
        self.stub([self.pr(self.x, GREEN)])
        p = self.judge()
        self.assert_blocked(p, "STALE-ONLY: head does not contain origin/main")
        self.assertTrue(self.events("judge-blocked")[0].startswith("STALE-ONLY: head does not contain origin/main"))

    def test_ii_head_containing_main_merges(self):
        self.move_main()
        git(self.wt, "fetch", "-q", "origin", "main")
        git(self.wt, "merge", "-q", "--no-edit", "origin/main")
        y = self.publish()
        self.stub([self.pr(y, GREEN)])
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("carried", p.stdout)
        self.assertEqual(len(self.gh_calls(["pr", "merge"])), 1)

    def test_iii_fetch_failure_refused(self):
        self.stub([self.pr(self.x, GREEN)], repo_url=str(self.env.home / "gone.git"))
        p = self.judge()
        self.assert_blocked(p, "cannot verify head contains origin/main")


if __name__ == "__main__":
    unittest.main()
