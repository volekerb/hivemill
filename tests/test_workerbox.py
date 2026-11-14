"""Worker profile (T-0161): scout/workerbox.py prepare_agent, the shim and the allow-list view, end to end.

Group A needs no namespaces (names, gitconfig, .claude.json, cwd rule, A1 symlink handling). Group B runs the real
unshare chain against a fake home that mirrors the real layout and skips with userns_available()'s reason when the
box cannot. Group C probes the owner's REAL paths (your-box only, read-only, content never printed; a check is skipped
when its path is absent). Everything else lives in temp dirs under /var/tmp (/tmp is a hide tree).
"""
import ast
import errno
import hashlib
import json
import os
import pty
import select
import shutil
import socket
import subprocess
import sys
import tempfile
import termios
import fcntl
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
import harness  # noqa: E402
from harness import REPO, _literal  # noqa: E402

sys.path.insert(0, str(REPO))
from scout import nsbox, workerbox  # noqa: E402

USERNS_OK, USERNS_WHY = nsbox.userns_available()
NEEDS_USERNS = unittest.skipUnless(USERNS_OK, f"user namespaces unavailable: {USERNS_WHY}")
PY = sys.executable
OAUTH = "oauth-" + "A1b2" * 8
TERMS_BODY = "dummy-term-one\ndummy-term-two\n"
GH_RO = "ghro-" + "C3d4" * 8
GH_RW = "ghrw-" + "E5f6" * 8
SLUGS = ["volekerb/hivemill"]
IDENT = ("Test Owner", "owner@example.invalid")
CONTROL_REL = _literal("DEFAULT_CONFIG")["herdr_socket"][2:]  # ".config/herdr/herdr.sock", same relative path as live
AGENT_REL = CONTROL_REL + ".agent"  # the SSH_AUTH_SOCK convention of the owner's panes

PROBE = f"""#!{PY}
import errno, json, os, shutil, socket, subprocess, sys
if sys.argv[1:2] == ["--session-id"]:
    print("PTY:" + json.dumps({{"pid": os.getpid(), "pgid": os.getpgrp(), "sid": os.getsid(0),
        "fg": os.tcgetpgrp(0), "comm": open("/proc/self/comm").read().strip(), "argv": sys.argv[1:]}}), flush=True)
    sys.exit(0)
print("RAN", flush=True)

def err(e):
    return errno.errorcode.get(e.errno, str(e.errno)) if isinstance(e, OSError) else type(e).__name__

def do(o):
    k = o["op"]
    if k == "open":
        with open(o["path"], "rb") as f:
            return f.read(0) == b""
    if k == "write":
        with open(o["path"], "w") as f:
            f.write(o.get("data", "x"))
        return True
    if k == "read":
        return open(o["path"]).read()
    if k == "unlink":
        return os.unlink(o["path"]) or True
    if k == "rename":
        return os.rename(o["src"], o["dst"]) or True
    if k == "listdir":
        return sorted(os.listdir(o["path"]))
    if k == "connect":
        s = socket.socket(socket.AF_UNIX)
        s.connect(o["path"])
        return True
    if k == "environ":
        return dict(p.split("=", 1) for p in open("/proc/self/environ").read().split("\\0") if p)
    if k == "status":
        return dict(l.split(":\\t", 1) for l in open("/proc/self/status").read().splitlines() if ":\\t" in l)
    if k == "cmdline":
        return open("/proc/self/cmdline").read().split("\\0")
    if k == "environ_of":
        return open(f"/proc/{{o['pid']}}/environ", "rb").read(1) is not None
    if k == "which":
        return shutil.which(o["name"])
    if k == "run":
        r = subprocess.run(o["argv"], capture_output=True, text=True)
        return [r.returncode, r.stdout.strip(), r.stderr.strip()]
    if k == "cwd":
        return os.getcwd()
    raise SystemExit("bad op " + k)

out = []
for o in json.loads(sys.argv[1]):
    try:
        out.append({{"ok": True, "val": do(o)}})
    except BaseException as e:
        out.append({{"ok": False, "err": err(e)}})
print("RESULT:" + json.dumps(out), flush=True)
"""

STUB_FACTORY = f"""#!{PY}
import os, sys
db = os.path.join(os.environ["FACTORY_HOME"], "db")
if sys.argv[1:2] == ["write"]:
    open(os.path.join(db, "written"), "w").write("from inside")
print("agent=%s role=%s db=%s" % (os.environ.get("FACTORY_AGENT"), os.environ.get("FACTORY_ROLE"), sorted(os.listdir(db))))
"""


def sha(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def put(path, text="", mode=None):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text)
    if mode:
        path.chmod(mode)
    return path


HOST_RUN_SOCKETS = ("/run/docker.sock", "/var/run/docker.sock", "/run/ssh-unix-local/socket")


def host_connectable(path):
    c = socket.socket(socket.AF_UNIX)
    try:
        c.connect(path)
        return True
    except OSError:
        return False
    finally:
        c.close()


class World:
    """A fake home that mirrors the owner's layout, a fake factory home inside it, tokens and listening sockets."""

    def __init__(self, case):
        self.case = case
        self.base = Path(tempfile.mkdtemp(prefix="zz-test-workerbox-", dir="/var/tmp")).resolve()
        case.addCleanup(shutil.rmtree, self.base, True)
        self.home = self.base / "home"
        self.runtime = self.base / "run"
        self.fh = self.home / "factory"
        self.socks = []
        put(self.home / ".claude/.credentials.json", '{"secret": "fake-credentials"}')
        put(self.home / ".ssh/id_ed25519", "fake-private-key")
        put(self.home / ".config/gh/hosts.yml", "github.com: fake")
        put(self.home / ".zshrc", "# owner zshrc\n")
        self.claude_real = put(self.home / ".local/share/claude/versions/1", PROBE, 0o755)
        self.claude = self.home / ".local/bin/claude"
        self.claude.parent.mkdir(parents=True, exist_ok=True)
        self.claude.symlink_to(self.claude_real)
        put(self.fh / "bin/factory", STUB_FACTORY, 0o755)
        (self.home / ".local/bin/factory").symlink_to(self.fh / "bin/factory")
        put(self.fh / "roles/settings.json", "{}")
        put(self.fh / "db/factory.db", "ROWS")
        put(self.fh / "worktrees/own/file.txt", "own")
        put(self.fh / "worktrees/other/file.txt", "other")
        self.own = self.fh / "worktrees/own"
        self.tokens = self.home / ".config/factory"
        for name, tok in (("worker-oauth-token", OAUTH), ("gh-worker-ro-token", GH_RO), ("gh-worker-rw-token", GH_RW)):
            put(self.tokens / name, tok + "\n", 0o600)
        put(self.tokens / workerbox.TERMS_NAME, TERMS_BODY, 0o600)
        self.bus = self.listen(self.runtime / "bus")
        self.control = self.listen(self.home / CONTROL_REL)
        self.agent_sock = self.listen(self.home / AGENT_REL)

    def listen(self, path):
        path = Path(path)
        path.parent.mkdir(parents=True, exist_ok=True)
        s = socket.socket(socket.AF_UNIX)
        s.bind(str(path))
        s.listen(4)
        self.socks.append(s)
        self.case.addCleanup(s.close)
        return path

    def prepare(self, agent="coder-t-1", role="coder", cwd=None, rw_cwd=True, **kw):
        kw = {"home": self.home, "runtime": self.runtime, "identity": IDENT, "gh_slugs": SLUGS, **kw}
        return workerbox.prepare_agent(agent, role, cwd or self.own, self.fh, self.fh / "db", self.claude, rw_cwd, **kw)

    def caller_env(self, **extra):
        env = {"PATH": f"{self.home}/.local/bin:/usr/bin:/bin", "LANG": "C.UTF-8", "TERM": "xterm", "LC_ALL": "C.UTF-8",
               "FACTORY_AGENT": "coder-t-1", "FACTORY_ROLE": "coder", "FACTORY_HOME": str(self.fh),
               "FACTORY_DB": str(self.fh / "db"), "HERDR_PANE_ID": "p_42", "DISABLE_AUTOUPDATER": "0",
               "FOO_SECRET": "x", "SSH_AUTH_SOCK": str(self.home / AGENT_REL), "TMPDIR": "/tmp",
               "DBUS_SESSION_BUS_ADDRESS": f"unix:path={self.runtime}/bus", "HOME": "/nonexistent",
               "HTTPS_PROXY": "http://example.invalid:1", "ANTHROPIC_API_KEY": "sk-fake"}
        env.update(extra)
        return env

    def run(self, agent_dir, ops, cwd=None, env=None, timeout=60):
        """Run the generated shim with a probe op list; returns (CompletedProcess, results or None)."""
        p = subprocess.run([str(agent_dir / "bin/claude"), json.dumps(ops)], cwd=cwd or self.own,
                           env=env if env is not None else self.caller_env(), capture_output=True, text=True,
                           timeout=timeout, stdin=subprocess.DEVNULL)
        res = None
        for line in p.stdout.splitlines():
            if line.startswith("RESULT:"):
                res = json.loads(line[7:])
        return p, res


