import json
import os
import re
import unittest

from harness import REPO, FactoryTestCase, ROLES

PLUGINS_ROOT = REPO / "roles" / "plugins"
PLUGINS = PLUGINS_ROOT / "staff-engineer"
WITH_PLUGINS = {"coder", "director", "lead", "pm", "qa", "reviewer", "staff-engineer"}
BAD_EXT = {".sh", ".py", ".js", ".ts", ".cmd", ".ps1", ".html"}
BAD_NAMES = {"hooks", "hooks.json", ".mcp.json"}
NAMED_WORKFLOW = REPO / "roles" / "workflows" / "multi-angle-review.js"
NAMED_WORKFLOW_LINK = "/workflows/multi-angle-review.js"
PROBE_WORKFLOW = REPO / "roles" / "workflows" / "guard-probe.js"
PROBE_WORKFLOW_LINK = "/qa/qa-skills/workflows/guard-probe.js"  # qa plugin only (tests/test_guard_probe.py)
# the CLI refuses `factory spawn evaluator` (only the skilleval harness spawns it): tests/test_scout_roles.py
CLI_SPAWNABLE = [r for r in ROLES if r != "evaluator"]


def spawn_cmd(role):
    base = ["spawn", role, "--name", f"zz-test-{role}"]
    if role in ("coder", "qa", "reviewer"):
        base += ["--goal", "G-001", "--task", "T-0001"]
    return base


class PluginDirSpawnTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_team(lead="zz-test-lead")
        self.env.seed_task("T-0001", role="coder", status="todo", pr_url=None, team="g-001-a")
        self.env.run("config", "max_live_agents", "99")
        self.env.run("config", "max_total_claude", "99")  # one pane per role now exceeds the default 10

    def test_plugin_dir_iff_role_dir_exists(self):
        for role in CLI_SPAWNABLE:
            with self.subTest(role=role):
                self.ok(*spawn_cmd(role))
        starts = {c["argv"][2]: c["argv"] for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "start"]}
        for role in CLI_SPAWNABLE:
            argv = starts[f"zz-test-{role}"]
            flags = [i for i, a in enumerate(argv) if a == "--plugin-dir"]
            pdir = PLUGINS_ROOT / role
            if pdir.is_dir():
                self.assertEqual(len(flags), 1, argv)
                self.assertEqual(os.path.realpath(argv[flags[0] + 1]), str(pdir))
            else:
                self.assertEqual(flags, [], f"{role} got --plugin-dir: {argv}")
            self.assertEqual(role in WITH_PLUGINS, bool(flags), f"{role}: {argv}")


def role_trees():
    """roles/plugins/<role>/ for each real role; skips 'goals' and any name not in ROLES."""
    return [p for p in sorted(PLUGINS_ROOT.iterdir()) if p.is_dir() and p.name in ROLES]


