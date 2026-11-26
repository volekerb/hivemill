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
# the private source keeps the public repo's file in the overlay; the public repository holds it at the root
DEPENDABOT = next((p for p in (ROOT / "ci" / "public-overlay" / ".github" / "dependabot.yml", ROOT / ".github" / "dependabot.yml") if p.is_file()),
                  ROOT / ".github" / "dependabot.yml")

ALLOWED_RUNNERS = {"ubuntu-latest", "ubuntu-24.04", "ubuntu-22.04"}
# Owner decision 2026-11-26: our own PRs and pushes may run on the self-hosted `hm-ci` runners (a dedicated user without
# sudo or the owner's credentials). Exactly this expression is allowed, character for character: a fork PR always gets
# ubuntu-latest, and fork PRs also need approval to run (repo setting fork-pr-contributor-approval). Any other
# self-hosted form stays blocked.
SELF_HOSTED_SAME_REPO = ("${{ (github.event_name != 'pull_request' || github.event.pull_request.head.repo.full_name == "
                         "github.repository) && fromJSON('[\"self-hosted\",\"hm-ci\"]') || 'ubuntu-latest' }}")
BANNED_TRIGGERS = {"pull_request_target", "workflow_run", "schedule"}
# SELF_HOSTED_SAME_REPO sends every non-pull_request event to hm-ci; other events (reviews, comments) can come from forks
SELF_HOSTED_TRIGGERS = {"pull_request", "push", "workflow_dispatch"}
FORK_EXPR = re.compile(r"\$\{\{(.*)")  # to end of line: a literal } (format('{0}', ..)) must not hide the rest
FORK_TEXT = re.compile(r"github\.event\.pull_request\.|github\.event\.issue\.|github\.head_ref")
BRACKET = re.compile(r"""\[\s*['"]([\w-]+)['"]\s*\]""")
ALLOWED_OWNERS = {"actions", "github", "volekerb"}
USES_PINNED = re.compile(r"^(?:[0-9a-f]{40}|v\d+(?:\.\d+){0,2})$")
SECRET_REF = re.compile(r"\bsecrets\b(?!\s*:)(?:\s*\.\s*(\w+))?")
KEY = re.compile(r"""^(\s*)(?:-\s+)?["']?([A-Za-z_][\w-]*)["']?:\s*(.*)$""")


def _fork_text(line):
    """True if a ${{ }} expression on `line` reads fork-controlled text, in dot or bracket notation."""
    m = FORK_EXPR.search(line)
    if not m:
        return False
    expr = BRACKET.sub(r".\1", m.group(1))
    expr = re.sub(r"\s*\.\s*", ".", expr)
    return bool(FORK_TEXT.search(expr))


def _uses_problem(ref):
    """Why a step/reusable-workflow `uses:` value is not allowed, or None. Local ./ paths are fine."""
    ref = re.sub(r"\s+#.*$", "", ref).strip("'\"")
    if ref.startswith("./"):
        return None
    if "://" in ref or "@" not in ref:
        return "'%s' must be OWNER/REPO[/path]@<tag or sha>" % ref
    path, version = ref.rsplit("@", 1)
    owner = path.split("/", 1)[0]
    if owner not in ALLOWED_OWNERS:
        return "'%s': owner '%s' is not allowed (%s)" % (ref, owner, ", ".join(sorted(ALLOWED_OWNERS)))
    if not USES_PINNED.match(version):
        return "'%s' is not pinned (use a version tag like v4 or a full commit sha, not '%s')" % (ref, version)
    return None


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


def _flow_line(s):
    """One line of a flow value: quoted names unquoted, other quoted text blanked (brackets and # in it are text), comment cut."""
    if "\\" in s or s.count("'") % 2 or s.count('"') % 2:  # escapes or a quote open across lines: reported as unparsed
        return "\x00"
    s = re.sub(r"'[^']*'|\"[^\"]*\"", lambda q: q.group(0)[1:-1] if re.fullmatch(r"[A-Za-z_][\w-]*", q.group(0)[1:-1]) else "_q", s)
    return re.sub(r"(^|\s+)#.*$", "", s).strip()


def _flow_top(s):
    """Top-level names of a scalar, a flow list or a flow mapping: not nested keys or values."""
    if s[0] not in "[{":
        return re.findall(r"[A-Za-z_][\w-]*", s)
    out, depth, value = [], 0, False
    for tok in re.findall(r"""[\[\]{},:]|["']?[A-Za-z_][\w-]*["']?""", s):
        if tok in "[{":
            depth += 1
        elif tok in "]}":
            depth -= 1
        elif depth == 1 and tok == ",":
            value = False
        elif depth == 1 and tok == ":":
            value = True
        elif depth == 1 and not value and tok not in ",:":
            out.append(tok.strip("'\""))
    return out


