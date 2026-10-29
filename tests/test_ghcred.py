"""Worker GitHub credential (G-010 R-c): scout/ghcred.py helper, gh shim and prepare_dir, through the real entry points.

Everything runs in temp dirs with a fake HOME and fake tokens; the owner's real ~/.config is never read.
"""
import io
import os
import shutil
import stat
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
from harness import REPO  # noqa: E402

sys.path.insert(0, str(REPO))
from scout import ghcred  # noqa: E402

TOKEN = "ghp_" + "Zq7Lm2" * 6
SLUG = "volekerb/hivemill"
HELPER_PATH = REPO / "scout" / "ghcred.py"


class Base(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp(prefix="ghcred-"))
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)
        self.home = self.tmp / "home"
        (self.home / ".config" / "factory").mkdir(parents=True)
        self.token = self.home / ".config" / "factory" / "gh-worker-rw-token"
        self.write_token(TOKEN)
        (self.home / ".gitconfig").write_text(
            "[user]\n\tname = Test Owner\n\temail = owner@example.invalid\n")
        self.root = self.tmp / "ghcred"
        self.gh_real = self.tmp / "real-gh"

    def write_token(self, text, mode=0o600):
        self.token.write_text(text)
        self.token.chmod(mode)

    def prepare(self, **kw):
        return ghcred.prepare_dir("coder-t-1", "coder", str(self.token), kw.pop("slugs", [SLUG]),
                                  str(self.gh_real), home=str(self.home), root=str(self.root), **kw)

    def env(self, d, **extra):
        e = {"HOME": str(self.home), "PATH": os.environ["PATH"]}
        with mock.patch.dict(os.environ, {"PATH": os.environ["PATH"]}):
            e.update(ghcred.pane_env(d, self.token))
        e.update(extra)
        return e

    def fill(self, d, path, **extra):
        inp = f"protocol=https\nhost=github.com\npath={path}\n\n"
        return subprocess.run(["git", "credential", "fill"], input=inp, capture_output=True, text=True,
                              env=self.env(d, **extra), cwd=str(self.tmp))


class HelperTests(Base):
    def test_fill_allowed_repo_returns_token(self):
        d = self.prepare()
        for path in (f"{SLUG}.git", SLUG):
            r = self.fill(d, path)
            self.assertEqual(r.returncode, 0, r.stderr)
            lines = r.stdout.splitlines()
            self.assertIn("username=x-access-token", lines)
            self.assertIn(f"password={TOKEN}", lines)

    def test_fill_other_repo_refused(self):
        d = self.prepare()
        for path in ("other/repo", "other/repo.git", f"{SLUG}-evil", f"x/{SLUG}"):
            r = self.fill(d, path)
            self.assertNotEqual(r.returncode, 0, path)
            self.assertNotIn("password=", r.stdout)
            self.assertNotIn(TOKEN, r.stdout + r.stderr)

    def test_fill_other_host_or_no_path_refused(self):
        d = self.prepare()
        for inp in ("protocol=https\nhost=evil.example\npath=volekerb/hivemill\n\n",
                    "protocol=http\nhost=github.com\npath=volekerb/hivemill\n\n",
                    "protocol=https\nhost=github.com\n\n"):
            r = subprocess.run([sys.executable, "-I", str(HELPER_PATH), "git-credential", SLUG, "get"], input=inp,
                               capture_output=True, text=True, env={"FACTORY_GH_TOKEN_FILE": str(self.token)})
            self.assertNotEqual(r.returncode, 0, inp)
            self.assertNotIn("password=", r.stdout)

    def assert_unavailable(self, d):
        r = self.fill(d, SLUG)
        self.assertNotEqual(r.returncode, 0)
        self.assertNotIn("password=", r.stdout)
        # the helper's own stderr line, via git credential's passthrough
        h = subprocess.run([sys.executable, "-I", str(HELPER_PATH), "git-credential", SLUG, "get"],
                           input=f"protocol=https\nhost=github.com\npath={SLUG}\n\n", capture_output=True, text=True,
                           env={"FACTORY_GH_TOKEN_FILE": str(self.token)})
        self.assertEqual(h.returncode, 1)
        self.assertEqual(h.stdout, "")
        self.assertIn("worker gh token unavailable", h.stderr)
        self.assertEqual(len(h.stderr.strip().splitlines()), 1)
        self.assertNotIn(TOKEN, h.stderr + r.stderr)

    def test_token_missing(self):
        d = self.prepare()
        self.token.unlink()
        self.assert_unavailable(d)

    def test_token_mode_0644(self):
        d = self.prepare()
        self.write_token(TOKEN, 0o644)
        self.assert_unavailable(d)

    def test_token_symlink(self):
        d = self.prepare()
        real = self.tmp / "elsewhere"
        real.write_text(TOKEN)
        real.chmod(0o600)
        self.token.unlink()
        self.token.symlink_to(real)
        self.assert_unavailable(d)

    def test_token_empty(self):
        d = self.prepare()
        self.write_token("  \n")
        self.assert_unavailable(d)

    def test_token_foreign_uid(self):
        self.prepare()
        out = io.StringIO()
        req = io.StringIO(f"protocol=https\nhost=github.com\npath={SLUG}\n\n")
        with mock.patch.dict(os.environ, {"FACTORY_GH_TOKEN_FILE": str(self.token)}, clear=True), \
                mock.patch.object(ghcred.os, "getuid", return_value=os.getuid() + 1), \
                mock.patch("sys.stderr", new=io.StringIO()) as err:
            rc = ghcred.git_credential(SLUG, "get", req, out)
        self.assertEqual(rc, 1)
        self.assertEqual(out.getvalue(), "")
        self.assertIn("worker gh token unavailable", err.getvalue())
        self.assertNotIn(TOKEN, err.getvalue())

    def test_gh_token_env_used(self):
        d = self.prepare()
        self.token.unlink()
        r = self.fill(d, SLUG, GH_TOKEN="from-env-token")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("password=from-env-token", r.stdout.splitlines())

    def test_store_and_erase_are_noops(self):
        for action in ("store", "erase"):
            r = subprocess.run([sys.executable, "-I", str(HELPER_PATH), "git-credential", SLUG, action],
                               input="protocol=https\nhost=github.com\npath=x/y\npassword=zzz\n\n",
                               capture_output=True, text=True, env={})
            self.assertEqual((r.returncode, r.stdout), (0, ""))


