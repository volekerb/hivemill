"""T-1035: oracle test for the `git config` read classifier. Invariant: for every corpus form, if real git WROTE the
`--file` target, the guard refuses `git config <form>` for the coder (exit 2). Only that direction is asserted, so it
holds across git versions; a git error counts as no write.

Real git runs ONLY here, in a subprocess under a fence sanctioned by M23527 (L-023): cwd=<tmp> outside any repo,
GIT_CEILING_DIRECTORIES=<tmp>, GIT_CONFIG_NOSYSTEM=1, HOME=<tmp>, GIT_DIR and every GIT_CONFIG* removed, target
`--file <tmp>/cfg`. A tripwire snapshots the real common-dir config and fails the test if it changed."""
import json
import os
import shutil
import sqlite3
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from guard_impl import hook_run  # noqa: E402
from harness import git  # noqa: E402

GUARD_IMPL_PYTHON_ONLY = "Go mirror follow-up in G-016"
TASK = "t-1035"
SEED = "[alias]\n\tx = status\n\ty = log\n[core]\n\tbare = false\n"

# every option git-config(1) knows (value options flagged), crossed below with each unique-ish prefix
VALUE_OPTS = ["--type", "--default", "--comment", "--blob", "--value", "--file", "--fixed-value"]
PLAIN_OPTS = ["--get", "--get-all", "--get-regexp", "--get-urlmatch", "--list", "--show-origin", "--show-scope",
              "--name-only", "--all", "--global", "--local", "--system", "--worktree", "--null", "--bool", "--int",
              "--bool-or-int", "--path", "--includes", "--no-includes", "--unset", "--unset-all", "--add",
              "--replace-all", "--edit", "--rename-section", "--remove-section"]
HEADS = ["get", "list", "--get", "--list"]
TAIL = ["alias.x", "push"]
CORPUS = []
for _o in VALUE_OPTS:
    for _p in sorted({_o[:n] for n in range(3, len(_o) + 1)}):
        for _h in HEADS:
            CORPUS.append([_p, _h, *TAIL])
for _o in PLAIN_OPTS:
    for _p in sorted({_o[:n] for n in range(3, len(_o) + 1)}):
        CORPUS.append([_p, "alias.x", "push"])
        CORPUS.append([_p, "alias.x"])
for _extra in ("--", "-ff", "-f"):
    for _h in HEADS:
        CORPUS.append([_extra, _h, *TAIL])
        CORPUS.append([_h, *TAIL, _extra])
CORPUS += [
    ["set", "--va", "--get", "alias.y", "push"], ["set", "--value", "--get", "alias.y", "push"],
    ["--ty", "list", "alias.x", "push"], ["--local", "list", "alias.x", "push"], ["--fixed-value", "list", "alias.x", "push"],
    ["alias.x", "push", "--get"], ["alias.x", "push", "--", "--get"], ["--", "--get", "alias.x", "push"],
    ["--get", "--", "alias.x", "push"], ["--unset-a", "alias.x"], ["--remove-s", "alias"],
    # T-1064: restored read forms and their neighbours; M23762: coder `-t bool <key>` and `--value x <key>`
    ["--get", "alias.x", "--show-origin"], ["--get", "alias.x", "--null"], ["list", "--show-origin"], ["list", "--name-only"],
    ["get", "--all", "alias.x"], ["get", "--show-origin", "alias.x"], ["get", "--regexp", "alias"], ["--get-color", "color.x"],
    ["--get-colorbool", "color.ui"], ["--get", "alias.x", "--local"], ["--get", "alias.x", "--bool"],
    ["--get", "alias.x", "--int"], ["--get", "alias.x", "--path"], ["--get", "alias.x", "--includes"],
    ["--get", "alias.x", "--global"], ["--get", "alias.x", "--system"], ["--regexp", "alias"], ["--regexp", "alias.x", "push"],
    ["alias.x", "push", "--null"], ["alias.x", "push", "--bool"], ["alias.x", "--bool", "push"], ["set", "alias.x", "push", "--local"],
    ["-t", "bool", "alias.x"], ["-t", "bool", "core.bare", "true"], ["--value", "x", "alias.y"], ["--value", "x", "alias.y", "push"],
    ["--get-color", "color.x", "push"], ["--get-colorbool", "color.ui", "alias.x", "push"],
]
# T-1064 v2: an option after a bare key is the VALUE (git's legacy parser stops at the first non-option)
VALUE_FORMS = [["-t", "bool"], ["--type=bool"], ["--default", "v"], ["--comment", "c"], ["--value", "v"], ["--value=v"]]
LATE_FORMS = [["alias.y", "--null"], ["core.hooksPath", "--all"], ["core.fsmonitor", "--null"],
              ["--global", "core.hooksPath", "--all"]]
