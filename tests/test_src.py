import os
import re
import subprocess
import sys
import unittest
from pathlib import Path

from harness import FACTORY, FactoryTestCase, git


class SrcTests(FactoryTestCase):
    """factory src <ref> <dest>: a scrubbed git archive into the caller's scratchpad."""

    def setUp(self):
        super().setUp()
        e = self.env
        self.repo = e.home / "proj"
        self.repo.mkdir()
        git("init", "-q", "-b", "main", cwd=self.repo)
        (self.repo / ".gitattributes").write_text("*.txt filter=x\n")  # any config naming filter x would smudge on export
        (self.repo / "a.txt").write_text("hello\n")
        (self.repo / "sub").mkdir()
        (self.repo / "sub" / "b.txt").write_text("b\n")
        git("add", ".", cwd=self.repo)
        git("commit", "-q", "-m", "one", cwd=self.repo)
        e.sql("INSERT INTO projects(id,name,repo_path,created) VALUES('zz-test-proj','x',?,'t')", (str(self.repo),))
        e.seed_goal(project="zz-test-proj")
        e.seed_agent("zz-test-qa", "qa", goal="G-001")
        e.sql("UPDATE agents SET cwd=?, session_id='sess' WHERE name='zz-test-qa'", (str(e.home),))
        slug = re.sub(r"[^A-Za-z0-9]", "-", str(e.home))
        self.scratch = e.home / "faketmp" / f"claude-{os.getuid()}" / slug / "sess" / "scratchpad"
        self.scratch.mkdir(parents=True)
        self.marker = e.home / "marker"

    def src(self, *args, extra_env=None):
        return self.env.run("src", *args, agent="zz-test-qa", extra_env=extra_env)

    def fail(self, *args, **kw):
        p = self.src(*args, **kw)
        self.assertNotEqual(p.returncode, 0, p.stdout + p.stderr)
        return p

    def script(self):
        s = self.env.home / "evil.sh"
        s.write_text(f"#!/bin/sh\necho ran >> '{self.marker}'\ncat\n")
        s.chmod(0o755)
        return s

    def evil_config(self, path):
        s = self.script()
        path.write_text(f"[tar \"tar\"]\n\tcommand = {s}\n[tar \"foo\"]\n\tcommand = {s}\n"
                        f"[filter \"x\"]\n\tsmudge = {s}\n\tclean = {s}\n\trequired = true\n")

    def test_happy_path(self):
        d = self.scratch / "main"
        p = self.src("main", str(d))
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual((d / "a.txt").read_text(), "hello\n")
        self.assertEqual((d / "sub" / "b.txt").read_text(), "b\n")

    def test_paths_limit_the_export(self):
        d = self.scratch / "part"
        p = self.src("main", str(d), "--", "sub")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertTrue((d / "sub" / "b.txt").exists())
        self.assertFalse((d / "a.txt").exists())

    def test_empty_existing_dest_ok(self):
        d = self.scratch / "empty"
        d.mkdir()
        self.assertEqual(self.src("main", str(d)).returncode, 0)
        self.assertTrue((d / "a.txt").exists())

    def test_home_gitconfig_never_runs(self):
        self.evil_config(self.env.user_home / ".gitconfig")
        p = self.src("main", str(self.scratch / "o"))
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertFalse(self.marker.exists())

    def test_env_config_never_runs(self):
        cfg = self.env.home / "evil.cfg"
        self.evil_config(cfg)
        xdg = self.env.home / "xdg"
        (xdg / "git").mkdir(parents=True)
        self.evil_config(xdg / "git" / "config")
        other_home = self.env.home / "otherhome"
        other_home.mkdir()
        self.evil_config(other_home / ".gitconfig")
        s = self.script()
        for i, extra in enumerate([
            {"GIT_CONFIG_GLOBAL": str(cfg)},
            {"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "filter.x.smudge", "GIT_CONFIG_VALUE_0": str(s)},
            {"HOME": str(other_home)},
            {"XDG_CONFIG_HOME": str(xdg)},
            {"GIT_CONFIG_PARAMETERS": f"'filter.x.smudge={s}'"},
        ]):
            p = self.src("main", str(self.scratch / f"o{i}"), extra_env=extra)
            self.assertEqual(p.returncode, 0, f"{extra}: {p.stderr}")
            self.assertFalse(self.marker.exists(), extra)

    def test_gitattributes_in_ref_never_runs(self):
        (self.repo / ".gitattributes").write_text("*.txt filter=x export-subst\n")
        (self.repo / "c.txt").write_text("$Format:%H$\n")
        git("add", ".", cwd=self.repo)
        git("commit", "-q", "-m", "attrs", cwd=self.repo)
        self.evil_config(self.env.user_home / ".gitconfig")
        d = self.scratch / "attrs"
        self.assertEqual(self.src("main", str(d)).returncode, 0)
        self.assertFalse(self.marker.exists())
        self.assertTrue((d / "c.txt").exists())

    def test_core_attributesfile_in_global_config_never_runs(self):
        s = self.script()
        attrs = self.env.home / "attrs"
        attrs.write_text("*.txt filter=x\n")
        self.evil_config(self.env.user_home / ".gitconfig")
        with open(self.env.user_home / ".gitconfig", "a") as f:
            f.write(f"[core]\n\tattributesFile = {attrs}\n")
        self.assertEqual(self.src("main", str(self.scratch / "o")).returncode, 0)
        self.assertFalse(self.marker.exists())

    def test_unscrubbed_git_archive_does_run_the_filter(self):
        """Positive control: the marker tests above are not vacuous."""
        self.evil_config(self.env.user_home / ".gitconfig")
        subprocess.run(["git", "-C", str(self.repo), "archive", "--format=tar", "-o", str(self.env.home / "x.tar"), "main"],
                       env={**os.environ, "HOME": str(self.env.user_home), "GIT_CONFIG_NOSYSTEM": "1"}, check=True,
                       capture_output=True)
        self.assertTrue(self.marker.exists())

    def test_refuses_dest_outside_scratchpad(self):
        for d in (self.env.home / "elsewhere", self.scratch.parent / "other", self.scratch, "/tmp/zz-src-nope"):
            p = self.fail("main", str(d))
            self.assertIn("scratchpad", p.stderr)
            self.assertFalse(Path(d).exists() and Path(d) != self.scratch)

    def test_refuses_shared_checkout(self):
        # a registered repo_path that sits inside the scratchpad is still shared
        shared = self.scratch / "shared"
        shared.mkdir()
        self.env.sql("INSERT INTO projects(id,name,repo_path,created) VALUES('p2','p2',?,'t')", (str(shared),))
        for d in (shared / "x", shared):
            p = self.fail("main", str(d))
            self.assertIn("shared checkout", p.stderr)
        # a dest that is a parent of a shared checkout
        p = self.fail("main", str(self.scratch))
        self.assertNotEqual(p.returncode, 0)
        self.assertFalse((shared / "x").exists())

    def test_refuses_symlink_escape(self):
        outside = self.env.home / "outside"
        outside.mkdir()
        (self.scratch / "link").symlink_to(outside)
        for d in (self.scratch / "link", self.scratch / "link" / "sub"):
            self.fail("main", str(d))
        self.assertEqual(list(outside.iterdir()), [])

    def test_refuses_non_empty_dest(self):
        d = self.scratch / "full"
        d.mkdir()
        (d / "keep").write_text("k")
        p = self.fail("main", str(d))
        self.assertIn("empty", p.stderr)
        self.assertEqual([x.name for x in d.iterdir()], ["keep"])
        f = self.scratch / "file"
        f.write_text("x")
        self.fail("main", str(f))

    def test_refuses_leading_dash(self):
        self.fail("-x", str(self.scratch / "o"))
        self.fail("--output=/tmp/zz", str(self.scratch / "o"))
        self.fail("main", "-d")
        self.assertFalse((self.scratch / "o").exists())

    def test_refuses_non_tree_ish_and_cleans_up(self):
        p = self.fail("no-such-ref", str(self.scratch / "o"))
        self.assertIn("tree-ish", p.stderr)
        self.assertFalse((self.scratch / "o").exists())
        pre = self.scratch / "pre"
        pre.mkdir()
        self.fail("no-such-ref", str(pre))
        self.assertEqual(list(pre.iterdir()), [])

    def other_scratch(self, cwd, sess="victim"):
        slug = re.sub(r"[^A-Za-z0-9]", "-", str(cwd))
        d = self.env.home / "faketmp" / f"claude-{os.getuid()}" / slug / sess / "scratchpad"
        d.mkdir(parents=True)
        return d

    def test_refuses_foreign_and_hq_scratchpads(self):
        agent_dir = self.env.home / "otheragent"
        agent_dir.mkdir()
        for cwd, pad in ((agent_dir, self.other_scratch(agent_dir)), (self.env.home / "hq", self.other_scratch(self.env.home / "hq"))):
            Path(cwd).mkdir(exist_ok=True)
            # cwd = the foreign dir (the QA repro): refused, nothing planted
            p = subprocess.run([sys.executable, str(FACTORY), "src", "main", str(pad / "planted")], capture_output=True,
                               text=True, env=self.env.env("zz-test-qa"), cwd=cwd)
            self.assertNotEqual(p.returncode, 0, p.stdout)
            self.assertIn("working directory", p.stderr)
            self.assertFalse((pad / "planted").exists())
            # cwd = own dir, dest in the foreign scratchpad: refused too
            self.fail("main", str(pad / "planted"))
            self.assertFalse((pad / "planted").exists())

    def test_only_own_session_scratchpad_allowed(self):
        sibling = self.scratch.parent.parent / "sess2" / "scratchpad"
        sibling.mkdir(parents=True)
        p = self.fail("main", str(sibling / "o"))
        self.assertIn("not inside your scratchpad", p.stderr)
        self.assertFalse((sibling / "o").exists())
        self.assertEqual(self.src("main", str(self.scratch / "o")).returncode, 0)

    def test_refuses_without_agent_identity(self):
        for env in ({"FACTORY_AGENT": ""}, {"FACTORY_AGENT": "no-such-agent"}):
            p = self.src("main", str(self.scratch / "o"), extra_env=env)
            self.assertNotEqual(p.returncode, 0, p.stdout)
            self.assertIn("registered agent", p.stderr)
            self.assertFalse((self.scratch / "o").exists())

    def test_repo_level_filter_config_refused(self):
        git("config", "filter.x.smudge", str(self.script()), cwd=self.repo)
        git("config", "filter.x.required", "true", cwd=self.repo)
        p = self.fail("main", str(self.scratch / "o"))
        self.assertIn("filter.x", p.stderr)
        self.assertFalse(self.marker.exists())
        self.assertFalse((self.scratch / "o").exists())

    def test_repo_info_attributes_refused(self):
        (self.repo / ".git" / "info").mkdir(exist_ok=True)
        (self.repo / ".git" / "info" / "attributes").write_text("*.txt filter=x\n")
        p = self.fail("main", str(self.scratch / "o"))
        self.assertIn("info/attributes", p.stderr)
        self.assertFalse(self.marker.exists())

    def test_failure_removes_created_parents(self):
        p = self.fail("no-such-ref", str(self.scratch / "p1" / "p2" / "o"))
        self.assertFalse((self.scratch / "p1").exists())
        self.assertTrue(self.scratch.exists())

    def test_help_documents_src(self):
        p = self.env.run("src", "--help")
        self.assertEqual(p.returncode, 0)
        self.assertIn("scratchpad", p.stdout)


if __name__ == "__main__":
    unittest.main()
