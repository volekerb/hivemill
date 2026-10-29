"""factory skilleval vendor: allow-list copy at a pinned sha, executable exclusion, caps, idempotency and the
exfil pre-screen. No network: `gh` is the tests/stubs fixture (rules in $FAKE_GH_STATE). Paths under test are temp dirs."""
import base64
import hashlib
import json
import os
import shutil
import stat
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from harness import FactoryTestCase, REPO

sys.path.insert(0, str(REPO))
from scout import vendor  # noqa: E402

SHA = "a" * 40
REPO_NAME = "zz-owner/zz-repo"
SKILL_MD = "---\nname: foo\ndescription: Does foo.\n---\nWrite clear prose.\n"


def blob_sha(data):
    return hashlib.sha1(b"blob %d\0" % len(data) + data).hexdigest()


class Tree:
    """gh api fixture for one repo tree: files {path: content | (mode, content)}."""

    def __init__(self, files, repo=REPO_NAME, sha=SHA, size_lie=None):
        self.repo, self.sha, self.rules, self.blob_shas = repo, sha, [], set()
        entries = []
        for path, spec in files.items():
            mode, content = spec if isinstance(spec, tuple) else ("100644", spec)
            data = content.encode() if isinstance(content, str) else content
            bsha = blob_sha(data)
            typ = "commit" if mode == "160000" else "blob"
            entries.append({"path": path, "mode": mode, "type": typ, "sha": bsha,
                            "size": len(data) if size_lie is None else size_lie})
            self.blob_shas.add(bsha)
            self.rules.append({"match": [f"repos/{repo}/git/blobs/{bsha}"],
                               "out": {"content": base64.b64encode(data).decode(), "encoding": "base64"}})
        self.rules.append({"match": [f"repos/{repo}/git/trees/{sha}"], "out": {"tree": entries, "truncated": False}})


EXEC_TREE = {
    "SKILL.md": SKILL_MD, "x.sh": "echo hi\n", "x.py": "print(1)\n", "x.js": "1\n", "x.cjs": "1\n", "x.mjs": "1\n",
    "x.ts": "1\n", "blob.bin": b"\x00\x01\x02\xff", "README.md": ("100755", "# readme\n"),
    "link": ("120000", "x.sh"), "hooks/hooks.json": "{}", ".mcp.json": "{}",
    "commands/run.md": "Do it\n!`rm -rf x`\n",
    ".claude-plugin/plugin.json": json.dumps({"name": "n", "hooks": {}, "mcpServers": {}}),
}

PLUGIN_TREE = {
    "other/evil.sh": "echo outside\n",
    "plugins/p/.claude-plugin/plugin.json": json.dumps({"name": "demo", "description": "d"}),
    "plugins/p/skills/foo/SKILL.md": SKILL_MD,
    "plugins/p/skills/foo/reference.md": "# Reference\nMore prose.\n",
    "plugins/p/skills/foo/data.csv": "a,b\n1,2\n",
    "plugins/p/LICENSE": "MIT License\n",
}


class VendorBase(FactoryTestCase):
    def setUp(self):
        super().setUp()
        for r in ("scout", "director", "lead", "coder", "qa", "reviewer", "pm", "evaluator"):
            self.env.seed_agent(f"zz-test-{r}", r)
        self.root = self.env.home / "evalroot"
        self.ok("config", "skilleval_root", str(self.root), agent="owner")

    def tree(self, files, path="", risk="prompt-only", cid="C-0001", status="new", **kw):
        t = Tree(files, **kw)
        self.env.set_gh(api=t.rules)
        self.seed(cid, path, risk, status)
        return t

    def seed(self, cid="C-0001", path="", risk="prompt-only", status="new"):
        self.env.sql("INSERT INTO scout_candidates(id,key,repo,path,head_sha,licence,install_risk,status,sources,"
                     "first_seen,last_seen) VALUES(?,?,?,?,?,?,?,?,'[]','t','t')",
                     (cid, f"gh:{REPO_NAME}/{path}".rstrip("/") + cid, REPO_NAME, path or None, SHA, "MIT", risk, status))

    def vendor(self, cid="C-0001", agent="owner", expect=0):
        if expect == 0:
            return self.ok("skilleval", "vendor", cid, agent=agent)
        return self.refused(expect, "skilleval", "vendor", cid, agent=agent)

    def cand(self, cid="C-0001"):
        return self.env.sql("SELECT * FROM scout_candidates WHERE id=?", (cid,))[0]

    def api_calls(self):
        return [" ".join(c["argv"]) for c in self.env.calls("gh") if c["argv"][:1] == ["api"]]

    def files_under(self, d):
        return sorted(p.relative_to(d).as_posix() for p in Path(d).rglob("*") if p.is_file())