LATE_FORMS += [[k, *v] for k in ("alias.y", "core.bare") for v in VALUE_FORMS]
CORPUS += LATE_FORMS
CORPUS = [list(f) for f in dict.fromkeys(tuple(f) for f in CORPUS)]

# T-1064 v3 acceptance 9: every scope/file token at every position, key core.hooksPath. The hook runs with cwd in a linked
# worktree (see setUp); a strip loop that is not order-aware turns `core.hooksPath --global` into a "read".
SCOPE_TOKENS = [["--global"], ["--glob"], ["--system"], ["--file=x"], ["--file", "x"], ["-f", "x"], ["-fx"]]
CROSS_KEY, CROSS_VAL = "core.hooksPath", "/tmp/x"
CROSS = []
for _t in SCOPE_TOKENS:
    CROSS += [[*_t, CROSS_KEY, CROSS_VAL], [CROSS_KEY, *_t], [CROSS_KEY, CROSS_VAL, *_t]]
    for _o in ("--type", "-t", "--default", "--comment", "--blob", "--value", "--file", "-f"):
        CROSS.append([_o, *_t, CROSS_KEY, CROSS_VAL])
    for _w in ("set", "unset"):
        CROSS += [[_w, *_t, CROSS_KEY, CROSS_VAL], [_w, CROSS_KEY, CROSS_VAL, *_t], [_w, CROSS_KEY, *_t]]
# T-1064 v5 acceptance 15: scratchpad-path file tokens after key+value (<S> is filled with the scratch dir at run time)
for _t in (["--file=<S>/y"], ["-f<S>/y"]):
    for _p in ([], ["--replace-all"], ["--rep"], ["--fixed-value"], ["--no-type"], ["-z"]):
        CROSS.append([*_p, CROSS_KEY, CROSS_VAL, *_t])
CROSS += [["--comment", "--global", CROSS_KEY, CROSS_VAL], ["--global", CROSS_KEY, "--global"], ["--global", CROSS_KEY, "--file=x"]]
CROSS = [list(f) for f in dict.fromkeys(tuple(f) for f in CROSS)]


def common_config():
    out = subprocess.run(["git", "rev-parse", "--git-common-dir"], capture_output=True, text=True, check=True,
                         cwd=os.path.dirname(os.path.abspath(__file__)))
    d = out.stdout.strip()
    if not os.path.isabs(d):
        d = os.path.join(os.path.dirname(os.path.abspath(__file__)), d)
    return os.path.join(d, "config")


def snapshot(path):
    try:
        with open(path, "rb") as f:
            return f.read()
    except FileNotFoundError:
        return None


def fenced_env(tmp):
    env = {k: v for k, v in os.environ.items() if k != "GIT_DIR" and not k.startswith("GIT_CONFIG")}
    env.update(HOME=tmp, XDG_CONFIG_HOME=os.path.join(tmp, "xdg"), GIT_CEILING_DIRECTORIES=tmp, GIT_CONFIG_NOSYSTEM="1", LC_ALL="C",
               GIT_CONFIG_SYSTEM=os.path.join(tmp, "system"))  # T-1064: --system must write under tmp, never the real system file
    return env


