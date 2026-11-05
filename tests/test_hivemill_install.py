"""plugins/hivemill/scripts/install.py: the modes, the refusals, the pinned downloads, the redaction and the RESULT line.
Every machine effect goes through a fake Host over a temporary HOME; nothing here touches the real home, network,
herdr or systemd."""
import ast
import contextlib
import hashlib
import importlib.util
import io
import os
import re
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest import mock

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SCRIPT = os.path.join(ROOT, "plugins", "hivemill", "scripts", "install.py")
FRESHBOX = os.path.join(ROOT, "ci", "freshbox_test.py")
REPO = "https://github.com/volekerb/hivemill.git"
OTHER = "192.0.2.7"  # documentation address standing in for a LAN bind the user chose
AGENTS = "NAME ORG ROLE STATUS\nboss claude boss live\ndirector claude director live\n"
TAGS = "".join("aaaa\trefs/tags/%s\n" % t for t in ("v0.1.0", "v0.10.0", "v0.9.0", "v1.0.0-rc1"))
TOKEN = "ghp_" + "abcdefghijklmnopqrstuvwxyzABCDEFGHIJ"  # ghp_ + 36 letters
RESULT_PASS = re.compile(r"^RESULT PASS url=http://127\.0\.0\.1:[0-9]+/ version=[0-9]+\.[0-9]+\.[0-9]+$")


def load(path, name):
    spec = importlib.util.spec_from_file_location(name, path)
    m = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(m)
    return m


ins = load(SCRIPT, "hivemill_install")


class FakeHost(ins.Host):
    """Records every call. `rules` maps a substring of the joined argv to (rc, output) or a callable(argv) -> (rc, out)."""

    def __init__(self, home, environ=None, euid=1000, tools=("git", "herdr", "gh", "claude"), gh_auth=True, systemd=True,
                 tags=TAGS, origin=REPO, mem=16.0, cpus=8, machine="x86_64", rules=None, assets=None):
        ins.Host.__init__(self, home, environ if environ is not None else {"PATH": os.path.join(home, ".local", "bin") + ":/usr/bin"})
        self._euid, self.tools, self.gh_auth, self.systemd = euid, set(tools), gh_auth, systemd
        self.tags, self.origin, self.mem, self.cpus, self._machine = tags, origin, mem, cpus, machine
        self.rules, self.assets = list(rules or []), dict(assets or {})
        self.calls, self.muts, self.popens, self.http = [], [], [], {}

    def euid(self): return self._euid
    def machine(self): return self._machine
    def mem_gib(self): return self.mem
    def cpu_count(self): return self.cpus
    def sleep(self, s): pass
    def http_status(self, url): return self.http.get(url, 200)

    def mkdirs(self, path): self.muts.append(("mkdirs", path)); ins.Host.mkdirs(self, path)
    def rmtree(self, path): self.muts.append(("rmtree", path)); ins.Host.rmtree(self, path)
    def symlink(self, target, link): self.muts.append(("symlink", link)); ins.Host.symlink(self, target, link)

    def download(self, url, dest):
        self.muts.append(("download", url))
        if url not in self.assets:
            raise ins.StepFail("cannot fetch %s" % url)
        with open(dest, "wb") as f:
            f.write(self.assets[url])

    def install_file(self, src, dest, mode=0o755):
        self.muts.append(("install_file", dest))
        ins.Host.install_file(self, src, dest, mode)
        self.tools.add(os.path.basename(dest))

    def popen(self, argv, logpath):
        self.muts.append(("popen", list(argv)))
        self.popens.append(list(argv))

    def run(self, argv, cwd=None, timeout=None, mutate=False):
        self.calls.append((list(argv), mutate))
        if mutate:
            self.muts.append(("run", list(argv)))
        joined = " ".join(argv)
        for sub, resp in self.rules:
            if sub in joined:
                return resp(argv) if callable(resp) else resp
        a0, rest = os.path.basename(argv[0]), argv[1:]
        if a0 == "python3" and rest == ["--version"]:
            return 0, "Python 3.12.1"
        if a0 in ("git", "herdr", "gh", "claude") and rest[:1] == ["--version"]:
            return (0, "%s 1.0\n" % a0) if a0 in self.tools else (127, "not found")
        if a0 == "gh" and rest[:2] == ["auth", "status"]:
            return (0, "Logged in") if self.gh_auth else (1, "You are not logged in")
        if a0 == "go":
            return 127, "not found"
        if a0 == "systemctl" and rest[:2] == ["--user", "show-environment"]:
            return (0, "") if self.systemd else (1, "Failed to connect to bus")
        if a0 == "git" and "ls-remote" in rest:
            return 0, self.tags
        if a0 == "git" and rest[-3:] == ["remote", "get-url", "origin"]:
            return (0, self.origin + "\n") if self.origin else (128, "fatal: not a git repository")
        if a0 == "git" and rest[:1] == ["clone"]:
            os.makedirs(argv[-1], exist_ok=True)
            return 0, ""
        if a0 == "factory":
            if rest == ["init"]:
                os.makedirs(os.path.join(self.home, "factory", "db"), exist_ok=True)
                open(os.path.join(self.home, "factory", "db", "factory.db"), "w").close()
                return 0, "initialised /x\n"
            if rest == ["--version"]:
                return 0, "hivemill 0.3.0 (git abc123)\n"
            if rest == ["agents"]:
                return 0, AGENTS
            if rest[:2] == ["project", "list"]:
                return 0, "ID NAME\n"
            if rest[:1] == ["config"] and len(rest) == 2:
                return 0, ""
        return 0, ""

    def argvs(self):
        return [" ".join(a) for a, _ in self.calls]