class AgentDirTests(unittest.TestCase):
    """Group A: prepare_agent without any namespace."""

    def setUp(self):
        self.w = World(self)

    def test_sandboxable_roles(self):
        self.assertEqual(workerbox.SANDBOXABLE, {"reviewer", "qa", "coder", "researcher"})
        self.assertEqual(set(workerbox.ROLE_GH), {"reviewer", "qa", "coder", "researcher"})
        with self.assertRaises(ValueError):
            self.w.prepare(role="pm")
        with self.assertRaises(ValueError):
            self.w.prepare(role="judge")

    def test_constants_come_from_the_passwd_home(self):
        home = nsbox.real_home()
        self.assertEqual(workerbox.WORKERS_ROOT, f"{home}/.local/state/factory/workers")
        self.assertEqual(workerbox.OAUTH_TOKEN, f"{home}/.config/factory/worker-oauth-token")
        self.assertEqual(workerbox.GH_RO, f"{home}/.config/factory/gh-worker-ro-token")
        self.assertEqual(workerbox.GH_RW, f"{home}/.config/factory/gh-worker-rw-token")
        self.assertEqual(workerbox.ROLE_GH, {"reviewer": workerbox.GH_RO, "qa": workerbox.GH_RO, "coder": workerbox.GH_RW,
                                            "researcher": workerbox.GH_RO})

    def test_bad_agent_names_are_refused(self):
        for bad in ("../x", "a/b", "", ".", "..", "a b", "x\n", "-x", "a" * 65):
            with self.subTest(bad):
                with self.assertRaises(ValueError):
                    self.w.prepare(agent=bad)
        self.assertEqual(self.w.prepare(agent="coder-t-0161").name, "coder-t-0161")
        self.assertFalse((self.w.home / ".local/state/factory/x").exists())

    def test_agent_dir_layout_and_shim(self):
        d = self.w.prepare()
        self.assertEqual(d, self.w.home / ".local/state/factory/workers/coder-t-1")
        shim = d / "bin/claude"
        self.assertEqual(shim.stat().st_mode & 0o777, 0o755)
        text = shim.read_text()
        self.assertTrue(text.startswith(f"#!{PY} -I\n"))
        for tok in (OAUTH, GH_RO, GH_RW):
            self.assertNotIn(tok, text)
        self.assertEqual((d / "claude").stat().st_mode & 0o777, 0o700)
        self.assertEqual(sorted(p.name for p in d.iterdir()), ["bin", "claude", "gitconfig"])

    def test_cfg_embedded_in_shim_is_the_allow_view(self):
        d = self.w.prepare(rw_cwd=False)
        cfg = json.loads(_shim_cfg_literal(d))
        ns = cfg["ns"]
        self.assertEqual((ns["mode"], ns["net"], ns["pid"], ns["forward_sock"]), ("allow", True, False, None))
        self.assertEqual(ns["trial"], str(d / "claude"))
        self.assertNotIn("CLAUDE_CODE_OAUTH_TOKEN", ns["scrub"])
        self.assertNotIn("GH_TOKEN", ns["scrub"])
        self.assertEqual(ns["rw"], [str(self.w.fh / "db"), str(d / "claude")])
        self.assertIn(str(self.w.own), ns["ro"])
        self.assertIn(str(self.w.fh), ns["ro"])
        self.assertIn(str(self.w.home / ".local/bin/factory"), ns["ro"])
        self.assertEqual(ns["masks"], [str(self.w.fh / "worktrees")])
        self.assertEqual(ns["tmpfs_sizes"], {os.path.realpath("/tmp"): "512m", str(self.w.home): "64m"})
        self.assertNotIn("tmpfs_size", ns)
        self.assertEqual(cfg["gh_token"], str(self.w.tokens / "gh-worker-rw-token"))
        self.assertEqual(cfg["oauth_token"], str(self.w.tokens / "worker-oauth-token"))

    def test_terms_file_is_the_one_extra_ro_path(self):
        t = str(self.w.tokens / workerbox.TERMS_NAME)
        ro = json.loads(_shim_cfg_literal(self.w.prepare(rw_cwd=False)))["ns"]["ro"]
        self.assertIn(t, ro)
        self.assertNotIn(str(self.w.tokens), ro)
        (self.w.tokens / workerbox.TERMS_NAME).unlink()
        ro = json.loads(_shim_cfg_literal(self.w.prepare(rw_cwd=False)))["ns"]["ro"]
        self.assertNotIn(t, ro)

    def test_symlinked_terms_file_is_refused(self):
        t = self.w.tokens / workerbox.TERMS_NAME
        t.unlink()
        t.symlink_to(self.w.fh / "roles/settings.json")
        with self.assertRaisesRegex(ValueError, "symlink"):
            self.w.prepare()

    def test_gitconfig_has_only_identity_and_the_credential_helper(self):
        d = self.w.prepare()
        out = subprocess.run(["git", "config", "-f", str(d / "gitconfig"), "--list"], capture_output=True, text=True,
                             check=True).stdout.splitlines()
        self.assertEqual([l.split("=", 1)[0] for l in out],
                         ["user.name", "user.email", "credential.usehttppath", "credential.https://github.com.helper"])
        self.assertEqual(out[0], "user.name=Test Owner")
        self.assertEqual(out[2], "credential.usehttppath=true")
        self.assertEqual(out[3].split("=", 1)[1], workerbox.gh_helper(Path(workerbox.__file__).resolve().parent.parent, SLUGS))
        r = subprocess.run(["git", "credential", "fill"], input="protocol=https\nhost=github.com\npath=volekerb/hivemill\n\n", text=True,
                           capture_output=True, env={"PATH": os.environ["PATH"], "HOME": "/nonexistent",
                                                     "GIT_CONFIG_GLOBAL": str(d / "gitconfig"), "GH_TOKEN": "tok-x",
                                                     "GIT_CONFIG_NOSYSTEM": "1", "GIT_TERMINAL_PROMPT": "0"})
        self.assertIn("password=tok-x", r.stdout)
        self.assertIn("username=x-access-token", r.stdout)

    def test_no_scoped_helper_without_valid_repo_slugs(self):
        for bad in (None, [], ["bad slug"], ["no-slash"]):
            with self.subTest(bad):
                with self.assertRaises(ValueError) as cm:
                    self.w.prepare(gh_slugs=bad)
                self.assertIn("worker_gh_repos", str(cm.exception))
        self.assertFalse((self.w.home / ".local/state/factory/workers/coder-t-1/gitconfig").exists())

    def test_gitconfig_without_identity_has_just_the_helper(self):
        d = self.w.prepare(identity=(None, None))
        out = subprocess.run(["git", "config", "-f", str(d / "gitconfig"), "--list"], capture_output=True, text=True,
                             check=True).stdout.splitlines()
        self.assertEqual([l.split("=", 1)[0] for l in out], ["credential.usehttppath", "credential.https://github.com.helper"])

    def test_claude_json_seeded_once_and_cwd_entry_added(self):
        d = self.w.prepare()
        path = d / "claude/.claude.json"
        seed = json.loads(path.read_text())
        self.assertEqual(seed["hasCompletedOnboarding"], True)
        self.assertEqual(seed["theme"], "dark")
        self.assertEqual(seed["projects"], {str(self.w.own): {"hasTrustDialogAccepted": True,
                                                              "hasCompletedProjectOnboarding": True}})
        self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        seed["sessionNote"] = "written by claude"
        path.write_text(json.dumps(seed))
        self.w.prepare()  # same cwd: not touched
        self.assertEqual(json.loads(path.read_text())["sessionNote"], "written by claude")
        self.w.prepare(cwd=self.w.fh / "worktrees/other")  # new cwd: entry added, the rest kept
        now = json.loads(path.read_text())
        self.assertEqual(now["sessionNote"], "written by claude")
        self.assertEqual(sorted(now["projects"]), sorted([str(self.w.own), str(self.w.fh / "worktrees/other")]))

    def test_trust_entry_is_under_the_file_and_key_claude_reads(self):
        """T-1291. File: with CLAUDE_CONFIG_DIR set, Claude (2.1.322) keeps its global config at <CLAUDE_CONFIG_DIR>/.claude.json
        (the live worker dirs hold the 70 KB file Claude itself rewrites there). Key: projects[<git root of the cwd>], which
        for a clone is the cwd, and the pane cwd is the realpath the namespace was built with (cfg["cwd"])."""
        owner = put(self.w.home / ".claude.json", json.dumps({"projects": {}}))
        before = sha(owner)
        d = self.w.prepare()
        env = workerbox.build_env({}, str(self.w.home), str(d / "claude"), "g", "o", "t")
        path = Path(env["CLAUDE_CONFIG_DIR"]) / ".claude.json"
        shim = ast.parse((d / "bin/claude").read_text().split("\n", 1)[1])  # the baked cfg: json.loads('<json>')
        cfg = json.loads(next(n.args[0].value for n in ast.walk(shim) if isinstance(n, ast.Call)
                              and getattr(n.func, "attr", "") == "loads"))
        key = cfg["ns"]["cwd"]
        self.assertEqual(key, os.path.realpath(self.w.own))
        self.assertEqual(json.loads(path.read_text())["projects"][key]["hasTrustDialogAccepted"], True)
        self.assertEqual(list(json.loads(path.read_text())["projects"]), [key])  # the pane cwd only: no wildcard, no parent
        self.assertEqual(sha(owner), before)  # the owner's ~/.claude.json is not touched by the sandbox path

    def test_existing_untrusted_entry_for_the_cwd_gets_trust(self):
        d = self.w.prepare()
        path = d / "claude/.claude.json"
        cur = json.loads(path.read_text())
        cur["projects"][str(self.w.own)] = {"hasTrustDialogAccepted": False, "lastCost": 1}
        cur["projects"]["/elsewhere"] = {"hasTrustDialogAccepted": False}
        path.write_text(json.dumps(cur))
        self.w.prepare()
        now = json.loads(path.read_text())["projects"]
        self.assertEqual(now[str(self.w.own)], {"hasTrustDialogAccepted": True, "lastCost": 1,
                                                "hasCompletedProjectOnboarding": True})
        self.assertEqual(now["/elsewhere"], {"hasTrustDialogAccepted": False})

    def test_claude_dir_is_kept_across_calls(self):
        d = self.w.prepare()
        put(d / "claude/session.jsonl", "kept")
        self.w.prepare()
        self.assertEqual((d / "claude/session.jsonl").read_text(), "kept")

    def test_symlinked_claude_json_is_replaced_and_its_target_untouched(self):
        d = self.w.prepare()
        target = put(self.w.base / "outside.txt", "owner file\n")
        before = sha(target)
        link = d / "claude/.claude.json"
        link.unlink()
        link.symlink_to(target)
        self.w.prepare()
        self.assertEqual(sha(target), before)
        self.assertFalse(link.is_symlink())
        self.assertEqual(json.loads(link.read_text())["hasCompletedOnboarding"], True)

    def test_symlink_to_a_json_file_is_not_followed_either(self):
        d = self.w.prepare()
        target = put(self.w.base / "outside.json", json.dumps({"projects": {}, "mine": 1}))
        before = sha(target)
        link = d / "claude/.claude.json"
        link.unlink()
        link.symlink_to(target)
        self.w.prepare(cwd=self.w.fh / "worktrees/other")
        self.assertEqual(sha(target), before)
        self.assertNotIn("mine", json.loads(link.read_text()))

    def test_oversized_claude_json_is_refused_and_kept(self):
        d = self.w.prepare()
        path = d / "claude/.claude.json"
        path.write_text(json.dumps({"projects": {}, "state": "x" * 4096}))
        before = sha(path)
        with mock.patch.object(workerbox, "CLAUDE_JSON_MAX", 1024):
            with self.assertRaises(ValueError) as cm:
                self.w.prepare(cwd=self.w.fh / "worktrees/other")
        self.assertIn("larger than 1024", str(cm.exception))
        self.assertEqual(sha(path), before)

    def test_claude_json_just_under_the_cap_is_read(self):
        d = self.w.prepare()
        path = d / "claude/.claude.json"
        path.write_text(json.dumps({"projects": {}, "state": "kept"}))
        with mock.patch.object(workerbox, "CLAUDE_JSON_MAX", 1024):
            self.w.prepare(cwd=self.w.fh / "worktrees/other")
        self.assertEqual(json.loads(path.read_text())["state"], "kept")

    def test_directory_at_claude_json_is_refused(self):
        d = self.w.prepare()
        (d / "claude/.claude.json").unlink()
        (d / "claude/.claude.json").mkdir()
        with self.assertRaises(ValueError):
            self.w.prepare()

    def test_symlinked_claude_dir_is_refused(self):
        d = self.w.prepare()
        other = self.w.base / "elsewhere"
        other.mkdir()
        shutil.rmtree(d / "claude")
        (d / "claude").symlink_to(other)
        with self.assertRaises((ValueError, OSError)):
            self.w.prepare()
        self.assertEqual(list(other.iterdir()), [])

    def test_symlink_planted_at_shim_or_gitconfig_is_replaced_not_followed(self):
        d = self.w.prepare()
        target = put(self.w.base / "victim.txt", "victim\n")
        before = sha(target)
        for p in (d / "bin/claude", d / "gitconfig"):
            p.unlink()
            p.symlink_to(target)
        self.w.prepare()
        self.assertEqual(sha(target), before)
        self.assertFalse((d / "bin/claude").is_symlink() or (d / "gitconfig").is_symlink())

    def test_cwd_equal_or_above_protected_trees_is_refused(self):
        w = self.w
        for bad in (w.home, w.fh, "/tmp", "/", "/dev/shm", w.home.parent, w.fh / "bin", w.fh / "db",
                    w.home / ".ssh", w.fh / "worktrees", w.home / "other", Path("/tmp/anything-here")):
            with self.subTest(str(bad)):
                with self.assertRaises(ValueError):
                    w.prepare(cwd=bad)
                self.assertFalse((w.home / ".local/state/factory").exists(), "a refused cwd must not create anything")

    def test_cwd_real_home_is_refused_without_overrides(self):
        for bad in (nsbox.real_home(), "/tmp"):
            with self.assertRaises(ValueError):
                workerbox.prepare_agent("a", "coder", bad, self.w.fh, self.w.fh / "db", self.w.claude, True,
                                        workers_root=self.w.base / "wr", identity=IDENT)

    def test_cwd_under_worktrees_outside_and_symlinks_are_resolved(self):
        outside = self.w.base / "plain-clone"
        outside.mkdir()
        self.assertTrue(self.w.prepare(cwd=outside))
        evil = self.w.base / "evil-link"
        evil.symlink_to(self.w.fh / "bin")
        with self.assertRaises(ValueError):
            self.w.prepare(cwd=evil)


