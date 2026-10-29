import argparse
import json
import os
import re
import subprocess
import unittest
from pathlib import Path
from unittest import mock

from harness import FactoryTestCase, gates_summary

GREEN = [{"name": "unittest", "status": "COMPLETED", "conclusion": "SUCCESS"}]


def git(cwd, *args, check=True):
    p = subprocess.run(["git", *args], cwd=cwd, capture_output=True, text=True)
    if check and p.returncode != 0:
        raise AssertionError(f"git {args}: {p.stderr}")
    return p.stdout.strip()


class VerdictCarryTests(FactoryTestCase):
    """`factory judge` accepts reviewer/QA PASS verdicts recorded at an older head across pure origin/main merges."""

    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_project()
        e.seed_goal(project="zz-test-proj")
        e.seed_task()
        for name, role in (("zz-test-reviewer", "reviewer"), ("zz-test-qa", "qa"), ("zz-test-judge", "judge")):
            e.seed_agent(name, role, goal="G-001", task="T-0001")
        self.remote = e.home / "origin.git"
        self.wt = e.home / "pr-wt"
        git(e.home, "init", "-q", "--bare", "-b", "main", str(self.remote))
        git(e.home, "clone", "-q", str(self.remote), str(self.wt))
        self.commit("base.txt", "line1\nline2\nline3\n", "base")
        git(self.wt, "push", "-q", "origin", "HEAD:main")
        git(self.wt, "checkout", "-q", "-b", "feature")
        self.commit("feature.txt", "f1\n", "feature work")
        e.sql("UPDATE tasks SET worktree=? WHERE id='T-0001'", (str(self.wt),))
        e.set_gh(repo_url=str(self.remote))
        self.x = self.publish()
        self.record_verdicts(self.x)

    # -- helpers ---------------------------------------------------------
    def commit(self, name, text, msg):
        (self.wt / name).write_text(text)
        git(self.wt, "add", name)
        git(self.wt, "commit", "-q", "-m", msg)

    def publish(self):
        """Push the current branch to the PR ref in the bare remote and point the gh stub at it."""
        git(self.wt, "push", "-q", "-f", "origin", "HEAD:refs/pull/1/head")
        sha = git(self.wt, "rev-parse", "HEAD")
        self.env.seed_pr(sha=sha, checks=GREEN)
        return sha

    def record_verdicts(self, sha):
        self.env.seed_pr(sha=sha, checks=GREEN)
        self.ok("verdict", "T-0001", "--pass", "--summary", "ok", agent="zz-test-reviewer")
        self.ok("verdict", "T-0001", "--pass", "--summary", gates_summary(), agent="zz-test-qa")

    def main_commit(self, name="main.txt", text="m\n", msg="main moves"):
        """A commit on origin/main made from a scratch clone."""
        other = self.env.home / "other"
        if not other.exists():
            git(self.env.home, "clone", "-q", str(self.remote), str(other))
        git(other, "pull", "-q", "origin", "main")
        (other / name).write_text(text)
        git(other, "add", name)
        git(other, "commit", "-q", "-m", msg)
        git(other, "push", "-q", "origin", "HEAD:main")

    def merge_main(self, *extra):
        git(self.wt, "fetch", "-q", "origin", "main")
        git(self.wt, "merge", "-q", "--no-edit", *extra, "origin/main")

    def judge(self):
        return self.env.run("judge", "T-0001", "--merge", "--reason", "zz-test", agent="zz-test-judge")

    def merge_calls(self):
        return [c for c in self.env.calls("gh") if c["argv"][:2] == ["pr", "merge"]]

    def carried_events(self):
        return [json.loads(r["detail"]) for r in self.env.sql("SELECT detail FROM events WHERE action='verdict-carried'")]

    def assert_refused(self, why):
        p = self.judge()
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn("reviewer PASS is for " + self.x[:8], p.stderr)
        self.assertIn("qa PASS is for " + self.x[:8], p.stderr)
        self.assertIn(why, p.stderr)
        self.assertEqual(self.merge_calls(), [])
        self.assertEqual(self.carried_events(), [])

    # -- accepted --------------------------------------------------------
    def test_carry_same_head_unchanged(self):
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(self.carried_events(), [])

    def test_carry_one_pure_main_merge(self):
        self.main_commit()
        self.merge_main()
        y = self.publish()
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("carried", p.stdout)
        main = git(self.wt, "rev-parse", "origin/main")
        evs = self.carried_events()
        self.assertEqual(sorted(e["role"] for e in evs), ["qa", "reviewer"])
        for e in evs:
            self.assertEqual((e["from"], e["to"], e["main"], e["merges"]), (self.x, y, main, [y]))
        calls = self.merge_calls()
        self.assertEqual(len(calls), 1)
        self.assertEqual(calls[0]["argv"][calls[0]["argv"].index("--match-head-commit") + 1], y)
        self.assertEqual(self.env.sql("SELECT status FROM tasks WHERE id='T-0001'")[0]["status"], "done")

    def test_carry_two_successive_main_merges(self):
        self.main_commit("m1.txt")
        self.merge_main()
        m1 = git(self.wt, "rev-parse", "HEAD")
        self.main_commit("m2.txt")
        self.merge_main()
        y = self.publish()
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.carried_events()[0]["merges"], [y, m1])
        self.assertEqual(len(self.merge_calls()), 1)

    # -- refused ---------------------------------------------------------
    def test_refuse_manual_edit(self):
        self.main_commit()
        self.merge_main()
        (self.wt / "feature.txt").write_text("sneaky\n")
        git(self.wt, "commit", "-q", "-a", "--amend", "--no-edit")
        self.publish()
        self.assert_refused("not a pure main merge: tree differs at")

    def test_refuse_conflict_resolved_merge(self):
        self.commit("base.txt", "line1\nbranch\nline3\n", "branch edit")
        self.x = self.publish()
        self.record_verdicts(self.x)
        self.main_commit("base.txt", "line1\nmain\nline3\n")
        git(self.wt, "fetch", "-q", "origin", "main")
        self.assertNotEqual(subprocess.run(["git", "merge", "--no-edit", "origin/main"], cwd=self.wt,
                                           capture_output=True).returncode, 0)
        (self.wt / "base.txt").write_text("line1\nresolved\nline3\n")
        git(self.wt, "commit", "-q", "-a", "--no-edit")
        self.publish()
        self.assert_refused("not a pure main merge: tree differs at")

    def test_refuse_x_not_first_parent(self):
        # main merged with the parents swapped: x is the second parent of the head
        self.main_commit()
        git(self.wt, "fetch", "-q", "origin", "main")
        git(self.wt, "checkout", "-q", "-b", "swap", "origin/main")
        git(self.wt, "merge", "-q", "--no-edit", "feature")
        self.publish()
        self.assert_refused("not a pure main merge: second parent of")

    def test_refuse_second_parent_off_main(self):
        git(self.wt, "checkout", "-q", "-b", "other-feature", "origin/main")
        self.commit("other.txt", "o\n", "other feature")
        git(self.wt, "checkout", "-q", "feature")
        git(self.wt, "merge", "-q", "--no-edit", "other-feature")
        self.publish()
        self.assert_refused("second parent of")

    def test_refuse_code_commit_after_x(self):
        self.commit("more.txt", "x\n", "real code")
        self.publish()
        self.assert_refused("is not a two-parent merge")

    def test_refuse_commit_on_top_of_pure_merge(self):
        self.main_commit()
        self.merge_main()
        self.commit("more.txt", "x\n", "real code after the merge")
        self.publish()
        self.assert_refused("is not a two-parent merge")

    def test_refuse_unrelated_head(self):
        git(self.wt, "checkout", "-q", "--orphan", "unrelated")
        self.commit("u.txt", "u\n", "unrelated")
        self.publish()
        self.assert_refused("is not a two-parent merge")

    def test_refuse_when_fetch_fails(self):
        self.commit("more.txt", "x\n", "real code")
        self.publish()
        self.env.set_gh(repo_url=str(self.env.home / "gone.git"))
        self.assert_refused("cannot verify, fetch of main failed")

    def test_refuse_replace_ref_posing_as_pure_merge(self):
        # Z is a genuine pure main merge on top of x; `evil` (a real code commit) is replaced by Z locally
        self.main_commit()
        git(self.wt, "fetch", "-q", "origin", "main")
        git(self.wt, "checkout", "-q", "-b", "fake", self.x)
        git(self.wt, "merge", "-q", "--no-edit", "origin/main")
        z = git(self.wt, "rev-parse", "HEAD")
        git(self.wt, "checkout", "-q", "-b", "evil", self.x)
        self.commit("evil.txt", "evil\n", "real code")
        evil = self.publish()
        git(self.wt, "replace", evil, z)
        # sanity: with replace refs honoured the evil commit looks like a two-parent merge
        self.assertEqual(len(git(self.wt, "rev-list", "--parents", "-n", "1", evil).split()), 3)
        self.assert_refused("is not a two-parent merge")

    def test_refuse_tampered_remote_url(self):
        # origin (and an insteadOf rewrite of the real remote) point at a fake remote whose main holds the evil head
        self.commit("more.txt", "x\n", "real code")
        self.publish()
        fake = self.env.home / "fake.git"
        git(self.env.home, "clone", "-q", "--bare", str(self.remote), str(fake))
        git(self.wt, "push", "-q", "-f", str(fake), "HEAD:refs/heads/main", "HEAD:refs/pull/1/head")
        git(self.wt, "config", "remote.origin.url", str(fake))
        git(self.wt, "config", f"url.{fake}.insteadOf", str(self.remote))
        self.refused_config("url.")  # the allow-list refuses the insteadOf key before the carry check is reached

    def test_refuse_octopus_merge(self):
        self.main_commit("m1.txt")
        self.main_commit("m2.txt")
        git(self.wt, "fetch", "-q", "origin", "main")
        git(self.wt, "checkout", "-q", "-b", "side", "origin/main~2")
        self.commit("side.txt", "s\n", "side")
        git(self.wt, "checkout", "-q", "feature")
        git(self.wt, "merge", "-q", "--no-edit", "side", "origin/main")
        self.assertEqual(len(git(self.wt, "rev-list", "--parents", "-n", "1", "HEAD").split()), 4)
        self.publish()
        self.assert_refused("is not a two-parent merge")

    def test_refuse_more_than_20_commits(self):
        for i in range(21):
            self.main_commit(f"m{i}.txt")
            self.merge_main()
        y = self.publish()
        self.assertEqual(git(self.wt, "rev-list", "--count", "--first-parent", f"{self.x}..{y}"), "21")
        self.assert_refused("within 20 commits")

    def test_accept_20_merges_at_the_bound(self):
        for i in range(20):
            self.main_commit(f"m{i}.txt")
            self.merge_main()
        self.publish()
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(len(self.carried_events()[0]["merges"]), 20)

    def test_no_carried_event_when_gh_merge_fails(self):
        self.main_commit()
        self.merge_main()
        self.publish()
        self.env.set_gh(merge_exit=1, merge_stderr="boom")
        p = self.judge()
        self.assertEqual(p.returncode, 5, p.stdout + p.stderr)
        self.assertEqual(self.carried_events(), [])
        self.assertNotIn("carried", p.stdout)

    def test_helper_only_fetches(self):
        self.main_commit()
        self.merge_main()
        self.publish()
        before = (git(self.wt, "rev-parse", "HEAD"), git(self.wt, "branch", "--show-current"), git(self.wt, "status", "--short"))
        self.assertEqual(self.judge().returncode, 0)
        after = (git(self.wt, "rev-parse", "HEAD"), git(self.wt, "branch", "--show-current"), git(self.wt, "status", "--short"))
        self.assertEqual(before, after)

    # -- hardened git: hooks, fsmonitor, drivers, filters, attributes, gh timeout ----------------
    def pure_merge(self):
        self.main_commit()
        self.merge_main()
        self.publish()

    def plant_hook(self, name="reference-transaction"):
        marker = self.env.home / "hook-ran"
        hook = self.wt / ".git" / "hooks" / name
        hook.write_text(f"#!/bin/sh\necho {name} >> '{marker}'\n")
        hook.chmod(0o755)
        return marker

    def refused_config(self, key_fragment):
        p = self.judge()
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn("git config", p.stderr)
        self.assertIn(key_fragment, p.stderr)
        self.assertEqual(self.merge_calls(), [])
        self.assertEqual(self.carried_events(), [])
        ev = self.env.sql("SELECT detail FROM events WHERE action='git-config-refused'")
        self.assertEqual(len(ev), 1)
        self.assertIn(key_fragment, json.loads(ev[0]["detail"])["keys"][0])
        self.assertEqual(self.env.sql("SELECT status FROM tasks WHERE id='T-0001'")[0]["status"], "in_review")

    def test_planted_hooks_do_not_run_in_carry_and_merge(self):
        self.pure_merge()
        marker = self.plant_hook()
        other = self.plant_hook("post-merge")
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("carried", p.stdout)
        self.assertEqual(len(self.merge_calls()), 1)
        self.assertFalse(marker.exists(), "reference-transaction hook ran")
        self.assertFalse(other.exists())

    def test_planted_fsmonitor_does_not_run(self):
        self.pure_merge()
        marker = self.env.home / "fsmonitor-ran"
        fsm = self.env.home / "fsm.sh"
        fsm.write_text(f"#!/bin/sh\necho ran >> '{marker}'\n")
        fsm.chmod(0o755)
        git(self.wt, "config", "core.fsmonitor", str(fsm))
        self.refused_config("core.fsmonitor")  # not on the allow-list
        self.assertFalse(marker.exists(), "core.fsmonitor command ran")
        with self.env.load_factory() as mod:  # and the -c override alone also keeps it from running
            self.assertEqual(mod.git_run(self.wt, "status").returncode, 0)
        self.assertFalse(marker.exists(), "core.fsmonitor command ran under git_run")

    def test_refuse_merge_driver_in_config(self):
        git(self.wt, "config", "merge.evil.driver", "true")
        self.refused_config("merge.evil.driver")

    def test_refuse_filter_keys_in_config(self):
        for key in ("smudge", "clean", "process", "required"):
            with self.subTest(key=key):
                self.env.sql("DELETE FROM events WHERE action='git-config-refused'")
                git(self.wt, "config", f"filter.x.{key}", "true")
                self.refused_config(f"filter.x.{key}")
                git(self.wt, "config", "--unset", f"filter.x.{key}")

    def test_refuse_attributes_file_and_info_attributes(self):
        git(self.wt, "config", "core.attributesFile", "/dev/null")
        self.refused_config("core.attributesfile")
        git(self.wt, "config", "--unset", "core.attributesFile")
        self.env.sql("DELETE FROM events WHERE action='git-config-refused'")
        info = self.wt / ".git" / "info"
        info.mkdir(exist_ok=True)
        (info / "attributes").write_text("* merge=evil\n")
        self.refused_config("info/attributes")

    def test_empty_info_attributes_is_fine(self):
        info = self.wt / ".git" / "info"
        info.mkdir(exist_ok=True)
        (info / "attributes").write_text("")
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)

    def test_refuse_allowlist_violations(self):
        """Anything outside the repo-scope allow-list refuses the judge (exit 4, no merge), whatever the key does."""
        cases = (("core.gitProxy", "/bin/true", "core.gitproxy"), ("merge.x.driver", "true", "merge.x.driver"),
                 ("remote.origin.url", "git://example.invalid/f.git", "remote.origin.url=git://"),
                 ("url.git://example.invalid/.insteadOf", "https://github.com/", "url.git://"),
                 ("includeIf.gitdir:%s/.git/worktrees/**.path" % self.wt, "inc.cfg", "includeif.gitdir:"),
                 ("user.email", "someone" + "@" + "example.invalid", "user.email"))
        for key, val, frag in cases:
            with self.subTest(key=key):
                self.env.sql("DELETE FROM events WHERE action='git-config-refused'")
                old = git(self.wt, "config", "--get", "remote.origin.url") if key == "remote.origin.url" else None
                git(self.wt, "config", key, val)
                self.refused_config(frag)
                if old:
                    git(self.wt, "config", key, old)
                else:
                    git(self.wt, "config", "--unset", key)
        p = self.judge()  # back to the allow-listed keys: merges
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)

    def test_refusal_director_message_at_most_hourly(self):
        git(self.wt, "config", "merge.evil.driver", "true")
        self.judge()
        self.judge()
        n = self.env.sql("SELECT count(*) c FROM messages WHERE recipient='director' AND subject LIKE 'git config refused%'")
        self.assertEqual(n[0]["c"], 1)

    # -- includes, other keys, global scope, attributes source ----------------------------------
    def include_cfg(self, body):
        (self.wt / ".git" / "inc.cfg").write_text(body)

    def plant_include(self, via):
        if via == "path":
            git(self.wt, "config", "include.path", "inc.cfg")
        else:  # includeIf.gitdir: matches this repo's git dir
            git(self.wt, "config", f"includeIf.gitdir:{self.wt}/.git.path", "inc.cfg")

    def test_refuse_driver_filter_attributes_file_via_include(self):
        bodies = {"merge.evil.driver": '[merge "evil"]\n\tdriver = true\n',
                  "filter.x.smudge": '[filter "x"]\n\tsmudge = true\n',
                  "core.attributesfile": "[core]\n\tattributesFile = /dev/null\n"}
        for via in ("path", "if"):
            for key, body in bodies.items():
                with self.subTest(via=via, key=key):
                    self.env.sql("DELETE FROM events WHERE action='git-config-refused'")
                    self.include_cfg(body)
                    self.plant_include(via)
                    self.refused_config("include.path" if via == "path" else "includeif.gitdir:")
                    git(self.wt, "config", "--remove-section", "include" if via == "path" else f"includeIf.gitdir:{self.wt}/.git")

    def test_refuse_credential_helper_gpg_program_ssh_command_in_repo_config(self):
        for key, val in (("credential.helper", "!touch pwned"), ("credential.https://example.com.helper", "!x"),
                         ("gpg.program", "/bin/true"), ("core.sshCommand", "/bin/true")):
            with self.subTest(key=key):
                self.env.sql("DELETE FROM events WHERE action='git-config-refused'")
                git(self.wt, "config", key, val)
                self.refused_config(key.lower() if key.startswith("credential.https") is False else "helper")
                git(self.wt, "config", "--unset", key)

    def test_global_config_is_out_of_scope_but_xdg_attributes_refuse(self):
        """Known limit (G-010): ~/.gitconfig is not examined. A non-empty ~/.config/git/attributes still refuses."""
        uh = self.env.user_home
        (uh / ".config" / "git").mkdir(parents=True)
        (uh / ".config" / "git" / "attributes").write_text("* merge=evil\n")
        self.refused_config("~/.config/git/attributes")
        (uh / ".config" / "git" / "attributes").unlink()
        (uh / ".gitconfig").write_text('[merge "g"]\n\tdriver = true\n')
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)

    def test_global_credential_helper_is_fine(self):
        (self.env.user_home / ".gitconfig").write_text('[credential "https://github.com"]\n\thelper = !gh auth git-credential\n')
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)

    def test_carry_merge_tree_ignores_worktree_gitattributes(self):
        marker = self.env.home / "driver-ran"
        drv = self.env.home / "drv.sh"
        drv.write_text(f"#!/bin/sh\necho ran >> '{marker}'\ncp \"$2\" \"$1\"\n")
        drv.chmod(0o755)
        self.commit("base.txt", "line1 f\nline2\nline3\n", "feature edits base")
        self.main_commit("base.txt", "line1\nline2\nline3 m\n", "main edits base")
        git(self.wt, "fetch", "-q", "origin", "main")
        (self.wt / ".gitattributes").write_text("* merge=evil\n")
        with self.env.load_factory() as mod:
            for env, expect_run in ((mod._carry_env(), False), ({k: v for k, v in mod._carry_env().items() if k != "GIT_ATTR_SOURCE"}, True)):
                marker.unlink(missing_ok=True)
                mod._git_rc(["-c", f"merge.evil.driver={drv} %A %B", "merge-tree", "--write-tree", "HEAD", "origin/main"],
                            repo=self.wt, env=env)
                self.assertEqual(marker.exists(), expect_run, "control must fire, hardened env must not")
            self.assertEqual(mod._carry_env()["GIT_ATTR_SOURCE"], "4b825dc642cb6eb9a060e54bf8d69288fbee4904")

    def test_gh_api_timeout_is_a_refusal_not_a_traceback(self):
        self.pure_merge()
        self.env.set_gh(api=[{"match": ["commits/main"], "sleep": 2, "out": "x"}])
        with self.env.load_factory() as mod:
            mod.GH_API_TIMEOUT = 0.2
            ok, why, main_sha, merges = mod.verdict_carries(
                self.wt, self.x, git(self.wt, "rev-parse", "HEAD"), 1, str(self.remote), "o/r")
        self.assertFalse(ok)
        self.assertIn("gh api for main timed out", why)
        self.assertEqual((main_sha, merges), (None, []))

    def test_gh_api_timeout_in_judge_gives_judge_blocked(self):
        self.pure_merge()
        self.env.set_gh(api=[{"match": ["commits/main"], "sleep": 2, "out": "x"}])
        old = os.getcwd()
        os.chdir(self.env.home)
        self.addCleanup(os.chdir, old)
        with self.env.load_factory() as mod, mock.patch.dict(os.environ, {"FACTORY_AGENT": "zz-test-judge", "FACTORY_ROLE": "judge"}):
            mod.GH_API_TIMEOUT = 0.2
            args = argparse.Namespace(task="T-0001", reject=False, merge=True, reason="zz-test")
            with self.assertRaises(SystemExit) as cm:
                mod.cmd_judge(args)
        self.assertEqual(cm.exception.code, 4)
        ev = self.env.sql("SELECT detail FROM events WHERE action='judge-blocked'")
        self.assertEqual(len(ev), 1)
        self.assertIn("gh api for main timed out", ev[0]["detail"])
        self.assertEqual(self.merge_calls(), [])

    def test_missing_gh_credentials_refuse_fast(self):
        self.pure_merge()
        self.env.set_gh(api=[{"match": ["commits/main"], "exit": 4, "err": "gh: To get started with GitHub CLI, run: gh auth login"}])
        self.assert_refused_carry("does not match GitHub's")

    def assert_refused_carry(self, why):
        p = self.judge()
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn(why, p.stderr)
        self.assertEqual(self.merge_calls(), [])

    def test_file_protocol_only_in_temp_home(self):
        with self.env.load_factory() as mod:
            self.assertEqual(mod.git_file_protocol(), "always")
            self.assertIn("protocol.file.allow=always", mod.git_argv(self.wt, "status"))
            mod.HOME = Path(mod.LIVE_HOME)
            self.assertEqual(mod.git_file_protocol(), "never")
            p = mod.git_run(self.wt, "ls-remote", str(self.remote))  # repo is the temp clone; only the HOME rule is "live"
            self.assertNotEqual(p.returncode, 0)
            self.assertIn("not allowed", p.stderr)
            self.assertEqual(mod.git_run(self.wt, "ls-remote", str(self.remote)).returncode, p.returncode)
        with self.env.load_factory() as mod:
            self.assertEqual(mod.git_run(self.wt, "ls-remote", str(self.remote)).returncode, 0)

    def test_linked_worktrees_and_worktree_scope_config(self):
        linked = self.env.home / "linked"
        git(self.wt, "worktree", "add", "-q", "-b", "other", str(linked))
        with self.env.load_factory() as mod:
            self.assertEqual(mod.git_config_refusal(linked), [])  # several worktrees, no worktreeConfig: not an error
            git(self.wt, "config", "extensions.worktreeConfig", "true")
            self.assertEqual(mod.git_config_refusal(linked), ["extensions.worktreeconfig"])  # only false or absent
            git(self.wt, "config", "extensions.worktreeConfig", "false")
            self.assertEqual(mod.git_config_refusal(linked), [])
            git(self.wt, "config", "extensions.worktreeConfig", "true")
            git(linked, "config", "--worktree", "merge.evil.driver", "true")
            self.assertIn("merge.evil.driver", mod.git_config_refusal(linked))
            self.assertIn("merge.evil.driver", mod.git_config_refusal(self.wt))  # every worktrees/*/config.worktree is read

    def test_not_a_repo_is_clean_but_other_git_errors_refuse(self):
        with self.env.load_factory() as mod:
            plain = self.env.home / "plain"
            plain.mkdir()
            self.assertEqual(mod.git_config_refusal(plain), [])
            git(self.wt, "config", "extensions.worktreeConfig", "maybe")
            self.assertTrue(mod.git_config_refusal(self.wt))

    def test_no_direct_git_calls_outside_helper(self):
        src = (Path(__file__).resolve().parent.parent / "bin" / "factory").read_text().splitlines()
        direct = [i + 1 for i, l in enumerate(src) if re.search(r"""[\[(]\s*["']git["']\s*,""", l)]  # an argv starting with git
        self.assertEqual(len(direct), 1, direct)  # git_argv itself
        self.assertIn("def git_argv", src[direct[0] - 2])


if __name__ == "__main__":
    unittest.main()
