import fcntl
import json
import os
import stat
import subprocess
import tempfile
import unittest
from pathlib import Path

from harness import FactoryTestCase, PR_URL, gates_summary

GREEN = [{"name": "unittest", "status": "COMPLETED", "conclusion": "SUCCESS"}]
BRANCH = "factory/t-0001"

# Fake `gh`, written by this test into its own temp dir (tests/stubs/gh is not touched). State: $ZZ_GH_STATE.
# `pr view` reports what GitHub would: the head is the remote branch (and refs/pull/1/head follows it), the checks
# depend on the head (original = green; a refreshed head can lag, have no checks yet, be pending, or fail).
GH_STUB = r'''#!/usr/bin/env python3
import json, os, subprocess, sys
argv = sys.argv[1:]
path = os.environ["ZZ_GH_STATE"]
st = json.load(open(path))
GREEN = [{"name": "unittest", "status": "COMPLETED", "conclusion": "SUCCESS"}]


def git(*a):
    return subprocess.run(["git", *a], capture_output=True, text=True).stdout.strip()


def save():
    json.dump(st, open(path, "w"))


def other_refresh():
    """Another writer moves the PR branch (no hook: it is not a push of this run): a pure main merge, or a coder commit."""
    repo, br = st["repo_url"], "refs/heads/" + st["branch"]
    head, main = git("-C", repo, "rev-parse", br), git("-C", repo, "rev-parse", "refs/heads/main")
    parents = ["-p", head]
    if st.get("race_kind") == "extra":
        tree = git("-C", repo, "rev-parse", head + "^{tree}")
    else:
        tree = git("-C", repo, "merge-tree", "--write-tree", head, main).splitlines()[0]
        parents += ["-p", main]
    env = {**os.environ, "GIT_AUTHOR_NAME": "x", "GIT_AUTHOR_EMAIL": "x@x", "GIT_COMMITTER_NAME": "x", "GIT_COMMITTER_EMAIL": "x@x"}
    new = subprocess.run(["git", "-C", repo, "commit-tree", tree, *parents, "-m", "other writer"],
                         capture_output=True, text=True, env=env).stdout.strip()
    git("-C", repo, "update-ref", br, new)


if argv[:2] == ["pr", "view"]:
    st["views"] = st.get("views", 0) + 1
    if st.get("advance_on_view") == st["views"]:
        other_refresh()
    real = git("-C", st["repo_url"], "rev-parse", "refs/heads/" + st["branch"])
    git("-C", st["repo_url"], "update-ref", "refs/pull/1/head", real)
    head, rollup = real, GREEN
    if real != st["orig_head"]:
        if st.get("lag", 0) > 0:
            st["lag"] -= 1
            head = st["orig_head"]
        elif st.get("empty_new", 0) > 0:
            st["empty_new"] -= 1
            rollup = []
        elif st.get("pending_new", 0) > 0:
            st["pending_new"] -= 1
            rollup = [{"name": "unittest", "status": "IN_PROGRESS", "conclusion": ""}]
        elif st.get("ci_new") == "fail":
            rollup = [{"name": "unittest", "status": "COMPLETED", "conclusion": "FAILURE"}]
    st["last_view"] = {"head": head, "rollup": rollup}
    save()
    print(json.dumps({**st["pr"], "headRefOid": head, "statusCheckRollup": rollup}))
elif argv[:2] == ["pr", "merge"]:
    st.setdefault("merges", []).append({"argv": argv, "last_view": st["last_view"]})
    st["pr"]["state"] = "MERGED"
    save()
elif argv[:2] == ["repo", "view"]:
    print(json.dumps({"url": st["repo_url"]}))
elif argv[:1] == ["api"] and argv[1].endswith("/commits/main"):
    st["api_calls"] = st.get("api_calls", 0) + 1
    save()
    if st.get("race_on_api") == st["api_calls"]:  # the branch moves between the verified fetch and the push
        other_refresh()
    if st.get("tamper"):  # someone rewrites the shared private ref after the fetch, before the caller could re-read it
        git("-C", st["wt"], "update-ref", "refs/factory-judge/main", st["tamper"])
    print(st.get("api_main") or git("-C", st["repo_url"], "rev-parse", "refs/heads/main"))
else:
    print("gh: unsupported by the test stub: " + " ".join(argv), file=sys.stderr)
    sys.exit(1)
'''