class PermissionAndRootTests(VendorBase):
    def test_roles(self):
        for r in ("coder", "qa", "reviewer", "pm", "scout", "evaluator"):
            self.refused(3, "skilleval", "vendor", "C-0001", agent=f"zz-test-{r}")
        self.assertEqual(self.api_calls(), [])
        self.assertFalse(self.root.exists())
        for who in ("zz-test-director", "zz-test-lead", "owner"):
            self.env.sql("DELETE FROM scout_candidates")
            shutil.rmtree(self.root, ignore_errors=True)
            self.tree(PLUGIN_TREE, "plugins/p")
            self.vendor(agent=who)
            self.assertTrue((self.root / "vendor" / f"C-0001@{SHA}" / "VENDOR.json").is_file(), who)

    def test_default_root_is_cache_dir(self):
        self.env.sql("DELETE FROM config WHERE key='skilleval_root'")
        self.assertIn("skilleval_root = ~/.cache/factory-skilleval", self.ok("config", agent="owner").stdout)
        with tempfile.TemporaryDirectory() as home, mock.patch.dict(os.environ, {"HOME": home}):
            want = Path(home).resolve() / ".cache" / "factory-skilleval"
            self.assertEqual(vendor.resolve_root(None), want)
            self.assertEqual(vendor.resolve_root("~/.cache/factory-skilleval"), want)

    def test_default_applies_through_the_cli(self):
        self.env.sql("DELETE FROM config WHERE key='skilleval_root'")
        self.tree(PLUGIN_TREE, "plugins/p")
        self.vendor()
        self.assertTrue((self.env.user_home / ".cache" / "factory-skilleval" / "vendor" / f"C-0001@{SHA}").is_dir())

    def test_root_inside_factory_or_claude_refused(self):
        for bad in ("~/factory/evals", "~/.claude/evals", "~/factory", "~", str(self.env.home_dir / "x"), "relative/dir"):
            p = self.env.run("config", "skilleval_root", bad, agent="owner")
            self.assertEqual(p.returncode, 2, bad + p.stderr)
            self.env.sql("INSERT INTO config(key,value) VALUES('skilleval_root',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", (bad,))
            self.env.sql("DELETE FROM scout_candidates")
            self.tree(PLUGIN_TREE, "plugins/p")
            p = self.vendor(expect=2)
            self.assertIn("skilleval_root", p.stderr)
            self.assertEqual(self.api_calls(), [], bad)
        self.assertFalse((self.env.user_home / "factory").exists())
        self.assertFalse((self.env.user_home / ".claude").exists())

    def test_symlink_into_factory_refused(self):
        link = self.env.home / "sneaky"
        link.symlink_to(self.env.user_home / ".claude", target_is_directory=True)
        p = self.env.run("config", "skilleval_root", str(link / "evals"), agent="owner")
        self.assertEqual(p.returncode, 2, p.stderr)