def _triggers(rows):
    """({trigger: lineno}, {trigger: [child key names]}) from the top-level `on:` key.

    A form this cannot read is reported as the trigger '<unparsed>', which fails every workflow.
    """
    found, kids = {}, {}
    for i, (n, raw, ind) in enumerate(rows):
        m = KEY.match(raw)
        if ind or not m or m.group(2) != "on":
            continue
        inline = _flow_line(m.group(3))
        if inline:
            if inline[0] in "[{":  # a flow list or mapping may go on over the next lines
                k = i + 1
                depth = lambda s: s.count("[") + s.count("{") - s.count("]") - s.count("}")
                while depth(inline) > 0 and k < len(rows):
                    inline += " " + _flow_line(rows[k][1])
                    k += 1
                if depth(inline) != 0:
                    return {"<unparsed>": n}, kids
            if "\x00" in inline:
                return {"<unparsed>": n}, kids
            for t in _flow_top(inline):
                found[t] = n
            if not found:
                found["<unparsed>"] = n
            return found, kids
        end = _block(rows, i)
        base = rows[i + 1][2] if i + 1 < end else 0
        cur = None
        for k in range(i + 1, end):
            kn, kraw, kind = rows[k]
            item = re.match(r"""^\s*-\s+["']?([A-Za-z_][\w-]*)["']?(?:\s+#.*)?\s*$""", kraw)
            km = KEY.match(kraw)
            if kind == base and not item and not km:
                found["<unparsed>"] = kn
            elif kind == base and item:
                found[item.group(1)] = kn
            elif kind == base and km:
                cur = km.group(2)
                found[cur] = kn
                kids[cur] = []
            elif cur and km:
                kids[cur].append(km.group(2))
        if not found:
            found["<unparsed>"] = n
        return found, kids
    return {"<unparsed>": 1}, kids  # no top-level `on:` this can read, e.g. `on :` or `? on`


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
    if "<unparsed>" in found:
        out.append("%s:%d: the `on:` triggers are in a form this check cannot read; use a block or a one-line list" % (rel, found["<unparsed>"]))
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
            if v not in ALLOWED_RUNNERS and re.sub(r"\s+#.*$", "", val) != SELF_HOSTED_SAME_REPO:
                out.append("%s:%d: runs-on '%s' is not a free standard runner (%s)"
                           % (rel, n, v, ", ".join(sorted(ALLOWED_RUNNERS))))
            elif v not in ALLOWED_RUNNERS and set(found) - SELF_HOSTED_TRIGGERS:
                out.append("%s:%d: runs-on self-hosted allows only the triggers %s (found %s)"
                           % (rel, n, ", ".join(sorted(SELF_HOSTED_TRIGGERS)),
                              ", ".join(sorted(set(found) - SELF_HOSTED_TRIGGERS))))
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
                if _fork_text(braw):
                    out.append("%s:%d: fork-controlled expression inside run: (move it to env:)" % (rel, bn))
        for sm in SECRET_REF.finditer(raw):
            if sm.group(1) != "GITHUB_TOKEN":
                out.append("%s:%d: secret reference not allowed (only secrets.GITHUB_TOKEN or github.token)" % (rel, n))
        if key == "uses":
            why = _uses_problem(val)
            if why:
                out.append("%s:%d: uses %s" % (rel, n, why))
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

    def test_same_repo_self_hosted_expression_allowed(self):
        self.assertClean(GOOD.replace("runs-on: ubuntu-latest", "runs-on: " + SELF_HOSTED_SAME_REPO))

    def test_self_hosted_with_fork_reachable_trigger_blocked(self):
        wf = GOOD.replace("runs-on: ubuntu-latest", "runs-on: " + SELF_HOSTED_SAME_REPO)
        for trigger in ("pull_request_review", "pull_request_review_comment", "issue_comment"):
            bad = wf.replace("on:\n", "on:\n  %s:\n" % trigger, 1)
            self.assertNotEqual(bad, wf)
            self.assertBlocked(bad, trigger)

    def test_self_hosted_trigger_spellings_cannot_hide_a_trigger(self):
        wf = GOOD.replace("runs-on: ubuntu-latest", "runs-on: " + SELF_HOSTED_SAME_REPO)
        block = "on:\n  pull_request:\n    branches: [main]\n  workflow_dispatch:\n"
        self.assertIn(block, wf)
        for on, want in (("on:\n  - push\n  - issue_comment  # chat\n", "issue_comment"),
                         ("on:\n  - push\n  - 'issue_comment'\n", "issue_comment"),
                         ("on: [push,\n  issue_comment]\n", "issue_comment"),
                         ("on:  # events\n  issue_comment:\n", "issue_comment"),
                         ("on:\n- issue_comment\n", "<unparsed>"),
                         ("on:\n  - push\n  - *chat\n", "<unparsed>"),
                         ("on: {push,\n  issue_comment}\n", "issue_comment"),
                         ("on: {push: {branches: [main]},\n  issue_comment: {}}\n", "issue_comment"),
                         ("on: [push,\n", "<unparsed>"),
                         ("on: {'push', \"issue_comment\": null}\n", "issue_comment"),
                         ("on :\n  issue_comment:\n", "<unparsed>"),
                         ("? on\n: [issue_comment]\n", "<unparsed>")):
            self.assertBlocked(wf.replace(block, on), want)
        self.assertClean(wf.replace(block, "on: {pull_request: {branches: [main]},\n  workflow_dispatch: {}}\n"))
        self.assertBlocked(wf.replace(block, "on: {push: {branches: ['[', 'x #y']}, issue_comment: {}}\n"),
                           "issue_comment")
        # a hosted workflow whose triggers cannot be read fails too, so a banned trigger cannot hide there
        for on in ("on:\n- schedule\n", "on: [push,\n", "on :\n  schedule:\n",
                   'on: {push: {branches: ["a\\"b"]}, schedule: {}}\n',
                   "on: {push: {branches: ['a\n  ]}']}, schedule: {}}\n"):
            self.assertBlocked(GOOD.replace(block, on), "cannot read")

    def test_self_hosted_for_forks_blocked(self):
        # a fork must never reach the self-hosted runners: every other spelling is blocked
        for bad in (SELF_HOSTED_SAME_REPO.replace("|| 'ubuntu-latest'", "|| fromJSON('[\"self-hosted\",\"hm-ci\"]')"),
                    SELF_HOSTED_SAME_REPO.replace("github.event.pull_request.head.repo.full_name == github.repository", "true"),
                    SELF_HOSTED_SAME_REPO.replace("!=", "=="),
                    "${{ fromJSON('[\"self-hosted\",\"hm-ci\"]') }}"):
            self.assertBlocked(GOOD.replace("runs-on: ubuntu-latest", "runs-on: " + bad), "runs-on", 10)

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
        self.assertClean(GOOD.replace("- run: echo hi", "- uses: actions/x@v1\n        with:\n          n: ${{ github.event.issue.number }}"))