class TokenFailTests(unittest.TestCase):
    """Group B1: the shim's token rules. The fake claude must never run when a token is bad."""

    def setUp(self):
        self.w = World(self)
        self.tokdir = self.w.base / "tokens"
        self.tokdir.mkdir()
        for name, tok in (("worker-oauth-token", OAUTH), ("gh-worker-ro-token", GH_RO), ("gh-worker-rw-token", GH_RW)):
            put(self.tokdir / name, tok + "\n", 0o600)

    def shim(self, role="coder"):
        return self.w.prepare(role=role, token_dir=self.tokdir)

    def assert_exit3(self, d, path):
        p, res = self.w.run(d, [])
        self.assertEqual(p.returncode, 3, p.stderr)
        self.assertIn(str(path), p.stderr)
        self.assertNotIn("RAN", p.stdout)
        self.assertIsNone(res)
        for tok in (OAUTH, GH_RO, GH_RW):
            self.assertNotIn(tok, p.stdout + p.stderr)

    @NEEDS_USERNS
    def test_0600_owned_files_start_it(self):
        p, res = self.w.run(self.shim(), [])
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn("RAN", p.stdout)

    def test_missing_oauth_token(self):
        d = self.shim()
        (self.tokdir / "worker-oauth-token").unlink()
        self.assert_exit3(d, self.tokdir / "worker-oauth-token")

    def test_missing_gh_token_of_the_role(self):
        d = self.shim("coder")
        (self.tokdir / "gh-worker-rw-token").unlink()
        self.assert_exit3(d, self.tokdir / "gh-worker-rw-token")

    def test_reviewer_needs_the_ro_token_not_the_rw_one(self):
        d = self.shim("reviewer")
        (self.tokdir / "gh-worker-ro-token").unlink()
        self.assert_exit3(d, self.tokdir / "gh-worker-ro-token")

    def test_mode_0644(self):
        d = self.shim()
        (self.tokdir / "worker-oauth-token").chmod(0o644)
        self.assert_exit3(d, self.tokdir / "worker-oauth-token")

    def test_gh_token_mode_0640(self):
        d = self.shim()
        (self.tokdir / "gh-worker-rw-token").chmod(0o640)
        self.assert_exit3(d, self.tokdir / "gh-worker-rw-token")

    def test_symlink(self):
        d = self.shim()
        real = put(self.w.base / "real-token", OAUTH, 0o600)
        (self.tokdir / "worker-oauth-token").unlink()
        (self.tokdir / "worker-oauth-token").symlink_to(real)
        self.assert_exit3(d, self.tokdir / "worker-oauth-token")

    def test_empty_token_file(self):
        d = self.shim()
        put(self.tokdir / "gh-worker-rw-token", "\n", 0o600)
        self.assert_exit3(d, self.tokdir / "gh-worker-rw-token")

    def test_foreign_owner(self):
        d = self.shim()
        cfg = json.loads(_shim_cfg_literal(d))
        import contextlib
        import io
        err = io.StringIO()
        with mock.patch.object(os, "getuid", return_value=os.getuid() + 1), \
                mock.patch.object(os, "execve", side_effect=AssertionError("must not exec")), \
                contextlib.redirect_stderr(err):
            with self.assertRaises(SystemExit) as cm:
                workerbox.shim_main(cfg, [])
        self.assertEqual(cm.exception.code, 3)
        self.assertIn(str(self.tokdir / "worker-oauth-token"), err.getvalue())
        for tok in (OAUTH, GH_RO, GH_RW):
            self.assertNotIn(tok, err.getvalue())


