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

from harness import FACTORY as FACTORY_BIN, VALID_PR_BODY, FactoryTestCase, PR_URL
from harness import git as harness_git

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
    return harness_git(*args, cwd=cwd)


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
        self.assertEqual(git(self.remote, "merge-base", main, new), main)
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

    def test_a2_public_repo_refresh_uses_noreply_identity(self):
        # T-0604: on a public_text_repos repo the pre-check merge is committed as the repo's noreply user.email
        main = self.main_commit()
        self.env.sql("INSERT OR REPLACE INTO config(key,value) VALUES('public_text_repos','zz-test/repo')")
        git(self.env.home_dir, "config", "user.email", "456+volekerb@users.noreply.github.com")
        p = self.spawn("judge")
        self.assertRefreshedSpawn(p, "judge", main)
        who = "factory refresh <456+volekerb@users.noreply.github.com>"
        self.assertEqual(git(self.remote, "log", "-1", "--format=%an <%ae>|%cn <%ce>", self.remote_head()), f"{who}|{who}")

    def test_a3_public_repo_no_noreply_address_pushes_nothing(self):
        self.main_commit()
        self.env.sql("INSERT OR REPLACE INTO config(key,value) VALUES('public_text_repos','zz-test/repo')")
        self.spawn("judge")
        self.assertEqual(self.pushes(), [])
        self.assertEqual(self.remote_head(), self.head)
        self.assertEqual(self.events("refreshed"), [])
        self.assertTrue(any("REFRESH-IDENT" in r["detail"] for r in self.events("precheck-skipped")))

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

    def test_g3_queued_judge_bounce_on_a_cursor_goal_still_reaches_the_lead_that_started_it(self):
        # T-1080: the bounce answers the lead's own spawn, so it stays with that lead whatever the goal's org
        self._queue("judge")
        self.env.sql("UPDATE goals SET org='cursor' WHERE id='G-001'")  # the goal moves while the spawn waits
        self.conflicting_main()
        p = self.run_cli("tick")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        msgs = self.env.sql("SELECT recipient FROM messages WHERE task_id='T-0001' AND subject LIKE 'spawn of%'")
        self.assertEqual([m["recipient"] for m in msgs], ["zz-test-lead"])

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


# ── G-057 T-1586: remote spawn on a worker machine (stub ssh + stub hc, mirror tree for the worker machine) ──
FWD_STUB = "#!/bin/sh\n[ \"$1\" = --version ] && echo 'hivemill test (forward stub)' && exit 0\nexit 2\n"
MARK = "zz-fixture-ghcred-7d1e"
_FORWARD_BIN = {}


def built_forwarder():
    """go/cmd/factory-forward built once per process, or None when no Go toolchain can build this module (a CI shard
    without setup-go has an older go that refuses go.mod). Any other build error raises."""
    if "bin" in _FORWARD_BIN:
        return _FORWARD_BIN["bin"]
    import atexit
    import shutil
    go = shutil.which("go") or os.path.expanduser("~/.local/go/bin/go")
    out = None
    if os.access(go, os.X_OK):
        tmp = tempfile.mkdtemp(prefix="factory-forward-")
        atexit.register(shutil.rmtree, tmp, True)
        out = os.path.join(tmp, "factory-forward")
        p = subprocess.run([go, "build", "-o", out, "./cmd/factory-forward"], cwd=str(Path(__file__).resolve().parent.parent / "go"),
                           capture_output=True, text=True, timeout=600,
                           env=dict(os.environ, GOTOOLCHAIN="local", GOFLAGS="-mod=readonly", GOPROXY="off"))
        if p.returncode != 0:
            if "requires go" not in p.stderr and "toolchain" not in p.stderr:
                raise RuntimeError(f"go build ./cmd/factory-forward failed: {p.stderr.strip()}")
            out = None
    _FORWARD_BIN["bin"] = out
    return out


