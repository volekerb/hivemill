"""ci/publish_repo.py through main(argv), against a stub gh only (the real gh is never run)."""
import importlib.util
import io
import json
import os
import pwd
import re
import stat
import sys
import tempfile
import unittest
import unittest.mock
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
TOOL = ROOT / "ci" / "publish_repo.py"
META = ROOT / "ci" / "publish_metadata.json"
WORKFLOW = ROOT / ".github" / "workflows" / "tests.yml"
PUBLIC = "acme/public"
LAN = "192.168." + "0.9"  # split so this file is not a scrub hit itself

STUB = """#!{python}
import json, os, sys
here = os.path.dirname(os.path.abspath(__file__))
cfg = json.load(open(os.path.join(here, "cfg.json")))
body = sys.stdin.read()
with open(os.path.join(here, "calls"), "a") as f:
    f.write(json.dumps({{"argv": sys.argv[1:], "stdin": body}}) + "\\n")
n = sum(1 for _ in open(os.path.join(here, "calls")))
if n == cfg.get("fail_at"):
    sys.stderr.write("HTTP 403: stub failure\\n")
    sys.exit(1)
if n == 1:
    print(json.dumps({{"private": cfg["private"], "fork": cfg.get("fork", False)}}))
"""


def load_tool():
    spec = importlib.util.spec_from_file_location("publish_repo_under_test", TOOL)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def workflow_checks(path):
    """Check names of every job of a workflow file: `name:` of the job (matrix.shard expanded), else the job key."""
    jobs = {}  # key -> {"name": str|None, "shards": [str]}
    in_jobs = False
    for line in Path(path).read_text().splitlines():
        if re.match(r"jobs:\s*$", line):
            in_jobs = True
        elif in_jobs and re.match(r"\S", line):
            in_jobs = False
        if not in_jobs:
            continue
        key = re.match(r"  ([A-Za-z0-9_-]+):\s*$", line)
        if key:
            cur = jobs.setdefault(key.group(1), {"name": None, "shards": []})
            continue
        name = re.match(r"    name:\s*(.+?)\s*$", line)
        if name:
            cur["name"] = name.group(1).strip("\"'")
        shards = re.match(r"\s+shard:\s*\[([^\]]*)\]", line)
        if shards:
            cur["shards"] = [x.strip() for x in shards.group(1).split(",")]
    names = []
    for key, job in jobs.items():
        name = job["name"] or key
        if "${{ matrix.shard }}" in name:
            names += [name.replace("${{ matrix.shard }}", sh) for sh in job["shards"]]
        else:
            names.append(name)
    return names


class Base(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name).resolve()
        self.mod = load_tool()
        # A fake passwd account: its ~/factory is the "live home" (the real one is never read).
        self.acct = self.root / "account"
        (self.acct / "factory").mkdir(parents=True)
        entry = pwd.struct_passwd(("u", "x", os.getuid(), os.getgid(), "", str(self.acct), "/bin/sh"))
        p = unittest.mock.patch.object(pwd, "getpwuid", return_value=entry)
        p.start()
        self.addCleanup(p.stop)
        self.stub_dir = self.root / "stub"
        self.stub_dir.mkdir()
        self.stub = self.stub_dir / "gh"
        self.calls = self.stub_dir / "calls"

    def make_stub(self, **cfg):
        self.stub.write_text(STUB.format(python=sys.executable))
        self.stub.chmod(self.stub.stat().st_mode | stat.S_IXUSR)
        (self.stub_dir / "cfg.json").write_text(json.dumps(cfg))

    def live_origin(self, url):
        """A minimal git dir by hand (no git binary needed): HEAD, config with the origin, objects/, refs/."""
        git = self.acct / "factory" / ".git"
        for d in ("objects", "refs"):
            (git / d).mkdir(parents=True, exist_ok=True)
        (git / "HEAD").write_text("ref: refs/heads/main\n")
        (git / "config").write_text(f'[core]\n\trepositoryformatversion = 0\n[remote "origin"]\n\turl = {url}\n')

    def main(self, *argv):
        out, err = io.StringIO(), io.StringIO()
        with redirect_stdout(out), redirect_stderr(err):
            rc = self.mod.main(list(argv))
        return rc, out.getvalue(), err.getvalue()

    def recorded(self):
        if not self.calls.exists():
            return []
        return [json.loads(line) for line in self.calls.read_text().splitlines()]