class Base(unittest.TestCase):
    def setUp(self):
        self.td = tempfile.TemporaryDirectory()
        self.addCleanup(self.td.cleanup)
        self.home = os.path.realpath(self.td.name)
        os.makedirs(os.path.join(self.home, ".claude"))
        open(os.path.join(self.home, ".claude", ".credentials.json"), "w").close()
        self.fh = os.path.join(self.home, "factory")

    def host(self, **kw):
        return FakeHost(self.home, **kw)

    def run_main(self, host, *argv):
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            rc = ins.main(list(argv), host=host, out=out)
        return rc, out.getvalue(), err.getvalue()

    def make_install(self, db=True):
        os.makedirs(os.path.join(self.fh, "db" if db else "bin"), exist_ok=True)
        if db:
            open(os.path.join(self.fh, "db", "factory.db"), "w").close()


class TestCheck(Base):
    def test_all_present_exit_0_and_read_only(self):
        h = self.host()
        rc, out, _ = self.run_main(h, "--check")
        self.assertEqual(rc, 0, out)
        names = [ln.split()[1] for ln in out.splitlines() if ln.split()[0] in ("ok", "missing", "warn")]
        self.assertEqual(names, ["python3>=3.11", "git", "gh", "gh-auth", "herdr", "claude", "claude-auth", "systemd-user",
                                 "ram", "cpu", "go"])
        self.assertIn("warn go ", out)
        self.assertEqual(out.splitlines()[-1], "suggested-max-agents 6")
        self.assertEqual(h.muts, [])

    def test_missing_item_exit_3(self):
        rc, out, _ = self.run_main(self.host(tools=("git", "gh")), "--check")
        self.assertEqual(rc, 3)
        self.assertIn("missing herdr ", out)
        self.assertIn("missing claude ", out)

    def test_not_logged_in_is_missing(self):
        rc, out, _ = self.run_main(self.host(gh_auth=False), "--check")
        self.assertEqual(rc, 3)
        self.assertIn("missing gh-auth ", out)

    def test_ram_cpu_go_are_warnings_only(self):
        rc, out, _ = self.run_main(self.host(mem=3.5, cpus=1), "--check")
        self.assertEqual(rc, 0, out)
        self.assertIn("warn ram ", out)
        self.assertIn("warn cpu ", out)
        self.assertIn("suggested-max-agents 3", out)

    def test_suggested_max_agents(self):
        for gib, want in ((1.0, 2), (2.9, 2), (3.5, 3), (5.2, 5), (6.0, 6), (64.0, 6)):
            self.assertEqual(ins.suggested_max_agents(gib), want, gib)

    def test_no_systemd_session_is_missing_unless_no_systemd(self):
        rc, out, _ = self.run_main(self.host(systemd=False), "--check")
        self.assertEqual(rc, 3)
        self.assertIn("missing systemd-user", out)
        rc, out, _ = self.run_main(self.host(systemd=False), "--check", "--no-systemd")
        self.assertEqual(rc, 0, out)
        self.assertIn("warn systemd-user", out)

    def test_old_python_is_missing(self):
        h = self.host(rules=[("python3 --version", (0, "Python 3.9.2"))])
        rc, out, _ = self.run_main(h, "--check")
        self.assertEqual(rc, 3)
        self.assertIn("missing python3>=3.11", out)