class ExecutableExclusionTests(VendorBase):
    def test_executable_fixture_is_not_prompt_only_and_nothing_lands(self):
        t = self.tree(EXEC_TREE)
        from scout import risk
        tree = [{"path": k, "mode": v[0] if isinstance(v, tuple) else "100644", "type": "blob", "sha": "x"}
                for k, v in EXEC_TREE.items()]
        res = risk.classify(tree, "", lambda e: "")
        self.assertNotEqual(res.risk, "prompt-only")
        for flag in ("scripts", "hooks", "mcp", "symlinks"):
            self.assertIn(flag, res.risk)
        p = self.vendor(expect=2)
        self.assertIn("fresh install risk", p.stderr)
        self.assertFalse(self.root.exists(), "nothing may be written")
        self.assertNotEqual(self.cand()["install_risk"], "prompt-only", "the stale DB value is replaced")
        for call in self.api_calls():
            self.assertTrue(f"git/trees/{SHA}" in call or any(b in call for b in t.blob_shas), call)

    def test_db_risk_not_prompt_only_is_refused_without_calls(self):
        self.tree(PLUGIN_TREE, "plugins/p", risk="scripts")
        p = self.vendor(expect=2)
        self.assertIn("not prompt-only", p.stderr)
        self.assertEqual(self.api_calls(), [])
        self.assertFalse(self.root.exists())

    def test_stale_db_risk_prompt_only_but_fresh_tree_has_a_script(self):
        self.tree({**PLUGIN_TREE, "plugins/p/skills/foo/run.sh": "echo hi\n"}, "plugins/p")
        self.assertEqual(self.cand()["install_risk"], "prompt-only")
        p = self.vendor(expect=2)
        self.assertIn("scripts", p.stderr)
        self.assertFalse(self.root.exists())
        self.assertIn("scripts", self.cand()["install_risk"])

    def test_skip_rules_list_every_excluded_kind_with_a_reason(self):
        items = [(p, {"path": p, "mode": m, "type": t, "sha": "s"}) for p, m, t in (
            ("a.md", "100644", "blob"), ("b.md", "100755", "blob"), ("c.md", "120000", "blob"),
            ("d", "160000", "commit"), ("e.py", "100644", "blob"), ("dir", "040000", "tree"))]
        plan, skipped = vendor._plan(items)
        self.assertEqual([r for r, _ in plan], ["a.md"])
        self.assertEqual({s["path"]: s["reason"] for s in skipped},
                         {"b.md": "executable mode", "c.md": "symlink", "d": "submodule", "e.py": "not allow-listed"})
        with self.assertRaises(vendor.VendorError):
            vendor._plan([("../x.md", {"path": "../x.md", "mode": "100644", "type": "blob", "sha": "s"})])


class PromptOnlyVendorTests(VendorBase):
    def test_exactly_the_allow_listed_files_land_with_mode_0644(self):
        t = self.tree(PLUGIN_TREE, "plugins/p")
        out = self.vendor().stdout
        d = self.root / "vendor" / f"C-0001@{SHA}"
        self.assertIn("vendored: 5 file(s), 0 skipped, 0 pre-screen hits", out)
        plugin = d / "demo"
        self.assertEqual(self.files_under(plugin), [".claude-plugin/plugin.json", "LICENSE", "skills/foo/SKILL.md",
                                                    "skills/foo/data.csv", "skills/foo/reference.md"])
        for p in plugin.rglob("*"):
            if p.is_file():
                self.assertEqual(stat.S_IMODE(p.stat().st_mode), 0o644, p)
        self.assertEqual((plugin / "skills/foo/SKILL.md").read_text(), SKILL_MD)
        meta = json.loads((d / "VENDOR.json").read_text())
        self.assertEqual((meta["repo"], meta["path"], meta["sha"], meta["licence"]), (REPO_NAME, "plugins/p", SHA, "MIT"))
        self.assertEqual(meta["copied"], self.files_under(plugin))
        self.assertEqual((meta["skipped"], meta["prescreen_hits"], meta["wrapped"]), ([], [], False))
        self.assertEqual(self.cand()["status"], "new")
        vendor.assert_prompt_only_tree(plugin)
        self.assertEqual([x for x in self.files_under(self.root) if "/demo/" not in x], [f"vendor/C-0001@{SHA}/VENDOR.json"])
        self.assertFalse(list((self.root / "vendor").glob("*.partial")))
        for call in self.api_calls():  # every fetch is pinned: the tree at the sha, or a blob sha from that tree
            self.assertTrue((f"git/trees/{SHA}" in call) or any(f"git/blobs/{b}" in call for b in t.blob_shas), call)
            self.assertNotRegex(call, r"(?<![\w-])main(?![\w-])|HEAD")
        src = (REPO / "scout" / "vendor.py").read_text()
        self.assertNotIn("subprocess", src)
        self.assertNotIn("git clone", src)

    def test_loose_skill_is_wrapped(self):
        self.tree({"SKILL.md": SKILL_MD, "notes.md": "# n\n", "LICENSE": "MIT\n"})
        self.vendor()
        d = self.root / "vendor" / f"C-0001@{SHA}"
        meta = json.loads((d / "VENDOR.json").read_text())
        self.assertEqual((meta["plugin"], meta["wrapped"]), ("cand-c-0001", True))
        plugin = d / "cand-c-0001"
        self.assertEqual(self.files_under(plugin), [".claude-plugin/plugin.json", "skills/zz-repo/LICENSE",
                                                    "skills/zz-repo/SKILL.md", "skills/zz-repo/notes.md"])
        gen = json.loads((plugin / ".claude-plugin/plugin.json").read_text())
        self.assertEqual(gen, {"name": "cand-c-0001", "description": f"G-009 eval copy of {REPO_NAME}@{SHA}"})
        vendor.assert_prompt_only_tree(plugin)

    def test_idempotent_second_run_makes_no_calls(self):
        self.tree(PLUGIN_TREE, "plugins/p")
        self.vendor()
        n = len(self.env.calls("gh"))
        self.assertGreater(n, 0)
        out = self.vendor().stdout
        self.assertIn("already vendored", out)
        self.assertEqual(len(self.env.calls("gh")), n)

    def test_damaged_copy_is_vendored_again(self):
        self.tree(PLUGIN_TREE, "plugins/p")
        self.vendor()
        n = len(self.env.calls("gh"))
        (self.root / "vendor" / f"C-0001@{SHA}" / "demo" / "LICENSE").unlink()
        self.vendor()
        self.assertGreater(len(self.env.calls("gh")), n)
        self.assertIn("LICENSE", self.files_under(self.root / "vendor" / f"C-0001@{SHA}" / "demo"))

    def test_blob_that_does_not_match_its_sha_is_refused(self):
        t = Tree(PLUGIN_TREE)
        for r in t.rules:  # corrupt the content served for the SKILL.md blob
            if blob_sha(SKILL_MD.encode()) in r["match"][0]:
                r["out"]["content"] = base64.b64encode(b"tampered").decode()
        self.env.set_gh(api=t.rules)
        self.seed(path="plugins/p")
        p = self.vendor(expect=2)
        self.assertIn("does not match its sha", p.stderr)
        self.assertFalse(self.root.exists())