class VendoredPluginsStaticTests(unittest.TestCase):
    def test_every_child_is_a_plugin(self):
        kids = {t.name: sorted(p.name for p in t.iterdir() if p.is_dir()) for t in role_trees()}
        self.assertEqual(kids, {
            "coder": ["coder-skills"],
            "director": ["factory-director"],
            "lead": ["factory-lead"],
            "pm": ["pm-skills"],
            "qa": ["qa-skills"],
            "reviewer": ["reviewer-skills"],
            "staff-engineer": ["feature-dev", "pr-review-toolkit", "staff-engineer-skills"],
        })
        for tree in role_trees():
            for k in kids[tree.name]:
                pj = tree / k / ".claude-plugin" / "plugin.json"
                self.assertTrue(pj.is_file(), f"{tree.name}/{k}")
                self.assertTrue(json.loads(pj.read_text()).get("name"), f"{tree.name}/{k}")

    def test_lead_skills_front_matter(self):
        skills = PLUGINS_ROOT / "lead" / "factory-lead" / "skills"
        self.assertEqual(sorted(p.name for p in skills.iterdir()), ["audit-round", "blocked-worker", "fix-loop", "park", "pr-triage", "spec-change", "worker-brief"])
        for d in skills.iterdir():
            text = (d / "SKILL.md").read_text()
            m = re.match(r"---\n(.*?)\n---\n", text, re.S)
            self.assertTrue(m, d.name)
            fm = m.group(1)
            self.assertEqual(re.search(r"^name:\s*(\S+)\s*$", fm, re.M).group(1), d.name)
            desc = re.search(r"^description:\s*(.+)$", fm, re.M).group(1)
            self.assertIn("Use when", desc, d.name)
            self.assertLess(len(text.splitlines()), 70, d.name)

    def test_lead_skill_contents(self):
        skills = PLUGINS_ROOT / "lead" / "factory-lead" / "skills"
        sc = (skills / "spec-change" / "SKILL.md").read_text()
        for needle in ("factory stop", "--acceptance", "factory spawn"):
            self.assertIn(needle, sc)
        wb = (skills / "worker-brief" / "SKILL.md").read_text()
        for needle in ("factory inbox", "factory verdict", "goal show", "merge origin/main", "Test count:"):
            self.assertIn(needle, wb)
        for needle in ("pilot-test-first", "docs/test-first-pilot.md"):
            self.assertIn(needle, wb)
        pt = (skills / "pr-triage" / "SKILL.md").read_text()
        self.assertIn("gh pr view", pt)
        self.assertIn("mergeable", pt)
        self.assertLess(pt.index("gh pr view"), pt.index("gh pr checks"))

    def test_test_first_pilot_doc(self):
        doc = (REPO / "docs" / "test-first-pilot.md").read_text()
        for needle in ("pilot-test-first", "byte-identical", "factory src origin/main", "grep '^-' | grep -v '^--- a/'", "5"):
            self.assertIn(needle, doc)

    def test_no_code_hooks_or_mcp(self):
        bad = []
        for root, dirs, files in (w for t in role_trees() for w in os.walk(t)):
            for n in dirs + files:
                p = os.path.join(root, n)
                if n in BAD_NAMES or os.path.splitext(n)[1].lower() in BAD_EXT:
                    if p.endswith(NAMED_WORKFLOW_LINK) and os.path.realpath(p) == str(NAMED_WORKFLOW):
                        continue  # the one allowed .js: a symlink to the single vendored workflow (T-0366)
                    if p.endswith(PROBE_WORKFLOW_LINK) and os.path.realpath(p) == str(PROBE_WORKFLOW):
                        continue  # the qa-only guard-probe symlink (T-0751)
                    bad.append(p)
            for n in files:
                p = os.path.join(root, n)
                if os.access(p, os.X_OK) or os.stat(p).st_mode & 0o111:
                    bad.append(p + " (executable)")
                if n == "plugin.json":
                    with open(p) as fh:
                        data = json.load(fh)
                    for key in ("hooks", "mcpServers"):
                        if key in data:
                            bad.append(f"{p} has {key}")
        self.assertEqual(bad, [])

    def test_output_rewired_to_factory_report(self):
        skills = PLUGINS / "staff-engineer-skills" / "skills"
        for name in ("improve-codebase-architecture", "differential-review"):
            text = "\n".join(p.read_text() for p in (skills / name).rglob("*.md"))
            self.assertIn("factory report", text, name)
            self.assertNotRegex(text, r"(?i)tailwind|cdn\.|mermaid|HTML-REPORT|<html")
        da = (skills / "differential-review" / "SKILL.md").read_text()
        tools = re.search(r"^allowed-tools:(.*)$", da, re.M).group(1).split()
        self.assertNotIn("Write", tools)
        self.assertTrue({"Read", "Grep", "Glob", "Bash"} <= set(tools))
        lic = (PLUGINS / "staff-engineer-skills" / "LICENSE-trailofbits").read_text()
        for needle in ("github.com/trailofbits/skills", "196a9073ab10d35bacf111f945e02c00b2c6883a", "CC-BY-SA-4.0",
                       "output rewired to factory report; Write removed"):
            self.assertIn(needle, lic + da)

    def test_no_file_output_instructions(self):
        bad = []
        pat = re.compile(r"(?i)output file|create[^.\n]{0,30}lazily|(add|update|write)[^.\n]{0,30}GLOSSARY\.md|grilling")
        skills = PLUGINS / "staff-engineer-skills" / "skills"
        for name in ("improve-codebase-architecture", "differential-review"):
            for f in (skills / name).rglob("*.md"):
                for n, line in enumerate(f.read_text().splitlines(), 1):
                    if "Modified:" not in line and pat.search(line):
                        bad.append(f"{f.relative_to(PLUGINS)}:{n}: {line.strip()}")
        self.assertEqual(bad, [])
        for f in (skills / "systematic-debugging").iterdir():
            self.assertFalse(re.match(r"(CREATION-LOG|test-)", f.name), f.name)

    def test_licences_present(self):
        for f in ("LICENSE-mattpocock", "LICENSE-trailofbits", "LICENSE-superpowers"):
            self.assertTrue((PLUGINS / "staff-engineer-skills" / f).stat().st_size > 500, f)
        for p in ("feature-dev", "pr-review-toolkit"):
            self.assertTrue((PLUGINS / p / "LICENSE").is_file())


if __name__ == "__main__":
    unittest.main()