class TestModes(Base):
    def test_dry_run_changes_nothing_and_lists_argv(self):
        h = self.host(tools=("git",))
        rc, out, _ = self.run_main(h, "--dry-run", "--project-id", "demo", "--project-repo", "/r", "--max-agents", "3")
        self.assertEqual(rc, 0, out)
        self.assertEqual(h.muts, [])
        self.assertFalse([c for c in h.calls if c[1]])
        self.assertFalse(os.path.exists(self.fh))
        self.assertIn("git clone -- %s %s" % (REPO, self.fh), out)
        self.assertIn("checkout --detach refs/tags/v0.10.0", out)
        self.assertIn("config max_live_agents 3", out)
        self.assertIn("project add demo --name demo --repo /r", out)
        self.assertIn("systemctl --user start herdr-server.service factory-tick.timer factory-serve.service factory-up.service", out)

    def test_serve_bind_before_any_serve_start_in_both_plans(self):
        for extra, start in (((), "systemctl --user start"), (("--no-systemd",), "serve --bind")):
            _, out, _ = self.run_main(self.host(), "--dry-run", *extra)
            lines = out.splitlines()
            bind = [i for i, ln in enumerate(lines) if "config serve_bind 127.0.0.1" in ln]
            serve = [i for i, ln in enumerate(lines) if start in ln]
            self.assertTrue(bind and serve, (extra, out))
            self.assertLess(bind[0], serve[0], extra)

    def test_no_mode_is_usage(self):
        rc, out, err = self.run_main(self.host())
        self.assertEqual(rc, 2)
        self.assertIn("--check", err)

    def test_two_modes_is_usage(self):
        self.assertEqual(self.run_main(self.host(), "--check", "--yes")[0], 2)

    def test_real_entry_point_usage_and_no_token_option(self):
        for argv in ([], ["--gh-token", "x"], ["--token", "x", "--yes"], ["--project-id", "a", "--check"]):
            p = subprocess.run([sys.executable, SCRIPT] + argv, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                               stderr=subprocess.PIPE, universal_newlines=True, timeout=60)
            self.assertEqual(p.returncode, 2, (argv, p.stderr))

    def test_real_entry_point_never_echoes_a_rejected_token(self):
        p = subprocess.run([sys.executable, SCRIPT, "--dry-run", "--gh-token", TOKEN], stdin=subprocess.DEVNULL,
                           stdout=subprocess.PIPE, stderr=subprocess.PIPE, universal_newlines=True, timeout=60)
        self.assertEqual(p.returncode, 2)
        self.assertNotIn(TOKEN, p.stdout + p.stderr)

    def test_real_entry_point_never_echoes_a_bad_option_value(self):
        for opt in ("--port", "--max-agents"):
            p = subprocess.run([sys.executable, SCRIPT, "--dry-run", opt, TOKEN], stdin=subprocess.DEVNULL,
                               stdout=subprocess.PIPE, stderr=subprocess.PIPE, universal_newlines=True, timeout=60)
            self.assertEqual(p.returncode, 2, opt)
            self.assertNotIn(TOKEN, p.stdout + p.stderr, opt)

    def test_real_entry_point_refuses_inside_an_agent_pane(self):
        env = dict(os.environ, FACTORY_AGENT="x", HOME=self.td_home())
        p = subprocess.run([sys.executable, SCRIPT, "--dry-run"], stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                           stderr=subprocess.PIPE, universal_newlines=True, timeout=60, env=env)
        self.assertEqual(p.returncode, 1, p.stdout + p.stderr)
        self.assertIn("FACTORY_AGENT", p.stdout)

    def td_home(self):
        d = tempfile.TemporaryDirectory()
        self.addCleanup(d.cleanup)
        return d.name

    def test_port_range_and_dash_ref_are_usage(self):
        self.assertEqual(self.run_main(self.host(), "--dry-run", "--port", "70000")[0], 2)
        self.assertEqual(self.run_main(self.host(), "--dry-run", "--ref", "--upload-pack=x")[0], 2)