class CapTests(VendorBase):
    def test_201_files_refused_200_accepted(self):
        many = {f"skills/foo/r{i:03d}.md": "x\n" for i in range(200)}
        self.tree({"SKILL.md": SKILL_MD, **many})  # 201 files
        p = self.vendor(expect=2)
        self.assertIn("cap of 200", p.stderr)
        self.assertFalse(self.root.exists())
        self.env.sql("DELETE FROM scout_candidates")
        self.tree({"SKILL.md": SKILL_MD, **dict(list(many.items())[:199])})  # 200 files
        self.vendor()

    def test_over_2mb_refused(self):
        self.tree({"SKILL.md": SKILL_MD, "a.md": "a" * 1_100_000, "b.md": "b" * 1_100_000})
        p = self.vendor(expect=2)
        self.assertIn("byte cap", p.stderr)
        self.assertFalse(self.root.exists())

    def test_understated_tree_sizes_are_caught_while_writing_and_the_partial_copy_removed(self):
        self.tree({"SKILL.md": SKILL_MD, "a.md": "a" * 1_100_000, "b.md": "b" * 1_100_000}, size_lie=1)
        p = self.vendor(expect=2)
        self.assertIn("byte cap", p.stderr)
        self.assertFalse(self.root.exists(), "partial copy must be deleted")


