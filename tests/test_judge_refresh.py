import fcntl
import json
import os
import stat
import subprocess
import tempfile
import unittest
from pathlib import Path

from harness import FactoryTestCase, PR_URL, gates_summary
from harness import git as harness_git

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
elif argv[:1] == ["api"] and argv[1].endswith("/branches/main/protection"):
    if st.get("strict") is None:
        print("gh: Branch not protected (HTTP 404)", file=sys.stderr)
        sys.exit(1)
    print("true" if st["strict"] else "false")
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

GOMOD = "module factory/go\n\ngo 1.27\n"
GO_STUB = r'''#!/usr/bin/env python3
import glob, os, re, sys, time
from pathlib import Path
log = Path(os.environ["HOME"]) / ".local" / "go" / "merged_go_stub.log"
log.parent.mkdir(parents=True, exist_ok=True)
with open(log, "a") as f:
    f.write(" ".join(sys.argv) + "\n")
sleep = Path(os.environ["HOME"]) / ".local" / "go" / "merged_go_test_sleep"
if sleep.is_file():
    (log.parent / "merged_go_sleep.pid").write_text(str(os.getpid()))
    time.sleep(30)
    sys.exit(0)
files = sorted(glob.glob("**/*.go", recursive=True))
seen = {}
for p in files:
    for m in re.finditer(r"^func (\w+)\(", open(p).read(), re.M):
        seen.setdefault(m.group(1), []).append(p)
if "vet" in sys.argv:
    for p in files:
        with open(log, "a") as f:
            f.write(p + "\n")
    for name, where in seen.items():
        if len(where) > 1:
            print(f"mergedproof.{name} redeclared", file=sys.stderr)
            sys.exit(1)
if "test" in sys.argv and "-count=1" in sys.argv:
    with open(log, "a") as f:
        f.write("test -count=1\n")
    for name, where in seen.items():
        if len(where) > 1:
            print(f"mergedproof.{name} redeclared", file=sys.stderr)
            sys.exit(1)
sys.exit(0)
'''


def git(cwd, *args, check=True):
    return harness_git(*args, cwd=cwd, check=check)


