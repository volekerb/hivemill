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
GH_STUB = r'''#!/usr/bin/env python3
import json, os, subprocess, sys
argv = sys.argv[1:]
st = json.load(open(os.environ["ZZ_GH_STATE"]))
if argv[:2] == ["pr", "view"]:
    print(json.dumps(st["pr"]))
elif argv[:2] == ["pr", "merge"]:
    st["pr"]["state"] = "MERGED"
    json.dump(st, open(os.environ["ZZ_GH_STATE"], "w"))
elif argv[:2] == ["repo", "view"]:
    print(json.dumps({"url": st["repo_url"]}))
elif argv[:1] == ["api"] and argv[1].endswith("/commits/main"):
    print(st.get("api_main") or subprocess.run(["git", "-C", st["repo_url"], "rev-parse", "refs/heads/main"],
                                              capture_output=True, text=True).stdout.strip())
else:
    print("gh: unsupported by the test stub: " + " ".join(argv), file=sys.stderr)
    sys.exit(1)
'''


def git(cwd, *args, check=True):
    p = subprocess.run(["git", *args], cwd=cwd, capture_output=True, text=True)
    if check and p.returncode != 0:
        raise AssertionError(f"git {args}: {p.stderr}")
    return p.stdout.strip()


class RefreshTests(FactoryTestCase):
    """`factory refresh T`: a pure origin/main merge of a stale clean PR branch, pushed with a lease, no worktree touched."""

    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_project()
        e.seed_goal(project="zz-test-proj")
        e.seed_task()
        for name, role in (("zz-test-lead", "lead"), ("zz-test-director", "director"), ("zz-test-coder", "coder"),
                           ("zz-test-pm", "pm"), ("zz-test-qa", "qa"), ("zz-test-reviewer", "reviewer"),
                           ("zz-test-judge", "judge")):
            e.seed_agent(name, role, goal="G-001", task="T-0001")
        self.stubdir = Path(tempfile.mkdtemp(prefix="zz-test-gh-", dir=e.home))
        gh = self.stubdir / "gh"
        gh.write_text(GH_STUB)
        gh.chmod(gh.stat().st_mode | stat.S_IXUSR)
        self.state = e.home / "zz-gh.json"
        self.remote = e.home / "origin.git"
        self.wt = e.home / "pr-wt"
        git(e.home, "init", "-q", "--bare", "-b", "main", str(self.remote))
        git(e.home, "clone", "-q", str(self.remote), str(self.wt))
        self.commit(self.wt, "base.txt", "line1\nline2\nline3\n", "base")
        git(self.wt, "push", "-q", "origin", "HEAD:main")
        git(self.wt, "checkout", "-q", "-b", BRANCH)
        self.commit(self.wt, "feature.txt", "f1\n", "feature work")
        git(self.wt, "push", "-q", "origin", f"HEAD:refs/heads/{BRANCH}", "HEAD:refs/pull/1/head")
        self.head = git(self.wt, "rev-parse", "HEAD")
        e.sql("UPDATE tasks SET worktree=?, branch=? WHERE id='T-0001'", (str(self.wt), BRANCH))
        self.write_state(self.head)
        self.ok_verdicts()

    # -- helpers ---------------------------------------------------------
    def commit(self, cwd, name, text, msg):
        (Path(cwd) / name).write_text(text)
        git(cwd, "add", name)
        git(cwd, "commit", "-q", "-m", msg)

    def write_state(self, sha, **extra):
        pr = {"number": 1, "state": "OPEN", "headRefOid": sha, "headRefName": BRANCH, "baseRefName": "main",
              "mergeable": "MERGEABLE", "isDraft": False, "url": PR_URL, "statusCheckRollup": GREEN}
        self.state.write_text(json.dumps({"pr": pr, "repo_url": str(self.remote), **extra}))

    def ok_verdicts(self):
        for summary, who in (("ok", "zz-test-reviewer"), (gates_summary(), "zz-test-qa")):
            p = self.run_cli("verdict", "T-0001", "--pass", "--summary", summary, agent=who)
            self.assertEqual(p.returncode, 0, p.stdout + p.stderr)

    def run_cli(self, *args, agent="zz-test-lead"):
        path = f"{self.stubdir}{os.pathsep}{self.env.env()['PATH']}"
        return self.env.run(*args, agent=agent, extra_env={"PATH": path, "ZZ_GH_STATE": str(self.state)})

    def main_commit(self, name="main.txt", text="m\n"):
        other = self.env.home / "other"
        if not other.exists():
            git(self.env.home, "clone", "-q", str(self.remote), str(other))
        git(other, "pull", "-q", "origin", "main")
        self.commit(other, name, text, "main moves")
        git(other, "push", "-q", "origin", "HEAD:main")
        return git(other, "rev-parse", "HEAD")

    def remote_ref(self, ref):
        return git(self.remote, "rev-parse", ref)

    def events(self, action):
        return self.env.sql("SELECT detail, actor FROM events WHERE entity_id='T-0001' AND action=?", (action,))

    # -- cases -----------------------------------------------------------
    def test_a_stale_clean_pushes_pure_merge(self):
        main = self.main_commit()
        p = self.run_cli("refresh", "T-0001")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        new = self.remote_ref(f"refs/heads/{BRANCH}")
        self.assertNotEqual(new, self.head)
        self.assertEqual(git(self.remote, "rev-list", "--parents", "-n", "1", new).split(), [new, self.head, main])
        tree = git(self.remote, "merge-tree", "--write-tree", self.head, main).splitlines()[0]
        self.assertEqual(git(self.remote, "rev-parse", f"{new}^{{tree}}"), tree)
        self.assertIn(new, p.stdout)
        self.assertIn("verdicts carry over by the T-0095 rule", p.stdout)
        ev = self.events("refreshed")
        self.assertEqual(len(ev), 1)
        self.assertEqual(json.loads(ev[0]["detail"]), {"from": self.head[:8], "to": new[:8], "main": main[:8]})
        notes = [r["detail"] for r in self.events("note")]
        self.assertIn(f"refreshed {self.head[:8]} -> {new[:8]} by zz-test-lead", notes)
        self.assertEqual(git(self.remote, "log", "-1", "--format=%an <%ae>", new), "factory refresh <factory-refresh@localhost>")

    def test_b_judge_merges_with_carried_verdicts(self):
        main = self.main_commit()
        # (since T-0230 the judge refreshes a stale head itself: tests/test_judge_refresh.py; here the lead refreshes by hand)
        self.write_state(self.head)
        self.ok_refresh = self.run_cli("refresh", "T-0001")
        self.assertEqual(self.ok_refresh.returncode, 0, self.ok_refresh.stderr)
        new = self.remote_ref(f"refs/heads/{BRANCH}")
        git(self.remote, "update-ref", "refs/pull/1/head", new)  # what GitHub does on a push to the PR branch
        self.write_state(new)
        p = self.run_cli("judge", "T-0001", "--merge", "--reason", "zz-test", agent="zz-test-judge")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("MERGED", p.stdout)
        carried = [json.loads(r["detail"]) for r in self.events("verdict-carried")]
        self.assertEqual(sorted(c["role"] for c in carried), ["qa", "reviewer"])
        for c in carried:
            self.assertEqual((c["from"], c["to"], c["main"]), (self.head, new, main))
        self.assertEqual(self.env.sql("SELECT status FROM tasks WHERE id='T-0001'")[0]["status"], "done")

    def test_c_already_fresh(self):
        git(self.wt, "fetch", "-q", "origin", "main")
        before = self.remote_ref(f"refs/heads/{BRANCH}")
        p = self.run_cli("refresh", "T-0001")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("already contains origin/main", p.stdout)
        self.assertEqual(self.remote_ref(f"refs/heads/{BRANCH}"), before)
        self.assertEqual(self.events("refreshed"), [])

    def test_d_conflict(self):
        self.main_commit("feature.txt", "from main\n")
        before = self.remote_ref(f"refs/heads/{BRANCH}")
        p = self.run_cli("refresh", "T-0001")
        self.assertEqual(p.returncode, 3, p.stdout + p.stderr)
        self.assertIn("CONFLICT-ONLY: feature.txt", p.stdout)
        self.assertEqual(self.remote_ref(f"refs/heads/{BRANCH}"), before)
        self.assertEqual(len(self.events("refresh-conflict")), 1)
        self.assertEqual(self.events("refreshed"), [])

    def test_e_moved_branch_lease_rejects(self):
        self.main_commit()
        other = self.env.home / "racer"
        git(self.env.home, "clone", "-q", "-b", BRANCH, str(self.remote), str(other))
        self.commit(other, "racer.txt", "r\n", "concurrent coder push")
        git(other, "push", "-q", "origin", f"HEAD:refs/heads/{BRANCH}")
        racer = git(other, "rev-parse", "HEAD")
        p = self.run_cli("refresh", "T-0001")  # PR head (refs/pull/1/head) is still the old one: moved after the fetch
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertEqual(self.remote_ref(f"refs/heads/{BRANCH}"), racer)
        self.assertEqual(len(self.events("refresh-failed")), 1)
        self.assertEqual(self.events("refreshed"), [])

    def test_e2_rewound_branch_lease_rejects(self):
        # the racer rewinds the branch to a commit the merge commit descends from: a plain push would fast-forward
        # and silently drop the feature commit, so only the lease stops it
        self.main_commit()
        other = self.env.home / "racer"
        git(self.env.home, "clone", "-q", "-b", BRANCH, str(self.remote), str(other))
        git(other, "reset", "-q", "--hard", "HEAD~1")
        git(other, "push", "-q", "-f", "origin", f"HEAD:refs/heads/{BRANCH}")
        rewound = git(other, "rev-parse", "HEAD")
        self.assertNotEqual(rewound, self.head)
        p = self.run_cli("refresh", "T-0001")  # PR head (refs/pull/1/head) is still the old one: moved after the fetch
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertEqual(self.remote_ref(f"refs/heads/{BRANCH}"), rewound)
        self.assertEqual(len(self.events("refresh-failed")), 1)
        self.assertEqual(self.events("refreshed"), [])

    def test_f_fetched_main_differs_from_github(self):
        self.main_commit()
        before = self.remote_ref(f"refs/heads/{BRANCH}")
        self.write_state(self.head, api_main="f" * 40)
        p = self.run_cli("refresh", "T-0001")
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn("does not match GitHub's", p.stderr)
        self.assertEqual(self.remote_ref(f"refs/heads/{BRANCH}"), before)
        self.assertEqual(len(self.events("refresh-failed")), 1)

    def test_g_only_lead_and_director(self):
        self.main_commit()
        before = self.remote_ref(f"refs/heads/{BRANCH}")
        for who in ("zz-test-coder", "zz-test-pm", "zz-test-qa", "zz-test-reviewer", "zz-test-judge"):
            p = self.run_cli("refresh", "T-0001", agent=who)
            self.assertEqual(p.returncode, 2, f"{who}: {p.stdout}{p.stderr}")
            self.assertEqual(self.remote_ref(f"refs/heads/{BRANCH}"), before)
        p = self.run_cli("refresh", "T-0001", agent="zz-test-director")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)

    def test_h_dry_run_pushes_nothing(self):
        main = self.main_commit()
        before = self.remote_ref(f"refs/heads/{BRANCH}")
        p = self.run_cli("refresh", "T-0001", "--dry-run")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("dry-run", p.stdout)
        self.assertIn(main[:8], p.stdout)
        self.assertEqual(self.remote_ref(f"refs/heads/{BRANCH}"), before)
        self.assertEqual(self.events("refreshed"), [])

    def test_i_no_worktree_modified(self):
        self.main_commit()
        status = git(self.wt, "status", "--porcelain")
        head = git(self.wt, "rev-parse", "HEAD")
        live = git(self.env.home_dir, "status", "--porcelain")
        self.assertEqual(self.run_cli("refresh", "T-0001").returncode, 0)
        self.assertEqual(git(self.wt, "status", "--porcelain"), status)
        self.assertEqual(git(self.wt, "rev-parse", "HEAD"), head)
        self.assertEqual(git(self.wt, "rev-parse", "--abbrev-ref", "HEAD"), BRANCH)
        self.assertEqual(git(self.env.home_dir, "status", "--porcelain"), live)

    # -- T-0604: refresh identity on a public_text_repos repo -------------
    def public(self, noreply=None, wt_email=None):
        """PR_URL's repo (zz-test/repo) is public; optional config noreply_email and user.email of the factory checkout (main's noreply_email(con))."""
        self.env.sql("INSERT OR REPLACE INTO config(key,value) VALUES('public_text_repos','zz-test/repo')")
        if noreply:
            self.env.sql("INSERT OR REPLACE INTO config(key,value) VALUES('noreply_email',?)", (noreply,))
        if wt_email:
            git(self.env.home_dir, "config", "user.email", wt_email)

    def refreshed_ident(self):
        new = self.remote_ref(f"refs/heads/{BRANCH}")
        return git(self.remote, "log", "-1", "--format=%an <%ae>|%cn <%ce>", new)

    def guard_push(self):
        terms = self.env.home / "zz-terms.txt"
        terms.write_text("zz-owner-secret-term\n")
        root = Path(__file__).resolve().parents[1]
        return subprocess.run(["python3", str(root / "ci" / "public_text_guard.py"), "push", "--repo", str(self.remote),
                               "--terms-file", str(terms), "main", BRANCH], capture_output=True, text=True)

    def test_j_public_repo_uses_config_noreply_email(self):
        self.main_commit()
        self.public(noreply="123+zz-test@users.noreply.github.com", wt_email="456+other@users.noreply.github.com")
        p = self.run_cli("refresh", "T-0001")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        who = "factory refresh <123+zz-test@users.noreply.github.com>"
        self.assertEqual(self.refreshed_ident(), f"{who}|{who}")
        g = self.guard_push()
        self.assertEqual(g.returncode, 0, g.stdout + g.stderr)
        self.assertIn("0 hits", g.stdout)

    def test_j2_public_repo_falls_back_to_repo_noreply_user_email(self):
        self.main_commit()
        self.public(wt_email="456+zz-test@users.noreply.github.com")
        p = self.run_cli("refresh", "T-0001")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        who = "factory refresh <456+zz-test@users.noreply.github.com>"
        self.assertEqual(self.refreshed_ident(), f"{who}|{who}")
        self.assertEqual(self.guard_push().returncode, 0)

    def test_j3_public_repo_without_noreply_refuses_nothing_pushed(self):
        self.main_commit()
        self.public()
        p = self.run_cli("refresh", "T-0001")
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn("REFRESH-IDENT", p.stderr)
        self.assertIn("factory config noreply_email", p.stderr)
        self.assertEqual(self.remote_ref(f"refs/heads/{BRANCH}"), self.head)
        ev = self.events("refresh-failed")
        self.assertEqual(len(ev), 1)
        self.assertTrue(ev[0]["detail"].startswith("REFRESH-IDENT"), ev[0]["detail"])
        self.assertEqual(self.events("refreshed"), [])

    def test_k_private_repo_keeps_localhost_identity(self):
        self.main_commit()
        self.env.sql("INSERT OR REPLACE INTO config(key,value) VALUES('public_text_repos','zz-test/other-public')")
        self.env.sql("INSERT OR REPLACE INTO config(key,value) VALUES('noreply_email','123+zz-test@users.noreply.github.com')")
        p = self.run_cli("refresh", "T-0001")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        who = "factory refresh <factory-refresh@localhost>"
        self.assertEqual(self.refreshed_ident(), f"{who}|{who}")

    def test_k2_public_project_gh_repo_applies_even_if_pr_repo_differs(self):
        self.main_commit()
        self.env.sql("UPDATE projects SET gh_repo='zz-test/proj-public' WHERE id='zz-test-proj'")
        self.env.sql("INSERT OR REPLACE INTO config(key,value) VALUES('public_text_repos','zz-test/proj-public')")
        p = self.run_cli("refresh", "T-0001")
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn("REFRESH-IDENT", p.stderr)
        self.assertEqual(self.remote_ref(f"refs/heads/{BRANCH}"), self.head)

    def test_l_config_noreply_email_accepts_noreply_only(self):
        e = self.env
        e.seed_agent("zz-test-dir", "director")
        bad = self.run_cli("config", "noreply_email", "zz-test@example.invalid", agent="zz-test-dir")
        self.assertEqual(bad.returncode, 2, bad.stdout + bad.stderr)
        self.assertIn("@users.noreply.github.com", bad.stderr)
        self.assertNotIn("zz-test@example.invalid", [r["value"] for r in e.sql("SELECT value FROM config WHERE key='noreply_email'")])
        ok = self.run_cli("config", "noreply_email", "123+zz-test@users.noreply.github.com", agent="zz-test-dir")
        self.assertEqual(ok.returncode, 0, ok.stdout + ok.stderr)
        self.assertEqual(e.sql("SELECT value FROM config WHERE key='noreply_email'")[0]["value"],
                         "123+zz-test@users.noreply.github.com")


if __name__ == "__main__":
    unittest.main()
