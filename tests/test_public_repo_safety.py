"""G-021 T3: bin/factory public-repo safety. config keys, state branch never pushed to a public or frozen origin, judge
pre-merge public text check (guard loaded from the main checkout), frozen repo refusals for every bin/factory push and
merge. Temp FACTORY_HOME, temp bare origins whose path ends acme/pub (public), acme/old (frozen), acme/priv (neither),
the tests/stubs gh, a temp terms file holding `zebracorp`."""
import json
import os
import subprocess
import unittest
from pathlib import Path

from harness import FactoryTestCase, REPO, gates_summary, git

GREEN = [{"name": "unittest", "status": "COMPLETED", "conclusion": "SUCCESS"}]
PUB_URL = "https://github.com/acme/pub/pull/1"
OLD_URL = "https://github.com/acme/old/pull/1"
NOREPLY = "1+dev@users.noreply.github.com"
MERGE_SHA = "a" * 40
CLEAN = {"GIT_AUTHOR_NAME": "dev", "GIT_AUTHOR_EMAIL": "dev@users.noreply.github.com",
         "GIT_COMMITTER_NAME": "dev", "GIT_COMMITTER_EMAIL": "dev@users.noreply.github.com"}


def run_git(cwd, *args, env=None):
    return subprocess.run(["git", *args], cwd=cwd, capture_output=True, text=True, check=True,
                          env={**os.environ, **(env or CLEAN)}).stdout.strip()