class ConfigOracleTests(unittest.TestCase):
    def setUp(self):
        self.real_cfg = common_config()
        self.before = snapshot(self.real_cfg)
        td = tempfile.TemporaryDirectory()
        self.addCleanup(td.cleanup)
        self.tmp = os.path.realpath(td.name)
        self.oracle = os.path.join(self.tmp, "oracle")
        self.scratch = os.path.join(self.tmp, "scratch")
        os.makedirs(self.oracle)
        os.makedirs(self.scratch)
        self.home = os.path.join(self.tmp, "factory")
        self.wt = os.path.join(self.home, "worktrees", "G-010-T-1035")
        main = os.path.join(self.home, "main-checkout")
        for d in (os.path.join(self.home, ".git"), os.path.join(self.home, "roles"), os.path.join(self.home, "db"), main,
                  os.path.dirname(self.wt)):
            os.makedirs(d)
        # a LINKED worktree: the shared-repo rule fires only there (not at /tmp), as for real workers
        git("init", "-q", "-b", "main", main, cwd=self.tmp)
        git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "i", cwd=main)
        git("worktree", "add", "-q", "-b", "factory/t-1035", self.wt, cwd=main)
        con = sqlite3.connect(os.path.join(self.home, "db", "factory.db"))
        con.execute("CREATE TABLE tasks (id TEXT PRIMARY KEY, worktree TEXT)")
        con.execute("INSERT INTO tasks VALUES (?, ?)", (TASK.upper(), self.wt))
        con.commit()
        con.close()

    def tearDown(self):
        self.assertEqual(snapshot(self.real_cfg), self.before, "TRIPWIRE: the real common-dir git config changed")

    def fill(self, form):
        return [t.replace("<S>", self.scratch) for t in form]

    def hook(self, cmd, role="coder"):
        env = {k: v for k, v in os.environ.items() if k not in ("WORKER_GUARD_LIVE_HOME", "FACTORY_AGENT")}
        env.update(HOME=self.tmp, WORKER_GUARD_LIVE_HOME=self.home, FACTORY_HOME=self.home, WORKER_GUARD_SCRATCH=self.scratch,
                   FACTORY_AGENT=f"coder-{TASK}" if role == "coder" else f"{role}-x")
        payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": self.wt})
        return hook_run(payload, ["--role", role], env=env)

    def git_wrote(self, form):
        cfg = os.path.join(self.oracle, "cfg")
        with open(cfg, "w") as f:
            f.write(SEED)
        before = snapshot(cfg)
        try:
            subprocess.run(["git", "config", "--file", cfg, *form], cwd=self.oracle, env=fenced_env(self.oracle),
                           capture_output=True, text=True, timeout=20, stdin=subprocess.DEVNULL)
        except subprocess.TimeoutExpired:
            return False
        return snapshot(cfg) != before

    def git_wrote_unfiled(self, form):
        """Same form without --file: real git in a fresh temp repo with a temp HOME (scope flags work here)."""
        form = self.fill(form)
        repo = os.path.join(self.tmp, "unfiled")
        home = os.path.join(self.tmp, "unfiled-home")
        for d in (repo, home):
            shutil.rmtree(d, ignore_errors=True)
            os.makedirs(d)
        env = fenced_env(repo)
        env["HOME"] = home
        env["XDG_CONFIG_HOME"] = os.path.join(home, "xdg")
        subprocess.run(["git", "init", "-q", repo], cwd=repo, env=env, check=True, stdin=subprocess.DEVNULL,
                       capture_output=True)
        local = os.path.join(repo, ".git", "config")
        with open(local, "a") as f:
            f.write(SEED)
        paths = [local, os.path.join(home, ".gitconfig"), os.path.join(repo, "x"), env["GIT_CONFIG_SYSTEM"]]
        before = [snapshot(x) for x in paths]
        try:
            subprocess.run(["git", "config", *form], cwd=repo, env=env, capture_output=True, text=True, timeout=20,
                           stdin=subprocess.DEVNULL)
        except subprocess.TimeoutExpired:
            return False
        return [snapshot(x) for x in paths] != before

    def test_unfiled_fence(self):
        # the unfiled run writes only under the temp dir; the real user-level config is never changed
        real = os.path.join(os.path.expanduser("~"), ".gitconfig")
        real_xdg = os.path.join(os.environ.get("XDG_CONFIG_HOME") or os.path.join(os.path.expanduser("~"), ".config"),
                                "git", "config")
        before, before_xdg = snapshot(real), snapshot(real_xdg)
        self.assertEqual(fenced_env(self.tmp)["XDG_CONFIG_HOME"], os.path.join(self.tmp, "xdg"))
        self.assertTrue(self.git_wrote_unfiled(["--global", "alias.z", "q"]))
        self.assertEqual(snapshot(real_xdg), before_xdg)
        self.assertTrue(os.path.isfile(os.path.join(self.tmp, "unfiled-home", ".gitconfig")))
        self.assertEqual(snapshot(real), before)

    def test_git_write_without_file_implies_guard_refusal(self):
        wrote = []
        for form in CORPUS:
            if self.git_wrote_unfiled(form):
                wrote.append(form)
                cmd = "git config " + " ".join(form)
                with self.subTest(form=cmd):
                    p = self.hook(cmd)
                    self.assertEqual(p.returncode, 2, f"git wrote (no --file) but the guard allowed: {cmd}\n{p.stderr}")
        self.assertTrue(wrote, "unfiled oracle never saw git write: the fixture is broken")
        # core.bare forms may be rejected by git as an invalid bool; every other late form must be a proven write
        for f in LATE_FORMS:
            if f[0] != "core.bare":
                self.assertIn(f, wrote, f"git did not write {f}: corpus entry is not a proven write")
            with self.subTest(late=f):
                self.assertEqual(self.hook("git config " + " ".join(f)).returncode, 2)

    def test_cross_product_invariant(self):
        wrote = [f for f in CROSS if self.git_wrote_unfiled(f)]
        self.assertTrue(wrote, "cross product: git never wrote, the fixture is broken")
        for form in wrote:
            cmd = "git config " + " ".join(self.fill(form))
            with self.subTest(form=cmd):
                p = self.hook(cmd)
                self.assertEqual(p.returncode, 2, f"git wrote but the guard allowed: {cmd}\n{p.stderr}")
        print(f"\ncross={len(CROSS)} git_wrote={len(wrote)}")
        for f in wrote:
            print("  cross wrote:", " ".join(f))
        for f in (["--comment", "--global", CROSS_KEY, CROSS_VAL], ["--global", CROSS_KEY, "--global"],
                  ["set", "--global", CROSS_KEY, CROSS_VAL]):
            self.assertIn(f, wrote, f"git did not write {f}")

    def test_system_fence(self):
        # --system writes the temp GIT_CONFIG_SYSTEM file, so the real system config is never touched
        self.assertTrue(self.git_wrote_unfiled(["--system", "alias.z", "q"]))
        self.assertTrue(os.path.isfile(fenced_env(os.path.join(self.tmp, "unfiled"))["GIT_CONFIG_SYSTEM"]))
        self.assertEqual(fenced_env(self.tmp)["GIT_CONFIG_SYSTEM"], os.path.join(self.tmp, "system"))

    def test_corpus_size(self):
        self.assertGreaterEqual(len(CORPUS), 100)

    def test_fence_is_in_place(self):
        env = fenced_env(self.oracle)
        self.assertEqual(env["HOME"], self.oracle)
        self.assertEqual(env["GIT_CEILING_DIRECTORIES"], self.oracle)
        self.assertEqual(env["GIT_CONFIG_NOSYSTEM"], "1")
        self.assertFalse([k for k in env if k.startswith("GIT_CONFIG") and k not in ("GIT_CONFIG_NOSYSTEM", "GIT_CONFIG_SYSTEM")] + ["GIT_DIR"] * ("GIT_DIR" in env))
        self.assertFalse(self.oracle.startswith(os.path.dirname(os.path.dirname(os.path.abspath(__file__)))))

    def test_git_write_implies_guard_refusal(self):
        wrote = []
        for form in CORPUS:
            if self.git_wrote(form):
                wrote.append(form)
                cmd = "git config " + " ".join(form)
                with self.subTest(form=cmd):
                    p = self.hook(cmd)
                    self.assertEqual(p.returncode, 2, f"git wrote but the guard allowed: {cmd}\n{p.stderr}")
        self.assertTrue(wrote, "oracle never saw git write: the fixture is broken")
        print(f"\ncorpus={len(CORPUS)} git_wrote={len(wrote)}")
        for f in wrote:
            print("  wrote:", " ".join(f))

    def test_known_write_forms_refused(self):
        deny_all = ["git config --comment --get alias.x push", "git config --co --get alias.x push",
                    "git config --c --get alias.x push", "git config alias.x push --get",
                    "git config alias.x push -- --get", "git config -- --get alias.x push",
                    "git config --get -- alias.x push"]
        coder = ["git config --co list alias.x push", "git config --c get alias.x push",
                 "git config --comm list alias.x push", "git config --global --co list core.fsmonitor x",
                 "git config --unset-a alias.x", "git config --remove-s alias",
                 "echo alias.x push | xargs git config", "echo x | xargs git config --comment x",
                 "echo alias.x push | xargs git config --comment --get",
                 "echo alias.y push | xargs git config set --value --get",
                 "echo alias.y push | xargs git config set --va --get"]
        for role in ("reviewer", "qa", "pm", "coder"):
            for cmd in deny_all + (coder if role == "coder" else []):
                with self.subTest(role=role, cmd=cmd):
                    self.assertEqual(self.hook(cmd, role).returncode, 2)

    def test_controls_allowed(self):
        for role in ("coder", "reviewer", "qa", "pm"):
            for cmd in ("git config --get alias.x", "git config --get user.name", "git config --list"):
                with self.subTest(role=role, cmd=cmd):
                    p = self.hook(cmd, role)
                    self.assertEqual(p.returncode, 0, p.stderr)
        p = self.hook("echo user.name | xargs git config --get")
        self.assertEqual(p.returncode, 0, p.stderr)


if __name__ == "__main__":
    unittest.main()