EXPECTED = [("GET", "repos/acme/public"), ("PATCH", "repos/acme/public"), ("PUT", "repos/acme/public/topics"),
            ("PUT", "repos/acme/public/vulnerability-alerts"), ("PUT", "repos/acme/public/private-vulnerability-reporting"),
            ("PUT", "repos/acme/public/branches/main/protection")]


class TestDryRun(Base):
    def test_default_makes_no_call_and_prints_six_lines_in_order(self):
        self.make_stub(private=False)
        rc, out, err = self.main("--repo", PUBLIC, "--gh", str(self.stub))
        self.assertEqual(rc, 0, err)
        self.assertFalse(self.calls.exists(), "the stub must never have run")
        lines = [ln.split() for ln in out.splitlines() if ln.startswith("gh api -X")]
        self.assertEqual([(ln[3], ln[4]) for ln in lines], EXPECTED)
        self.assertIn("Upload docs/images/social-preview.png by hand: Settings > General > Social preview.", out)
        self.assertIn('"has_wiki": false', out)
        self.assertIn("https://github.com/acme/public#readme", out)
        self.assertNotIn("__PUBLIC_", out)

    def test_dry_run_with_missing_gh_does_not_care(self):
        rc, out, err = self.main("--repo", PUBLIC, "--gh", str(self.root / "no-such-gh"))
        self.assertEqual(rc, 0, err)


class TestApply(Base):
    def test_apply_records_exactly_six_calls_with_bodies(self):
        self.make_stub(private=False)
        rc, out, err = self.main("--repo", PUBLIC, "--apply", "--gh", str(self.stub))
        self.assertEqual(rc, 0, err)
        rec = self.recorded()
        self.assertEqual([(r["argv"][2], r["argv"][3]) for r in rec], EXPECTED)
        self.assertTrue(all(r["argv"][:2] == ["api", "-X"] for r in rec))
        meta = json.loads(META.read_text())
        patch = json.loads(rec[1]["stdin"])
        self.assertEqual(patch, {"description": meta["description"], "homepage": "https://github.com/acme/public#readme",
                                 "has_issues": True, "has_discussions": True, "has_wiki": False, "has_projects": False})
        self.assertEqual(json.loads(rec[2]["stdin"]), {"names": meta["topics"]})
        self.assertEqual(rec[0]["stdin"], "")
        self.assertEqual(rec[3]["stdin"], "")
        self.assertEqual(rec[4]["stdin"], "")
        self.assertEqual(json.loads(rec[5]["stdin"]), {"required_status_checks": {"strict": True, "contexts": meta["required_checks"]},
                                                       "enforce_admins": False, "required_pull_request_reviews": None, "restrictions": None})
        self.assertIn("Upload docs/images/social-preview.png by hand", out)

    def test_private_repository_is_refused_after_the_get_only(self):
        self.make_stub(private=True)
        rc, out, err = self.main("--repo", PUBLIC, "--apply", "--gh", str(self.stub))
        self.assertEqual(rc, 2)
        self.assertIn("branch protection is only for the public repository", err)
        self.assertEqual([r["argv"][3] for r in self.recorded()], ["repos/acme/public"])

    def test_fork_is_refused(self):
        self.make_stub(private=False, fork=True)
        rc, _out, err = self.main("--repo", PUBLIC, "--apply", "--gh", str(self.stub))
        self.assertEqual(rc, 2, err)
        self.assertEqual(len(self.recorded()), 1)

    def test_first_write_failing_stops_the_rest(self):
        self.make_stub(private=False, fail_at=2)
        rc, _out, err = self.main("--repo", PUBLIC, "--apply", "--gh", str(self.stub))
        self.assertEqual(rc, 1)
        self.assertEqual(len(self.recorded()), 2, "calls after the failing PATCH must not be made")
        self.assertIn("FAILED PATCH repos/acme/public", err)
        self.assertIn("calls already made: GET repos/acme/public", err)


