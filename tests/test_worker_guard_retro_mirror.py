"""G-036 SC4: Go guard mirror — retro Write cwd/tilde, unicode-space redirect stderr, FuzzCheck retro parsers."""
import json
import os
import pathlib
import pwd
import re
import unittest

from guard_impl import hook_run

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
LIVE = os.path.join(pwd.getpwuid(os.getuid()).pw_dir, "factory")
GO = os.path.join(ROOT, "go")

RETRO_ROLE_WHY = (
    "the retro agent may not edit files under <live>/roles/; role prompts and hooks are "
    "changed only by the owner and the tick"
)
RETRO_WRITE_BLOCK = (
    "worker-guard: blocked ({path}): " + RETRO_ROLE_WHY + ". Tell your lead if this is wrong.\n"
)
RETRO_REDIRECT_BLOCK = (
    "worker-guard: blocked (a redirect built by a substitution into a role file: " + RETRO_ROLE_WHY
    + "). Workers may not run herdr or manage factory/herdr systemd units directly; "
    "use the factory CLI (factory done, factory msg, ...). Tell your lead if this is wrong.\n"
)
UNQUOTED_U001C_REDIRECT_BLOCK = (
    "worker-guard: blocked (a redirect into a protected config/init file: it would persist config "
    "into the live box (git config keys like core.fsmonitor or diff.external, and shell init files, "
    "run later as the tick and other agents); only the owner changes ~/.gitconfig, ~/.config/git "
    "and shell rc files). Workers may not run herdr or manage factory/herdr systemd units directly; "
    "use the factory CLI (factory done, factory msg, ...). Tell your lead if this is wrong.\n"
)

_RETRO_PARSER_RE = re.compile(
    r"^func (retro(?:PathExpr|QuoteUnquote|SplitShellWords|PrintfSegment|ResolvePathExpr|SubstRedirectFrags))\(",
    re.MULTILINE,
)


def _retro_parsers(retro_src):
    return sorted(_RETRO_PARSER_RE.findall(retro_src))


def _fuzzcheck_body(fuzz_src):
    m = re.search(r"func FuzzCheck\(f \*testing\.F\) \{", fuzz_src)
    if not m:
        return ""
    start = m.start()
    depth = 0
    i = fuzz_src.find("{", m.end() - 1)
    for j in range(i, len(fuzz_src)):
        if fuzz_src[j] == "{":
            depth += 1
        elif fuzz_src[j] == "}":
            depth -= 1
            if depth == 0:
                return fuzz_src[start : j + 1]
    return fuzz_src[start:]


def fuzzcheck_retro_gaps(fuzz_src, retro_src):
    """Missing retroRole or retro parser names inside FuzzCheck; empty when the real sources pass."""
    body = _fuzzcheck_body(fuzz_src)
    gaps = []
    if "retroRole" not in body:
        gaps.append("retroRole")
    for name in _retro_parsers(retro_src):
        if name not in body:
            gaps.append(name)
    return gaps


class RetroMirrorTests(unittest.TestCase):
    def _retro_write(self, file_path, *, payload_cwd=None, hook_cwd=None, content="x"):
        payload = {"tool_name": "Write", "tool_input": {"file_path": file_path, "content": content}}
        if payload_cwd is not None:
            payload["cwd"] = payload_cwd
        return hook_run(json.dumps(payload), ["--role", "retro"], cwd=hook_cwd)

    def _retro_bash(self, command, *, hook_cwd=None):
        payload = {"tool_name": "Bash", "tool_input": {"command": command}}
        return hook_run(json.dumps(payload), ["--role", "retro"], cwd=hook_cwd)

    def test_write_unresolved_cwd(self):
        p = self._retro_write(
            "~nosuchuser123/factory/roles/coder.md",
            payload_cwd="/tmp",
        )
        self.assertEqual(p.returncode, 2, p.stderr)
        self.assertEqual(
            p.stderr,
            RETRO_WRITE_BLOCK.format(path="'~nosuchuser123/factory/roles/coder.md'"),
        )

        p = self._retro_write("~+/roles/coder.md", hook_cwd=LIVE)
        self.assertEqual(p.returncode, 2, p.stderr)
        self.assertEqual(p.stderr, RETRO_WRITE_BLOCK.format(path="'~+/roles/coder.md'"))

        p = self._retro_write(
            "roles/coder.md",
            payload_cwd="/no/such/retro-cwd-unresolved",
        )
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(p.stderr, "")

        p = self._retro_write(
            "/tmp/zz-retro-mirror-ok.txt",
            payload_cwd="/tmp",
        )
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(p.stderr, "")

    def test_unicode_space_redirect(self):
        roles_target = f"{LIVE}/roles/coder.md"
        for sep in ("\u001c", "\u00a0"):
            allowed = f'echo x >{sep} "$(printf %s /tmp/zz-retro-note.txt)"'
            p = self._retro_bash(allowed)
            self.assertEqual(p.returncode, 0, (sep, p.stderr))
            self.assertEqual(p.stderr, "", (sep, allowed))

            blocked = f'echo x >{sep} "$(printf %s {roles_target})"'
            p = self._retro_bash(blocked)
            self.assertEqual(p.returncode, 2, (sep, p.stderr))
            self.assertEqual(p.stderr, RETRO_REDIRECT_BLOCK, (sep, blocked))

        fs = "\u001c"
        unquoted = f"echo x >{fs}$(printf %s /tmp/zz-retro-note.txt)"
        p = self._retro_bash(unquoted)
        self.assertEqual(p.returncode, 2, p.stderr)
        self.assertEqual(p.stderr, UNQUOTED_U001C_REDIRECT_BLOCK)

    def test_fuzzcheck_role_retro(self):
        fuzz = pathlib.Path(GO, "internal/guard/fuzz_test.go").read_text(encoding="utf-8")
        retro = pathlib.Path(GO, "internal/guard/retro.go").read_text(encoding="utf-8")
        gaps = fuzzcheck_retro_gaps(fuzz, retro)
        self.assertEqual(gaps, [], gaps)
        self.assertIn("retroRole", _fuzzcheck_body(fuzz))


if __name__ == "__main__":
    unittest.main()