class OwnerCredentialTests(Base):
    def test_owner_helper_is_never_consulted(self):
        owner = self.tmp / "owner-helper"
        owner.write_text("#!/bin/sh\necho username=owner\necho password=OWNER\n")
        owner.chmod(0o755)
        with open(self.home / ".gitconfig", "a") as f:
            f.write(f'[credential "https://github.com"]\n\thelper = "!{owner}"\n')
        d = self.prepare()
        env = self.env(d)
        r = subprocess.run(["git", "config", "--get-all", "credential.https://github.com.helper"],
                           capture_output=True, text=True, env=env, cwd=str(self.tmp))
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(len(r.stdout.strip().splitlines()), 1)
        self.assertIn("ghcred.py", r.stdout)
        self.assertNotIn(str(owner), r.stdout)
        self.assertNotIn("OWNER", self.fill(d, SLUG).stdout)
        self.token.unlink()  # fail closed: the owner helper does not take over
        r = self.fill(d, SLUG)
        self.assertNotEqual(r.returncode, 0)
        self.assertNotIn("OWNER", r.stdout)

    def test_identity_and_http_path_in_gitconfig(self):
        d = self.prepare()
        env = self.env(d)
        get = lambda k: subprocess.run(["git", "config", "--get", k], capture_output=True, text=True, env=env,
                                       cwd=str(self.tmp)).stdout.strip()
        self.assertEqual(get("user.name"), "Test Owner")
        self.assertEqual(get("user.email"), "owner@example.invalid")
        self.assertEqual(get("credential.useHttpPath"), "true")


class ShimTests(Base):
    def stub_gh(self):
        self.gh_real.write_text(
            "#!/bin/sh\n"
            f'echo "$GH_TOKEN|$GH_CONFIG_DIR|$GH_PROMPT_DISABLED|$*" > {self.tmp}/gh-call\n'
            f"touch {self.tmp}/gh-ran\n")
        self.gh_real.chmod(0o755)

    def run_shim(self, d, **extra):
        env = self.env(d, **extra)
        env["PATH"] = os.path.join(d, "bin") + ":/usr/bin:/bin"
        return subprocess.run(["gh", "pr", "view", "7"], capture_output=True, text=True, env=env, cwd=str(self.tmp))

    def test_shim_sets_token_and_config_dir(self):
        self.stub_gh()
        d = self.prepare()
        r = self.run_shim(d)
        self.assertEqual(r.returncode, 0, r.stderr)
        tok, cfg, prompt, args = (self.tmp / "gh-call").read_text().strip().split("|")
        self.assertEqual(tok, TOKEN)
        self.assertEqual(cfg, os.path.join(d, "gh"))
        self.assertEqual((prompt, args), ("1", "pr view 7"))

    def test_shim_reads_token_at_call_time(self):
        self.stub_gh()
        d = self.prepare()
        self.write_token("rotated-token")
        self.run_shim(d)
        self.assertTrue((self.tmp / "gh-call").read_text().startswith("rotated-token|"))

    def test_shim_token_missing_exits_4_without_running_gh(self):
        self.stub_gh()
        d = self.prepare()
        self.token.unlink()
        r = self.run_shim(d)
        self.assertEqual(r.returncode, 4)
        self.assertIn("worker gh token unavailable", r.stderr)
        self.assertFalse((self.tmp / "gh-ran").exists())

    def test_shim_uses_fixed_real_gh_not_path(self):
        self.stub_gh()
        d = self.prepare()
        evil = self.tmp / "evil"
        evil.mkdir()
        (evil / "gh").write_text(f"#!/bin/sh\ntouch {self.tmp}/evil-ran\n")
        (evil / "gh").chmod(0o755)
        env = self.env(d)
        env["PATH"] = f"{d}/bin:{evil}:/usr/bin:/bin"
        subprocess.run(["gh", "x"], env=env, capture_output=True, cwd=str(self.tmp))
        self.assertTrue((self.tmp / "gh-ran").exists())
        self.assertFalse((self.tmp / "evil-ran").exists())


