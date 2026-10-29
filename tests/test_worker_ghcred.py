"""G-010 T-0651 (R-c): bin/factory spawn wiring for the worker gh credential (config worker_gh_roles).

Fake tokens only, in a temp HOME under a temp FACTORY_HOME; gh and herdr are the harness stubs. Nothing reads a real
token file."""
import json
import os
import shutil
import sys
import unittest
from pathlib import Path

from harness import FACTORY, REPO, STUBS, FactoryTestCase, git

RO, RW = "zz-fake-ro-token-7d41e", "zz-fake-rw-token-9a3b2"
SLUG = "zz-owner/zz-repo"
ROLES = ("coder", "qa", "reviewer", "judge", "cleaner", "pm")
SCRUBBED = ("GIT_CONFIG_PARAMETERS", "GIT_ASKPASS", "SSH_ASKPASS", "GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN")
DANGEROUS = {"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "credential.helper", "GIT_CONFIG_VALUE_0": "!echo pw",
             "GIT_CONFIG_PARAMETERS": "'credential.helper=!echo pw'", "GIT_ASKPASS": "/bin/echo",
             "SSH_ASKPASS": "/bin/echo", "GH_TOKEN": "zz-owner-token", "GITHUB_TOKEN": "zz-owner-token2",
             "GH_ENTERPRISE_TOKEN": "zz-owner-token3"}


def pane_env(argv):
    """{K: V} of the `--env K=V` pairs of one herdr argv."""
    return dict(a.split("=", 1) for i, a in enumerate(argv) if i and argv[i - 1] == "--env")


