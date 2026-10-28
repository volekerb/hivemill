"""CI cost + fork-safety policy (G-021 T4): the workflows may only use free standard runners, a read-only token, no
secrets and no fork-controlled text inside `run:`. Line-based parsing (stdlib only); every rule has a blocked and an
allowed fixture that goes through the same check_dir() entry point as the real repo."""
import re
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
WORKFLOW_DIRS = [ROOT / ".github" / "workflows", ROOT / "ci" / "public-overlay" / ".github" / "workflows"]
DEPENDABOT = ROOT / "ci" / "public-overlay" / ".github" / "dependabot.yml"

ALLOWED_RUNNERS = {"ubuntu-latest", "ubuntu-24.04", "ubuntu-22.04"}
BANNED_TRIGGERS = {"pull_request_target", "workflow_run", "schedule"}
FORK_TEXT = re.compile(r"\$\{\{[^}]*(github\.event\.pull_request\.|github\.event\.issue\.|github\.head_ref)")
SECRET_REF = re.compile(r"\bsecrets\b(?!\s*:)(?:\s*\.\s*(\w+))?")
KEY = re.compile(r"""^(\s*)(?:-\s+)?["']?([A-Za-z_][\w-]*)["']?:\s*(.*)$""")


def _lines(text):
    """(lineno, raw, indent) for non-blank, non-comment lines."""
    for n, raw in enumerate(text.splitlines(), 1):
        s = raw.strip()
        if s and not s.startswith("#"):
            yield n, raw, len(raw) - len(raw.lstrip())


def _block(rows, i):
    """Index just past the lines more indented than rows[i]."""
    j = i + 1
    while j < len(rows) and rows[j][2] > rows[i][2]:
        j += 1
    return j


def _triggers(rows):
    """({trigger: lineno}, {trigger: [child key names]}) from the top-level `on:` key."""
    found, kids = {}, {}
    for i, (n, raw, ind) in enumerate(rows):
        m = KEY.match(raw)
        if ind or not m or m.group(2) != "on":
            continue
        inline = m.group(3).strip()
        if inline:
            for t in re.findall(r"[A-Za-z_][\w-]*", inline):
                found[t] = n
            return found, kids
        end = _block(rows, i)
        base = rows[i + 1][2] if i + 1 < end else 0
        cur = None
        for k in range(i + 1, end):
            kn, kraw, kind = rows[k]
            item = re.match(r"^\s*-\s+([A-Za-z_][\w-]*)\s*$", kraw)
            km = KEY.match(kraw)
            if kind == base and item:
                found[item.group(1)] = kn
            elif kind == base and km:
                cur = km.group(2)
                found[cur] = kn
                kids[cur] = []
            elif cur and km:
                kids[cur].append(km.group(2))
    return found, kids


def _write_ok(found, kids):
    """Job-level `contents: write` is allowed only for push-tags / workflow_dispatch workflows."""
    for t in found:
        if t == "workflow_dispatch":
            continue
        if t == "push" and "tags" in kids.get("push", []) and "branches" not in kids.get("push", []):
            continue
        return False
    return True


def _check_workflow(path, text):
    rel, out = str(path), []
    rows = list(_lines(text))
    found, kids = _triggers(rows)
    for t in sorted(BANNED_TRIGGERS & set(found)):
        why = "burns minutes unattended" if t == "schedule" else "runs with a write token / secrets on fork code"
        out.append("%s:%d: trigger '%s' not allowed (%s)" % (rel, found[t], t, why))
    write_ok = _write_ok(found, kids)
    top_perm = None
    i = 0
    while i < len(rows):
        n, raw, ind = rows[i]
        m = KEY.match(raw)
        key, val = (m.group(2), m.group(3).strip()) if m else (None, "")
        if key == "runs-on":
            v = re.sub(r"\s+#.*$", "", val).strip("'\"")
            if v not in ALLOWED_RUNNERS:
                out.append("%s:%d: runs-on '%s' is not a free standard runner (%s)"
                           % (rel, n, v, ", ".join(sorted(ALLOWED_RUNNERS))))
        if key == "permissions":
            if ind == 0:
                top_perm = n
            end = _block(rows, i)
            entries = [(n, "", val)] if val else []
            for k in range(i + 1, end):
                km = KEY.match(rows[k][1])
                if km:
                    entries.append((rows[k][0], km.group(2), km.group(3).strip()))
            for gn, name, level in entries:
                if "write" not in level:
                    continue
                if ind > 0 and name == "contents" and level == "write" and write_ok:
                    continue
                out.append("%s:%d: permissions grant '%s' (read-only only; contents: write needs a push-tags or "
                           "workflow_dispatch workflow)" % (rel, gn, level))
        if key == "run":
            body = [(n, raw)] if val and val[0] not in "|>" else []
            end = i + 1
            kind = ind + 2 if raw.lstrip().startswith("-") else ind
            while end < len(rows) and rows[end][2] > kind:
                end += 1
            body += [(rows[k][0], rows[k][1]) for k in range(i + 1, end)]
            for bn, braw in body:
                if FORK_TEXT.search(braw):
                    out.append("%s:%d: fork-controlled expression inside run: (move it to env:)" % (rel, bn))
        for sm in SECRET_REF.finditer(raw):
            if sm.group(1) != "GITHUB_TOKEN":
                out.append("%s:%d: secret reference not allowed (only secrets.GITHUB_TOKEN or github.token)" % (rel, n))
        if key == "secrets" and re.sub(r"\s+#.*$", "", val).strip("'\"") == "inherit":
            out.append("%s:%d: 'secrets: inherit' not allowed" % (rel, n))
        i += 1
    if top_perm is None:
        out.append("%s:1: no top-level permissions: block (add 'permissions: {contents: read}')" % rel)
    return out


