"""G-031 SC5 (T-1500): the HQ deny rule. In HQ mode (`worker-guard --hq`: boss, director, lead) the terminal, claude,
merge and DB commands that roles/settings deny by prefix are blocked in every spelling the PRIV pass follows.

Runs the real hook (tests/guard_impl.py, so GUARD_IMPL=go runs the Go port). Case lists are mirrored in
go/internal/guard/hq_deny_test.go in the same order.
Counts: block entries 33, forms 12, block cases 396 + 5 extra, allow 35.
"""
import glob
import json
import os
import re
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from guard_impl import ROOT, hook_run  # noqa: E402

HERDR_SUBS = ["server stop", "workspace close", "workspace create", "tab close", "tab create", "pane close",
              "pane split", "agent start"]
HC_SUBS = HERDR_SUBS + ["kill x", "rm x", "run x"]

# (command word, rest) for every denied entry, in table order
ENTRIES = (
    [("herdr", s) for s in HERDR_SUBS]
    + [("hc", s) for s in HC_SUBS]
    + [("herdr-core", s) for s in HC_SUBS]
    + [("claude", "-p hi"), ("gh", "pr merge 1"), ("sqlite3", "x.db .tables")]
)

# each form takes (word, rest) and returns the command line
FORMS = [
    ("plain", lambda w, r: f"{w} {r}"),
    ("assign", lambda w, r: f"FOO=1 {w} {r}"),
    ("env", lambda w, r: f"env {w} {r}"),
    ("command", lambda w, r: f"command {w} {r}"),
    ("exec", lambda w, r: f"exec {w} {r}"),
    ("nohup", lambda w, r: f"nohup {w} {r}"),
    ("timeout", lambda w, r: f"timeout 5 {w} {r}"),
    ("abspath", lambda w, r: f"/usr/local/bin/{w} {r}"),
    ("dotpath", lambda w, r: f"./{w} {r}"),
    ("shell-c", lambda w, r: f"bash -c '{w} {r}'"),
    ("and-chain", lambda w, r: f"true && {w} {r}"),
    ("subst", lambda w, r: "echo $(" + f"{w} {r})"),
]

EXTRA_BLOCK = [
    "gh pr merge 1 --repo o/n",
    "gh -R o/n pr merge 1",
    "gh --repo o/n pr merge 1",
    "npx @anthropic-ai/claude-code -p hi",
    "node /x/node_modules/@anthropic-ai/claude-code/cli.js",
]

ALLOW = [
    "herdr pane list", "herdr pane read p1", "hc ls", "hc read x", "hc send x hi", "herdr agent list",
    "grep 'herdr server stop' f", "echo claude", "git log --grep sqlite3",
    "factory msg director s 'herdr pane close happened'",
    "cat <<'EOF'\nherdr server stop\nclaude\nEOF",
    "factory spawn coder --task T-1", "factory relaunch director",
    "gh pr view 1", "gh pr diff 1", "gh pr checks 1", "gh pr list",
    "which claude", "which herdr", "command -v claude", "command -v herdr", "command -v hc",
    "command -v herdr-core", "command -v sqlite3", "command -v gh",
    "echo sqlite3 gh pr merge", "grep -r 'gh pr merge' docs",
    "./tests/stubs/herdr server stop", "tests/stubs/hc kill x",
    "$X pane close", "${X} server stop", "echo 'unbalanced herdr server stop",
    "git status", "factory inbox", "echo hc run",
]


class HqDenyTests(unittest.TestCase):
    def _rc(self, command):
        payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": command}})
        return hook_run(payload, ["--hq"])

    def _blocked(self, cmd):
        p = self._rc(cmd)
        self.assertEqual(p.returncode, 2, (cmd, p.stderr))
        self.assertIn("HQ deny rule", p.stderr, cmd)

    def test_denied_entries_in_every_form(self):
        n = 0
        for w, r in ENTRIES:
            for fname, fn in FORMS:
                cmd = fn(w, r)
                with self.subTest(form=fname, cmd=cmd):
                    self._blocked(cmd)
                n += 1
        self.assertEqual(n, len(ENTRIES) * len(FORMS))

    def test_extra_block_cases(self):
        for cmd in EXTRA_BLOCK:
            with self.subTest(cmd=cmd):
                self._blocked(cmd)

    def test_reason_text(self):
        p = self._rc("hc server stop")
        self.assertIn("HQ deny rule: hc server stop is denied for boss, director and lead in every spelling "
                      "(see roles/settings; use the factory CLI)", p.stderr)

    def test_allowed_list(self):
        for cmd in ALLOW:
            with self.subTest(cmd=cmd):
                p = self._rc(cmd)
                self.assertEqual(p.returncode, 0, (cmd, p.stderr))

    def test_settings_denies_covered_by_table(self):
        """Every settings deny entry for herdr, hc, herdr-core, claude, gh or sqlite3 has an HQ_DENY row."""
        import importlib.util
        spec = importlib.util.spec_from_file_location("worker_guard_mod", os.path.join(ROOT, "roles", "hooks",
                                                                                      "worker-guard.py"))
        mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(mod)
        table = {(w, tuple(args)) for w, args in mod.HQ_DENY}
        seen = 0
        for path in sorted(glob.glob(os.path.join(ROOT, "roles", "settings", "*.json.in"))):
            if os.path.basename(path).split(".")[0] not in ("boss", "director", "lead"):
                continue
            with open(path, encoding="utf-8") as f:
                text = f.read()
            deny = text.split('"deny"', 1)[1].split("]", 1)[0]
            for m in re.finditer(r'"Bash\(([^"]*?):\*\)"', deny):
                words = m.group(1).split()
                if not words or words[0] not in ("herdr", "hc", "herdr-core", "claude", "gh", "sqlite3"):
                    continue
                seen += 1
                covered = any(w == words[0] and tuple(words[1:len(a) + 1]) == tuple(a) for w, a in table)
                self.assertTrue(covered, f"{os.path.basename(path)}: deny entry Bash({m.group(1)}:*) has no "
                                         "HQ_DENY row in roles/hooks/worker-guard.py (and hqDeny in go)")
        self.assertGreater(seen, 0)


if __name__ == "__main__":
    unittest.main()
