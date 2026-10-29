"""qa/coder worker profile on a per-task clone (T-0167): scout/workerbox.py prepare_agent with rw_cwd=True, end to end.

The cwd is a standalone `git clone --shared` of a fake <factory> (objects through alternates, origin = a local bare
repo over file://), as T-0165's make_task_clone builds it. The real unshare chain runs a probe in place of claude;
the probe runs git / python / the factory CLI inside the sandbox and reports exit codes and output. The tokens are
fake and never printed: the probe redacts them and the tests compare by hash. Everything lives under /var/tmp
(/tmp is a hide tree). Skips with userns_available()'s reason when the box cannot make namespaces.
"""
import ast
import hashlib
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
from harness import FACTORY, FactoryTestCase  # noqa: E402
from harness import git as harness_git  # noqa: E402

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
from scout import nsbox, workerbox  # noqa: E402

USERNS_OK, USERNS_WHY = nsbox.userns_available()
NEEDS_USERNS = unittest.skipUnless(USERNS_OK, f"user namespaces unavailable: {USERNS_WHY}")
PY = sys.executable
OAUTH = "oauth-" + "A1b2" * 8
GH_RO = "ghro-" + "C3d4" * 8
GH_RW = "ghrw-" + "E5f6" * 8
IDENT = ("Test Owner", "owner@example.invalid")
TASK = "G-001-T-0001"
BRANCH = "factory/T-0001"

PROBE = f"""#!{PY}
import errno, hashlib, json, os, subprocess, sys
if sys.argv[1:2] == ["--session-id"]:
    sys.exit(0)
SECRETS = [os.environ.get(k) for k in ("GH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN")]

def redact(s):
    for t in SECRETS:
        if t:
            s = s.replace(t, "<token>")
    return s

def errname(e):
    return errno.errorcode.get(e.errno, str(e.errno)) if isinstance(e, OSError) else type(e).__name__

def do(o):
    k = o["op"]
    if k == "run":
        r = subprocess.run(o["argv"], capture_output=True, text=True, input=o.get("input"), cwd=o.get("cwd"))
        return [r.returncode, redact(r.stdout), redact(r.stderr)]
    if k == "write":
        with open(o["path"], "w") as f:
            f.write("x")
        return True
    if k == "mkdir":
        os.makedirs(o["path"])
        return True
    if k == "listdir":
        return sorted(os.listdir(o["path"]))
    if k == "gh_sha":
        return hashlib.sha256(os.environ["GH_TOKEN"].encode()).hexdigest()
    if k == "credential":
        r = subprocess.run(["git", "credential", "fill"], capture_output=True, text=True,
                           input="protocol=https\\nhost=github.com\\n\\n", env=dict(os.environ, GIT_TERMINAL_PROMPT="0"))
        kv = dict(l.split("=", 1) for l in r.stdout.splitlines() if "=" in l)
        return {{"rc": r.returncode, "username": kv.get("username"),
                "password_is_gh_token": kv.get("password") == os.environ["GH_TOKEN"]}}
    raise SystemExit("bad op " + k)

out = []
for o in json.loads(sys.argv[1]):
    try:
        out.append({{"ok": True, "val": do(o)}})
    except BaseException as e:
        out.append({{"ok": False, "err": errname(e)}})
print("RESULT:" + json.dumps(out), flush=True)
"""

# a gh that only says whether GH_TOKEN reached it; the real tests/stubs/gh needs FAKE_* variables the sandbox drops
GH_STUB = f"""#!{PY}
import os
print("GH_TOKEN set" if os.environ.get("GH_TOKEN") else "GH_TOKEN unset")
"""


def git(*args, cwd):
    env = {**os.environ, "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.invalid", "GIT_COMMITTER_NAME": "t",
           "GIT_COMMITTER_EMAIL": "t@example.invalid", "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": "/dev/null"}  # no owner-level git config
    return harness_git(*args, cwd=cwd, env={k: v for k, v in env.items() if k.startswith("GIT_")})