def _shim_cfg_literal(agent_dir):
    """The JSON string literal the shim embeds, as the python repr it was written with."""
    for line in (agent_dir / "bin/claude").read_text().splitlines():
        if line.startswith("workerbox.shim_main(json.loads("):
            import ast
            return ast.literal_eval(line[len("workerbox.shim_main(json.loads("):].rsplit("), sys.argv", 1)[0])
    raise AssertionError("shim has no cfg line")


@NEEDS_USERNS
class SandboxTests(unittest.TestCase):
    """Group B2: the real namespaces against the fake home."""

    def setUp(self):
        self.w = World(self)
        self.d = self.w.prepare()

    def probe(self, ops, **kw):
        p, res = self.w.run(self.d, ops, **kw)
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIsNotNone(res, p.stdout + p.stderr)
        return res

    def test_hidden_credentials_and_sockets(self):
        w = self.w
        paths = [w.home / ".claude/.credentials.json", w.home / ".ssh/id_ed25519", w.home / ".config/gh/hosts.yml"]
        socks = [w.bus, w.control, w.agent_sock]
        for s in socks:  # control: the sockets do accept connections outside
            c = socket.socket(socket.AF_UNIX)
            c.connect(str(s))
            c.close()
        res = self.probe([{"op": "open", "path": str(p)} for p in paths] + [{"op": "connect", "path": str(s)} for s in socks])
        for item, r in zip([*paths, *socks], res):
            with self.subTest(str(item)):
                self.assertFalse(r["ok"], f"reachable from inside: {item}")
                self.assertEqual(r["err"], "ENOENT")

    def test_host_run_sockets_unreachable(self):
        """T-0673: the container-engine and ssh-unix-local sockets under the host /run are masked in the worker profile."""
        socks = [p for p in HOST_RUN_SOCKETS if host_connectable(p)]  # control: connectable outside, or not asserted
        if not socks:
            self.skipTest("no connectable host /run socket on this box")
        for r, p in zip(self.probe([{"op": "connect", "path": p} for p in socks]), socks):
            with self.subTest(p):
                self.assertFalse(r["ok"], f"reachable from inside: {p}")
                self.assertEqual(r["err"], "ENOENT")

    def test_ssh_agent_and_bus_env_unset(self):
        env = self.probe([{"op": "environ"}])[0]["val"]
        self.assertNotIn("SSH_AUTH_SOCK", env)
        self.assertNotIn("DBUS_SESSION_BUS_ADDRESS", env)

    def test_home_is_private_and_zshrc_write_stays_inside(self):
        zshrc = self.w.home / ".zshrc"
        before = sha(zshrc)
        res = self.probe([{"op": "read", "path": str(zshrc)}, {"op": "write", "path": str(zshrc), "data": "pwned\n"},
                          {"op": "read", "path": str(zshrc)}, {"op": "listdir", "path": str(self.w.home)}])
        self.assertFalse(res[0]["ok"])
        self.assertTrue(res[1]["ok"])
        self.assertEqual(res[2]["val"], "pwned\n")
        self.assertEqual(sha(zshrc), before)
        self.assertNotIn(".ssh", res[3]["val"])
        self.assertNotIn(".claude", res[3]["val"])

    def test_factory_tree_is_read_only(self):
        x = self.w.fh / "bin/x"
        res = self.probe([{"op": "write", "path": str(x)},
                          {"op": "write", "path": str(self.w.fh / "roles/settings.json")},
                          {"op": "unlink", "path": str(self.w.fh / "bin/factory")}])
        for r in res:
            self.assertEqual((r["ok"], r.get("err")), (False, "EROFS"))
        self.assertFalse(x.exists())

    def test_worktrees_lists_only_the_own_worktree(self):
        res = self.probe([{"op": "listdir", "path": str(self.w.fh / "worktrees")},
                          {"op": "read", "path": str(self.w.fh / "worktrees/own/file.txt")},
                          {"op": "read", "path": str(self.w.fh / "worktrees/other/file.txt")}])
        self.assertEqual(res[0]["val"], ["own"])
        self.assertEqual(res[1]["val"], "own")
        self.assertFalse(res[2]["ok"])

    def test_factory_cli_resolves_via_path_and_writes_db(self):
        res = self.probe([{"op": "which", "name": "factory"},
                          {"op": "run", "argv": ["factory", "whoami"]},
                          {"op": "run", "argv": ["factory", "write"]}])
        self.assertEqual(res[0]["val"], str(self.w.home / ".local/bin/factory"))
        self.assertEqual(res[1]["val"][0], 0, res[1]["val"])
        self.assertIn("agent=coder-t-1 role=coder", res[1]["val"][1])
        self.assertIn("factory.db", res[1]["val"][1])
        self.assertEqual(res[2]["val"][0], 0, res[2]["val"])
        self.assertEqual((self.w.fh / "db/written").read_text(), "from inside")

    def test_db_dir_is_writable_but_db_dir_parent_is_not(self):
        res = self.probe([{"op": "write", "path": str(self.w.fh / "db/new.txt"), "data": "r"},
                          {"op": "write", "path": str(self.w.fh / "newfile")}])
        self.assertTrue(res[0]["ok"])
        self.assertEqual(res[1]["err"], "EROFS")
        self.assertEqual((self.w.fh / "db/new.txt").read_text(), "r")

    def test_claude_config_dir_persists_between_two_runs(self):
        cd = self.probe([{"op": "environ"}])[0]["val"]["CLAUDE_CONFIG_DIR"]
        self.assertEqual(cd, str(self.d / "claude"))
        self.probe([{"op": "write", "path": f"{cd}/session.jsonl", "data": "run one"}])
        self.assertEqual((self.d / "claude/session.jsonl").read_text(), "run one")
        res = self.probe([{"op": "read", "path": f"{cd}/session.jsonl"}])
        self.assertEqual(res[0]["val"], "run one")
        self.assertTrue(json.loads((self.d / "claude/.claude.json").read_text())["hasCompletedOnboarding"])

    def test_cwd_rw_when_asked_and_read_only_otherwise(self):
        res = self.probe([{"op": "cwd"}, {"op": "write", "path": str(self.w.own / "new.txt"), "data": "w"}])
        self.assertEqual(res[0]["val"], str(self.w.own))
        self.assertTrue(res[1]["ok"])
        self.assertEqual((self.w.own / "new.txt").read_text(), "w")
        self.d = self.w.prepare(rw_cwd=False)
        res = self.probe([{"op": "cwd"}, {"op": "write", "path": str(self.w.own / "again.txt")},
                          {"op": "read", "path": str(self.w.own / "file.txt")}])
        self.assertEqual(res[0]["val"], str(self.w.own))
        self.assertEqual((res[1]["ok"], res[1]["err"]), (False, "EROFS"))
        self.assertEqual(res[2]["val"], "own")
        self.assertFalse((self.w.own / "again.txt").exists())

    def test_env_is_exactly_the_allow_list(self):
        caller = self.w.caller_env()
        env = self.probe([{"op": "environ"}])[0]["val"]
        passed = {k for k in caller if k in workerbox.WORKER_ENV_PASS or k.startswith("LC_")}
        set_keys = {"HOME", "CLAUDE_CONFIG_DIR", "GIT_CONFIG_GLOBAL", "DISABLE_AUTOUPDATER", "CLAUDE_CODE_OAUTH_TOKEN",
                    "GH_TOKEN"}
        self.assertEqual(set(env), passed | set_keys)
        self.assertNotIn("FOO_SECRET", env)
        for gone in ("SSH_AUTH_SOCK", "DBUS_SESSION_BUS_ADDRESS", "TMPDIR", "HTTPS_PROXY", "ANTHROPIC_API_KEY", "FACTORY_DB"):
            self.assertNotIn(gone, env)
        for k in passed - {"DISABLE_AUTOUPDATER"}:
            self.assertEqual(env[k], caller[k])
        self.assertEqual(env["HOME"], str(self.w.home))
        self.assertEqual(env["DISABLE_AUTOUPDATER"], "1")
        self.assertEqual(env["GIT_CONFIG_GLOBAL"], str(self.d / "gitconfig"))
        self.assertEqual(env["CLAUDE_CODE_OAUTH_TOKEN"], OAUTH)
        self.assertEqual(env["GH_TOKEN"], GH_RW)

    def test_role_picks_its_gh_token(self):
        for role, tok in (("reviewer", GH_RO), ("qa", GH_RO), ("coder", GH_RW)):
            with self.subTest(role):
                self.d = self.w.prepare(agent=f"{role}-x", role=role, rw_cwd=False)
                self.assertEqual(self.probe([{"op": "environ"}])[0]["val"]["GH_TOKEN"], tok)

    def test_no_secret_leak(self):
        res = self.probe([{"op": "cmdline"}, {"op": "environ"}])
        p, _ = self.w.run(self.d, [{"op": "cmdline"}])
        shim_text = (self.d / "bin/claude").read_text()
        cmdline = res[0]["val"]
        seen = [shim_text, json.dumps(cmdline), p.stdout, p.stderr, (self.d / "gitconfig").read_text(),
                (self.d / "claude/.claude.json").read_text()]
        for tok in (OAUTH, GH_RO, GH_RW):
            for text in seen:
                self.assertNotIn(tok, text)
        self.assertEqual(cmdline[-2], json.dumps([{"op": "cmdline"}, {"op": "environ"}]))  # the last argument, then the trailing NUL

    def test_no_capabilities_and_no_new_privs(self):
        st = self.probe([{"op": "status"}])[0]["val"]
        self.assertEqual(int(st["CapEff"], 16), 0)
        self.assertEqual(st["NoNewPrivs"].strip(), "1")

    def test_agent_bin_and_gitconfig_are_immutable_from_inside(self):
        d = self.d
        files = [d / "bin/claude", d / "gitconfig"]
        before = {str(f): sha(f) for f in files}
        listing = sorted(p.name for p in d.iterdir())
        ops = []
        for f in files:
            ops += [{"op": "write", "path": str(f), "data": "evil"}, {"op": "unlink", "path": str(f)},
                    {"op": "write", "path": f"{f}.new", "data": "evil"} if f.parent == d / "bin" else {"op": "cwd"},
                    {"op": "rename", "src": str(f), "dst": f"{f}.old"}]
        ops += [{"op": "rename", "src": str(d / "bin"), "dst": str(d / "bin2")},
                {"op": "rename", "src": str(d / "gitconfig"), "dst": str(d / "gitconfig2")},
                {"op": "unlink", "path": str(d / "gitconfig")}]
        res = self.probe(ops)
        for op, r in zip(ops, res):
            if op["op"] == "cwd":
                continue
            with self.subTest(str(op)):
                self.assertFalse(r["ok"], op)
                self.assertIn(r["err"], ("EROFS", "EBUSY", "EXDEV"))
        self.assertEqual({str(f): sha(f) for f in files}, before)
        self.assertEqual(sorted(p.name for p in d.iterdir()), listing)
        self.assertTrue((d / "bin").is_dir())

    def test_terms_file_is_readable_and_immutable_and_siblings_stay_hidden(self):
        t = self.w.tokens / workerbox.TERMS_NAME
        before = sha(t)
        res = self.probe([{"op": "read", "path": str(t)}, {"op": "write", "path": str(t), "data": "evil"},
                          {"op": "rename", "src": str(t), "dst": f"{t}.old"}, {"op": "unlink", "path": str(t)},
                          {"op": "open", "path": str(self.w.tokens / "gh-worker-ro-token")},
                          {"op": "open", "path": str(self.w.tokens / "worker-oauth-token")}])
        self.assertEqual((res[0]["ok"], res[0]["val"]), (True, TERMS_BODY))
        for r in res[1:4]:
            self.assertFalse(r["ok"])
            self.assertIn(r["err"], ("EROFS", "EBUSY", "EXDEV"))
        for r in res[4:]:
            self.assertEqual((r["ok"], r["err"]), (False, "ENOENT"))
        self.assertEqual(sha(t), before)
        self.assertTrue(t.is_file())

    def test_missing_terms_file_still_starts_the_sandbox(self):
        (self.w.tokens / workerbox.TERMS_NAME).unlink()
        d = self.w.prepare(agent="coder-t-2")
        p, res = self.w.run(d, [{"op": "open", "path": str(self.w.tokens / workerbox.TERMS_NAME)}])
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual((res[0]["ok"], res[0]["err"]), (False, "ENOENT"))

    def test_new_files_in_agent_dir_do_not_reach_the_host(self):
        self.probe([{"op": "write", "path": str(self.d / "evil.sh")}, {"op": "write", "path": str(self.d / "bin/evil")}])
        self.assertEqual(sorted(p.name for p in self.d.iterdir()), ["bin", "claude", "gitconfig"])

    def test_pty_foreground_pid_and_argv(self):
        argv = ["--session-id", "x", "--settings", "y"]
        master, slave = pty.openpty()
        self.addCleanup(os.close, master)

        def ctty():
            fcntl.ioctl(0, termios.TIOCSCTTY, 0)  # after setsid: the pty becomes the controlling tty

        proc = subprocess.Popen([str(self.d / "bin/claude"), *argv], cwd=self.w.own, env=self.w.caller_env(),
                                stdin=slave, stdout=slave, stderr=slave, start_new_session=True, preexec_fn=ctty)
        os.close(slave)
        try:
            sid_before = os.getsid(proc.pid)
            out = b""
            while True:
                ready, _, _ = select.select([master], [], [], 30)
                if not ready:
                    break
                try:
                    chunk = os.read(master, 4096)
                except OSError:
                    break
                if not chunk:
                    break
                out += chunk
            proc.wait(timeout=30)
        finally:
            if proc.poll() is None:
                proc.kill()
                proc.wait()
        line = [l for l in out.decode().replace("\r", "").splitlines() if l.startswith("PTY:")]
        self.assertTrue(line, out)
        info = json.loads(line[0][4:])
        self.assertEqual(info["comm"], "claude")
        self.assertEqual(info["pid"], proc.pid)  # exec-only chain: claude keeps the shim's pid
        self.assertEqual(info["fg"], info["pgid"])  # in the terminal's foreground process group
        self.assertEqual(info["sid"], sid_before)  # no setsid happened
        self.assertEqual(info["argv"], argv)