def check_dependabot(path):
    path = Path(path)
    if not path.is_file():
        return ["%s:1: dependabot.yml missing" % path]
    out, seen = [], 0
    for n, raw, _ in _lines(path.read_text()):
        m = KEY.match(raw)
        if not m:
            continue
        if m.group(2) == "package-ecosystem":
            seen += 1
            if m.group(3).strip().strip("'\"") != "github-actions":
                out.append("%s:%d: only the github-actions ecosystem is allowed" % (path, n))
        if m.group(2) == "interval" and m.group(3).strip().strip("'\"") != "weekly":
            out.append("%s:%d: interval must be weekly" % (path, n))
    if not seen:
        out.append("%s:1: no package-ecosystem entry" % path)
    return out


def check_dir(directory):
    """Problems (each 'file:line: message') for every workflow in `directory`."""
    out = []
    for f in sorted(Path(directory).glob("*.y*ml")):
        if f.suffix in (".yml", ".yaml"):
            out += _check_workflow(f, f.read_text())
    return out


GOOD = """name: t
on:
  pull_request:
    branches: [main]
  workflow_dispatch:
permissions:
  contents: read
jobs:
  a:
    runs-on: ubuntu-latest
    steps:
      - run: echo hi
"""


class PolicyBase(unittest.TestCase):
    def check(self, text, name="w.yml"):
        with tempfile.TemporaryDirectory() as d:
            (Path(d) / name).write_text(text)
            return [p.replace(d + "/", "") for p in check_dir(d)]

    def assertBlocked(self, text, needle, line=None):
        got = self.check(text)
        hit = [p for p in got if needle in p]
        self.assertTrue(hit, "expected %r in %r" % (needle, got))
        self.assertRegex(hit[0], r"^w\.yml:%s: " % (line or r"\d+"))

    def assertClean(self, text):
        self.assertEqual(self.check(text), [])


class RepoTest(PolicyBase):
    def test_repo_workflows_comply(self):
        for d in WORKFLOW_DIRS:
            if d.is_dir():
                self.assertEqual(check_dir(d), [])
        self.assertTrue((WORKFLOW_DIRS[0] / "tests.yml").is_file())

    def test_repo_dependabot_complies(self):
        self.assertEqual(check_dependabot(DEPENDABOT), [])

    def test_good_fixture_is_clean(self):
        self.assertClean(GOOD)


class RunnerRule(PolicyBase):  # a
    def test_self_hosted_blocked(self):
        self.assertBlocked(GOOD.replace("ubuntu-latest", "self-hosted"), "runs-on", 10)

    def test_matrix_and_large_runner_blocked(self):
        self.assertBlocked(GOOD.replace("ubuntu-latest", "${{ matrix.os }}"), "runs-on")
        self.assertBlocked(GOOD.replace("ubuntu-latest", "ubuntu-latest-8-cores"), "runs-on")

    def test_quoted_key_and_trailing_comment(self):
        self.assertBlocked(GOOD.replace("runs-on: ubuntu-latest", "'runs-on': self-hosted"), "runs-on", 10)
        self.assertClean(GOOD.replace("ubuntu-latest", "ubuntu-latest # standard"))

    def test_standard_runners_allowed(self):
        for r in sorted(ALLOWED_RUNNERS):
            self.assertClean(GOOD.replace("ubuntu-latest", r))