class GhCredCase(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        self.tdir = e.user_home / ".config" / "factory"
        self.tdir.mkdir(parents=True)
        for name, tok in (("gh-worker-ro-token", RO), ("gh-worker-rw-token", RW)):
            (self.tdir / name).write_text(tok + "\n")
            (self.tdir / name).chmod(0o600)
        e.seed_goal()
        e.seed_team(lead="zz-lead")
        e.seed_agent("zz-test-director", "director")
        self.envlog = e.home / "gh-env.log"  # every gh call: GH_TOKEN|GH_CONFIG_DIR
        (e.stub_bin / "gh").unlink()
        (e.stub_bin / "gh").write_text(f'#!/bin/sh\nprintf "%s|%s\\n" "$GH_TOKEN" "$GH_CONFIG_DIR" >> {self.envlog}\n'
                                       f'exec {sys.executable} {STUBS / "gh"} "$@"\n')
        (e.stub_bin / "gh").chmod(0o755)
        self.ok("config", "worker_gh_repos", SLUG)

    def flag(self, roles):
        self.ok("config", "worker_gh_roles", roles, agent="zz-test-director")

    def spawn(self, role, extra_env=None, tag=""):
        cwd = self.env.home_dir / "worktrees" / f"w-{role}"
        cwd.mkdir(parents=True, exist_ok=True)
        return self.env.run("spawn", role, "--name", f"zz-{role}{tag}", "--goal", "G-001", "--team", "g-001-a", "--cwd", str(cwd),
                            extra_env=extra_env)

    def started(self, role, tag=""):
        """The `--env` map of the pane the spawn created."""
        for c in self.env.calls("herdr"):
            if c["argv"][:2] in (["pane", "split"], ["tab", "create"]) and f"FACTORY_AGENT=zz-{role}{tag}" in c["argv"]:
                return pane_env(c["argv"])
        self.fail(f"no pane was placed for zz-{role}")

    def ghcred_dir(self):
        return self.env.user_home / ".local" / "state" / "factory" / "ghcred"


class FlagOnTests(GhCredCase):
    def setUp(self):
        super().setUp()
        self.flag(",".join(ROLES))

    def test_coder_gets_the_rw_token_file_and_the_ghcred_env(self):
        self.ok_spawn = self.spawn("coder")
        self.assertEqual(self.ok_spawn.returncode, 0, self.ok_spawn.stderr)
        env = self.started("coder")
        agent_dir = self.ghcred_dir() / "zz-coder"
        self.assertEqual(env["GIT_CONFIG_GLOBAL"], str(agent_dir / "gitconfig"))
        self.assertEqual(env["GH_CONFIG_DIR"], str(agent_dir / "gh"))
        self.assertEqual(env["FACTORY_GH_TOKEN_FILE"], str(self.tdir / "gh-worker-rw-token"))
        self.assertTrue(env["PATH"].startswith(str(agent_dir / "bin") + os.pathsep))
        self.assertIn("ghcred.py", (agent_dir / "gitconfig").read_text())

    def test_each_role_gets_the_token_its_config_says(self):
        want = {"coder": "rw", "judge": "rw", "cleaner": "rw", "reviewer": "ro", "qa": "ro", "pm": "ro"}
        for role, kind in want.items():
            p = self.spawn(role)
            self.assertEqual(p.returncode, 0, f"{role}: {p.stderr}")
            self.assertEqual(self.started(role)["FACTORY_GH_TOKEN_FILE"], str(self.tdir / f"gh-worker-{kind}-token"), role)

    def test_gh_rw_roles_moves_a_role_to_the_rw_token(self):
        self.ok("config", "gh_rw_roles", "reviewer", agent="zz-test-director")
        self.spawn("reviewer")
        self.assertEqual(self.started("reviewer")["FACTORY_GH_TOKEN_FILE"], str(self.tdir / "gh-worker-rw-token"))

    def test_no_fake_token_in_herdr_calls_or_events(self):
        for role in ROLES:
            self.spawn(role)
        blob = json.dumps(self.env.calls("herdr")) + json.dumps([dict(r) for r in self.env.sql("SELECT * FROM events")])
        for tok in (RO, RW):
            self.assertNotIn(tok, blob)
        self.assertNotIn(RW, "".join(p.read_text() for p in self.ghcred_dir().rglob("*") if p.is_file()))

    def test_parent_credential_env_is_scrubbed_from_the_pane(self):
        p = self.spawn("coder", extra_env=DANGEROUS)
        self.assertEqual(p.returncode, 0, p.stderr)
        env = self.started("coder")
        self.assertEqual(env["GIT_CONFIG_COUNT"], "0")
        for k in (*SCRUBBED, "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0"):
            self.assertEqual(env.get(k, ""), "", k)

    def test_judge_gh_calls_use_the_rw_token_and_not_the_owner_gh_config(self):
        e = self.env
        self.assertEqual(self.spawn("judge").returncode, 0)
        penv = self.started("judge")
        e.seed_project()
        e.seed_task()
        e.seed_agent("zz-test-judge", "judge", goal="G-001", task="T-0001")
        e.use_git_head()
        e.seed_pr()
        p = e.run("judge", "T-0001", "--merge", "--reason", "zz-test", agent="zz-test-judge",
                  extra_env={k: penv[k] for k in ("PATH", "GIT_CONFIG_GLOBAL", "GH_CONFIG_DIR", "FACTORY_GH_TOKEN_FILE")})
        self.assertEqual(p.returncode, 4, p.stderr)  # no verdicts: refused, but only after it asked gh
        rows = [l.split("|") for l in self.envlog.read_text().splitlines()]
        self.assertTrue(rows, "the judge never called gh")
        for token, cfg_dir in rows:
            self.assertEqual(token, RW)
            self.assertNotEqual(cfg_dir, str(e.user_home / ".config" / "gh"))
            self.assertEqual(cfg_dir, penv["GH_CONFIG_DIR"])


class RefusalTests(GhCredCase):
    def assert_refused(self, role="coder", key=None):
        before = len(self.env.calls("herdr"))
        p = self.spawn(role)
        self.assertEqual(p.returncode, 3, p.stdout + p.stderr)
        self.assertIn("gh-cred", p.stderr)
        if key:
            self.assertIn(key, p.stderr)
        for tok in (RO, RW, "gh-worker"):
            self.assertNotIn(tok, p.stderr)
        self.assertEqual([c for c in self.env.calls("herdr")[before:] if c["argv"][:2] in (["agent", "start"], ["pane", "split"], ["tab", "create"])], [])
        self.assertEqual(self.env.sql("SELECT * FROM agents WHERE name=? AND status='live'", (f"zz-{role}",)), [])
        self.assertEqual([r["entity_id"] for r in self.env.sql("SELECT entity_id FROM events WHERE action='gh-cred-refused'")], [f"zz-{role}"])
        self.assertEqual(len(self.env.sql("SELECT * FROM messages WHERE subject LIKE 'worker gh credential refused%'")), 1)
        return p

    def test_missing_token_file_refuses_with_exit_3(self):
        self.flag("coder")
        (self.tdir / "gh-worker-rw-token").unlink()
        self.assert_refused("coder", "gh_token_rw")

    def test_token_file_with_loose_mode_is_refused(self):
        self.flag("reviewer")
        (self.tdir / "gh-worker-ro-token").chmod(0o644)
        self.assert_refused("reviewer", "gh_token_ro")

    def test_symlinked_token_file_is_refused(self):
        self.flag("coder")
        (self.tdir / "gh-worker-rw-token").rename(self.tdir / "real")
        (self.tdir / "gh-worker-rw-token").symlink_to(self.tdir / "real")
        self.assert_refused("coder", "gh_token_rw")

    def test_token_file_outside_the_config_dir_is_refused_without_naming_it(self):
        self.flag("coder")
        elsewhere = self.env.user_home / "zz-elsewhere-tok"
        elsewhere.write_text(RW + "\n")
        elsewhere.chmod(0o600)
        self.ok("config", "gh_token_rw", str(elsewhere), agent="zz-test-director")
        p = self.assert_refused("coder", "gh_token_rw")
        self.assertNotIn("zz-elsewhere-tok", p.stderr)
        body = self.env.sql("SELECT body FROM messages WHERE subject LIKE 'worker gh credential refused%'")[0]["body"]
        self.assertNotIn("zz-elsewhere-tok", body)

    def test_no_repository_refuses(self):
        self.flag("coder")
        self.env.sql("DELETE FROM config WHERE key='worker_gh_repos'")
        self.ok("config", "factory_remote_url", "https://example.invalid/x", agent="zz-test-director")
        self.assert_refused("coder", "worker_gh_repos")

    def test_no_gh_refuses(self):
        self.flag("coder")
        only = self.env.home / "only-bin"  # the multiplexer stub and git, no gh anywhere on the PATH
        only.mkdir()
        (only / "herdr").symlink_to(self.env.stub_bin / "herdr")
        (only / "git").symlink_to(shutil.which("git"))
        (only / "python3").symlink_to(sys.executable)  # the stub's shebang is env python3
        p = self.env.run("spawn", "coder", "--name", "zz-coder", "--goal", "G-001", "--team", "g-001-a",
                         "--cwd", str(self.env.home_dir), extra_env={"PATH": str(only)})
        self.assertEqual(p.returncode, 3, p.stdout + p.stderr)
        self.assertIn("gh-cred", p.stderr)
        self.assertIn("no gh on the PATH", p.stderr)

    def test_a_refused_queued_row_is_cancelled_by_the_tick(self):
        self.env.set_herdr(fail=True)
        self.assertIn("QUEUED", self.spawn("coder").stdout)
        self.env.set_herdr(fail=False)
        self.flag("coder")
        (self.tdir / "gh-worker-rw-token").unlink()
        starts = len([c for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "start"]])
        self.ok("tick")
        self.assertEqual(self.env.sql("SELECT status FROM agents WHERE name='zz-coder'")[0]["status"], "cancelled")
        self.assertEqual(len([c for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "start"]]), starts)
        ev = [json.loads(r["detail"]) for r in self.env.sql("SELECT detail FROM events WHERE action='dequeued'")]
        self.assertEqual([x["by"] for x in ev], ["gh-cred-preflight"])
        self.assertEqual(len(self.env.sql("SELECT * FROM events WHERE action='gh-cred-refused'")), 1)
        msg = self.env.sql("SELECT body FROM messages WHERE subject LIKE 'gh-cred refused queued spawn%'")
        self.assertEqual(len(msg), 1)
        self.assertNotIn("gh-worker", msg[0]["body"])

    def test_config_rejects_a_non_worker_role(self):
        p = self.refused(2, "config", "worker_gh_roles", "coder,boss", agent="zz-test-director")
        self.assertIn("boss", p.stderr)