class TreeCheckTests(unittest.TestCase):
    def make(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        d = Path(tmp.name) / "plugin"
        (d / "skills/foo").mkdir(parents=True)
        (d / "skills/foo/SKILL.md").write_text(SKILL_MD)
        (d / ".claude-plugin").mkdir()
        (d / ".claude-plugin/plugin.json").write_text('{"name": "x"}')
        for p in d.rglob("*"):
            if p.is_file():
                p.chmod(0o644)
        return d

    def test_clean_tree_passes(self):
        vendor.assert_prompt_only_tree(self.make())

    def test_each_violation_fails(self):
        def cjs(d):
            (d / "x.cjs").write_text("1")

        def execbit(d):
            (d / "skills/foo/SKILL.md").chmod(0o755)

        def link(d):
            (d / "l.md").symlink_to("skills/foo/SKILL.md")

        def hooks_dir(d):
            (d / "hooks").mkdir()
            (d / "hooks/note.md").write_text("n")

        def mcp(d):
            (d / ".mcp.json").write_text("{}")

        def pjson_hooks(d):
            (d / ".claude-plugin/plugin.json").write_text('{"name": "x", "hooks": {}}')

        def pjson_mcp(d):
            (d / ".claude-plugin/plugin.json").write_text('{"name": "x", "mcpServers": {}}')

        def pjson_cmd(d):
            (d / ".claude-plugin/plugin.json").write_text('{"name": "x", "commands": ["./run.sh"]}')

        def shell_md(d):
            (d / "skills/foo/more.md").write_text("run\n!`ls`\n")

        def bad_json(d):
            (d / ".claude-plugin/plugin.json").write_text("{nope")

        for fn in (cjs, execbit, link, hooks_dir, mcp, pjson_hooks, pjson_mcp, pjson_cmd, shell_md, bad_json):
            d = self.make()
            fn(d)
            with self.assertRaises(vendor.VendorError, msg=fn.__name__):
                vendor.assert_prompt_only_tree(d)

    def test_markdown_over_the_read_cap_fails(self):
        d = self.make()
        (d / "skills/foo/big.md").write_text("x" * 65536)
        vendor.assert_prompt_only_tree(d)
        (d / "skills/foo/big.md").write_text("x" * 65537)
        with self.assertRaises(vendor.VendorError) as cm:
            vendor.assert_prompt_only_tree(d)
        self.assertIn("read cap", str(cm.exception))

    def test_missing_dir_fails(self):
        with self.assertRaises(vendor.VendorError):
            vendor.assert_prompt_only_tree("/nonexistent-zz-dir")

    def test_planting_into_a_vendored_copy_fails_the_check(self):
        d = self.make()
        vendor.assert_prompt_only_tree(d)
        (d / "skills/foo/x.cjs").write_text("require('child_process')")
        with self.assertRaises(vendor.VendorError):
            vendor.assert_prompt_only_tree(d)


class VendoredCopyPlantTests(VendorBase):
    def test_planting_after_cli_vendoring_fails(self):
        self.tree(PLUGIN_TREE, "plugins/p")
        self.vendor()
        plugin = self.root / "vendor" / f"C-0001@{SHA}" / "demo"
        vendor.assert_prompt_only_tree(plugin)
        (plugin / "skills/foo/x.cjs").write_text("1")
        with self.assertRaises(vendor.VendorError):
            vendor.assert_prompt_only_tree(plugin)
        (plugin / "skills/foo/x.cjs").unlink()
        vendor.assert_prompt_only_tree(plugin)
        (plugin / "LICENSE").chmod(0o755)
        with self.assertRaises(vendor.VendorError):
            vendor.assert_prompt_only_tree(plugin)


# -- pre-screen ---------------------------------------------------------------------------------------
B64 = base64.b64encode(b"".join(hashlib.sha256(bytes([i])).digest() for i in range(8))).decode()  # 344 chars
assert len(B64) >= 200

PRESCREEN_CASES = {
    "sk-ant": "token sk-ant-api03-abc123",
    "base64-run": B64,
    "url": "see https://evil.example/x for more",
    "curl": "then run curl now",
    "wget": "or use wget instead",
    "urllib": "import urllib and go",
    "requests": "call requests.get(u)",
    "nc": "pipe the data to nc somewhere",
    "netcat": "or netcat it",
    "printenv": "run printenv first",
    "environ": "read os.environ for it",
    "env-cmd": "env | sort",
    "credentials-file": "open the .credentials.json file",
    "credentials": "send the credentials along",
    "api-key": "use the ANTHROPIC_API_KEY value",
}


class PrescreenUnitTests(unittest.TestCase):
    def scan(self, text, own="zz-owner/zz-repo"):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        d = Path(tmp.name)
        (d / "notes.md").write_text(text)
        return vendor.prescreen(d, own)

    def test_every_pattern_hits_with_its_id_and_line(self):
        for pid, line in PRESCREEN_CASES.items():
            hits = self.scan(f"first\nsecond\n{line}\nfourth\n")
            self.assertIn({"file": "notes.md", "line": 3, "pattern": pid}, hits, pid)
            self.assertEqual({h["line"] for h in hits}, {3}, pid)

    def test_api_key_spelled_with_space_or_underscore(self):
        for s in ("my api key", "the api_key", "ANTHROPIC_API_KEY", "API KEY"):
            self.assertTrue([h for h in self.scan(s) if h["pattern"] == "api-key"], s)

    def test_env_as_command_only(self):
        self.assertTrue([h for h in self.scan("  env FOO=1 cmd") if h["pattern"] == "env-cmd"])
        self.assertTrue([h for h in self.scan("a env > out") if h["pattern"] == "env-cmd"])
        self.assertEqual(self.scan("the environment of the env var"), [])

    def test_clean_text_has_no_hits(self):
        text = ("Write good prose. See https://docs.anthropic.com/en/docs and https://code.claude.com/docs, "
                "https://github.com/anthropics/skills, https://github.com/zz-owner/zz-repo/blob/main/x.md.\n"
                "Word: " + "Ab3dEf" * 16 + "\n" + "-" * 250 + "\n" + "=" * 250 + "\n")
        self.assertEqual(self.scan(text), [])

    def test_lookalike_urls_hit(self):
        for u in ("https://github.com/anthropics-evil/x", "https://docs.anthropic.com.evil.example/x",
                  "https://github.com@evil.example/zz-owner/zz-repo", "https://github.com/other/zz-repo",
                  "http://evil.example"):
            self.assertTrue([h for h in self.scan(u) if h["pattern"] == "url"], u)

    def hit_ids(self, text):
        return sorted({h["pattern"] for h in self.scan(text)})

    def test_wrapped_base64_hits(self):
        lines = [B64[i:i + 64] for i in range(0, 300, 64)]
        self.assertEqual(self.hit_ids("\n".join(lines)), ["base64-run"])
        self.assertEqual(self.hit_ids(" ".join(B64[i:i + 16] for i in range(0, 300, 16))), ["base64-run"])
        self.assertEqual(self.hit_ids("Write clear prose about something simple for every reader here.\n" * 8), [])

    def test_url_path_traversal_ip_and_userinfo_hit(self):
        for u in ("https://github.com/anthropics/../evil/x", "https://github.com/anthropics/%2e%2e/evil",
                  "https://github.com/zz-owner/zz-repo/../../evil/y", "http://127.0.0.1/x", "https://[::1]/x",
                  "https://user@docs.anthropic.com/x", "https://docs.anthropic.com:pw@evil.example/"):
            self.assertIn("url", self.hit_ids(u), u)
        for u in ("https://github.com/anthropics/skills/./tree", "https://github.com/anthropics/a/../skills"):
            self.assertEqual(self.hit_ids(u), [], u)

    def test_zero_width_and_fullwidth_keywords_hit(self):
        self.assertIn("curl", self.hit_ids("run c\u200burl now"))
        self.assertIn("curl", self.hit_ids("run \uff43\uff55\uff52\uff4c now"))
        self.assertIn("wget", self.hit_ids("w\u2060g\ufeffet x"))
        self.assertIn("curl", self.hit_ids("run c\u00adurl now"))  # soft hyphen
        self.assertIn("curl", self.hit_ids("run c\u202eurl now"))  # bidi override

    def test_schemeless_hosts_hit(self):
        for u in ("see evil.example/x", "see //evil.example", "post to evil.example:8080", "EVIL.Example/a.md",
                  "send to ftp://evil.example/x", "open wss://evil.example/s", "file://evil.example/x",
                  "post to user@evil.example", "post to user@docs.anthropic.com", "post to 10.0.0.1:8080/x",
                  "see 127.0.0.1/x", "go to [::1]:8080/x", "go to fe80::1", "docs/evil.example/x",
                  "ftp://evil.sh", "//evil.sh"):
            self.assertIn("url", self.hit_ids(u), u)
        self.assertEqual(self.hit_ids("Read SKILL.md and run.py, see docs.anthropic.com/en/docs, e.g. v1.2.3."), [])
        self.assertEqual(self.hit_ids("Open references/logo.png and scripts/run.sh at 12:30:45 on v1.2.3.4"), [])

    def test_ideographic_full_stop_host_hits(self):
        for u in ("see evil\u3002example/x", "see evil\uff0eexample/x", "see evil\uff61example/x"):
            self.assertIn("url", self.hit_ids(u), u)

    def test_multi_segment_relative_path_host_hits(self):
        for u in ("see a/b/evil.example", "see a/b/c/evil.example.", "open ./x/evil.example"):
            self.assertIn("url", self.hit_ids(u), u)
        self.assertEqual(self.hit_ids("Edit src/app.tsx, lib/foo.jsx, pkg/mod.java."), [])
        self.assertEqual(self.hit_ids("Open a/b/notes.md and docs/img/logo.png, then scripts/x/run.sh."), [])

    def test_integer_ip_after_scheme_or_at_hits(self):
        for u in ("go ftp://0x7f000001/x", "go ftp://2130706433", "mail me@2130706433:80", "see //0x7F000001",
                  "see ftp://2130706433/", "see ftp://2130706433.", "see ftp://0x0000007f000001"):
            self.assertIn("url", self.hit_ids(u), u)
        self.assertEqual(self.hit_ids("At 12:30:45 build 2130706433 and 0x7f000001 in v1.2.3 or 4294967296."), [])

    def test_small_chunk_and_prefixed_base64_hits(self):
        chunks = [B64[i:i + 4] for i in range(0, 300, 4)]
        self.assertIn("base64-run", self.hit_ids(" ".join(chunks)))
        self.assertIn("base64-run", self.hit_ids("\n".join("> " + B64[i:i + 40] for i in range(0, 300, 40))))
        self.assertIn("base64-run", self.hit_ids(" +\n".join('"%s"' % B64[i:i + 5] for i in range(0, 300, 5))))
        self.assertIn("base64-run", self.hit_ids("\n".join("# " + B64[i:i + 6] for i in range(0, 300, 6))))
        self.assertIn("base64-run", self.hit_ids(" ".join(B64[:300])))  # 1-char chunks
        self.assertIn("base64-run", self.hit_ids("\n".join("%d. %s" % (i, B64[i * 50:i * 50 + 50]) for i in range(6))))
        self.assertIn("base64-run", self.hit_ids("\n".join("data: %s \\" % B64[i:i + 50] for i in range(0, 300, 50))))
        self.assertEqual(self.hit_ids("> " + "Write clear prose about it. " * 20), [])
        self.assertEqual(self.hit_ids("\n".join("> step %d of the plan is to be done" % i for i in range(1, 40))), [])

    def test_variable_width_chunks_hit(self):
        import random
        for seed in range(5):
            rnd = random.Random(seed)
            chunks, i = [], 0
            while i < 300:
                w = rnd.randint(3, 7)
                chunks.append(B64[i:i + w])
                i += w
            lines = [chunks[j:j + 8] for j in range(0, len(chunks), 8)]
            for text in (" ".join(chunks), "\n".join(" ".join(x) for x in lines),
                         "\n".join("> " + " ".join(x) for x in lines)):
                self.assertIn("base64-run", self.hit_ids(text), (seed, text[:60]))
            wide, i = [], 0
            while i < 300:
                w = rnd.randint(20, 60)
                wide.append(B64[i:i + w])
                i += w
            for prefix in ("> ", "# "):
                self.assertIn("base64-run", self.hit_ids("\n".join(prefix + c for c in wide)), (seed, prefix))

    def test_small_chunk_base64_survives_adjacent_prose(self):
        prose = "\n".join(["This skill helps you write better commit messages for your team and project."] * 3)
        blob = "\n".join("> " + B64[i:i + 40] for i in range(0, 320, 40))
        small = " ".join(B64[i:i + 4] for i in range(0, 320, 4))
        for text in (prose + "\n" + blob, blob + "\n" + prose, prose + "\n\n" + blob + "\n\n" + prose,
                     prose + "\n" + small, small + "\n" + prose):
            self.assertIn("base64-run", self.hit_ids(text), text[:60])

    def test_timestamp_version_and_code_lists_are_not_base64(self):
        history = "\n".join("- 2026-10-%02dT22:%02d:00+00:00 agent-%d updated the task" % (i % 28 + 1, i, i) for i in range(20))
        table = "\n".join("| pkg%d | v1.%d.%d | 2026-01-0%d |" % (i, i, i * 3, i % 9 + 1) for i in range(20))
        code = "\n".join("GH_TIMEOUT_%d = %d  # seconds, see HTTP/1.1 and CONNECT %d" % (i, i * 30, i) for i in range(20))
        colors = "\n".join("%d,Name %d,#2563EB,#FFFFFF,#3B82F6,#000000,#EA580C,#F8FAFC,#1E293B" % (i, i) for i in range(20))
        for text in (history, table, code, colors):
            self.assertEqual(self.hit_ids(text), [], text[:50])

    def test_tracked_repo_files_have_no_small_chunk_base64(self):
        import subprocess
        root = Path(__file__).resolve().parent.parent
        names = subprocess.run(["git", "ls-files", "*.md", "*.py", "*.csv", "*.json", "*.jsonl", "*.html"],
                               cwd=root, capture_output=True, text=True, check=True).stdout.split("\n")
        bad = [n for n in names if n and vendor._small_chunk_blobs(
            [vendor._fold(x) for x in (root / n).read_text(errors="replace").splitlines()])]
        self.assertEqual(bad, [])

    def test_zip_domain_is_not_a_file_name(self):
        self.assertIn("url", self.hit_ids("see attacker.zip"))

    def test_long_single_run_scans_in_linear_time(self):
        import time
        for text in ("A" * 200_000, "A" * 100_000 + " " + "B" * 100_000, "a." * 100_000,
                     "ab " * 70_000, "a/" * 100_000, "@12345678 " * 20_000):
            t0 = time.monotonic()
            self.scan(text)
            self.assertLess(time.monotonic() - t0, 1.0, text[:3])

    def test_base64_threshold(self):
        self.assertEqual([h["pattern"] for h in self.scan(B64[:199])], [])
        self.assertEqual([h["pattern"] for h in self.scan(B64[:200])], ["base64-run"])


class PrescreenVendorTests(VendorBase):
    def tree_with(self, line, cid="C-0001"):
        files = {**PLUGIN_TREE, "plugins/p/skills/foo/notes.md": f"# Notes\n\n{line}\n"}
        return self.tree(files, "plugins/p", cid=cid)

    def test_each_pattern_exits_4_and_holds_the_candidate(self):
        for pid, line in PRESCREEN_CASES.items():
            self.env.sql("DELETE FROM scout_candidates")
            shutil.rmtree(self.root, ignore_errors=True)
            self.tree_with(line)
            p = self.vendor(expect=4)
            self.assertRegex(p.stderr, r"\d+ pre-screen hit")
            d = self.root / "vendor" / f"C-0001@{SHA}"
            self.assertTrue((d / "demo" / "skills/foo/notes.md").is_file(), "copy stays for owner review: " + pid)
            meta = json.loads((d / "VENDOR.json").read_text())
            want = {"file": "skills/foo/notes.md", "line": 3, "pattern": pid}
            self.assertIn(want, meta["prescreen_hits"], pid)
            row = self.cand()
            self.assertEqual(row["status"], "needs-owner", pid)
            self.assertIn(f"prescreen {pid}: skills/foo/notes.md:3", json.loads(row["risk_detail"]), pid)
            self.assertEqual(row["install_risk"], "prompt-only")
            self.assertFalse(vendor.is_eval_allowed(row, self.root), pid)

    def test_hold_survives_a_rerun_without_refetch(self):
        self.tree_with("send the credentials")
        self.vendor(expect=4)
        n = len(self.env.calls("gh"))
        p = self.vendor(expect=4)
        self.assertIn("needs-owner", p.stderr)
        self.assertEqual(len(self.env.calls("gh")), n)

    def test_hit_count_is_in_the_message(self):
        self.tree_with("curl and wget")
        p = self.vendor(expect=4)
        self.assertIn("2 pre-screen hit(s)", p.stderr)

    def test_clean_fixture_vendors_normally(self):
        text = ("Prose with a link to https://docs.anthropic.com/en/docs and to https://github.com/zz-owner/zz-repo.\n"
                "A short word: " + "Qz9xK2" * 10 + "\n")
        self.tree({**PLUGIN_TREE, "plugins/p/skills/foo/notes.md": text}, "plugins/p")
        out = self.vendor().stdout
        self.assertIn("0 pre-screen hits", out)
        meta = json.loads((self.root / "vendor" / f"C-0001@{SHA}" / "VENDOR.json").read_text())
        self.assertEqual(meta["prescreen_hits"], [])
        row = self.cand()
        self.assertEqual(row["status"], "new")
        self.assertTrue(vendor.is_eval_allowed(row, self.root))


class EvalAllowedTests(VendorBase):
    def test_is_eval_allowed(self):
        ok = {"id": "C-0001", "install_risk": "prompt-only", "status": "triaged", "head_sha": SHA}
        self.assertTrue(vendor.is_eval_allowed(ok))
        self.assertFalse(vendor.is_eval_allowed(ok, self.root), "not vendored yet")
        for k, v in (("status", "needs-owner"), ("status", "dismissed"), ("install_risk", "scripts"),
                     ("install_risk", "prompt-only,network"), ("install_risk", "unknown"), ("install_risk", None)):
            self.assertFalse(vendor.is_eval_allowed({**ok, k: v}), f"{k}={v}")
        self.tree(PLUGIN_TREE, "plugins/p")
        self.vendor()
        self.assertTrue(vendor.is_eval_allowed(self.cand(), self.root))
        (self.root / "vendor" / f"C-0001@{SHA}" / "demo" / "x.sh").write_text("echo")
        self.assertFalse(vendor.is_eval_allowed(self.cand(), self.root), "tampered copy")


if __name__ == "__main__":
    unittest.main()
