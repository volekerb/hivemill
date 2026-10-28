import json
import os
import re
import unittest

from harness import REPO

GOALS_ROOT = REPO / "roles" / "plugins" / "goals"
README = GOALS_ROOT / "README.md"
# test_plugins.py's blacklist, extended
BAD_EXT = {".sh", ".py", ".js", ".ts", ".cmd", ".ps1", ".html", ".mjs", ".cjs", ".exe"}
BAD_NAMES = {"hooks", "hooks.json", ".mcp.json"}
OK_EXT = {".md", ".json", ".csv", ".txt"}
CDN_RE = re.compile(r"fonts\.googleapis|cdn\.|unpkg|jsdelivr|tailwindcss\.com")
RUN_RE = re.compile(r"python3? .*\.py|bash .*\.sh")
SHA_RE = re.compile(r"\b[0-9a-f]{40}\b")


def goal_plugins():
    return [p for g in sorted(GOALS_ROOT.iterdir()) if g.is_dir() for p in sorted(g.iterdir()) if p.is_dir()]


def files(root):
    return [os.path.join(d, f) for d, _, fs in os.walk(root) for f in fs]


def text_files():
    out = []
    for g in sorted(GOALS_ROOT.iterdir()):
        if g.is_dir():
            out += [f for f in files(g) if os.path.splitext(f)[1] in OK_EXT or os.path.basename(f).startswith("LICENSE")]
    return out


class GoalPluginContentTests(unittest.TestCase):
    def test_plugins_exist(self):
        self.assertTrue((GOALS_ROOT / "G-008" / "ui-ux-pro-max").is_dir())

    def test_every_child_is_a_plugin(self):
        for p in goal_plugins():
            pj = p / ".claude-plugin" / "plugin.json"
            with self.subTest(plugin=str(p.relative_to(GOALS_ROOT))):
                self.assertTrue(pj.is_file())
                data = json.loads(pj.read_text())
                self.assertTrue(data.get("name"))
                self.assertEqual(data["name"], p.name)

    def test_no_hooks_or_mcp(self):
        bad = []
        for g in GOALS_ROOT.iterdir():
            if not g.is_dir():
                continue
            for root, dirs, fs in os.walk(g):
                for n in dirs + fs:
                    if n in BAD_NAMES:
                        bad.append(os.path.join(root, n))
            for f in files(g):
                if os.path.basename(f) == "plugin.json":
                    data = json.loads(open(f).read())
                    for key in ("hooks", "mcpServers"):
                        if key in data:
                            bad.append(f"{f} has {key}")
        self.assertEqual(bad, [])

    def test_only_text_and_data_files(self):
        bad = []
        for g in GOALS_ROOT.iterdir():
            if not g.is_dir():
                continue
            for f in files(g):
                n = os.path.basename(f)
                ext = os.path.splitext(n)[1].lower()
                if ext in BAD_EXT:
                    bad.append(f)
                elif ext not in OK_EXT and not n.startswith("LICENSE"):
                    bad.append(f)
                if os.stat(f).st_mode & 0o111:
                    bad.append(f + " (executable)")
                # data JSON only as plugin.json inside .claude-plugin
                if ".claude-plugin" in f.split(os.sep) and n != "plugin.json":
                    bad.append(f + " (extra file in .claude-plugin)")
        self.assertEqual(bad, [])

    def test_no_cdn_or_font_urls(self):
        hits = []
        for f in text_files():
            for i, line in enumerate(open(f, encoding="utf-8"), 1):
                if CDN_RE.search(line):
                    hits.append(f"{f}:{i}")
        self.assertEqual(hits, [])

    def test_no_script_run_instructions(self):
        hits = []
        for f in text_files():
            for i, line in enumerate(open(f, encoding="utf-8"), 1):
                if RUN_RE.search(line):
                    hits.append(f"{f}:{i}")
        self.assertEqual(hits, [])

    def test_license_present(self):
        for p in goal_plugins():
            lic = [f for f in p.iterdir() if f.is_file() and f.name.startswith("LICENSE")]
            with self.subTest(plugin=p.name):
                self.assertTrue(lic)
                for f in lic:
                    self.assertGreater(len(f.read_text().strip()), 100)

    def test_readme_sha_matches_skill_source_comment(self):
        readme = README.read_text()
        for skill in GOALS_ROOT.glob("*/*/skills/*/SKILL.md"):
            m = re.search(r"<!--\s*Source:.*?-->", skill.read_text(), re.S)
            with self.subTest(skill=skill.parent.name):
                self.assertTrue(m, "no source comment")
                shas = SHA_RE.findall(m.group(0))
                self.assertEqual(len(shas), 1)
                self.assertIn(shas[0], readme)

    def test_readme_names_licence_and_not_copied(self):
        readme = README.read_text()
        for needle in ("MIT", "Modifications", "NOT copied", "Updating"):
            self.assertIn(needle, readme)

    def test_typography_has_no_font_urls(self):
        import csv
        p = GOALS_ROOT / "G-008/ui-ux-pro-max/skills/ui-ux-pro-max/data/typography.csv"
        with open(p, newline="", encoding="utf-8") as fh:
            rows = list(csv.DictReader(fh))
        self.assertGreater(len(rows), 50)
        for r in rows:
            self.assertEqual((r["Google Fonts URL"], r["CSS Import"]), ("", ""))

    def test_stacks_is_only_html_tailwind(self):
        import csv
        d = GOALS_ROOT / "G-008/ui-ux-pro-max/skills/ui-ux-pro-max/data/stacks"
        self.assertEqual(sorted(x.name for x in d.iterdir()), ["html-tailwind.csv"])
        with open(d / "html-tailwind.csv", newline="", encoding="utf-8") as fh:
            rows = list(csv.DictReader(fh))
        self.assertGreater(len(rows), 50)
        for r in rows:
            self.assertEqual(r["Docs URL"], "")

    def test_scanner_catches_bad_lines(self):
        self.assertTrue(CDN_RE.search('<link href="https://fonts.googleapis.com/css2">'))
        self.assertTrue(CDN_RE.search("https://cdn.jsdelivr.net/npm/x"))
        self.assertTrue(RUN_RE.search("python3 scripts/search.py query"))
        self.assertTrue(RUN_RE.search("bash tools/run.sh"))
        self.assertFalse(RUN_RE.search("read the CSV files directly"))


if __name__ == "__main__":
    unittest.main()