class OwnerIdentityTests(unittest.TestCase):
    """owner_identity resolves in the pane cwd with --includes so the owner's includeIf gitdir identity applies."""

    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp()).resolve()
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.home = self.tmp / "home"
        self.repo = self.tmp / "tree" / "repo"
        self.repo.mkdir(parents=True)
        self.home.mkdir()
        (self.home / ("." + "gitconfig")).write_text(
            "[user]\n\tname = Default Test\n\temail = test@example.invalid\n"
            f'[includeIf "gitdir:{self.tmp}/tree/repo/"]\n\tpath = {self.home}/inc\n')
        (self.home / "inc").write_text("[user]\n\tname = Noreply Test\n\temail = zz-include@example.invalid\n")
        subprocess.run(["git", "init", "-q", str(self.repo)], check=True,
                       env={"HOME": str(self.home), "PATH": os.environ["PATH"]})

    def test_cwd_inside_includeif_tree_gets_noreply(self):
        self.assertEqual(workerbox.owner_identity(str(self.home), str(self.repo)),
                         ("Noreply Test", "zz-include@example.invalid"))

    def test_cwd_outside_tree_gets_default(self):
        self.assertEqual(workerbox.owner_identity(str(self.home), str(self.tmp)),
                         ("Default Test", "test@example.invalid"))

    def test_missing_or_none_cwd_falls_back_to_root(self):
        want = ("Default Test", "test@example.invalid")
        self.assertEqual(workerbox.owner_identity(str(self.home), None), want)
        self.assertEqual(workerbox.owner_identity(str(self.home), str(self.tmp / "nope")), want)
        self.assertEqual(workerbox.owner_identity(str(self.home)), want)