class PrepareDirTests(Base):
    def test_no_secret_written_and_modes(self):
        d = self.prepare()
        r = subprocess.run(["grep", "-rF", TOKEN, d], capture_output=True, text=True)
        self.assertEqual((r.returncode, r.stdout), (1, ""))
        self.assertEqual(stat.S_IMODE(os.stat(d).st_mode), 0o700)
        self.assertEqual(stat.S_IMODE(os.stat(os.path.join(d, "bin", "gh")).st_mode), 0o700)
        self.assertTrue(os.path.isdir(os.path.join(d, "gh")))
        self.assertEqual(os.listdir(os.path.join(d, "gh")), [])
        self.assertNotIn(TOKEN, repr(ghcred.pane_env(d, self.token)))

    def test_pane_env_keys(self):
        d = self.prepare()
        e = ghcred.pane_env(d, self.token)
        self.assertEqual(e["GIT_CONFIG_GLOBAL"], f"{d}/gitconfig")
        self.assertEqual(e["GH_CONFIG_DIR"], f"{d}/gh")
        self.assertEqual(e["FACTORY_GH_TOKEN_FILE"], str(self.token))
        self.assertEqual((e["GIT_CONFIG_NOSYSTEM"], e["GIT_TERMINAL_PROMPT"]), ("1", "0"))
        self.assertTrue(e["PATH"].startswith(f"{d}/bin:"))

    def test_prepare_twice_replaces_files(self):
        d = self.prepare()
        self.assertEqual(self.prepare(), d)
        self.assertEqual(self.fill(d, SLUG).returncode, 0)

    def test_symlinked_agent_dir_refused(self):
        self.root.mkdir(parents=True)
        (self.tmp / "target").mkdir()
        (self.root / "coder-t-1").symlink_to(self.tmp / "target")
        with self.assertRaises(OSError):
            self.prepare()
        self.assertEqual(os.listdir(self.tmp / "target"), [])

    def test_token_path_outside_config_factory_refused(self):
        hosts = self.home / ".config" / "gh" / "hosts.yml"
        hosts.parent.mkdir(parents=True)
        hosts.write_text("github.com:\n  oauth_token: OWNER\n")
        for bad in (hosts, self.tmp / "tok", self.home / ".config" / "factory" / "sub" / "t"):
            Path(bad).parent.mkdir(parents=True, exist_ok=True)
            Path(bad).write_text("x")
            with self.assertRaises(ValueError, msg=str(bad)):
                ghcred.prepare_dir("a", "coder", str(bad), [SLUG], "/usr/bin/gh", home=str(self.home),
                                   root=str(self.root))

    def test_token_symlink_into_gh_config_refused(self):
        hosts = self.home / ".config" / "gh" / "hosts.yml"
        hosts.parent.mkdir(parents=True)
        hosts.write_text("x")
        link = self.home / ".config" / "factory" / "link"
        link.symlink_to(hosts)
        with self.assertRaises(ValueError):
            ghcred.prepare_dir("a", "coder", str(link), [SLUG], "/usr/bin/gh", home=str(self.home),
                               root=str(self.root))

    def test_bad_inputs(self):
        for kw in ({"slugs": []}, {"slugs": ["no-slash"]}, {"slugs": ["a/b;rm"]}):
            with self.assertRaises(ValueError):
                self.prepare(**kw)
        with self.assertRaises(ValueError):
            ghcred.prepare_dir("../x", "coder", str(self.token), [SLUG], "/usr/bin/gh", home=str(self.home),
                               root=str(self.root))
        with self.assertRaises(ValueError):
            ghcred.prepare_dir("a", "coder", str(self.token), [SLUG], "gh", home=str(self.home), root=str(self.root))


if __name__ == "__main__":
    unittest.main()