def put(path, text="", mode=None):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text)
    if mode:
        path.chmod(mode)
    return path


class Box:
    """A fake home with tokens and a probe in place of claude, around a given fake <factory>."""

    def __init__(self, home, fh, runtime):
        self.home, self.fh, self.runtime = Path(home), Path(fh), Path(runtime)
        self.runtime.mkdir(exist_ok=True)
        self.claude_real = put(self.home / ".local/share/claude/versions/1", PROBE, 0o755)
        self.claude = self.home / ".local/bin/claude"
        self.claude.parent.mkdir(parents=True, exist_ok=True)
        self.claude.symlink_to(self.claude_real)
        for name, tok in (("worker-oauth-token", OAUTH), ("gh-worker-ro-token", GH_RO), ("gh-worker-rw-token", GH_RW)):
            put(self.home / ".config/factory" / name, tok + "\n", 0o600)

    def prepare(self, agent, role, cwd, rw_cwd=True):
        return workerbox.prepare_agent(agent, role, cwd, self.fh, self.fh / "db", self.claude, rw_cwd, home=self.home,
                                       runtime=self.runtime, identity=IDENT)

    def caller_env(self, agent, role):
        return {"PATH": f"{self.home}/.local/bin:/usr/bin:/bin", "LANG": "C.UTF-8", "TERM": "xterm", "LC_ALL": "C.UTF-8",
                "FACTORY_AGENT": agent, "FACTORY_ROLE": role, "FACTORY_HOME": str(self.fh)}

    def run(self, agent_dir, ops, agent, role, cwd):
        """Run the generated shim with a probe op list; returns (CompletedProcess, results or None)."""
        p = subprocess.run([str(agent_dir / "bin/claude"), json.dumps(ops)], cwd=cwd, env=self.caller_env(agent, role),
                           capture_output=True, text=True, timeout=120, stdin=subprocess.DEVNULL)
        res = None
        for line in p.stdout.splitlines():
            if line.startswith("RESULT:"):
                res = json.loads(line[7:])
        return p, res


