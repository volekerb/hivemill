"""T-0172: staleness pre-check at reviewer/qa/judge spawn. A clean-behind head is refreshed with refresh_branch (one
push) and the spawn goes on; a conflicting head or a refresh in flight is refused (exit 2, spawn-bounced, no agent row);
a fetch/git/gh error fails open (precheck-skipped). Real CLI as a subprocess, a temp bare origin, a gh stub written here."""
import fcntl
import json
import os
import stat
import subprocess
import tempfile
import unittest
from pathlib import Path

from harness import FactoryTestCase, PR_URL

BRANCH = "factory/t-0001"

GH_STUB = r'''#!/usr/bin/env python3
import json, os, subprocess, sys
argv = sys.argv[1:]
path = os.environ["ZZ_GH_STATE"]
st = json.load(open(path))
open(os.path.join(os.path.dirname(path), "gh-calls.log"), "a").write(" ".join(argv) + "\n")


def git(*a):
    return subprocess.run(["git", *a], capture_output=True, text=True).stdout.strip()


repo, br = st["repo_url"], "refs/heads/" + st["branch"]
if st.get("gh_fail"):
    print("gh down", file=sys.stderr)
    sys.exit(1)
if argv[:2] == ["pr", "view"]:
    real = git("-C", st["origin"], "rev-parse", br)
    git("-C", st["origin"], "update-ref", "refs/pull/1/head", real)
    print(json.dumps({"number": 1, "state": "OPEN", "headRefName": st["branch"], "baseRefName": "main",
                      "headRefOid": real, "mergeable": st.get("mergeable", "MERGEABLE"), "isDraft": False, "statusCheckRollup": [],
                      "url": st["url"]}))
elif argv[:1] == ["api"] and argv[1].endswith("/branches/main/protection"):
    if st.get("strict") is None:
        print("gh: Branch not protected (HTTP 404)", file=sys.stderr)
        sys.exit(1)
    print("true" if st["strict"] else "false")
elif argv[:2] == ["repo", "view"]:
    print(json.dumps({"url": repo}))
elif argv[:1] == ["api"] and argv[1].endswith("/commits/main"):
    if st.get("race"):  # the PR branch moves between the verified fetch and the push: the lease is lost
        o = st["origin"]
        head = git("-C", o, "rev-parse", br)
        tree = git("-C", o, "rev-parse", head + "^{tree}")
        env = {**os.environ, "GIT_AUTHOR_NAME": "x", "GIT_AUTHOR_EMAIL": "x@x", "GIT_COMMITTER_NAME": "x", "GIT_COMMITTER_EMAIL": "x@x"}
        new = subprocess.run(["git", "-C", o, "commit-tree", tree, "-p", head, "-m", "coder pushes"],
                             capture_output=True, text=True, env=env).stdout.strip()
        git("-C", o, "update-ref", br, new)
        st["race"] = False
        json.dump(st, open(path, "w"))
    print(git("-C", st["origin"], "rev-parse", "refs/heads/main"))
else:
    print("gh: unsupported by the test stub: " + " ".join(argv), file=sys.stderr)
    sys.exit(1)
'''

HOOK = r'''#!/bin/sh
while read old new ref; do
  case "$ref" in refs/heads/factory/*) echo "$new" >> "%(zz)s/pushes.log";; esac
done
'''


def git(cwd, *args):
    p = subprocess.run(["git", *args], cwd=cwd, capture_output=True, text=True)
    if p.returncode != 0:
        raise AssertionError(f"git {args}: {p.stderr}")
    return p.stdout.strip()


class SpawnPrecheckTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_project()
        e.seed_goal(project="zz-test-proj")
        e.seed_team("g-001-a", lead="zz-test-lead")
        e.seed_task(team="g-001-a")
        e.seed_agent("zz-test-lead", "lead", goal="G-001", team="g-001-a")
        e.seed_agent("zz-test-coder", "coder", goal="G-001", team="g-001-a", task="T-0001")
        self.zz = Path(tempfile.mkdtemp(prefix="zz-test-sp-", dir=e.home))
        (self.zz / "bin").mkdir()
        gh = self.zz / "bin" / "gh"
        gh.write_text(GH_STUB)
        gh.chmod(gh.stat().st_mode | stat.S_IXUSR)
        self.state = self.zz / "gh.json"
        self.remote = e.home / "origin.git"
        self.wt = e.home / "pr-wt"
        git(e.home, "init", "-q", "--bare", "-b", "main", str(self.remote))
        hook = self.remote / "hooks" / "post-receive"
        hook.write_text(HOOK % {"zz": self.zz})
        hook.chmod(0o755)
        git(e.home, "clone", "-q", str(self.remote), str(self.wt))
        self.commit(self.wt, "base.txt", "line1\nline2\nline3\n", "base")
        git(self.wt, "push", "-q", "origin", "HEAD:main")
        git(self.wt, "checkout", "-q", "-b", BRANCH)
        self.commit(self.wt, "feature.txt", "f1\n", "feature work")
        git(self.wt, "push", "-q", "origin", f"HEAD:refs/heads/{BRANCH}")
        self.head = git(self.wt, "rev-parse", "HEAD")
        (self.zz / "pushes.log").write_text("")
        e.sql("UPDATE tasks SET worktree=?, branch=? WHERE id='T-0001'", (str(self.wt), BRANCH))
        self.write_state()

    # -- helpers ---------------------------------------------------------
    def commit(self, cwd, name, text, msg):
        (Path(cwd) / name).write_text(text)
        git(cwd, "add", name)
        git(cwd, "commit", "-q", "-m", msg)

    def write_state(self, repo_url=None, **extra):
        self.state.write_text(json.dumps({"repo_url": repo_url or str(self.remote), "origin": str(self.remote),
                                          "branch": BRANCH, "url": PR_URL, **extra}))

    def main_commit(self, name="main.txt", text="m\n"):
        other = self.env.home / "other"
        if not other.exists():
            git(self.env.home, "clone", "-q", str(self.remote), str(other))
        git(other, "pull", "-q", "origin", "main")
        self.commit(other, name, text, "main moves")
        git(other, "push", "-q", "origin", "HEAD:main")
        return git(other, "rev-parse", "HEAD")

    def conflicting_main(self):
        """The PR branch cannot merge with main: both change base.txt line 2."""
        git(self.wt, "checkout", "-q", BRANCH)
        self.commit(self.wt, "base.txt", "line1\nPR\nline3\n", "pr edits base")
        git(self.wt, "push", "-q", "origin", f"HEAD:refs/heads/{BRANCH}")
        self.head = git(self.wt, "rev-parse", "HEAD")
        (self.zz / "pushes.log").write_text("")
        return self.main_commit("base.txt", "line1\nMAIN\nline3\n")

    def run_cli(self, *args, agent="zz-test-lead"):
        path = f"{self.zz / 'bin'}{os.pathsep}{self.env.env()['PATH']}"
        return self.env.run(*args, agent=agent, extra_env={"PATH": path, "ZZ_GH_STATE": str(self.state)})

    def spawn(self, role, agent="zz-test-lead"):
        return self.run_cli("spawn", role, "--task", "T-0001", "--name", f"zz-test-{role}-x", agent=agent)

    def pushes(self):
        return (self.zz / "pushes.log").read_text().split()

    def events(self, action):
        return self.env.sql("SELECT detail, actor FROM events WHERE entity_id='T-0001' AND action=?", (action,))

    def agent(self, role):
        return self.env.sql("SELECT status FROM agents WHERE name=?", (f"zz-test-{role}-x",))

    def gh_calls(self):
        f = self.zz / "gh-calls.log"
        return f.read_text().splitlines() if f.exists() else []

    def remote_head(self):
        return git(self.remote, "rev-parse", f"refs/heads/{BRANCH}")

    def assertRefreshedSpawn(self, p, role, main):
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        new = self.remote_head()
        self.assertEqual(self.pushes(), [new])
        self.assertEqual(git(self.remote, "rev-list", "--parents", "-n", "1", new).split(), [new, self.head, main])
        self.assertEqual(subprocess.run(["git", "merge-base", "--is-ancestor", main, new], cwd=self.remote).returncode, 0)
        ev = self.events("refreshed")
        self.assertEqual(len(ev), 1)
        self.assertEqual(json.loads(ev[0]["detail"]),
                         {"from": self.head[:8], "to": new[:8], "main": main[:8], "how": "by spawn pre-check"})
        self.assertIn(f"refreshed {self.head[:8]} -> {new[:8]} by spawn pre-check ({role})",
                      [r["detail"] for r in self.events("note")])
        self.assertIn("by spawn pre-check", p.stderr)
        self.assertNotIn("refreshed", p.stdout)
        self.assertEqual(self.events("spawn-bounced"), [])
        self.assertEqual(len(self.agent(role)), 1)

    def assertBounced(self, p, role, prefix, detail=""):
        self.assertEqual(p.returncode, 2, p.stdout + p.stderr)
        self.assertIn(prefix, p.stderr)
        self.assertIn(detail, p.stderr)
        self.assertEqual(self.pushes(), [])
        ev = self.events("spawn-bounced")
        self.assertEqual(len(ev), 1)
        self.assertTrue(ev[0]["detail"].startswith(f"{role}: {prefix}"), ev[0]["detail"])
        self.assertEqual(self.agent(role), [])

    # -- cases -----------------------------------------------------------
    def test_a_judge_clean_behind_is_refreshed_then_queued_until_ci(self):
        # T-0391: the refreshed head has no green CI yet, so the judge waits in the queue instead of taking a pane
        main = self.main_commit()
        p = self.spawn("judge")
        self.assertRefreshedSpawn(p, "judge", main)
        self.assertIn("QUEUED", p.stdout)
        self.assertEqual(self.agent("judge")[0]["status"], "queued")
        starts = [c for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "start"]]
        self.assertEqual(starts, [])

    def test_g_behind_ok_clean_behind_is_not_refreshed(self):
        # G-022: strict=false on GitHub + stale_policy=behind_ok -> reviewer, qa and judge spawn on a clean BEHIND head
        self.main_commit()
        self.write_state(strict=False)
        for role in ("reviewer", "qa", "judge"):
            p = self.spawn(role)
            self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.pushes(), [])
        self.assertEqual(self.remote_head(), self.head)
        self.assertEqual(self.events("refreshed"), [])
        self.assertEqual(self.events("spawn-bounced"), [])

    def test_g2_behind_ok_dirty_is_still_refreshed(self):
        main = self.main_commit()
        self.write_state(strict=False, mergeable="UNKNOWN")
        self.assertRefreshedSpawn(self.spawn("judge"), "judge", main)

    def test_g3_strict_true_unreadable_or_policy_strict_still_refresh(self):
        main = self.main_commit()
        self.write_state(strict=True)  # strict=true on GitHub
        self.assertRefreshedSpawn(self.spawn("judge"), "judge", main)

    def test_g4_protection_unreadable_refreshes(self):
        main = self.main_commit()
        self.write_state()  # the stub answers 404 for the protection read
        self.assertRefreshedSpawn(self.spawn("judge"), "judge", main)

    def test_g5_policy_strict_refreshes_even_if_github_strict_false(self):
        main = self.main_commit()
        self.write_state(strict=False)
        self.env.sql("INSERT OR REPLACE INTO config(key,value) VALUES('stale_policy','strict')")
        self.assertRefreshedSpawn(self.spawn("judge"), "judge", main)

    def test_b_head_contains_main_no_push(self):
        git(self.wt, "fetch", "-q", "origin", "main")
        git(self.wt, "merge", "-q", "--no-edit", "origin/main")  # already contained: nothing to do
        p = self.spawn("judge")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.pushes(), [])
        self.assertEqual(self.events("refreshed"), [])
        self.assertEqual(self.events("spawn-bounced"), [])
        self.assertEqual(self.events("precheck-skipped"), [])
        self.assertEqual(len(self.agent("judge")), 1)

    def assertSpawnedOnCurrentHead(self, p, role):
        # T-0488: reviewer/qa on a clean-behind PR: no merge commit, no push, head unchanged, no refresh events
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.remote_head(), self.head)
        self.assertEqual(self.pushes(), [])
        self.assertEqual(self.events("refreshed"), [])
        self.assertEqual(self.events("spawn-bounced"), [])
        self.assertEqual(self.events("precheck-skipped"), [])
        self.assertEqual(len(self.agent(role)), 1)

    def test_c_qa_clean_behind_spawns_on_current_head(self):
        self.main_commit()
        self.assertSpawnedOnCurrentHead(self.spawn("qa"), "qa")

    def test_c2_reviewer_clean_behind_spawns_on_current_head(self):
        self.main_commit()
        self.assertSpawnedOnCurrentHead(self.spawn("reviewer"), "reviewer")

    def test_d_reviewer_conflict_refused_names_file(self):
        self.conflicting_main()
        p = self.spawn("reviewer")
        self.assertBounced(p, "reviewer", "CONFLICT-ONLY:", "base.txt")

    def test_d2_judge_conflict_refused_nothing_pushed(self):
        self.conflicting_main()
        p = self.spawn("judge")
        self.assertBounced(p, "judge", "CONFLICT-ONLY:", "base.txt")
        self.assertEqual(self.remote_head(), self.head)

    def test_e_fetch_fails_open(self):
        self.main_commit()
        self.write_state(repo_url=str(self.env.home / "no-such-origin.git"))
        p = self.spawn("judge")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("staleness pre-check skipped", p.stderr)
        self.assertEqual(len(self.events("precheck-skipped")), 1)
        self.assertEqual(self.events("spawn-bounced"), [])
        self.assertEqual(self.pushes(), [])
        self.assertEqual(len(self.agent("judge")), 1)

    def test_e2_gh_failure_fails_open_with_one_stderr_line(self):
        self.main_commit()
        self.write_state(gh_fail=True)
        p = self.spawn("judge")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        lines = [l for l in p.stderr.splitlines() if "gh down" in l]
        self.assertEqual(len(lines), 1, p.stderr)
        self.assertTrue(lines[0].startswith("staleness pre-check skipped: gh failed: "), lines[0])
        self.assertIn("gh down", lines[0])
        self.assertEqual(len(self.events("precheck-skipped")), 1)
        self.assertEqual(len(self.agent("judge")), 1)

    def test_f_coder_and_pm_are_not_checked(self):
        self.main_commit()
        self.write_state(repo_url=str(self.env.home / "no-such-origin.git"))
        git_stub = self.zz / "bin" / "git"  # a git call from the pre-check path would be logged here and fail
        git_stub.write_text(f'#!/bin/sh\necho "$@" >> "{self.zz}/git-calls.log"\nexit 1\n')
        git_stub.chmod(0o755)
        for role in ("coder", "pm"):
            p = self.spawn(role)
            self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
            self.assertEqual(len(self.agent(role)), 1)
        self.assertEqual(self.gh_calls(), [])
        self.assertFalse((self.zz / "git-calls.log").exists())
        self.assertEqual(self.events("precheck-skipped"), [])
        self.assertEqual(self.events("spawn-bounced"), [])
        self.assertEqual(self.pushes(), [])

    def _queue(self, role):
        self.env.set_herdr(fail=True)  # gate closed: the spawn is queued
        p = self.spawn(role)
        self.assertIn("QUEUED", p.stdout, p.stdout + p.stderr)
        self.env.set_herdr(fail=False)
        self.run_cli("config", "max_live_agents", "1")

    def test_g_queued_judge_conflict_at_start_is_bounced(self):
        self._queue("judge")
        self.conflicting_main()
        p = self.run_cli("tick")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.env.sql("SELECT status FROM agents WHERE name='zz-test-judge-x'")[0]["status"], "cancelled")
        self.assertEqual(self.pushes(), [])
        ev = self.events("spawn-bounced")
        self.assertEqual(len(ev), 1)
        self.assertIn("CONFLICT-ONLY:", ev[0]["detail"])
        msgs = self.env.sql("SELECT recipient, kind, body FROM messages WHERE task_id='T-0001' AND subject LIKE 'spawn of%'")
        self.assertEqual([(m["recipient"], m["kind"]) for m in msgs], [("zz-test-lead", "report")])
        self.assertIn("CONFLICT-ONLY:", msgs[0]["body"])
        starts = [c for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "start"]]
        self.assertEqual(starts, [])

    def test_g2_queued_judge_clean_behind_is_refreshed_then_stays_queued_until_ci(self):
        self._queue("judge")
        main = self.main_commit()
        p = self.run_cli("tick")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        new = self.remote_head()
        self.assertEqual(self.pushes(), [new])
        self.assertEqual(git(self.remote, "rev-list", "--parents", "-n", "1", new).split(), [new, self.head, main])
        self.assertEqual(len(self.events("refreshed")), 1)
        self.assertEqual(self.events("spawn-bounced"), [])
        self.assertEqual(self.env.sql("SELECT status FROM agents WHERE name='zz-test-judge-x'")[0]["status"], "queued")

    def test_h_lock_held_is_refused_in_flight(self):
        self.main_commit()
        lock = open(self.env.home_dir / "db" / "refresh-pr-1.lock", "w")
        self.addCleanup(lock.close)
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        p = self.spawn("judge")
        self.assertBounced(p, "judge", "REFRESH-IN-FLIGHT: PR #1")
        self.assertEqual(self.remote_head(), self.head)

    def test_i_lost_lease_fails_open_nothing_overwritten(self):
        self.main_commit()
        self.write_state(race=True)
        p = self.spawn("judge")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("staleness pre-check skipped", p.stderr)
        self.assertEqual(len(self.events("precheck-skipped")), 1)
        self.assertEqual(self.events("refreshed"), [])
        self.assertEqual(self.pushes(), [])
        self.assertNotEqual(self.remote_head(), self.head)  # still the coder's moved commit
        self.assertEqual(git(self.remote, "rev-list", "--parents", "-n", "1", self.remote_head()).split()[1:], [self.head])
        self.assertEqual(len(self.agent("judge")), 1)

    def test_j_worker_role_cannot_reach_the_push_path(self):
        self.main_commit()
        p = self.spawn("qa", agent="zz-test-coder")
        self.assertNotEqual(p.returncode, 0)
        self.assertEqual(self.pushes(), [])
        self.assertEqual(self.agent("qa"), [])
        self.assertEqual(self.events("refreshed"), [])


if __name__ == "__main__":
    unittest.main()
