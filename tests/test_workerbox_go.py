"""Go toolchain in the worker sandbox (T-1395): scout/workerbox.py go_paths / make_cfg / build_env / prepare_agent.

Unit tests need no namespace. The real-namespace tests run a probe in place of claude through the generated shim and
skip with userns_available()'s reason; the real-toolchain tests skip only when ~/.local/go/bin/go is missing; the
heavy tests (go vet / go test -race of this checkout's go/ module, offline) run only with FACTORY_GO_SANDBOX_HEAVY=1.
Everything lives under /var/tmp (/tmp is a hide tree); the tokens are fake.
"""
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
from scout import nsbox, workerbox  # noqa: E402

USERNS_OK, USERNS_WHY = nsbox.userns_available()
NEEDS_USERNS = unittest.skipUnless(USERNS_OK, f"user namespaces unavailable: {USERNS_WHY}")
PY = sys.executable
REPO = Path(__file__).resolve().parent.parent
REAL_HOME = Path(nsbox.real_home())
REAL_GO = REAL_HOME / ".local/go"
REAL_MOD = next((REAL_HOME / rel for rel in workerbox.GO_MODCACHE_REL if (REAL_HOME / rel).is_dir()), None)
NEEDS_GO = unittest.skipUnless((REAL_GO / "bin/go").is_file(), "no ~/.local/go/bin/go on this host")
HEAVY = unittest.skipUnless(os.environ.get("FACTORY_GO_SANDBOX_HEAVY") == "1", "set FACTORY_GO_SANDBOX_HEAVY=1")
IDENT = ("Test Owner", "owner@example.invalid")
GO_KEYS = {"GOROOT", "GOCACHE", "GOTMPDIR", "GOMODCACHE", "GOPATH", "GOTOOLCHAIN", "GOPROXY", "GOSUMDB", "GOFLAGS",
           "GOENV", "GOWORK"}

PROBE = f"""#!{PY}
import errno, json, os, subprocess, sys
if sys.argv[1:2] == ["--session-id"]:
    sys.exit(0)

def errname(e):
    return errno.errorcode.get(e.errno, str(e.errno)) if isinstance(e, OSError) else type(e).__name__

def do(o):
    k = o["op"]
    if k == "run":
        r = subprocess.run(o["argv"], capture_output=True, text=True, cwd=o.get("cwd"))
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


def put(path, text="", mode=None):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text)
    if mode:
        path.chmod(mode)
    return path


def fake_go(root):
    """A fake GOROOT whose bin/go is a tiny script."""
    put(Path(root) / "bin/go", "#!/bin/sh\necho fake go\n", 0o755)
    return Path(root)


class Box:
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
        real = put(self.home / ".local/share/claude/versions/1", PROBE, 0o755)
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


class BoxCase(unittest.TestCase):
    def setUp(self):
        base = Path(tempfile.mkdtemp(prefix="zz-test-wbgo-", dir="/var/tmp"))
        self.addCleanup(shutil.rmtree, base, True)
        self.box = Box(base)
        self.home = self.box.home


class UnitTests(BoxCase):
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


class NamespaceTests(BoxCase):
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


class RealToolchainTests(BoxCase):
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


class HeavyTests(BoxCase):
    def clone_go(self):
        dest = self.box.cwd
        shutil.rmtree(dest)
        subprocess.run(["git", "-c", "protocol.file.allow=always", "clone", "-q", "--shared", str(REPO), str(dest)],
                       check=True, env={**os.environ, "GIT_CONFIG_GLOBAL": "/dev/null"})
        subprocess.run(["git", "checkout", "-q", "--detach", subprocess.run(
            ["git", "rev-parse", "HEAD"], cwd=REPO, capture_output=True, text=True, check=True).stdout.strip()],
            cwd=dest, check=True)
        # the working tree of the checkout may hold uncommitted changes: copy go/ over the clone
        shutil.rmtree(dest / "go", ignore_errors=True)
        shutil.copytree(REPO / "go", dest / "go")
        return dest / "go"

    def heavy(self, role, rw_cwd):
        gd = self.clone_go()
        res = run_in(self, [{"op": "run", "argv": ["go", "vet", "./..."], "cwd": str(gd)},
                            {"op": "run", "argv": ["go", "test", "-race", "./..."], "cwd": str(gd)}],
                     role=role, rw_cwd=rw_cwd)
        for r, what in zip(res, ("vet", "test -race")):
            self.assertTrue(r["ok"], r)
            self.assertEqual(r["val"][0], 0, f"go {what}: {r['val'][1]}\n{r['val'][2]}")

    @HEAVY
    @NEEDS_USERNS
    @NEEDS_GO
    def test_heavy_coder_vet_and_race_test_of_go_module_offline(self):
        self.heavy("coder", True)

    @HEAVY
    @NEEDS_USERNS
    @NEEDS_GO
    def test_heavy_qa_vet_and_race_test_of_go_module_offline(self):
        self.heavy("qa", True)

    @HEAVY
    @NEEDS_USERNS
    @NEEDS_GO
    def test_heavy_reviewer_vet_and_race_test_of_go_module_offline(self):
        self.heavy("reviewer", False)


if __name__ == "__main__":
    unittest.main()