class TriggerRule(PolicyBase):  # b
    def test_pull_request_target_blocked(self):
        self.assertBlocked(GOOD.replace("pull_request:", "pull_request_target:"), "pull_request_target", 3)

    def test_workflow_run_and_inline_blocked(self):
        self.assertBlocked(GOOD.replace("  workflow_dispatch:", "  workflow_run:\n    workflows: [x]"), "workflow_run", 5)
        self.assertBlocked(GOOD.replace("on:\n  pull_request:\n    branches: [main]\n  workflow_dispatch:",
                                        "on: [push, workflow_run]"), "workflow_run", 2)

    def test_schedule_blocked(self):
        self.assertBlocked(GOOD.replace("  workflow_dispatch:", "  schedule:\n    - cron: '0 0 * * *'"), "schedule", 5)

    def test_pull_request_push_tags_and_dispatch_allowed(self):
        self.assertClean(GOOD.replace("  workflow_dispatch:", "  push:\n    tags: ['v*']\n  workflow_dispatch:"))


class PermissionsRule(PolicyBase):  # c
    def test_missing_top_level_permissions_blocked(self):
        self.assertBlocked(GOOD.replace("permissions:\n  contents: read\n", ""), "no top-level permissions", 1)

    def test_write_and_write_all_blocked(self):
        self.assertBlocked(GOOD.replace("contents: read", "contents: write"), "write", 7)
        self.assertBlocked(GOOD.replace("permissions:\n  contents: read", "permissions: write-all"), "write-all", 6)

    def test_pull_request_workflow_job_write_blocked(self):
        self.assertBlocked(GOOD.replace("  a:\n", "  a:\n    permissions:\n      contents: write\n"), "write", 11)

    def test_release_workflow_job_contents_write_allowed(self):
        rel = GOOD.replace("pull_request:\n    branches: [main]", "push:\n    tags: ['v*']")
        self.assertClean(rel.replace("  a:\n", "  a:\n    permissions:\n      contents: write\n"))

    def test_read_only_allowed(self):
        self.assertClean(GOOD.replace("contents: read", "contents: read\n  pull-requests: read"))


class SecretsRule(PolicyBase):  # d
    def test_named_secret_blocked(self):
        self.assertBlocked(GOOD.replace("echo hi", "echo x\n        env:\n          K: ${{ secrets.DEPLOY_KEY }}"),
                           "secret reference", 14)

    def test_secrets_inherit_blocked(self):
        self.assertBlocked(GOOD.replace("    runs-on: ubuntu-latest", "    secrets: inherit"), "inherit", 10)

    def test_tojson_secrets_blocked(self):
        self.assertBlocked(GOOD.replace("echo hi", "echo ${{ toJSON(secrets) }}"), "secret reference", 12)

    def test_github_token_allowed(self):
        self.assertClean(GOOD.replace("echo hi", "echo x\n        env:\n          A: ${{ secrets.GITHUB_TOKEN }}\n"
                                      "          B: ${{ github.token }}"))


class InjectionRule(PolicyBase):  # e
    def test_inline_run_blocked(self):
        for expr in ("github.event.pull_request.title", "github.event.issue.body", "github.head_ref"):
            self.assertBlocked(GOOD.replace("echo hi", "echo ${{ %s }}" % expr), "run:", 12)

    def test_block_run_blocked(self):
        self.assertBlocked(GOOD.replace("- run: echo hi", "- run: |\n          echo a\n          echo \"${{ github.head_ref }}\""),
                           "run:", 14)

    def test_env_after_run_in_same_step_allowed(self):
        self.assertClean(GOOD.replace("- run: echo hi", "- run: echo \"$T\"\n        env:\n          T: ${{ github.head_ref }}"))

    def test_env_and_with_allowed(self):
        self.assertClean(GOOD.replace("- run: echo hi", "- env:\n          T: ${{ github.event.pull_request.title }}\n"
                                      "          H: ${{ github.head_ref }}\n        run: echo \"$T\""))
        self.assertClean(GOOD.replace("- run: echo hi", "- uses: x/y@v1\n        with:\n          n: ${{ github.event.issue.number }}"))


class DependabotRule(PolicyBase):  # f
    def check_dep(self, text):
        with tempfile.TemporaryDirectory() as d:
            p = Path(d) / "dependabot.yml"
            p.write_text(text)
            return [x.replace(d + "/", "") for x in check_dependabot(p)]

    DEP = "version: 2\nupdates:\n  - package-ecosystem: github-actions\n    directory: \"/\"\n    schedule:\n      interval: weekly\n"

    def test_extra_ecosystem_blocked(self):
        got = self.check_dep(self.DEP + "  - package-ecosystem: pip\n    schedule:\n      interval: weekly\n")
        self.assertEqual(len(got), 1)
        self.assertRegex(got[0], r"^dependabot\.yml:7: only the github-actions")

    def test_daily_interval_blocked(self):
        got = self.check_dep(self.DEP.replace("weekly", "daily"))
        self.assertRegex(got[0], r"^dependabot\.yml:6: interval must be weekly")

    def test_github_actions_weekly_allowed(self):
        self.assertEqual(self.check_dep(self.DEP), [])


if __name__ == "__main__":
    unittest.main()