class TestRefusals(Base):
    def test_root_refused_before_any_host_call(self):
        h = self.host(euid=0)
        rc, out, _ = self.run_main(h, "--yes")
        self.assertEqual(rc, 1)
        self.assertIn("root", out)
        self.assertEqual(h.calls, [])
        self.assertEqual(h.muts, [])

    def test_factory_agent_refused(self):
        h = self.host(environ={"FACTORY_AGENT": "x", "PATH": "/usr/bin"})
        rc, out, _ = self.run_main(h, "--yes")
        self.assertEqual(rc, 1)
        self.assertIn("FACTORY_AGENT", out)
        self.assertEqual(h.calls, [])

    def test_plain_user_passes_preflight(self):
        rc, out, _ = self.run_main(self.host(), "--dry-run")
        self.assertEqual(rc, 0, out)

    def test_existing_database_without_resume_exit_4_untouched(self):
        self.make_install()
        h = self.host()
        rc, out, _ = self.run_main(h, "--yes")
        self.assertEqual(rc, 4, out)
        self.assertEqual(h.muts, [])
        joined = " ".join(h.argvs())
        for word in ("clone", "checkout", " config ", "systemctl", "init"):
            self.assertNotIn(word, joined)

    def test_partial_clone_without_resume_exit_4(self):
        self.make_install(db=False)
        h = self.host()
        self.assertEqual(self.run_main(h, "--yes")[0], 4)
        self.assertEqual(h.muts, [])

    def test_other_origin_exit_1_untouched(self):
        self.make_install()
        h = self.host(origin="https://github.com/someone/else.git")
        rc, out, _ = self.run_main(h, "--yes", "--resume")
        self.assertEqual(rc, 1, out)
        self.assertEqual(h.muts, [])
        self.assertNotIn("config", " ".join(h.argvs()))

    def test_not_a_git_dir_exit_1(self):
        self.make_install()
        h = self.host(origin="")
        self.assertEqual(self.run_main(h, "--yes")[0], 1)
        self.assertEqual(h.muts, [])

    def test_same_origin_spelling_variants_match(self):
        self.make_install()
        h = self.host(origin="https://github.com/VOLEKERB/hivemill")
        self.assertEqual(self.run_main(h, "--yes")[0], 4)

    def test_resume_records_no_checkout_and_keeps_config(self):
        self.make_install()
        h = self.host(rules=[("config serve_bind", (0, OTHER + "\n")), ("config serve_port", (0, "9100\n"))])
        rc, out, _ = self.run_main(h, "--yes", "--resume", "--no-systemd")
        self.assertEqual(rc, 0, out)
        joined = h.argvs()
        self.assertFalse([a for a in joined if "checkout" in a or "clone" in a or "fetch" in a or "ls-remote" in a], joined)
        self.assertFalse([a for a in joined if re.search(r"factory config \w+ \S+", a) and "config serve_" not in a])
        self.assertFalse([a for a in joined if "config serve_bind 127.0.0.1" in a], "an existing bind must be kept")
        self.assertFalse([a for a in joined if a.endswith(" init")])
        self.assertEqual(out.splitlines()[-1], "RESULT PASS url=http://127.0.0.1:9100/ version=0.3.0")

    def test_resume_sets_only_passed_options(self):
        self.make_install()
        h = self.host(rules=[("config serve_bind", (0, OTHER + "\n"))])
        self.run_main(h, "--yes", "--resume", "--no-systemd", "--max-agents", "4")
        sets = [a for a in h.argvs() if re.search(r"config (max_live_agents|serve_\w+|scout_enabled) \S+$", a)]
        self.assertEqual(len(sets), 1, sets)
        self.assertTrue(sets[0].endswith("config max_live_agents 4"))


