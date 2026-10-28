import re
import unittest

from harness import REPO

PLUGINS_ROOT = REPO / "roles" / "plugins"
# Vendored byte-for-byte third-party text (see roles/plugins/staff-engineer/README.md):
# name/description/length rules still apply, the "Use when" trigger rule does not.
VENDORED_PREFIXES = ("roles/plugins/staff-engineer/",)
NAME_RE = re.compile(r"^[a-z0-9]+(-[a-z0-9]+)*$")
TRIGGER_RE = re.compile(r"(?i)\buse (when|whenever)\b")
MAX_NAME = 64
MAX_DESC = 1024


def _unquote(v):
    v = v.strip()
    if len(v) >= 2 and v[0] == v[-1] and v[0] in "'\"":
        return v[1:-1]
    return v


def parse_front_matter(text):
    """Return dict of top-level scalar keys, or None if no front matter."""
    lines = text.split("\n")
    if not lines or lines[0].rstrip() != "---":
        return None
    end = None
    for i in range(1, len(lines)):
        if lines[i].rstrip() == "---":
            end = i
            break
    if end is None:
        return None
    out = {}
    i = 1
    while i < end:
        m = re.match(r"^([A-Za-z0-9_-]+):\s*(.*)$", lines[i])
        i += 1
        if not m:
            continue
        key, val = m.group(1), m.group(2).strip()
        if val and val[0] in ">|":
            parts = []
            while i < end and (not lines[i].strip() or lines[i][0] in " \t"):
                if lines[i].strip():
                    parts.append(lines[i].strip())
                i += 1
            val = " ".join(parts)
        else:
            val = _unquote(val)
        out[key] = val
    return out


def lint_skill(relpath, text):
    fm = parse_front_matter(text)
    if fm is None:
        return [f"{relpath}: no front matter"]
    found = []
    name = fm.get("name", "")
    desc = fm.get("description", "")
    if not name:
        found.append(f"{relpath}: missing name")
    else:
        if not NAME_RE.match(name):
            found.append(f"{relpath}: name {name!r} must be lowercase letters, digits and single hyphens")
        if len(name) > MAX_NAME:
            found.append(f"{relpath}: name is {len(name)} chars, max {MAX_NAME}")
        parent = relpath.rsplit("/", 2)[-2] if relpath.count("/") >= 1 else ""
        if name != parent:
            found.append(f"{relpath}: name {name!r} != directory {parent!r}")
    if not desc:
        found.append(f"{relpath}: missing description")
    else:
        if len(desc) > MAX_DESC:
            found.append(f"{relpath}: description is {len(desc)} chars, max {MAX_DESC}")
        if not relpath.startswith(VENDORED_PREFIXES) and not TRIGGER_RE.search(desc):
            found.append(f"{relpath}: description has no 'Use when' trigger")
    return found


def skill(name="my-skill", desc="Does a thing. Use when asked.", extra=""):
    return f"---\nname: {name}\ndescription: {desc}\n{extra}---\n\n# body\n"


P = "roles/plugins/lead/factory-lead/skills/my-skill/SKILL.md"
V = "roles/plugins/staff-engineer/staff-engineer-skills/skills/my-skill/SKILL.md"


class LintFixtureTests(unittest.TestCase):
    def test_good_skill_clean(self):
        self.assertEqual(lint_skill(P, skill()), [])

    def test_use_whenever_accepted(self):
        self.assertEqual(lint_skill(P, skill(desc="Does a thing. Use whenever X.")), [])

    def test_front_matter_missing_rejected(self):
        self.assertIn("no front matter", lint_skill(P, "# just a body\n")[0])

    def test_front_matter_unclosed_rejected(self):
        self.assertIn("no front matter", lint_skill(P, "---\nname: my-skill\n")[0])

    def test_name_missing_rejected(self):
        f = lint_skill(P, "---\ndescription: X. Use when y.\n---\n")
        self.assertTrue(any("missing name" in x for x in f), f)

    def test_description_empty_rejected(self):
        f = lint_skill(P, skill(desc=""))
        self.assertTrue(any("missing description" in x for x in f), f)

    def test_name_dir_mismatch_rejected(self):
        f = lint_skill(P, skill(name="other"))
        self.assertTrue(any("!= directory" in x for x in f), f)

    def test_bad_name_chars_rejected(self):
        for bad in ("My-Skill", "my_skill", "my--skill", "-my", "my-"):
            p = f"roles/plugins/lead/p/skills/{bad}/SKILL.md"
            f = lint_skill(p, skill(name=bad))
            self.assertTrue(any("lowercase" in x for x in f), (bad, f))

    def test_name_over_64_rejected(self):
        long = "a" * 65
        f = lint_skill(f"roles/plugins/lead/p/skills/{long}/SKILL.md", skill(name=long))
        self.assertTrue(any("max 64" in x for x in f), f)
        ok = "a" * 64
        self.assertEqual(lint_skill(f"roles/plugins/lead/p/skills/{ok}/SKILL.md", skill(name=ok)), [])

    def test_description_over_1024_rejected(self):
        f = lint_skill(P, skill(desc="Use when " + "x" * 1020))
        self.assertTrue(any("max 1024" in x for x in f), f)
        self.assertEqual(lint_skill(P, skill(desc="Use when " + "x" * 1015)), [])

    def test_trigger_required_for_factory_paths(self):
        f = lint_skill(P, skill(desc="Does a thing."))
        self.assertTrue(any("Use when" in x for x in f), f)

    def test_trigger_not_required_for_vendored(self):
        self.assertEqual(lint_skill(V, skill(desc="Does a thing.")), [])

    def test_vendored_still_checks_name(self):
        f = lint_skill(V, skill(name="other", desc="Does a thing."))
        self.assertTrue(any("!= directory" in x for x in f), f)

    def test_quoted_description_parses(self):
        for q in ("'", '"'):
            self.assertEqual(lint_skill(P, skill(desc=f"{q}Does: a thing. Use when x.{q}")), [])
        f = lint_skill(P, skill(desc="'Does: a thing.'"))
        self.assertTrue(any("Use when" in x for x in f), f)

    def test_block_scalar_description_parses(self):
        for ind in (">", "|"):
            text = f"---\nname: my-skill\ndescription: {ind}\n  Does a thing.\n  Use when asked.\n---\n"
            self.assertEqual(lint_skill(P, text), [])
        text = "---\nname: my-skill\ndescription: >\n  Does a thing.\n---\n"
        self.assertTrue(any("Use when" in x for x in lint_skill(P, text)))


class RepoSkillTests(unittest.TestCase):
    def test_every_skill_passes_lint(self):
        findings = []
        factory_checked = 0
        files = sorted(PLUGINS_ROOT.rglob("SKILL.md"))
        self.assertTrue(files)
        for p in files:
            rel = p.relative_to(REPO).as_posix()
            if not rel.startswith(VENDORED_PREFIXES):
                factory_checked += 1
            findings += lint_skill(rel, p.read_text(encoding="utf-8"))
        self.assertGreaterEqual(factory_checked, 1, "no factory-authored SKILL.md checked")
        self.assertEqual(findings, [])


class GuideTests(unittest.TestCase):
    def test_authoring_guide_content(self):
        p = PLUGINS_ROOT / "AUTHORING.md"
        self.assertTrue(p.is_file())
        text = p.read_text(encoding="utf-8")
        for needle in ("Use when", "bin/factory", "12000",
                       "tests/test_skill_authoring.py", "steps in order"):
            self.assertIn(needle, text)


if __name__ == "__main__":
    unittest.main()