@NEEDS_USERNS
class RealPathTests(unittest.TestCase):
    """Group C: the owner's real paths (read-only probes, nothing printed). Each check is skipped if its path is absent."""

    @classmethod
    def setUpClass(cls):
        cls.home = Path(nsbox.real_home())
        cls.runtime = Path(f"/run/user/{os.getuid()}")
        cls.paths = {"creds": cls.home / ".claude/.credentials.json", "bus": cls.runtime / "bus",
                     "control": cls.home / CONTROL_REL, "agent": cls.home / AGENT_REL}
        cls.res = {}
        cls.base = Path(tempfile.mkdtemp(prefix="zz-test-workerbox-real-", dir="/var/tmp")).resolve()
        w = cls.base
        put(w / "tokens/worker-oauth-token", OAUTH, 0o600)
        put(w / "tokens/gh-worker-rw-token", GH_RW, 0o600)
        put(w / "claude-bin/claude", PROBE, 0o755)
        put(w / "fh/bin/factory", STUB_FACTORY, 0o755)
        (w / "fh/db").mkdir()
        (w / "fh/worktrees/own").mkdir(parents=True)
        d = workerbox.prepare_agent("realpath-t", "coder", w / "fh/worktrees/own", w / "fh", w / "fh/db",
                                    w / "claude-bin/claude", False, token_dir=w / "tokens", workers_root=w / "workers",
                                    identity=IDENT, gh_slugs=SLUGS)
        ops = [{"op": "open", "path": str(cls.paths["creds"])}] + [
            {"op": "connect", "path": str(cls.paths[k])} for k in ("bus", "control", "agent")] + [
            {"op": "environ_of", "pid": os.getpid()}, {"op": "environ_of", "pid": os.getppid()}]
        env = {"PATH": "/usr/bin:/bin", "LANG": "C.UTF-8", "FACTORY_AGENT": "realpath-t"}
        p = subprocess.run([str(d / "bin/claude"), json.dumps(ops)], cwd=w / "fh/worktrees/own", env=env,
                           capture_output=True, text=True, timeout=60, stdin=subprocess.DEVNULL)
        assert p.returncode == 0, p.stderr
        cls.stdout, cls.stderr = p.stdout, p.stderr
        cls.res = json.loads([l for l in p.stdout.splitlines() if l.startswith("RESULT:")][0][7:])

    @classmethod
    def tearDownClass(cls):
        shutil.rmtree(cls.base, True)

    def refused(self, i, allowed=("ENOENT", "EACCES", "EPERM", "ECONNREFUSED")):
        r = self.res[i]
        self.assertFalse(r["ok"])
        self.assertIn(r["err"], allowed)

    def test_real_credentials_not_openable(self):
        if not self.paths["creds"].exists():
            self.skipTest("no real ~/.claude/.credentials.json on this box")
        self.refused(0)

    def test_real_user_bus_refuses_connect(self):
        if not self.paths["bus"].exists():
            self.skipTest(f"no {self.paths['bus']} on this box")
        self.refused(1)

    def test_real_control_socket_refuses_connect(self):
        if not os.path.lexists(self.paths["control"]):
            self.skipTest("no multiplexer control socket on this box")
        self.refused(2)

    def test_real_agent_proxy_socket_refuses_connect(self):
        if not os.path.lexists(self.paths["agent"]):
            self.skipTest("no agent-proxy socket on this box")
        self.refused(3)

    def test_outside_process_environ_is_denied(self):
        self.assertTrue((Path("/proc") / str(os.getpid()) / "environ").exists())
        for i, who in ((4, "test runner"), (5, "its parent")):
            with self.subTest(who):
                self.refused(i, ("EACCES", "EPERM"))

    def test_output_has_no_secret_content(self):
        for text in (self.stdout, self.stderr):
            for tok in (OAUTH, GH_RW, "fake-credentials"):
                self.assertNotIn(tok, text)


# --- Go toolchain in the worker sandbox (T-1395): offline go/gofmt/vet/test -race inside ---
REAL_HOME = Path(nsbox.real_home())
REAL_GO = REAL_HOME / ".local/go"
REAL_MOD = next((REAL_HOME / rel for rel in workerbox.GO_MODCACHE_REL if (REAL_HOME / rel).is_dir()), None)
NEEDS_GO = unittest.skipUnless((REAL_GO / "bin/go").is_file(), "no ~/.local/go/bin/go on this host")
GO_HEAVY = unittest.skipUnless(os.environ.get("FACTORY_GO_SANDBOX_HEAVY") == "1", "set FACTORY_GO_SANDBOX_HEAVY=1")
GO_KEYS = {"GOROOT", "GOCACHE", "GOTMPDIR", "GOMODCACHE", "GOPATH", "GOTOOLCHAIN", "GOPROXY", "GOSUMDB", "GOFLAGS",
           "GOENV", "GOWORK"}

GO_PROBE = f"""#!{PY}
import errno, json, os, subprocess, sys
if sys.argv[1:2] == ["--session-id"]:
    sys.exit(0)

def errname(e):
    return errno.errorcode.get(e.errno, str(e.errno)) if isinstance(e, OSError) else type(e).__name__

def do(o):
    k = o["op"]
    if k == "run":
        r = subprocess.run(o["argv"], capture_output=True, text=True, cwd=o.get("cwd"),
                           env=dict(os.environ, **o.get("env", {{}})))
        return [r.returncode, r.stdout, r.stderr]
    if k == "write":
        with open(o["path"], "w") as f:
            f.write("x")
        return True
    if k == "exists":
        return os.path.exists(o["path"])
    if k == "env":
        return os.environ.get(o["name"])
    raise SystemExit("bad op " + k)

out = []
for o in json.loads(sys.argv[1]):
    try:
        out.append({{"ok": True, "val": do(o)}})
    except BaseException as e:
        out.append({{"ok": False, "err": errname(e)}})
print("RESULT:" + json.dumps(out), flush=True)
"""