class TestRefusals(Base):
    def assert_refused(self, *argv):
        rc, _out, err = self.main(*argv)
        self.assertEqual(rc, 2, err)
        self.assertFalse(self.calls.exists(), "a refusal must come before any call")
        return err

    def test_live_home_origin_slug_is_refused_in_both_modes(self):
        self.make_stub(private=False)
        self.live_origin("https://github.com/Acme/Private.git")
        for extra in ([], ["--apply"]):
            err = self.assert_refused("--repo", "acme/private", "--gh", str(self.stub), *extra)
            self.assertIn("private repository", err)

    def test_other_slug_passes_with_a_live_home_origin(self):
        self.live_origin("https://github.com/acme/private.git")
        rc, _out, err = self.main("--repo", PUBLIC)
        self.assertEqual(rc, 0, err)

    def test_private_repo_flag_is_refused_case_insensitively(self):
        self.make_stub(private=False)
        self.assert_refused("--repo", "ACME/private", "--private-repo", "acme/private", "--gh", str(self.stub), "--apply")

    def test_bad_slugs(self):
        self.make_stub(private=False)
        for bad in ("a/b/c", "nosep", "/x", "x/", "a b/c", "o/.."):
            self.assert_refused("--repo", bad, "--gh", str(self.stub))
        self.assert_refused("--repo", PUBLIC, "--private-repo", "bad", "--gh", str(self.stub))

    def test_scrub_hit_in_metadata_is_refused_and_masked(self):
        self.make_stub(private=False)
        meta = json.loads(META.read_text())
        meta["description"] = f"server at {LAN}"
        copy = self.root / "meta.json"
        copy.write_text(json.dumps(meta))
        err = self.assert_refused("--repo", PUBLIC, "--metadata", str(copy), "--gh", str(self.stub), "--apply")
        self.assertIn("scrub-scan hits", err)
        self.assertNotIn(LAN, err)

    def test_private_slug_inside_metadata_is_refused(self):
        self.make_stub(private=False)
        self.live_origin("https://github.com/acme/private")
        meta = json.loads(META.read_text())
        meta["description"] = "mirror of acme/private"
        copy = self.root / "meta.json"
        copy.write_text(json.dumps(meta))
        err = self.assert_refused("--repo", PUBLIC, "--metadata", str(copy), "--gh", str(self.stub))
        self.assertIn("private repository slug", err)

    def test_prefix_slug_does_not_falsely_refuse(self):
        self.live_origin("https://github.com/acme/pub")
        rc, _out, err = self.main("--repo", PUBLIC)
        self.assertEqual(rc, 0, err)

    def test_dot_git_suffix_cannot_bypass(self):
        self.make_stub(private=False)
        self.live_origin("https://github.com/acme/private")
        self.assert_refused("--repo", "acme/private.git", "--gh", str(self.stub))
        self.assert_refused("--repo", PUBLIC, "--private-repo", "acme/other.git", "--metadata", str(self.root / "missing.json"))

    def test_unparsable_live_origin_is_refused(self):
        self.live_origin("https://git.example.test/acme/private")
        self.assert_refused("--repo", PUBLIC)

    def test_broken_metadata_is_refused(self):
        copy = self.root / "meta.json"
        copy.write_text('{"description": "x"}')
        self.assert_refused("--repo", PUBLIC, "--metadata", str(copy))


class TestMetadata(unittest.TestCase):
    def test_required_checks_equal_the_workflow_jobs(self):
        meta = json.loads(META.read_text())
        self.assertEqual(meta["required_checks"], workflow_checks(WORKFLOW))

    def test_a_renamed_job_no_longer_matches(self):
        with tempfile.TemporaryDirectory() as tmp:
            copy = Path(tmp) / "tests.yml"
            copy.write_text(WORKFLOW.read_text().replace("name: unittest-shard", "name: shard", 1))
            self.assertNotEqual(json.loads(META.read_text())["required_checks"], workflow_checks(copy))

    def test_topics_and_homepage(self):
        meta = json.loads(META.read_text())
        self.assertEqual(len(meta["topics"]), 14)
        self.assertLessEqual(len(meta["topics"]), 20)
        self.assertEqual(len(set(meta["topics"])), 14)
        self.assertEqual(meta["homepage"], "https://github.com/__PUBLIC_" + "REPO__#readme")
        self.assertLessEqual(len(meta["description"]), 100)


if __name__ == "__main__":
    unittest.main()