# post-receive hook of the bare origin: logs every push to the PR branch and, while $ZZ/moves > 0, moves main once more
HOOK = r'''#!/bin/sh
while read old new ref; do
  case "$ref" in
    refs/heads/factory/*)
      echo "$new" >> "%(zz)s/pushes.log"
      n=$(cat "%(zz)s/moves" 2>/dev/null || echo 0)
      if [ "$n" -gt 0 ]; then
        main=$(git rev-parse refs/heads/main)
        c=$(git commit-tree "$main^{tree}" -p "$main" -m "main moves again")
        git update-ref refs/heads/main "$c"
        echo $((n - 1)) > "%(zz)s/moves"
      fi
      ;;
  esac
done
'''


def git(cwd, *args, check=True):
    p = subprocess.run(["git", *args], cwd=cwd, capture_output=True, text=True)
    if check and p.returncode != 0:
        raise AssertionError(f"git {args}: {p.stderr}")
    return p.stdout.strip()


class JudgeRefreshTests(FactoryTestCase):
    """`factory judge --merge` refreshes a STALE-ONLY clean head itself, waits for CI, re-runs the full gate, merges."""

    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_project()
        e.seed_goal(project="zz-test-proj")
        e.seed_task()
        for name, role in (("zz-test-lead", "lead"), ("zz-test-coder", "coder"), ("zz-test-qa", "qa"),
                           ("zz-test-reviewer", "reviewer"), ("zz-test-judge", "judge")):
            e.seed_agent(name, role, goal="G-001", task="T-0001")
        self.zz = Path(tempfile.mkdtemp(prefix="zz-test-jr-", dir=e.home))
        gh = self.zz / "bin" / "gh"
        gh.parent.mkdir()
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
        self.base = git(self.wt, "rev-parse", "HEAD")
        git(self.wt, "checkout", "-q", "-b", BRANCH)
        self.commit(self.wt, "feature.txt", "f1\n", "feature work")
        git(self.wt, "push", "-q", "origin", f"HEAD:refs/heads/{BRANCH}", "HEAD:refs/pull/1/head")
        self.head = git(self.wt, "rev-parse", "HEAD")
        (self.zz / "pushes.log").write_text("")
        e.sql("UPDATE tasks SET worktree=?, branch=? WHERE id='T-0001'", (str(self.wt), BRANCH))
        self.write_state()
        self.ok_verdicts()

    # -- helpers ---------------------------------------------------------
    def commit(self, cwd, name, text, msg):
        (Path(cwd) / name).write_text(text)
        git(cwd, "add", name)
        git(cwd, "commit", "-q", "-m", msg)

    def write_state(self, **extra):
        pr = {"number": 1, "state": "OPEN", "headRefName": BRANCH, "baseRefName": "main", "mergeable": "MERGEABLE",
              "isDraft": False, "url": PR_URL}
        self.state.write_text(json.dumps({"pr": pr, "repo_url": str(self.remote), "branch": BRANCH, "wt": str(self.wt),
                                          "orig_head": self.head, **extra}))

    def gh_state(self):
        return json.loads(self.state.read_text())

    def ok_verdicts(self):
        for summary, who in (("ok", "zz-test-reviewer"), (gates_summary(), "zz-test-qa")):
            p = self.run_cli("verdict", "T-0001", "--pass", "--summary", summary, agent=who)
            self.assertEqual(p.returncode, 0, p.stdout + p.stderr)

    def run_cli(self, *args, agent="zz-test-lead", poll="0"):
        path = f"{self.zz / 'bin'}{os.pathsep}{self.env.env()['PATH']}"
        return self.env.run(*args, agent=agent, extra_env={"PATH": path, "ZZ_GH_STATE": str(self.state),
                                                           "FACTORY_JUDGE_POLL_S": poll})

    def judge(self):
        return self.run_cli("judge", "T-0001", "--merge", "--reason", "zz-test", agent="zz-test-judge")

    def main_commit(self, name="main.txt", text="m\n"):
        other = self.env.home / "other"
        if not other.exists():
            git(self.env.home, "clone", "-q", str(self.remote), str(other))
        git(other, "pull", "-q", "origin", "main")
        self.commit(other, name, text, "main moves")
        git(other, "push", "-q", "origin", "HEAD:main")
        return git(other, "rev-parse", "HEAD")

    def more_moves(self, n):
        (self.zz / "moves").write_text(str(n))

    def remote_ref(self, ref):
        return git(self.remote, "rev-parse", ref)

    def pushes(self):
        return (self.zz / "pushes.log").read_text().split()

    def merges(self):
        return self.gh_state().get("merges", [])

    def events(self, action):
        return self.env.sql("SELECT detail, actor FROM events WHERE entity_id='T-0001' AND action=?", (action,))

    def assertNoMerge(self):
        self.assertEqual(self.merges(), [])
        self.assertEqual(self.events("merged"), [])
        self.assertNotEqual(self.env.sql("SELECT status FROM tasks WHERE id='T-0001'")[0]["status"], "done")

    def assertMergedPinned(self, head):
        """Exactly one `gh pr merge`, pinned to `head`, which is the remote branch tip, contains origin/main and had
        green CI as last served by GitHub."""
        m = self.merges()
        self.assertEqual(len(m), 1)
        argv = m[0]["argv"]
        self.assertEqual(argv[argv.index("--match-head-commit") + 1], head)
        self.assertEqual(self.remote_ref(f"refs/heads/{BRANCH}"), head)
        self.assertEqual(subprocess.run(["git", "merge-base", "--is-ancestor", self.remote_ref("refs/heads/main"), head],
                                        cwd=self.remote).returncode, 0, "merged head does not contain origin/main")
        self.assertEqual(m[0]["last_view"]["head"], head)
        self.assertEqual([c["conclusion"] for c in m[0]["last_view"]["rollup"]], ["SUCCESS"])
        self.assertEqual(len(self.events("merged")), 1)

    # -- cases -----------------------------------------------------------
    def test_a_stale_clean_refreshed_waits_ci_carries_and_merges(self):
        main = self.main_commit()
        # GitHub lags one view, then shows the new head with no checks, then pending, then green
        self.write_state(lag=1, empty_new=1, pending_new=1)
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("MERGED", p.stdout)
        self.assertIn("waiting for CI", p.stdout)
        new = self.remote_ref(f"refs/heads/{BRANCH}")
        self.assertEqual(git(self.remote, "rev-list", "--parents", "-n", "1", new).split(), [new, self.head, main])
        self.assertEqual(self.pushes(), [new])
        ev = self.events("refreshed")
        self.assertEqual(len(ev), 1)
        self.assertEqual(ev[0]["actor"], "zz-test-judge")
        self.assertEqual(json.loads(ev[0]["detail"]),
                         {"from": self.head[:8], "to": new[:8], "main": main[:8], "how": "by judge"})
        self.assertIn(f"refreshed {self.head[:8]} -> {new[:8]} by judge zz-test-judge",
                      [r["detail"] for r in self.events("note")])
        self.assertMergedPinned(new)
        carried = [json.loads(r["detail"]) for r in self.events("verdict-carried")]
        self.assertEqual(sorted(c["role"] for c in carried), ["qa", "reviewer"])
        for c in carried:
            self.assertEqual((c["from"], c["to"], c["main"]), (self.head, new, main))
        self.assertEqual(self.env.sql("SELECT status FROM tasks WHERE id='T-0001'")[0]["status"], "done")

    def test_a3_public_repo_refresh_uses_noreply_identity(self):
        # T-0604: the judge's refresh merge on a public_text_repos repo is committed as config noreply_email
        self.main_commit()
        self.env.sql("INSERT OR REPLACE INTO config(key,value) VALUES('public_text_repos','zz-test/repo')")
        self.env.sql("INSERT OR REPLACE INTO config(key,value) VALUES('noreply_email','123+zz-test@users.noreply.github.com')")
        self.judge()  # the merge itself then meets the public text check; only the refresh is under test here
        new = self.remote_ref(f"refs/heads/{BRANCH}")
        self.assertEqual(self.pushes(), [new])
        who = "factory refresh <123+zz-test@users.noreply.github.com>"
        self.assertEqual(git(self.remote, "log", "-1", "--format=%an <%ae>|%cn <%ce>", new), f"{who}|{who}")

    def test_a4_public_repo_no_noreply_address_refuses_nothing_pushed(self):
        self.main_commit()
        self.env.sql("INSERT OR REPLACE INTO config(key,value) VALUES('public_text_repos','zz-test/repo')")
        p = self.judge()
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn("REFRESH-IDENT", p.stderr)
        self.assertEqual(self.pushes(), [])
        self.assertEqual(self.remote_ref(f"refs/heads/{BRANCH}"), self.head)
        self.assertTrue(any("REFRESH-IDENT" in r["detail"] for r in self.events("judge-blocked")))
        self.assertNoMerge()

    def test_a2_fresh_head_merges_without_refresh(self):
        git(self.wt, "fetch", "-q", "origin", "main")
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.pushes(), [])
        self.assertEqual(self.events("refreshed"), [])
        self.assertMergedPinned(self.head)

    def test_b_main_moves_once_more_second_refresh_merges(self):
        main1 = self.main_commit()
        self.more_moves(1)
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        new = self.remote_ref(f"refs/heads/{BRANCH}")
        main2 = self.remote_ref("refs/heads/main")
        self.assertNotEqual(main1, main2)
        self.assertEqual(len(self.pushes()), 2)
        self.assertEqual(len(self.events("refreshed")), 2)
        self.assertEqual(git(self.remote, "rev-parse", f"{new}^2"), main2)
        self.assertMergedPinned(new)

    def test_c_main_moves_three_times_stale_loop(self):
        self.env.run("config", "judge_max_refreshes", "2")
        self.main_commit()
        self.more_moves(5)
        p = self.judge()
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn("STALE-LOOP: main moved 2 times during refresh", p.stderr)
        blocked = [r["detail"] for r in self.events("judge-blocked")]
        self.assertEqual(len(blocked), 1)
        self.assertTrue(blocked[0].startswith("STALE-LOOP: main moved 2 times during refresh; "), blocked[0])
        self.assertEqual(len(self.events("refreshed")), 2)
        self.assertEqual(len(self.pushes()), 2)
        self.assertNoMerge()

    def assertStaleLoopAfter(self, n):
        p = self.judge()
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn(f"STALE-LOOP: main moved {n} times during refresh", p.stderr)
        self.assertEqual(len(self.events("refreshed")), n)
        self.assertEqual(len(self.pushes()), n)
        self.assertNoMerge()

    def test_c2_unset_limit_is_four(self):
        self.main_commit()
        self.more_moves(9)
        self.assertStaleLoopAfter(4)

    def test_c3_config_limit_one(self):
        self.env.run("config", "judge_max_refreshes", "1")
        self.main_commit()
        self.more_moves(9)
        self.assertStaleLoopAfter(1)

    def test_c4_invalid_limit_falls_back_to_four(self):
        for bad in ("x", "0", "-2"):
            with self.subTest(value=bad):
                self.tearDown()
                self.setUp()
                self.env.run("config", "judge_max_refreshes", bad)
                self.main_commit()
                self.more_moves(9)
                self.assertStaleLoopAfter(4)

    def test_d_conflict_only_nothing_pushed(self):
        self.main_commit("feature.txt", "from main\n")
        p = self.judge()
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        blocked = [r["detail"] for r in self.events("judge-blocked")]
        self.assertEqual(len(blocked), 1)
        self.assertTrue(blocked[0].startswith("CONFLICT-ONLY: feature.txt"), blocked[0])
        self.assertEqual(self.pushes(), [])
        self.assertEqual(self.remote_ref(f"refs/heads/{BRANCH}"), self.head)
        self.assertEqual(self.events("refreshed"), [])
        self.assertNoMerge()

    def test_d2_github_conflicting_flag_still_reports_conflict_only(self):
        self.main_commit("feature.txt", "from main\n")
        self.write_state()
        st = self.gh_state()
        st["pr"]["mergeable"] = "CONFLICTING"
        self.state.write_text(json.dumps(st))
        p = self.judge()
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertTrue([r["detail"] for r in self.events("judge-blocked")][0].startswith("CONFLICT-ONLY: feature.txt"))
        self.assertEqual(self.pushes(), [])

    def test_e_other_problem_plus_stale_refuses_as_today(self):
        self.main_commit()
        p = self.run_cli("verdict", "T-0001", "--fail", "--summary", "broken", agent="zz-test-qa")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        p = self.judge()
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn("STALE-ONLY", p.stderr)
        self.assertIn("no qa PASS", p.stderr)
        self.assertEqual(self.pushes(), [])
        self.assertEqual(self.events("refreshed"), [])
        self.assertNoMerge()

    def test_e2_extra_non_pure_commit_is_refused_not_refreshed(self):
        # a coder commit after the PASS verdicts: the carry rule must refuse, and the judge must not refresh around it
        self.main_commit()
        self.commit(self.wt, "extra.txt", "sneaky\n", "late commit")
        git(self.wt, "push", "-q", "origin", f"HEAD:refs/heads/{BRANCH}")
        (self.zz / "pushes.log").write_text("")  # the setup push is not a refresh
        p = self.judge()
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn("not a pure main merge", p.stderr)
        self.assertEqual(self.pushes(), [])
        self.assertEqual(self.events("refreshed"), [])
        self.assertNoMerge()

    def test_f_fetched_main_differs_from_github_cannot_verify(self):
        self.main_commit()
        self.write_state(api_main="f" * 40)
        p = self.judge()
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn("cannot verify", p.stderr)
        self.assertEqual(self.pushes(), [])
        self.assertEqual(self.events("refreshed"), [])
        self.assertNoMerge()

    def test_g_ci_fails_on_new_head_refuses(self):
        self.main_commit()
        self.write_state(ci_new="fail")
        p = self.judge()
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn("checks not green: unittest=FAILURE", p.stderr)
        self.assertEqual(len(self.pushes()), 1)
        self.assertEqual(len(self.events("refreshed")), 1)
        self.assertNoMerge()

    def test_h_base_not_main_refuses_without_push(self):
        self.main_commit()
        self.write_state()
        st = self.gh_state()
        st["pr"]["baseRefName"] = "develop"
        self.state.write_text(json.dumps(st))
        p = self.judge()
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn("PR base is develop, not main", p.stderr)
        self.assertEqual(self.pushes(), [])
        self.assertEqual(self.events("refreshed"), [])
        self.assertNoMerge()

    def test_h2_refresh_command_refuses_base_not_main(self):
        self.main_commit()
        self.write_state()
        st = self.gh_state()
        st["pr"]["baseRefName"] = "develop"
        self.state.write_text(json.dumps(st))
        p = self.run_cli("refresh", "T-0001")
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn("PR base is develop, not main", p.stderr)
        self.assertEqual(self.pushes(), [])

    def test_i_verified_main_sha_is_used_not_the_shared_ref(self):
        main = self.main_commit()
        self.write_state(tamper=self.base)  # the shared ref is rewritten to an older commit right after verification
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        new = self.remote_ref(f"refs/heads/{BRANCH}")
        self.assertEqual(git(self.remote, "rev-parse", f"{new}^2"), main)
        self.assertEqual(git(self.remote, "rev-parse", f"{new}^1"), self.head)
        self.assertMergedPinned(new)

    def test_j_refresh_command_uses_verified_main_sha(self):
        main = self.main_commit()
        self.write_state(tamper=self.base)
        p = self.run_cli("refresh", "T-0001")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        new = self.remote_ref(f"refs/heads/{BRANCH}")
        self.assertEqual(git(self.remote, "rev-parse", f"{new}^2"), main)

    # -- T-0260: per-PR refresh lock ---------------------------------------
    def hold_lock(self):
        """Play the other refresher: hold the PR's refresh lock for the rest of the test."""
        f = open(self.env.home_dir / "db" / "refresh-pr-1.lock", "w")
        fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)
        self.addCleanup(f.close)

    def hold_lock_as(self, who):
        """Play a refresher that holds the PR's lock and has written its name into the file, as refresh_lock does."""
        f = open(self.env.home_dir / "db" / "refresh-pr-1.lock", "a+")
        f.truncate(0)
        f.write(who)
        f.flush()
        fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)
        self.addCleanup(f.close)

    def test_k2_dry_run_does_not_take_the_lock(self):
        self.main_commit()
        self.hold_lock()
        p = self.run_cli("refresh", "T-0001", "--dry-run")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)  # a held lock would make it `refresh in flight`, exit 4
        self.assertIn("dry-run", p.stdout)
        self.assertEqual(self.pushes(), [])

    def test_k3_lock_file_is_not_truncated_by_a_loser_and_names_its_holder(self):
        self.main_commit()
        self.hold_lock_as("zz-test-director")
        p = self.run_cli("refresh", "T-0001")
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn("another refresh is pushing (lock held by zz-test-director); wait for it", p.stderr)
        self.assertEqual((self.env.home_dir / "db" / "refresh-pr-1.lock").read_text(), "zz-test-director")
        self.assertEqual(self.pushes(), [])

    def test_k4_a_refresher_writes_its_name_into_the_lock_file(self):
        self.main_commit()
        p = self.run_cli("refresh", "T-0001")  # as zz-test-lead
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual((self.env.home_dir / "db" / "refresh-pr-1.lock").read_text(), "zz-test-lead")

    def test_k5_judge_block_names_the_lock_holder(self):
        self.main_commit()
        self.hold_lock_as("zz-test-lead")
        self.env.run("config", "judge_ci_wait_s", "1")
        p = self.run_cli("judge", "T-0001", "--merge", "--reason", "zz-test", agent="zz-test-judge", poll="0.05")
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        want = "STALE-ONLY: refresh in flight did not land within 1s (lock held by zz-test-lead)"
        self.assertIn(want, p.stderr)
        self.assertEqual([r["detail"] for r in self.events("judge-blocked")], [want])

    def test_k_lock_held_refresh_command_exits_4_pushes_nothing(self):
        self.main_commit()
        self.hold_lock()
        p = self.run_cli("refresh", "T-0001")
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn("refresh in flight for PR #1: another refresh is pushing; wait for it", p.stderr)
        self.assertEqual(self.pushes(), [])
        self.assertEqual(self.remote_ref(f"refs/heads/{BRANCH}"), self.head)
        self.assertEqual(self.events("refreshed"), [])
        self.assertEqual(self.events("refresh-failed"), [])

    def test_l_lock_held_judge_waits_then_merges_on_the_other_refresh(self):
        main = self.main_commit()
        self.hold_lock()
        self.write_state(advance_on_view=3)  # the other refresher's merge lands on the 3rd PR read
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("refresh in flight for PR #1", p.stdout)
        self.assertEqual(self.pushes(), [])
        self.assertEqual(self.events("refreshed"), [])
        new = self.remote_ref(f"refs/heads/{BRANCH}")
        self.assertEqual(git(self.remote, "rev-parse", f"{new}^2"), main)
        self.assertMergedPinned(new)
        self.assertEqual(sorted(json.loads(r["detail"])["role"] for r in self.events("verdict-carried")), ["qa", "reviewer"])

    def test_m_lock_held_head_never_changes_judge_blocked(self):
        self.main_commit()
        self.hold_lock()
        self.env.run("config", "judge_ci_wait_s", "1")
        p = self.run_cli("judge", "T-0001", "--merge", "--reason", "zz-test", agent="zz-test-judge", poll="0.05")
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn("STALE-ONLY: refresh in flight did not land within 1s", p.stderr)
        blocked = [r["detail"] for r in self.events("judge-blocked")]
        self.assertEqual(blocked, ["STALE-ONLY: refresh in flight did not land within 1s"])
        self.assertEqual(self.pushes(), [])
        self.assertEqual(self.events("refreshed"), [])
        self.assertNoMerge()

    def test_n_lease_race_with_pure_merge_head_waits_and_merges_without_second_push(self):
        main = self.main_commit()
        self.write_state(race_on_api=2)  # api call 1 = the gate, 2 = the judge's own refresh fetch
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.pushes(), [])  # the judge's push was rejected, and it did not push again
        self.assertEqual(self.events("refreshed"), [])
        self.assertEqual(self.events("judge-blocked"), [])
        new = self.remote_ref(f"refs/heads/{BRANCH}")
        self.assertEqual(git(self.remote, "rev-list", "--parents", "-n", "1", new).split(), [new, self.head, main])
        self.assertMergedPinned(new)

    def test_n2_lease_race_with_non_merge_head_refuses_as_today(self):
        self.main_commit()
        self.write_state(race_on_api=2, race_kind="extra")
        p = self.judge()
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn("cannot verify, refresh failed: push rejected", p.stderr)
        self.assertEqual(self.pushes(), [])
        self.assertNoMerge()


if __name__ == "__main__":
    unittest.main()
