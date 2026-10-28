import json
import shutil
import subprocess
import unittest
from pathlib import Path

from harness import FactoryTestCase, git

SAFE = ("-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false")


class TickGitHardeningTests(FactoryTestCase):
    """The tick (sync + export + state branch) ignores planted hooks/fsmonitor and refuses planted drivers,
    filters and attributes, without any git action."""

    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_project()
        e.seed_goal(project="zz-test-proj", title="zz-test hardening goal")
        e.seed_agent("zz-test-director", "director")
        self.h = e.home_dir
        self.bare = e.home / "origin.git"
        git("init", "-q", "--bare", "-b", "main", str(self.bare), cwd=e.home)
        git("remote", "add", "origin", str(self.bare), cwd=self.h)
        git("commit", "-q", "--allow-empty", "-m", "base", cwd=self.h)
        git("push", "-q", "-u", "origin", "main", cwd=self.h)
        self.ok("config", "git_push", "1")
        self.ok("config", "push_interval_s", "0")
        self.marker = e.home / "marker"
        self.clean_config = (self.h / ".git" / "config").read_text()  # the live-like allow-list keys only

    def g(self, *args):
        """git in the live-shaped repo without running anything the test planted."""
        return git(*SAFE, *args, cwd=self.h)

    def merge_code(self):
        other = self.env.home / "other"
        if not other.exists():
            git("clone", "-q", "-b", "main", str(self.bare), str(other), cwd=self.env.home)
        self.n = getattr(self, "n", 0) + 1
        (other / f"code{self.n}.txt").write_text("code\n")
        git("add", ".", cwd=other)
        git("commit", "-q", "-m", "code", cwd=other)
        git("push", "-q", "origin", "main", cwd=other)
        return git("rev-parse", "HEAD", cwd=other)

    def script(self, name):
        p = self.env.home / name
        p.write_text(f"#!/bin/sh\necho ran >> '{self.marker}'\n")
        p.chmod(0o755)
        return p

    def refused_events(self):
        return [json.loads(r["detail"]) for r in self.env.sql("SELECT detail FROM events WHERE action='git-config-refused'")]

    def refusal_msgs(self):
        return self.env.sql("SELECT subject FROM messages WHERE subject LIKE 'git config refused%'")

    # -- planted code does not run ----------------------------------------------------------
    def test_reference_transaction_hook_not_run_by_tick(self):
        hook = self.h / ".git" / "hooks" / "reference-transaction"
        hook.write_text(f"#!/bin/sh\necho ran >> '{self.marker}'\n")
        hook.chmod(0o755)
        new = self.merge_code()
        self.ok("plan", "G-001", "zz-test plan", agent="zz-test-director")
        self.ok("tick")
        self.assertEqual(self.g("rev-parse", "main"), new)  # fetch + ff-merge + state commit + push all ran
        self.assertTrue(self.g("rev-parse", "refs/heads/state"))
        self.assertFalse(self.marker.exists(), "reference-transaction hook ran")

    def test_fsmonitor_config_is_refused_and_overridden(self):
        git("config", "core.fsmonitor", str(self.script("fsm.sh")), cwd=self.h)
        self.merge_code()
        self.ok("tick")
        self.assertFalse(self.marker.exists(), "core.fsmonitor command ran")
        self.assertEqual(len(self.refused_events()), 1)  # not on the allow-list
        with self.env.load_factory() as mod:  # and even without the guard the -c override keeps it off
            self.assertEqual(mod.git_run(self.h, "status").returncode, 0)
        self.assertFalse(self.marker.exists(), "core.fsmonitor command ran under git_run")

    def test_git_protocol_and_proxy_not_executed(self):
        """(a) a git:// remote plus core.gitProxy: the protocol allow-list stops the fetch before the proxy runs."""
        git("config", "remote.origin.url", "git://example.invalid/factory.git", cwd=self.h)
        git("config", "core.gitProxy", str(self.script("p.sh")), cwd=self.h)
        git("config", "url.git://example.invalid/.insteadOf", "https://example.invalid/", cwd=self.h)
        with self.env.load_factory() as mod:
            for args in (("fetch", "origin"), ("ls-remote", "https://example.invalid/x.git")):
                p = mod.git_run(self.h, *args)
                self.assertNotEqual(p.returncode, 0, args)
                self.assertIn("not allowed", p.stderr)
            argv = mod.git_argv(self.h, "status")
            env = mod.gh_git_env()
        self.assertFalse(self.marker.exists(), "core.gitProxy ran")
        self.assertIn("protocol.allow=never", argv)
        self.assertIn("protocol.https.allow=always", argv)
        vals = {env[f"GIT_CONFIG_KEY_{i}"]: env[f"GIT_CONFIG_VALUE_{i}"] for i in range(int(env["GIT_CONFIG_COUNT"]))}
        self.assertEqual((vals["protocol.allow"], vals["protocol.https.allow"], env["GIT_CONFIG_NOSYSTEM"]), ("never", "always", "1"))

    def test_hook_runs_without_the_override(self):
        """Control: the planted hook really fires for a plain git call, so the tests above prove something."""
        hook = self.h / ".git" / "hooks" / "reference-transaction"
        hook.write_text(f"#!/bin/sh\necho ran >> '{self.marker}'\n")
        hook.chmod(0o755)
        git("update-ref", "refs/heads/zz", "HEAD", cwd=self.h)
        self.assertTrue(self.marker.exists())

    # -- planted config refuses -------------------------------------------------------------
    def plant(self, kind):
        """Plant one thing the allow-list must refuse; returns the prefix of the key the event must list."""
        if kind == "driver":
            git("config", "merge.evil.driver", str(self.script("d.sh")), cwd=self.h)
            return "merge.evil.driver"
        if kind == "smudge":
            git("config", "filter.x.smudge", str(self.script("s.sh")), cwd=self.h)
            return "filter.x.smudge"
        if kind == "attrfile":
            git("config", "core.attributesFile", str(self.env.home / "attrs"), cwd=self.h)
            return "core.attributesfile"
        if kind == "gitproxy":
            git("config", "core.gitProxy", str(self.script("p.sh")), cwd=self.h)
            return "core.gitproxy"
        if kind == "url":
            git("config", "remote.origin.url", "git://example.invalid/factory.git", cwd=self.h)
            return "remote.origin.url="
        if kind == "insteadof":
            git("config", "url.git://example.invalid/.insteadOf", "https://github.com/", cwd=self.h)
            return "url."
        if kind in ("include-path", "include-if", "include-worktrees", "include-filter", "include-attrfile"):
            body = {"include-filter": '[filter "x"]\n\tclean = true\n',
                    "include-attrfile": "[core]\n\tattributesFile = /dev/null\n"}.get(kind, '[merge "evil"]\n\tdriver = true\n')
            (self.h / ".git" / "inc.cfg").write_text(body)
            if kind == "include-if":
                git("config", f"includeIf.gitdir:{self.h}/.git.path", "inc.cfg", cwd=self.h)
            elif kind == "include-worktrees":
                git("config", f"includeIf.gitdir:{self.h}/.git/worktrees/**.path", "inc.cfg", cwd=self.h)
            else:
                git("config", "include.path", "inc.cfg", cwd=self.h)
            return "includeif." if kind.startswith("include-") and kind in ("include-if", "include-worktrees") else "include.path"
        if kind in ("credential", "gpg", "ssh"):
            key = {"credential": "credential.helper", "gpg": "gpg.program", "ssh": "core.sshCommand"}[kind]
            git("config", key, "!true", cwd=self.h)
            return key.lower()
        (self.h / ".git" / "info").mkdir(exist_ok=True)
        (self.h / ".git" / "info" / "attributes").write_text("* merge=evil\n")
        return "info/attributes"

    KINDS = ("driver", "smudge", "attrfile", "info", "include-path", "include-if", "include-worktrees", "include-filter",
             "include-attrfile", "credential", "gpg", "ssh", "gitproxy", "url", "insteadof")

    def test_tick_refuses_and_takes_no_git_action(self):
        for kind in self.KINDS:
            with self.subTest(kind=kind):
                self.env.sql("DELETE FROM events WHERE action='git-config-refused'")
                self.env.sql("DELETE FROM messages WHERE subject LIKE 'git config refused%'")
                self.env.sql("DELETE FROM config WHERE key='last_git_config_refused_ts'")
                before_main = self.g("rev-parse", "main")
                before_origin = self.g("rev-parse", "refs/remotes/origin/main")
                state_before = git(*SAFE, "rev-parse", "-q", "--verify", "refs/heads/state", cwd=self.h) \
                    if self.has_state() else ""
                key = self.plant(kind)
                self.merge_code()
                self.ok("plan", "G-001", f"zz-test plan {kind}", agent="zz-test-director")
                self.ok("tick")
                self.ok("tick")
                self.assertEqual(self.g("rev-parse", "main"), before_main)  # no merge
                self.assertEqual(self.g("rev-parse", "refs/remotes/origin/main"), before_origin)  # no fetch
                self.assertEqual(self.has_state() and self.g("rev-parse", "refs/heads/state") or "", state_before)
                self.assertFalse(self.marker.exists())
                ev = self.refused_events()
                self.assertEqual(len(ev), 1, ev)  # at most hourly
                self.assertTrue(any(k.startswith(key) for k in ev[0]["keys"]), (key, ev[0]["keys"]))
                self.assertEqual(len(self.refusal_msgs()), 1)
                self.unplant(kind)
                self.ok("tick")
                self.assertEqual(self.g("rev-parse", "main"), self.g("rev-parse", "refs/remotes/origin/main"))
                self.assertNotEqual(self.g("rev-parse", "main"), before_main)  # synced again once clean

    def has_state(self):
        return bool(git(*SAFE, "for-each-ref", "refs/heads/state", cwd=self.h))

    def unplant(self, kind):
        """Put the repo config back to the live-like allow-list."""
        (self.h / ".git" / "config").write_text(self.clean_config)
        info = self.h / ".git" / "info" / "attributes"
        if info.exists():
            info.write_text("")

    def test_export_alone_refuses_state_commit(self):
        self.plant("driver")
        self.ok("plan", "G-001", "zz-test plan", agent="zz-test-director")
        self.ok("export")
        self.assertFalse(self.has_state())
        self.assertEqual(len(self.refused_events()), 1)

    def test_global_config_is_out_of_scope_xdg_attributes_refuse(self):
        """Known limit (G-010): ~/.gitconfig is not examined; a non-empty ~/.config/git/attributes still refuses."""
        uh = self.env.user_home
        (uh / ".gitconfig").write_text('[credential "https://github.com"]\n\thelper = !gh auth git-credential\n[merge "g"]\n\tdriver = true\n')
        new = self.merge_code()
        self.ok("tick")
        self.assertEqual(self.g("rev-parse", "main"), new)
        self.assertEqual(self.refused_events(), [])
        (uh / ".config" / "git").mkdir(parents=True)
        (uh / ".config" / "git" / "attributes").write_text("* merge=evil\n")
        self.merge_code()
        self.ok("tick")
        self.assertIn("~/.config/git/attributes", self.refused_events()[0]["keys"])

    def test_live_like_config_passes(self):
        """The allow-list covers every key the live ~/factory/.git/config has (branch names with dots and slashes too)."""
        for k, v in (("branch.factory/t-0001.remote", "origin"), ("branch.factory/t-0001.merge", "refs/heads/factory/t-0001"),
                     ("branch.a.b.remote", "origin"), ("extensions.worktreeConfig", "false")):
            git("config", k, v, cwd=self.h)
        with self.env.load_factory() as mod:
            self.assertEqual(mod.git_config_refusal(self.h), [])
            url = "https://github.com/example-org/factory.git"
            self.env.sql("UPDATE config SET value=? WHERE key='factory_remote_url'", (url,))
            mod.HOME = Path(mod.LIVE_HOME)  # the live rule: only the factory https url, no plain paths
            self.assertEqual(mod.git_config_refusal(self.h), [f"remote.origin.url={self.bare}"])
            git("config", "remote.origin.url", url, cwd=self.h)
            self.assertEqual(mod.git_config_refusal(self.h), [])
            git("config", "remote.origin.url", "https://github.com/evil/x.git", cwd=self.h)
            self.assertEqual(mod.git_config_refusal(self.h), ["remote.origin.url=https://github.com/evil/x.git"])

    URL = "https://github.com/example-org/factory.git"

    def live_checkout(self, origin):
        """A throwaway 'live' checkout whose origin is `origin`; returns its path."""
        live = self.env.home / "live"
        live.mkdir()
        git("init", "-q", "-b", "main", cwd=live)
        git("remote", "add", "origin", origin, cwd=live)
        return live

    def stored_url(self):
        rows = self.env.sql("SELECT value FROM config WHERE key='factory_remote_url'")
        return rows[0][0] or None if rows else None

    def test_remote_url_tofu_stores_the_live_origin_and_records_an_event(self):
        live = self.live_checkout(self.URL)
        with self.env.load_factory() as mod:
            mod.LIVE_HOME = str(live)
            self.assertIsNone(self.stored_url())
            self.assertEqual(mod.factory_remote_url(), self.URL)
        self.assertEqual(self.stored_url(), self.URL)
        ev = self.env.sql("SELECT detail FROM events WHERE action='tofu' AND entity='config' AND entity_id='factory_remote_url'")
        self.assertEqual([r[0] for r in ev], [self.URL])
        live_cfg = live / ".git" / "config"
        git("config", "remote.origin.url", "https://github.com/evil/x.git", cwd=live)
        with self.env.load_factory() as mod:
            mod.LIVE_HOME = str(live)
            self.assertEqual(mod.factory_remote_url(), self.URL)  # stored: the live checkout is not read again
        self.assertTrue(live_cfg.exists())

    def test_remote_url_configured_wins_over_the_live_checkout(self):
        live = self.live_checkout(self.URL)
        other = "https://github.com/example-org/other.git"
        self.env.sql("UPDATE config SET value=? WHERE key='factory_remote_url'", (other,))
        with self.env.load_factory() as mod:
            mod.LIVE_HOME = str(live)
            self.assertEqual(mod.factory_remote_url(), other)
        self.assertEqual(self.env.sql("SELECT 1 FROM events WHERE action='tofu'"), [])

    def test_remote_url_malformed_or_missing_live_origin_fails_closed(self):
        for origin in ("ext::sh -c x", "https://github.com/example-org/factory", "git@github.com:example-org/factory.git",
                       "https://github.com/example-org/factory.git/extra"):
            with self.subTest(origin=origin):
                live = self.live_checkout(origin)
                with self.env.load_factory() as mod:
                    mod.LIVE_HOME = str(live)
                    self.assertEqual(mod.factory_remote_url(), "")
                    mod.HOME = Path(mod.LIVE_HOME)
                    git("config", "remote.origin.url", origin, cwd=self.h)
                    self.assertEqual(mod.git_config_refusal(self.h), [f"remote.origin.url={origin}"])
                self.assertIsNone(self.stored_url())
                self.assertEqual(self.env.sql("SELECT 1 FROM events WHERE action='tofu'"), [])
                shutil.rmtree(live)
        with self.env.load_factory() as mod:
            mod.LIVE_HOME = str(self.env.home / "no-such-live")
            self.assertEqual(mod.factory_remote_url(), "")

    def test_remote_guard_refuses_changed_origin_through_the_tick(self):
        self.env.sql("UPDATE config SET value=? WHERE key='factory_remote_url'", (str(self.bare),))
        self.merge_code()
        self.ok("tick")
        self.assertEqual(self.refused_events(), [])
        git("config", "remote.origin.url", "https://github.com/evil/x.git", cwd=self.h)
        self.merge_code()
        self.ok("tick")
        self.assertEqual(self.refused_events()[0]["keys"], ["remote.origin.url=https://github.com/evil/x.git"])

    def spawn_repo(self):
        self.env.seed_task(goal="G-001")
        self.env.sql("UPDATE projects SET repo_path=? WHERE id='zz-test-proj'", (str(self.h),))

    def prepare(self, role="coder"):
        with self.env.load_factory() as mod:
            con = mod.db()
            try:
                mod.prepare_worktree(con, "T-0001", role)
            except SystemExit as e:
                return e.code
        return 0

    def test_spawn_worktree_refuses_planted_config_before_fetch(self):
        self.spawn_repo()
        for kind in self.KINDS:
            with self.subTest(kind=kind):
                self.env.sql("DELETE FROM events WHERE action='git-config-refused'")
                self.env.sql("DELETE FROM config WHERE key='last_git_config_refused_ts'")
                key = self.plant(kind)
                before = self.g("rev-parse", "refs/remotes/origin/main")
                self.merge_code()
                self.assertEqual(self.prepare(), 4)  # spawn exits non-zero
                self.assertEqual(self.g("rev-parse", "refs/remotes/origin/main"), before)  # no fetch
                self.assertFalse(list((self.env.home_dir / "worktrees").glob("*")))
                self.assertFalse(self.marker.exists())
                ev = self.refused_events()
                self.assertEqual(len(ev), 1)
                self.assertTrue(any(k.startswith(key) for k in ev[0]["keys"]), ev)
                self.unplant(kind)
        self.assertEqual(self.prepare(), 0)  # live-like config again: the worktree is made
        self.assertTrue(list((self.env.home_dir / "worktrees").glob("*")))

    def commit_filter_attributes(self):
        """origin/main gets `* filter=x`, so any checkout runs a configured smudge filter."""
        other = self.env.home / "other"
        self.merge_code()
        (other / ".gitattributes").write_text("* filter=x\n")
        git("add", ".", cwd=other)
        git("commit", "-q", "-m", "attrs", cwd=other)
        git("push", "-q", "origin", "main", cwd=other)

    def test_worktree_add_does_not_run_includeif_smudge_filter(self):
        """(c) includeIf gitdir:.../worktrees/** applies only to the new worktree: refused on the includeIf itself, and
        when the first guard is bypassed the guard on the new worktree runs before any checkout."""
        self.spawn_repo()
        self.commit_filter_attributes()
        (self.h / ".git" / "inc.cfg").write_text(f'[filter "x"]\n\tsmudge = {self.script("s.sh")}\n')
        git("config", f"includeIf.gitdir:{self.h}/.git/worktrees/**.path", "inc.cfg", cwd=self.h)
        self.assertEqual(self.prepare(), 4)
        self.assertFalse(self.marker.exists())
        with self.env.load_factory() as mod:
            real, calls = mod.git_config_guard, []
            def first_passes(con, repo, always_event=False):
                calls.append(str(repo))
                return True if len(calls) == 1 else real(con, repo, always_event)
            mod.git_config_guard = first_passes
            con = mod.db()
            with self.assertRaises(SystemExit) as cm:
                mod.prepare_worktree(con, "T-0001", "coder")
        self.assertEqual(cm.exception.code, 4)
        self.assertEqual(len(calls), 2)
        self.assertTrue(calls[1].endswith("G-001-T-0001"), calls)  # the guard ran on the new worktree
        self.assertFalse(self.marker.exists(), "smudge filter ran during worktree add")
        self.assertFalse(list((self.env.home_dir / "worktrees").glob("*")))  # and the half-made worktree is gone

    def test_worktree_checkout_still_runs_after_guard(self):
        self.spawn_repo()
        self.merge_code()
        self.assertEqual(self.prepare(), 0)
        wt = next((self.env.home_dir / "worktrees").glob("*"))
        self.assertTrue(list(wt.glob("code*.txt")), "files must be checked out after --no-checkout + guard")

    def test_retry_switch_is_guarded(self):
        """B3a: a coder retry in an existing worktree runs `git switch`, which checks out; it is guarded too."""
        self.spawn_repo()
        self.commit_filter_attributes()
        self.assertEqual(self.prepare(), 0)
        wt = next((self.env.home_dir / "worktrees").glob("*"))
        self.env.sql("UPDATE tasks SET worktree=?, branch='factory/t-0001' WHERE id='T-0001'", (str(wt),))
        git("checkout", "-q", "--detach", cwd=wt)
        git("config", "filter.x.smudge", str(self.script("s.sh")), cwd=self.h)
        git("config", "filter.x.required", "true", cwd=self.h)
        self.assertEqual(self.prepare(), 4)
        self.assertFalse(self.marker.exists(), "smudge filter ran during the retry switch")
        self.assertEqual(self.refused_events()[0]["keys"][0][:7], "filter.")

    def test_clean_refuses_fetch_on_planted_config(self):
        self.env.seed_task(goal="G-001")
        self.env.sql("UPDATE projects SET repo_path=? WHERE id='zz-test-proj'", (str(self.h),))
        self.env.sql("UPDATE tasks SET branch='factory/t-0001' WHERE id='T-0001'")
        self.env.seed_agent("zz-test-cleaner", "cleaner", goal="G-001")
        self.plant("include-path")
        before = self.g("rev-parse", "refs/remotes/origin/main")
        self.merge_code()
        self.env.run("clean", agent="zz-test-cleaner")
        self.assertEqual(self.g("rev-parse", "refs/remotes/origin/main"), before)
        self.assertEqual(len(self.refused_events()), 1)

    def test_commit_gpgsign_is_forced_off(self):
        """gpg.program is refused, and commit.gpgSign=true in repo config is overridden on the command line."""
        with self.env.load_factory() as mod:
            argv = mod.git_argv(self.h, "status")
        self.assertIn("commit.gpgSign=false", argv)
        self.assertIn("tag.gpgSign=false", argv)
        with self.env.load_factory() as mod:
            env = mod.gh_git_env()
        vals = {env[f"GIT_CONFIG_KEY_{i}"]: env[f"GIT_CONFIG_VALUE_{i}"] for i in range(int(env["GIT_CONFIG_COUNT"]))}
        self.assertEqual((vals["commit.gpgSign"], vals["tag.gpgSign"]), ("false", "false"))

    def test_clean_repo_no_refusal_event(self):
        self.ok("tick")
        self.assertEqual(self.refused_events(), [])


if __name__ == "__main__":
    unittest.main()