class TestPrereqsAndAuth(Base):
    def test_missing_git_exit_3_prints_hint_and_changes_nothing(self):
        h = self.host(tools=())
        rc, out, _ = self.run_main(h, "--yes")
        self.assertEqual(rc, 3, out)
        self.assertIn("sudo apt-get install -y git python3", out)
        self.assertEqual([m for m in h.muts if m[0] != "rmtree"], [])

    def test_old_python_exit_3(self):
        h = self.host(rules=[("python3 --version", (0, "Python 3.9.2"))])
        rc, out, _ = self.run_main(h, "--yes")
        self.assertEqual(rc, 3)
        self.assertIn("python3 3.11 or newer", out)

    def test_gh_not_logged_in_exit_3_before_clone(self):
        h = self.host(gh_auth=False)
        rc, out, _ = self.run_main(h, "--yes")
        self.assertEqual(rc, 3, out)
        self.assertIn("Run this in your own terminal: gh auth login", out)
        self.assertFalse([a for a in h.argvs() if "clone" in a])
        self.assertFalse(os.path.exists(self.fh))

    def test_claude_not_logged_in_exit_3(self):
        os.remove(os.path.join(self.home, ".claude", ".credentials.json"))
        rc, out, _ = self.run_main(self.host(), "--yes")
        self.assertEqual(rc, 3)
        self.assertIn("claude", out)

    def test_no_systemd_session_exit_3_explains_both_options(self):
        h = self.host(systemd=False)
        rc, out, _ = self.run_main(h, "--yes")
        self.assertEqual(rc, 3)
        self.assertIn("--no-systemd", out)
        self.assertFalse(os.path.exists(self.fh))


class TestLatestTag(Base):
    def test_highest_vxyz_wins(self):
        self.assertEqual(ins.resolve_latest(TAGS), "v0.10.0")
        peeled = "aa\trefs/tags/v0.2.0\nbb\trefs/tags/v0.2.0^{}\ncc\trefs/tags/v0.11.1\ndd\trefs/tags/v0.11.1^{}\n"
        self.assertEqual(ins.resolve_latest(peeled), "v0.11.1")

    def test_no_matching_tag_is_none(self):
        self.assertIsNone(ins.resolve_latest("aa\trefs/tags/v1.0.0-rc1\nbb\trefs/tags/release-3\n"))
        self.assertIsNone(ins.resolve_latest(""))

    def test_no_matching_tag_exit_1(self):
        h = self.host(tags="aa\trefs/tags/v1.0.0-rc1\n")
        rc, out, _ = self.run_main(h, "--yes", "--no-systemd")
        self.assertEqual(rc, 1, out)
        self.assertIn("no release tag", out)
        self.assertFalse(os.path.exists(self.fh))

    def test_checkout_uses_the_resolved_tag(self):
        h = self.host()
        self.run_main(h, "--yes", "--no-systemd")
        self.assertIn("git -C %s checkout --detach refs/tags/v0.10.0" % self.fh, h.argvs())

    def test_explicit_ref_is_fetched_then_detached(self):
        h = self.host()
        rc, out, _ = self.run_main(h, "--yes", "--no-systemd", "--ref", "main")
        self.assertEqual(rc, 0, out)
        argvs = h.argvs()
        self.assertFalse([a for a in argvs if "ls-remote" in a])
        self.assertIn("git -C %s fetch origin -- main" % self.fh, argvs)
        self.assertIn("git -C %s checkout --detach FETCH_HEAD" % self.fh, argvs)