class CloneProfileTests(unittest.TestCase):
    """The qa/coder profile (rw_cwd=True) on a task clone: a fake <factory>, a bare origin and two task clones."""

    def setUp(self):
        base = Path(tempfile.mkdtemp(prefix="zz-test-wbclone-", dir="/var/tmp")).resolve()
        self.addCleanup(shutil.rmtree, base, True)
        self.base = base
        self.fh = base / "home/factory"
        self.bare = base / "origin.git"
        self.url = f"file://{self.bare}"
        put(self.fh / "tests/stubs/gh", GH_STUB, 0o755)
        put(self.fh / "README", "factory\n")
        (self.fh / "db").mkdir()
        git("init", "-q", "-b", "main", cwd=self.fh)
        git("add", "README", "tests/stubs/gh", cwd=self.fh)
        git("commit", "-q", "-m", "base", cwd=self.fh)
        git("init", "-q", "--bare", "-b", "main", str(self.bare), cwd=base)
        git("remote", "add", "origin", self.url, cwd=self.fh)
        git("push", "-q", "-u", "origin", "main", cwd=self.fh)
        self.own = self.make_clone(TASK, BRANCH)
        self.other = self.make_clone("G-001-T-0002", "factory/T-0002")
        # an object only the shared <factory>/.git has: the clones reach it through alternates alone
        put(self.fh / "only-in-factory", "alternates\n")
        git("add", "only-in-factory", cwd=self.fh)
        git("commit", "-q", "-m", "factory-only", cwd=self.fh)
        self.factory_only = git("rev-parse", "HEAD", cwd=self.fh)
        self.box = Box(base / "home", self.fh, base / "run")
        self.d = self.box.prepare("coder-t-1", "coder", self.own)

    def make_clone(self, name, branch):
        wt = self.fh / "worktrees" / name
        git("-c", "protocol.file.allow=always", "clone", "-q", "--shared", "--no-checkout", str(self.fh), str(wt), cwd=self.base)
        git("remote", "set-url", "origin", self.url, cwd=wt)
        git("fetch", "-q", "origin", cwd=wt)
        git("checkout", "-q", "-b", branch, "origin/main", cwd=wt)
        return wt

    def push_to_origin(self, name, text, ref="refs/heads/main"):
        """A commit on top of origin's main made by someone else, pushed to `ref` of the bare repo."""
        seed = self.base / f"seed-{name}"
        git("-c", "protocol.file.allow=always", "clone", "-q", self.url, str(seed), cwd=self.base)
        put(seed / name, text)
        git("add", name, cwd=seed)
        git("commit", "-q", "-m", name, cwd=seed)
        git("push", "-q", "origin", f"HEAD:{ref}", cwd=seed)
        return git("rev-parse", "HEAD", cwd=seed)

    def probe(self, ops, d=None, agent="coder-t-1", role="coder"):
        p, res = self.box.run(d or self.d, ops, agent, role, self.own)
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIsNotNone(res, p.stdout + p.stderr)
        return res

    def sh(self, *argv):
        """One command inside; (rc, stdout, stderr)."""
        (r,) = self.probe([{"op": "run", "argv": list(argv)}])
        self.assertTrue(r["ok"], r)
        return tuple(r["val"])

    def ok(self, *argv):
        rc, out, err = self.sh(*argv)
        self.assertEqual(rc, 0, f"{argv}: rc={rc}\n{out}\n{err}")
        return out

    def outside(self, *args):
        return git(*args, cwd=self.own)

    @NEEDS_USERNS
    def test_git_status_log_diff_and_commit_inside(self):
        self.assertEqual(self.ok("git", "status", "--porcelain"), "")
        self.assertIn("base", self.ok("git", "log", "--oneline"))
        put(self.own / "README", "changed\n")
        self.assertIn("README", self.ok("git", "diff", "--stat"))
        self.ok("git", "commit", "-q", "-am", "change")
        self.assertEqual(self.ok("git", "log", "-1", "--format=%s"), "change\n")
        self.assertEqual(self.outside("log", "-1", "--format=%s"), "change")  # the commit is in the clone on the host
        self.assertEqual(self.outside("log", "-1", "--format=%an <%ae>"), "Test Owner <owner@example.invalid>")

    @NEEDS_USERNS
    def test_fetch_and_merge_origin_main_against_a_local_bare_origin(self):
        self.ok("git", "commit", "-q", "--allow-empty", "-m", "local work")
        new = self.push_to_origin("upstream", "from origin\n")
        self.ok("git", "fetch", "origin")
        self.assertEqual(self.outside("rev-parse", "origin/main"), new)
        self.ok("git", "merge", "--no-edit", "origin/main")
        self.assertEqual((self.own / "upstream").read_text(), "from origin\n")
        self.assertEqual(self.sh("git", "merge-base", "--is-ancestor", "origin/main", "HEAD")[0], 0)

    @NEEDS_USERNS
    def test_push_task_branch_lands_in_the_bare_repo(self):
        self.ok("git", "commit", "-q", "--allow-empty", "-m", "work")
        self.ok("git", "push", "-u", "origin", "HEAD")
        head = self.outside("rev-parse", "HEAD")
        self.assertEqual(git("rev-parse", f"refs/heads/{BRANCH}", cwd=self.bare), head)
        self.assertEqual(self.outside("rev-parse", f"origin/{BRANCH}"), head)

    @NEEDS_USERNS
    def test_credential_helper_returns_x_access_token_and_the_gh_token(self):
        self.assertEqual(self.ok("git", "config", "--global", "--get", "credential.https://github.com.helper").strip(),
                         workerbox.GH_HELPER)
        cred, gh_sha = self.probe([{"op": "credential"}, {"op": "gh_sha"}])
        self.assertEqual(cred["val"], {"rc": 0, "username": "x-access-token", "password_is_gh_token": True})
        self.assertTrue(gh_sha["val"] == hashlib.sha256(GH_RW.encode()).hexdigest(), "GH_TOKEN is not the coder rw token")

    @NEEDS_USERNS
    def test_objects_only_in_the_factory_git_dir_are_readable_through_alternates(self):
        sha = self.factory_only
        self.assertFalse((self.own / ".git/objects" / sha[:2] / sha[2:]).exists())  # not in the clone's own store
        self.assertEqual(self.sh("git", "cat-file", "-e", sha)[0], 0)
        self.assertEqual(self.sh("git", "cat-file", "-p", f"{sha}:only-in-factory")[1], "alternates\n")

    @NEEDS_USERNS
    def test_qa_fetches_a_pr_ref_checks_it_out_detached_and_runs_a_command(self):
        pr = self.push_to_origin("pr-change", "from the PR\n", ref="refs/pull/1/head")
        d = self.box.prepare("qa-t-1", "qa", self.own)
        res = self.probe([{"op": "run", "argv": ["git", "fetch", "origin", "refs/pull/1/head"]},
                          {"op": "run", "argv": ["git", "checkout", "-q", "--detach", "FETCH_HEAD"]},
                          {"op": "run", "argv": ["python3", "-c", "print(1)"]}], d=d, agent="qa-t-1", role="qa")
        self.assertEqual([r["val"][0] for r in res], [0, 0, 0], res)
        self.assertEqual(res[2]["val"][1], "1\n")
        self.assertEqual(self.outside("rev-parse", "HEAD"), pr)
        self.assertEqual(subprocess.run(["git", "symbolic-ref", "-q", "HEAD"], cwd=self.own).returncode, 1)  # detached

    @NEEDS_USERNS
    def test_gh_stub_inside_reports_gh_token_set(self):
        self.assertEqual(self.ok(str(self.fh / "tests/stubs/gh")), "GH_TOKEN set\n")
        outside = subprocess.run([str(self.fh / "tests/stubs/gh")], capture_output=True, text=True,
                                 env=self.box.caller_env("coder-t-1", "coder"))
        self.assertEqual(outside.stdout, "GH_TOKEN unset\n")  # only the shim sets it

    @NEEDS_USERNS
    def test_shared_git_dir_is_read_only_inside(self):
        g = self.fh / ".git"
        res = self.probe([{"op": "write", "path": str(g / "config")},
                          {"op": "write", "path": str(g / "hooks/x")},
                          {"op": "write", "path": str(g / "info/attributes")},
                          {"op": "write", "path": str(g / "refs/heads/planted")},
                          {"op": "write", "path": str(self.fh / "README")}])
        self.assertEqual([r.get("err") for r in res], ["EROFS"] * 5, res)
        self.assertNotIn("planted", os.listdir(g / "refs/heads"))
        self.assertFalse((g / "hooks/x").exists())

    @NEEDS_USERNS
    def test_another_task_dir_is_not_listed(self):
        (res,) = self.probe([{"op": "listdir", "path": str(self.fh / "worktrees")}])
        self.assertEqual(res["val"], [TASK])
        (res,) = self.probe([{"op": "listdir", "path": str(self.other)}])
        self.assertEqual(res.get("err"), "ENOENT", res)

    def test_the_clone_is_the_only_rw_path_beyond_db_and_claude_dir(self):
        """The gate is not weakened: coder/qa get exactly their clone writable, reviewer none."""
        for role, rw_cwd in (("coder", True), ("qa", True), ("reviewer", False)):
            ns = self.shim_ns(self.box.prepare(f"{role}-t-9", role, self.own, rw_cwd))
            want = [str(self.fh / "db"), str(self.box.home / f".local/state/factory/workers/{role}-t-9/claude")]
            self.assertEqual(ns["rw"], want + ([str(self.own)] if rw_cwd else []), role)
            self.assertEqual(self.fh.as_posix() in ns["ro"], True)
            self.assertEqual(str(self.own) in ns["ro"], not rw_cwd, role)

    @staticmethod
    def shim_ns(agent_dir):
        text = (agent_dir / "bin/claude").read_text()
        line = next(l for l in text.splitlines() if l.startswith("workerbox.shim_main("))
        return json.loads(ast.literal_eval(line[len("workerbox.shim_main(json.loads("):].rsplit("), sys.argv", 1)[0]))["ns"]