class JudgeRefreshTests(FactoryTestCase):
    INPROC = True
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
        (Path(cwd) / name).parent.mkdir(parents=True, exist_ok=True)
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

    def run_cli(self, *args, agent="zz-test-lead", poll="0", extra_env=None):
        path = f"{self.zz / 'bin'}{os.pathsep}{self.env.env()['PATH']}"
        env = {"PATH": path, "ZZ_GH_STATE": str(self.state), "FACTORY_JUDGE_POLL_S": poll}
        if extra_env:
            env.update(extra_env)
        return self.env.run(*args, agent=agent, extra_env=env)

    def judge(self):
        return self.run_cli("judge", "T-0001", "--merge", "--reason", "zz-test", agent="zz-test-judge")

    def plant_go_stub(self):
        go = self.env.user_home / ".local" / "go" / "bin" / "go"
        go.parent.mkdir(parents=True, exist_ok=True)
        go.write_text(GO_STUB)
        go.chmod(go.stat().st_mode | stat.S_IXUSR)
        log = self.env.user_home / ".local" / "go" / "merged_go_stub.log"
        log.parent.mkdir(parents=True, exist_ok=True)
        log.write_text("")

    def go_log(self):
        p = self.env.user_home / ".local" / "go" / "merged_go_stub.log"
        return p.read_text() if p.is_file() else ""

    def _init_go_pr(self, pr_name, pr_body):
        git(self.wt, "checkout", "-q", BRANCH)
        self.commit(self.wt, "go/go.mod", GOMOD, "go mod")
        self.commit(self.wt, f"go/internal/mergedproof/{pr_name}", pr_body, "pr go")
        git(self.wt, "push", "-q", "origin", f"HEAD:refs/heads/{BRANCH}", "HEAD:refs/pull/1/head")
        self.head = git(self.wt, "rev-parse", "HEAD")
        (self.zz / "pushes.log").write_text("")
        self.ok_verdicts()

    def _behind_go_main(self, main_name, main_body):
        self.env.sql("INSERT OR REPLACE INTO config(key,value) VALUES('stale_strict:zz-test/repo',?)",
                     (json.dumps({"at": 1, "strict": False}),))
        self.main_commit(f"go/internal/mergedproof/{main_name}", main_body)
        self.write_state(strict=False)

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
        main_tip = self.remote_ref("refs/heads/main")
        self.assertEqual(git(self.remote, "merge-base", main_tip, head), main_tip,
                         "merged head does not contain origin/main")
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
        self.env.sql("INSERT OR REPLACE INTO config(key,value) VALUES('noreply_email','123+volekerb@users.noreply.github.com')")
        self.judge()  # the merge itself then meets the public text check; only the refresh is under test here
        new = self.remote_ref(f"refs/heads/{BRANCH}")
        self.assertEqual(self.pushes(), [new])
        who = "factory refresh <123+volekerb@users.noreply.github.com>"
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

    def test_g_behind_ok_merges_clean_behind_head_without_refresh(self):
        # G-022 acceptance 2: strict=false on GitHub, BEHIND but MERGEABLE, green checks, PASS verdicts
        self.main_commit()
        self.write_state(strict=False)
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.pushes(), [])
        self.assertEqual(self.events("refreshed"), [])
        m = self.merges()
        self.assertEqual(len(m), 1)
        self.assertEqual(m[0]["argv"][m[0]["argv"].index("--match-head-commit") + 1], self.head)
        self.assertEqual(self.remote_ref(f"refs/heads/{BRANCH}"), self.head)
        reasons = [r["summary"] for r in self.env.sql("SELECT summary FROM verdicts WHERE task_id='T-0001' AND role='judge'")]
        self.assertTrue(any("merged behind main by stale_policy=behind_ok (strict=false on GitHub)" in d for d in reasons), reasons)
        self.assertEqual(self.env.sql("SELECT status FROM tasks WHERE id='T-0001'")[0]["status"], "done")

    def test_g5_behind_ok_refreshes_when_main_changed_tests_yml(self):
        # T-1000: the head predates a CI workflow change on main: behind_ok does not apply, the judge refreshes first
        self.main_commit(".github/workflows/tests.yml", "jobs: {guard-equiv: {}}\n")
        self.write_state(strict=False)
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(len(self.events("refreshed")), 1)
        self.assertEqual(len(self.pushes()), 1)
        self.assertMergedPinned(self.remote_ref(f"refs/heads/{BRANCH}"))
        reasons = [r["summary"] for r in self.env.sql("SELECT summary FROM verdicts WHERE role='judge'")]
        self.assertFalse(any("behind_ok" in d for d in reasons), reasons)

    def test_g6_behind_ok_still_merges_when_main_changed_other_workflow_file(self):
        self.main_commit(".github/workflows/other.yml", "x: 1\n")
        self.write_state(strict=False)
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.pushes(), [])
        reasons = [r["summary"] for r in self.env.sql("SELECT summary FROM verdicts WHERE task_id='T-0001' AND role='judge'")]
        self.assertTrue(any("tests.yml unchanged on main since the merge-base" in d for d in reasons), reasons)

    def _pr_edits_base_main_edits_other_line(self):
        # T-1025: the PR and main both change base.txt on different lines: merge-tree is clean, no CI ever ran the sum
        git(self.wt, "checkout", "-q", BRANCH)
        self.commit(self.wt, "base.txt", "PR\nline2\nline3\n", "pr edits base")
        git(self.wt, "push", "-q", "origin", f"HEAD:refs/heads/{BRANCH}", "HEAD:refs/pull/1/head")
        self.head = git(self.wt, "rev-parse", "HEAD")
        (self.zz / "pushes.log").write_text("")
        self.main_commit("base.txt", "line1\nline2\nMAIN\n")
        self.ok_verdicts()
        self.write_state(strict=False)

    def test_g7b_behind_ok_refreshes_when_main_changed_a_file_the_pr_also_changes(self):
        self._pr_edits_base_main_edits_other_line()
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(len(self.events("refreshed")), 1)  # refreshed (CI reruns on the combined tree), not merged behind
        self.assertEqual(len(self.pushes()), 1)
        self.assertMergedPinned(self.remote_ref(f"refs/heads/{BRANCH}"))
        reasons = [r["summary"] for r in self.env.sql("SELECT summary FROM verdicts WHERE role='judge'")]
        self.assertFalse(any("behind_ok" in d for d in reasons), reasons)

    def test_g8_named_red_main_fix_with_shared_file_refreshes_and_merges_not_deadlocked(self):
        # the fix task of a frozen main is exempt from the freeze; the overlap rule makes it refresh, never wait forever
        self._pr_edits_base_main_edits_other_line()
        self.env.sql("INSERT OR REPLACE INTO config(key,value) VALUES('red_main_freeze','1')")
        self.env.sql("INSERT OR REPLACE INTO config(key,value) VALUES('red_main:zz-test/repo',?)",
                     (json.dumps({"run": "1", "sha": "abc", "url": "u", "since": "t", "task": "T-0001"}),))
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(len(self.events("refreshed")), 1)
        self.assertMergedPinned(self.remote_ref(f"refs/heads/{BRANCH}"))

    def test_g7_stale_policy_doc_quotes_current_reason_and_tests_yml_exception(self):
        # T-1010: docs/stale-policy.md must quote the reason text the judge writes and name the T-1000 exception
        root = Path(__file__).resolve().parent.parent
        reasons = [l for l in (root / "bin" / "factory_cli.py").read_text().splitlines() if l.startswith("BEHIND_OK_NOTE = ")]
        self.assertEqual(len(reasons), 1)
        note = reasons[0].split('"')[1]
        doc = (root / "docs" / "stale-policy.md").read_text()
        self.assertIn(note, doc)
        self.assertIn(".github/workflows/tests.yml", doc)
        self.assertIn("T-1000", doc)

    def _strict_refreshes(self, strict):
        self.main_commit()
        self.write_state(strict=strict)
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(len(self.events("refreshed")), 1)
        self.assertEqual(len(self.pushes()), 1)
        self.assertMergedPinned(self.remote_ref(f"refs/heads/{BRANCH}"))
        self.assertFalse(any("behind_ok" in r["summary"] for r in self.env.sql("SELECT summary FROM verdicts WHERE role='judge'")))

    def test_g2_strict_true_is_stale_only_as_before(self):  # acceptance 3
        self._strict_refreshes(True)

    def test_g2b_unreadable_protection_is_stale_only_as_before(self):  # acceptance 3
        self._strict_refreshes(None)

    def test_g3_policy_strict_refreshes_even_if_github_strict_false(self):
        self.main_commit()
        self.write_state(strict=False)
        self.env.sql("INSERT OR REPLACE INTO config(key,value) VALUES('stale_policy','strict')")
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(len(self.pushes()), 1)
        self.assertFalse(any("behind_ok" in r["summary"] for r in self.env.sql("SELECT summary FROM verdicts WHERE role='judge'")))

    def test_g4_behind_ok_does_not_merge_a_conflicting_pr(self):
        # acceptance 4: GitHub says CONFLICTING (DIRTY): refreshed as today, here the refresh finds a real conflict
        git(self.wt, "checkout", "-q", BRANCH)
        self.commit(self.wt, "base.txt", "line1\nPR\nline3\n", "pr edits base")
        git(self.wt, "push", "-q", "origin", f"HEAD:refs/heads/{BRANCH}", "HEAD:refs/pull/1/head")
        self.head = git(self.wt, "rev-parse", "HEAD")
        (self.zz / "pushes.log").write_text("")
        self.main_commit("base.txt", "line1\nMAIN\nline3\n")
        self.ok_verdicts()
        self.write_state(strict=False)
        st = self.gh_state()
        st["pr"]["mergeable"] = "CONFLICTING"
        self.state.write_text(json.dumps(st))
        p = self.judge()
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("CONFLICT-ONLY", p.stderr + p.stdout)
        self.assertNoMerge()
        self.assertEqual(self.pushes(), [])

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
        p = self.run_cli("verdict", "T-0001", "--fail", "--cause", "bug", "--summary", "broken", agent="zz-test-qa")
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


    def _merge_main_go_mod(self, extra_go=()):
        self.main_commit("go/go.mod", GOMOD)
        for name, text in extra_go:
            self.main_commit(name, text)
        git(self.wt, "fetch", "-q", "origin", "main")
        git(self.wt, "merge", "-q", "--no-edit", "origin/main")

    def test_merged_go_proof_env_uses_toolchain_cache_paths(self):
        """GOCACHE and GOMODCACHE match docs/go-toolchain.md (same roots as the HQ Go install)."""
        home = self.env.user_home
        with self.env.load_factory() as f:
            env = f.merged_go_proof_env()
        self.assertEqual(env["GOCACHE"], str(home / ".cache" / "go" / "build"))
        self.assertEqual(env["GOMODCACHE"], str(home / ".cache" / "go" / "path" / "pkg" / "mod"))

    def test_merged_go_testdata_only_clash_vets_internal_cli(self):
        git(self.wt, "checkout", "-q", BRANCH)
        self.env.sql("INSERT OR REPLACE INTO config(key,value) VALUES('stale_strict:zz-test/repo',?)",
                     (json.dumps({"at": 1, "strict": False}),))
        self._merge_main_go_mod((("go/internal/cli/proof.go", "package cli\nfunc ProofPkg() {}\n"),))
        self.commit(self.wt, "go/internal/cli/testdata/a.txt", "a\n", "pr testdata")
        git(self.wt, "push", "-q", "origin", f"HEAD:refs/heads/{BRANCH}", "HEAD:refs/pull/1/head")
        self.head = git(self.wt, "rev-parse", "HEAD")
        (self.zz / "pushes.log").write_text("")
        self.main_commit("go/internal/cli/testdata/b.txt", "b\n")
        self.write_state(strict=False)
        self.plant_go_stub()
        git(self.wt, "fetch", "-q", "origin", "main")
        sha, main = self.head, self.remote_ref("refs/heads/main")
        with self.env.load_factory() as f:
            msg = f.merged_go_refusal(f.db(), str(self.wt), sha, main)
        self.assertIsNone(msg)
        log = self.go_log()
        self.assertIn("vet", log)
        self.assertIn("./internal/cli", log)

    def test_merged_go_root_go_file_vets_dot_package(self):
        git(self.wt, "checkout", "-q", BRANCH)
        self.env.sql("INSERT OR REPLACE INTO config(key,value) VALUES('stale_strict:zz-test/repo',?)",
                     (json.dumps({"at": 1, "strict": False}),))
        self._merge_main_go_mod()
        self.commit(self.wt, "go/rootpkg.go", "package rootpkg\nfunc Root() {}\n", "root go file")
        git(self.wt, "push", "-q", "origin", f"HEAD:refs/heads/{BRANCH}", "HEAD:refs/pull/1/head")
        self.head = git(self.wt, "rev-parse", "HEAD")
        (self.zz / "pushes.log").write_text("")
        self.main_commit("go/sibling.go", "package sibling\nfunc Sibling() {}\n")
        self.write_state(strict=False)
        self.plant_go_stub()
        git(self.wt, "fetch", "-q", "origin", "main")
        sha, main = self.head, self.remote_ref("refs/heads/main")
        with self.env.load_factory() as f:
            msg = f.merged_go_refusal(f.db(), str(self.wt), sha, main)
        self.assertIsNone(msg)
        log = self.go_log()
        self.assertIn(" vet .", log)
        self.assertNotIn("./...", log)

    def test_merged_go_merge_tree_conflict_not_behind_ok(self):
        git(self.wt, "checkout", "-q", BRANCH)
        self._merge_main_go_mod()
        body = "package mergedproof\nfunc Same() int { return 1 }\n"
        self.commit(self.wt, "go/internal/mergedproof/same.go", body, "pr")
        git(self.wt, "push", "-q", "origin", f"HEAD:refs/heads/{BRANCH}", "HEAD:refs/pull/1/head")
        self.head = git(self.wt, "rev-parse", "HEAD")
        (self.zz / "pushes.log").write_text("")
        self.main_commit("go/internal/mergedproof/same.go",
                         "package mergedproof\nfunc Same() int { return 2 }\n")
        git(self.wt, "fetch", "-q", "origin", "main")
        sha, main = self.head, self.remote_ref("refs/heads/main")
        with self.env.load_factory() as f:
            msg = f.merged_go_refusal(f.db(), str(self.wt), sha, main)
        self.assertIs(msg, f.MERGED_GO_UNMERGEABLE)

    def test_merged_go_clash_refuses(self):
        self._init_go_pr("pr_clash.go", "package mergedproof\nfunc Clash() {}\n")
        self._behind_go_main("main_clash.go", "package mergedproof\nfunc Clash() {}\n")
        self.plant_go_stub()
        p = self.judge()
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn("MERGED-TREE:", p.stderr)
        self.assertIn("Clash redeclared", p.stderr)
        self.assertNotIn("STALE-ONLY", p.stderr)
        self.assertNoMerge()
        self.assertEqual(self.pushes(), [])
        self.assertEqual(self.events("refreshed"), [])
        log = self.go_log()
        self.assertIn("vet", log)
        self.assertIn("pr_clash.go", log)
        self.assertIn("main_clash.go", log)

    def test_merged_go_clean_merges_behind(self):
        self._init_go_pr("pr_clean.go", "package mergedproof\nfunc TaskPyStr() {}\n")
        self._behind_go_main("main_clean.go", "package mergedproof\nfunc FromMain() {}\n")
        self.plant_go_stub()
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.pushes(), [])
        self.assertEqual(self.events("refreshed"), [])
        m = self.merges()
        self.assertEqual(len(m), 1)
        self.assertEqual(m[0]["argv"][m[0]["argv"].index("--match-head-commit") + 1], self.head)
        reasons = [r["summary"] for r in self.env.sql("SELECT summary FROM verdicts WHERE role='judge'")]
        self.assertTrue(any("merged behind main by stale_policy=behind_ok" in d for d in reasons), reasons)
        log = self.go_log()
        self.assertIn("vet", log)
        self.assertIn("test -count=1", log)
        self.assertIn("pr_clean.go", log)
        self.assertIn("main_clean.go", log)

    def test_merged_go_proof_off_merges_clash(self):
        self._init_go_pr("pr_clash.go", "package mergedproof\nfunc Clash() {}\n")
        self._behind_go_main("main_clash.go", "package mergedproof\nfunc Clash() {}\n")
        self.env.sql("INSERT OR REPLACE INTO config(key,value) VALUES('merged_go_proof','0')")
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(len(self.merges()), 1)
        self.assertEqual(self.pushes(), [])

    def test_merged_go_missing_toolchain_refuses(self):
        self._init_go_pr("pr_clash.go", "package mergedproof\nfunc Clash() {}\n")
        self._behind_go_main("main_clash.go", "package mergedproof\nfunc Clash() {}\n")
        p = self.judge()
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn("MERGED-TREE: go proof could not run (~/.local/go/bin/go missing); nothing merged", p.stderr)
        self.assertNoMerge()

    def test_merged_go_timeout_kills_the_group(self):
        self._init_go_pr("pr_clean.go", "package mergedproof\nfunc TaskPyStr() {}\n")
        self._behind_go_main("main_clean.go", "package mergedproof\nfunc FromMain() {}\n")
        self.plant_go_stub()
        flag = self.env.user_home / ".local" / "go" / "merged_go_test_sleep"
        pid_file = self.env.user_home / ".local" / "go" / "merged_go_sleep.pid"
        flag.parent.mkdir(parents=True, exist_ok=True)
        flag.touch()
        pid_file.write_text("")
        git(self.wt, "fetch", "-q", "origin", "main")
        sha, main = self.head, self.remote_ref("refs/heads/main")
        with self.env.load_factory() as f:
            msg = f.merged_go_refusal(f.db(), str(self.wt), sha, main, timeout=1)
        self.assertIsNotNone(msg)
        self.assertIn("MERGED-TREE:", msg)
        self.assertIn("timed out", msg)
        pid = int(pid_file.read_text().strip())
        with self.assertRaises(ProcessLookupError):
            os.kill(pid, 0)

    def test_merged_go_proof_config_rejects_other_values(self):
        self.env.seed_agent("zz-test-director", "director")
        p = self.env.run("config", "merged_go_proof", "2", agent="zz-test-director")
        self.assertEqual(p.returncode, 2, p.stdout + p.stderr)
        self.assertIn("merged_go_proof must be 0 or 1", p.stderr)