class RemoteSpawnTests(FactoryTestCase):
    """A coder placed on worker machine workerhost (machine_hq hqhost). ssh runs remote commands in <tmp>/remote-workerhost,
    a mirror of the test tree; hc records `run --on workerhost`. Never real ssh or hc."""

    def setUp(self):
        super().setUp()
        e = self.env
        self.origin = e.home / "origin.git"
        harness_git("init", "-q", "--bare", "-b", "main", str(self.origin), cwd=e.home)
        self.repo = e.home / "repo"
        harness_git("clone", "-q", str(self.origin), str(self.repo), cwd=e.home)
        (self.repo / "a.txt").write_text("a\n")
        git(self.repo, "add", "a.txt")
        git(self.repo, "commit", "-q", "-m", "a")
        git(self.repo, "push", "-q", "origin", "HEAD:main")
        self.root = e.home / "remote-workerhost"  # workerhost's filesystem: <root>/<same absolute path>
        rclone = Path(str(self.root) + str(self.repo))
        rclone.parent.mkdir(parents=True)
        harness_git("clone", "-q", str(self.origin), str(rclone), cwd=e.home)
        fwd = e.stub_bin / "factory-forward"
        fwd.write_text(FWD_STUB)
        fwd.chmod(0o755)
        self.fdir = Path(str(self.root) + str(e.user_home / ".local" / "lib" / "hivemill" / "forward"))
        self.fdir.mkdir(parents=True)
        (self.fdir / "factory").symlink_to(fwd)
        e.seed_project("zz-proj")
        e.sql("UPDATE projects SET repo_path=?, base_branch='main' WHERE id='zz-proj'", (str(self.repo),))
        e.seed_goal(project="zz-proj")
        e.seed_team(lead="zz-lead")
        e.seed_agent("zz-lead", "lead", goal="G-001", team="g-001-a")
        e.seed_task(status="todo", pr_url=None, team="g-001-a")
        self.hc_state = e.home / "hc.json"
        self.hc_state.write_text(json.dumps({"machines": ["workerhost"]}))
        self.ok("config", "terminal_backend", "hc", agent="owner")
        self.ok("config", "machine_hq", "hqhost", agent="owner")
        self.ok("config", "worker_machines", "workerhost", agent="owner")
        self.hosts = {"workerhost": {"mem_mb": 11000, "load1": 0.1, "nproc": 16, "root": str(self.root)},
                      "hqhost": {"root": ""}}

    def xenv(self, **extra):
        return {"FACTORY_MEM_MB": "8000", "FACTORY_NPROC": "8", "FAKE_HC_STATE": str(self.hc_state),
                "FACTORY_SSH_FACTS": json.dumps(self.hosts), "FAKE_SSH_PREFIX": str(self.env.home), **extra}

    def spawn(self, name="zz-c1", role="coder", **extra):
        return self.env.run("spawn", role, "--goal", "G-001", "--task", "T-0001", "--name", name, extra_env=self.xenv(**extra))

    def row(self, name):
        rows = self.env.sql("SELECT status, cwd, spawn_args FROM agents WHERE name=?", (name,))
        return rows[0] if rows else None

    def hc_runs(self):
        return [c["argv"] for c in self.env.calls("hc") if c["argv"][:1] == ["run"]]

    def remote(self, path):
        return Path(str(self.root) + str(path))

    def test_remote_spawn_records_machine_and_starts_hc_on_workerhost(self):
        p = self.spawn()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("SPAWNED zz-c1", p.stdout)
        r = self.row("zz-c1")
        sa = json.loads(r["spawn_args"])
        self.assertEqual((r["status"], sa["machine"]), ("live", "workerhost"))
        self.assertTrue(sa["hc_address"].startswith("workerhost/t"), sa)
        runs = self.hc_runs()
        self.assertEqual(len(runs), 1, runs)
        self.assertEqual(runs[0][runs[0].index("--on") + 1], "workerhost")
        wt = self.env.home_dir / "worktrees" / "G-001-T-0001"
        self.assertEqual(r["cwd"], str(wt))
        self.assertEqual(runs[0][runs[0].index("--cwd") + 1], str(wt))
        self.assertFalse(wt.exists())  # the worktree is on workerhost only
        rwt = self.remote(wt)
        self.assertEqual(git(rwt, "rev-parse", "--abbrev-ref", "HEAD"), "factory/t-0001")
        script = Path(runs[0][-1])
        self.assertEqual(self.remote(script).read_text(), script.read_text())  # the copy hc runs is this exact file
        self.assertIn(str(self.env.user_home / ".local" / "lib" / "hivemill" / "forward") + ":", script.read_text())
        self.assertTrue(self.remote(self.env.home_dir / "run" / "zz-c1.prompt.md").is_file())
        sends = [c["argv"] for c in self.env.calls("hc") if c["argv"][:1] == ["send"]]
        self.assertEqual([s[1] for s in sends], [sa["hc_address"]])  # the kickoff goes to workerhost/tN, not the bare name
        self.assertEqual(self.env.sql("SELECT worktree FROM tasks WHERE id='T-0001'")[0]["worktree"], str(wt))

    def test_remote_unreachable_machine_falls_back_with_one_alert(self):
        self.hosts["workerhost"] = {"exit": 255}
        for name in ("zz-u1", "zz-u2"):
            p = self.spawn(name)
            self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
            self.assertIn(f"SPAWNED {name}", p.stdout)
            self.assertNotIn("machine", json.loads(self.row(name)["spawn_args"]))
        self.assertTrue(all("--on" not in a for a in self.hc_runs()))
        msgs = self.env.sql("SELECT recipient, kind, subject FROM messages WHERE subject LIKE '%workerhost unreachable%'")
        self.assertEqual(len(msgs), 1, msgs)  # the second spawn within 30 min adds none
        self.assertEqual((msgs[0]["kind"], msgs[0]["subject"]), ("alert", "workerhost unreachable: workers fall back to hqhost"))
        self.assertTrue(msgs[0]["recipient"].endswith("director"), msgs[0]["recipient"])
        self.assertEqual(len(self.env.sql("SELECT 1 FROM events WHERE action='machine-unreachable'")), 1)

    def test_remote_hc_machine_missing_falls_back(self):
        self.hc_state.write_text(json.dumps({"machines": []}))
        p = self.spawn()
        self.assertIn("SPAWNED zz-c1", p.stdout)
        self.assertNotIn("machine", json.loads(self.row("zz-c1")["spawn_args"]))
        self.assertTrue(all("--on" not in a for a in self.hc_runs()))
        body = self.env.sql("SELECT body FROM messages WHERE subject LIKE 'workerhost unreachable%'")[0]["body"]
        self.assertIn("hc machine ls does not list workerhost", body)

    def test_remote_preflight_failure_after_worktree_starts_nothing_on_machine(self):
        self.hosts["workerhost"]["fail_on"] = ["chmod 700"]  # the spawn-script copy fails, after the remote worktree
        p = self.spawn()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("SPAWNED zz-c1", p.stdout)
        self.assertTrue(self.remote(self.env.home_dir / "worktrees" / "G-001-T-0001").is_dir())  # the worktree step ran
        self.assertNotIn("machine", json.loads(self.row("zz-c1")["spawn_args"]))
        self.assertTrue(all("--on" not in a for a in self.hc_runs()))  # no terminal on workerhost
        self.assertEqual(len(self.hc_runs()), 1)  # started once, on hqhost
        live = [r for r in self.env.sql("SELECT spawn_args FROM agents WHERE status='live'")
                if json.loads(r["spawn_args"] or "{}").get("machine")]
        self.assertEqual(live, [])
        self.assertEqual(len(self.env.sql("SELECT 1 FROM events WHERE action='machine-unreachable'")), 1)

    def test_remote_hq_roles_never_start_on_machine(self):
        self.env.sql("UPDATE tasks SET status='in_review', pr_url=? WHERE id='T-0001'", (PR_URL,))
        env = self.xenv(FACTORY_MEM_MB="2000")  # hqhost below the claude floor: workerhost is the only machine with room
        for role in ("boss", "director", "lead", "judge", "pm"):
            args = {"lead": ["--team", "g-001-a"], "judge": ["--task", "T-0001"], "pm": ["--goal", "G-001"]}.get(role, [])
            self.env.run("spawn", role, *args, "--name", f"zz-hq-{role}", extra_env=env)
            sa = json.loads((self.row(f"zz-hq-{role}") or {"spawn_args": "{}"})["spawn_args"] or "{}")
            self.assertNotIn("machine", sa, role)
        self.assertEqual([a for a in self.hc_runs() if "--on" in a], [])
        self.assertEqual(self.env.calls("ssh") and [c for c in self.env.calls("ssh") if "factory --version" in " ".join(c["argv"])], [])
        p = self.ok("spawn", "--help")
        self.assertEqual(p.stdout.count("--machine"), 0)
        with self.env.load_factory() as mod:
            os.environ.update(env)
            with self.assertRaises(SystemExit) as cm:  # a caller handing spawn a worker machine for a judge: refused
                mod._spawn_start(mod.db(), "judge", "G-001", None, "T-0001", "zz-hq-x", None, None, None, None, None, None,
                                 {"org": "claude"}, {"machine": "workerhost", "live": 0})
            self.assertEqual(cm.exception.code, 3)

    def test_remote_off_by_default_no_ssh_and_same_hc_calls_as_origin_main(self):
        self.env.sql("DELETE FROM config WHERE key='worker_machines'")
        p = self.spawn("zz-off")
        self.assertIn("SPAWNED zz-off", p.stdout)
        self.assertEqual(self.env.calls("ssh"), [])
        self.assertNotIn("machine", json.loads(self.row("zz-off")["spawn_args"]))
        ours = [c["argv"] for c in self.env.calls("hc")]
        origin = Path(__file__).resolve().parent.parent / "bin" / "_factory_cli_origin_main_t1586.py"
        self.addCleanup(origin.unlink, missing_ok=True)
        body = subprocess.check_output(["git", "show", "origin/main:bin/factory_cli.py"], cwd=Path(__file__).resolve().parent.parent)
        origin.write_bytes(body + b"\nmain()\n")
        self.env.sql("DELETE FROM agents WHERE name='zz-off'")
        self.env.sql("UPDATE tasks SET worktree=NULL, branch=NULL WHERE id='T-0001'")
        git(self.repo, "worktree", "remove", "--force", str(self.env.home_dir / "worktrees" / "G-001-T-0001"))
        git(self.repo, "branch", "-D", "factory/t-0001")
        self.env.calls_file.write_text("")
        import sys as _sys
        q = subprocess.run([_sys.executable, str(origin), "spawn", "coder", "--goal", "G-001", "--task", "T-0001", "--name", "zz-off"],
                           capture_output=True, text=True, env=self.env.env(extra=self.xenv()), cwd=self.env.home, timeout=60)
        self.assertEqual(q.returncode, 0, q.stderr)
        self.assertEqual([c["argv"] for c in self.env.calls("hc")], ours)
        self.assertEqual(self.env.calls("ssh"), [])

    def test_remote_secret_token_copied_0600_and_never_logged(self):
        e = self.env
        tdir = e.user_home / ".config" / "factory"
        tdir.mkdir(parents=True)
        tok = tdir / "gh-worker-rw-token"
        tok.write_text(MARK + "\n")
        tok.chmod(0o600)
        self.ok("config", "worker_gh_repos", "zz-owner/zz-repo")
        self.ok("config", "worker_gh_roles", "coder")
        self.ok("config", "gh_rw_roles", "coder")
        p = self.spawn()
        self.assertIn("SPAWNED zz-c1", p.stdout, p.stderr)
        self.assertEqual(json.loads(self.row("zz-c1")["spawn_args"])["machine"], "workerhost")
        rtok = self.remote(tok)
        self.assertEqual(rtok.read_text(), MARK + "\n")
        self.assertEqual(stat.S_IMODE(rtok.stat().st_mode), 0o600)
        self.assertTrue(self.remote(e.user_home / ".local" / "state" / "factory" / "ghcred" / "zz-c1" / "gitconfig").is_file())
        self.assertNotIn(MARK, p.stdout + p.stderr)
        for table in ("events", "messages"):
            for r in e.sql(f"SELECT * FROM {table}"):
                self.assertNotIn(MARK, json.dumps(dict(r)), table)
        hits = subprocess.run(["grep", "-rlF", MARK, str(e.home_dir), str(e.calls_file)], capture_output=True, text=True)
        self.assertEqual(hits.stdout.strip(), "")  # not in run/, logs, exports or the call log (the ssh argv)

    def test_remote_legacy_row_without_machine_relaunch_and_reap_unchanged(self):
        """A row with no spawn_args.machine is on machine_hq: the tick lists it by its bare name and relaunch starts it
        there, with worker_machines set. A remote row stays live while hc lists workerhost/<name> and is lost when not."""
        e = self.env
        e.seed_agent("zz-legacy", "reviewer", goal="G-001", pane_id="zz-legacy")
        e.sql("UPDATE agents SET spawn_args=?, session_id='sess-zz-legacy' WHERE name='zz-legacy'",
              (json.dumps({"host_backend": "hc"}),))
        e.seed_agent("zz-remote", "coder", goal="G-001", pane_id="zz-remote")
        e.sql("UPDATE agents SET spawn_args=? WHERE name='zz-remote'",
              (json.dumps({"host_backend": "hc", "machine": "workerhost", "hc_address": "workerhost/t9"}),))
        st = json.loads(self.hc_state.read_text())
        st["agents"] = [{"name": "zz-legacy", "agent": "claude", "agent_status": "working"},
                        {"name": "zz-remote", "agent": "claude", "agent_status": "working", "address": "workerhost/t9"}]
        self.hc_state.write_text(json.dumps(st))
        self.ok("tick", agent="owner", extra_env=self.xenv())
        status = {r["name"]: r["status"] for r in e.sql("SELECT name, status FROM agents")}
        self.assertEqual((status["zz-legacy"], status["zz-remote"]), ("live", "live"))
        st["agents"] = st["agents"][:1]
        self.hc_state.write_text(json.dumps(st))
        self.ok("tick", agent="owner", extra_env=self.xenv())
        status = {r["name"]: r["status"] for r in e.sql("SELECT name, status FROM agents")}
        self.assertEqual((status["zz-legacy"], status["zz-remote"]), ("live", "lost"))
        e.calls_file.write_text("")
        p = e.run("relaunch", "zz-legacy", "--fresh", "--force", agent="owner", extra_env=self.xenv(FACTORY_MEM_MB="20000", FACTORY_NPROC="64"))
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertNotIn("machine", json.loads(self.row("zz-legacy")["spawn_args"]))
        self.assertEqual([a for a in self.hc_runs() if "--on" in a], [])

    def test_remote_worker_factory_roundtrip(self):
        """SC1: a coder placed on workerhost, then its `factory` calls go through the forwarder (the built binary in client
        mode over the ssh stub when Go can build it, else the server's exact env) and land in this DB."""
        e = self.env
        p = self.spawn("zz-rc")
        self.assertIn("SPAWNED zz-rc", p.stdout, p.stderr)
        e.seed_agent("zz-rr", "reviewer", goal="G-001", task="T-0001")
        e.sql("UPDATE agents SET spawn_args=? WHERE name='zz-rr'", (json.dumps({"machine": "workerhost"}),))
        for twin, role in (("zz-lc", "coder"), ("zz-lr", "reviewer")):  # the same calls run locally, for the exit codes
            e.seed_agent(twin, role, goal="G-001", task="T-0001")
        e.sql("UPDATE tasks SET status='in_review', pr_url=? WHERE id='T-0001'", (PR_URL,))
        e.seed_pr(body=VALID_PR_BODY)
        # the forwarder server passes only its base env to the CLI: the stubs it reaches get their state paths baked in
        import sys as _sys
        for tool, state in (("gh", e.gh_state), ("hc", self.hc_state), ("herdr", e.herdr_state)):
            src = e.stub_bin / f"{tool}.real"
            (e.stub_bin / tool).rename(src)
            var = {"gh": "FAKE_GH_STATE", "hc": "FAKE_HC_STATE", "herdr": "FAKE_HERDR_STATE"}[tool]
            (e.stub_bin / tool).write_text(f'#!/bin/sh\n{var}={state} FAKE_HERDR_STATE={e.herdr_state} '
                                           f'FAKE_CALLS={e.calls_file} exec {_sys.executable} {src} "$@"\n')
            (e.stub_bin / tool).chmod(0o755)
        fbin = built_forwarder()
        cfg = e.home / "forward.json"
        cfg.write_text(json.dumps({"hq_host": "hqhost", "remote_receiver": fbin or "/x", "factory_bin": str(FACTORY_BIN),
                                   "path_map": []}))

        def forwarded(agent, role, *argv, stdin=None):
            if fbin:  # worker side: the forwarder client; ssh (stub) runs `<receiver> --serve` for hqhost
                env = {**e.env(), **self.xenv(), "FORWARD_CONFIG": str(cfg), "FACTORY_AGENT": agent, "FACTORY_ROLE": role}
                return subprocess.run([fbin, *argv], input=stdin, capture_output=True, text=True, env=env, cwd=e.home, timeout=120)
            base = {k: v for k, v in e.env().items() if k in ("PATH", "HOME", "LANG", "LC_ALL", "TZ", "FACTORY_HOME")}
            env = {**base, "FACTORY_AGENT": agent, "FACTORY_ROLE": role, "FACTORY_FORWARDED": "1"}
            return subprocess.run([str(FACTORY_BIN), *argv], input=stdin, capture_output=True, text=True, env=env,
                                  cwd=e.home, timeout=120)

        def local(agent, role, *argv, stdin=None):
            return e.run(*argv, agent=agent, stdin=stdin, extra_env={"FACTORY_ROLE": role, **self.xenv()})

        calls = [("zz-rc", "zz-lc", "coder", ("inbox",), None),
                 ("zz-rc", "zz-lc", "coder", ("msg", "lead:g-001-a", "zz-fwd-subject", "zz-fwd-body"), None),
                 ("zz-rr", "zz-lr", "reviewer", ("verdict", "T-0001", "--fail", "--cause", "bug", "--summary", "zz-fwd-verdict"), None),
                 ("zz-rc", "zz-lc", "coder", ("msg",), None),
                 ("zz-rc", "zz-lc", "coder", ("done", "-"), "zz-fwd-done line one\nline two\n")]
        for remote_agent, twin, role, argv, stdin in calls:
            f = forwarded(remote_agent, role, *argv, stdin=stdin)
            loc = local(twin, role, *argv, stdin=stdin)
            self.assertEqual(f.returncode, loc.returncode, f"{argv}: forwarded {f.stderr} / local {loc.stderr}")
            self.assertEqual(f.returncode, 2 if argv == ("msg",) else 0, f"{argv}: {f.stdout}{f.stderr}")
        self.assertEqual(len(e.sql("SELECT 1 FROM messages WHERE sender='zz-rc' AND subject='zz-fwd-subject'")), 1)
        self.assertEqual(len(e.sql("SELECT 1 FROM verdicts WHERE author='zz-rr' AND summary LIKE 'zz-fwd-verdict%'")), 1)
        if fbin:  # the calls really went client -> ssh -> `factory-forward --serve` -> bin/factory
            self.assertEqual(len([c for c in e.calls("ssh") if "hqhost" in c["argv"] and "--serve" in c["argv"]]), len(calls))
        done = e.sql("SELECT status, summary FROM agents WHERE name='zz-rc'")[0]
        self.assertEqual(done["status"], "done")
        self.assertIn("line two", done["summary"])


if __name__ == "__main__":
    unittest.main()