class InjectionGapsRule(PolicyBase):  # e (format/bracket gaps)
    def test_format_with_literal_braces_blocked(self):
        self.assertBlocked(GOOD.replace("echo hi", "echo ${{ format('{0}', github.head_ref) }}"), "run:", 12)

    def test_bracket_notation_blocked(self):
        for expr in ("github.event['pull_request'].title", "github['event']['pull_request']['title']",
                     "github.event.pull_request[\"body\"]", "github['head_ref']", "github . head_ref"):
            self.assertBlocked(GOOD.replace("echo hi", "echo ${{ %s }}" % expr), "run:", 12)

    def test_format_and_bracket_in_env_allowed(self):
        self.assertClean(GOOD.replace("- run: echo hi", "- env:\n          T: ${{ format('{0}', github.head_ref) }}\n"
                                      "          U: ${{ github.event['pull_request'].title }}\n        run: echo \"$T\""))

    def test_safe_bracket_and_format_allowed(self):
        self.assertClean(GOOD.replace("echo hi", "echo ${{ format('{0}', github.run_id) }} ${{ github['run_id'] }}"))


class UsesRule(PolicyBase):  # g (step and reusable-workflow uses:)
    def step(self, ref):
        return GOOD.replace("- run: echo hi", "- uses: %s" % ref)

    def job(self, ref):
        return GOOD.replace("    runs-on: ubuntu-latest\n    steps:\n      - run: echo hi\n", "    uses: %s\n" % ref)

    def test_step_unpinned_or_foreign_blocked(self):
        self.assertBlocked(self.step("actions/checkout@main"), "not pinned", 12)
        self.assertBlocked(self.step("actions/checkout"), "must be OWNER/REPO", 12)
        self.assertBlocked(self.step("evil/tool@v1"), "owner 'evil'", 12)
        self.assertBlocked(self.step("oci://alpine:3"), "must be OWNER/REPO", 12)

    def test_step_pinned_allowed(self):
        self.assertClean(self.step("actions/checkout@v4"))
        self.assertClean(self.step("actions/checkout@v4 # note"))
        self.assertClean(self.step("actions/checkout@" + "a" * 40))
        self.assertClean(self.step("./.github/actions/local"))

    def test_reusable_workflow_unpinned_or_foreign_blocked(self):
        self.assertBlocked(self.job("evil/repo/.github/workflows/x.yml@v1"), "owner 'evil'", 10)
        self.assertBlocked(self.job("volekerb/repo/.github/workflows/x.yml@main"), "not pinned", 10)
        self.assertBlocked(self.job("volekerb/repo/.github/workflows/x.yml"), "must be OWNER/REPO", 10)

    def test_reusable_workflow_pinned_allowed(self):
        self.assertClean(self.job("volekerb/repo/.github/workflows/x.yml@v1"))
        self.assertClean(self.job("./.github/workflows/x.yml"))


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