class TestFullRun(Base):
    def test_systemd_run_result_line_and_order(self):
        h = self.host()
        rc, out, _ = self.run_main(h, "--yes", "--port", "9000", "--max-agents", "3", "--no-scout",
                                   "--project-id", "demo", "--project-repo", "/r", "--project-gh", "o/r")
        self.assertEqual(rc, 0, out)
        self.assertRegex(out.splitlines()[-1], RESULT_PASS)
        self.assertEqual(out.splitlines()[-1], "RESULT PASS url=http://127.0.0.1:9000/ version=0.3.0")
        a = h.argvs()
        fac = os.path.join(self.home, ".local", "bin", "factory")
        order = [a.index(x) for x in (
            "git clone -- %s %s" % (REPO, self.fh), "%s init" % fac, "%s config serve_bind 127.0.0.1" % fac,
            "%s config serve_port 9000" % fac, "%s config max_live_agents 3" % fac, "%s config scout_enabled 0" % fac,
            "%s project add demo --name demo --repo /r --gh o/r" % fac,
            "bash %s/systemd/install.sh --with-serve" % self.fh,
            "systemctl --user start herdr-server.service factory-tick.timer factory-serve.service factory-up.service")]
        self.assertEqual(order, sorted(order))
        self.assertEqual(os.readlink(fac), os.path.join(self.fh, "bin", "factory"))
        self.assertFalse([x for x in a if "loginctl" in x], "linger only with --linger")
        self.assertEqual(h.popens, [])
        self.assertFalse([x for x in a if "sudo" in x.split()])

    def test_no_systemd_run_starts_background_and_warns(self):
        h = self.host()
        rc, out, _ = self.run_main(h, "--yes", "--no-systemd")
        self.assertEqual(rc, 0, out)
        self.assertRegex(out.splitlines()[-1], RESULT_PASS)
        self.assertEqual(h.popens, [["herdr", "server"], [os.path.join(self.home, ".local", "bin", "factory"), "serve", "--bind", "127.0.0.1"]])
        self.assertIn("WARNING: no tick timer: agents will not be nudged and queued spawns will not start", out)
        a = h.argvs()
        self.assertLess(a.index("herdr workspace list"), a.index("%s up" % os.path.join(self.home, ".local", "bin", "factory")))
        self.assertFalse([x for x in a if "systemctl" in x and "show-environment" not in x])

    def test_linger_refusal_is_a_warning(self):
        h = self.host(rules=[("loginctl enable-linger", (1, "Access denied"))])
        rc, out, _ = self.run_main(h, "--yes", "--linger")
        self.assertEqual(rc, 0, out)
        self.assertIn("enable-linger was refused", out)
        self.assertRegex(out.splitlines()[-1], RESULT_PASS)

    def test_listed_project_is_skipped(self):
        h = self.host(rules=[("project list", (0, "ID NAME\ndemo Demo\n"))])
        self.make_install(db=False)  # partial clone: --resume
        rc, out, _ = self.run_main(h, "--yes", "--resume", "--project-id", "demo", "--project-repo", "/r")
        self.assertEqual(rc, 0, out)
        self.assertFalse([a for a in h.argvs() if "project add" in a])

    def test_queued_agents_count_as_started(self):
        h = self.host(rules=[(" agents", (0, "NAME ORG ROLE STATUS\nboss claude boss queued\ndirector claude director live\n"))])
        self.assertEqual(self.run_main(h, "--yes", "--no-systemd")[0], 0)

    def test_agents_never_live_fails_smoke(self):
        h = self.host(rules=[(" agents", (0, "NAME ORG ROLE STATUS\n"))])
        rc, out, _ = self.run_main(h, "--yes", "--no-systemd")
        self.assertEqual(rc, 1)
        self.assertEqual(out.splitlines()[-1], "RESULT FAIL step=smoke")

    def test_ui_not_answering_fails_smoke(self):
        h = self.host()
        h.http_status = lambda url: 0
        rc, out, _ = self.run_main(h, "--yes", "--no-systemd")
        self.assertEqual(rc, 1)
        self.assertEqual(out.splitlines()[-1], "RESULT FAIL step=smoke")

    def test_failing_step_result_fail_and_exit_1(self):
        h = self.host(rules=[("git clone", (128, "fatal: repository not found"))])
        rc, out, _ = self.run_main(h, "--yes", "--no-systemd")
        self.assertEqual(rc, 1)
        self.assertRegex(out.splitlines()[-1], r"^RESULT FAIL step=clone$")
        self.assertNotIn("RESULT PASS", out)

    def test_init_without_initialised_line_fails(self):
        h = self.host(rules=[("factory init", (0, "something else"))])
        rc, out, _ = self.run_main(h, "--yes", "--no-systemd")
        self.assertEqual((rc, out.splitlines()[-1]), (1, "RESULT FAIL step=init"))