class FlagOffTests(GhCredCase):
    def test_flag_off_adds_no_env_and_creates_nothing(self):
        for tag, value in (("", None), ("-b", "")):  # never set, and explicitly empty
            if value is not None:
                self.flag(value)
            for role in ("coder", "judge", "cleaner"):
                p = self.spawn(role, extra_env=DANGEROUS, tag=tag)
                self.assertEqual(p.returncode, 0, p.stderr)
                self.assertEqual(set(self.started(role, tag)), {"FACTORY_AGENT", "FACTORY_ROLE", "FACTORY_HOME"}, role)
        self.assertFalse(self.ghcred_dir().exists())

    def test_flag_off_lets_missing_tokens_spawn(self):
        for p in self.tdir.iterdir():
            p.unlink()
        self.assertEqual(self.spawn("coder").returncode, 0)

    def test_a_role_not_listed_is_untouched(self):
        self.flag("reviewer")
        self.spawn("coder", extra_env=DANGEROUS)
        self.assertEqual(set(self.started("coder")), {"FACTORY_AGENT", "FACTORY_ROLE", "FACTORY_HOME"})


class SandboxedTests(GhCredCase):
    def test_a_sandboxed_spawn_creates_no_ghcred_dir(self):
        self.flag("reviewer")
        self.ok("config", "sandbox_roles", "reviewer", agent="zz-test-director")
        # sandbox preflight fails first (no worker oauth token): the point is that ghcred never ran for it
        p = self.spawn("reviewer")
        self.assertIn("sandbox refused", p.stderr)
        self.assertNotIn("gh-cred", p.stderr)
        self.assertFalse(self.ghcred_dir().exists())