def fake_go(root):
    """A fake GOROOT whose bin/go is a tiny script."""
    put(Path(root) / "bin/go", "#!/bin/sh\necho fake go\n", 0o755)
    return Path(root)


class GoBox:
    """A fake home with tokens, a probe in place of claude and a fake <factory> with one task dir."""

    def __init__(self, base):
        self.base = Path(base).resolve()
        self.home = self.base / "home"
        self.fh = self.home / "factory"
        self.runtime = self.base / "run"
        self.runtime.mkdir()
        (self.fh / "db").mkdir(parents=True)
        self.cwd = self.fh / "worktrees/G-001-T-0001"
        self.cwd.mkdir(parents=True)
        real = put(self.home / ".local/share/claude/versions/1", GO_PROBE, 0o755)
        self.claude = self.home / ".local/bin/claude"
        self.claude.parent.mkdir(parents=True)
        self.claude.symlink_to(real)
        for name in ("worker-oauth-token", "gh-worker-ro-token", "gh-worker-rw-token"):
            put(self.home / ".config/factory" / name, "tok-" + name + "\n", 0o600)
        shutil.copytree(REPO / "scout", self.fh / "scout", ignore=shutil.ignore_patterns("__pycache__"))

    def prepare(self, agent="coder-t-1", role="coder", rw_cwd=True, cwd=None, **kw):
        return workerbox.prepare_agent(agent, role, cwd or self.cwd, self.fh, self.fh / "db", self.claude, rw_cwd,
                                       home=self.home, runtime=self.runtime, identity=IDENT,
                                       gh_slugs=["volekerb/hivemill"], repo=self.fh, **kw)

    def run(self, agent_dir, ops, agent="coder-t-1", role="coder", cwd=None):
        env = {"PATH": f"{self.home}/.local/bin:/usr/bin:/bin", "LANG": "C.UTF-8", "TERM": "xterm",
               "FACTORY_AGENT": agent, "FACTORY_ROLE": role, "FACTORY_HOME": str(self.fh)}
        p = subprocess.run([str(agent_dir / "bin/claude"), json.dumps(ops)], cwd=cwd or self.cwd, env=env,
                           capture_output=True, text=True, timeout=900, stdin=subprocess.DEVNULL)
        res = None
        for line in p.stdout.splitlines():
            if line.startswith("RESULT:"):
                res = json.loads(line[7:])
        assert p.returncode == 0 and res is not None, p.stdout + p.stderr
        return res


class GoBoxCase(unittest.TestCase):
    def setUp(self):
        base = Path(tempfile.mkdtemp(prefix="zz-test-wbgo-", dir="/var/tmp"))
        self.addCleanup(shutil.rmtree, base, True)
        self.box = GoBox(base)
        self.home = self.box.home


class GoUnitTests(GoBoxCase):
    def cfg(self, go=None):
        agent_dir = self.box.base / "agent"
        return workerbox.make_cfg(agent_dir, self.box.cwd, self.box.fh, self.box.fh / "db", self.box.claude, True,
                                  home=self.home, runtime=self.box.runtime, go=go), agent_dir

    def go(self, **kw):
        root = fake_go(self.home / ".local/go")
        mod = self.home / ".cache/go/path/pkg/mod"
        mod.mkdir(parents=True)
        return workerbox.go_paths(self.home, self.box.base / "agent", **kw), root, mod

    def test_goroot_in_ro_and_never_in_rw_and_agent_go_in_rw_and_modcache_in_ro(self):
        go, root, mod = self.go()
        cfg, agent_dir = self.cfg(go)
        self.assertIn(str(root.resolve()), cfg["ro"])
        self.assertIn(str(mod.resolve()), cfg["ro"])
        self.assertNotIn(str(root.resolve()), cfg["rw"])
        self.assertNotIn(str(mod.resolve()), cfg["rw"])
        self.assertIn(f"{agent_dir}/go", cfg["rw"])
        self.assertTrue(cfg["net"])

    def test_only_new_rw_path_is_the_agent_go_dir(self):
        go, _, _ = self.go()
        with_go, agent_dir = self.cfg(go)
        without, _ = self.cfg(None)
        self.assertEqual(set(with_go["rw"]) - set(without["rw"]), {f"{agent_dir}/go"})
        self.assertEqual(with_go["hide_dirs"], without["hide_dirs"])
        self.assertEqual(with_go["masks"], without["masks"])

    def test_missing_bin_go_gives_none_and_a_cfg_equal_to_the_no_go_cfg(self):
        (self.home / ".local/go/bin").mkdir(parents=True)
        go = workerbox.go_paths(self.home, self.box.base / "agent")
        self.assertIsNone(go)
        self.assertEqual(self.cfg(go)[0], self.cfg(None)[0])

    def test_non_executable_bin_go_gives_none(self):
        put(self.home / ".local/go/bin/go", "x", 0o644)
        self.assertIsNone(workerbox.go_paths(self.home, self.box.base / "agent"))

    def test_symlinked_go_root_is_bound_by_its_realpath(self):
        real = fake_go(self.home / "opt/setup-go/1.27")
        (self.home / ".local").mkdir(exist_ok=True)
        (self.home / ".local/go").symlink_to(real)
        go = workerbox.go_paths(self.home, self.box.base / "agent")
        self.assertEqual(go["root"], str(real.resolve()))
        cfg, _ = self.cfg(go)
        self.assertIn(str(real.resolve()), cfg["ro"])
        self.assertNotIn(str(self.home / ".local/go"), cfg["ro"])
        self.assertNotIn(str(real.resolve()), cfg["rw"])

    def test_go_root_outside_home_is_not_bound_at_all(self):
        real = fake_go(self.box.base / "system-go")
        go = workerbox.go_paths(self.home, self.box.base / "agent", go_root=real)
        cfg, _ = self.cfg(go)
        self.assertNotIn(str(real.resolve()), cfg["ro"] + cfg["rw"])

    def test_modcache_first_existing_candidate_wins_and_may_be_none(self):
        fake_go(self.home / ".local/go")
        self.assertIsNone(workerbox.go_paths(self.home, "/a")["modcache"])
        second = self.home / ".local/lib/factory/go/mod"
        second.mkdir(parents=True)
        self.assertEqual(workerbox.go_paths(self.home, "/a")["modcache"], str(second.resolve()))
        first = self.home / ".cache/go/path/pkg/mod"
        first.mkdir(parents=True)
        self.assertEqual(workerbox.go_paths(self.home, "/a")["modcache"], str(first.resolve()))

    def test_go_env_has_exactly_the_eleven_keys_with_the_exact_values(self):
        go, root, mod = self.go()
        env = workerbox.go_env_for(go)
        ad = f"{self.box.base}/agent/go"
        self.assertEqual(env, {"GOROOT": str(root.resolve()), "GOCACHE": f"{ad}/cache", "GOTMPDIR": f"{ad}/tmp",
                               "GOMODCACHE": str(mod.resolve()), "GOPATH": f"{ad}/path", "GOTOOLCHAIN": "local",
                               "GOPROXY": "off", "GOSUMDB": "off", "GOFLAGS": "-mod=readonly", "GOENV": "off",
                               "GOWORK": "off"})
        self.assertEqual(set(env), GO_KEYS)

    def test_go_env_omits_gomodcache_without_a_modcache(self):
        fake_go(self.home / ".local/go")
        env = workerbox.go_env_for(workerbox.go_paths(self.home, "/a"))
        self.assertEqual(set(env), GO_KEYS - {"GOMODCACHE"})

    def test_build_env_path_starts_with_the_caller_path_and_ends_with_goroot_bin(self):
        go, root, _ = self.go()
        genv = workerbox.go_env_for(go)
        env = workerbox.build_env({"PATH": "/agent/bin:/usr/bin", "GOPROXY": "https://evil"}, "/h", "/c", "/g", "o", "g", genv)
        self.assertEqual(env["PATH"], f"/agent/bin:/usr/bin:{root.resolve()}/bin")
        self.assertEqual(env["GOPROXY"], "off")  # HQ's value, not the caller's
        env = workerbox.build_env({}, "/h", "/c", "/g", "o", "g", genv)
        self.assertEqual(env["PATH"], f"{root.resolve()}/bin:/usr/bin:/bin")

    def test_build_env_without_go_env_is_as_before_and_drops_caller_go_vars(self):
        env = workerbox.build_env({"PATH": "/usr/bin", "GOPROXY": "x", "GOFLAGS": "y"}, "/h", "/c", "/g", "o", "g")
        self.assertEqual(env["PATH"], "/usr/bin")
        self.assertFalse(GO_KEYS & set(env))
        self.assertFalse(GO_KEYS & set(workerbox.WORKER_ENV_PASS))

    def test_prepare_agent_stores_go_env_and_creates_private_dirs(self):
        root = fake_go(self.home / ".local/go")
        d = self.box.prepare()
        shim = (d / "bin/claude").read_text()
        self.assertIn("go_env", shim)
        for sub in ("go", "go/cache", "go/tmp"):
            self.assertEqual((d / sub).stat().st_mode & 0o777, 0o700, sub)
        self.assertIn(str(root.resolve()), shim)

    def test_prepare_agent_without_go_has_no_go_env_and_no_go_dir(self):
        d = self.box.prepare()
        self.assertNotIn("go_env", (d / "bin/claude").read_text())
        self.assertFalse((d / "go").exists())

    def test_prepare_agent_refuses_a_symlinked_go_dir_and_writes_nothing_through_it(self):
        fake_go(self.home / ".local/go")
        d = self.box.prepare()
        target = self.box.base / "elsewhere"
        target.mkdir()
        shutil.rmtree(d / "go")
        (d / "go").symlink_to(target)
        with self.assertRaises(ValueError):
            self.box.prepare()
        self.assertEqual(os.listdir(target), [])

    def test_prepare_agent_refuses_a_dangling_symlink_at_go_tmp(self):
        fake_go(self.home / ".local/go")
        d = self.box.prepare()
        shutil.rmtree(d / "go/tmp")
        (d / "go/tmp").symlink_to(self.box.base / "nowhere")
        with self.assertRaises(ValueError):
            self.box.prepare()
        self.assertFalse((self.box.base / "nowhere").exists())

    def test_prepare_agent_refuses_a_symlinked_go_cache_dir(self):
        fake_go(self.home / ".local/go")
        d = self.box.prepare()
        target = self.box.base / "elsewhere"
        target.mkdir()
        shutil.rmtree(d / "go/cache")
        (d / "go/cache").symlink_to(target)
        with self.assertRaises(ValueError):
            self.box.prepare()
        self.assertEqual(os.listdir(target), [])