class TestNoToken(Base):
    def test_token_in_failing_step_output_never_reaches_stdout_or_stderr(self):
        leak = "fatal: unable to access 'https://github.com/o/r.git/' with %s: denied\nGH_TOKEN=%s\n" % (TOKEN, TOKEN)
        h = self.host(environ={"GH_TOKEN": TOKEN, "PATH": "/usr/bin"}, rules=[("git clone", (128, leak))])
        rc, out, err = self.run_main(h, "--yes", "--no-systemd")
        self.assertEqual(rc, 1)
        self.assertNotIn(TOKEN, out)
        self.assertNotIn(TOKEN, err)
        self.assertIn("RESULT FAIL step=clone", out)

    def test_env_token_with_unknown_shape_is_still_masked(self):
        odd = "not-a-known-shape-0123456789"
        h = self.host(environ={"GITHUB_TOKEN": odd, "PATH": "/usr/bin"}, rules=[("git clone", (1, "boom " + odd))])
        _, out, err = self.run_main(h, "--yes", "--no-systemd")
        self.assertNotIn(odd, out + err)

    def test_redact_patterns(self):
        for s in (TOKEN, "github_pat_" + "A1" * 12, "sk-ant-" + "x" * 20, "Authorization: Bearer abc"):
            self.assertIn("[REDACTED]", ins.redact("x %s y" % s))
            self.assertNotIn(s, ins.redact(s))


def fake_assets(name, payload, sha=None, size=None):
    sha = sha or hashlib.sha256(payload).hexdigest()
    return {"x86_64": ("https://example.invalid/" + name, size if size is not None else len(payload), sha)}


class TestDownloadGuard(Base):
    def herdr_case(self, sha=None, size=None):
        payload = b"\x7fELF fake herdr"
        table = fake_assets("herdr", payload, sha, size)
        h = self.host(tools=("git", "gh", "claude"), assets={table["x86_64"][0]: payload})
        with mock.patch.dict(ins.HERDR_ASSETS, table, clear=True):
            rc, out, _ = self.run_main(h, "--yes", "--no-systemd")
        return h, rc, out

    def test_wrong_sha256_aborts_and_installs_nothing(self):
        h, rc, out = self.herdr_case(sha="0" * 64)
        self.assertEqual(rc, 1, out)
        self.assertIn("sha256 mismatch", out)
        self.assertEqual(out.splitlines()[-1], "RESULT FAIL step=tools")
        self.assertFalse(os.path.exists(os.path.join(self.home, ".local", "bin", "herdr")))
        self.assertFalse([m for m in h.muts if m[0] == "install_file"])
        self.assertFalse(os.path.exists(self.fh))

    def test_wrong_size_aborts_and_installs_nothing(self):
        h, rc, out = self.herdr_case(size=3)
        self.assertEqual(rc, 1, out)
        self.assertIn("size mismatch", out)
        self.assertFalse(os.path.exists(os.path.join(self.home, ".local", "bin", "herdr")))

    def test_correct_sha256_installs_executable(self):
        h, rc, out = self.herdr_case()
        self.assertEqual(rc, 0, out)
        dest = os.path.join(self.home, ".local", "bin", "herdr")
        self.assertTrue(os.access(dest, os.X_OK))
        with open(dest, "rb") as f:
            self.assertEqual(f.read(), b"\x7fELF fake herdr")

    def test_gh_tarball_verified_and_member_extracted(self):
        tarpath = os.path.join(self.home, "gh.tar.gz")
        member = "gh_%s_linux_amd64/bin/gh" % ins.GH_VERSION
        src = os.path.join(self.home, "ghbin")
        with open(src, "wb") as f:
            f.write(b"#!/bin/sh\n")
        with tarfile.open(tarpath, "w:gz") as t:
            t.add(src, arcname=member)
        with open(tarpath, "rb") as f:
            payload = f.read()
        table = fake_assets("gh.tar.gz", payload)
        h = self.host(tools=("git", "herdr", "claude"), assets={table["x86_64"][0]: payload})
        with mock.patch.dict(ins.GH_ASSETS, table, clear=True):
            rc, out, _ = self.run_main(h, "--yes", "--no-systemd")
        self.assertEqual(rc, 0, out)
        with open(os.path.join(self.home, ".local", "bin", "gh"), "rb") as f:
            self.assertEqual(f.read(), b"#!/bin/sh\n")
        bad = fake_assets("gh.tar.gz", payload, sha="1" * 64)
        h2 = self.host(tools=("git", "herdr", "claude"), assets={bad["x86_64"][0]: payload})
        os.remove(os.path.join(self.home, ".local", "bin", "gh"))
        import shutil
        shutil.rmtree(self.fh)
        with mock.patch.dict(ins.GH_ASSETS, bad, clear=True):
            rc, out, _ = self.run_main(h2, "--yes", "--no-systemd")
        self.assertEqual(rc, 1)
        self.assertFalse(os.path.exists(os.path.join(self.home, ".local", "bin", "gh")))

    def test_claude_installer_is_a_file_with_logged_sha_never_piped(self):
        payload = b"#!/bin/sh\necho hi\n"
        h = self.host(tools=("git", "herdr", "gh"), assets={ins.CLAUDE_INSTALLER_URL: payload})
        rc, out, _ = self.run_main(h, "--yes", "--no-systemd")
        self.assertEqual(rc, 0, out)
        self.assertIn(hashlib.sha256(payload).hexdigest(), out)
        run = [a for a in h.argvs() if a.startswith("bash ") and "claude-install.sh" in a]
        self.assertEqual(len(run), 1)
        self.assertFalse([a for a in h.argvs() if "|" in a or "curl" in a])

    def test_non_https_download_refused(self):
        with self.assertRaises(ins.StepFail):
            ins.Host(self.home, {}).download("http://example.invalid/x", os.path.join(self.home, "x"))

    def test_unsupported_arch_fails_cleanly(self):
        h = self.host(tools=("git", "gh", "claude"), machine="riscv64")
        rc, out, _ = self.run_main(h, "--yes", "--no-systemd")
        self.assertEqual((rc, out.splitlines()[-1]), (1, "RESULT FAIL step=tools"))