class FactoryDoneTests(FactoryTestCase):
    """`factory done` of a coder inside the sandbox, on a clone the factory itself made, in a temp FACTORY_HOME."""

    def setUp(self):
        with mock.patch.object(tempfile, "tempdir", "/var/tmp"):  # /tmp is a hide tree: the home must live elsewhere
            super().setUp()
        e = self.env
        e.seed_project()
        e.seed_goal(project="zz-test-proj", title="zz-test clone goal")
        e.seed_task(goal="G-001", status="in_progress")
        e.seed_agent("zz-test-coder", "coder", goal="G-001", task="T-0001")
        self.h = e.home_dir
        self.bare = e.home / "origin.git"
        git("init", "-q", "--bare", "-b", "main", str(self.bare), cwd=e.home)
        git("remote", "add", "origin", str(self.bare), cwd=self.h)  # a plain path: the factory refuses a file:// url in git config
        git("commit", "-q", "--allow-empty", "-m", "base", cwd=self.h)
        git("push", "-q", "-u", "origin", "main", cwd=self.h)
        e.sql("UPDATE projects SET repo_path=? WHERE id='zz-test-proj'", (str(self.h),))
        self.ok("config", "task_clones", "1")
        with e.load_factory() as mod:
            con = mod.db()
            mod.prepare_worktree(con, "T-0001", "coder")
            con.commit()
        self.wt = self.h / "worktrees" / TASK
        self.assertTrue((self.wt / ".git").is_dir(), "task_clones=1 did not make a standalone clone")
        (self.h / "bin").mkdir(exist_ok=True)  # the repo checkout is under the hidden home: the sandbox runs a copy
        shutil.copy2(FACTORY, self.h / "bin/factory")
        for pkg in ("scout", "web"):  # bin/factory imports both from the checkout root
            shutil.copytree(FACTORY.parent.parent / pkg, self.h / pkg, ignore=shutil.ignore_patterns("__pycache__"))
        self.box = Box(e.user_home, self.h, e.home / "run-hidden")
        self.d = self.box.prepare("zz-test-coder", "coder", self.wt)

    def inside(self, *argv):
        p, res = self.box.run(self.d, [{"op": "run", "argv": list(argv)}], "zz-test-coder", "coder", self.wt)
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertTrue(res[0]["ok"], res)
        return tuple(res[0]["val"])

    @NEEDS_USERNS
    def test_coder_done_is_refused_with_an_unpushed_commit_and_accepted_after_the_push(self):
        done = [PY, str(self.h / "bin/factory"), "done", "finished T-0001"]
        rc, out, err = self.inside("git", "commit", "-q", "--allow-empty", "-m", "work")
        self.assertEqual(rc, 0, err)
        rc, out, err = self.inside(*done)
        self.assertEqual(rc, 3, f"{out}\n{err}")
        self.assertIn("is not on origin/", err)
        self.assertEqual(self.env.sql("SELECT status FROM agents WHERE name='zz-test-coder'")[0]["status"], "live")
        branch = git("rev-parse", "--abbrev-ref", "HEAD", cwd=self.wt)
        rc, out, err = self.inside("git", "push", "-q", "-u", "origin", "HEAD")
        self.assertEqual(rc, 0, err)
        self.assertEqual(git("rev-parse", f"refs/heads/{branch}", cwd=self.bare), git("rev-parse", "HEAD", cwd=self.wt))
        rc, out, err = self.inside(*done)
        self.assertEqual(rc, 0, f"{out}\n{err}")
        self.assertEqual(self.env.sql("SELECT status FROM agents WHERE name='zz-test-coder'")[0]["status"], "done")


if __name__ == "__main__":
    unittest.main()