class Base(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        (e.home_dir / "ci").symlink_to(REPO / "ci")  # the guard is loaded from the main checkout (HOME/ci)
        self.terms = e.home / "terms.txt"
        self.terms.write_text("zebracorp\n")
        self.terms_env = {"SCRUB_TERMS_FILE": str(self.terms)}

    def bare(self, slug):
        """A bare origin whose path ends OWNER/NAME.git, so its slug parses."""
        path = self.env.home / "remotes" / (slug + ".git")
        path.parent.mkdir(parents=True, exist_ok=True)
        run_git(self.env.home, "init", "-q", "--bare", "-b", "main", str(path))
        return path

    def refs(self, bare):
        return run_git(bare, "for-each-ref")


class StateBranchTests(Base):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_project()
        e.seed_goal(project="zz-test-proj", title="zz-test state goal")
        self.ok("config", "git_push", "1")

    def origin(self, slug, url=None):
        self.bare_path = self.bare(slug)
        run_git(self.env.home_dir, "remote", "add", "origin", url or str(self.bare_path))
        run_git(self.env.home_dir, "commit", "-q", "--allow-empty", "-m", "base")
        run_git(self.env.home_dir, "push", "-q", "-u", "origin", "main")
        return self.bare_path

    def has_state(self, bare):
        return subprocess.run(["git", "show-ref", "--verify", "-q", "refs/heads/state"], cwd=bare).returncode == 0

    def local_state(self):
        return run_git(self.env.home_dir, "rev-parse", "refs/heads/state")

    def test_default_off_state_push_to_pub_origin_still_pushes(self):
        bare = self.origin("acme/pub")
        p = self.ok("export")
        self.assertNotIn("state push refused", p.stdout)
        self.assertTrue(self.has_state(bare))
        self.assertEqual(run_git(bare, "rev-parse", "refs/heads/state"), self.local_state())

    def test_public_origin_never_gets_state(self):
        bare = self.origin("acme/pub")
        before = self.refs(bare)
        self.ok("config", "public_text_repos", "acme/pub")
        p = self.ok("export")
        self.assertIn("state push refused: origin acme/pub is public (G-021)", p.stdout)
        self.assertFalse(self.has_state(bare))
        self.assertEqual(self.refs(bare), before)
        self.assertTrue(self.local_state())  # the local state commit still happens

    def test_unparsable_origin_refused_when_public_list_set(self):
        bare = self.bare("acme/priv")
        (self.env.home / "lonely").symlink_to(bare)  # "../lonely" names no OWNER/NAME
        run_git(self.env.home_dir, "remote", "add", "origin", "../lonely")
        run_git(self.env.home_dir, "commit", "-q", "--allow-empty", "-m", "base")
        before = self.refs(bare)
        self.ok("config", "public_text_repos", "acme/pub")
        p = self.ok("export")
        self.assertIn("state push refused: origin cannot be parsed", p.stdout)
        self.assertFalse(self.has_state(bare))
        self.assertEqual(self.refs(bare), before)

    def test_private_origin_pushes_when_public_list_names_another_repo(self):
        bare = self.origin("acme/priv")
        self.ok("config", "public_text_repos", "acme/pub")
        p = self.ok("export")
        self.assertNotIn("state push refused", p.stdout)
        self.assertTrue(self.has_state(bare))

    def test_frozen_origin_never_gets_state(self):
        bare = self.origin("acme/old")
        before = self.refs(bare)
        self.ok("config", "frozen_repos", "acme/old")
        p = self.ok("export")
        self.assertIn("repo acme/old is frozen (G-021)", p.stdout)
        self.assertEqual(self.refs(bare), before)
        self.assertTrue(self.local_state())

    def test_push_url_is_checked_too(self):
        bare = self.origin("acme/priv")
        pub = self.bare("acme/pub")
        run_git(self.env.home_dir, "remote", "set-url", "--push", "origin", str(pub))
        before = self.refs(pub)
        self.ok("config", "public_text_repos", "acme/pub")
        self.assertIn("origin acme/pub is public", self.ok("export").stdout)
        self.assertEqual(self.refs(pub), before)

    def test_state_push_zero_message_unchanged(self):
        self.origin("acme/priv")
        self.ok("config", "state_push", "0")
        self.assertIn("state push disabled (state_push=0)", self.ok("export").stdout)


class JudgeTextTests(Base):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_project()
        e.sql("UPDATE projects SET gh_repo='acme/pub' WHERE id='zz-test-proj'")
        e.seed_goal(project="zz-test-proj")
        e.seed_task()
        for name, role in (("zz-test-reviewer", "reviewer"), ("zz-test-qa", "qa"), ("zz-test-judge", "judge")):
            e.seed_agent(name, role, goal="G-001", task="T-0001")
        e.seed_agent("zz-test-director", "director")
        self.ok("config", "judge_ci_wait_s", "0")
        self.ok("config", "public_text_repos", "acme/pub")
        self.ok("config", "noreply_email", NOREPLY)
        self.pr(self.terms_env)

    def pr(self, extra, author_email="dev@users.noreply.github.com", title="fix T-0529", comments=(), body="clean body",
           merge_emails=(NOREPLY, "noreply@github.com")):
        """A PR head one commit past main on a temp bare remote (refs/pull/1/head), the gh stub's PR text in `texts`."""
        e = self.env
        remote, wt = e.home / "pr-origin.git", e.home / "pr-wt"
        for d in (remote, wt):
            if d.exists():
                subprocess.run(["rm", "-rf", str(d)], check=True)
        run_git(e.home, "init", "-q", "--bare", "-b", "main", str(remote))
        run_git(e.home, "clone", "-q", str(remote), str(wt))
        run_git(wt, "commit", "-q", "--allow-empty", "-m", "base")
        run_git(wt, "push", "-q", "origin", "HEAD:main")
        run_git(wt, "checkout", "-q", "-b", "feature")
        (wt / "feature.txt").write_text("feature\n")
        run_git(wt, "add", "feature.txt")
        env = {**CLEAN, "GIT_AUTHOR_EMAIL": author_email}
        run_git(wt, "commit", "-q", "-m", "add feature", env=env)
        run_git(wt, "push", "-q", "origin", "HEAD:refs/pull/1/head")
        e.head_sha = run_git(wt, "rev-parse", "HEAD")
        e.set_gh(repo_url=str(remote))
        e.seed_pr(checks=GREEN)
        st = json.loads(e.gh_state.read_text())
        st["prs"]["https://github.com/zz-test/repo/pull/1"].update(
            {"title": title, "body": body, "comments": [{"body": c} for c in comments], "reviews": [],
             "commits": [{"messageHeadline": "add feature", "messageBody": ""}],
             "mergeCommit": {"oid": MERGE_SHA}})
        st["api"] = [{"match": ["/commits/" + MERGE_SHA], "out": {"commit": {
            "author": {"email": merge_emails[0]}, "committer": {"email": merge_emails[1]}}}}]
        e.gh_state.write_text(json.dumps(st))
        self.ok("verdict", "T-0001", "--pass", "--summary", "ok", agent="zz-test-reviewer")
        self.ok("verdict", "T-0001", "--pass", "--summary", gates_summary(), agent="zz-test-qa")
        self.extra = extra

    def judge(self, extra=None):
        return self.env.run("judge", "T-0001", "--merge", "--reason", "zz-test", agent="zz-test-judge",
                            extra_env=self.extra if extra is None else extra)

    def merges(self):
        return [c for c in self.env.calls("gh") if c["argv"][:2] == ["pr", "merge"]]

    def assert_text_refused(self, p, *words):
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn("judge: refused (public text:", p.stderr)
        for w in words:
            self.assertIn(w, p.stderr)
        self.assertNotIn("zebracorp", p.stdout + p.stderr)
        self.assertEqual(self.merges(), [])
        ev = self.env.sql("SELECT detail FROM events WHERE action='judge-blocked'")
        self.assertTrue(ev and "judge: refused (public text:" in ev[-1]["detail"])

    def merge_args(self):
        return self.merges()[0]["argv"]

    def test_public_merge_passes_noreply_author_email(self):
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        argv = self.merge_args()
        self.assertEqual(argv[argv.index("--author-email") + 1], NOREPLY)

    def set_commits(self, commits):
        st = json.loads(self.env.gh_state.read_text())
        st["prs"]["https://github.com/zz-test/repo/pull/1"]["commits"] = commits
        self.env.gh_state.write_text(json.dumps(st))

    def test_public_squash_body_drops_non_noreply_coauthors(self):
        self.set_commits([
            {"messageHeadline": "one", "messageBody": "Co-authored-by: Dev <1+dev@users.noreply.github.com>"},
            {"messageHeadline": "two", "messageBody": "keep this\n\nCo-authored-by: factory refresh <factory-refresh@localhost>"},
            {"messageHeadline": "three", "messageBody": "Co-authored-by: Claude <noreply@anthropic.com>"}])
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        argv = self.merge_args()
        body = argv[argv.index("--body") + 1]
        self.assertNotIn("localhost", body)
        self.assertIn("1+dev@users.noreply.github.com", body)
        self.assertIn("noreply@anthropic.com", body)
        self.assertIn("keep this", body)
        self.assertEqual(argv[argv.index("--author-email") + 1], NOREPLY)
        self.assertIn("--match-head-commit", argv)
        self.assertNotIn("--subject", argv)

    def test_private_repo_merge_has_no_body(self):
        self.ok("config", "public_text_repos", "")
        self.pr({})
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertNotIn("--body", self.merge_args())
        self.assertNotIn("--author-email", self.merge_args())

    def test_unreadable_commits_refuse_public_merge(self):
        self.set_commits([])
        p = self.judge()
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn("cannot build the public squash body", p.stderr)
        self.assertEqual(self.merges(), [])

    def test_private_repo_merge_has_no_author_email(self):
        self.ok("config", "public_text_repos", "")
        self.pr({})
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertNotIn("--author-email", self.merge_args())

    def test_no_noreply_address_refuses_to_merge(self):
        self.ok("config", "noreply_email", "")  # falls back to the repo-local user.email: example.invalid
        p = self.judge()
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn("no noreply author email", p.stderr)
        self.assertEqual(self.merges(), [])
        self.assertNotEqual(self.env.sql("SELECT status FROM tasks WHERE id='T-0001'")[0]["status"], "done")

    def test_git_identity_noreply_user_email_is_used(self):
        self.ok("config", "noreply_email", "")
        ident = {**self.terms_env, "GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "user.email",
                 "GIT_CONFIG_VALUE_0": "7+me@users.noreply.github.com"}
        p = self.judge(ident)
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        argv = self.merge_args()
        self.assertEqual(argv[argv.index("--author-email") + 1], "7+me@users.noreply.github.com")

    def test_post_merge_real_author_email_fails_loudly(self):
        self.pr(self.terms_env, merge_emails=("real.person@example.com", "noreply@github.com"))
        p = self.judge()
        self.assertEqual(p.returncode, 6, p.stdout + p.stderr)
        self.assertIn("public-main email check failed", p.stderr)
        self.assertNotIn("real.person", p.stdout + p.stderr)
        self.assertEqual(self.env.sql("SELECT status FROM tasks WHERE id='T-0001'")[0]["status"], "done")
        self.assertTrue(self.env.sql("SELECT 1 FROM events WHERE action='merge-email-leak'"))
        m = self.env.sql("SELECT recipient, kind, body FROM messages WHERE subject LIKE '%email check FAILED%'")
        self.assertEqual(len(m), 1)
        self.assertNotIn("real.person", m[0]["body"])

    def test_post_merge_real_committer_email_fails_loudly(self):
        self.pr(self.terms_env, merge_emails=(NOREPLY, "real.person@example.com"))
        self.assertEqual(self.judge().returncode, 6)

    def test_post_merge_unreadable_commit_fails_loudly(self):
        st = json.loads(self.env.gh_state.read_text())
        st["api"] = []
        self.env.gh_state.write_text(json.dumps(st))
        p = self.judge()
        self.assertEqual(p.returncode, 6, p.stdout + p.stderr)
        self.assertIn("email not verified", p.stderr)

    def test_default_off_private_text_merges(self):
        self.ok("config", "public_text_repos", "")
        self.pr({}, title="zebracorp fix")
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(len(self.merges()), 1)

    def test_clean_pr_in_public_repo_merges(self):
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(len(self.merges()), 1)
        self.assertIn("view", [c["argv"][1] for c in self.env.calls("gh") if c["argv"][:1] == ["pr"]])

    def test_comment_with_owner_term_refused(self):
        self.pr(self.terms_env, comments=["thanks zebracorp team"])
        self.assert_text_refused(self.judge(), "owner-term")

    def test_title_and_body_and_branch_are_scanned(self):
        self.pr(self.terms_env, title="zebracorp rename")
        self.assert_text_refused(self.judge(), "owner-term")
        self.pr(self.terms_env, body="see /home/zebracorp/notes")
        self.assert_text_refused(self.judge(), "owner-term")

    def test_commit_identity_email_refused(self):
        self.pr(self.terms_env, author_email="alice@realmail.test")
        self.assert_text_refused(self.judge(), "email")

    def test_no_terms_refused(self):
        p = self.judge(extra={"SCRUB_TERMS_FILE": ""})
        self.assert_text_refused(p, "no owner terms loaded")

    def test_scrub_terms_file_config_is_used(self):
        self.ok("config", "scrub_terms_file", str(self.terms))
        self.pr({}, comments=["zebracorp"])
        self.assert_text_refused(self.judge({}), "owner-term")

    def test_guard_comes_from_main_checkout_not_pr_tree(self):
        wt = self.env.home / "pr-wt"  # the PR's own checkout, a git repo carrying a guard that never finds anything
        (wt / "ci").mkdir()
        (wt / "ci" / "public_text_guard.py").write_text(
            "class UsageError(Exception): pass\nclass NoTermsError(UsageError): pass\n"
            "def load_terms_or_fail(*a, **k): return ['x']\ndef check_text(*a): return []\n"
            "def check_ref(*a): return []\ndef check_push(*a, **k): return []\n")
        self.env.sql("UPDATE tasks SET worktree=? WHERE id='T-0001'", (str(wt),))
        self.pr(self.terms_env, comments=["zebracorp"])
        (wt / "ci").mkdir(exist_ok=True)
        (wt / "ci" / "public_text_guard.py").write_text(
            "def load_terms_or_fail(*a, **k): return ['x']\ndef check_text(*a): return []\n"
            "def check_ref(*a): return []\ndef check_push(*a, **k): return []\n")
        self.env.sql("UPDATE tasks SET worktree=? WHERE id='T-0001'", (str(wt),))
        self.assert_text_refused(self.judge(), "owner-term")
        src = (REPO / "bin" / "factory").read_text()
        self.assertIn("HOME / PUBLIC_GUARD", src)
        self.assertNotIn("git_repo / PUBLIC_GUARD", src)


class FrozenTests(Base):
    def setUp(self):
        super().setUp()
        self.ok("config", "git_push", "1")

    def pr_repo(self, slug, stale=False):
        """bare <slug>.git with main, a PR branch factory/t-0001 one commit ahead, and (stale) main one commit further."""
        e = self.env
        bare = self.bare(slug)
        wt = e.home / ("wt-" + slug.replace("/", "-"))
        run_git(e.home, "clone", "-q", str(bare), str(wt))
        run_git(wt, "commit", "-q", "--allow-empty", "-m", "base")
        run_git(wt, "push", "-q", "origin", "HEAD:main")
        run_git(wt, "checkout", "-q", "-b", "factory/t-0001")
        run_git(wt, "commit", "-q", "--allow-empty", "-m", "work")
        run_git(wt, "push", "-q", "origin", "HEAD:refs/heads/factory/t-0001")
        run_git(wt, "push", "-q", "origin", "HEAD:refs/pull/1/head")
        head = run_git(wt, "rev-parse", "HEAD")
        if stale:
            run_git(wt, "checkout", "-q", "main")
            run_git(wt, "commit", "-q", "--allow-empty", "-m", "main moved")
            run_git(wt, "push", "-q", "origin", "HEAD:main")
        e.set_gh(repo_url=str(bare))
        return bare, head

    def test_refresh_push_refused_for_frozen_repo(self):
        bare, head = self.pr_repo("acme/old", stale=True)
        self.ok("config", "frozen_repos", "acme/old")
        before = self.refs(bare)
        with self.env.load_factory() as f:
            run_git(self.env.home_dir, "remote", "add", "origin", str(bare))
            r = f.refresh_branch(f.HOME, "factory/t-0001", head, 1, str(bare), "acme/old", "T-0001")
        self.assertEqual(r["status"], "error", r)
        self.assertIn("repo acme/old is frozen (G-021)", r["why"])
        self.assertEqual(self.refs(bare), before)

    def test_refresh_push_proceeds_for_other_repo(self):
        bare, head = self.pr_repo("acme/priv", stale=True)
        self.ok("config", "frozen_repos", "acme/old")
        with self.env.load_factory() as f:
            run_git(self.env.home_dir, "remote", "add", "origin", str(bare))
            r = f.refresh_branch(f.HOME, "factory/t-0001", head, 1, str(bare), "acme/priv", "T-0001")
        self.assertEqual(r["status"], "refreshed", r)
        self.assertNotEqual(run_git(bare, "rev-parse", "refs/heads/factory/t-0001"), head)

    def rb(self, slug):
        bare, head = self.pr_repo(slug)
        run_git(self.env.home_dir, "remote", "add", "origin", str(bare))
        return bare, head

    def test_remote_branch_delete_refused_for_frozen_repo(self):
        bare, head = self.rb("acme/old")
        self.ok("config", "frozen_repos", "acme/old")
        before = self.refs(bare)
        with self.env.load_factory() as f:
            why = f._rb_delete("factory/t-0001", head, con=f.db())
        self.assertEqual(why, "repo acme/old is frozen (G-021)")
        self.assertEqual(self.refs(bare), before)

    def test_remote_branch_delete_proceeds_for_other_repo(self):
        bare, head = self.rb("acme/priv")
        self.ok("config", "frozen_repos", "acme/old")
        with self.env.load_factory() as f:
            why = f._rb_delete("factory/t-0001", head, con=f.db())
        self.assertIsNone(why)
        self.assertNotIn("refs/heads/factory/t-0001", self.refs(bare))

    def judge_setup(self, url, slug):
        e = self.env
        e.seed_project()
        e.seed_goal(project="zz-test-proj")
        e.seed_task(pr_url=url)
        for name, role in (("zz-test-reviewer", "reviewer"), ("zz-test-qa", "qa"), ("zz-test-judge", "judge")):
            e.seed_agent(name, role, goal="G-001", task="T-0001")
        self.ok("config", "judge_ci_wait_s", "0")
        bare, head = self.pr_repo(slug)
        e.head_sha = head
        e.seed_pr(url=url, sha=head, checks=GREEN)
        e.auto_sha = False
        for agent, summary in (("zz-test-reviewer", "ok"), ("zz-test-qa", gates_summary())):
            self.ok("verdict", "T-0001", "--pass", "--sha", head, "--summary", summary, agent=agent)

    def merges(self):
        return [c for c in self.env.calls("gh") if c["argv"][:2] == ["pr", "merge"]]

    def test_gh_merge_refused_for_frozen_repo(self):
        self.judge_setup(OLD_URL, "acme/old")
        self.ok("config", "frozen_repos", "acme/old")
        p = self.env.run("judge", "T-0001", "--merge", "--reason", "zz-test", agent="zz-test-judge")
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn("repo acme/old is frozen (G-021)", p.stderr)
        self.assertEqual(self.merges(), [])

    def test_gh_merge_proceeds_for_other_repo(self):
        self.judge_setup("https://github.com/acme/priv/pull/1", "acme/priv")
        self.ok("config", "frozen_repos", "acme/old")
        p = self.env.run("judge", "T-0001", "--merge", "--reason", "zz-test", agent="zz-test-judge")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(len(self.merges()), 1)


class ConfigKeyTests(Base):
    def test_keys_default_empty_and_listed(self):
        out = self.ok("config").stdout
        for k in ("public_text_repos", "frozen_repos", "scrub_terms_file"):
            self.assertIn(f"{k} = \n", out + "\n")

    def test_keys_settable_by_director_only(self):
        self.env.seed_agent("zz-test-director", "director")
        self.env.seed_agent("zz-test-coder", "coder")
        self.ok("config", "frozen_repos", "acme/old", agent="zz-test-director")
        self.assertIn("frozen_repos = acme/old", self.ok("config").stdout)
        self.assertNotEqual(self.env.run("config", "frozen_repos", "", agent="zz-test-coder").returncode, 0)

    def test_config_refuses_unparsable_list_entry(self):
        self.env.seed_agent("zz-test-director", "director")
        p = self.env.run("config", "frozen_repos", "acme-old", agent="zz-test-director")
        self.assertEqual(p.returncode, 2, p.stderr)

    def test_repo_slug_parses_urls_and_paths(self):
        with self.env.load_factory() as f:
            for u, want in (("https://github.com/Acme/Pub.git", "acme/pub"), ("git@github.com:acme/pub.git", "acme/pub"),
                            ("acme/pub", "acme/pub"), ("/tmp/x/acme/pub.git", "acme/pub"), ("pub", None), ("", None), ("https://github.com/acme/pub.GIT?x#y", "acme/pub")):
                self.assertEqual(f._repo_slug(u), want, u)


if __name__ == "__main__":
    unittest.main()