class TestPins(unittest.TestCase):
    def test_herdr_pin_equals_freshbox_and_setup_doc(self):
        fb = load(FRESHBOX, "freshbox_pin")
        self.assertEqual(ins.HERDR_ASSETS, fb.HERDR_ASSETS)
        self.assertEqual(ins.HERDR_VERSION, fb.HERDR_VERSION)
        with open(os.path.join(ROOT, "docs", "SETUP.md")) as f:
            doc = f.read()
        self.assertIn(ins.HERDR_ASSETS["x86_64"][2], doc)
        self.assertIn(ins.HERDR_ASSETS["aarch64"][2], doc)
        self.assertIn("/" + ins.HERDR_VERSION + "/", doc)

    def test_gh_pin_shape(self):
        for url, size, sha in ins.GH_ASSETS.values():
            self.assertTrue(url.startswith("https://github.com/cli/cli/releases/download/v%s/" % ins.GH_VERSION))
            self.assertRegex(sha, r"^[0-9a-f]{64}$")
            self.assertGreater(size, 1000000)


class TestSourceRules(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        with open(SCRIPT) as f:
            cls.src = f.read()

    def test_python38_syntax(self):
        ast.parse(self.src, feature_version=(3, 8))

    def test_stdlib_only_and_self_contained(self):
        allowed = {"argparse", "hashlib", "os", "re", "shutil", "subprocess", "sys", "tarfile", "tempfile", "time", "urllib"}
        mods = set()
        for node in ast.walk(ast.parse(self.src)):
            if isinstance(node, ast.Import):
                mods.update(n.name.split(".")[0] for n in node.names)
            elif isinstance(node, ast.ImportFrom):
                mods.add((node.module or "").split(".")[0])
        self.assertLessEqual(mods, allowed)
        self.assertNotRegex(self.src, r"ci/|bin/factory_cli")

    def test_no_wide_bind_prompts_or_elevation(self):
        self.assertNotIn("0.0.0." + "0", self.src)
        self.assertEqual([ln for ln in self.src.splitlines() if re.search(r"\bsudo\b", ln)],
                         ['APT_HINT = "sudo apt-get install -y git python3"'])
        self.assertNotRegex(self.src, r"\binput\(|getpass|sys\.stdin")
        self.assertNotRegex(self.src, r"--(gh-)?token")


if __name__ == "__main__":
    unittest.main()