class StaleBehindOkRulesetTests(FactoryTestCase):
    """T-1310: stale_behind_ok ruleset fallback when classic branch protection is missing (SC1)."""
    SLUG = "acme/rules-repo"

    def setUp(self):
        super().setUp()
        self.env.sql("INSERT OR REPLACE INTO config(key,value) VALUES('stale_policy','behind_ok')")

    def _gh_api(self, *, prot=None, prot_exit=0, prot_err="", rules=None, rules_exit=0, rules_err=""):
        api = []
        if prot_exit:
            api.append({"match": [f"repos/{self.SLUG}/branches/main/protection"],
                        "exit": prot_exit, "err": prot_err})
        else:
            api.append({"match": [f"repos/{self.SLUG}/branches/main/protection"], "out": prot})
        if rules_exit:
            api.append({"match": [f"repos/{self.SLUG}/rules/branches/main"],
                        "exit": rules_exit, "err": rules_err})
        elif rules is not None:
            api.append({"match": [f"repos/{self.SLUG}/rules/branches/main"], "out": rules})
        self.env.set_gh(api=api)

    def _behind_ok(self):
        with self.env.load_factory() as f:
            return f.stale_behind_ok(f.db(), self.SLUG)

    def test_sc1_ruleset_without_strict_status_checks_is_behind_ok_and_cached(self):
        rules = [{"type": "deletion"}, {"type": "non_fast_forward"}, {"type": "pull_request", "parameters": {}}]
        self._gh_api(prot_exit=1, prot_err="gh: Branch not protected (HTTP 404)", rules=rules)
        self.env.calls_file.write_text("")
        self.assertTrue(self._behind_ok())
        [row] = self.env.sql("SELECT value FROM config WHERE key=?", ("stale_strict:" + self.SLUG,))
        self.assertEqual(json.loads(row["value"])["strict"], False)
        rules_calls = lambda: sum(1 for c in self.env.calls() if "rules/branches/main" in " ".join(c["argv"]))
        self.assertEqual(rules_calls(), 1)
        self.assertTrue(self._behind_ok())
        self.assertEqual(rules_calls(), 1)

    def test_sc1_ruleset_strict_required_status_checks_is_strict(self):
        rules = [{"type": "required_status_checks",
                  "parameters": {"strict_required_status_checks_policy": True, "required_status_checks": []}}]
        self._gh_api(prot_exit=1, prot_err="gh: Branch not protected (HTTP 404)", rules=rules)
        self.assertFalse(self._behind_ok())

    def test_sc1_ruleset_read_failure_is_strict(self):
        self._gh_api(prot_exit=1, prot_err="gh: Branch not protected (HTTP 404)",
                     rules_exit=1, rules_err="gh: forbidden")
        self.assertFalse(self._behind_ok())

    def test_sc1_ruleset_bad_json_is_strict(self):
        self._gh_api(prot_exit=1, prot_err="gh: Branch not protected (HTTP 404)", rules="{")
        self.assertFalse(self._behind_ok())

    def test_sc1_classic_strict_false_is_behind_ok(self):
        self._gh_api(prot="false")
        self.assertTrue(self._behind_ok())

    def test_sc1_classic_strict_true_is_strict(self):
        self._gh_api(prot="true")
        self.assertFalse(self._behind_ok())

    def test_sc1_classic_404_not_found_does_not_use_ruleset_or_cache_non_strict(self):
        self._gh_api(prot_exit=1, prot_err="gh: Not Found (HTTP 404)", rules=[])
        self.env.calls_file.write_text("")
        self.assertFalse(self._behind_ok())
        row = self.env.sql("SELECT value FROM config WHERE key=?", ("stale_strict:" + self.SLUG,))
        self.assertFalse(row)
        rules_calls = sum(1 for c in self.env.calls() if "rules/branches/main" in " ".join(c["argv"]))
        self.assertEqual(rules_calls, 0)

    def test_sc1_ruleset_paginated_strict_rule_on_later_page_is_strict(self):
        # First page alone would be non-strict; --paginate must include the strict rule from a later page.
        rules = [{"type": "deletion"},
                 {"type": "required_status_checks",
                  "parameters": {"strict_required_status_checks_policy": True, "required_status_checks": []}}]
        self._gh_api(prot_exit=1, prot_err="gh: Branch not protected (HTTP 404)", rules=rules)
        self.env.calls_file.write_text("")
        self.assertFalse(self._behind_ok())
        rules_argv = [c["argv"] for c in self.env.calls()
                      if "rules/branches/main" in " ".join(c["argv"])]
        self.assertEqual(len(rules_argv), 1)
        self.assertIn("--paginate", rules_argv[0])


if __name__ == "__main__":
    unittest.main()