class GoNamespaceTests(GoBoxCase):
    @NEEDS_USERNS
    def test_goroot_visible_executable_readonly_cache_writable_rest_of_home_hidden(self):
        root = fake_go(self.home / ".local/go")
        mod = self.home / ".cache/go/path/pkg/mod"
        put(mod / "m.txt", "m")
        put(self.home / ".local/owner-file", "secret")
        d = self.box.prepare()
        res = self.box.run(d, [
            {"op": "run", "argv": [str(root.resolve() / "bin/go")]},
            {"op": "write", "path": str(root.resolve() / "bin/planted")},
            {"op": "write", "path": str(mod.resolve() / "planted")},
            {"op": "write", "path": f"{d}/go/cache/ok"},
            {"op": "exists", "path": str(self.home / ".local/owner-file")},
            {"op": "env", "name": "GOPROXY"},
            {"op": "run", "argv": ["sh", "-c", "command -v go"]}])
        self.assertEqual(res[0]["val"][:2], [0, "fake go\n"], res[0])
        self.assertEqual(res[1].get("err"), "EROFS", res[1])
        self.assertEqual(res[2].get("err"), "EROFS", res[2])
        self.assertTrue(res[3]["ok"], res[3])
        self.assertEqual(res[4], {"ok": True, "val": False})
        self.assertEqual(res[5]["val"], "off")
        self.assertEqual(res[6]["val"][1], f"{root.resolve()}/bin/go\n")


def run_in(case, ops, *, role="coder", rw_cwd=True, cwd=None, agent=None):
    agent = agent or f"{role}-t-1"
    d = case.box.prepare(agent, role, rw_cwd=rw_cwd, cwd=cwd, go_root=REAL_GO, go_modcache=REAL_MOD)
    return case.box.run(d, ops, agent=agent, role=role, cwd=cwd)


class GoRealToolchainTests(GoBoxCase):
    @NEEDS_USERNS
    @NEEDS_GO
    def test_go_gofmt_vet_and_race_test_work_inside_offline(self):
        mod = self.box.cwd
        put(mod / "go.mod", "module example.invalid/tiny\n\ngo 1.21\n")
        put(mod / "a.go", "package tiny\n\nfunc Add(a, b int) int { return a + b }\n")
        put(mod / "a_test.go", 'package tiny\n\nimport "testing"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 2) != 3 {\n\t\tt.Fatal("bad")\n\t}\n}\n')
        res = run_in(self, [
            {"op": "run", "argv": ["go", "version"]},
            {"op": "run", "argv": ["go", "env", "GOPROXY", "GOTOOLCHAIN"]},
            {"op": "run", "argv": ["gofmt", "-l", "a.go"]},
            {"op": "run", "argv": ["go", "vet", "./..."]},
            {"op": "run", "argv": ["go", "test", "-race", "./..."]}])
        vals = [r["val"] for r in res]
        self.assertTrue(vals[0][1].startswith("go version go1."), vals[0])
        self.assertEqual(vals[1][1].split(), ["off", "local"], vals[1])
        self.assertEqual(vals[2][:2], [0, ""], vals[2])
        self.assertEqual(vals[3][0], 0, vals[3])
        self.assertEqual(vals[4][0], 0, vals[4])


GO_TEST_PKGS = "go test -race ./..."


class GoHeavyTests(GoBoxCase):
    def clone_go(self):
        dest = self.box.cwd
        # plain local clone (HEAD only, not --shared): the Go tests also read ci/ and roles/ files outside go/
        harness.git("clone", "--quiet", str(REPO), str(dest / "repo"), cwd=dest, local_clone_of=REPO)
        return dest / "repo" / "go"

    def heavy(self, role, rw_cwd):
        gd = self.clone_go()
        # internal/guard runs too: its tests resolve ~/factory against the passwd home, which this box's fake home is
        # not, so go/internal/guard/main_test.go aligns HOME with the passwd home for that package's test process
        live = {"WORKER_GUARD_LIVE_HOME": str(self.box.fh)}
        res = run_in(self, [{"op": "run", "argv": ["go", "vet", "./..."], "cwd": str(gd), "env": live},
                            {"op": "run", "argv": ["sh", "-c", GO_TEST_PKGS], "cwd": str(gd), "env": live}],
                     role=role, rw_cwd=rw_cwd)
        for r, what in zip(res, ("vet", "test -race")):
            self.assertTrue(r["ok"], r)
            self.assertEqual(r["val"][0], 0, f"go {what}: {r['val'][1]}\n{r['val'][2]}")

    @GO_HEAVY
    @NEEDS_USERNS
    @NEEDS_GO
    def test_heavy_coder_vet_and_race_test_of_go_module_offline(self):
        self.heavy("coder", True)

    @GO_HEAVY
    @NEEDS_USERNS
    @NEEDS_GO
    def test_heavy_qa_vet_and_race_test_of_go_module_offline(self):
        self.heavy("qa", True)

    @GO_HEAVY
    @NEEDS_USERNS
    @NEEDS_GO
    def test_heavy_reviewer_vet_and_race_test_of_go_module_offline(self):
        self.heavy("reviewer", False)


if __name__ == "__main__":
    unittest.main()