class CleanRemoteBranchTests(GhCredCase):
    def test_clean_remote_branch_push_runs_with_the_ghcred_gitconfig(self):
        e = self.env
        origin = e.home / "origin.git"
        repo = e.home_dir
        git("init", "-q", "--bare", "-b", "main", str(origin), cwd=e.home)
        git("remote", "add", "origin", str(origin), cwd=repo)
        (repo / "README").write_text("x\n")
        git("add", "README", cwd=repo)
        git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "init", cwd=repo)
        git("push", "-q", "origin", "main", cwd=repo)
        git("checkout", "-q", "-b", "factory/t-0007", cwd=repo)
        (repo / "f").write_text("f")
        git("add", "f", cwd=repo)
        git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "f", cwd=repo)
        sha = git("rev-parse", "HEAD", cwd=repo)
        git("push", "-q", "origin", "HEAD:refs/heads/factory/t-0007", cwd=repo)
        git("checkout", "-q", "main", cwd=repo)
        git("branch", "-q", "-D", "factory/t-0007", cwd=repo)
        git("--git-dir", str(origin), "update-ref", "refs/pull/1/head", sha, cwd=repo)
        pr = {"number": 1, "state": "MERGED", "headRefName": "factory/t-0007", "headRefOid": sha, "isCrossRepository": False}
        st = json.loads(e.gh_state.read_text())
        st["pr_list"] = [pr]
        e.gh_state.write_text(json.dumps(st))
        e.seed_project("zz-test-proj")
        e.seed_agent("zz-test-cleaner", "cleaner", goal="G-001")
        self.flag("cleaner")
        self.assertEqual(self.spawn("cleaner").returncode, 0)
        penv = self.started("cleaner")
        gitlog = e.home / "git-env.log"  # wraps the real git: records which gitconfig each call ran with
        real_git = next(d for d in os.environ["PATH"].split(os.pathsep) if (Path(d) / "git").exists())
        (e.stub_bin / "git").write_text(f'#!/bin/sh\necho "$GIT_CONFIG_GLOBAL" >> {gitlog}\nexec {real_git}/git "$@"\n')
        (e.stub_bin / "git").chmod(0o755)
        p = e.run("clean", "--remote-branch", "factory/t-0007", agent="zz-test-cleaner",
                  extra_env={k: penv[k] for k in ("PATH", "GIT_CONFIG_GLOBAL", "GH_CONFIG_DIR", "FACTORY_GH_TOKEN_FILE")})
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("deleted", p.stdout)
        used = set(gitlog.read_text().split())
        self.assertEqual(used, {penv["GIT_CONFIG_GLOBAL"]})
        self.assertIn("git-credential", Path(penv["GIT_CONFIG_GLOBAL"]).read_text())


if __name__ == "__main__":
    unittest.main()
